// Package storefront is the suite's HTTP client for the storefront of a
// Sovereign: the routes that https://marketplace.<fqdn> calls under /api,
// through the gateway, with the bodies the storefront sends
// (core/marketplace/src/lib/api.ts and components/CheckoutStep.svelte).
//
// A customer is signed in by the bearer token that POST /auth/verify returns.
// The client never sends a cookie, so it cannot carry the console session of
// a sovereign-admin. Every request is kept in Calls with the credential it
// carried, so a caller can prove what each request was sent with. The token
// is never printed: an error carries method, path, status and a short body.
package storefront

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to https://marketplace.<fqdn>/api.
type Client struct {
	Base    string // https://marketplace.<fqdn>/api, no trailing slash
	Token   string // the customer's bearer token; empty before Verify
	Refresh string // the customer's refresh token; empty before Verify
	HTTP    *http.Client
	Calls   []Call // every request sent, in order
}

// Call is one request the client sent.
type Call struct {
	Method, Path string
	Status       int    // 0 when no answer came
	Credential   string // "none" or "customer"
}

// New returns a client for the storefront of the Sovereign fqdn.
func New(fqdn string, h *http.Client) *Client {
	if h == nil {
		h = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{Base: "https://marketplace." + strings.TrimSpace(fqdn) + "/api", HTTP: h}
}

// Error is an answer outside 2xx.
type Error struct {
	Method, Path string
	Status       int
	Body         string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s %s: HTTP %d %s", e.Method, e.Path, e.Status, e.Body)
}

// do sends one request with body as JSON and decodes a 2xx answer into out.
// When the token has expired (HTTP 401) the client renews it once with the
// refresh token, as the storefront does, and sends the request again.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = b
	}
	status, b, err := c.once(ctx, method, path, payload, c.Token)
	if err == nil && status == http.StatusUnauthorized && c.Refresh != "" {
		if c.renew(ctx) == nil {
			status, b, err = c.once(ctx, method, path, payload, c.Token)
		}
	}
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return &Error{Method: method, Path: path, Status: status, Body: snippet(b)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("%s %s: decode HTTP %d body: %w (body=%s)", method, path, status, err, snippet(b))
	}
	return nil
}

// renew is POST /auth/refresh with the refresh token and no bearer token.
func (c *Client) renew(ctx context.Context) error {
	payload, err := json.Marshal(map[string]string{"refresh_token": c.Refresh})
	if err != nil {
		return err
	}
	status, b, err := c.once(ctx, http.MethodPost, "/auth/refresh", payload, "")
	if err != nil {
		return err
	}
	var out tokens
	if status != http.StatusOK || json.Unmarshal(b, &out) != nil || out.Token == "" {
		return &Error{Method: http.MethodPost, Path: "/auth/refresh", Status: status, Body: snippet(b)}
	}
	c.Token, c.Refresh = out.Token, out.Refresh
	return nil
}

// once sends one request, with the bearer token when it is not empty, and
// reads the answer.
func (c *Client) once(ctx context.Context, method, path string, payload []byte, token string) (int, []byte, error) {
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	call := Call{Method: method, Path: path, Credential: "none"}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		call.Credential = "customer"
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		c.Calls = append(c.Calls, call)
		return 0, nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()
	call.Status = res.StatusCode
	c.Calls = append(c.Calls, call)
	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return res.StatusCode, nil, fmt.Errorf("%s %s: read body: %w", method, path, err)
	}
	return res.StatusCode, b, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// Plan is one plan of the catalog's plan list.
type Plan struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	ProductSlug string `json:"product_slug"`
	PriceBaisa  int64  `json:"price_baisa"`
}

// Plans is GET /catalog/plans?product=generic, the plan step of the storefront.
func (c *Client) Plans(ctx context.Context) ([]Plan, error) {
	var out []Plan
	return out, c.do(ctx, http.MethodGet, "/catalog/plans?product=generic", nil, &out)
}

// App is one application of the catalog that the storefront lists.
type App struct {
	ID           string   `json:"id"`
	Slug         string   `json:"slug"`
	Kind         string   `json:"kind"`
	System       bool     `json:"system"`
	Deployable   bool     `json:"deployable"`
	Dependencies []string `json:"dependencies"`
}

// Apps is GET /catalog/apps?published=true, the application step of the storefront.
func (c *Client) Apps(ctx context.Context) ([]App, error) {
	var out []App
	return out, c.do(ctx, http.MethodGet, "/catalog/apps?published=true", nil, &out)
}

// MagicLink is POST /auth/magic-link: the sign-in service mails a code to email.
func (c *Client) MagicLink(ctx context.Context, email string) error {
	return c.do(ctx, http.MethodPost, "/auth/magic-link", map[string]string{"email": email}, nil)
}

