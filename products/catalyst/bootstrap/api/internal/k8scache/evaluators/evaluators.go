// Package evaluators — synthetic PolicyReport producers for compliance
// signals Kyverno cannot evaluate at admission.
//
// EPIC-1 (#1096) Slice W2 — five evaluators ship in this package:
//
//   - hpa     : HPA min replicas vs Deployment.replicas
//   - otel    : Pod has otel-collector sidecar OR namespace has Instrumentation CR
//   - hubble  : Cilium Hubble has observed flow to/from this Pod (deferred — no client dep)
//   - harbor  : Container image refs harbor.<sovereign>/...
//   - flux    : Resource has app.kubernetes.io/managed-by=flux OR Flux ownerRef
//
// Architecture (per docs/EPICS-1-6-unified-design.md §4.1, brief
// `02-W-watcher-extension.md`):
//
//	k8scache.Factory                    evaluators.Engine
//	─────────────────                   ──────────────────
//	[informer events] ─Pod ADDED ──→  Subscribe(kinds={pod})
//	                                     │
//	                            ┌────────┴────────┐
//	                            │ EvaluateAll(...) │
//	                            └────────┬────────┘
//	                                     │
//	                          synthetic SyntheticReport rows
//	                                     │
//	                            Factory.Publish(Event{Kind:"compliance-evaluator"})
//	                                     │
//	[SSE fanout] ←───────────── re-enters the same fanout the
//	                            architecture-graph subscribers consume
//
// Per ADR-0001 §5: the 30s ticker re-evaluates over the in-process
// Indexer, NOT against the apiserver. Evaluators are pure functions
// over a Snapshot read-interface — they never make REST calls.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4: every threshold (lookback
// window, regex pattern, registry domain template) is a Config field
// — no hardcoded values.
package evaluators

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

// Result is the canonical outcome — string-typed so it round-trips
// 1:1 through the synthetic PolicyReport JSON wire format the score
// aggregator (slice S1) consumes.
type Result string

const (
	// ResultPass — workload satisfies the policy.
	ResultPass Result = "pass"
	// ResultFail — workload violates the policy.
	ResultFail Result = "fail"
	// ResultSkip — policy not applicable to this workload (e.g. HPA
	// not present, Hubble UI disabled). Slice S1's normalizer drops
	// skip rows from the denominator.
	ResultSkip Result = "skip"
	// ResultWarn — informational, does not affect the score.
	ResultWarn Result = "warn"
)

// SyntheticReport mirrors the wgpolicyk8s.io PolicyReport row schema so
// the score aggregator's join is uniform across Kyverno-emitted rows
// and evaluator-emitted ones.
//
// Wire-shape: encoded directly into the SSE event's Object. The score
// aggregator deserialises this struct from each event's
// .object.results[0] field — keep field names stable across releases.
type SyntheticReport struct {
	// Policy — short canonical policy name, matching the EPIC-1 §4.3
	// table ("hpa-effective", "otel-injected", "harbor-proxy-pull",
	// "flux-managed", "hubble-flows-seen").
	Policy string `json:"policy"`

	// Rule — for parity with Kyverno's per-rule reporting; equal to
	// Policy for the simple one-rule evaluators in this package.
	Rule string `json:"rule"`

	// Result — pass / fail / skip / warn.
	Result Result `json:"result"`

	// Resource — back-pointer to the offending K8s object. Populated
	// from the workload metadata (apiVersion, kind, name, namespace,
	// uid). Skip rows still set Resource so the UI drill-down shows
	// the workload that was inspected.
	Resource metav1.OwnerReference `json:"resource"`

	// Namespace — copied from the workload for the namespace-aware
	// drill-down filter.
	Namespace string `json:"namespace,omitempty"`

	// Message — human-readable explanation.
	Message string `json:"message,omitempty"`

	// Properties — arbitrary key-value details (e.g. for HPA the
	// minReplicas + currentReplicas observed values; for Harbor the
	// rejected image ref).
	Properties map[string]string `json:"properties,omitempty"`

	// Time — server-side timestamp the row was produced. Driven by
	// the Engine's clock for deterministic test fixtures.
	Time metav1.Time `json:"time"`
}

