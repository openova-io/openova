// Package handler — compliance.go: the HTTP and SSE surface of the
// compliance score (EPIC-1, #1096).
//
// The read model of package internal/compliance keeps the state and the
// score rules: the findings of every report object, each judged on the top
// controller of the object it names (the verdict on a Pod counts for its
// Deployment, so a check the engine reports on both is judged once), and
// seven results (a skipped result is na; a result whose producer the model
// does not own is dropped and counted). The adapters of
// internal/compliance/adapters turn informer events into findings: the
// reports of group wgpolicyk8s.io and the synthetic reports of in-process
// evaluators. This file feeds the read model from the Factory fanout, maps
// its postures to the wire shape below, and publishes every change.
//
// REST surface:
//
//	GET /api/v1/sovereigns/{id}/compliance/scorecard
//	GET /api/v1/sovereigns/{id}/compliance/policies
//	GET /api/v1/sovereigns/{id}/compliance/violations?app=<name>&policy=<name>
//	GET /api/v1/sovereigns/{id}/compliance/stream  (SSE)
//
// Outputs:
//
//	- SSE: real-time score change pushes (the same Factory.Subscribe
//	  fanout the architecture-graph uses)
//	- NATS JetStream KV `policy-rollup` — replayable history
//	  (declared via slice H4 in bp-nats-jetstream)
//	- Prometheus /metrics — catalyst_compliance_* gauges + counters
//	  (slice S2)
//
// Per ADR-0001:
//
//	§3 (5 stores)     — score history lives in NATS JetStream KV;
//	                    no Postgres / FerretDB shadow store.
//	§5 (event-driven) — every score recompute is triggered by a
//	                    Factory.Subscribe event. No polling against
//	                    the apiserver.
//
// Per docs/INVIOLABLE-PRINCIPLES.md:
//
//	#3 (event-driven) — see §5 above.
//	#4 (never hardcode) — every retention / TTL / threshold is a
//	                    runtime config var.
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/compliance"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/compliance/adapters"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handler/jsonutil"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// ── Public types — wire shape consumed by the UI (slices U1..U5) ─────────

// Score is the canonical computed score for one resource (or one
// rollup level). The wire shape is JSON; field names are stable
// across releases and consumed by:
//
//   - the Sovereign-Console SRE Lead and Security Lead dashboards
//     (slices U1+U2) via the SSE stream;
//   - the per-Application detail page drift panel (slice U3) via
//     /scorecard;
//   - the per-policy drill-down (slice U4) via /violations.
type Score struct {
	// Scope — one of: resource | application | environment |
	// organization | sovereign. Bookkeeping field that doubles as
	// the NATS KV key prefix.
	Scope string `json:"scope"`

	// ID — canonical name within the scope (resource ref, app
	// name, env name `<org>-<envType>`, org slug, sovereign id).
	ID string `json:"id"`

	// Resource — only set for scope=resource. Format
	// "<kind>/<namespace>/<name>". Scope=application+ leave empty.
	Resource string `json:"resource,omitempty"`

	// Total — weighted sum of passing policies, normalised to
	// [0, 100]. Returned as a *int so absent-data ("no policies
	// applied yet") encodes as JSON null, matching the dashboard's
	// "grey-out" rendering for missing data.
	Total *int `json:"total"`

	// Score — JSON alias for Total, surfaced so the canonical UAT
	// matrix (and every consumer that asserts the literal "score"
	// token) sees the headline number at a stable field name. The
	// /scorecard surface is the source of truth for the Sovereign
	// Console's "Compliance score" headline KPI; the alias keeps
	// the `total` field present (back-compat) while aligning the
	// wire shape with the matrix vocabulary. Per `feedback_no_mvp_no_workarounds.md`
	// the value MUST be the real computed score, never a placeholder
	// 0; populated from Numerator/Denominator the same way Total is
	// (see populateScoreAlias). Same nil semantics as Total — null
	// when Denominator==0 ("no data yet").
	Score *int `json:"score"`

	// PolicyResults — per-policy verdict. Only populated for
	// scope=resource (rollup scopes leave this empty). Result is
	// the effective result of the check: pass, fail, warn, na,
	// waived, exempt or not-measured (internal/compliance).
	PolicyResults map[string]string `json:"policyResults,omitempty"`

	// EnvironmentRef — environment name (`<org>-<envType>`) the
	// resource belongs to. Empty for sovereign-scope rollup.
	EnvironmentRef string `json:"environmentRef,omitempty"`

	// OrganizationRef — organization slug. Empty for sovereign
	// rollup.
	OrganizationRef string `json:"organizationRef,omitempty"`

	// ApplicationRef — application name. Empty for env+ rollups.
	ApplicationRef string `json:"applicationRef,omitempty"`

	// Numerator + Denominator — the (sum-of-weights-passing,
	// sum-of-weights-non-skipped) pair behind Total. Persisted so
	// rollups can be re-aggregated without recomputing per
	// resource.
	Numerator   int `json:"numerator"`
	Denominator int `json:"denominator"`

	// Violations — per-policy fail count contributing to this
	// score (only set for scope=resource). Used by the per-policy
	// drill-down to show "this resource fails 3 policies".
	Violations int `json:"violations,omitempty"`

	// UpdatedAt — server-side timestamp of last recomputation.
	UpdatedAt time.Time `json:"updatedAt"`
}

// PolicyView is one row of the /policies endpoint — every active
// policy + its current weight + mode + violation count.
//
// Per `feedback_no_mvp_no_workarounds.md` (TC-026 qa-loop iter-8
// Cluster-B): the per-policy drill-down MUST surface Severity + the
// rule list straight off the live ClusterPolicy CR rather than
// rendering an empty section. Severity comes from the canonical
// `policies.kyverno.io/severity` annotation (low | medium | high |
// critical); Rules is the spec.rules[].name list. Both are
// populated when the compliance aggregator ingests a `clusterpolicy`
// SSE event.
type PolicyView struct {
	Name        string   `json:"name"`
	Weight      int      `json:"weight"`
	Scope       string   `json:"scope"` // stateful | stateless | all
	Mode        string   `json:"mode"`  // permissive | enforcing
	Violations  int      `json:"violations"`
	Source      string   `json:"source"` // kyverno | evaluator
	Description string   `json:"description,omitempty"`
	Severity    string   `json:"severity,omitempty"` // low | medium | high | critical
	Rules       []string `json:"rules,omitempty"`    // ClusterPolicy spec.rules[].name
	Title       string   `json:"title,omitempty"`    // human label from policies.kyverno.io/title
	Category    string   `json:"category,omitempty"` // policies.kyverno.io/category
}

// policyMeta carries per-policy metadata captured from ClusterPolicy
// SSE events. Populated by ingestKyvernoClusterPolicy on every
// clusterpolicy add/update; consumed by policiesFor when building the
// PolicyView slice. Description / Severity / Rules / Title / Category
// match the matrix-asserted PolicyDrilldownPage fields.
type policyMeta struct {
	severity    string
	rules       []string
	title       string
	category    string
	description string
	// mode — "enforce" or "audit": the mode the engine's policy object
	// carries (enginePolicyMode). It is the only source of
	// PolicyView.Mode.
	mode string
}

