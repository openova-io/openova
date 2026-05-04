// Package handlers exposes the small HTTP surfaces that the
// metering sidecar serves directly (i.e., NOT proxied through to
// NewAPI): /healthz (liveness/readiness) and /metrics (publisher
// counters).
//
// Per docs/INVIOLABLE-PRINCIPLES.md the metrics endpoint is JSON
// (not Prometheus text) until kube-prometheus-stack is wired into
// the Sovereign — once that lands, a follow-up adds a /metrics-prom
// endpoint emitting the canonical exposition format. The current
// JSON shape is sufficient for the sidecar's own health UI and the
// e2e tests that count published envelopes.
package handlers

import (
	"net/http"

	"github.com/openova-io/openova/core/services/metering-sidecar/proxy"
	"github.com/openova-io/openova/core/services/shared/respond"
)

// MetricsHandler returns an http.HandlerFunc that emits the
// publisher's atomic counters as JSON. The handler is read-only and
// requires no auth — the sidecar's listen address is in-cluster only,
// not reachable from the public internet.
func MetricsHandler(pub *proxy.MeteringPublisher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if pub == nil {
			respond.OK(w, map[string]int64{})
			return
		}
		respond.OK(w, pub.MetricsSnapshot())
	}
}