// Snapshot is the read-side interface evaluators consume. Backed in
// production by k8scache.Factory.List which reads the in-process
// Indexer (no apiserver calls). Tests inject a fake.
//
// Cluster-scope: every evaluator works against ONE Sovereign at a
// time — the cluster id is passed at Engine.Run time. Snapshot.List
// returns objects from that single cluster's Indexer.
type Snapshot interface {
	// List returns every object of `kindName` currently in the cache,
	// optionally filtered by a label selector. An empty selector
	// returns all objects. Returns an error when the kind is not
	// registered.
	List(kindName string, sel labels.Selector) ([]*unstructured.Unstructured, error)
}

// Evaluator is a pure function: given a snapshot of the cluster and a
// target workload (Pod / Deployment / etc.), produce zero or more
// SyntheticReport rows.
//
// Evaluators MUST NOT block on I/O — they read from `snap` (the local
// Indexer) and return. Long-running reachability checks (e.g. Hubble
// flow query) belong on the engine's tick goroutine, not in
// Evaluate.
type Evaluator interface {
	// Name returns the canonical policy name. Used for logging,
	// metrics, and the SyntheticReport.Policy field.
	Name() string

	// Evaluate applies the policy to the target. May return:
	//   - one row (typical)
	//   - zero rows (target not in scope — skipped silently rather
	//     than emitting a skip row; e.g. otel evaluator skips
	//     non-application workloads)
	//   - multiple rows (one per container, etc.)
	Evaluate(ctx context.Context, snap Snapshot, target *unstructured.Unstructured) []SyntheticReport
}

// Config — runtime knobs for the engine and individual evaluators.
// Per INVIOLABLE-PRINCIPLES #4 every threshold is a config var.
type Config struct {
	// Logger — required.
	Logger *slog.Logger

	// TickInterval — how often the engine re-evaluates over the
	// snapshot. Defaults to 30s. Setting to 0 disables the ticker (the
	// engine still reacts to Pod ADD/MODIFY events).
	TickInterval time.Duration

	// HPAMinReplicas — only inspected by hpa.go; the floor under
	// which the evaluator emits FAIL even when HPA is happy. Default
	// 1 (any positive replica count is acceptable).
	HPAMinReplicas int32

	// HubbleEnabled — when false the hubble evaluator emits skip
	// without trying to talk to the Hubble Observer API. Wired from
	// the bp-cilium chart's `hubble.relay.enabled` value.
	HubbleEnabled bool

	// HubbleLookbackWindow — how far back in time the hubble
	// evaluator searches for flows touching the Pod. Default 5min.
	HubbleLookbackWindow time.Duration

	// HarborDomain — the per-Sovereign Harbor host used by the harbor
	// evaluator's prefix check (e.g. `harbor.omantel.omani.works`).
	// Empty disables the harbor evaluator (skip everywhere). Per
	// `feedback_never_hardcode_urls.md` this is a runtime config —
	// the value is sourced from the Sovereign's bp-harbor chart, NOT
	// from Go.
	HarborDomain string

	// HarborAllowedPrefixes — additional registry prefixes that are
	// permitted (e.g. internal mirrors). Default empty. The harbor
	// evaluator passes if image starts with `<HarborDomain>/` OR any
	// of these prefixes.
	HarborAllowedPrefixes []string

	// FluxManagedByLabel — label key whose value `flux` indicates
	// Flux ownership. Default `app.kubernetes.io/managed-by`.
	FluxManagedByLabel string

	// FluxManagedByValue — label value indicating Flux ownership.
	// Default `flux`.
	FluxManagedByValue string

	// OTelInjectAnnotationPrefix — annotation prefix that signals
	// OTel auto-instrumentation (Pod-level). Default
	// `instrumentation.opentelemetry.io/inject-`. The evaluator
	// checks any annotation key with this prefix whose value is
	// `true`.
	OTelInjectAnnotationPrefix string

	// OTelSidecarImageMatch — substring matched against each
	// container's image to detect an OTel collector sidecar.
	// Default `opentelemetry-collector`.
	OTelSidecarImageMatch string

	// OTelInstrumentationKind — the kind name (in the k8scache
	// registry) for the namespace-scoped Instrumentation CR. When
	// the kind is not registered the evaluator falls back to
	// sidecar + annotation checks only. Default
	// `instrumentation.opentelemetry.io`.
	OTelInstrumentationKind string

	// Now — time source for SyntheticReport.Time. Defaults to
	// time.Now. Overridden by tests for deterministic timestamps.
	Now func() time.Time
}