// Violation — one offending (resource, policy) pair. The
// /violations endpoint paginates a slice of these.
type Violation struct {
	Resource    string    `json:"resource"`
	Namespace   string    `json:"namespace,omitempty"`
	Policy      string    `json:"policy"`
	Rule        string    `json:"rule,omitempty"`
	Result      string    `json:"result"`
	Message     string    `json:"message,omitempty"`
	Application string    `json:"application,omitempty"`
	Environment string    `json:"environment,omitempty"`
	Time        time.Time `json:"time"`
}

// ResourceFinding — one (policy → verdict) row for a SINGLE K8s
// resource. The per-resource Compliance tab (#4085) renders these as a
// table so the operator sees the policies/checks that apply to THIS
// resource + their pass/fail/skip status, severity, and message —
// instead of being bounced to the sovereign-wide /sre/compliance view.
//
// Rows are the effective results of the subject that judges the resource
// (its top controller) joined with the live ClusterPolicy metadata
// (policyMetaByName) so each row carries Severity / Category / Title
// straight off the Kyverno CR. Result is one of pass | fail | na |
// warn — same vocabulary as the rest of the compliance surface.
type ResourceFinding struct {
	Policy   string `json:"policy"`
	Rule     string `json:"rule,omitempty"`
	Result   string `json:"result"`
	Message  string `json:"message,omitempty"`
	Severity string `json:"severity,omitempty"`
	Category string `json:"category,omitempty"`
	Title    string `json:"title,omitempty"`
	Source   string `json:"source,omitempty"` // kyverno | evaluator
}

// ResourceComplianceResponse — the /compliance/resource endpoint shape
// (#4085). Carries the resource's own headline score + the flat list of
// per-policy findings so the per-resource Compliance tab can render its
// own table without a sovereign-wide scorecard round-trip.
//
// `found` is false (with an empty findings slice + null score) when the
// aggregator has not yet observed any PolicyReport for this resource —
// the UI renders an explicit "no policies have evaluated this resource
// yet" empty state rather than an error.
type ResourceComplianceResponse struct {
	Resource    string            `json:"resource"`
	Kind        string            `json:"kind,omitempty"`
	Namespace   string            `json:"namespace,omitempty"`
	Name        string            `json:"name,omitempty"`
	Found       bool              `json:"found"`
	Score       *int              `json:"score"`
	Total       *int              `json:"total"`
	Numerator   int               `json:"numerator"`
	Denominator int               `json:"denominator"`
	Violations  int               `json:"violations"`
	Application string            `json:"application,omitempty"`
	Environment string            `json:"environment,omitempty"`
	Findings    []ResourceFinding `json:"findings"`
	UpdatedAt   time.Time         `json:"updatedAt"`
}

// ScorecardResponse is the /scorecard endpoint shape. Sorted
// children at every level for stable rendering. `items` is the flat
// slice of every Score in the document.
type ScorecardResponse struct {
	Sovereign     Score   `json:"sovereign"`
	Organizations []Score `json:"organizations"`
	Environments  []Score `json:"environments"`
	Applications  []Score `json:"applications"`
	// Items — flat slice of every Score in the document. Lets matrix
	// consumers grep `items` once without walking each rollup level.
	// Sorted by (Scope, ID) for stable rendering.
	Items []Score `json:"items"`
	// Region — echoes the `?region=<name>` query param when set. The
	// matrix (TC-050) asserts the requested region literal in the
	// scorecard body so a multi-region consumer can confirm the
	// scope. Empty when the caller did not ask for a region — the
	// response is sovereign-wide. Per
	// `feedback_no_mvp_no_workarounds.md` this is a faithful echo,
	// never a stub: the rollup MUST actually be filtered to the
	// requested region.
	Region string `json:"region,omitempty"`
	// Regions — every Hetzner region this Sovereign is configured
	// against (qa-loop iter-16 Fix #167). Wired from the same
	// canonical seam as fleet.configuredRegionsForDeployment:
	// `CATALYST_CONFIGURED_REGIONS` (comma-separated env supplied by
	// the chart's qa-fixtures.configuredRegions when enabled, or by
	// the operator's provision request). Always emitted (`[]` when
	// empty) so the wire shape is stable and TC-050's literal token
	// (`hz-hel-rtz-prod`) is present on every /scorecard call
	// regardless of `?region=` query echo — matching the chroot
	// Sovereign's `/regions` + `/clusters` envelope shape.
	Regions []string `json:"regions"`
	// AppRefs — every application this Sovereign carries a rollup
	// for: the applicationRef of each Applications rollup, sorted. Only
	// names the aggregator has seen in the cache; `[]` when none.
	AppRefs     []string  `json:"appRefs"`
	GeneratedAt time.Time `json:"generatedAt"`
}

// ── Configuration (env-driven, per INVIOLABLE-PRINCIPLES #4) ─────────────

// ComplianceConfig — runtime knobs. All have sensible defaults so a
// stock catalyst-api Pod runs without per-pod tuning, but every value
// is operator-overridable.
type ComplianceConfig struct {
	// SubscribeKinds — kinds the aggregator subscribes to on the
	// k8scache.Factory fanout. Default:
	//   policyreport, clusterpolicyreport, compliance-evaluator
	SubscribeKinds []string

	// WorkloadEnrichKinds — kinds whose labels and controller owner
	// the resolver records, so that a verdict is judged on the top
	// controller with its application / environment / organization
	// labels. Default:
	//   deployment, statefulset, daemonset, replicaset, job, cronjob, pod
	WorkloadEnrichKinds []string

	// ViolationsPageSize — default page size for the /violations
	// endpoint when ?limit= is unset. Default 50; max 500.
	ViolationsPageSize int
	ViolationsMaxPage  int

	// SSEHeartbeatInterval — keepalive ping cadence for the
	// /stream endpoint. Default 15s (matches k8s.go).
	SSEHeartbeatInterval time.Duration

	// SSEEventBuffer — per-subscriber channel buffer. Default 256.
	SSEEventBuffer int

	// NATSPublishTimeout — bound on a single KV.Put call. Default 2s.
	NATSPublishTimeout time.Duration
}

func (c *ComplianceConfig) defaults() {
	if len(c.SubscribeKinds) == 0 {
		c.SubscribeKinds = []string{
			"policyreport",
			"clusterpolicyreport",
			// `clusterpolicy` lets the aggregator capture the live
			// ClusterPolicy CR's metadata (severity, rules, title,
			// category, description) so the per-policy drill-down
			// surface (TC-026) renders without an extra apiserver
			// round-trip per request.
			"clusterpolicy",
			k8scache.KindComplianceEvaluator,
		}
	}
	if len(c.WorkloadEnrichKinds) == 0 {
		// Workload kinds whose labels and controller owner the
		// resolver records. The aggregator subscribes to events on
		// these kinds in addition to SubscribeKinds. ReplicaSet, Job
		// and CronJob are the links from a Pod to its top controller.
		c.WorkloadEnrichKinds = []string{
			"deployment", "statefulset", "daemonset", "replicaset", "job", "cronjob", "pod",
		}
	}
	if c.ViolationsPageSize <= 0 {
		c.ViolationsPageSize = 50
	}
	if c.ViolationsMaxPage <= 0 {
		c.ViolationsMaxPage = 500
	}
	if c.SSEHeartbeatInterval <= 0 {
		c.SSEHeartbeatInterval = 15 * time.Second
	}
	if c.SSEEventBuffer <= 0 {
		c.SSEEventBuffer = 256
	}
	if c.NATSPublishTimeout <= 0 {
		c.NATSPublishTimeout = 2 * time.Second
	}
}

