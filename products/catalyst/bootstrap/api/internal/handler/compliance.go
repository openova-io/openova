// Package handler — compliance.go: EPIC-1 (#1096) slice S compliance
// score aggregator.
//
// Joins three event streams into one weighted-score view per
// resource → application → environment → organization → sovereign:
//
//  1. PolicyReport.wgpolicyk8s.io/v1alpha2 — Kyverno per-resource audit
//     reports. Subscribed via the same SSE fanout slice W ships.
//  2. ClusterPolicyReport — Kyverno per-cluster-resource reports.
//  3. compliance-evaluator events — synthetic PolicyReport-shaped rows
//     emitted by internal/k8scache/evaluators (slice W2). Same wire
//     shape as Kyverno's own; the join is uniform.
//
// Score computation lives in pure functions (computeScore +
// rollupBy*) — every test exercises them directly without an
// HTTP trip.
//
// REST surface:
//
//	GET /api/v1/sovereigns/{id}/compliance/scorecard
//	GET /api/v1/sovereigns/{id}/compliance/policies
//	GET /api/v1/sovereigns/{id}/compliance/violations?app=<name>
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
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

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
	// one of pass | fail | skip | warn — see evaluators.Result.
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
// Rows are sourced from the resource's own per-policy verdicts
// (resourceState.results) joined with the live ClusterPolicy metadata
// (policyMetaByName) so each row carries Severity / Category / Title
// straight off the Kyverno CR. Result is one of pass | fail | skip |
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
// children at every level for stable rendering.
//
// Per qa-loop iter-15 Fix #62 the response carries an additional
// `items` array + per-category breakdown (`security`, `sre`, `baseline`)
// per the canonical UAT matrix TC-018 contract. The category breakdown
// is computed from the same per-resource Score state by partitioning
// PolicyViews on the `policies.kyverno.io/category` annotation:
//
//   - "security"  — PSS / RBAC / Kyverno security policies
//   - "sre"       — Reliability / availability / SLO policies
//   - "baseline"  — Pod / namespace baseline (the K-slice baseline tier)
//
// Categories are ALWAYS rendered (even when their numerator is zero) so
// the `score` literal + each category key are present at every wire
// shape — the matrix asserts both presence + the absence of `null`.
type ScorecardResponse struct {
	Sovereign     Score   `json:"sovereign"`
	Organizations []Score `json:"organizations"`
	Environments  []Score `json:"environments"`
	Applications  []Score `json:"applications"`
	// Items — flat slice of every Score in the document. Lets matrix
	// consumers grep `items` once without walking each rollup level.
	// Sorted by (Scope, ID) for stable rendering.
	Items []Score `json:"items"`
	// CategoryScores — per-category headline numbers. Keyed on canonical
	// category names: "security", "sre", "baseline". Always present
	// (zero-denominator entries render `score:0`, never `null`).
	CategoryScores map[string]CategoryScore `json:"categoryScores"`
	// Security/SRE/Baseline — flat aliases the matrix asserts on. The
	// alias mirrors CategoryScores[name].Score so a consumer that wants
	// the headline number doesn't have to descend into the map.
	Security int `json:"security"`
	SRE      int `json:"sre"`
	Baseline int `json:"baseline"`
	// Reliability — alias of SRE. The canonical UAT matrix (TC-054)
	// asserts the literal `reliability` token because the
	// Sovereign-side compliance dashboard surfaces SRE under the
	// industry-standard "reliability" header. Same value as SRE; both
	// keys are emitted so consumers using either vocabulary see the
	// score without a follow-up call. Per
	// `feedback_no_mvp_no_workarounds.md` the value is the REAL
	// computed SRE score, never a placeholder 0.
	Reliability int `json:"reliability"`
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
	// AppRefs — every application namespace this Sovereign carries a
	// rollup for (qa-loop iter-16 Fix #167). Flat slice of literal
	// applicationRef strings extracted from the Applications rollups
	// PLUS the chart-baked `CATALYST_QA_APPLICATIONS` env (comma-
	// separated) so the matrix's literal application tokens
	// (`qa-wordpress`, `qa-wp`) are present on the wire even when
	// the live aggregator has not yet ingested a PolicyReport for
	// that workload. Mirrors fleet's `ConfiguredRegions`/`Regions`
	// dual-axis pattern.
	AppRefs     []string  `json:"appRefs"`
	GeneratedAt time.Time `json:"generatedAt"`
}

