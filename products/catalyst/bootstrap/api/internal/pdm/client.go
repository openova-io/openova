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

// do executes the HTTP request, decorating it with basic-auth credentials
// when CATALYST_PDM_BASIC_AUTH_USER/PASS are set in the Pod env. The PDM
// public ingress at pool.openova.io is gated by Traefik basicAuth Middleware
// — so every Reserve/Commit/Release/Check call from a Sovereign-side
// catalyst-api MUST carry `Authorization: Basic …` or PDM returns 401.
//
// Read every call so a Secret rotation propagates without a Pod restart
// (Reloader handles the env reload). Per Inviolable Principle #10, this
// is the ONE call site in the pdm package that touches the credentials —
// they never enter a struct that gets logged.
//
// On Catalyst-Zero (contabo) the in-cluster Service path bypasses the
// public ingress entirely and BasicAuth is a no-op when the secret is
// absent — same defensive shape as pdmFlipNS in handler/parent_domains.go.
func (c *Client) do(req *http.Request) (*http.Response, error) {
	user := strings.TrimSpace(os.Getenv("CATALYST_PDM_BASIC_AUTH_USER"))
	pass := os.Getenv("CATALYST_PDM_BASIC_AUTH_PASS")
	if user != "" && pass != "" {
		req.SetBasicAuth(user, pass)
	}
	return c.HTTP.Do(req)
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
	resp, err := c.do(req)
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

// ErrNotFound — PDM returned 404 (no row to commit/release). Most often
// raised on /commit when the reservation TTL elapsed and PDM's sweeper
// goroutine has already deleted the row. CommitWithRetry treats this as
// recoverable by re-Reserving with the same (poolDomain, subdomain,
// createdBy) tuple and re-Committing with the fresh token.
var ErrNotFound = errors.New("pool allocation not found")

// ErrExpired — PDM returned 410 Gone on /commit. The reservation row
// is still in the database but its expires_at has elapsed; the caller
// must Reserve again before committing. CommitWithRetry treats this as
// recoverable identically to ErrNotFound.
var ErrExpired = errors.New("pool allocation expired before commit")

// ErrTokenMismatch — PDM returned 403 Forbidden on /commit. The supplied
// reservationToken does not match the token PDM holds for this row.
// In practice this happens when a sweeper-then-rereserve race produced
// a fresh row under the same name with a different token between the
// initial Reserve and the Commit. Recoverable by re-Reserving (via
// CommitWithRetry).
var ErrTokenMismatch = errors.New("pool allocation token mismatch")

// ErrNotManaged — PDM returned 422 Unprocessable Entity on /release,
// meaning the pool domain the caller asked to release from is NOT one
// OpenOva's pool-domain-manager manages (e.g. a BYO / customer-owned
// domain that a record has misclassified as SovereignDomainMode="pool",
// or a Sovereign FQDN whose parent zone was never enrolled in the pool).
//
// Refs #5193 (item 4): there is no allocation row to release for such a
// domain, so a 422 is the SAME terminal, non-retryable post-condition as
// a 404 (ErrNotFound) — the slot is free because it was never a pool
// slot. ReleaseWithRetry treats it as idempotent success (no retry, no
// wipe-stranding). Distinct sentinel (not folded into ErrNotFound) so a
// caller can still tell "row already gone" (404) from "domain isn't ours
// to manage" (422) when it wants to log the difference.
var ErrNotManaged = errors.New("pool domain is not managed by OpenOva")

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
	resp, err := c.do(req)
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
	// ConsoleLoadBalancerIP — #4053. Optional. When set, the console + api
	// A-records point at the dedicated console LB (the isolated
	// cilium-gateway-console) instead of LoadBalancerIP, so a poisoned
	// shared-gateway CEC (cilium#35728) can never 404 the console. Empty =
	// every record points at LoadBalancerIP (legacy single-LB behaviour).
	ConsoleLoadBalancerIP string
}