func (c *Config) defaults() {
	if c.TickInterval == 0 {
		c.TickInterval = 30 * time.Second
	}
	if c.HPAMinReplicas == 0 {
		c.HPAMinReplicas = 1
	}
	if c.HubbleLookbackWindow == 0 {
		c.HubbleLookbackWindow = 5 * time.Minute
	}
	if c.FluxManagedByLabel == "" {
		c.FluxManagedByLabel = "app.kubernetes.io/managed-by"
	}
	if c.FluxManagedByValue == "" {
		c.FluxManagedByValue = "flux"
	}
	if c.OTelInjectAnnotationPrefix == "" {
		c.OTelInjectAnnotationPrefix = "instrumentation.opentelemetry.io/inject-"
	}
	if c.OTelSidecarImageMatch == "" {
		c.OTelSidecarImageMatch = "opentelemetry-collector"
	}
	if c.OTelInstrumentationKind == "" {
		c.OTelInstrumentationKind = "instrumentation.opentelemetry.io"
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

// Publisher is the write-side interface — the Engine pushes synthetic
// events through this. Production wires it to k8scache.Factory.Publish;
// tests inject a recorder.
type Publisher interface {
	// Publish emits one synthetic event. The Engine wraps every
	// SyntheticReport into the wire envelope before calling.
	Publish(clusterID string, report SyntheticReport)
}

// EvaluateAll runs every evaluator against the target and returns the
// concatenated results. Convenience wrapper used by the engine and
// tests; never returns nil — empty slice on no findings.
//
// Order is deterministic — evaluators are applied in the order
// supplied. Tests rely on this; do not parallelise here.
func EvaluateAll(ctx context.Context, snap Snapshot, target *unstructured.Unstructured, evals []Evaluator) []SyntheticReport {
	if target == nil {
		return nil
	}
	out := make([]SyntheticReport, 0, len(evals))
	for _, e := range evals {
		rows := e.Evaluate(ctx, snap, target)
		out = append(out, rows...)
	}
	return out
}

// Engine subscribes to the watcher's Pod events, runs every registered
// evaluator on each event, and publishes the synthetic reports back
// through the SSE fanout.
//
// Lifecycle:
//   - NewEngine validates Config + evaluator set
//   - Run blocks until ctx is cancelled; spawns one goroutine per
//     subscribed cluster + one ticker goroutine
//   - Multiple invocations are NOT supported on the same Engine; create
//     a new Engine for re-runs
type Engine struct {
	cfg        Config
	evaluators []Evaluator
	pub        Publisher

	// resolveSnapshot maps clusterID → Snapshot. The factory has one
	// Indexer per cluster; the engine looks up by id on every event /
	// tick.
	resolveSnapshot func(clusterID string) (Snapshot, []string, error)

	// subscribe maps a (user, kinds) pair to a channel. Production
	// wires to Factory.Subscribe.
	subscribe func(kinds map[string]struct{}) (<-chan Event, func())

	mu      sync.Mutex
	started bool
}

// Event is the engine's internal copy of k8scache.Event minus the
// EventType — the engine only cares about ADD / MODIFY for the trigger
// path. DELETE is handled implicitly: when a target disappears from
// the Indexer, the next tick stops emitting reports for it.
type Event struct {
	Cluster string
	Kind    string
	Object  *unstructured.Unstructured
}

// NewEngine wires an Engine without starting it.
func NewEngine(cfg Config, evals []Evaluator, pub Publisher,
	resolveSnapshot func(clusterID string) (Snapshot, []string, error),
	subscribe func(kinds map[string]struct{}) (<-chan Event, func()),
) (*Engine, error) {
	if cfg.Logger == nil {
		return nil, errors.New("evaluators: Config.Logger is required")
	}
	if pub == nil {
		return nil, errors.New("evaluators: Publisher is required")
	}
	if resolveSnapshot == nil {
		return nil, errors.New("evaluators: resolveSnapshot is required")
	}
	if subscribe == nil {
		return nil, errors.New("evaluators: subscribe is required")
	}
	if len(evals) == 0 {
		return nil, errors.New("evaluators: at least one Evaluator must be registered")
	}
	cfg.defaults()
	return &Engine{
		cfg:             cfg,
		evaluators:      evals,
		pub:             pub,
		resolveSnapshot: resolveSnapshot,
		subscribe:       subscribe,
	}, nil
}

// Run starts the engine and blocks until ctx is cancelled. On exit it
// closes the watcher subscription cleanly.
func (e *Engine) Run(ctx context.Context) error {
	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		return errors.New("evaluators: Engine already started")
	}
	e.started = true
	e.mu.Unlock()

	// Subscribe to Pod events from the underlying watcher.
	ch, unsub := e.subscribe(map[string]struct{}{"pod": {}})
	defer unsub()

	// Periodic ticker — pure cache reads, no apiserver polling. Per
	// ADR-0001 §5 this is acceptable because evaluators compute over
	// data already in the in-process Indexer.
	var tick <-chan time.Time
	if e.cfg.TickInterval > 0 {
		t := time.NewTicker(e.cfg.TickInterval)
		defer t.Stop()
		tick = t.C
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-ch:
			if !ok {
				// Subscription closed by the factory (Stop). Exit
				// cleanly.
				return nil
			}
			e.evaluateOne(ctx, ev.Cluster, ev.Object)
		case <-tick:
			e.evaluateAllClusters(ctx)
		}
	}
}

