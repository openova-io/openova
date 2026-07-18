// console_ready.go — #5205 same-origin readiness proxy for the funnel
// completion interstitial (marketplace's LaunchingStep.svelte).
//
// The per-Org console host (console.<slug>.<parentDomain>) is provisioned
// ASYNCHRONOUSLY (DNS A-record, TLS cert, HTTPRoute — see tenant_route.go)
// after the Org CR is created. The interstitial used to poll that host's
// /healthz DIRECTLY from the browser with `fetch(..., {mode:'no-cors'})`.
// Live hw270 funnel walk (#5205, 2026-07-18): a real signup completed with
// backend Org+cert+DNS+WordPress all Ready in ~35s, but the cross-origin
// no-cors probe NEVER observed success — the interstitial hit its 5-min
// timeout every time, stranding every real customer on "Setting up your
// console…" and requiring a manual "Open console anyway" click. A direct
// curl/browser-nav to the SAME host returned 200 within seconds. The
// no-cors opaque Response is the root cause: a cross-origin opaque
// response's status is unreadable, so the browser-side probe was guessing
// blind.
//
// Fix: the marketplace gateway (marketplace.<sov-fqdn>/api/*, PUBLIC,
// SAME-ORIGIN as the /launching page) exposes this readiness proxy. The
// frontend polls it same-origin — a plain JSON {"ready": bool} is fully
// readable, no opaque response involved — and THIS handler does the actual
// cross-host GET /healthz server-side, where no browser CORS/opaque-response
// restriction applies at all.
package handlers

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openova-io/openova/core/services/shared/respond"
)

// consoleReadyProbeTimeout bounds the outbound GET /healthz this handler
// issues. Kept short because the frontend polls on a backoff schedule (as
// low as every 1.5s) — a single slow probe must fail fast rather than pile
// up concurrent outbound requests.
const consoleReadyProbeTimeout = 4 * time.Second

// consoleReadyHTTPClient is dedicated to the outbound per-Org console probe.
// Standard TLS verification is intentional: this hits a public-internet host
// (the same cert chain a real browser nav to /auth/org-handover will
// validate), so skipping verification here would let this endpoint report
// "ready" against a host whose cert the eventual browser nav then rejects.
var consoleReadyHTTPClient = &http.Client{
	Timeout: consoleReadyProbeTimeout,
	CheckRedirect: func(_ *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return http.ErrUseLastResponse
		}
		return nil
	},
}

// isConsoleHost mirrors the marketplace frontend's own guard
// (LaunchingStep.svelte parseParams): the target must be an https `console`
// or `console.*` host. This is the SSRF guard for this endpoint — without
// it, the `host` query param would let any caller make this service issue
// an arbitrary outbound GET.
func isConsoleHost(u *url.URL) bool {
	if u == nil || u.Scheme != "https" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "console" || strings.HasPrefix(h, "console.")
}

// ipsArePublic reports whether every address in ips is a real, publicly
// routable unicast address (not private/loopback/link-local/unspecified).
// Every per-Org console host on this platform is a real public-internet
// endpoint (a pool-domain A-record + LE cert, per docs/DOD.md domains
// canon) — resolving to anything else means either DNS-rebinding abuse of
// the `host` param or a caller error, and either way this handler should
// fail closed to "not ready" rather than let this service's outbound
// network access be used to probe internal infrastructure.
func ipsArePublic(ips []net.IP) bool {
	if len(ips) == 0 {
		return false
	}
	for _, ip := range ips {
		if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return false
		}
	}
	return true
}

// resolvesToPublicIP resolves host and applies the ipsArePublic guard.
var resolvesToPublicIP = func(host string) bool {
	ips, err := net.LookupIP(host)
	if err != nil {
		return false
	}
	return ipsArePublic(ips)
}

// probeConsoleHealthz issues the actual GET <origin>/healthz and reports
// whether it answered with a 2xx. Any transport failure (DNS, TLS,
// connection refused, timeout) or non-2xx status is "not ready" — the exact
// semantics the original no-cors probe intended (NXDOMAIN / TLS-handshake /
// connection-refused all tolerated as "still provisioning") but, unlike an
// opaque no-cors Response, this ACTUALLY reads the status instead of
// guessing from fetch-resolved-vs-rejected.
func probeConsoleHealthz(ctx context.Context, origin string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := consoleReadyHTTPClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// ConsoleReady probes a per-Org console host's /healthz server-side and
// reports whether it is reachable + answering. Always responds 200 with
// {"ready": bool} for a validly-shaped host param — network/TLS/DNS
// failures and non-2xx upstream responses all fold into ready:false,
// matching the original probe's "swallow and keep polling" contract. A
// missing or non-console host param 400s instead: that is a caller bug,
// never "still provisioning".
func (h *Handler) ConsoleReady(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("host"))
	if raw == "" {
		respond.Error(w, http.StatusBadRequest, "host query param is required")
		return
	}
	target, err := url.Parse(raw)
	if err != nil || !isConsoleHost(target) {
		respond.Error(w, http.StatusBadRequest, "host must be an https console.* origin")
		return
	}
	if !resolvesToPublicIP(target.Hostname()) {
		respond.OK(w, map[string]bool{"ready": false})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), consoleReadyProbeTimeout)
	defer cancel()
	// Reconstruct the origin ourselves (scheme + host only) rather than
	// trusting any path/query the caller appended — the probe path is
	// always literally /healthz.
	origin := (&url.URL{Scheme: target.Scheme, Host: target.Host}).String()
	respond.OK(w, map[string]bool{"ready": probeConsoleHealthz(ctx, origin)})
}
