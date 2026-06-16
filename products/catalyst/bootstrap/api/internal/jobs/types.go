// Package jobs implements the Jobs/Executions data model + persistence
// the catalyst-api Sovereign Admin surfaces consume.
//
// # Architecture (recursive Job model — issue #351)
//
// Every install action and every audit-trail mutation is a Job. A Job
// can be a leaf ("install" — one HelmRelease watch attempt, one Day-2
// mutation, etc.) or a parent ("group" — a synthesised Job whose
// status, timing and child-ids are derived from its descendants). The
// previous "batch" concept is gone: bootstrap-kit and day-2-mutations
// are now parent group Jobs, not a parallel namespace.
//
// Each `bp-<chart>` HelmRelease the Phase-1 helmwatch observes maps
// 1:1 to a leaf Job (jobName="install-<chart>", appId="<chart>",
// parentId="<deploymentId>:bootstrap-kit"). The bootstrap-kit parent
// is materialised lazily on the first child seed. Day-2 mutations
// hang off the day-2-mutations parent in the same way.
//
// Two parallel feeds live for the same data:
//
//   - The existing `/api/v1/deployments/{id}/events` SSE feed — kept
//     untouched, the wizard's live banner reads it.
//   - The Jobs/Executions REST surface (`/jobs`, `/jobs/{jobId}`,
//     `/actions/executions/{execId}/logs`) — the canvas, table view
//     and per-job detail page read it.
//
// Persistence (per docs/INVIOLABLE-PRINCIPLES.md #4: every path is
// runtime-configurable via CATALYST_EXECUTIONS_DIR; default lives on
// the same `catalyst-api-deployments` PVC mount the deployments store
// uses):
//
//	/var/lib/catalyst/executions/<deploymentId>/index.json   — Job +
//	                                                           Execution
//	                                                           metadata,
//	                                                           atomic
//	                                                           write
//	                                                           (temp+
//	                                                           rename).
//	/var/lib/catalyst/executions/<deploymentId>/<execId>.log — append-
//	                                                           only
//	                                                           NDJSON
//	                                                           (one
//	                                                           LogLine
//	                                                           per
//	                                                           line).
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 (credential hygiene) no log
// line ever carries a kubeconfig, bearer token, or other secret — the
// helmwatch package only emits HelmRelease status messages, which are
// public-cluster-state by definition.
package jobs

import (
	"strings"
	"time"
)

// Status enums — kept in lockstep with helmwatch.State* via the
// translation in helmwatch_bridge.go. The wire contract uses these
// strings verbatim.
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

// Log levels — the helmwatch bridge maps Helm condition severity onto
// these. ERROR for "failed", WARN for "degraded", INFO for everything
// else (DEBUG is reserved for future client-instrumented log feeds).
const (
	LevelDebug = "DEBUG"
	LevelInfo  = "INFO"
	LevelWarn  = "WARN"
	LevelError = "ERROR"
)

// Job types — leaf jobs vs synthesised parent groups. The "group" type
// is the load-bearing signal: status, timing and durationMs on a
// group Job are derived from its descendants at read time, not from
// persisted leaf-state. Persisted Status on a group is ignored on
// read.
const (
	JobTypeInstall = "install"
	JobTypeGroup   = "group"
)