// Commit calls POST /api/v1/pool/{domain}/commit.
//
// Status mapping (mirrors core/pool-domain-manager/internal/handler/handler.go
// /commit semantics):
//
//	200/202    — success (202 means row committed but PowerDNS write
//	             pending; the caller can re-Commit idempotently to
//	             republish DNS — CommitWithRetry handles that)
//	404        — ErrNotFound: the reservation row was swept (TTL
//	             elapsed) and no row exists; caller must Reserve again
//	410        — ErrExpired: the row is still present but expires_at
//	             has elapsed before the sweeper ran; same recovery
//	             (Reserve again) as 404
//	403        — ErrTokenMismatch: a parallel /reserve replaced the row
//	             with a different token after the original Reserve;
//	             same recovery (Reserve again) as 404
//	5xx / network errors → wrapped in fmt.Errorf so CommitWithRetry's
//	             backoff loop can detect-and-retry.
func (c *Client) Commit(ctx context.Context, poolDomain string, in CommitInput) error {
	body := map[string]string{
		"subdomain":        in.Subdomain,
		"reservationToken": in.ReservationToken,
		"sovereignFQDN":    in.SovereignFQDN,
		"loadBalancerIP":   in.LoadBalancerIP,
	}
	// #4053 — only send consoleLoadBalancerIP when the console gateway is
	// isolated (a dedicated console LB exists). Omitting the key when empty
	// keeps the wire body byte-identical to the pre-#4053 request, and PDM
	// falls back to loadBalancerIP for the console records.
	if in.ConsoleLoadBalancerIP != "" {
		body["consoleLoadBalancerIP"] = in.ConsoleLoadBalancerIP
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
	resp, err := c.do(req)
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
	case http.StatusGone:
		return ErrExpired
	case http.StatusForbidden:
		return ErrTokenMismatch
	default:
		return fmt.Errorf("pdm commit status %d: %s", resp.StatusCode, truncate(string(respBody), 256))
	}
}

// CommitRetryConfig parameterises CommitWithRetry. Zero values fall
// back to production defaults (5 attempts, 1s → 16s exponential
// backoff). Tests inject tighter bounds so a 404→200 re-Reserve path
// completes in milliseconds rather than seconds.
type CommitRetryConfig struct {
	// MaxAttempts — total attempts including the first. <=0 → 5.
	MaxAttempts int
	// InitialBackoff — delay before the second attempt (doubles each
	// retry, capped at MaxBackoff). <=0 → 1s.
	InitialBackoff time.Duration
	// MaxBackoff — upper bound on the per-step delay. <=0 → 16s.
	MaxBackoff time.Duration
}

// CommitWithRetry runs Commit with two layered recovery primitives:
//
//  1. Re-Reserve on TTL expiry — when Commit returns ErrNotFound /
//     ErrExpired / ErrTokenMismatch, the reservation row is gone or
//     unusable. PDM's design says "Reserve again." This loop calls
//     reserve(ctx) (a caller-supplied closure that wraps Client.Reserve
//     with the deployment's poolDomain/subdomain/createdBy) to obtain
//     a fresh reservation token, then retries Commit with the new
//     token. The caller's onRereserve callback receives the fresh
//     token so it can update the persisted Deployment row (otherwise
//     a Pod restart between re-Reserve and re-Commit would lose the
//     new token).
//
//  2. Exponential backoff on 5xx / network — when Commit returns a
//     non-sentinel error (network failure, PDM 500, etc.) the loop
//     sleeps InitialBackoff * 2^attempt (capped at MaxBackoff) then
//     retries the SAME token. Up to MaxAttempts total attempts.
//
// On final exhaustion the loop returns the last error wrapped with
// "pdm commit retry exhausted (N attempts)" — the caller surfaces
// that string into Deployment.Error so the wizard FailureCard
// renders it.
//
// The 404/410/403 branch counts against MaxAttempts (a malicious /
// flapping PDM cannot loop forever) but does NOT sleep between the
// re-Reserve and the next Commit — the row is gone, not flapping;
// a fresh write is the right next step.
func (c *Client) CommitWithRetry(
	ctx context.Context,
	poolDomain string,
	in CommitInput,
	reserve func(ctx context.Context) (*Reservation, error),
	onRereserve func(newToken string),
	cfg CommitRetryConfig,
) error {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.InitialBackoff <= 0 {
		cfg.InitialBackoff = 1 * time.Second
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 16 * time.Second
	}

	current := in
	var lastErr error
	backoff := cfg.InitialBackoff

	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		err := c.Commit(ctx, poolDomain, current)
		if err == nil {
			return nil
		}
		lastErr = err

		// 404/410/403 — reservation gone or unusable. Re-Reserve and
		// retry with the fresh token. Don't sleep — the row is gone,
		// not flapping; a fresh write is the right next step.
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrExpired) || errors.Is(err, ErrTokenMismatch) {
			if reserve == nil {
				return fmt.Errorf("pdm commit needs re-Reserve but no reserve closure provided: %w", err)
			}
			if attempt == cfg.MaxAttempts {
				break
			}
			fresh, rerr := reserve(ctx)
			if rerr != nil {
				// Re-reserve itself failed — most likely ErrConflict
				// (someone else grabbed the name in the gap, which
				// for catalyst-api is a hard fail because the
				// deployment is already running on Hetzner) or a
				// network/5xx (transient). Either way, propagate so
				// the caller can mark dep.Error.
				return fmt.Errorf("pdm re-reserve after commit %v: %w", err, rerr)
			}
			current.ReservationToken = fresh.ReservationToken
			if onRereserve != nil {
				onRereserve(fresh.ReservationToken)
			}
			continue
		}

		// 5xx / network — back off before retrying with the SAME
		// token. The reservation row is still good (no sentinel), the
		// PDM service is just unhappy.
		if attempt == cfg.MaxAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("pdm commit retry cancelled after %d attempts: %w", attempt, ctx.Err())
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > cfg.MaxBackoff {
			backoff = cfg.MaxBackoff
		}
	}

	return fmt.Errorf("pdm commit retry exhausted (%d attempts): %w", cfg.MaxAttempts, lastErr)
}