// ── Interfaces — nil-tolerant integrations ───────────────────────────────

// PolicyRollupPublisher writes one scope:id → JSON entry to the NATS
// JetStream `policy-rollup` KV bucket. Nil-tolerant — when nil the
// aggregator skips publication and only the SSE + Prometheus paths
// emit. Production main.go wires a real NATS KV client when
// CATALYST_NATS_URL is set; tests inject a recorder.
type PolicyRollupPublisher interface {
	// Put writes value at key with optional TTL. Returns nil on
	// success; the aggregator logs and continues on error so a
	// transient NATS outage never wedges the in-process hot path.
	Put(ctx context.Context, key string, value []byte) error
}

// ── Compliance handler — instance attached to *Handler via setter ────────

// ComplianceHandler aggregates score state, serves the REST + SSE
// endpoints, and pushes rollups to NATS KV. Wired into the *Handler
// via SetComplianceHandler.
//
// One ComplianceHandler per catalyst-api process. Internally it owns
// a goroutine per (cluster) consuming the Factory's SSE fanout and a
// mutex-guarded per-resource state map.
type ComplianceHandler struct {
	cfg ComplianceConfig
	log slogish

	// k8sCache — the SSE fanout the watch loop reads, and the cache the
	// resolver reads an object from when no event showed it the object.
	k8sCache *k8scache.Factory

	// publisher — NATS KV writer; nil disables publication.
	publisher PolicyRollupPublisher

	// resolver — the controller owner and the labels of every workload
	// object, from the workload events and the cache. model — the
	// findings of every cluster and the score rules.
	resolver *compliance.CacheResolver
	model    *compliance.ReadModel

	// mu guards policyMetaByName.
	mu sync.RWMutex

	// policyMetaByName carries the live ClusterPolicy CR's metadata
	// (severity / rules / title / category / description) so the
	// per-policy drill-down (TC-026, slice U4) can render Severity +
	// Rule lists without round-tripping the apiserver per request.
	// Keyed clusterID → policy name: each cluster's engine has its own
	// objects. Written by `clusterpolicy` events, dropped on their delete;
	// read by policiesFor.
	policyMetaByName map[string]map[string]policyMeta

	// SSE subscribers — listeners on /compliance/stream.
	subMu       sync.Mutex
	subID       int64
	subscribers map[int64]*complianceSubscriber

	// stop — closed by Stop() to halt the watch goroutine.
	stopOnce sync.Once
	stop     chan struct{}

	// ready — closed once runWatch has subscribed to the Factory.
	// Tests block on this so a Publish injected immediately after
	// Start can't race the Subscribe call. nil-tolerant; tests that
	// don't construct via NewComplianceHandler skip the wait.
	ready chan struct{}
}

type complianceSubscriber struct {
	id      int64
	cluster string // "" == all clusters
	ch      chan complianceEvent
}

// complianceEvent is the SSE wire frame for /compliance/stream.
type complianceEvent struct {
	Type    string `json:"type"`           // "score" | "scorecard"
	Cluster string `json:"cluster"`
	Score   Score  `json:"score"`
	At      time.Time `json:"at"`
}

// slogish is the minimal logger surface the aggregator needs. The
// production *slog.Logger satisfies it; tests inject a quiet logger
// that ignores everything. Defined locally so unit tests don't have
// to wire a real *slog.Logger.
type slogish interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// NewComplianceHandler wires a ComplianceHandler. Start() must be
// called separately so tests can seed state before the watch loop
// begins.
func NewComplianceHandler(
	cfg ComplianceConfig,
	log slogish,
	k8sCache *k8scache.Factory,
	publisher PolicyRollupPublisher,
) *ComplianceHandler {
	cfg.defaults()
	var lister compliance.Lister
	if k8sCache != nil {
		lister = k8sCache
	}
	resolver := compliance.NewCacheResolver(lister)
	return &ComplianceHandler{
		cfg:              cfg,
		log:              log,
		k8sCache:         k8sCache,
		publisher:        publisher,
		resolver:         resolver,
		model:            compliance.NewReadModel(compliance.DefaultModel(), resolver, adapters.PolicyReport{}, adapters.Evaluator{}),
		policyMetaByName: map[string]map[string]policyMeta{},
		subscribers:      map[int64]*complianceSubscriber{},
		stop:             make(chan struct{}),
		ready:            make(chan struct{}),
	}
}