// evaluateOne runs every evaluator against a single target and
// publishes the resulting rows.
func (e *Engine) evaluateOne(ctx context.Context, clusterID string, target *unstructured.Unstructured) {
	if target == nil {
		return
	}
	snap, _, err := e.resolveSnapshot(clusterID)
	if err != nil {
		e.cfg.Logger.Warn("evaluators: snapshot unavailable for event",
			"cluster", clusterID, "err", err)
		return
	}
	rows := EvaluateAll(ctx, snap, target, e.evaluators)
	for i := range rows {
		if rows[i].Time.IsZero() {
			rows[i].Time = metav1.NewTime(e.cfg.Now())
		}
		e.pub.Publish(clusterID, rows[i])
	}
}

// evaluateAllClusters fans the tick across every cluster the
// resolveSnapshot function knows about. Each cluster's Pod list is
// pulled from its Indexer and every evaluator runs against every
// target. Cost is O(clusters × pods × evaluators) but all reads are
// from the local cache.
func (e *Engine) evaluateAllClusters(ctx context.Context) {
	// resolveSnapshot returns the cluster's Snapshot AND the list of
	// known cluster ids when the second return is non-empty. Callers
	// can pass clusterID="" to mean "give me the list of known
	// clusters".
	_, clusters, err := e.resolveSnapshot("")
	if err != nil || len(clusters) == 0 {
		return
	}
	for _, id := range clusters {
		snap, _, err := e.resolveSnapshot(id)
		if err != nil {
			continue
		}
		pods, err := snap.List("pod", labels.Everything())
		if err != nil {
			continue
		}
		for _, p := range pods {
			rows := EvaluateAll(ctx, snap, p, e.evaluators)
			for i := range rows {
				if rows[i].Time.IsZero() {
					rows[i].Time = metav1.NewTime(e.cfg.Now())
				}
				e.pub.Publish(id, rows[i])
			}
		}
	}
}

