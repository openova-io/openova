// Package pdm — HTTP client for pool-domain-manager.
//
// This package is the catalyst-api side of the contract introduced by #163.
// PDM owns every Dynadot write in the OpenOva fleet; catalyst-api never calls
// api.dynadot.com directly anymore. The wizard's pre-submit check, the
// reservation taken before `tofu apply`, the commit after the LB IP is known,
// and the release on `tofu destroy` all flow through this client.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 the base URL is read from the
// POOL_DOMAIN_MANAGER_URL env var — defaulting to the in-cluster service
// FQDN so a stock catalyst-api deployment "just works" against the PDM
// running in openova-system. Tests/dev override the env var.
package pdm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Client is the catalyst-api → PDM HTTP client.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// New constructs a Client. baseURL must NOT have a trailing slash.
func New(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
}

// CheckResult mirrors PDM's response shape — kept loose so the wizard can
// surface PDM's reason/detail strings verbatim without an extra mapping.
type CheckResult struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Detail    string `json:"detail,omitempty"`
	FQDN      string `json:"fqdn,omitempty"`
}

// Check calls GET /api/v1/pool/{domain}/check?sub=X.
func (c *Client) Check(ctx context.Context, poolDomain, subdomain string) (*CheckResult, error) {
	u := fmt.Sprintf("%s/api/v1/pool/%s/check?sub=%s",
		c.BaseURL, url.PathEscape(poolDomain), url.QueryEscape(subdomain))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pdm check: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("pdm /check status %d: %s", resp.StatusCode, truncate(string(body), 256))
	}
	var out CheckResult
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode pdm check: %w (body=%s)", err, truncate(string(body), 256))
	}
	return &out, nil
}

// Reservation is the wire response of POST /reserve.
type Reservation struct {
	PoolDomain       string `json:"poolDomain"`
	Subdomain        string `json:"subdomain"`
	State            string `json:"state"`
	ReservedAt       string `json:"reservedAt"`
	ExpiresAt        string `json:"expiresAt"`
	ReservationToken string `json:"reservationToken"`
	CreatedBy        string `json:"createdBy"`
}

// ErrConflict — PDM returned 409 Conflict (subdomain already taken).
var ErrConflict = errors.New("pool allocation conflict")

// ErrNotFound — PDM returned 404 (no row to commit/release).
var ErrNotFound = errors.New("pool allocation not found")

// Reserve calls POST /api/v1/pool/{domain}/reserve. Returns ErrConflict on
// 409 so callers can distinguish "name taken" from "PDM down".
func (c *Client) Reserve(ctx context.Context, poolDomain, subdomain, createdBy string) (*Reservation, error) {
	body := map[string]string{
		"subdomain": subdomain,
		"createdBy": createdBy,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	u := fmt.Sprintf("%s/api/v1/pool/%s/reserve", c.BaseURL, url.PathEscape(poolDomain))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pdm reserve: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusCreated:
		var out Reservation
		if err := json.Unmarshal(respBody, &out); err != nil {
			return nil, fmt.Errorf("decode reserve: %w (body=%s)", err, truncate(string(respBody), 256))
		}
		return &out, nil
	case http.StatusConflict:
		return nil, ErrConflict
	default:
		return nil, fmt.Errorf("pdm reserve status %d: %s", resp.StatusCode, truncate(string(respBody), 256))
	}
}

// CommitInput maps to PDM's commit body shape.
type CommitInput struct {
	Subdomain        string
	ReservationToken string
	SovereignFQDN    string
	LoadBalancerIP   string
}