// User is the signed-in user as the sign-in service answers it.
type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

// tokens is the answer of POST /auth/verify and POST /auth/refresh.
type tokens struct {
	Token   string `json:"token"`
	Refresh string `json:"refresh_token"`
	User    User   `json:"user"`
}

// Verify is POST /auth/verify with the mailed code. On success the client
// keeps the two tokens and sends the bearer token with every later request.
func (c *Client) Verify(ctx context.Context, email, code string) (User, error) {
	var out tokens
	if err := c.do(ctx, http.MethodPost, "/auth/verify", map[string]string{"email": email, "code": code}, &out); err != nil {
		return User{}, err
	}
	if out.Token == "" {
		return User{}, fmt.Errorf("POST /auth/verify answered no token")
	}
	c.Token, c.Refresh = out.Token, out.Refresh
	return out.User, nil
}

// Me is GET /auth/me with the customer's token.
func (c *Client) Me(ctx context.Context) (User, error) {
	var out struct {
		User User `json:"user"`
	}
	return out.User, c.do(ctx, http.MethodGet, "/auth/me", nil, &out)
}

// VoucherPreview is the answer of POST /billing/vouchers/redeem-preview.
type VoucherPreview struct {
	Code      string `json:"code"`
	CreditOMR int    `json:"credit_omr"`
}

// RedeemPreview is POST /billing/vouchers/redeem-preview: the voucher landing.
func (c *Client) RedeemPreview(ctx context.Context, code string) (VoucherPreview, error) {
	var out VoucherPreview
	return out, c.do(ctx, http.MethodPost, "/billing/vouchers/redeem-preview", map[string]string{"code": code}, &out)
}

// OrgRequest is the body of POST /tenant/orgs as the checkout step sends it.
type OrgRequest struct {
	Slug         string                    `json:"slug"`
	Name         string                    `json:"name"`
	PlanID       string                    `json:"plan_id"`
	Apps         []string                  `json:"apps"`
	Addons       []string                  `json:"addons"`
	PackageSKU   string                    `json:"package_sku,omitempty"`
	ParentDomain string                    `json:"parent_domain"`
	AppConfigs   map[string]map[string]any `json:"app_configs"`
	DeferLaunch  bool                      `json:"defer_launch"`
}

// Org is the Organization the tenant service answers.
type Org struct {
	ID           string `json:"id"`
	Slug         string `json:"slug"`
	Status       string `json:"status"`
	ConsoleHost  string `json:"console_host"`
	ParentDomain string `json:"parent_domain"`
}

// CreateOrg is POST /tenant/orgs.
func (c *Client) CreateOrg(ctx context.Context, req OrgRequest) (Org, error) {
	var out Org
	return out, c.do(ctx, http.MethodPost, "/tenant/orgs", req, &out)
}

// DeleteOrg is DELETE /tenant/orgs/{id}: the owner deletes its Organization.
func (c *Client) DeleteOrg(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/tenant/orgs/"+url.PathEscape(id), nil, nil)
}

// CheckoutRequest is the body of POST /billing/checkout as the checkout step sends it.
type CheckoutRequest struct {
	PlanID      string   `json:"plan_id"`
	Apps        []string `json:"apps"`
	Addons      []string `json:"addons"`
	PackageSKU  string   `json:"package_sku,omitempty"`
	Topology    string   `json:"topology"`
	OverageMode string   `json:"overage_mode"`
	TenantID    string   `json:"tenant_id"`
	PromoCode   string   `json:"promo_code,omitempty"`
}

// CheckoutResult is the part of the checkout answer the suite reads.
type CheckoutResult struct {
	OrderID      string `json:"order_id"`
	PaidByCredit bool   `json:"paid_by_credit"`
	SessionURL   string `json:"session_url"`
	AmountBaisa  int64  `json:"amount_baisa"`
	PriceSource  string `json:"price_source"`
	PackageSKU   string `json:"package_sku"`
}

// Checkout is POST /billing/checkout.
func (c *Client) Checkout(ctx context.Context, req CheckoutRequest) (CheckoutResult, error) {
	var out CheckoutResult
	return out, c.do(ctx, http.MethodPost, "/billing/checkout", req, &out)
}

// ProvisionRequest is the body of POST /provisioning/start, which the
// checkout step sends after a checkout paid from credit.
type ProvisionRequest struct {
	TenantID  string   `json:"tenant_id"`
	OrderID   string   `json:"order_id"`
	PlanID    string   `json:"plan_id"`
	Apps      []string `json:"apps"`
	Subdomain string   `json:"subdomain"`
}

// StartProvisioning is POST /provisioning/start.
func (c *Client) StartProvisioning(ctx context.Context, req ProvisionRequest) error {
	return c.do(ctx, http.MethodPost, "/provisioning/start", req, nil)
}