// helpers --------------------------------------------------------

// resourceFor builds an OwnerReference from the target's metadata.
// All five evaluators populate SyntheticReport.Resource via this so
// the wire shape is uniform.
func resourceFor(target *unstructured.Unstructured) metav1.OwnerReference {
	if target == nil {
		return metav1.OwnerReference{}
	}
	gv := target.GetAPIVersion()
	return metav1.OwnerReference{
		APIVersion: gv,
		Kind:       target.GetKind(),
		Name:       target.GetName(),
		UID:        target.GetUID(),
	}
}

// containerImages returns every container image string in the Pod
// (initContainers + containers + ephemeralContainers). Empty slice
// when the target is not a Pod or has no containers. Used by harbor
// + otel evaluators.
func containerImages(target *unstructured.Unstructured) []string {
	if target == nil {
		return nil
	}
	out := []string{}
	for _, group := range []string{"containers", "initContainers", "ephemeralContainers"} {
		raw, found, _ := unstructured.NestedSlice(target.Object, "spec", group)
		if !found {
			continue
		}
		for _, item := range raw {
			c, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if img, ok := c["image"].(string); ok && img != "" {
				out = append(out, img)
			}
		}
	}
	return out
}

// containerNames returns container names alongside images — needed by
// the otel evaluator's "is there a sidecar named otel-*" branch.
func containerNames(target *unstructured.Unstructured) []string {
	if target == nil {
		return nil
	}
	out := []string{}
	for _, group := range []string{"containers", "initContainers"} {
		raw, found, _ := unstructured.NestedSlice(target.Object, "spec", group)
		if !found {
			continue
		}
		for _, item := range raw {
			c, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if n, ok := c["name"].(string); ok && n != "" {
				out = append(out, n)
			}
		}
	}
	return out
}

// hasOwnerOfKind returns true when the target has at least one
// ownerReference whose APIVersion and Kind match. Empty apiVersion
// matches any group (used by the flux evaluator's HelmRelease /
// Kustomization detection).
func hasOwnerOfKind(target *unstructured.Unstructured, apiGroupSuffix, kind string) bool {
	if target == nil {
		return false
	}
	for _, ref := range target.GetOwnerReferences() {
		if !strings.EqualFold(ref.Kind, kind) {
			continue
		}
		if apiGroupSuffix == "" || strings.Contains(ref.APIVersion, apiGroupSuffix) {
			return true
		}
	}
	return false
}

// isPod returns true when the target is a v1 Pod.
func isPod(target *unstructured.Unstructured) bool {
	if target == nil {
		return false
	}
	return target.GetKind() == "Pod"
}

// reportTime — produce a metav1.Time from the engine's clock; used by
// evaluators that may emit before the engine fills it in.
func reportTime(now func() time.Time) metav1.Time {
	if now == nil {
		now = time.Now
	}
	return metav1.NewTime(now())
}

// formatLabelMap stringifies a label set for human-readable
// SyntheticReport.Properties values.
func formatLabelMap(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, ",")
}

// SubscribeAdapter wraps a k8scache.Factory.Subscribe call into the
// engine's Event channel. Production code in the catalyst-api
// `cmd/api/main.go` wires this. Exposed here so tests can construct
// an Engine without depending on the full Factory.
type SubscribeAdapter func(kinds map[string]struct{}) (<-chan Event, func())

// EventFromUnstructured is a test convenience to build Event
// values without exposing the unexported field. Used by the table
// tests in evaluators_test.go.
func EventFromUnstructured(cluster, kind string, obj *unstructured.Unstructured) Event {
	return Event{Cluster: cluster, Kind: kind, Object: obj}
}
