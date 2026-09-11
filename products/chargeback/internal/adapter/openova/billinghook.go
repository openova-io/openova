package openova

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/settle"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// BillingHook is the ADR-0014 D6 seam: after a statement is ISSUED for a
// customer that is a synced Organization with billing_mode=real, the total
// is posted to the billing service's existing metering endpoint
// (core/services/billing/handlers/metering.go) as a credit debit.
// Idempotent by construction: metadata.request_id — billing's external_ref —
// is the statement id, so re-posting the same statement records nothing
// twice (billing answers duplicate=true).
//
// Off when BILLING_HOOK_URL is unset (the handler is simply not wired).
// Chargeback never touches money beyond this call; billing never learns
// about cloud providers.
type BillingHook struct {
	URL     string // base URL of the billing service (no trailing slash)
	Token   string // BILLING_HOOK_TOKEN — a superadmin bearer token
	Client  *http.Client
	Metrics *metrics.Registry
	// CallbackSecret — BILLING_HOOK_CALLBACK_SECRET — is the shared secret
	// the billing service signs its payment callbacks with (DESIGN.md
	// §9.2): HMAC-SHA256 over the raw body, `sha256=<hex>` in
	// X-Gateway-Signature. Unset ⇒ callbacks are refused, never trusted.
	CallbackSecret string
}

// CallbackSignatureHeader carries the callback's HMAC.
const CallbackSignatureHeader = "X-Gateway-Signature"

// CallbackMaxBytes bounds a callback body.
const CallbackMaxBytes = 1 << 20

// callbackBody is what the billing service posts to
// POST /api/v1/gateways/stripe/callback once its gateway settled money:
//
//	{ "customer_id" | "customer_slug": ..., "statement_id": "...", "intent_id": "...",
//	  "amount": 1200.000000, "paid_at": "2026-09-19", "reference": "pi_3Q...", "status": "settled" }
//
// statement_id names the invoice it settles; without one it is a checkout
// (a top-up) and lands as credit on the account.
type callbackBody struct {
	CustomerID   string        `json:"customer_id"`
	CustomerSlug string        `json:"customer_slug"`
	StatementID  string        `json:"statement_id"`
	IntentID     string        `json:"intent_id"`
	Amount       store.Decimal `json:"amount"`
	PaidAt       string        `json:"paid_at"`
	Reference    string        `json:"reference"`
	Status       string        `json:"status"`
}

// SignCallback returns the header value for a body — exported so the
// billing service's sender and this product's tests compute it alike.
func SignCallback(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// VerifyCallback implements settle.Gateway's inbound half: the signature is
// checked over the RAW bytes, in constant time, before the body is decoded,
// and no secret configured means every callback is refused.
func (b *BillingHook) VerifyCallback(r *http.Request) (settle.Confirmation, error) {
	if b == nil || strings.TrimSpace(b.CallbackSecret) == "" {
		return settle.Confirmation{}, fmt.Errorf("%w: no callback secret is configured (BILLING_HOOK_CALLBACK_SECRET)", settle.ErrCallbackRejected)
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, CallbackMaxBytes))
	if err != nil {
		return settle.Confirmation{}, fmt.Errorf("%w: could not read the body", settle.ErrCallbackRejected)
	}
	sig := strings.TrimSpace(r.Header.Get(CallbackSignatureHeader))
	if sig == "" || !hmac.Equal([]byte(SignCallback(b.CallbackSecret, raw)), []byte(sig)) {
		return settle.Confirmation{}, fmt.Errorf("%w: the signature does not match the body", settle.ErrCallbackRejected)
	}
	var body callbackBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return settle.Confirmation{}, fmt.Errorf("%w: invalid body: %v", store.ErrInvalid, err)
	}
	paidAt := time.Now().UTC()
	if s := strings.TrimSpace(body.PaidAt); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			if t, err = time.Parse(time.RFC3339, s); err != nil {
				return settle.Confirmation{}, fmt.Errorf("%w: paid_at must be YYYY-MM-DD or an RFC3339 timestamp", store.ErrInvalid)
			}
		}
		paidAt = t.UTC()
	}
	return settle.Confirmation{
		CustomerID: strings.TrimSpace(body.CustomerID), CustomerSlug: strings.TrimSpace(body.CustomerSlug),
		StatementID: strings.TrimSpace(body.StatementID), IntentID: strings.TrimSpace(body.IntentID),
		Amount: body.Amount, PaidAt: paidAt, Reference: strings.TrimSpace(body.Reference), Status: strings.TrimSpace(body.Status),
		Method: store.PaymentMethodGateway, GatewayName: store.GatewayStripe, Actor: "gateway:stripe",
	}, nil
}