// Job kinds — the typed discriminator (issue #3646 §4b). Replaces the
// three disjoint JobName-prefix vocabularies (`install-`, `<group>-step-`,
// `mutation-<verb>-<kind>`) a consumer used to string-match. Every leaf
// Job carries exactly one Kind, populated at write time by the bridge
// that mints it; deriveTreeView back-fills it on read for legacy rows
// (kindForLeaf) so an index persisted before this field landed still
// renders honestly. Group Jobs carry KindGroup.
//
//   - KindInstall    — a HelmRelease install leaf ("install-<chart>").
//   - KindReconcile  — a Flux Kustomization reconcile leaf
//     ("reconcile-<name>"). Status from Ready condition.
//   - KindStep       — one step of a projected activity ("<group>-step-
//     <slug>"), e.g. the cutover / DR-switchover steps.
//   - KindMutation   — a Day-2 Crossplane XRC submission
//     ("mutation-<verb>-<kind>").
//   - KindCron       — a recurring CronJob ("cron-<name>"); each spawned
//     Job is an Execution. Status from last-N runs.
//   - KindTask       — a standalone / owned batch Job ("task-<name>")
//     that is NOT an install-hook of a tracked HR.
//   - KindReconciler — a long-running reconciler Deployment carrying the
//     catalyst.openova.io/reconciler marker
//     ("reconciler-<name>"); reports a HEALTH axis
//     (healthy/degraded/failing), never a one-shot
//     "succeeded".
//   - KindLifecycle  — a Phase-0 / lifecycle leaf (tofu-init etc.) or a
//     synthetic lifecycle phase with no other kind.
//   - KindGroup      — a synthesised parent group Job.
const (
	KindInstall    = "install"
	KindReconcile  = "reconcile"
	KindStep       = "step"
	KindMutation   = "mutation"
	KindCron       = "cron"
	KindTask       = "task"
	KindReconciler = "reconciler"
	KindLifecycle  = "lifecycle"
	KindGroup      = "group"
)

// JobName prefixes for the §5a ingestion kinds. Kept disjoint from the
// helmwatch "install-" leaves and the activity "<group>-step-" leaves so
// the source bridges can never mint colliding Job ids under one
// deployment.
const (
	// ReconcileJobPrefix — Flux Kustomization reconcile leaf.
	ReconcileJobPrefix = "reconcile-"
	// CronJobPrefix — recurring CronJob leaf.
	CronJobPrefix = "cron-"
	// TaskJobPrefix — standalone / owned batch Job leaf.
	TaskJobPrefix = "task-"
	// ReconcilerJobPrefix — long-running reconciler Deployment leaf.
	ReconcilerJobPrefix = "reconciler-"
)

// HealthStatus — the extra status tone recurring/reconciler kinds report
// in addition to the one-shot pending/running/succeeded/failed set. A
// never-terminating reconciler that is "running forever and that's
// correct" reports StatusHealthy; "running forever and that's a hang"
// reports StatusDegraded/StatusFailing. This keeps the
// sso-bridge-shows-Running-forever kind-confusion (issue #3646 §4c)
// distinct from a genuine hang. On the wire these reuse the Status field;
// deriveTreeView treats a degraded/failing recurring leaf as a surfaced
// failure on its group rather than averaging it away.
const (
	StatusHealthy  = "healthy"
	StatusDegraded = "degraded"
	StatusFailing  = "failing"
)

// IsHealthAxisStatus reports whether a status string is one of the
// recurring/reconciler HEALTH-axis values (healthy/degraded/failing) as
// opposed to the one-shot lifecycle axis. The rollup uses this to fold a
// degraded/failing health leaf into its group's surfaced state.
func IsHealthAxisStatus(status string) bool {
	switch status {
	case StatusHealthy, StatusDegraded, StatusFailing:
		return true
	}
	return false
}

// kindForLeaf infers a leaf Job's Kind from its JobName when the
// persisted Kind is empty (legacy rows written before the field existed).
// Group Jobs are handled by the caller (KindGroup). The order matters:
// the "<group>-step-<slug>" infix is checked before the bare prefixes so
// a "cutover-step-..." leaf is a step, not mis-tagged.
func kindForLeaf(jobName string) string {
	switch {
	case strings.Contains(jobName, ActivityStepInfix):
		return KindStep
	case strings.HasPrefix(jobName, MutationJobNamePrefix):
		return KindMutation
	case strings.HasPrefix(jobName, JobNamePrefix):
		return KindInstall
	case strings.HasPrefix(jobName, ReconcileJobPrefix):
		return KindReconcile
	case strings.HasPrefix(jobName, CronJobPrefix):
		return KindCron
	case strings.HasPrefix(jobName, TaskJobPrefix):
		return KindTask
	case strings.HasPrefix(jobName, ReconcilerJobPrefix):
		return KindReconciler
	default:
		// Phase-0 lifecycle slugs (tofu-init, …) + any other leaf.
		return KindLifecycle
	}
}