// Commit calls POST /api/v1/pool/{domain}/commit.
func (c *Client) Commit(ctx context.Context, poolDomain string, in CommitInput) error {
	body := map[string]string{
		"subdomain":        in.Subdomain,
		"reservationToken": in.ReservationToken,
		"sovereignFQDN":    in.SovereignFQDN,
		"loadBalancerIP":   in.LoadBalancerIP,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/api/v1/pool/%s/commit", c.BaseURL, url.PathEscape(poolDomain))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("pdm commit: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted:
		return nil
	case http.StatusNotFound:
		return ErrNotFound
	default:
		return fmt.Errorf("pdm commit status %d: %s", resp.StatusCode, truncate(string(respBody), 256))
	}
}

// Release calls DELETE /api/v1/pool/{domain}/release.
func (c *Client) Release(ctx context.Context, poolDomain, subdomain string) error {
	body := map[string]string{"subdomain": subdomain}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/api/v1/pool/%s/release", c.BaseURL, url.PathEscape(poolDomain))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("pdm release: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted:
		return nil
	case http.StatusNotFound:
		return ErrNotFound
	default:
		return fmt.Errorf("pdm release status %d: %s", resp.StatusCode, truncate(string(respBody), 256))
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// ── Managed-pool resolution ─────────────────────────────────────────────
//
// catalyst-api needs to know which pool domains PDM owns (so it knows when
// to delegate to PDM vs. fall back to the BYO/DNS path). PDM exposes the
// list at /healthz, but caching that on every wizard keystroke is wasteful.
// Instead — per docs/INVIOLABLE-PRINCIPLES.md #4 — we read the same
// DYNADOT_MANAGED_DOMAINS env var that the K8s ExternalSecret projects into
// the PDM Pod, and that the same secret can project into the catalyst-api
// Pod for this purpose. The env var value is the contract; PDM is the writer.

var managedDomainsState struct {
	once sync.Once
	set  map[string]struct{}
}

// IsManagedDomain reports whether the given domain is in the runtime
// DYNADOT_MANAGED_DOMAINS list. catalyst-api uses this to route /check
// requests: managed → PDM, BYO → DNS lookup.
//
// Resolution order mirrors the legacy dynadot package's so a deployment
// migrating to PDM keeps working without secret edits:
//  1. DYNADOT_MANAGED_DOMAINS env var (canonical)
//  2. DYNADOT_DOMAIN single-value fallback
//  3. Built-in defaults: openova.io, omani.works
func IsManagedDomain(domain string) bool {
	d := strings.ToLower(strings.TrimSpace(domain))
	if d == "" {
		return false
	}
	managedDomainsState.once.Do(func() {
		managedDomainsState.set = computeManagedDomains()
	})
	_, ok := managedDomainsState.set[d]
	return ok
}

// ResetManagedDomains clears the cache so tests can re-evaluate after
// mutating env vars.
func ResetManagedDomains() {
	managedDomainsState.once = sync.Once{}
	managedDomainsState.set = nil
}

// ManagedDomains returns a sorted, deduplicated copy of the configured
// managed-domain list.
func ManagedDomains() []string {
	managedDomainsState.once.Do(func() {
		managedDomainsState.set = computeManagedDomains()
	})
	out := make([]string, 0, len(managedDomainsState.set))
	for d := range managedDomainsState.set {
		out = append(out, d)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

func computeManagedDomains() map[string]struct{} {
	out := make(map[string]struct{})
	if raw := os.Getenv("DYNADOT_MANAGED_DOMAINS"); strings.TrimSpace(raw) != "" {
		out = splitDomainsList(raw)
		if len(out) > 0 {
			return out
		}
	}
	if d := strings.ToLower(strings.TrimSpace(os.Getenv("DYNADOT_DOMAIN"))); d != "" {
		out[d] = struct{}{}
		return out
	}
	out["openova.io"] = struct{}{}
	out["omani.works"] = struct{}{}
	return out
}

func splitDomainsList(raw string) map[string]struct{} {
	raw = strings.ToLower(raw)
	raw = strings.ReplaceAll(raw, ",", " ")
	out := make(map[string]struct{})
	for _, p := range strings.Fields(raw) {
		out[p] = struct{}{}
	}
	return out
}