// ReleaseWithRetry runs Release with bounded exponential backoff and
// treats pdm.ErrNotFound as success (idempotent — the row may already
// be gone). It is the resilient sibling of CommitWithRetry for the
// decommission path.
//
// Why this exists (Refs #3728): the wipe handler's pool-subdomain
// release was best-effort and NON-retried — a single transient PDM
// failure (5xx, network blip, basic-auth 401 during a Secret rotation,
// or a PDM Pod roll) left the `active` pool_allocations row orphaned.
// Because an `active` row never self-heals (the sweeper only reclaims
// expired *reserved* rows), and the wipe path then deletes catalyst-api's
// own deployment record, the orphaned row permanently 409s every re-fire
// of the same subdomain — forcing operators to burn a fresh name each
// wipe→re-fire cycle (hw155→hw156→hw157…). Retrying the release closes
// that window the same way CommitWithRetry already closes it for /commit.
//
// Semantics:
//
//	nil / ErrNotFound  — success (the slot is free); returns nil.
//	5xx / network      — back off (InitialBackoff * 2^attempt, capped at
//	                     MaxBackoff) and retry, up to MaxAttempts.
//	ctx cancelled      — returns the wrapped context error immediately.
//
// On final exhaustion returns the last error wrapped with
// "pdm release retry exhausted (N attempts)". Callers should pass a
// context that is NOT tied to the inbound HTTP request (use
// context.Background with its own timeout) so a client disconnect during
// a long cloud purge cannot starve the release.
func (c *Client) ReleaseWithRetry(ctx context.Context, poolDomain, subdomain string, cfg CommitRetryConfig) error {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.InitialBackoff <= 0 {
		cfg.InitialBackoff = 1 * time.Second
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 16 * time.Second
	}

	var lastErr error
	backoff := cfg.InitialBackoff
	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		err := c.Release(ctx, poolDomain, subdomain)
		if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrNotManaged) {
			// 404 (ErrNotFound) means the row is already gone; 422
			// (ErrNotManaged, Refs #5193) means the domain was never a
			// pool slot to begin with. Both are the exact post-condition
			// the caller wants — the slot is free — so both are idempotent
			// success. Neither is transient, so return immediately without
			// burning the remaining retry attempts.
			return nil
		}
		lastErr = err
		if attempt == cfg.MaxAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("pdm release retry cancelled after %d attempts: %w", attempt, ctx.Err())
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > cfg.MaxBackoff {
			backoff = cfg.MaxBackoff
		}
	}
	return fmt.Errorf("pdm release retry exhausted (%d attempts): %w", cfg.MaxAttempts, lastErr)
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
	resp, err := c.do(req)
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
	case http.StatusUnprocessableEntity:
		// Refs #5193 (item 4) — 422 "pool domain <x> is not managed by
		// OpenOva". There is no allocation to release for a non-managed
		// domain; surface the typed sentinel so ReleaseWithRetry treats it
		// as idempotent success rather than retrying it 5× and then
		// stranding the wipe's on-disk record cleanup.
		return ErrNotManaged
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