// Group slugs — used as the JobName for synthesised parent group
// Jobs. The full Job ID is JobID(deploymentID, slug).
const (
	GroupBootstrapKit  = "bootstrap-kit"
	GroupDay2Mutations = "day-2-mutations"
	GroupProvisioner   = "provisioner"
	GroupCutover       = "cutover"
	GroupHandover      = "handover"
	GroupApps          = "apps"
	// GroupReconcilers — top-level group for the §5a non-install
	// reconciler activities (Kustomizations, CronJobs, standalone Jobs,
	// reconciler Deployments). Kept distinct from bootstrap-kit so a
	// failing recurring activity (a Failed openbao-snapshot CronJob)
	// surfaces RED at the top even while its `install-openbao` leaf stays
	// green under bootstrap-kit — the operator sees the truth (issue
	// #3646 §3a/§6).
	GroupReconcilers = "reconcilers"
)

// Group display names — user-visible labels for the synthesised
// parent groups. The wire shape carries `displayName`; the UI falls
// back to `jobName` when `displayName` is empty (leaf jobs).
const (
	GroupBootstrapKitDisplay  = "Bootstrap"
	GroupDay2MutationsDisplay = "Day-2 Mutations"
	GroupProvisionerDisplay   = "Provision Hetzner"
	GroupCutoverDisplay       = "Cutover"
	GroupHandoverDisplay      = "Handover"
	GroupAppsDisplay          = "Apps"
	GroupReconcilersDisplay   = "Reconcilers"
)

// Phase-0 lifecycle phase slugs — durable Job rows the bridge writes
// when the provisioner.Provision goroutine emits the corresponding
// named-phase events. The slugs match provisioner.go's emit() phase
// strings 1:1 so OnProvisionerLifecycleEvent's lookup is direct.
const (
	PhaseTofuInit         = "tofu-init"
	PhaseTofuPlan         = "tofu-plan"
	PhaseTofuApply        = "tofu-apply"
	PhaseTofuOutput       = "tofu-output"
	PhaseClusterBootstrap = "cluster-bootstrap"
)

// JobNamePrefix — every Phase-1 leaf install Job is named
// "install-<chart>". The helmwatch bridge derives this from the
// HelmRelease metadata.name ("bp-foo" → "install-foo").
const JobNamePrefix = "install-"

// IsTerminal reports whether a status string represents a terminal
// state (no further state transitions). The store's running→done
// transitions and the "executionFinished" pagination flag both key
// off this.
func IsTerminal(status string) bool {
	switch status {
	case StatusSucceeded, StatusFailed:
		return true
	}
	return false
}