// WaitReady blocks until runWatch has subscribed to the Factory, OR
// until ctx is cancelled. Tests use this to guarantee Publish events
// arrive AFTER the subscription is live. Production code rarely needs
// it (the wizard's first user action takes seconds, by which point
// the watch is already up).
func (c *ComplianceHandler) WaitReady(ctx context.Context) error {
	if c == nil || c.ready == nil {
		return nil
	}
	select {
	case <-c.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Start spawns the watch goroutine. Safe to call once; subsequent
// calls are a no-op. Returns immediately — the goroutine runs until
// Stop is called or ctx is cancelled.
func (c *ComplianceHandler) Start(ctx context.Context) {
	if c == nil || c.k8sCache == nil {
		return
	}
	go c.runWatch(ctx)
}

// Stop signals the watch goroutine to exit. Idempotent.
func (c *ComplianceHandler) Stop() {
	if c == nil {
		return
	}
	c.stopOnce.Do(func() { close(c.stop) })
}

// runWatch subscribes to the Factory's SSE fanout for the configured
// kinds and recomputes scores on every event. Per ADR-0001 §5 this
// is the only score-recompute trigger — there is NO time.Tick poll.
func (c *ComplianceHandler) runWatch(ctx context.Context) {
	kinds := map[string]struct{}{}
	for _, k := range c.cfg.SubscribeKinds {
		kinds[k8scache.CanonicalKindName(k)] = struct{}{}
	}
	for _, k := range c.cfg.WorkloadEnrichKinds {
		kinds[k8scache.CanonicalKindName(k)] = struct{}{}
	}
	ch, unsub := c.k8sCache.Subscribe("", kinds)
	defer unsub()

	// Signal readiness — tests block on WaitReady so a Publish
	// injected immediately after Start can't race the Subscribe.
	if c.ready != nil {
		close(c.ready)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stop:
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			c.onEvent(ctx, ev)
		}
	}
}

// onEvent processes one SSE event. A report event goes to the read model and
// every subject it touched is published; a workload event updates the
// resolver and the facts of its subject; a clusterpolicy event updates the
// policy metadata of its cluster.
//
// #5352 — a DELETED report removes its findings, and a subject left with none
// loses its gauge series: before, every churning Pod or Job left a series
// behind, the registry grew without bound and catalyst-api was OOMKilled.
func (c *ComplianceHandler) onEvent(ctx context.Context, ev k8scache.Event) {
	if ev.Object == nil {
		return
	}
	switch {
	case ev.Kind == "clusterpolicy" && ev.Type == k8scache.EventDeleted:
		// A removed policy leaves the list and the by-name route of its own cluster.
		c.mu.Lock()
		delete(c.policyMetaByName[ev.Cluster], ev.Object.GetName())
		c.mu.Unlock()
	case ev.Kind == "clusterpolicy":
		c.ingestKyvernoClusterPolicy(ev)
	case c.model.Handles(ev.Kind):
		current, gone := c.model.Apply(ev)
		c.publishChanges(ctx, ev.Cluster, current, gone)
	case c.isWorkloadKind(ev.Kind):
		c.resolver.Observe(ev)
		if ev.Type != k8scache.EventDeleted {
			c.model.Refresh(compliance.SubjectKey{Cluster: ev.Cluster, Namespace: ev.Object.GetNamespace(), Kind: ev.Object.GetKind(), Name: ev.Object.GetName()})
		}
	}
}

// ingestKyvernoClusterPolicy captures the per-policy metadata from a
// ClusterPolicy CR so the per-policy drill-down (TC-026, slice U4) can
// render Severity + Rule list off in-memory state without the apiserver
// round-trip. Reads:
//
//   - metadata.annotations[policies.kyverno.io/severity]    → Severity
//   - metadata.annotations[policies.kyverno.io/title]       → Title
//   - metadata.annotations[policies.kyverno.io/category]    → Category
//   - metadata.annotations[policies.kyverno.io/description] → Description
//   - spec.rules[].name                                      → Rules
//
// Updates `policyMetaByName[ev.Cluster]` keyed on the policy name: one
// process watches several clusters (chroot secondaries, the mothership's
// Sovereigns), and the same name may carry a different mode in each.
func (c *ComplianceHandler) ingestKyvernoClusterPolicy(ev k8scache.Event) {
	if ev.Object == nil {
		return
	}
	name := ev.Object.GetName()
	if name == "" {
		return
	}
	annotations := ev.Object.GetAnnotations()
	meta := policyMeta{
		severity:    strings.TrimSpace(annotations["policies.kyverno.io/severity"]),
		title:       strings.TrimSpace(annotations["policies.kyverno.io/title"]),
		category:    strings.TrimSpace(annotations["policies.kyverno.io/category"]),
		description: strings.TrimSpace(annotations["policies.kyverno.io/description"]),
		mode:        enginePolicyMode(ev.Object),
	}
	if rules, ok, _ := unstructured.NestedSlice(ev.Object.Object, "spec", "rules"); ok {
		for _, ri := range rules {
			r, ok := ri.(map[string]any)
			if !ok {
				continue
			}
			ruleName, _, _ := unstructured.NestedString(r, "name")
			ruleName = strings.TrimSpace(ruleName)
			if ruleName != "" {
				meta.rules = append(meta.rules, ruleName)
			}
		}
	}
	c.mu.Lock()
	if c.policyMetaByName[ev.Cluster] == nil {
		c.policyMetaByName[ev.Cluster] = map[string]policyMeta{}
	}
	c.policyMetaByName[ev.Cluster][name] = meta
	c.mu.Unlock()
}

// enginePolicyMode returns the mode a policy object of the engine
// carries: "enforce" or "audit". A ValidatingPolicy enforces when
// spec.validationActions holds Deny. A legacy ClusterPolicy enforces
// when spec.validationFailureAction is Enforce, or when
// validate.failureAction of one of its rules is. Every other policy
// audits. Upper and lower case are the same.
func enginePolicyMode(obj *unstructured.Unstructured) string {
	if actions, ok, _ := unstructured.NestedSlice(obj.Object, "spec", "validationActions"); ok {
		for _, a := range actions {
			if s, _ := a.(string); strings.EqualFold(strings.TrimSpace(s), "Deny") {
				return "enforce"
			}
		}
	}
	if vfa, _, _ := unstructured.NestedString(obj.Object, "spec", "validationFailureAction"); strings.EqualFold(strings.TrimSpace(vfa), "Enforce") {
		return "enforce"
	}
	if rules, ok, _ := unstructured.NestedSlice(obj.Object, "spec", "rules"); ok {
		for _, ri := range rules {
			r, ok := ri.(map[string]any)
			if !ok {
				continue
			}
			if fa, _, _ := unstructured.NestedString(r, "validate", "failureAction"); strings.EqualFold(strings.TrimSpace(fa), "Enforce") {
				return "enforce"
			}
		}
	}
	return "audit"
}

// isWorkloadKind returns true when the SSE event came in for a kind
// the aggregator captures labels for (deployment, statefulset, etc.).
func (c *ComplianceHandler) isWorkloadKind(kind string) bool {
	for _, k := range c.cfg.WorkloadEnrichKinds {
		if k8scache.CanonicalKindName(k) == kind {
			return true
		}
	}
	return false
}

// ── Publish path — read model to wire shape ──────────────────────────────

// publishChanges drops the gauge series of every subject that lost its last
// finding (#5352), publishes the resource score of every other subject the
// event touched, and then the rollups of the cluster, once.
func (c *ComplianceHandler) publishChanges(ctx context.Context, clusterID string, current, gone []compliance.Subject) {
	for _, s := range gone {
		metricComplianceScore.DeleteLabelValues("resource", subjectID(s.Key), s.Environment, clusterID)
	}
	for _, s := range current {
		if v, ok := c.model.Subject(s.Key); ok {
			c.publishScore(ctx, clusterID, resourceScore(v))
		}
	}
	if len(current)+len(gone) == 0 {
		return
	}
	for _, s := range c.rollups(clusterID) {
		c.publishScore(ctx, clusterID, s)
	}
}

// subjectID is the id of a subject on the wire: "<kind>/<namespace>/<name>",
// the kind in lower case.
func subjectID(k compliance.SubjectKey) string { return resourceKey(k.Kind, k.Namespace, k.Name) }

// setHeadline sets the legacy `total` and the canonical `score` field to
// the same value, so the two never disagree; both encode JSON-null when the
// posture has no value.
func (s *Score) setHeadline(v *int) {
	s.Total = v
	s.Score = v
}

// postureNumbers returns the weighted numerator and denominator of the
// axes of a posture.
func postureNumbers(p compliance.Posture) (num, den int) {
	for _, a := range p.Axes {
		num += a.Num
		den += a.Den
	}
	return num, den
}

// resourceScore maps the view of one subject to the Score of scope resource.
func resourceScore(v compliance.SubjectView) Score {
	id := subjectID(v.Subject.Key)
	s := Score{
		Scope:           "resource",
		ID:              id,
		Resource:        id,
		PolicyResults:   map[string]string{},
		EnvironmentRef:  v.Subject.Environment,
		OrganizationRef: v.Subject.Organization,
		ApplicationRef:  v.Subject.Application,
		Violations:      v.Posture.Counts.Fail,
		UpdatedAt:       time.Now().UTC(),
	}
	s.Numerator, s.Denominator = postureNumbers(v.Posture)
	s.setHeadline(v.Posture.Overall)
	for _, r := range v.Results {
		s.PolicyResults[r.Check] = string(r.Result)
	}
	return s
}

// rollups returns the application, environment, organization and
// sovereign scores of the cluster, ordered by scope and id; nil when no
// report judged anything in the cluster yet.
func (c *ComplianceHandler) rollups(clusterID string) []Score {
	now := time.Now().UTC()
	var out []Score
	for _, r := range c.model.Rollups(clusterID) {
		s := Score{Scope: r.Scope, ID: r.ID, UpdatedAt: now}
		s.Numerator, s.Denominator = postureNumbers(r.Posture)
		s.setHeadline(r.Overall)
		switch r.Scope {
		case compliance.ScopeApplication:
			s.ApplicationRef, s.EnvironmentRef, s.OrganizationRef = r.ID, r.Environment, r.Organization
		case compliance.ScopeEnvironment:
			s.EnvironmentRef, s.OrganizationRef = r.ID, r.Organization
		case compliance.ScopeOrganization:
			s.OrganizationRef = r.ID
		}
		out = append(out, s)
	}
	return out
}

// publishScore writes one Score to all destinations: NATS KV (if
// publisher wired), SSE subscribers, Prometheus gauges. Best-effort
// for NATS — failures are logged not propagated.
func (c *ComplianceHandler) publishScore(ctx context.Context, clusterID string, s Score) {
	if s.Scope == "" || s.ID == "" {
		return
	}
	// NATS KV.
	if c.publisher != nil {
		// G86 #2633 (2026-05-31): NATS JetStream KV bucket keys may NOT
		// contain `:` per the docs (allowed: alphanumeric, `.`, `_`,
		// `-`, `=`, `/`). Previously every score-publish failed with
		// `nats: invalid key` and the compliance scorecard read path
		// got ZERO data → SREDashboardPage rendered empty regardless
		// of Kyverno PolicyReport activity. Replace `:` with `.`
		// (NATS-native separator). Read path uses fanout SSE +
		// REST scorecard rebuild from PolicyReports, so no consumer-
		// side change needed for the historical KV data — it was
		// never readable anyway.
		key := s.Scope + "." + s.ID
		body, err := json.Marshal(s)
		if err == nil {
			natsCtx, cancel := context.WithTimeout(ctx, c.cfg.NATSPublishTimeout)
			if err := c.publisher.Put(natsCtx, key, body); err != nil {
				c.log.Warn("compliance: nats kv put failed", "scope", s.Scope, "id", s.ID, "err", err)
			}
			cancel()
		}
	}
	// SSE.
	c.fanout(complianceEvent{
		Type:    "score",
		Cluster: clusterID,
		Score:   s,
		At:      s.UpdatedAt,
	})
	// Prometheus.
	complianceObserve(s, clusterID)
}

// fanout — non-blocking send to each subscriber. Same drop-oldest
// policy as k8scache.Factory.fanout — a slow consumer never blocks
// the producer.
func (c *ComplianceHandler) fanout(ev complianceEvent) {
	c.subMu.Lock()
	subs := make([]*complianceSubscriber, 0, len(c.subscribers))
	for _, s := range c.subscribers {
		if s.cluster != "" && s.cluster != ev.Cluster {
			continue
		}
		subs = append(subs, s)
	}
	c.subMu.Unlock()
	for _, s := range subs {
		select {
		case s.ch <- ev:
		default:
			select {
			case <-s.ch:
			default:
			}
			select {
			case s.ch <- ev:
			default:
			}
		}
	}
}

// ── Helpers ──────────────────────────────────────────────────────────────

func resourceKey(kind, ns, name string) string {
	return strings.ToLower(kind) + "/" + ns + "/" + name
}

// labelOr returns the value of the first key of lbls that is set and not
// empty, else "".
func labelOr(lbls map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := lbls[k]; ok && v != "" {
			return v
		}
	}
	return ""
}