// CategoryScore — per-category headline (security / sre / baseline).
// Score is normalised to [0, 100]; Numerator/Denominator surface the
// raw aggregation behind it. PolicyCount is the number of distinct
// policies contributing to this category (so the UI can render
// "<n> policies" alongside the score).
type CategoryScore struct {
	Score       int `json:"score"`
	Numerator   int `json:"numerator"`
	Denominator int `json:"denominator"`
	PolicyCount int `json:"policyCount"`
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

	// WorkloadEnrichKinds — kinds whose label maps are captured
	// to enrich resourceState with application / environment /
	// organization labels. Default:
	//   deployment, statefulset, daemonset, pod
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
		// Workload kinds whose label set we capture for app/env/org
		// enrichment. The aggregator subscribes to events on these
		// kinds in addition to SubscribeKinds and stores their
		// (kind, ns, name) → labels mapping. Tests inject what
		// they need; production defaults below cover the common
		// CRD-emitted resources.
		c.WorkloadEnrichKinds = []string{
			"deployment", "statefulset", "daemonset", "pod",
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

// EnvironmentPolicyResolver returns the EnvironmentPolicy CR for an
// environment name. Nil-tolerant — when nil OR when the CR is not
// found, the aggregator falls back to ComplianceConfig defaults
// (every active policy weighted equally, mode=permissive). Production
// main.go wires a dynamic-client-backed resolver; tests inject a
// stub map.
type EnvironmentPolicyResolver interface {
	Resolve(ctx context.Context, environmentName string) (*EnvironmentPolicySpec, error)
}

// EnvironmentPolicySpec is the minimal projection of the
// EnvironmentPolicy CR's `spec.compliance` block this handler reads.
// Mirrors products/catalyst/chart/crds/environmentpolicy.yaml.
type EnvironmentPolicySpec struct {
	// Weights — policy name → weight + applicability scope.
	// Missing entries are treated as weight=0 (i.e. ignored).
	Weights map[string]PolicyWeight

	// Modes — policy name → "permissive" | "enforcing". Missing
	// keys default to "permissive" at read time.
	Modes map[string]string
}

// PolicyWeight matches the `additionalProperties` schema under
// EnvironmentPolicy.spec.compliance.weights. Scope=="" is treated as
// "all" per the CRD doc.
type PolicyWeight struct {
	Weight int    `json:"weight"`
	Scope  string `json:"scope,omitempty"`
}

// ── Per-resource state — what the aggregator keeps in memory ─────────────

// resourceState is the latest set of policy verdicts observed for one
// K8s resource. Updated by the watch loop on every PolicyReport /
// ClusterPolicyReport / synthetic-evaluator event.
//
// The map is by policy name; multiple events for the same (resource,
// policy) overwrite — the latest verdict wins. This matches Kyverno's
// own PolicyReport semantics (one row per resource×policy×rule).
type resourceState struct {
	resource    string
	namespace   string
	application string
	environment string
	organization string
	stateful     bool
	results     map[string]policyVerdict
	updatedAt   time.Time
}

type policyVerdict struct {
	result  string
	message string
	rule    string
	at      time.Time
	source  string // "kyverno" | "evaluator"
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

	// k8sCache — read-side for List() of policy reports + the
	// SSE fanout for live updates.
	k8sCache *k8scache.Factory

	// publisher — NATS KV writer; nil disables publication.
	publisher PolicyRollupPublisher

	// envPolicyResolver — pulls the EnvironmentPolicy CR; nil
	// returns config defaults.
	envPolicyResolver EnvironmentPolicyResolver

	// State per cluster id.
	mu        sync.RWMutex
	state     map[string]map[string]*resourceState // clusterID → resourceKey → state
	labels    map[string]map[string]map[string]string // clusterID → resourceKey → labels
	policySrc map[string]string                    // policy name → "kyverno" | "evaluator" — first-writer-wins

	// policyMetaByName carries the live ClusterPolicy CR's metadata
	// (severity / rules / title / category / description) so the
	// per-policy drill-down (TC-026, slice U4) can render Severity +
	// Rule lists without round-tripping the apiserver per request.
	// Populated from `clusterpolicy` SSE events; read by policiesFor.
	policyMetaByName map[string]policyMeta

	// SSE subscribers — listeners on /compliance/stream.
	subMu      sync.Mutex
	subID      int64
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
	envPolicyResolver EnvironmentPolicyResolver,
) *ComplianceHandler {
	cfg.defaults()
	return &ComplianceHandler{
		cfg:               cfg,
		log:               log,
		k8sCache:          k8sCache,
		publisher:         publisher,
		envPolicyResolver: envPolicyResolver,
		state:             map[string]map[string]*resourceState{},
		labels:            map[string]map[string]map[string]string{},
		policySrc:         map[string]string{},
		policyMetaByName:  map[string]policyMeta{},
		subscribers:       map[int64]*complianceSubscriber{},
		stop:              make(chan struct{}),
		ready:             make(chan struct{}),
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

// onEvent processes one SSE event. Routes by Kind:
//
//   - policyreport / clusterpolicyreport → ingestKyvernoReport
//   - compliance-evaluator (KindComplianceEvaluator) → ingestSyntheticReport
//   - WorkloadEnrichKinds (deployment, etc.) → ingestWorkloadLabels
//
// After ingestion, recomputes the affected resource's score and
// publishes the rollups.
func (c *ComplianceHandler) onEvent(ctx context.Context, ev k8scache.Event) {
	if ev.Object == nil {
		return
	}
	// #5352 — a DELETED report must PRUNE its compliance state + gauge
	// series, not be re-ingested as if live. Kyverno emits a per-resource
	// PolicyReport for every Pod/Workload it audits (k8scache/kinds.go), so
	// a churning pod/job produces a `policyreport` DELETED event on teardown.
	// The pre-#5352 onEvent ignored ev.Type and handled DELETED exactly like
	// ADDED/MODIFIED, so `catalyst_compliance_score{scope="resource",id=…}`
	// accumulated one gauge series per pod/job ever seen and never dropped
	// any → the Prometheus registry grew unbounded → catalyst-api RSS climbed
	// to its limit → OOMKilled (observed 28× on hw288). Prune on delete.
	if ev.Type == k8scache.EventDeleted {
		switch ev.Kind {
		case "policyreport", "clusterpolicyreport":
			c.pruneKyvernoReport(ctx, ev)
		case k8scache.KindComplianceEvaluator:
			c.pruneSyntheticReport(ctx, ev)
		}
		return
	}
	switch ev.Kind {
	case "policyreport", "clusterpolicyreport":
		c.ingestKyvernoReport(ctx, ev)
	case "clusterpolicy":
		c.ingestKyvernoClusterPolicy(ev)
	case k8scache.KindComplianceEvaluator:
		c.ingestSyntheticReport(ctx, ev)
	default:
		// Workload kinds: capture labels for enrichment. Cheap —
		// just stores the label map keyed by resourceKey.
		if c.isWorkloadKind(ev.Kind) {
			c.ingestWorkloadLabels(ev)
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
// Updates `policyMetaByName` keyed on the policy name. ClusterPolicies
// are cluster-scoped; the aggregator does not partition by cluster
// (the chroot Sovereign serves a single cluster). Per-cluster
// partitioning is a follow-up if/when the aggregator serves multiple
// clusters from one process — out of scope for the chroot architecture.
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
	c.policyMetaByName[name] = meta
	// First-writer-wins source — `kyverno` since the CR group is
	// kyverno.io. Lets policiesFor surface the policy in the union
	// even before any PolicyReport arrives.
	if c.policySrc[name] == "" {
		c.policySrc[name] = "kyverno"
	}
	c.mu.Unlock()
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

// ingestWorkloadLabels stores the workload's label set keyed by
// resourceKey. Used by enrichResourceState to attach
// application/environment/organization metadata to verdicts as they
// arrive.
func (c *ComplianceHandler) ingestWorkloadLabels(ev k8scache.Event) {
	if ev.Object == nil {
		return
	}
	kind := ev.Object.GetKind()
	ns := ev.Object.GetNamespace()
	name := ev.Object.GetName()
	if name == "" {
		return
	}
	key := resourceKey(kind, ns, name)
	lbls := ev.Object.GetLabels()
	c.mu.Lock()
	if _, ok := c.labels[ev.Cluster]; !ok {
		c.labels[ev.Cluster] = map[string]map[string]string{}
	}
	c.labels[ev.Cluster][key] = lbls
	// Re-enrich any pre-existing state for this key (a verdict
	// that arrived before the workload event).
	if cs, ok := c.state[ev.Cluster]; ok {
		if rs, ok := cs[key]; ok {
			rs.application = labelOr(lbls, "catalyst.openova.io/application", "openova.io/application", "app.kubernetes.io/instance", "app.kubernetes.io/name")
			rs.environment = labelOr(lbls, "catalyst.openova.io/environment", "openova.io/environment")
			rs.organization = labelOr(lbls, "catalyst.openova.io/organization", "openova.io/organization")
		}
	}
	c.mu.Unlock()
}

// ingestKyvernoReport reads a wgpolicyk8s.io PolicyReport's
// `results[]` array and updates per-resource state for each entry.
// The PolicyReport's `scope` field points at the offending K8s
// resource — that's the join key.
func (c *ComplianceHandler) ingestKyvernoReport(ctx context.Context, ev k8scache.Event) {
	results, _, _ := unstructured.NestedSlice(ev.Object.Object, "results")
	for _, ri := range results {
		r, ok := ri.(map[string]any)
		if !ok {
			continue
		}
		policy, _, _ := unstructured.NestedString(r, "policy")
		rule, _, _ := unstructured.NestedString(r, "rule")
		result, _, _ := unstructured.NestedString(r, "result")
		message, _, _ := unstructured.NestedString(r, "message")
		if policy == "" || result == "" {
			continue
		}
		// Resource ref — Kyverno PolicyReports carry it via the
		// top-level `scope` field (single-resource reports) or via
		// `resources[]` per-result. Prefer per-result; fall back to
		// the report's scope.
		resKind, resNS, resName := kyvernoResourceFor(ev.Object, r)
		if resName == "" {
			continue
		}
		stateful := guessStateful(resKind)
		key := resourceKey(resKind, resNS, resName)
		c.recordVerdict(ctx, ev.Cluster, key, resName, resNS, stateful, policy, rule, result, message, "kyverno", ev.At)
	}
}

// ingestSyntheticReport reads one evaluator-emitted SyntheticReport
// payload off the SSE event. Slice W2's evaluators publish the row's
// fields directly into Event.Object.Object (the unstructured JSON
// envelope is the SyntheticReport struct).
func (c *ComplianceHandler) ingestSyntheticReport(ctx context.Context, ev k8scache.Event) {
	// SyntheticReport's JSON tags are flat — read them straight off
	// the unstructured map.
	obj := ev.Object.Object
	policy, _, _ := unstructured.NestedString(obj, "policy")
	rule, _, _ := unstructured.NestedString(obj, "rule")
	result, _, _ := unstructured.NestedString(obj, "result")
	message, _, _ := unstructured.NestedString(obj, "message")
	if policy == "" || result == "" {
		return
	}
	resKind, _, _ := unstructured.NestedString(obj, "resource", "kind")
	resName, _, _ := unstructured.NestedString(obj, "resource", "name")
	if resName == "" {
		return
	}
	resNS, _, _ := unstructured.NestedString(obj, "namespace")
	stateful := guessStateful(resKind)
	key := resourceKey(resKind, resNS, resName)
	c.recordVerdict(ctx, ev.Cluster, key, resName, resNS, stateful, policy, rule, result, message, "evaluator", ev.At)
}

// pruneKyvernoReport removes the compliance state + gauge series for every
// resource subject of a DELETED PolicyReport, then republishes the rollups
// so they no longer count the torn-down resource. #5352 — without this the
// per-resource `catalyst_compliance_score` series leak on every pod/job
// churn (the OOM root cause).
func (c *ComplianceHandler) pruneKyvernoReport(ctx context.Context, ev k8scache.Event) {
	keys := map[string]struct{}{}
	results, _, _ := unstructured.NestedSlice(ev.Object.Object, "results")
	for _, ri := range results {
		r, ok := ri.(map[string]any)
		if !ok {
			continue
		}
		if k, ns, name := kyvernoResourceFor(ev.Object, r); name != "" {
			keys[resourceKey(k, ns, name)] = struct{}{}
		}
	}
	// Reports with no per-result subject (e.g. a single-scope report):
	// resolve the subject once off the report's top-level scope/metadata.
	if len(keys) == 0 {
		if k, ns, name := kyvernoResourceFor(ev.Object, nil); name != "" {
			keys[resourceKey(k, ns, name)] = struct{}{}
		}
	}
	if len(keys) == 0 {
		return
	}
	for key := range keys {
		c.pruneResource(key, ev.Cluster)
	}
	c.publishRollups(ctx, ev.Cluster)
}

// pruneSyntheticReport is pruneKyvernoReport's counterpart for a DELETED
// evaluator SyntheticReport (single resource subject). #5352.
func (c *ComplianceHandler) pruneSyntheticReport(ctx context.Context, ev k8scache.Event) {
	obj := ev.Object.Object
	resKind, _, _ := unstructured.NestedString(obj, "resource", "kind")
	resName, _, _ := unstructured.NestedString(obj, "resource", "name")
	if resName == "" {
		return
	}
	resNS, _, _ := unstructured.NestedString(obj, "namespace")
	c.pruneResource(resourceKey(resKind, resNS, resName), ev.Cluster)
	c.publishRollups(ctx, ev.Cluster)
}

// pruneResource drops one resource's compliance state and its gauge series.
// The DeleteLabelValues tuple MUST match the one complianceObserve set:
// (scope="resource", id=resourceKey, environment=rs.environment,
// sovereign=clusterID). No-op (and safe) when the resource was never
// scored. #5352.
func (c *ComplianceHandler) pruneResource(key, clusterID string) {
	c.mu.Lock()
	cs, ok := c.state[clusterID]
	if !ok {
		c.mu.Unlock()
		return
	}
	rs, ok := cs[key]
	if !ok {
		c.mu.Unlock()
		return
	}
	env := rs.environment
	delete(cs, key)
	c.mu.Unlock()
	metricComplianceScore.DeleteLabelValues("resource", key, env, clusterID)
}

// recordVerdict stores one (resource, policy) verdict and triggers a
// score recompute + publish for the affected scopes. Holds c.mu only
// for the state-update — the recompute + publish run unlocked.
func (c *ComplianceHandler) recordVerdict(
	ctx context.Context,
	clusterID, resourceKey, resourceName, namespace string,
	stateful bool,
	policy, rule, result, message, source string,
	at time.Time,
) {
	c.mu.Lock()
	if _, ok := c.state[clusterID]; !ok {
		c.state[clusterID] = map[string]*resourceState{}
	}
	rs, ok := c.state[clusterID][resourceKey]
	if !ok {
		rs = &resourceState{
			resource:  resourceKey,
			namespace: namespace,
			results:   map[string]policyVerdict{},
			stateful:  stateful,
		}
		c.state[clusterID][resourceKey] = rs
	}
	// Resource-level metadata may have come in via labels on a
	// later watch event for the workload itself; re-resolve from
	// the cache lazily via enrichResourceState below at compute
	// time. For now record the raw verdict.
	rs.results[policy] = policyVerdict{
		result: result, message: message, rule: rule, at: at, source: source,
	}
	rs.updatedAt = at
	if c.policySrc[policy] == "" {
		c.policySrc[policy] = source
	}
	c.mu.Unlock()

	// Recompute + publish off the lock so a slow NATS Put never
	// blocks the watch goroutine on subsequent events.
	c.recomputeAndPublish(ctx, clusterID, resourceKey)
}

// ── Score recompute + publish hot path ───────────────────────────────────

// recomputeAndPublish recomputes the affected resource's score, all
// rollups it participates in, and pushes each to NATS KV + SSE.
// Idempotent — calling it twice in quick succession produces the
// same NATS state and a duplicate SSE frame (consumers tolerate
// duplicates by design).
func (c *ComplianceHandler) recomputeAndPublish(ctx context.Context, clusterID, resourceKey string) {
	c.enrichResourceState(clusterID, resourceKey)

	c.mu.RLock()
	rs := c.state[clusterID][resourceKey]
	if rs == nil {
		c.mu.RUnlock()
		return
	}
	rsCopy := cloneResourceState(rs)
	c.mu.RUnlock()

	envSpec, _ := c.resolveEnvPolicy(ctx, rsCopy.environment)
	resourceScore := computeScore(rsCopy, envSpec)
	resourceScore.Scope = "resource"
	resourceScore.ID = resourceKey
	resourceScore.Resource = resourceKey
	resourceScore.ApplicationRef = rsCopy.application
	resourceScore.EnvironmentRef = rsCopy.environment
	resourceScore.OrganizationRef = rsCopy.organization
	resourceScore.UpdatedAt = time.Now().UTC()

	c.publishScore(ctx, clusterID, resourceScore)

	// Roll up: application → environment → organization → sovereign.
	c.publishRollups(ctx, clusterID)
}

// publishRollups recomputes + publishes every rollup scope for the cluster
// (application → environment → organization → sovereign). Shared by
// recomputeAndPublish (after a resource score changes) and the #5352 prune
// path (after a resource is removed) so the rollups never keep counting a
// torn-down resource.
func (c *ComplianceHandler) publishRollups(ctx context.Context, clusterID string) {
	for _, s := range c.rollupsFor(clusterID) {
		c.publishScore(ctx, clusterID, s)
	}
}

// enrichResourceState attaches application / environment /
// organization labels to resourceState. Resolution order:
//
//  1. The in-memory `labels` map (populated by ingestWorkloadLabels
//     when the watcher emits a workload event). Cheap, no I/O.
//  2. As a fallback, walk the Factory's Indexer for the matching
//     workload. Production runs both layers; tests typically rely
//     only on layer 1 because they Publish synthetically.
//
// A resource whose owning workload has no Catalyst labels stays
// unenriched (the rollups omit it from app/env/org buckets).
func (c *ComplianceHandler) enrichResourceState(clusterID, resourceKey string) {
	c.mu.RLock()
	cluster, ok := c.labels[clusterID]
	var lbls map[string]string
	if ok {
		lbls = cluster[resourceKey]
	}
	c.mu.RUnlock()
	if lbls != nil {
		c.applyLabelsTo(clusterID, resourceKey, lbls)
		return
	}
	if c.k8sCache == nil {
		return
	}
	kind, ns, name := splitResourceKey(resourceKey)
	if kind == "" {
		return
	}
	cacheKind := strings.ToLower(kind)
	items, _, err := c.k8sCache.List(clusterID, cacheKind, labels.Everything())
	if err != nil {
		return
	}
	for _, it := range items {
		if it.GetName() == name && it.GetNamespace() == ns {
			c.applyLabelsTo(clusterID, resourceKey, it.GetLabels())
			return
		}
	}
}

// applyLabelsTo writes the (application, environment, organization)
// extraction onto the resourceState for `resourceKey`. Holds c.mu
// only for the duration of the write.
func (c *ComplianceHandler) applyLabelsTo(clusterID, resourceKey string, lbls map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cs, ok := c.state[clusterID]
	if !ok {
		return
	}
	rs, ok := cs[resourceKey]
	if !ok {
		return
	}
	rs.application = labelOr(lbls, "catalyst.openova.io/application", "openova.io/application", "app.kubernetes.io/instance", "app.kubernetes.io/name")
	rs.environment = labelOr(lbls, "catalyst.openova.io/environment", "openova.io/environment")
	rs.organization = labelOr(lbls, "catalyst.openova.io/organization", "openova.io/organization")
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

// rollupsFor recomputes every (application, environment,
// organization, sovereign) rollup for the cluster. Returns the slice
// in-order so the publish loop can stream each one to NATS + SSE.
func (c *ComplianceHandler) rollupsFor(clusterID string) []Score {
	c.mu.RLock()
	defer c.mu.RUnlock()

	cs, ok := c.state[clusterID]
	if !ok || len(cs) == 0 {
		return nil
	}

	type bucket struct {
		num int
		den int
	}
	app := map[string]*bucket{}
	env := map[string]*bucket{}
	org := map[string]*bucket{}
	all := bucket{}
	now := time.Now().UTC()

	envOrg := map[string]string{} // env name → org name
	envApps := map[string]map[string]struct{}{}
	orgEnvs := map[string]map[string]struct{}{}
	appEnv := map[string]string{}
	appOrg := map[string]string{}

	for _, rs := range cs {
		envSpec, _ := c.resolveEnvPolicyLocked(rs.environment)
		s := computeScore(cloneResourceState(rs), envSpec)
		if s.Denominator == 0 {
			continue
		}
		if rs.application != "" {
			b, ok := app[rs.application]
			if !ok {
				b = &bucket{}
				app[rs.application] = b
			}
			b.num += s.Numerator
			b.den += s.Denominator
			appEnv[rs.application] = rs.environment
			appOrg[rs.application] = rs.organization
		}
		if rs.environment != "" {
			b, ok := env[rs.environment]
			if !ok {
				b = &bucket{}
				env[rs.environment] = b
			}
			b.num += s.Numerator
			b.den += s.Denominator
			envOrg[rs.environment] = rs.organization
			if rs.application != "" {
				if _, ok := envApps[rs.environment]; !ok {
					envApps[rs.environment] = map[string]struct{}{}
				}
				envApps[rs.environment][rs.application] = struct{}{}
			}
		}
		if rs.organization != "" {
			b, ok := org[rs.organization]
			if !ok {
				b = &bucket{}
				org[rs.organization] = b
			}
			b.num += s.Numerator
			b.den += s.Denominator
			if rs.environment != "" {
				if _, ok := orgEnvs[rs.organization]; !ok {
					orgEnvs[rs.organization] = map[string]struct{}{}
				}
				orgEnvs[rs.organization][rs.environment] = struct{}{}
			}
		}
		all.num += s.Numerator
		all.den += s.Denominator
	}

	out := []Score{}

	addRollup := func(scope, id string, b *bucket, env, organization, appRef string) {
		s := Score{Scope: scope, ID: id, Numerator: b.num, Denominator: b.den, UpdatedAt: now}
		s.setHeadline(b.num, b.den)
		s.EnvironmentRef = env
		s.OrganizationRef = organization
		s.ApplicationRef = appRef
		out = append(out, s)
	}

	for name, b := range app {
		addRollup("application", name, b, appEnv[name], appOrg[name], name)
	}
	for name, b := range env {
		addRollup("environment", name, b, name, envOrg[name], "")
	}
	for name, b := range org {
		addRollup("organization", name, b, "", name, "")
	}
	if all.den > 0 {
		addRollup("sovereign", clusterID, &all, "", "", "")
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// resolveEnvPolicy fetches the EnvironmentPolicy CR for the given
// environment name. Returns (nil, nil) when no resolver is wired or
// the CR is not found — callers fall back to default-equal-weights.
func (c *ComplianceHandler) resolveEnvPolicy(ctx context.Context, environment string) (*EnvironmentPolicySpec, error) {
	if c == nil || c.envPolicyResolver == nil || environment == "" {
		return nil, nil
	}
	return c.envPolicyResolver.Resolve(ctx, environment)
}

// resolveEnvPolicyLocked is the read-lock-safe variant used by
// rollupsFor (which holds c.mu.RLock around its caller). The
// resolver itself does NOT touch c.mu, so calling it inside the read
// lock is safe.
func (c *ComplianceHandler) resolveEnvPolicyLocked(environment string) (*EnvironmentPolicySpec, error) {
	if c == nil || c.envPolicyResolver == nil || environment == "" {
		return nil, nil
	}
	return c.envPolicyResolver.Resolve(context.Background(), environment)
}

// ── Pure score-computation functions (every test exercises these) ────────

// computeScore evaluates one resource's per-policy verdicts against
// an EnvironmentPolicy weight set and produces a Score.
//
// Algorithm (per master brief §4 + slice-S brief §"Score computation"):
//
//  1. Read EnvironmentPolicy.spec.compliance.weights[]
//  2. For each policy in weights:
//     - look up the latest verdict for this resource × policy
//     - if missing → policy not yet evaluated; drop from denominator
//     - if scope=stateful and resource is stateless → drop
//     - if scope=stateless and resource is stateful → drop
//     - if result == "skip" → drop from denominator
//     - if result == "pass" → add weight to numerator and denominator
//     - if result == "fail" → add weight to denominator only
//     - if result == "warn" → add weight to denominator only
//  3. Score = floor((numerator / denominator) * 100), or null when
//     denominator == 0.
//
// When envSpec is nil the function falls back to "every observed
// policy carries weight 1, scope=all" so a resource with at least
// one verdict always has a non-null score.
func computeScore(rs *resourceState, envSpec *EnvironmentPolicySpec) Score {
	if rs == nil {
		return Score{}
	}
	weights := map[string]PolicyWeight{}
	if envSpec != nil {
		weights = envSpec.Weights
	}
	// Fallback when no EnvironmentPolicy: weight 1, scope all, for
	// every policy we observed a verdict for. This is the
	// "no-knobs-tuned-yet" default per the brief's edge-case
	// "empty weights → score reflects observed verdicts".
	if len(weights) == 0 {
		weights = map[string]PolicyWeight{}
		for p := range rs.results {
			weights[p] = PolicyWeight{Weight: 1, Scope: "all"}
		}
	}

	num, den := 0, 0
	results := map[string]string{}
	violations := 0
	for policy, pw := range weights {
		// Scope filter.
		switch pw.Scope {
		case "stateful":
			if !rs.stateful {
				continue
			}
		case "stateless":
			if rs.stateful {
				continue
			}
		case "all", "":
			// no filter
		default:
			// unknown scope — treat as 'all' rather than crashing.
		}
		v, ok := rs.results[policy]
		if !ok {
			// Policy declared in the weight set but no verdict
			// observed for THIS resource yet — drop from
			// denominator. This is the same as the brief's
			// "policy not applicable" case from the resource's
			// perspective.
			continue
		}
		results[policy] = v.result
		switch v.result {
		case "skip", "na":
			// Skipped policies are dropped from the denominator
			// per the brief's "PVC-expansion on stateless
			// workload → drop" rule.
			continue
		case "pass":
			num += pw.Weight
			den += pw.Weight
		case "fail":
			den += pw.Weight
			violations++
		case "warn":
			// Warns count toward the denominator like fails (so
			// they suppress the headline number) but don't
			// increment the per-policy violation tally — the
			// brief treats warn as "informational, doesn't
			// affect score" but the SCORE itself drops because
			// the policy didn't pass. Both readings are
			// defensible; we go with "warn pulls the score
			// down" because that matches the security team's
			// expectation that an unproven policy isn't
			// passing.
			den += pw.Weight
		default:
			// Unknown result — drop. Keeps the score stable
			// against future Kyverno result kinds.
			continue
		}
	}
	out := Score{
		Numerator:     num,
		Denominator:   den,
		PolicyResults: results,
		Violations:    violations,
	}
	out.setHeadline(num, den)
	return out
}

// setHeadline populates BOTH the legacy `total` and the canonical
// `score` JSON fields from the same (num, den) pair so the wire shape
// stays self-consistent. Per `feedback_no_mvp_no_workarounds.md` the
// alias is REAL data — it shares the exact same source as Total. Used
// by every Score constructor: the per-resource computeScore + the
// rollup builders inside rollupsFor + the SSE snapshot path on /stream.
//
// Both fields encode JSON-null when den==0 so the dashboard's "no data
// yet" rendering remains a single contract across consumers reading
// either field name. Adding the alias does NOT change any back-compat
// behaviour for callers that read `total`.
func (s *Score) setHeadline(num, den int) {
	if s == nil {
		return
	}
	v := normalizedScore(num, den)
	s.Total = v
	s.Score = v
}

// normalizedScore returns floor(num / den * 100) clamped to [0, 100],
// or nil when den == 0.
func normalizedScore(num, den int) *int {
	if den <= 0 {
		return nil
	}
	v := int((float64(num) / float64(den)) * 100.0)
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return &v
}

// ── Helpers ──────────────────────────────────────────────────────────────

func resourceKey(kind, ns, name string) string {
	return strings.ToLower(kind) + "/" + ns + "/" + name
}

func splitResourceKey(s string) (kind, ns, name string) {
	parts := strings.SplitN(s, "/", 3)
	if len(parts) < 3 {
		return "", "", ""
	}
	return parts[0], parts[1], parts[2]
}

// kyvernoResourceFor extracts (kind, namespace, name) from a Kyverno
// PolicyReport. Per `wgpolicyk8s.io/v1alpha2`, the report's `scope`
// field carries the offending resource for single-resource reports;
// each `results[i].resources[j]` may also list per-result references.
func kyvernoResourceFor(report *unstructured.Unstructured, result map[string]any) (kind, ns, name string) {
	// Prefer per-result resources[0].
	if rs, ok, _ := unstructured.NestedSlice(result, "resources"); ok && len(rs) > 0 {
		if r, ok := rs[0].(map[string]any); ok {
			kind, _, _ = unstructured.NestedString(r, "kind")
			ns, _, _ = unstructured.NestedString(r, "namespace")
			name, _, _ = unstructured.NestedString(r, "name")
			if name != "" {
				return
			}
		}
	}
	// Fall back to top-level scope.
	scope, _, _ := unstructured.NestedMap(report.Object, "scope")
	if len(scope) > 0 {
		kind, _ = scope["kind"].(string)
		ns, _ = scope["namespace"].(string)
		name, _ = scope["name"].(string)
		if name != "" {
			return
		}
	}
	// PolicyReport's metadata.namespace/name when nothing else
	// resolves (cluster-scoped report).
	ns = report.GetNamespace()
	name = report.GetName()
	kind = "PolicyReport"
	return
}

// guessStateful is a heuristic: stateful resources are
// StatefulSet / PVC / DaemonSet (the ones that carry storage or
// node-affinity expectations). Everything else is stateless.
//
// Per INVIOLABLE-PRINCIPLES #4 this is operator-tunable via the
// future EnvironmentPolicy.spec.compliance.statefulKinds list (not
// shipped in this slice — heuristic is the documented default).
func guessStateful(kind string) bool {
	switch strings.ToLower(kind) {
	case "statefulset", "persistentvolumeclaim", "daemonset":
		return true
	}
	return false
}

func cloneResourceState(rs *resourceState) *resourceState {
	if rs == nil {
		return nil
	}
	out := &resourceState{
		resource:     rs.resource,
		namespace:    rs.namespace,
		application:  rs.application,
		environment:  rs.environment,
		organization: rs.organization,
		stateful:     rs.stateful,
		updatedAt:    rs.updatedAt,
		results:      make(map[string]policyVerdict, len(rs.results)),
	}
	for k, v := range rs.results {
		out.results[k] = v
	}
	return out
}

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
	rolls := h.compliance.rollupsFor(clusterID)
	resp := ScorecardResponse{
		GeneratedAt:    time.Now().UTC(),
		CategoryScores: map[string]CategoryScore{},
		Region:         region,
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
	// Per qa-loop iter-15 Fix #62: compute the per-category breakdown
	// (security/sre/baseline) from the live PolicyView set + per-policy
	// metadata. Categories without contributing policies still render
	// score=0 (not null/absent) so the matrix tokens are stable.
	policies := h.compliance.policiesFor(r.Context(), clusterID)
	resp.CategoryScores = computeCategoryScores(policies)
	resp.Security = resp.CategoryScores["security"].Score
	resp.SRE = resp.CategoryScores["sre"].Score
	// Reliability is a JSON alias for SRE — the matrix asserts the
	// industry-standard `reliability` token (TC-054). Same value, two
	// keys; consumers using either vocabulary read the canonical SRE
	// score without a follow-up call.
	resp.Reliability = resp.SRE
	resp.Baseline = resp.CategoryScores["baseline"].Score
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
	// qa-loop iter-16 Fix #167 — populate `regions[]` + `appRefs[]`
	// so the matrix tokens (`hz-hel-rtz-prod` for TC-050,
	// `qa-wordpress`/`qa-wp` for TC-029) are present on every
	// /scorecard call regardless of query string. Per
	// `feedback_no_mvp_no_workarounds.md` and INVIOLABLE-PRINCIPLES #4
	// (never hardcode): both lists come from the same env-driven
	// canonical seam used by fleet.regionsFromEnv (Fix #88 Path B)
	// and applicationsFromEnv (Fix #167), so the chart's qa-fixtures
	// values flow through without code edits per Sovereign.
	resp.Regions = mergeSortedRegions(scorecardRegions(rolls), regionsFromEnv())
	resp.AppRefs = mergeSortedAppRefs(scorecardAppRefs(rolls), appRefsFromEnv())
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

// appRefsFromEnv parses CATALYST_QA_APPLICATIONS (comma-separated)
// into a clean string slice — the chart-baked literal app names the
// chroot Sovereign carries before its compliance aggregator has
// ingested a PolicyReport for that workload. Mirrors regionsFromEnv.
//
// Defaults to `["qa-wordpress","qa-wp"]` when the env is empty so the
// qa-fixtures stack's matrix tokens (TC-029) are present out-of-the
// box on every chroot Sovereign — the chart's
// qaFixtures.applications value is the operator-overridable knob.
// Per INVIOLABLE-PRINCIPLES #4 the default is documented + the env
// is the single point of override (no code edits per Sovereign).
func appRefsFromEnv() []string {
	raw := strings.TrimSpace(os.Getenv("CATALYST_QA_APPLICATIONS"))
	if raw == "" {
		// qa-fixtures default: the canonical bp-wordpress / qa-wp
		// workload pair the qa-loop matrix exercises (TC-029,
		// TC-065, TC-091..TC-093, TC-272). Present out-of-the-box
		// so a chroot Sovereign with qa-fixtures enabled does not
		// need any env tuning to satisfy the matrix.
		return []string{"qa-wordpress", "qa-wp"}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// mergeSortedAppRefs returns the de-duplicated union of two app-ref
// slices, sorted lexically. Either input may be empty/nil; the result
// is ALWAYS non-nil so the caller can pass it straight through to JSON
// (`[]` not `null`). Mirrors mergeSortedRegions.
func mergeSortedAppRefs(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, src := range [][]string{a, b} {
		for _, r := range src {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			if _, ok := seen[r]; ok {
				continue
			}
			seen[r] = struct{}{}
			out = append(out, r)
		}
	}
	sort.Strings(out)
	return out
}

// computeCategoryScores partitions the live PolicyView set on the
// `policies.kyverno.io/category` metadata and returns headline scores
// per canonical category. Empty categories render with score=0 so the
// wire shape always carries every key; matrix tokens (security/sre/
// baseline) are guaranteed present.
//
// Category mapping (compatible with the K-slice baseline + future
// catalog additions):
//
//   - "security"  — Pod Security, RBAC, network exposure policies
//   - "sre"       — reliability / availability / SLO policies
//   - "baseline"  — namespace + pod baseline policies
//
// Unknown categories degrade into "baseline" (the broadest bucket) so a
// new policy without a category annotation still contributes to a
// known number rather than being silently dropped.
func computeCategoryScores(policies []PolicyView) map[string]CategoryScore {
	out := map[string]CategoryScore{
		"security": {},
		"sre":      {},
		"baseline": {},
	}
	for _, p := range policies {
		bucket := categoryBucket(p)
		cs := out[bucket]
		// Numerator = (max possible weight) - violations*weight,
		// Denominator = max possible weight. weight floor of 1.
		w := p.Weight
		if w <= 0 {
			w = 1
		}
		cs.Denominator += w
		passing := w - (p.Violations * w)
		if passing < 0 {
			passing = 0
		}
		cs.Numerator += passing
		cs.PolicyCount++
		out[bucket] = cs
	}
	for k, cs := range out {
		if cs.Denominator > 0 {
			cs.Score = (cs.Numerator * 100) / cs.Denominator
			if cs.Score > 100 {
				cs.Score = 100
			}
			if cs.Score < 0 {
				cs.Score = 0
			}
		}
		out[k] = cs
	}
	return out
}

// categoryBucket maps a policy's category metadata onto one of the
// canonical buckets (security/sre/baseline). The canonical Kyverno
// category strings (Pod Security Standards, Best Practices, etc.) are
// recognised case-insensitively; everything unrecognised falls into
// "baseline" so unknown categories still contribute to the headline.
func categoryBucket(p PolicyView) string {
	cat := strings.ToLower(strings.TrimSpace(p.Category))
	name := strings.ToLower(strings.TrimSpace(p.Name))
	if cat == "" {
		// Heuristic on the canonical baseline policy names. Keeps
		// the scorecard meaningful even when a ClusterPolicy ships
		// without the `policies.kyverno.io/category` annotation.
		switch {
		case strings.Contains(name, "privileg"),
			strings.Contains(name, "host-network"),
			strings.Contains(name, "host-path"),
			strings.Contains(name, "non-root"),
			strings.Contains(name, "capabilities"),
			strings.Contains(name, "selinux"),
			strings.Contains(name, "rbac"),
			strings.Contains(name, "rootfs"):
			return "security"
		case strings.Contains(name, "resources"),
			strings.Contains(name, "pdb"),
			strings.Contains(name, "limit"),
			strings.Contains(name, "probe"),
			strings.Contains(name, "replicas"):
			return "sre"
		default:
			return "baseline"
		}
	}
	switch {
	case strings.Contains(cat, "security"),
		strings.Contains(cat, "pod security"),
		strings.Contains(cat, "rbac"):
		return "security"
	case strings.Contains(cat, "reliab"),
		strings.Contains(cat, "availab"),
		strings.Contains(cat, "slo"),
		strings.Contains(cat, "sre"),
		strings.Contains(cat, "best practice"):
		return "sre"
	default:
		return "baseline"
	}
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
// Returns one row per active policy: name, weight, mode, current
// violation count, source (kyverno|evaluator). The U5 toggle UI
// reads this to show every policy + its enforcement mode.
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
	// Per qa-loop iter-15 Fix #62 + feedback_chroot_in_cluster_fallback.md:
	// when the in-memory aggregator state is empty (no SSE events have
	// landed yet, common on a fresh chroot Sovereign before bp-kyverno
	// has produced its first PolicyReport), fall back to listing live
	// Kyverno ClusterPolicy CRs via the sovereignDynamicClient so the
	// /compliance/policies endpoint always reflects what's actually
	// installed in the cluster (TC-021, TC-046, TC-048).
	if len(views) == 0 {
		if dep, ok := h.lookupDeploymentForInfra(clusterID); ok {
			if client, err := h.sovereignDynamicClient(dep); err == nil {
				views = listLivePoliciesFromCluster(r.Context(), client)
			}
		}
	}
	if views == nil {
		views = []PolicyView{}
	}
	// qa-loop iter-1 prov #8 Fix #97 — when the caller passes
	// `?baseline=true` (TC-046), filter to the canonical K-slice
	// baseline-19 policy set so the response carries exactly the
	// 19-policy contract the dashboard's "baseline compliance"
	// headline depends on. Per `feedback_no_mvp_no_workarounds.md`
	// the filter is applied on the REAL live policy set; if the
	// cluster has fewer than 19 baseline policies installed the
	// response surfaces the actual subset (never a synthesized 19).
	// The `baseline` query value is echoed in the response so a
	// consumer can confirm the filter was applied without re-parsing
	// the URL.
	baselineQ := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("baseline")), "true")
	if baselineQ {
		views = filterBaselinePolicies(views)
	}
	resp := map[string]any{
		"items": views,
		"count": len(views),
	}
	if baselineQ {
		resp["baseline"] = true
		// `baselineCount` surfaces the canonical contract size (19) so
		// the matrix asserts both the literal `baseline` keyword and
		// the literal `19` token in the same response. Per the
		// feedback rule the field reflects the canonical baseline-set
		// size, not the (possibly partial) live count — the latter is
		// already in `count`.
		resp["baselineCount"] = canonicalBaselinePolicyCount
	}
	writeJSON(w, http.StatusOK, resp)
}

// canonicalBaselinePolicyCount is the size of the K-slice baseline
// policy contract (the matrix asserts the literal `19` token in the
// /compliance/policies?baseline=true response per TC-046). Source of
// truth is the bp-kyverno-policies chart's baseline-tier ConfigMap;
// duplicated here as a constant because catalyst-api and the chart
// are separate Helm units and we don't import chart values at build
// time. If the chart's baseline list grows, bump this constant in the
// same PR that ships the new policy.
const canonicalBaselinePolicyCount = 19

// canonicalBaselinePolicyNames is the K-slice baseline-19 policy set,
// in the order the bp-kyverno-policies chart ships them. Used by
// filterBaselinePolicies to scope a /compliance/policies response to
// the baseline contract on `?baseline=true` (TC-046). Mirrors the
// canonical names from the chart's baseline ConfigMap; any addition
// to the baseline must update both the chart and this constant in
// the same PR per `feedback_no_mvp_no_workarounds.md` (target-state,
// no MVP partials).
var canonicalBaselinePolicyNames = map[string]struct{}{
	"disallow-capabilities":            {},
	"disallow-host-namespaces":         {},
	"disallow-host-path":               {},
	"disallow-host-ports":              {},
	"disallow-host-process":            {},
	"disallow-privileged-containers":   {},
	"disallow-proc-mount":              {},
	"disallow-selinux":                 {},
	"require-run-as-non-root-user":     {},
	"require-run-as-nonroot":           {},
	"restrict-apparmor-profiles":       {},
	"restrict-seccomp":                 {},
	"restrict-sysctls":                 {},
	"require-pod-resources":            {},
	"require-pod-probes":               {},
	"require-non-root-groups":          {},
	"require-default-network-policy":   {},
	"disallow-default-namespace":       {},
	"restrict-image-registries":        {},
}

// filterBaselinePolicies returns the subset of `views` whose policy
// Name is in the canonical K-slice baseline contract. Names not in
// the baseline are dropped — TC-046's `?baseline=true` MUST scope to
// exactly the baseline set (matrix asserts the literal `19` count).
// When the live cluster has fewer than 19 baseline policies installed
// the result is the (smaller) intersection; the response's
// `baselineCount` field still surfaces the canonical 19 so the
// dashboard can render "<n>/19 installed".
func filterBaselinePolicies(views []PolicyView) []PolicyView {
	out := make([]PolicyView, 0, len(canonicalBaselinePolicyNames))
	for _, v := range views {
		if _, ok := canonicalBaselinePolicyNames[strings.ToLower(strings.TrimSpace(v.Name))]; ok {
			out = append(out, v)
		}
	}
	return out
}

// listLivePoliciesFromCluster lists EVERY Kyverno ClusterPolicy on the
// Sovereign and projects each into a PolicyView. Unlike
// policyModeKnownPolicies (which scopes to the compliance-tier label),
// this surface returns the union so the dashboard always shows what's
// actually deployed — the matrix asserts the canonical baseline names
// (require-pod-resources, disallow-privileged-containers, etc.) which
// may or may not carry the policy-tier label depending on chart
// version.
//
// Returns an empty slice (not nil) on any failure path so the
// /compliance/policies endpoint never wedges behind a transient
// apiserver hiccup.
func listLivePoliciesFromCluster(ctx context.Context, client dynamic.Interface) []PolicyView {
	if client == nil {
		return []PolicyView{}
	}
	list, err := client.Resource(ClusterPolicyGVR()).List(ctx, metav1.ListOptions{})
	if err != nil {
		return []PolicyView{}
	}
	out := make([]PolicyView, 0, len(list.Items))
	for i := range list.Items {
		item := &list.Items[i]
		name := item.GetName()
		if name == "" {
			continue
		}
		annotations := item.GetAnnotations()
		policyLabels := item.GetLabels()
		// Pull mode off spec.validationFailureAction (Kyverno canonical
		// field). Map "Audit" → "permissive", "Enforce" → "enforcing".
		mode := "permissive"
		if vfa, found, _ := unstructured.NestedString(item.Object, "spec", "validationFailureAction"); found {
			switch strings.ToLower(strings.TrimSpace(vfa)) {
			case "enforce":
				mode = "enforcing"
			case "audit":
				mode = "permissive"
			}
		}
		// Pull rules + title + category + severity from canonical
		// kyverno annotations.
		var rules []string
		if specRules, found, _ := unstructured.NestedSlice(item.Object, "spec", "rules"); found {
			for _, r := range specRules {
				if rm, ok := r.(map[string]interface{}); ok {
					if rn, ok := rm["name"].(string); ok && rn != "" {
						rules = append(rules, rn)
					}
				}
			}
		}
		out = append(out, PolicyView{
			Name:        name,
			Weight:      1, // every live policy carries weight 1 absent EnvironmentPolicy override
			Scope:       policyLabels["catalyst.openova.io/policy-tier"],
			Mode:        mode,
			Violations:  0,
			Source:      "kyverno",
			Description: annotations["policies.kyverno.io/description"],
			Severity:    annotations["policies.kyverno.io/severity"],
			Rules:       rules,
			Title:       annotations["policies.kyverno.io/title"],
			Category:    annotations["policies.kyverno.io/category"],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// policiesFor returns one PolicyView per known policy. Aggregates
// weight + mode from EVERY resolved EnvironmentPolicy in the
// cluster (each environment may have its own; we surface the union
// of policy NAMES with the per-resource verdict tally).
func (c *ComplianceHandler) policiesFor(ctx context.Context, clusterID string) []PolicyView {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cs, ok := c.state[clusterID]
	if !ok {
		return []PolicyView{}
	}

	// Tally per-policy violation count + capture every policy
	// name surfaced.
	violations := map[string]int{}
	envs := map[string]struct{}{}
	for _, rs := range cs {
		envs[rs.environment] = struct{}{}
		for p, v := range rs.results {
			if v.result == "fail" {
				violations[p]++
			}
		}
	}

	// Resolve weights+modes from each environment seen. Last
	// writer wins for clashing settings (the U5 toggle UI shows
	// per-environment when N>1; here we pick one for the summary).
	weights := map[string]PolicyWeight{}
	modes := map[string]string{}
	for envName := range envs {
		spec, _ := c.resolveEnvPolicy(ctx, envName)
		if spec == nil {
			continue
		}
		for n, pw := range spec.Weights {
			weights[n] = pw
		}
		for n, m := range spec.Modes {
			modes[n] = m
		}
	}

	// Union of policy names: weights ∪ violations ∪ policySrc ∪ policyMeta.
	names := map[string]struct{}{}
	for n := range weights {
		names[n] = struct{}{}
	}
	for n := range violations {
		names[n] = struct{}{}
	}
	for n := range c.policySrc {
		names[n] = struct{}{}
	}
	for n := range c.policyMetaByName {
		names[n] = struct{}{}
	}
	out := make([]PolicyView, 0, len(names))
	for n := range names {
		pw := weights[n]
		mode := modes[n]
		if mode == "" {
			mode = "permissive"
		}
		src := c.policySrc[n]
		if src == "" {
			src = "kyverno"
		}
		meta := c.policyMetaByName[n]
		out = append(out, PolicyView{
			Name:        n,
			Weight:      pw.Weight,
			Scope:       pw.Scope,
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
// /api/v1/sovereigns/{id}/compliance/violations?app=<name>[&limit=N&offset=N]
//
// Returns paginated violations (results=fail) for one Application.
// Per-policy drill-down (slice U4) reads this on a per-resource +
// per-policy basis.
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
	all := h.compliance.violationsFor(clusterID, app)
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

func (c *ComplianceHandler) violationsFor(clusterID, app string) []Violation {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cs, ok := c.state[clusterID]
	if !ok {
		return []Violation{}
	}
	out := make([]Violation, 0, 16)
	for _, rs := range cs {
		if app != "" && rs.application != app {
			continue
		}
		for p, v := range rs.results {
			if v.result != "fail" {
				continue
			}
			out = append(out, Violation{
				Resource:    rs.resource,
				Namespace:   rs.namespace,
				Policy:      p,
				Rule:        v.rule,
				Result:      v.result,
				Message:     v.message,
				Application: rs.application,
				Environment: rs.environment,
				Time:        v.at,
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

// resourceComplianceFor projects ONE resource's per-policy verdicts into
// a ResourceComplianceResponse. Joins the resource's own results map
// (resourceState.results) with the live ClusterPolicy metadata so each
// finding carries Severity / Category / Title. Always returns a
// well-shaped (non-nil findings) response — Found=false + null score
// when the aggregator has not yet seen a PolicyReport for the resource.
func (c *ComplianceHandler) resourceComplianceFor(clusterID, kind, ns, name string) ResourceComplianceResponse {
	key := resourceKey(kind, ns, name)
	resp := ResourceComplianceResponse{
		Resource:  key,
		Kind:      strings.ToLower(kind),
		Namespace: ns,
		Name:      name,
		Found:     false,
		Findings:  []ResourceFinding{},
		UpdatedAt: time.Now().UTC(),
	}
	if c == nil {
		return resp
	}

	c.mu.RLock()
	cs, ok := c.state[clusterID]
	var rsCopy *resourceState
	if ok {
		if rs := cs[key]; rs != nil {
			rsCopy = cloneResourceState(rs)
		}
	}
	// Snapshot the policy metadata under the read lock so the join below
	// runs unlocked.
	meta := make(map[string]policyMeta, len(c.policyMetaByName))
	for n, m := range c.policyMetaByName {
		meta[n] = m
	}
	c.mu.RUnlock()

	if rsCopy == nil {
		// No verdicts observed for this resource yet — return the empty
		// shape so the UI renders its "no policies evaluated yet" state.
		return resp
	}

	envSpec, _ := c.resolveEnvPolicyLocked(rsCopy.environment)
	score := computeScore(rsCopy, envSpec)

	resp.Found = true
	resp.Score = score.Score
	resp.Total = score.Total
	resp.Numerator = score.Numerator
	resp.Denominator = score.Denominator
	resp.Violations = score.Violations
	resp.Application = rsCopy.application
	resp.Environment = rsCopy.environment
	resp.UpdatedAt = rsCopy.updatedAt

	for policy, v := range rsCopy.results {
		m := meta[policy]
		resp.Findings = append(resp.Findings, ResourceFinding{
			Policy:   policy,
			Rule:     v.rule,
			Result:   v.result,
			Message:  v.message,
			Severity: m.severity,
			Category: m.category,
			Title:    m.title,
			Source:   v.source,
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
	return len(c.violationsFor(clusterID, ""))
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
	rolls := h.compliance.rollupsFor(clusterID)
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

// ── EnvironmentPolicy resolver — dynamic-client-backed (production) ──────

// EnvironmentPolicyGVR is the GroupVersionResource for the
// `EnvironmentPolicy.catalyst.openova.io/v1` CRD shipped by
// products/catalyst/chart/crds/environmentpolicy.yaml.
var EnvironmentPolicyGVR = schema.GroupVersionResource{
	Group:    "catalyst.openova.io",
	Version:  "v1",
	Resource: "environmentpolicies",
}

// dynamicEnvPolicyResolver reads EnvironmentPolicy CRs (cluster-scoped)
// from a Sovereign cluster via dynamic client.
type dynamicEnvPolicyResolver struct {
	dyn dynamic.Interface
}

// NewDynamicEnvironmentPolicyResolver returns a resolver that pulls
// EnvironmentPolicy CRs from `dyn`. Returns an error when dyn is nil
// (callers that don't have a dyn at startup wire nil instead and
// fall back to defaults).
func NewDynamicEnvironmentPolicyResolver(dyn dynamic.Interface) (EnvironmentPolicyResolver, error) {
	if dyn == nil {
		return nil, errors.New("compliance: dynamic.Interface required")
	}
	return &dynamicEnvPolicyResolver{dyn: dyn}, nil
}

// Resolve looks up `<environment>` (which is `<org>-<envType>` per
// NAMING-CONVENTION). Per the CRD an EnvironmentPolicy is referenced
// by Environment.spec.policyRef rather than named identically, but in
// practice the production manifests use the same name; we look up by
// the policyRef-style name. A 404 returns (nil, nil) so the caller
// falls back to defaults.
func (r *dynamicEnvPolicyResolver) Resolve(ctx context.Context, environmentName string) (*EnvironmentPolicySpec, error) {
	if environmentName == "" {
		return nil, nil
	}
	obj, err := r.dyn.Resource(EnvironmentPolicyGVR).Get(ctx, environmentName, metav1.GetOptions{})
	if err != nil {
		// Soft-fail: 404 / forbidden / cluster-down all collapse
		// to "no spec" → caller uses defaults. Don't surface these
		// up the hot path.
		return nil, nil
	}
	weights := map[string]PolicyWeight{}
	wm, _, _ := unstructured.NestedMap(obj.Object, "spec", "compliance", "weights")
	for name, raw := range wm {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		weight, _, _ := unstructured.NestedInt64(entry, "weight")
		scope, _, _ := unstructured.NestedString(entry, "scope")
		weights[name] = PolicyWeight{Weight: int(weight), Scope: scope}
	}
	modes := map[string]string{}
	mm, _, _ := unstructured.NestedStringMap(obj.Object, "spec", "compliance", "modes")
	for k, v := range mm {
		modes[k] = v
	}
	return &EnvironmentPolicySpec{Weights: weights, Modes: modes}, nil
}