func (b *BillingHook) client() *http.Client {
	if b.Client != nil {
		return b.Client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (b *BillingHook) metricsReg() *metrics.Registry {
	if b.Metrics != nil {
		return b.Metrics
	}
	return metrics.Default
}

// meteringPayload mirrors events.UsageRecordedPayload
// (core/services/shared/events/nats.go) — the exact shape
// POST /billing/metering/record decodes.
type meteringPayload struct {
	CustomerID     string           `json:"customer_id"`
	AmountMicroOMR int64            `json:"amount_micro_omr"`
	Reason         string           `json:"reason"`
	Metadata       meteringMetadata `json:"metadata"`
}

type meteringMetadata struct {
	RequestID string `json:"request_id"`
	TenantID  string `json:"tenant_id"`
}

// RequestSettlement implements settle.Gateway. This is the STRIPE-backed
// implementation, registered under the gateway name "stripe": the registry
// hands it a statement only when the customer's charging is billed, its
// payment_method is gateway and its gateway_name is stripe. The behaviour is
// unchanged from the pre-seam call — the same metering post, the same
// idempotency on the statement id, the same silent no-op for anything that
// is not an Organization charge.
//
// Replacing Stripe with Omantel's gateway is registering a different
// implementation under a different name; nothing here has to move.
func (b *BillingHook) RequestSettlement(ctx context.Context, req settle.Request) (settle.Result, error) {
	if b == nil || b.URL == "" {
		return settle.Result{Outcome: settle.NotApplicable, Gateway: "billing", Detail: "the billing hook is not configured"}, nil
	}
	if !applies(req.Customer) {
		return settle.Result{Outcome: settle.NotApplicable, Gateway: "billing", Detail: "only a billed Organization collected through the stripe gateway is debited through billing"}, nil
	}
	if req.IsCheckout() {
		// The metering endpoint DEBITS an Organization's credit for an issued
		// statement; a checkout (a top-up) is a CREDIT the billing service
		// takes on its own checkout page, not here. Answering not-applicable
		// leaves the intent awaiting the gateway's own confirmation.
		return settle.Result{Outcome: settle.NotApplicable, Gateway: "billing", Detail: "the billing hook debits issued statements; a checkout is collected on the billing service's own checkout page"}, nil
	}
	if err := b.StatementIssued(ctx, req.Statement, req.Customer); err != nil {
		return settle.Result{Outcome: settle.NotApplicable, Gateway: "billing"}, err
	}
	return settle.Result{Outcome: settle.Settled, Gateway: "billing", Reference: req.Statement.ID,
		Detail: "debited to the Organization's billing credit as usage:chargeback"}, nil
}

// applies is the one condition the billing service can serve.
//
// The commercial half is charging=billed AND payment_method=gateway AND
// gateway_name=stripe — the four-field replacement for the old
// billing_mode=real, and narrower than it was: a billed customer paying by
// TRANSFER also derives billing_mode=real, and must never be debited.
//
// The kind=organization guard is LOAD-BEARING and stays: the payload's
// customer_id is the Organization slug, which is the only identifier the
// platform billing service knows. An external customer has no billing
// account there, so posting for one would be a debit against nothing.
func applies(c store.Customer) bool {
	return c.Kind == "organization" &&
		c.Charging == store.ChargingBilled &&
		c.PaymentMethod == store.PaymentMethodGateway &&
		c.GatewayName == store.GatewayStripe
}

// ConfirmSettlement implements settle.Gateway's second half: the billing
// service relays a confirmation (its own gateway's callback, or a manual
// credit), and this normalises it into the payment to record. It writes
// nothing itself — the store books the payment and owns the lifecycle.
func (b *BillingHook) ConfirmSettlement(_ context.Context, c settle.Confirmation) (settle.Payment, error) {
	return settle.Normalise(c, "billing")
}

// ---------------------------------------------------------------------------
// saved payment methods (DESIGN.md §16)
// ---------------------------------------------------------------------------

// SetupMethod implements settle.Gateway's saved-method half for the
// Stripe-backed billing service. It opens that service's OWN portal session
// for the Organization — `POST /billing/portal/{slug}`, the existing
// surface on which a payer adds, replaces or removes the card Stripe keeps —
// and returns the URL to send them to.
//
// No card detail crosses this process at any point: the payer enters it on
// the gateway's page, and what comes back here is a link.
func (b *BillingHook) SetupMethod(ctx context.Context, req settle.SetupRequest) (settle.SetupResult, error) {
	if b == nil || b.URL == "" {
		return settle.SetupResult{}, fmt.Errorf("%w: the billing hook is not configured", settle.ErrMethodSetupNotSupported)
	}
	if !applies(req.Customer) {
		return settle.SetupResult{}, fmt.Errorf("%w: only a billed Organization collected through the stripe gateway keeps a method with the billing service", settle.ErrMethodSetupNotSupported)
	}
	slug := orgSlugOf(req.Customer)
	url, err := b.portalSession(ctx, slug)
	if err != nil {
		return settle.SetupResult{}, err
	}
	return settle.SetupResult{
		Gateway:  "billing",
		SetupID:  slug,
		SetupURL: url,
		Detail:   "enter the card on the billing portal; it is kept by the payment processor, never here",
	}, nil
}

// ConfirmMethod implements the other half: the payer has returned from the
// portal and this says what is now on file.
//
// The billing service reports no card detail back — its subscription
// document carries none — so the method is recorded as MANAGED BY THE
// GATEWAY: the gateway's name and the Organization handle, and no brand, last four
// or expiry invented for a card nobody in this process has seen. Fabricating
// a display triple here would be a guess printed to a customer as fact. When
// the billing service grows an endpoint that reports them, this function is
// the only place that changes.
func (b *BillingHook) ConfirmMethod(_ context.Context, c settle.MethodConfirmation) (settle.SavedMethod, error) {
	if b == nil || b.URL == "" {
		return settle.SavedMethod{}, fmt.Errorf("%w: the billing hook is not configured", settle.ErrMethodSetupNotSupported)
	}
	if !applies(c.Customer) {
		return settle.SavedMethod{}, fmt.Errorf("%w: only a billed Organization collected through the stripe gateway keeps a method with the billing service", settle.ErrMethodSetupNotSupported)
	}
	return settle.SavedMethod{Gateway: "billing", Label: "card on file in the payment portal"}, nil
}

// portalSession asks the billing service for a portal URL for one Organization.
func (b *BillingHook) portalSession(ctx context.Context, slug string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.URL+"/billing/portal/"+slug, bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if b.Token != "" {
		req.Header.Set("Authorization", "Bearer "+b.Token)
	}
	resp, err := b.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("open the billing portal: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("billing answered %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	url := stringField(raw, "portal_url")
	if url == "" {
		return "", errors.New("billing returned no portal url")
	}
	return url, nil
}

// orgSlugOf is the identifier the billing service knows an Organization by —
// its Organization slug, falling back to the customer slug, exactly as the
// metering post does.
func orgSlugOf(c store.Customer) string {
	if c.OrgSlug != nil && *c.OrgSlug != "" {
		return *c.OrgSlug
	}
	return c.Slug
}

// stringField reads a string field of billing's response, at the top level
// or nested one level (respond-wrapper tolerant), like duplicateFlag.
func stringField(raw []byte, key string) string {
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		return ""
	}
	if v, ok := top[key].(string); ok {
		return v
	}
	for _, v := range top {
		if m, ok := v.(map[string]any); ok {
			if s, ok := m[key].(string); ok {
				return s
			}
		}
	}
	return ""
}

// StatementIssued implements the api.StatementHook seam. Only issued
// statements of an Organization collected through the stripe gateway reach
// billing; everything else is a silent no-op (D6 is adapter-only).
func (b *BillingHook) StatementIssued(ctx context.Context, st store.Statement, c store.Customer) error {
	if b == nil || b.URL == "" {
		return nil
	}
	if !applies(c) {
		return nil
	}
	micro, err := microOMR(st.Total)
	if err != nil {
		return fmt.Errorf("statement %s total %q: %w", st.ID, st.Total, err)
	}
	if micro <= 0 {
		// Billing refuses non-negative usage amounts; a zero statement has
		// nothing to debit.
		return nil
	}
	slug := c.Slug
	if c.OrgSlug != nil && *c.OrgSlug != "" {
		slug = *c.OrgSlug
	}
	period := st.PeriodStart
	if len(period) >= 7 {
		period = period[:7]
	}
	payload := meteringPayload{
		CustomerID:     slug,
		AmountMicroOMR: -micro,
		Reason:         "usage:chargeback:" + period,
		Metadata:       meteringMetadata{RequestID: st.ID, TenantID: slug},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.URL+"/billing/metering/record", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if b.Token != "" {
		req.Header.Set("Authorization", "Bearer "+b.Token)
	}
	resp, err := b.client().Do(req)
	if err != nil {
		b.metricsReg().Inc("chargeback_billing_hook_total", "Billing hook posts by result", map[string]string{"result": "error"}, 1)
		return fmt.Errorf("post metering record: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b.metricsReg().Inc("chargeback_billing_hook_total", "Billing hook posts by result", map[string]string{"result": "error"}, 1)
		return fmt.Errorf("billing answered %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	result := "ok"
	if dup := duplicateFlag(raw); dup {
		result = "duplicate"
	}
	b.metricsReg().Inc("chargeback_billing_hook_total", "Billing hook posts by result", map[string]string{"result": result}, 1)
	slog.Info("billing hook: statement debited", "statement", st.ID, "customer", slug, "period", period, "amount_micro_omr", -micro, "result", result)
	return nil
}

// duplicateFlag reads the `duplicate` field of billing's response, at the
// top level or nested one level (respond-wrapper tolerant).
func duplicateFlag(raw []byte) bool {
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		return false
	}
	if d, ok := top["duplicate"].(bool); ok {
		return d
	}
	for _, v := range top {
		if m, ok := v.(map[string]any); ok {
			if d, ok := m["duplicate"].(bool); ok {
				return d
			}
		}
	}
	return false
}

// microOMR converts an exact decimal OMR amount into micro-OMR without
// passing through floating point (1 OMR = 1,000,000 micro-OMR). Statement
// totals carry at most 6 decimals, so nothing is lost.
func microOMR(d store.Decimal) (int64, error) {
	s := strings.TrimSpace(string(d))
	if s == "" {
		return 0, nil
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intPart, frac, _ := strings.Cut(s, ".")
	if intPart == "" {
		intPart = "0"
	}
	if len(frac) > 6 {
		frac = frac[:6]
	}
	for len(frac) < 6 {
		frac += "0"
	}
	n, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, errors.New("not a decimal amount")
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, errors.New("not a decimal amount")
	}
	v := n*1_000_000 + f
	if neg {
		v = -v
	}
	return v, nil
}
