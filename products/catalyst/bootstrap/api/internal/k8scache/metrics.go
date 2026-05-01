// metrics.go — Prometheus metrics for the K8s data plane.
//
// Per ADR-0001 §5 the catalyst-api informer cache is the system-wide
// view of cluster state. These metrics let an operator answer:
//   - Is every (cluster, kind) informer making progress?
//     → informer_last_event_seconds (now − last event)
//   - How many objects sit in each Indexer?
//     → informer_cache_size
//   - How often did an informer LIST again from scratch?
//     → informer_resync_total
//   - How many SSE clients are connected?
//     → sse_clients_connected
//   - Did the producer drop events because a slow SSE consumer wedged?
//     → informer_event_drops_total
//
// The metrics handler is mounted on /metrics by main.go (alongside
// /healthz). Prometheus scrapes it the same way it scrapes every
// other catalyst component.
package k8scache

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// informer_last_event_seconds — UNIX timestamp of the most
	// recent ADD/UPDATE/DELETE event a given (cluster, kind)
	// informer dispatched. An alert fires when (now − this value)
	// exceeds the watch-stream max-idle threshold (typical: 10
	// minutes for low-cardinality kinds, 30s for Pod/Deployment).
	metricLastEvent = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "catalyst",
		Subsystem: "k8scache",
		Name:      "informer_last_event_seconds",
		Help:      "UNIX timestamp of the most recent informer event per (cluster, kind).",
	}, []string{"cluster", "kind"})

	// informer_cache_size — current count of objects in the
	// (cluster, kind) Indexer. Updated on every List() call and on
	// every event dispatch.
	metricCacheSize = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "catalyst",
		Subsystem: "k8scache",
		Name:      "informer_cache_size",
		Help:      "Object count in the in-process Indexer per (cluster, kind).",
	}, []string{"cluster", "kind"})

	// informer_resync_total — counts every full re-LIST a given
	// informer performed since process start. A non-zero rate
	// indicates the informer's watch was disconnected and
	// re-established.
	metricResyncs = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "catalyst",
		Subsystem: "k8scache",
		Name:      "informer_resync_total",
		Help:      "Number of full LIST resyncs per (cluster, kind).",
	}, []string{"cluster", "kind"})

	// informer_events_total — counts every dispatched event by
	// type. Lets an operator see "we got 12k Pod ADDED in the
	// last hour" → cluster churn.
	metricEvents = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "catalyst",
		Subsystem: "k8scache",
		Name:      "informer_events_total",
		Help:      "Dispatched events per (cluster, kind, event_type).",
	}, []string{"cluster", "kind", "event_type"})

	// sse_clients_connected — gauge of currently-connected SSE
	// subscribers. Updated on every Subscribe / unsubscribe.
	metricSubscribers = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "catalyst",
		Subsystem: "k8scache",
		Name:      "sse_clients_connected",
		Help:      "Currently-connected SSE subscribers.",
	})

	// sse_event_drops_total — counts events the producer dropped
	// because a slow subscriber's channel was full. The drop
	// strategy is "drop oldest"; see Factory.fanout.
	metricSubDrops = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "catalyst",
		Subsystem: "k8scache",
		Name:      "sse_event_drops_total",
		Help:      "Events dropped due to slow SSE subscribers, per (cluster, kind).",
	}, []string{"cluster", "kind"})

	// snapshot_writes_total — counts disk-snapshot writes per
	// (cluster, kind). Increments once per snapshot interval per
	// (cluster, kind).
	metricSnapshotWrites = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "catalyst",
		Subsystem: "k8scache",
		Name:      "snapshot_writes_total",
		Help:      "Successful disk-snapshot writes per (cluster, kind).",
	}, []string{"cluster", "kind"})

	// snapshot_hydrate_total — counts how many objects were
	// hydrated from disk on cold start. A label `result` =
	// "hydrated" | "expired" | "missing" | "failed" lets the
	// operator see how often the cold-start mitigation paid off.
	metricSnapshotHydrate = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "catalyst",
		Subsystem: "k8scache",
		Name:      "snapshot_hydrate_total",
		Help:      "Hydrate outcomes per (cluster, kind, result).",
	}, []string{"cluster", "kind", "result"})

	// sar_cache_hits_total / sar_cache_miss_total — SubjectAccessReview
	// cache effectiveness. The handler caches SAR results for ~30s
	// per (user, kind, namespace) to avoid hammering the apiserver
	// per-event.
	metricSARHits = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "catalyst",
		Subsystem: "k8scache",
		Name:      "sar_cache_hits_total",
		Help:      "SubjectAccessReview cache hits, per (cluster, kind).",
	}, []string{"cluster", "kind"})
	metricSARMiss = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "catalyst",
		Subsystem: "k8scache",
		Name:      "sar_cache_miss_total",
		Help:      "SubjectAccessReview cache misses (apiserver calls), per (cluster, kind).",
	}, []string{"cluster", "kind"})
)
