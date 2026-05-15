// Package dynadot — Registrar adapter for Dynadot.
//
// This adapter is the NS-flip-only side of Dynadot integration; the DNS
// record writer at core/pool-domain-manager/internal/dynadot/ is a
// separate concern (record management on OpenOva-managed pool domains).
// They are kept distinct because:
//
//   - Adding/removing records targets pool domains we own (openova.io,
//     omani.works, ...).
//   - Flipping nameservers via SetNameservers targets a customer-owned
//     domain that happens to be registered at Dynadot — the customer's
//     token, not our pool credential, authenticates the call.
//
// Dynadot's HTTP/JSON API (api3.json) supports a `set_ns` command that
// replaces the nameserver list for a domain. We use it via the customer-
// supplied (apiKey, apiSecret) pair. Token format: "<apiKey>:<apiSecret>".
//
// Reference: https://www.dynadot.com/domain/api3.html#set_ns
//
// ── Glue records (issue #900) ──────────────────────────────────────────
//
// Dynadot rejects set_ns calls when the supplied NS hostnames are NOT
// registered as "host records" against the customer's Dynadot account:
//
//	{"SetNsResponse":{"ResponseCode":"-1","Status":"error",
//	 "Error":"'ns1.otech103.omani.works' needs to be registered with an
//	  ip address before it can be used."}}
//
// This is Dynadot's "registered nameserver" feature — the customer's
// account must know the IPv4 of every external NS host before it can
// reference them in set_ns. The fix: BEFORE set_ns, register every
// out-of-bailiwick NS (one whose suffix is NOT the customer-owned
// domain) via the `register_ns` command using the customer's token. The
// flow is idempotent: get_ns first → register_ns only when missing or
// the IP differs (set_ns_ip when it differs).
//
// The glue-record path is invoked when SetNameservers receives a
// non-empty `glueIP` argument — the upstream caller (catalyst-api's
// pdmFlipNS, PDM's HTTP /set-ns handler) supplies the Sovereign's
// load-balancer IPv4 from `SOVEREIGN_LB_IP`/the deployment record so
// the registration uses ground-truth state, never a hardcoded value.
// Adapters whose providers do not require pre-registration can keep
// SetNameservers (no-glueIP) — see the GlueRegistrar interface below.
package dynadot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	registrar "github.com/openova-io/openova/core/pool-domain-manager/internal/registrar"
)

// Adapter implements registrar.Registrar by speaking Dynadot api3.json.
//
// NSGlueIP, when non-empty, opts-in to the adapter-level auto-glue path:
// SetNameservers will pre-register every out-of-bailiwick NS hostname
// against the customer's account (via RegisterGlueRecord, idempotent)
// BEFORE issuing set_ns. This is required for the mothership parent-
// domain onboard flow (issue #1500 — fresh Dynadot domains pick set_ns
// rejection on ns3.openova.io until it is registered as a glue record).
//
// The IP is the mothership PowerDNS LoadBalancer public IPv4 — the
// ground-truth A-record value for ns1/ns2/ns3.openova.io. Sourced from
// POOL_DOMAIN_MANAGER_NS_GLUE_IP at PDM startup (cmd/pdm/main.go); plumbed
// through the openova-private deployment.yaml so the value is operator-
// configurable and never hardcoded (Inviolable Principle #4).
//
// When NSGlueIP is empty (no env var set), SetNameservers preserves its
// historical pass-through behaviour — the handler-level glueIP path
// (handler/registrar.go SetNS, BYO Flow B) is still authoritative for
// per-request glue, so customer Sovereign flows are unaffected.
type Adapter struct {
	BaseURL  string
	HTTP     *http.Client
	NSGlueIP string
}

