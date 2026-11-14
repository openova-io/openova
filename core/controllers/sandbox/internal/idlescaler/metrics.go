// metrics.go — Prometheus metrics for the IdleScaler (Wave 15, PR #1674).
//
// Wave 14 (PR #1674) shipped a Grafana dashboard panel
// "Idle-Timeout Scale-Down Events / hour" that targets metric
// `sandbox_controller_idle_timeout_events_total`. The panel renders
// "No data" until the sandbox-controller image carrying this emitter
// rolls out across the fleet (Inviolable Principle #11 — no fabricated
// metrics).
//
// This file closes that loop on the controller side:
//
//   - Registers Counter `sandbox_controller_idle_timeout_events_total`
//     with label {namespace} via controller-runtime's metrics registry
//     (sigs.k8s.io/controller-runtime/pkg/metrics). The controller's
//     manager already wires up /metrics on :8080 — registering with
//     ctrlmetrics.Registry surfaces this counter on the same scrape.
//   - The IdleScaler calls IncIdleTimeoutEvent(namespace) inside
//     scaleToZero() so the counter ticks once per pty-server
//     StatefulSet scaled to 0 replicas, with the namespace label
//     matching the dashboard's `sum by (namespace) (rate(...))`
//     aggregation.
package idlescaler

import (
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var idleTimeoutEvents = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "sandbox_controller_idle_timeout_events_total",
	Help: "Number of pty-server StatefulSets scaled to 0 replicas by the IdleScaler, partitioned by namespace.",
}, []string{"namespace"})

func init() {
	// Register with controller-runtime's shared registry so the
	// manager's existing :8080 /metrics endpoint exposes it. Re-
	// registration on test process reuse is guarded by ctrlmetrics.
	ctrlmetrics.Registry.MustRegister(idleTimeoutEvents)
}