// Job is the wire-contract Job shape. The store materialises one
// leaf Job per `bp-<chart>` HelmRelease; the helmwatch bridge keeps
// its state in sync as conditions transition. Group Jobs (`Type =
// JobTypeGroup`) are materialised lazily on first child seed; their
// runtime status is derived at read time and overrides any persisted
// value.
//
// Fields use omitempty for nullable timestamps and for the derived
// `childIds` so the JSON shape the frontend sees matches the spec
// verbatim.
type Job struct {
	// ID is the stable identifier "<deploymentId>:<jobName>". It is
	// the URL-safe id the GET /jobs/{jobId} endpoint accepts.
	ID string `json:"id"`

	// DeploymentID — the parent deployment the Job belongs to.
	DeploymentID string `json:"deploymentId"`

	// JobName — slug used in URLs and as the persistence key. Leaf
	// install jobs are "install-<chart>"; group jobs use their slug
	// constant (e.g. "bootstrap-kit").
	JobName string `json:"jobName"`

	// DisplayName — optional user-visible label. Used by group jobs
	// to surface a friendlier name (e.g. "Bootstrap" for the
	// bootstrap-kit group). Empty for leaf install jobs — the FE
	// renders JobName when DisplayName is absent.
	DisplayName string `json:"displayName,omitempty"`

	// Type — JobTypeInstall (leaf) or JobTypeGroup (parent).
	Type string `json:"type"`

	// Kind — the typed activity discriminator (issue #3646 §4b): one of
	// install|reconcile|step|mutation|cron|task|reconciler|lifecycle for
	// leaves, group for parents. Replaces the JobName-prefix string-match
	// every consumer used to do. Populated at write time by each bridge;
	// deriveTreeView back-fills it on read (kindForLeaf / KindGroup) so a
	// row persisted before this field existed still carries an honest
	// kind on the wire. omitempty so an all-zero test Job marshals
	// without a stray "" — the read path always stamps it.
	Kind string `json:"kind,omitempty"`

	// AppID — the Sovereign component id (e.g. "cilium") for leaf
	// install jobs. Empty for group jobs and Day-2 mutation jobs that
	// are not 1:1 with a component.
	//
	// On a multi-region Sovereign the secondary regions' install jobs
	// carry the region in a "<region>:<chart>" prefix (e.g.
	// "me-east-215-b-1:cilium") so the AppID stays globally-unique
	// across clusters. The first-class Region field below is the
	// unambiguous source of truth for which region the job ran in;
	// AppID's prefix is kept for URL-routing compatibility.
	AppID string `json:"appId,omitempty"`

	// Region — the Hetzner/cloud region key the job's HelmRelease was
	// observed in (e.g. "me-east-215-b-1"). Empty for primary-region
	// rows and group jobs, so a single-region Sovereign never surfaces
	// a Region column (the UI's region filter hides itself when fewer
	// than two distinct regions appear). Populated by the multi-region
	// chroot seed fan-out (chrootSeedSecondaryRegions) and the
	// mothership's secondary helmwatch.Watcher bridge from the
	// "<region>:" component prefix.
	//
	// Wire tag is lowercase camelCase "region" — kept verbatim in
	// lockstep with the TS Job interface (a casing mismatch silently
	// drops the field on the JSON round-trip; see the documented
	// LaunchURLResponse.URL outage).
	Region string `json:"region,omitempty"`

	// ParentID — full ID of the parent Job, or "" for top-level
	// jobs. Replaces the old BatchID denormalisation: instead of a
	// flat batch tag, jobs form a directed tree rooted at top-level
	// group jobs. Cycles are forbidden by construction (the bridges
	// only ever set ParentID to a known group's ID).
	ParentID string `json:"parentId"`

	// DependsOn — list of Job IDs this Job depends on. Derived from
	// the HelmRelease's `spec.dependsOn[*].name` with the bp- prefix
	// stripped, "install-" prepended, and the deployment-prefix
	// applied (e.g. spec.dependsOn entry `name: bp-cilium` →
	// DependsOn entry `<deploymentId>:install-cilium`). For dep
	// arrows to render the canvas resolves these by exact ID match.
	DependsOn []string `json:"dependsOn"`

	// Status — pending|running|succeeded|failed. For group jobs the
	// persisted value is ignored on read; the runtime status is
	// derived from descendants (failed > running > pending >
	// succeeded).
	Status string `json:"status"`

	// StartedAt — UTC instant the Job first transitioned out of
	// pending. nil while pending. For groups this is the earliest
	// startedAt across all descendants.
	StartedAt *time.Time `json:"startedAt,omitempty"`

	// FinishedAt — UTC instant the Job reached a terminal state.
	// nil until succeeded/failed. For groups this is the latest
	// finishedAt across all descendants, populated only when every
	// descendant has terminated.
	FinishedAt *time.Time `json:"finishedAt,omitempty"`

	// DurationMs — milliseconds between StartedAt and FinishedAt.
	// For groups: derived from the group's StartedAt/FinishedAt the
	// store recomputes at read time. For leaf jobs running in flight,
	// the bridge stamps the elapsed-so-far value.
	DurationMs int64 `json:"durationMs"`

	// LatestExecutionID — id of the most-recent Execution for this
	// Job, empty until the first attempt starts. The frontend uses
	// this to deep-link to the GitLab-style log viewer without
	// having to load the full Execution list first. Empty for group
	// jobs (groups don't run Executions of their own).
	LatestExecutionID string `json:"latestExecutionId,omitempty"`

	// ChildIDs — full IDs of jobs whose ParentID == this Job's ID.
	// DERIVED at read time by Store.ListJobs / Store.GetJob; never
	// persisted. Empty for leaf install jobs.
	ChildIDs []string `json:"childIds,omitempty"`

	// LegacyBatchID — read-only migration field. Pre-#351 indexes
	// persisted the deprecated `batchId` denormalisation; loadIndex
	// hoists any non-empty legacy value into ParentID
	// (= JobID(deploymentID, batchID)) and deriveTreeView synthesizes
	// the matching parent group Job in-memory when no on-disk row
	// exists. Stripped from every persistIndex write so the on-disk
	// record stays canonical (one source of truth: ParentID).
	LegacyBatchID string `json:"batchId,omitempty"`
}

