// Package jobs implements the Jobs/Executions data model + persistence
// the catalyst-api Sovereign Admin surfaces consume (issue #205, sub of
// epic #204).
//
// # Architecture
//
// Each `bp-<chart>` HelmRelease the Phase-1 helmwatch observes maps 1:1
// to a Job (jobName="install-<chart>", appId="<chart>",
// batchId="bootstrap-kit"). Each watch attempt the helmwatch emits is
// an Execution; LogLines append to the active Execution's NDJSON log
// file as the helmwatch goroutine derives them from HelmRelease
// status.conditions.
//
// Two parallel feeds live for the same data:
//
//   - The existing `/api/v1/deployments/{id}/events` SSE feed — kept
//     untouched, the wizard's live banner reads it.
//   - The new Jobs/Executions REST surface — the table-view UX (issue
//     #204) reads it.
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

import "time"

// Status enums — kept in lockstep with helmwatch.State* via the
// translation in helmwatch_bridge.go. The wire contract uses these
// strings verbatim (frontend agents code against the spec in #205).
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

// BatchID — the only batch the bootstrap-kit currently emits. Future
// batches (Phase-2 component installs, Day-2 Crossplane reconciles)
// will introduce additional batch ids; this constant is the canonical
// "Phase-1 install" batch tag.
const BatchBootstrapKit = "bootstrap-kit"

// JobNamePrefix — every Phase-1 Job is named "install-<chart>". The
// helmwatch bridge derives this from the HelmRelease metadata.name
// ("bp-foo" → "install-foo").
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

// Job is the wire-contract Job shape. The store materialises one Job
// per `bp-<chart>` HelmRelease; the helmwatch bridge keeps its state
// in sync as conditions transition.
//
// Fields use omitempty for nullable timestamps so the JSON shape the
// frontend sees matches the spec verbatim.
type Job struct {
	// ID is the stable identifier "<deploymentId>:<jobName>". It is
	// the URL-safe id the GET /jobs/{jobId} endpoint accepts.
	ID string `json:"id"`

	// DeploymentID — the parent deployment the Job belongs to.
	DeploymentID string `json:"deploymentId"`

	// JobName — "install-<chart>", e.g. "install-cilium".
	JobName string `json:"jobName"`

	// AppID — the Sovereign component id, e.g. "cilium". Equals
	// helmwatch.ComponentIDFromHelmRelease(HR.metadata.name).
	AppID string `json:"appId"`

	// BatchID — currently always BatchBootstrapKit; reserved for
	// future Day-2 batches.
	BatchID string `json:"batchId"`

	// DependsOn — list of jobNames this Job depends on. Derived from
	// the HelmRelease's `spec.dependsOn[*].name` with the bp- prefix
	// stripped and "install-" prepended (e.g. spec.dependsOn entry
	// `name: bp-cilium` → DependsOn entry `install-cilium`).
	DependsOn []string `json:"dependsOn"`

	// Status — pending|running|succeeded|failed. See package consts.
	Status string `json:"status"`

	// StartedAt — UTC instant the Job first transitioned out of
	// pending. nil while the Job is still pending.
	StartedAt *time.Time `json:"startedAt,omitempty"`

	// FinishedAt — UTC instant the Job reached a terminal state. nil
	// until the Job is succeeded or failed.
	FinishedAt *time.Time `json:"finishedAt,omitempty"`

	// DurationMs — milliseconds between StartedAt and FinishedAt.
	// Zero while either is nil.
	DurationMs int64 `json:"durationMs"`

	// LatestExecutionID — id of the most-recent Execution for this
	// Job, empty until the first attempt starts. The frontend uses
	// this to deep-link to the GitLab-style log viewer without
	// having to load the full Execution list first.
	LatestExecutionID string `json:"latestExecutionId,omitempty"`
}

// Execution captures one attempt of a Job. The store appends a new
// Execution every time the Job transitions back into running from a
// terminal state (Day-2 retry flows; Phase-1 installs typically have
// exactly one Execution per Job).
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

// BatchSummary is the wire-contract row for the
// /api/v1/deployments/{depId}/jobs/batches endpoint. The handler
// computes this on read by aggregating Job statuses keyed by BatchID.
type BatchSummary struct {
	// BatchID — e.g. "bootstrap-kit".
	BatchID string `json:"batchId"`

	// Total — number of Jobs in this batch.
	Total int `json:"total"`

	// Finished — number of Jobs in a terminal state (succeeded |
	// failed). Equals Succeeded + Failed.
	Finished int `json:"finished"`

	// Succeeded — number of Jobs with Status=succeeded.
	Succeeded int `json:"succeeded"`

	// Failed — number of Jobs with Status=failed.
	Failed int `json:"failed"`

	// Running — number of Jobs with Status=running.
	Running int `json:"running"`

	// Pending — number of Jobs with Status=pending.
	Pending int `json:"pending"`
}

// JobID — synthesises the stable per-deployment Job id. Exported so
// the helmwatch bridge AND the API handler agree on the format.
func JobID(deploymentID, jobName string) string {
	return deploymentID + ":" + jobName
}