// New returns a Dynadot registrar adapter pointing at the production
// api3.json endpoint. Override BaseURL / NSGlueIP after construction in
// tests and at PDM startup respectively.
func New() *Adapter {
	return &Adapter{
		BaseURL: "https://api.dynadot.com/api3.json",
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Name returns "dynadot".
func (a *Adapter) Name() string { return "dynadot" }

// parseToken splits the customer credential into apiKey and apiSecret.
// Format: "apiKey:apiSecret".
func parseToken(token string) (key, secret string, err error) {
	parts := strings.SplitN(strings.TrimSpace(token), ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("dynadot: token must be 'apiKey:apiSecret': %w", registrar.ErrInvalidToken)
	}
	return parts[0], parts[1], nil
}

// respHeader carries the per-command status fields Dynadot returns. They
// live DIRECTLY under each `<Command>Response` envelope — Dynadot does
// NOT wrap them in a `ResponseHeader` sub-object the way an earlier read
// of the docs assumed. Live api3.json output (`domain_info` against an
// account-owned domain) returned 2026-05-05:
//
//	{"DomainInfoResponse":{"ResponseCode":0,"Status":"success",
//	 "DomainInfo":{...}}}
//
// And the error envelope (`domain_info` for an unowned domain):
//
//	{"DomainInfoResponse":{"ResponseCode":"-1","Status":"error",
//	 "Error":"could not find domain in your account"}}
//
// Note `ResponseCode` is JSON-typed `int` on success but `string` on
// error — the same field flips representation across responses (see
// issue #939). We decode into `json.Number` so both shapes round-trip
// without an Unmarshal failure, then compare via the codeIsZero helper.
//
// The legacy `ResponseHeader` wrapper that this struct replaces was
// fictional — every prior production call to ValidateToken / SetNs /
// GetNs unmarshalled into a zero-value header (Status="", Code="") and
// fell through to "unknown-error" → 500. Tests masked the bug because
// the test fixtures used the same fictional shape. Refs: #939, #170.
type respHeader struct {
	ResponseCode json.Number `json:"ResponseCode"`
	Status       string      `json:"Status"`
	Error        string      `json:"Error"`
}

// codeIsZero reports whether the Dynadot response code indicates success.
// Dynadot mixes int (`0`) and string (`"0"`) representations across
// success/error paths, sometimes within the same command — this helper
// normalises both so callers don't have to.
func codeIsZero(c json.Number) bool {
	s := strings.TrimSpace(c.String())
	return s == "0" || s == ""
}

// classifyHTTP turns an HTTP-level outcome into a typed registrar error.
// Used by every command path so 401/429/5xx map consistently.
func classifyHTTP(statusCode int) error {
	switch {
	case statusCode == http.StatusUnauthorized, statusCode == http.StatusForbidden:
		return registrar.ErrInvalidToken
	case statusCode == http.StatusTooManyRequests:
		return registrar.ErrRateLimited
	case statusCode >= 500:
		return registrar.ErrAPIUnavailable
	}
	return nil
}

// classifyDynadotError inspects an api3.json command response header and
// maps to a typed error. Dynadot's auth-failure messages are not rigidly
// coded; we match the substring patterns the public docs document.
func classifyDynadotError(h respHeader) error {
	if strings.EqualFold(h.Status, "success") || codeIsZero(h.ResponseCode) {
		return nil
	}
	msg := strings.ToLower(h.Error)
	// Order matters: domain-not-in-account must precede the auth match
	// because Dynadot's "could not find domain in your account" message
	// would otherwise hit the auth branch on the substring "account".
	switch {
	case strings.Contains(msg, "not found"),
		strings.Contains(msg, "could not find"),
		strings.Contains(msg, "not in your account"),
		strings.Contains(msg, "in your account"),
		strings.Contains(msg, "not own"),
		strings.Contains(msg, "no such domain"):
		return fmt.Errorf("dynadot api: %s: %w", h.Error, registrar.ErrDomainNotInAccount)
	case strings.Contains(msg, "invalid api"),
		strings.Contains(msg, "invalid key"),
		strings.Contains(msg, "invalid secret"),
		strings.Contains(msg, "invalid token"),
		strings.Contains(msg, "auth"):
		return fmt.Errorf("dynadot api: %s: %w", h.Error, registrar.ErrInvalidToken)
	case strings.Contains(msg, "rate"), strings.Contains(msg, "too many"):
		return fmt.Errorf("dynadot api: %s: %w", h.Error, registrar.ErrRateLimited)
	}
	return fmt.Errorf("dynadot api error: code=%s status=%s err=%s", h.ResponseCode.String(), h.Status, h.Error)
}

// ValidateToken probes the registrar with `domain_info` for the named
// domain. Success means: the credentials authenticate AND the domain is
// in the customer's account.
func (a *Adapter) ValidateToken(ctx context.Context, token, domain string) error {
	key, secret, err := parseToken(token)
	if err != nil {
		return err
	}
	params := url.Values{}
	params.Set("key", key)
	params.Set("secret", secret)
	params.Set("command", "domain_info")
	params.Set("domain", domain)

	body, err := a.do(ctx, params)
	if err != nil {
		return err
	}
	// Dynadot's domain_info live response (verified 2026-05-05) puts
	// ResponseCode/Status/Error DIRECTLY under DomainInfoResponse — there
	// is no `ResponseHeader` wrapper. Refs #939.
	var raw struct {
		DomainInfoResponse struct {
			respHeader
		} `json:"DomainInfoResponse"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("dynadot: parse domain_info: %w", err)
	}
	return classifyDynadotError(raw.DomainInfoResponse.respHeader)
}

// SetNameservers replaces the domain's nameserver list via set_ns.
// Dynadot accepts up to 13 nameservers; the API uses indexed params
// ns0, ns1, ... — same as the DNS records writer.
//
// When the adapter is configured with NSGlueIP (POOL_DOMAIN_MANAGER_NS_GLUE_IP
// at PDM startup) — the mothership parent-domain onboard path — every
// out-of-bailiwick NS hostname is pre-registered as a Dynadot glue
// record BEFORE set_ns. This is non-optional and idempotent: the
// underlying RegisterGlueRecord short-circuits on already-registered
// hosts with the same IP, and updates via set_ns_ip on a different IP.
// Without this, Dynadot rejects set_ns the first time a previously-
// unregistered NS (e.g. ns3.openova.io for a fresh parent domain) is
// referenced — the live blocker that motivated this change (issue
// #1500, 2026-05-15 omani.homes onboard).
//
// When NSGlueIP is empty, this method preserves its prior pass-through
// behaviour — the handler-level glueIP path (BYO Flow B) is the
// authoritative per-request seam and is unchanged.
func (a *Adapter) SetNameservers(ctx context.Context, token, domain string, ns []string) error {
	if len(ns) == 0 {
		return errors.New("dynadot: nameservers list is empty")
	}
	key, secret, err := parseToken(token)
	if err != nil {
		return err
	}

	// Adapter-level glue auto-registration — runs before set_ns when the
	// mothership PowerDNS LB IPv4 is configured on the adapter. Per the
	// issue header above, set_ns will fail with "needs to be registered
	// with an ip address" otherwise. In-bailiwick NS hostnames (children
	// of the domain whose NS we are setting) are skipped: Dynadot
	// resolves those via the child-zone's own DNS and they do NOT need
	// an account-level registered-host entry.
	if a.NSGlueIP != "" {
		for _, host := range ns {
			if isInBailiwickHost(host, domain) {
				continue
			}
			if err := a.RegisterGlueRecord(ctx, token, host, a.NSGlueIP); err != nil {
				return fmt.Errorf("dynadot: pre-register glue %s: %w", host, err)
			}
		}
	}

	params := url.Values{}
	params.Set("key", key)
	params.Set("secret", secret)
	params.Set("command", "set_ns")
	params.Set("domain", domain)
	for i, n := range ns {
		params.Set(fmt.Sprintf("ns%d", i), n)
	}

	body, err := a.do(ctx, params)
	if err != nil {
		return err
	}
	// set_ns live response shape per Dynadot api3.json docs:
	//   {"SetNsResponse":{"ResponseCode":0,"Status":"success"}}
	// Status fields live DIRECTLY under SetNsResponse (no wrapper).
	var raw struct {
		SetNsResponse struct {
			respHeader
		} `json:"SetNsResponse"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("dynadot: parse set_ns: %w", err)
	}
	return classifyDynadotError(raw.SetNsResponse.respHeader)
}

// GetNameservers reads the current nameserver list via domain_info.
func (a *Adapter) GetNameservers(ctx context.Context, token, domain string) ([]string, error) {
	key, secret, err := parseToken(token)
	if err != nil {
		return nil, err
	}
	params := url.Values{}
	params.Set("key", key)
	params.Set("secret", secret)
	params.Set("command", "domain_info")
	params.Set("domain", domain)

	body, err := a.do(ctx, params)
	if err != nil {
		return nil, err
	}
	// domain_info live response shape (verified 2026-05-05 against omani.works):
	//   {"DomainInfoResponse":{
	//      "ResponseCode": 0, "Status": "success",
	//      "DomainInfo": {
	//        "NameServerSettings": {"NameServers": [{"ServerName": "..."}]}
	//      }
	//   }}
	// Status fields live directly under DomainInfoResponse (no
	// `ResponseHeader` wrapper — see issue #939).
	var raw struct {
		DomainInfoResponse struct {
			respHeader
			DomainInfo struct {
				NameServerSettings struct {
					NameServers []struct {
						ServerName string `json:"ServerName"`
					} `json:"NameServers"`
				} `json:"NameServerSettings"`
			} `json:"DomainInfo"`
		} `json:"DomainInfoResponse"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("dynadot: parse domain_info: %w", err)
	}
	if err := classifyDynadotError(raw.DomainInfoResponse.respHeader); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(raw.DomainInfoResponse.DomainInfo.NameServerSettings.NameServers))
	for _, ns := range raw.DomainInfoResponse.DomainInfo.NameServerSettings.NameServers {
		if ns.ServerName != "" {
			out = append(out, ns.ServerName)
		}
	}
	return out, nil
}

// do is the shared HTTP transport: encode params, GET BaseURL, classify
// HTTP-level errors, return the raw body for command-specific decode.
func (a *Adapter) do(ctx context.Context, params url.Values) ([]byte, error) {
	endpoint := a.BaseURL
	if strings.Contains(endpoint, "?") {
		endpoint += "&" + params.Encode()
	} else {
		endpoint += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("dynadot: build request: %w", err)
	}
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dynadot: %s: %w", err.Error(), registrar.ErrAPIUnavailable)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if e := classifyHTTP(resp.StatusCode); e != nil {
		return nil, fmt.Errorf("dynadot api status %d: %w", resp.StatusCode, e)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dynadot api unexpected status %d", resp.StatusCode)
	}
	return body, nil
}

// GetGlueRecord reads the currently-registered IPv4 for the given host
// via Dynadot's `get_ns` command. Returns ("", nil) when Dynadot reports
// the host is not registered (the canonical "needs to be registered"
// signal); a non-nil error for any other API/HTTP failure.
//
// The customer's (key, secret) pair authenticates the call — Dynadot's
// `register_ns`/`get_ns`/`set_ns_ip` commands are account-scoped, so the
// host appears registered on the customer's account once we've called
// register_ns with their token. This is what unblocks the subsequent
// set_ns: the customer's account knows the NS hostname's IP.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 the host string is intentionally
// not embedded in any error message that propagates outside this package
// without first going through classifyDynadotError so token-bearing
// Dynadot error strings can't echo into logs.
func (a *Adapter) GetGlueRecord(ctx context.Context, token, host string) (string, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return "", fmt.Errorf("dynadot: host is required")
	}
	key, secret, err := parseToken(token)
	if err != nil {
		return "", err
	}
	params := url.Values{}
	params.Set("key", key)
	params.Set("secret", secret)
	params.Set("command", "get_ns")
	params.Set("ns", host)

	body, err := a.do(ctx, params)
	if err != nil {
		return "", err
	}
	// Dynadot's get_ns response shape (Status fields live DIRECTLY under
	// GetNsResponse — there is NO `ResponseHeader` wrapper, see #939):
	//   {"GetNsResponse":{"ResponseCode":0,"Status":"success",
	//    "NsInfo":{"NameServer":{"Host":"...","IP":["..."]}}}}
	// On a host that is NOT registered, Status="error" with a
	// "not found"-style message — we surface that as ("", nil) so
	// callers can distinguish "missing" from "API failed".
	var raw struct {
		GetNsResponse struct {
			respHeader
			NsInfo struct {
				NameServer struct {
					Host string   `json:"Host"`
					IP   []string `json:"IP"`
				} `json:"NameServer"`
			} `json:"NsInfo"`
		} `json:"GetNsResponse"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", fmt.Errorf("dynadot: parse get_ns: %w", err)
	}
	hdr := raw.GetNsResponse.respHeader
	if !strings.EqualFold(hdr.Status, "success") && !codeIsZero(hdr.ResponseCode) {
		// "not registered" / "not found" → ("", nil). Anything else is a
		// real error (auth, rate-limit, API down, ...).
		msg := strings.ToLower(hdr.Error)
		if strings.Contains(msg, "not found") ||
			strings.Contains(msg, "not register") ||
			strings.Contains(msg, "no such") ||
			strings.Contains(msg, "does not exist") {
			return "", nil
		}
		return "", classifyDynadotError(hdr)
	}
	for _, ip := range raw.GetNsResponse.NsInfo.NameServer.IP {
		if ip = strings.TrimSpace(ip); ip != "" {
			return ip, nil
		}
	}
	return "", nil
}

// RegisterGlueRecord registers `host` with the supplied `ip` against the
// customer's Dynadot account so subsequent set_ns calls referencing
// `host` succeed. Idempotent — a host already registered with the same
// IP is a no-op; one registered with a different IP is updated in place
// via `set_ns_ip`. The customer's token authenticates every call.
//
// Implementation:
//
//  1. GetGlueRecord(host) — short-circuit on identical IP.
//  2. If host exists with a DIFFERENT IP → set_ns_ip(host, ip).
//  3. If host does not exist → register_ns(host, ip).
//
// Per docs/INVIOLABLE-PRINCIPLES.md #2 the function returns typed
// registrar errors (ErrInvalidToken, ErrRateLimited, ...) so the HTTP
// handler can map them to the same status codes as SetNameservers.
func (a *Adapter) RegisterGlueRecord(ctx context.Context, token, host, ip string) error {
	host = strings.ToLower(strings.TrimSpace(host))
	ip = strings.TrimSpace(ip)
	if host == "" {
		return fmt.Errorf("dynadot: host is required")
	}
	if ip == "" {
		return fmt.Errorf("dynadot: ip is required")
	}

	// 1. Idempotency probe.
	currentIP, err := a.GetGlueRecord(ctx, token, host)
	if err != nil {
		return fmt.Errorf("dynadot: probe glue record: %w", err)
	}

	// 2. Short-circuit when the host is already registered with the
	//    same IP — a re-run of SetNameservers must not cost an extra
	//    write at the registrar's billed-API tier.
	if currentIP == ip {
		return nil
	}

	key, secret, err := parseToken(token)
	if err != nil {
		return err
	}

	// 3. Update-in-place when host is registered but with a stale IP.
	if currentIP != "" {
		params := url.Values{}
		params.Set("key", key)
		params.Set("secret", secret)
		params.Set("command", "set_ns_ip")
		params.Set("ns", host)
		params.Set("ip0", ip)

		body, err := a.do(ctx, params)
		if err != nil {
			return err
		}
		// set_ns_ip live shape: {"SetNsIpResponse":{"ResponseCode":0,
		// "Status":"success"}} — no `ResponseHeader` wrapper (see #939).
		var raw struct {
			SetNsIpResponse struct {
				respHeader
			} `json:"SetNsIpResponse"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return fmt.Errorf("dynadot: parse set_ns_ip: %w", err)
		}
		return classifyDynadotError(raw.SetNsIpResponse.respHeader)
	}

	// 4. First-time registration.
	params := url.Values{}
	params.Set("key", key)
	params.Set("secret", secret)
	params.Set("command", "register_ns")
	params.Set("ns", host)
	params.Set("ip0", ip)

	body, err := a.do(ctx, params)
	if err != nil {
		return err
	}
	// register_ns live shape: {"RegisterNsResponse":{"ResponseCode":0,
	// "Status":"success"}} — no `ResponseHeader` wrapper (see #939).
	var raw struct {
		RegisterNsResponse struct {
			respHeader
		} `json:"RegisterNsResponse"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("dynadot: parse register_ns: %w", err)
	}
	return classifyDynadotError(raw.RegisterNsResponse.respHeader)
}

// isInBailiwickHost reports whether nsHost is a child of zone (the
// domain whose NS we are flipping). Used by SetNameservers to skip
// glue pre-registration for in-bailiwick NS hostnames (RFC 1034
// §4.2.1) — those resolve via the customer's own DNS and never
// require an account-level registered-host entry on Dynadot.
//
// Mirrors handler.isInBailiwick (kept package-local so the adapter
// has zero dependency on the handler layer). Both arguments are
// case-insensitive; a trailing dot on either is tolerated.
func isInBailiwickHost(nsHost, zone string) bool {
	nsHost = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(nsHost), "."))
	zone = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(zone), "."))
	if nsHost == "" || zone == "" {
		return false
	}
	if nsHost == zone {
		return true
	}
	return strings.HasSuffix(nsHost, "."+zone)
}

// compile-time guards.
var _ registrar.Registrar = (*Adapter)(nil)
var _ registrar.GlueRegistrar = (*Adapter)(nil)