// Execution captures one attempt of a Job. The store appends a new
// Execution every time the Job transitions back into running from a
// terminal state (Day-2 retry flows; Phase-1 installs typically have
// exactly one Execution per Job). Group jobs have no Executions.
type Execution struct {
	// ID — opaque identifier, hex-encoded random bytes. Globally
	// unique within a deployment.
	ID string `json:"id"`

	// JobID — parent Job stable id ("<deploymentId>:<jobName>").
	JobID string `json:"jobId"`

	// DeploymentID — parent deployment id, denormalised so the
	// /executions/{execId}/logs endpoint can resolve the log file
	// path without a Job lookup.
	DeploymentID string `json:"deploymentId"`

	// Status — running|succeeded|failed (pending is reserved for the
	// Job aggregate; an Execution is always at least running).
	Status string `json:"status"`

	// StartedAt — UTC instant the Execution attempt began.
	StartedAt time.Time `json:"startedAt"`

	// FinishedAt — UTC instant the Execution reached terminal. nil
	// while still running.
	FinishedAt *time.Time `json:"finishedAt,omitempty"`

	// LineCount — total LogLines appended to this Execution's NDJSON
	// log file. The /logs endpoint compares fromLine against this to
	// derive `total` and pagination boundaries.
	LineCount int `json:"lineCount"`
}

// LogLine is one record in an Execution's append-only NDJSON file.
// LineNumber is 1-indexed — the GitLab CI runner viewer the frontend
// renders keys off it for the gutter.
type LogLine struct {
	// LineNumber — 1-indexed. The store stamps this on append.
	LineNumber int `json:"lineNumber"`

	// Timestamp — RFC3339Nano in UTC.
	Timestamp time.Time `json:"timestamp"`

	// Level — INFO|DEBUG|WARN|ERROR. See package consts.
	Level string `json:"level"`

	// Message — the rendered log line. The frontend strips ANSI on
	// its own; the store does not parse or re-encode.
	Message string `json:"message"`
}

// Index is the on-disk shape of <depId>/index.json. Holds Job +
// Execution metadata; log lines live in the per-execution NDJSON
// files. Persisted via atomic temp+rename through Store.persistIndex.
type Index struct {
	// DeploymentID — denormalised so a stray index.json file is
	// self-describing.
	DeploymentID string `json:"deploymentId"`

	// Jobs — all Jobs for this deployment, in insertion order. The
	// API handler sorts on read (started-at DESC, pending last).
	Jobs []Job `json:"jobs"`

	// Executions — all Executions across all Jobs. The API handler
	// filters by JobID on the per-job endpoint. Stored flat so a
	// per-execution mutation only rewrites this slice once, not a
	// nested per-Job list.
	Executions []Execution `json:"executions"`
}

// JobID — synthesises the stable per-deployment Job id. Exported so
// the helmwatch bridge AND the API handler agree on the format.
func JobID(deploymentID, jobName string) string {
	return deploymentID + ":" + jobName
}

// RegionFromComponent extracts the region key from a multi-region
// component id. Secondary regions' components arrive as
// "<region>:<chart>" (e.g. "me-east-215-b-1:cilium" — the region key
// itself may contain hyphens but never a colon, while chart names are
// bare bp-* slugs without a colon). The region is everything before the
// FIRST colon; a component with no colon is a primary-region row and
// returns "".
//
// Kept in lockstep with the UI's regionFromJob() (JobsTable.tsx) and
// the chroot fan-out's snapshotsToSeedsForRegion() prefix convention so
// the Go-stamped Region field and the FE-derived region agree exactly.
func RegionFromComponent(component string) string {
	i := strings.IndexByte(component, ':')
	if i <= 0 {
		return ""
	}
	return component[:i]
}
