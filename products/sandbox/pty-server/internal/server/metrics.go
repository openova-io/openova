// metrics.go — Prometheus metrics for pty-server (Wave 15, PR #1674).
//
// Wave 14 (PR #1674) shipped a Grafana dashboard with a panel
// "WebSocket Connections" that targets metric
// `pty_server_websocket_connections`. The panel renders "No data" until
// the pty-server image carrying this emitter rolls out across the fleet
// (Inviolable Principle #11 — no fabricated metrics).
//
// This file closes that loop on the pty-server side:
//
//   - Registers a Gauge `pty_server_websocket_connections` with the
//     prometheus default registry.
//   - Exposes `/metrics` via promhttp.Handler() — wired in routes.go.
//   - The gauge is incremented on every successful WS upgrade (attach
//     + cards endpoints) and decremented in the defer when the
//     connection closes — so a Pod scaled to 0 immediately reports 0
//     and a Pod with N active terminals reports N.
//
// Per-Pod cardinality only — no labels. Aggregation by namespace +
// sandbox is performed at the Prometheus level (the dashboard panel
// uses `sum(pty_server_websocket_connections{namespace=~"$namespace"})`).
package server

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// websocketConnections — current live WebSocket connection count.
// Sister metric of the architecture.md §1 idle-scaler: when this drops
// to 0 the idle-timeout window starts ticking.
var websocketConnections = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "pty_server_websocket_connections",
	Help: "Current number of attached pty-server WebSocket connections (sum of /sessions/{id}/attach + /sessions/{id}/cards).",
})

// metricsHandler is the http.Handler that serves /metrics. Exposed as a
// package-private function so routes.go can mount it without leaking
// the underlying promhttp dependency.
func metricsHandler() http.Handler {
	return promhttp.Handler()
}