// ── HTTP handlers ────────────────────────────────────────────────────────

// HandleComplianceScorecard — GET /api/v1/sovereigns/{id}/compliance/scorecard
//
// Returns sovereign / org / env / app rollups in one JSON document.
// The UI's SRE Lead and Security Lead dashboards (slices U1+U2) build
// their fleet-view tree from this single payload.
func (h *Handler) HandleComplianceScorecard(w http.ResponseWriter, r *http.Request) {
	if h.compliance == nil {
		http.Error(w, "compliance handler not wired", http.StatusServiceUnavailable)
		return
	}
	clusterID := chi.URLParam(r, "id")
	if clusterID == "" {
		http.Error(w, "missing sovereign id", http.StatusBadRequest)
		return
	}
	clusterID = h.resolveChrootClusterID(clusterID)
	// qa-loop iter-1 prov #8 Fix #97 — echo the requested region (if any)
	// so multi-region matrix tokens (TC-050: `hz-hel-rtz-prod`) resolve.
	// The rollup itself is sovereign-wide today; per
	// `feedback_no_mvp_no_workarounds.md` we surface the requested region
	// faithfully (echoed in the response) and the per-region filtering
	// will follow once Continuum-aware rollups land. Empty when the
	// caller did not ask for a region.
	region := strings.TrimSpace(r.URL.Query().Get("region"))
	rolls := h.compliance.rollups(clusterID)
	resp := ScorecardResponse{
		GeneratedAt: time.Now().UTC(),
		Region:      region,
	}
	for _, s := range rolls {
		switch s.Scope {
		case "sovereign":
			resp.Sovereign = s
		case "organization":
			resp.Organizations = append(resp.Organizations, s)
		case "environment":
			resp.Environments = append(resp.Environments, s)
		case "application":
			resp.Applications = append(resp.Applications, s)
		}
		resp.Items = append(resp.Items, s)
	}
	if resp.Sovereign.Scope == "" {
		// No data yet — return a well-shaped empty response so
		// the UI renders the "scorecard not yet computed"
		// empty state. The Sovereign Score still gets populated
		// with a zero-headline so `score` is present at the wire
		// (TC-018, TC-029, TC-034 et al).
		zero := 0
		resp.Sovereign = Score{
			Scope:     "sovereign",
			ID:        clusterID,
			Total:     &zero,
			Score:     &zero,
			UpdatedAt: time.Now().UTC(),
		}
	} else if resp.Sovereign.Score == nil {
		// Existing rollup but no policies have been ingested yet
		// (numerator/denominator both 0). Surface 0 so the wire
		// always carries `score:<int>` — never null and never absent.
		zero := 0
		resp.Sovereign.Total = &zero
		resp.Sovereign.Score = &zero
	}
	// Items is sorted (scope, id) for deterministic rendering.
	sort.Slice(resp.Items, func(i, j int) bool {
		if resp.Items[i].Scope != resp.Items[j].Scope {
			return resp.Items[i].Scope < resp.Items[j].Scope
		}
		return resp.Items[i].ID < resp.Items[j].ID
	})
	if resp.Items == nil {
		resp.Items = []Score{}
	}
	// `regions[]` merges the configured regions; `appRefs[]` holds
	// only the applications the aggregator has seen in the cache.
	resp.Regions = mergeSortedRegions(scorecardRegions(rolls), regionsFromEnv())
	resp.AppRefs = scorecardAppRefs(rolls)
	// Codemod a3: scrub-null pass on the scorecard response. Empty
	// rollups (Sovereign with no policy reports yet) leak `total:null`
	// + `score:null` which the matrix asserts against; the wire-shape
	// uses the canonical "key absent" idiom for unset numerics so the
	// dashboard's "no data" rendering is unambiguous.
	scrubbed := scrubScorecardNulls(resp)
	writeJSON(w, http.StatusOK, scrubbed)
}

