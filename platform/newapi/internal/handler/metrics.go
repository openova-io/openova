// metrics.go — Prometheus metrics for the newapi /admin/tokens/sandbox
// bridge handler (Wave 15, PR #1674 follow-up).
//
// Wave 14 (PR #1674) shipped a Grafana dashboard panel
// "newapi Token Mint Requests / hour (tool=\"sandbox-controller\")"
// that targets metric `newapi_admin_token_mint_requests_total` with
// labels {tool, status}. The panel renders "No data" until the catalyst-
// api binary carrying this emitter rolls out across the fleet
// (Inviolable Principle #11 — no fabricated metrics).
//
// This file closes that loop on the bridge-handler side:
//
//   - Registers Counter `newapi_admin_token_mint_requests_total`
//     with labels {tool, status} on the prometheus default registry.
//     Any HTTP server that exposes promhttp.Handler() picks it up.
//   - `tool` is read from the inbound `X-Catalyst-Tool` header. The
//     sandbox-controller sends `X-Catalyst-Tool: sandbox-controller`
//     on every POST so the dashboard's `tool="sandbox-controller"`
//     filter has a non-empty value. Missing header ⇒ "unknown" so the
//     counter still emits (no metric loss).
//   - `status` is the response classification:
//       - "ok"           — 200 OK
//       - "unauthorized" — 401
//       - "bad_request"  — 400
//       - "unavailable"  — 503
//       - "server_error" — 500 (mint failed)
//       - "method_not_allowed" — 405 (non-POST hit /admin/tokens/sandbox)
//     The classification is finite (cardinality cap = 6 × N(tool)) so
//     Prometheus storage stays bounded.
//   - `MetricsHandler()` returns promhttp.Handler() so the catalyst-api
//     wiring code can mount /metrics on the same listener as the
//     bridge handler.
package handler

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// HeaderTool is the conventional header callers stamp on every bridge
// request so the metrics counter can attribute mints to the calling
// tool. Exported so the sandbox-controller test in
// core/controllers/sandbox/internal/newapi can reference the same
// constant string.
const HeaderTool = "X-Catalyst-Tool"

// ToolUnknown is the fallback `tool` label value when the inbound
// request carries no X-Catalyst-Tool header.
const ToolUnknown = "unknown"

var tokenMintRequests = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "newapi_admin_token_mint_requests_total",
	Help: "POST /admin/tokens/sandbox calls landing on the catalyst-api bridge handler, partitioned by calling tool + response status.",
}, []string{"tool", "status"})

// MetricsHandler returns the Prometheus scrape http.Handler. Exposed
// so the catalyst-api server that mounts SandboxToken can mount
// /metrics on the same listener.
func MetricsHandler() http.Handler {
	return promhttp.Handler()
}

// classifyStatus maps an HTTP status code to one of the finite-cardinality
// status label values. Anything outside the documented set maps to
// "other" so a future status code never silently leaks unbounded labels.
func classifyStatus(code int) string {
	switch code {
	case http.StatusOK:
		return "ok"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusServiceUnavailable:
		return "unavailable"
	case http.StatusInternalServerError:
		return "server_error"
	case http.StatusMethodNotAllowed:
		return "method_not_allowed"
	default:
		return "other"
	}
}

// recordMint increments the counter with the inbound tool header +
// classified status. Wrapped so the SandboxToken handler stays a clean
// "decide status, then writeJSON + recordMint" idiom.
func recordMint(r *http.Request, status int) {
	tool := r.Header.Get(HeaderTool)
	if tool == "" {
		tool = ToolUnknown
	}
	tokenMintRequests.WithLabelValues(tool, classifyStatus(status)).Inc()
}