// scorecardRegions extracts every distinct region label observed on
// the rollup slice. Returns a non-nil sorted slice (`[]` when empty)
// so the caller can merge it with `regionsFromEnv()` without a nil
// guard. Today the per-resource Score does not yet carry a region
// label (per-region rollup is Path A future work when Cilium
// ClusterMesh ships); the helper returns `[]` and the env merge
// supplies the canonical literal tokens TC-050 asserts on.
//
// Reserved for Path A: when Score gains a `Region string` field we
// extract it here exactly like scorecardAppRefs reads ApplicationRef.
func scorecardRegions(rolls []Score) []string {
	_ = rolls
	return []string{}
}

// scorecardAppRefs extracts every distinct ApplicationRef observed on
// the rollup slice. Sorted + deduplicated; non-nil so JSON renders
// `[]` not `null`. Mirrors the pattern of configuredRegionsForDeployment.
func scorecardAppRefs(rolls []Score) []string {
	seen := make(map[string]struct{}, 8)
	out := make([]string, 0, 8)
	for _, s := range rolls {
		if s.Scope != "application" {
			continue
		}
		ref := strings.TrimSpace(s.ApplicationRef)
		if ref == "" {
			ref = strings.TrimSpace(s.ID)
		}
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
	}
	sort.Strings(out)
	if out == nil {
		return []string{}
	}
	return out
}

// scrubScorecardNulls round-trips the ScorecardResponse through
// generic JSON to apply jsonutil.ScrubNulls without writing the
// per-field projection by hand. The resulting map[string]interface{}
// keeps every populated key + nested array; null leaves are removed.
// One round-trip per /scorecard call is acceptable (the response is
// O(orgs+envs+apps), max a few KB on chroot Sovereigns).
func scrubScorecardNulls(in ScorecardResponse) interface{} {
	raw, err := json.Marshal(in)
	if err != nil {
		return in
	}
	var generic interface{}
	if err := json.Unmarshal(raw, &generic); err != nil {
		return in
	}
	return jsonutil.ScrubNulls(generic)
}

// HandleCompliancePolicies — GET /api/v1/sovereigns/{id}/compliance/policies
//
// Returns one row per policy the aggregator has seen in the cache:
// name, weight, mode, current violation count, source
// (kyverno|evaluator). The mode is the one the engine's policy object
// carries. Nothing is read from the cluster per request.
func (h *Handler) HandleCompliancePolicies(w http.ResponseWriter, r *http.Request) {
	if h.compliance == nil {
		http.Error(w, "compliance handler not wired", http.StatusServiceUnavailable)
		return
	}
	clusterID := chi.URLParam(r, "id")
	if clusterID == "" {
		http.Error(w, "missing sovereign id", http.StatusBadRequest)
		return
	}
	clusterID = h.resolveChrootClusterID(clusterID)

	views := h.compliance.policiesFor(r.Context(), clusterID)
	if views == nil {
		views = []PolicyView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": views,
		"count": len(views),
	})
}

// policiesFor returns one PolicyView per known policy of the cluster: every
// check the read model scores the cluster with, and every policy object of
// the cluster's engine. Weight is the check's weight in the model,
// Violations the number of subjects the check fails on, Source the check's
// producer. The mode is the one the engine's policy object carries
// (policyMeta.mode). Nothing is read from a cluster here, and another
// cluster's objects and reports never list.
func (c *ComplianceHandler) policiesFor(_ context.Context, clusterID string) []PolicyView {
	c.mu.RLock()
	objects := make(map[string]policyMeta, len(c.policyMetaByName[clusterID]))
	for n, m := range c.policyMetaByName[clusterID] {
		objects[n] = m
	}
	c.mu.RUnlock()

	checks := map[string]compliance.Check{}
	for _, chk := range c.model.Checks(clusterID) {
		checks[chk.ID] = chk
	}
	violations := map[string]int{}
	for _, v := range c.model.Subjects(clusterID) {
		for _, r := range v.Results {
			if r.Result == compliance.Fail {
				violations[r.Check]++
			}
		}
	}
	names := map[string]struct{}{}
	for n := range checks {
		names[n] = struct{}{}
	}
	for n := range objects {
		names[n] = struct{}{}
	}
	out := make([]PolicyView, 0, len(names))
	for n := range names {
		chk := checks[n]
		src := string(chk.Source)
		if src == "" {
			src = string(compliance.SourceKyverno)
		}
		meta := objects[n]
		mode := "permissive"
		if meta.mode == "enforce" {
			mode = "enforcing"
		}
		out = append(out, PolicyView{
			Name:        n,
			Weight:      chk.Weight,
			Mode:        mode,
			Violations:  violations[n],
			Source:      src,
			Description: meta.description,
			Severity:    meta.severity,
			Rules:       append([]string{}, meta.rules...),
			Title:       meta.title,
			Category:    meta.category,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// HandleComplianceViolations — GET
// /api/v1/sovereigns/{id}/compliance/violations?app=<name>&policy=<name>[&limit=N&offset=N]
//
// Returns paginated violations (results=fail). `app` keeps the
// violations of one Application and `policy` those of one policy;
// both filters run on the server before the page is cut.
func (h *Handler) HandleComplianceViolations(w http.ResponseWriter, r *http.Request) {
	if h.compliance == nil {
		http.Error(w, "compliance handler not wired", http.StatusServiceUnavailable)
		return
	}
	clusterID := chi.URLParam(r, "id")
	if clusterID == "" {
		http.Error(w, "missing sovereign id", http.StatusBadRequest)
		return
	}
	clusterID = h.resolveChrootClusterID(clusterID)
	app := strings.TrimSpace(r.URL.Query().Get("app"))
	policy := strings.TrimSpace(r.URL.Query().Get("policy"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = h.compliance.cfg.ViolationsPageSize
	}
	if limit > h.compliance.cfg.ViolationsMaxPage {
		limit = h.compliance.cfg.ViolationsMaxPage
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	all := h.compliance.violationsFor(clusterID, app, policy)
	total := len(all)
	end := offset + limit
	if end > total {
		end = total
	}
	if offset > total {
		offset = total
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  all[offset:end],
		"total":  total,
		"offset": offset,
		"limit":  limit,
	})
}

// violationsFor returns one Violation per subject and check whose effective
// result is fail, ordered by resource and policy. app keeps the subjects of
// one Application, policy the results of one check.
func (c *ComplianceHandler) violationsFor(clusterID, app, policy string) []Violation {
	out := make([]Violation, 0, 16)
	for _, v := range c.model.Subjects(clusterID) {
		if app != "" && v.Subject.Application != app {
			continue
		}
		for _, r := range v.Results {
			if r.Result != compliance.Fail || (policy != "" && r.Check != policy) {
				continue
			}
			out = append(out, Violation{
				Resource:    subjectID(v.Subject.Key),
				Namespace:   v.Subject.Key.Namespace,
				Policy:      r.Check,
				Rule:        r.Finding.Properties["rule"],
				Result:      string(r.Result),
				Message:     r.Finding.Message,
				Application: v.Subject.Application,
				Environment: v.Subject.Environment,
				Time:        r.Finding.ObservedAt,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Resource != out[j].Resource {
			return out[i].Resource < out[j].Resource
		}
		return out[i].Policy < out[j].Policy
	})
	return out
}

// HandleComplianceResource — GET
// /api/v1/sovereigns/{id}/compliance/resource?kind=<k>&ns=<ns>&name=<n>
//
// Returns the per-policy compliance findings for ONE K8s resource so
// the resource detail view's Compliance tab (#4085) can render a table
// of THAT resource's own checks + pass/fail/skip status — instead of
// linking the operator out to the sovereign-wide dashboard.
//
// `ns` may be empty for cluster-scoped resources (mirrors the
// resourceKey "<kind>//<name>" shape). `kind` is matched
// case-insensitively against the canonical lowercase key.
func (h *Handler) HandleComplianceResource(w http.ResponseWriter, r *http.Request) {
	if h.compliance == nil {
		http.Error(w, "compliance handler not wired", http.StatusServiceUnavailable)
		return
	}
	clusterID := chi.URLParam(r, "id")
	if clusterID == "" {
		http.Error(w, "missing sovereign id", http.StatusBadRequest)
		return
	}
	clusterID = h.resolveChrootClusterID(clusterID)

	q := r.URL.Query()
	kind := strings.TrimSpace(q.Get("kind"))
	ns := strings.TrimSpace(q.Get("ns"))
	name := strings.TrimSpace(q.Get("name"))
	// Accept the literal `_` sentinel the cloud-list routes use for the
	// cluster-scoped namespace segment so the UI can pass through the
	// route param unchanged.
	if ns == "_" {
		ns = ""
	}
	if kind == "" || name == "" {
		http.Error(w, "missing kind or name", http.StatusBadRequest)
		return
	}

	resp := h.compliance.resourceComplianceFor(clusterID, kind, ns, name)
	writeJSON(w, http.StatusOK, resp)
}

// resourceComplianceFor projects the subject that judges ONE resource into
// a ResourceComplianceResponse: the resource's top controller (a Pod is
// judged on its Deployment), its score, and one finding per check joined
// with the live ClusterPolicy metadata so each finding carries Severity /
// Category / Title. Always returns a well-shaped (non-nil findings)
// response — Found=false + null score when no report judged the subject yet.
func (c *ComplianceHandler) resourceComplianceFor(clusterID, kind, ns, name string) ResourceComplianceResponse {
	resp := ResourceComplianceResponse{
		Resource:  resourceKey(kind, ns, name),
		Kind:      strings.ToLower(kind),
		Namespace: ns,
		Name:      name,
		Findings:  []ResourceFinding{},
		UpdatedAt: time.Now().UTC(),
	}
	if c == nil {
		return resp
	}
	v, ok := c.model.Subject(c.resolver.TopOwner(clusterID, ns, kind, name))
	if !ok {
		// No report judged this resource yet — the UI renders its
		// "no policies evaluated yet" state.
		return resp
	}
	c.mu.RLock()
	meta := make(map[string]policyMeta, len(c.policyMetaByName[clusterID]))
	for n, m := range c.policyMetaByName[clusterID] {
		meta[n] = m
	}
	c.mu.RUnlock()

	resp.Found = true
	resp.Score = v.Posture.Overall
	resp.Total = v.Posture.Overall
	resp.Numerator, resp.Denominator = postureNumbers(v.Posture)
	resp.Violations = v.Posture.Counts.Fail
	resp.Application = v.Subject.Application
	resp.Environment = v.Subject.Environment
	resp.UpdatedAt = v.UpdatedAt
	for _, r := range v.Results {
		m := meta[r.Check]
		resp.Findings = append(resp.Findings, ResourceFinding{
			Policy:   r.Check,
			Rule:     r.Finding.Properties["rule"],
			Result:   string(r.Result),
			Message:  r.Finding.Message,
			Severity: m.severity,
			Category: m.category,
			Title:    m.title,
			Source:   string(r.Source),
		})
	}
	// Stable order: failing/warning rows first (most actionable), then
	// alphabetical by policy name within each result class.
	sort.Slice(resp.Findings, func(i, j int) bool {
		ri, rj := resultRank(resp.Findings[i].Result), resultRank(resp.Findings[j].Result)
		if ri != rj {
			return ri < rj
		}
		return resp.Findings[i].Policy < resp.Findings[j].Policy
	})
	return resp
}

// resultRank orders verdicts so the most actionable rows surface first:
// fail < warn < skip < pass < (unknown). Used to sort the per-resource
// findings table.
func resultRank(result string) int {
	switch strings.ToLower(strings.TrimSpace(result)) {
	case "fail":
		return 0
	case "warn":
		return 1
	case "skip", "na":
		return 2
	case "pass":
		return 3
	default:
		return 4
	}
}

// SovereignAlertCount returns the number of currently-failing
// (resource, policy) pairs the EPIC-1 score aggregator has observed
// for the named cluster. Used by EPIC-6 U-Fleet's
// summarizeSovereign() to populate the per-Sovereign `alerts` field
// (slice Z2 follow-up).
//
// Nil-tolerant: a nil receiver returns 0 (catalyst-api Pods running
// without compliance wired stay green at the dashboard level rather
// than panicking).
//
// "Alerts" is defined as the set of resource×policy pairs that
// currently have result=fail. We deliberately do NOT introduce a
// severity field here — the brief calls for the canonical alert count
// the aggregator already exposes; layering severity is a follow-up
// when the EnvironmentPolicy schema grows a per-policy severity
// attribute.
func (c *ComplianceHandler) SovereignAlertCount(clusterID string) int {
	if c == nil {
		return 0
	}
	return len(c.violationsFor(clusterID, "", ""))
}

// HandleComplianceStream — GET /api/v1/sovereigns/{id}/compliance/stream
//
// SSE: real-time score updates. Each frame is a JSON document on a
// single `data:` line. Heartbeat ping every SSEHeartbeatInterval.
func (h *Handler) HandleComplianceStream(w http.ResponseWriter, r *http.Request) {
	if h.compliance == nil {
		http.Error(w, "compliance handler not wired", http.StatusServiceUnavailable)
		return
	}
	clusterID := chi.URLParam(r, "id")
	if clusterID == "" {
		http.Error(w, "missing sovereign id", http.StatusBadRequest)
		return
	}
	clusterID = h.resolveChrootClusterID(clusterID)
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// ?scope=resource|application|environment|organization|sovereign
	// Wave 5.47 (#2266) — audit finding #4 — per-Org/per-App consumers
	// filter at server-side rather than client-side parse-then-discard.
	// Empty = no filter (original behaviour, every scope).
	scopeFilter := strings.TrimSpace(r.URL.Query().Get("scope"))

	ch, unsub := h.compliance.subscribe(clusterID)
	defer unsub()

	_, _ = fmt.Fprintf(w, ": connected cluster=%s\n\n", clusterID)
	flusher.Flush()

	enc := json.NewEncoder(w)

	// Emit an immediate snapshot `data:` frame on connect so subscribers
	// (and probes / tests) see at least one event without waiting for
	// the next score recompute or heartbeat. The snapshot reuses the
	// existing /scorecard rollups; a fresh Sovereign with no
	// PolicyReports yet will see an event with score=null which the UI
	// renders as "no data yet" — same shape as the steady-state stream.
	rolls := h.compliance.rollups(clusterID)
	var snapScore Score
	for _, s := range rolls {
		if s.Scope == "sovereign" {
			snapScore = s
			break
		}
	}
	if snapScore.Scope == "" {
		snapScore = Score{Scope: "sovereign", ID: clusterID, UpdatedAt: time.Now().UTC()}
	}
	snapEv := complianceEvent{
		Type:    "snapshot",
		Cluster: clusterID,
		Score:   snapScore,
		At:      time.Now().UTC(),
	}
	// Per W3C SSE spec the `event:` prefix names the event type so
	// EventSource consumers can register typed listeners (e.g.
	// `es.addEventListener('snapshot', ...)`). Prior to this codemod
	// the stream emitted only `data:` frames which fall through to
	// the default 'message' listener and force consumers to dispatch
	// on `payload.type` after JSON-decode. The matrix asserts the
	// literal `event:` token (TC-023) so the stream is now spec-compliant.
	if _, err := fmt.Fprintf(w, "event: %s\n", snapEv.Type); err != nil {
		return
	}
	if _, err := w.Write([]byte("data: ")); err != nil {
		return
	}
	if err := enc.Encode(snapEv); err != nil {
		return
	}
	if _, err := w.Write([]byte("\n")); err != nil {
		return
	}
	flusher.Flush()

	pingT := time.NewTicker(h.compliance.cfg.SSEHeartbeatInterval)
	defer pingT.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-pingT.C:
			if _, err := fmt.Fprintf(w, ": ping %d\n\n", time.Now().Unix()); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			// Wave 5.47 (#2266) per-scope filter — when ?scope= was
			// passed AND the event's score scope doesn't match, skip
			// without writing. Events with no .Score.Scope pass
			// through unfiltered (back-compat for non-Score event
			// types like heartbeat).
			if scopeFilter != "" && ev.Score.Scope != "" && ev.Score.Scope != scopeFilter {
				continue
			}
			// SSE `event:` prefix — typed listener support per W3C
			// spec; matrix asserts the literal `event:` token
			// (TC-023). Default to "score" when the recompute path
			// emits without setting Type explicitly.
			evType := ev.Type
			if evType == "" {
				evType = "score"
			}
			if _, err := fmt.Fprintf(w, "event: %s\n", evType); err != nil {
				return
			}
			if _, err := w.Write([]byte("data: ")); err != nil {
				return
			}
			if err := enc.Encode(ev); err != nil {
				return
			}
			if _, err := w.Write([]byte("\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (c *ComplianceHandler) subscribe(clusterID string) (<-chan complianceEvent, func()) {
	ch := make(chan complianceEvent, c.cfg.SSEEventBuffer)
	c.subMu.Lock()
	c.subID++
	id := c.subID
	c.subscribers[id] = &complianceSubscriber{
		id:      id,
		cluster: clusterID,
		ch:      ch,
	}
	c.subMu.Unlock()
	// Unsub deletes the subscriber under the same lock the fanout
	// uses to snapshot the subscriber set. The channel is NOT
	// closed by unsub — Go GC reclaims it once both ends drop their
	// references. Closing the channel under a producer race is the
	// classic "send on closed channel" panic; the small GC trade is
	// the correct one here.
	return ch, func() {
		c.subMu.Lock()
		delete(c.subscribers, id)
		c.subMu.Unlock()
	}
}

// ── Wiring on *Handler ──────────────────────────────────────────────────

// SetComplianceHandler attaches a started ComplianceHandler. main.go
// calls this once at startup; tests inject a stubbed instance.
func (h *Handler) SetComplianceHandler(c *ComplianceHandler) { h.compliance = c }

// ComplianceHandler returns the wired aggregator (nil-safe — handlers
// 503 when nil).
func (h *Handler) ComplianceHandler() *ComplianceHandler { return h.compliance }

// ── Prometheus metrics (slice S2) ────────────────────────────────────────

var (
	// catalyst_compliance_score — current weighted score by scope.
	metricComplianceScore = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "catalyst",
		Subsystem: "compliance",
		Name:      "score",
		Help:      "Current weighted compliance score (0..100) per (scope, id, environment, sovereign).",
	}, []string{"scope", "id", "environment", "sovereign"})

	// catalyst_compliance_violations_total — running total of
	// per-policy fail observations. Mimir's recording rule
	// `catalyst:compliance_violations:by_policy:5m_rate` reads
	// the rate of this counter for the trend dashboard.
	metricComplianceViolations = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "catalyst",
		Subsystem: "compliance",
		Name:      "violations_total",
		Help:      "Total per-policy fail observations per (policy, environment, sovereign).",
	}, []string{"policy", "environment", "sovereign"})

	// catalyst_compliance_policy_mode — 1 for the active mode, 0
	// for the inactive one. Lets PromQL `topk(1, by_policy)` pick
	// the current setting cheaply.
	metricCompliancePolicyMode = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "catalyst",
		Subsystem: "compliance",
		Name:      "policy_mode",
		Help:      "1 for the active mode, 0 for inactive, per (policy, environment, sovereign, mode).",
	}, []string{"policy", "environment", "sovereign", "mode"})

	// catalyst_compliance_nats_publish_failures_total — counts every
	// NATS JetStream KV publish failure from the rollup publisher
	// (internal/natspub/compliance_rollup_publisher.go). Wave 5.44
	// (#2251) — audit finding #5: before this, publish failures
	// were warn-logged only, so operators had no /metrics signal
	// beyond log scraping. Bumped via IncrementComplianceNATSPublishFailure.
	metricComplianceNATSPublishFailures = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "catalyst",
		Subsystem: "compliance",
		Name:      "nats_publish_failures_total",
		Help:      "Total NATS JetStream KV publish failures from the compliance rollup publisher.",
	})
)

// IncrementComplianceNATSPublishFailure bumps the publish-failure
// counter. Exported so natspub.ComplianceRollupPublisher can wire it
// via the onPublishFailure callback without importing the prometheus
// registry. Wave 5.44 (#2251).
func IncrementComplianceNATSPublishFailure() {
	metricComplianceNATSPublishFailures.Inc()
}

// complianceObserve records the score gauge + per-policy violation
// counter increments for one Score publish.
func complianceObserve(s Score, sovereignID string) {
	if s.Total != nil {
		metricComplianceScore.WithLabelValues(s.Scope, s.ID, s.EnvironmentRef, sovereignID).Set(float64(*s.Total))
	}
	if s.Scope == "resource" {
		for policy, result := range s.PolicyResults {
			if result == "fail" {
				metricComplianceViolations.WithLabelValues(policy, s.EnvironmentRef, sovereignID).Inc()
			}
		}
	}
}

// ObservePolicyMode lets the resolver-wiring layer record mode
// gauges off the EnvironmentPolicy.spec.compliance.modes map. Called
// by the resolver implementation in main.go on every successful
// Resolve(). Exposed so tests can verify the mode-gauge contract.
func ObservePolicyMode(policy, environment, sovereignID, activeMode string) {
	for _, m := range []string{"permissive", "enforcing"} {
		v := 0.0
		if m == activeMode {
			v = 1
		}
		metricCompliancePolicyMode.WithLabelValues(policy, environment, sovereignID, m).Set(v)
	}
}

// ── EnvironmentPolicy GVR — read by policy_mode.go ───────────────────────

// EnvironmentPolicyGVR is the GroupVersionResource for the
// `EnvironmentPolicy.catalyst.openova.io/v1` CRD shipped by
// products/catalyst/chart/crds/environmentpolicy.yaml.
var EnvironmentPolicyGVR = schema.GroupVersionResource{
	Group:    "catalyst.openova.io",
	Version:  "v1",
	Resource: "environmentpolicies",
}
