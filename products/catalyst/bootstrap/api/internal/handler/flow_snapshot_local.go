// Package handler — flow_snapshot_local.go: assemble an OpenovaFlow
// FlowMessage snapshot from catalyst-api's local jobs.Store so the
// mothership canvas at /sovereign/provision/<id>/jobs renders every
// mothership-owned Phase-0 + Phase-1 Job (tofu-init/plan/apply/output,
// flux-bootstrap, install-bp-<chart>, cutover, handover, ...) as a
// FlowNode FROM T+0, regardless of whether the chroot's own
// openova-flow-server is reachable.
//
// Architecture rationale:
//
//   - catalyst-api OWNS the Phase-0 OpenTofu lifecycle Jobs AND the
//     Phase-1 install-bp-<chart> Jobs (helmwatch.Bridge writes them
//     into jobs.Store via the canonical helmwatch_bridge.go hook). The
//     URL the operator clicks (/jobs/<deploymentId>:tofu-apply) carries
//     the legacy <deploymentId>:<jobName> id form — the same form the
//     jobs.Store keys Jobs on (Job.ID = JobID(deploymentID, jobName)).
//
//   - The OpenovaFlow canvas at FlowCanvasOrganic reads
//     /api/v1/flows/<deploymentId>/snapshot for initial paint and
//     subscribes to /stream for live updates. Before this file the
//     snapshot handler always proxied to https://openova-flow.<sovereignFQDN>
//     which can't serve until cilium + cert-manager + the HTTPRoute
//     TLS cert are all up on the chroot (~T+30m). Result: the
//     mothership-owned Jobs the operator wants to watch live were
//     invisible for the first half-hour.
//
//   - We don't need a chroot adapter to render those Jobs. The data
//     is already in jobs.Store, updated by helmwatch.Bridge for every
//     HelmRelease state transition AND by the provisioner.go lifecycle
//     emit() for Phase-0 phase changes. We translate it on-read into
//     the canonical FlowMessage envelope shape.
//
// Wire format: the assembled envelope matches products/openova-flow/
// adapter-flux/internal/types/flow.go FlowMessage — `nodes` are
// FlowNode with id=Job.ID, label=Job.JobName, status=Job.Status,
// `relationships` are `finish-to-start` edges for Job.DependsOn AND
// `contains` edges for Job.ParentID. Same shape the chroot's adapter
// emits, so when the chroot DOES come up later the operator can switch
// to the chroot path without any FE change.
//
// docs/INVIOLABLE-PRINCIPLES.md:
//   - #1 (target-state) — full envelope, not a stripped stub. Every
//     Job becomes a FlowNode; every DependsOn becomes a Relationship.
//   - #2 (no compromise) — same wire shape the chroot adapter uses, so
//     the canvas reducer doesn't branch on data source.
//   - #4 (never hardcode) — no region/FQDN literal; the flow id is
//     the deployment id verbatim, the relationship types are the
//     canonical RelationshipType enum from products/openova-flow/core.

package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

// flowSnapshotLocalNode mirrors products/openova-flow/adapter-flux/
// internal/types/flow.go FlowNode — declared inline here so the
// handler doesn't take a dependency on the producer-side adapter
// module. Field names match the wire JSON exactly.
type flowSnapshotLocalNode struct {
	ID        string  `json:"id"`
	FlowID    string  `json:"flowId"`
	Label     string  `json:"label"`
	Status    string  `json:"status"`
	Family    *string `json:"family,omitempty"`
	Region    *string `json:"region,omitempty"`
	StartedAt *int64  `json:"startedAt,omitempty"`
	EndedAt   *int64  `json:"endedAt,omitempty"`
}

// flowSnapshotLocalRelationship mirrors the same types/flow.go
// Relationship — see node comment for rationale.
type flowSnapshotLocalRelationship struct {
	FromID string `json:"fromId"`
	ToID   string `json:"toId"`
	Type   string `json:"type"`
}

// flowSnapshotLocalFlow mirrors FlowInstance.
type flowSnapshotLocalFlow struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	StartedAt int64  `json:"startedAt"`
}

// flowSnapshotLocalMessage mirrors FlowMessage.
type flowSnapshotLocalMessage struct {
	Type          string                          `json:"type"`
	Flow          *flowSnapshotLocalFlow          `json:"flow,omitempty"`
	Nodes         []flowSnapshotLocalNode         `json:"nodes,omitempty"`
	Relationships []flowSnapshotLocalRelationship `json:"relationships,omitempty"`
}

// jobStatusToFlowStatus maps the jobs.Store status enum (pending |
// running | succeeded | failed) to the OpenovaFlow status convention
// the canvas keys off. The values overlap by design — but we route
// through this function so a future divergence stays contained.
func jobStatusToFlowStatus(jobStatus string) string {
	switch strings.ToLower(strings.TrimSpace(jobStatus)) {
	case jobs.StatusSucceeded:
		return "succeeded"
	case jobs.StatusFailed:
		return "failed"
	case jobs.StatusRunning:
		return "running"
	case jobs.StatusPending:
		return "pending"
	}
	return "pending"
}

// jobFamilyForJob returns a coarse `family` tag the canvas can use to
// colour-group bubbles (Phase-0 vs Phase-1-install vs cutover vs
// handover vs day-2). Optional — passes through as nil for jobs whose
// JobName doesn't match a known family. Inspect-able by the operator
// via the bubble tooltip.
func jobFamilyForJob(j jobs.Job) *string {
	name := j.JobName
	switch {
	case j.Type == jobs.JobTypeGroup:
		s := "group"
		return &s
	case strings.HasPrefix(name, jobs.JobNamePrefix): // "install-<chart>"
		s := "install"
		return &s
	case name == jobs.PhaseTofuInit || name == jobs.PhaseTofuPlan ||
		name == jobs.PhaseTofuApply || name == jobs.PhaseTofuOutput:
		s := "tofu"
		return &s
	case name == jobs.PhaseClusterBootstrap:
		s := "bootstrap"
		return &s
	case strings.HasPrefix(name, "cutover"):
		s := "cutover"
		return &s
	case strings.HasPrefix(name, "handover"):
		s := "handover"
		return &s
	}
	return nil
}

// flowSnapshotFromJobs builds a FlowMessage `snapshot` envelope from
// the catalyst-api jobs.Store for the given deployment id. Returns
// `nil, false` when the store is unconfigured OR when no Jobs are
// known for this deployment (caller should fall through to the
// upstream openova-flow-server proxy path).
//
// All Job.ID values are kept verbatim — the canvas drill-down resolves
// /jobs/<jobId> by exact id match, and the legacy form
// "<deploymentId>:<jobName>" is exactly what jobs.JobID emits.
func (h *Handler) flowSnapshotFromJobs(deploymentID string) (*flowSnapshotLocalMessage, bool) {
	st := h.jobsStore()
	if st == nil {
		return nil, false
	}
	js, err := st.ListJobs(deploymentID)
	if err != nil || len(js) == 0 {
		return nil, false
	}

	// FlowInstance.StartedAt — earliest non-zero Job.StartedAt across
	// the deployment. If every Job is still pending (no StartedAt
	// set), default to 0 — the canvas just shows the flow as "newly
	// open" without a duration column entry.
	var earliestUnix int64
	flowStatus := "running"
	allTerminal := true
	anyFailed := false
	for _, j := range js {
		if j.StartedAt != nil {
			t := j.StartedAt.Unix()
			if earliestUnix == 0 || t < earliestUnix {
				earliestUnix = t
			}
		}
		if !jobs.IsTerminal(j.Status) {
			allTerminal = false
		}
		if j.Status == jobs.StatusFailed {
			anyFailed = true
		}
	}
	if allTerminal {
		if anyFailed {
			flowStatus = "failed"
		} else {
			flowStatus = "succeeded"
		}
	}

	nodes := make([]flowSnapshotLocalNode, 0, len(js))
	rels := make([]flowSnapshotLocalRelationship, 0, len(js)*2)
	for _, j := range js {
		n := flowSnapshotLocalNode{
			ID:     j.ID,
			FlowID: deploymentID,
			Label:  jobDisplayLabel(j),
			Status: jobStatusToFlowStatus(j.Status),
			Family: jobFamilyForJob(j),
		}
		if j.StartedAt != nil {
			t := j.StartedAt.Unix()
			n.StartedAt = &t
		}
		if j.FinishedAt != nil {
			t := j.FinishedAt.Unix()
			n.EndedAt = &t
		}
		nodes = append(nodes, n)
		// Hierarchy edge — parent contains child. Skipped for
		// top-level Jobs whose ParentID is empty (root group jobs).
		if j.ParentID != "" {
			rels = append(rels, flowSnapshotLocalRelationship{
				FromID: j.ParentID,
				ToID:   j.ID,
				Type:   "contains",
			})
		}
		// Dependency edges — finish-to-start arrows from upstream
		// installs to this job. jobs.Bridge already normalises the
		// dep ids into the JobID(deploymentID, "install-<chart>")
		// form, so we copy them verbatim.
		for _, dep := range j.DependsOn {
			if dep == "" || dep == j.ID {
				continue
			}
			rels = append(rels, flowSnapshotLocalRelationship{
				FromID: dep,
				ToID:   j.ID,
				Type:   "finish-to-start",
			})
		}
	}

	return &flowSnapshotLocalMessage{
		Type: "snapshot",
		Flow: &flowSnapshotLocalFlow{
			ID:        deploymentID,
			Status:    flowStatus,
			StartedAt: earliestUnix,
		},
		Nodes:         nodes,
		Relationships: rels,
	}, true
}

// jobDisplayLabel returns the user-visible label for a Job's FlowNode.
// Prefers DisplayName (group jobs set this; the helmwatch bridge
// leaves it empty for leaf install jobs which renders the slug).
func jobDisplayLabel(j jobs.Job) string {
	if strings.TrimSpace(j.DisplayName) != "" {
		return j.DisplayName
	}
	return j.JobName
}

// flowStreamLocal serves the SSE `/stream` endpoint from the local
// jobs.Store — emit a `snapshot` frame on connect AND every 3 seconds
// thereafter. The OpenovaFlow consumer's reducer is idempotent on
// repeated snapshots so re-emitting an unchanged envelope costs only
// the JSON encode + a few bytes on the wire; in exchange the canvas
// renders the latest Job state without a page refresh.
//
// Why poll, not subscribe: jobs.Store does NOT publish update
// notifications today (intentional — Issue #205 left subscribe out
// of v1 to keep the API surface small). The poll cadence is shorter
// than a typical browser reconnect delay so the apparent latency
// from a Job transition to the canvas updating is ≤ 3s. When
// jobs.Store grows a subscribe API in a future change, this loop
// flips to event-driven without touching the wire shape.
func (h *Handler) flowStreamLocal(w http.ResponseWriter, r *http.Request, flusher http.Flusher, deploymentID string) {
	// Canonical SSE response headers — must be set BEFORE the first
	// write byte so the EventSource handshake completes cleanly.
	// Mirrors openova_flow_proxy.go HandleFlowStream and
	// deployments.go StreamLogs (the canonical SSE pattern in this
	// codebase).
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	emit := func(eventName string, msg *flowSnapshotLocalMessage) bool {
		if msg == nil {
			return true
		}
		buf, err := json.Marshal(msg)
		if err != nil {
			return false
		}
		// Named SSE events — the FE's openflow-adapter-sse.ts
		// registers addEventListener for `snapshot`/`upsert-*`/etc.,
		// not the default `message`. Emit under the named event so
		// the canvas reducer dispatches correctly.
		if _, werr := w.Write([]byte("event: " + eventName + "\ndata: ")); werr != nil {
			return false
		}
		if _, werr := w.Write(buf); werr != nil {
			return false
		}
		if _, werr := w.Write([]byte("\n\n")); werr != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// First frame — guaranteed snapshot on connect.
	if msg, ok := h.flowSnapshotFromJobs(deploymentID); ok {
		if !emit("snapshot", msg) {
			return
		}
	}

	// Poll cadence — 3 seconds. The browser EventSource has its own
	// 1-2 second retry-on-error timer, so a 3-second emit cadence
	// keeps the gap between "transition observed" and "canvas
	// re-renders" within human-perceivable tolerance without
	// flooding the wire.
	const pollInterval = 3 * time.Second
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// Heartbeat to keep the SSE connection alive through any
	// intermediaries that idle-time TCP connections out (Cilium
	// Gateway, browser nat helpers). Mirrors the heartbeat path the
	// chroot openova-flow-server uses.
	const heartbeatInterval = 15 * time.Second
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			msg, ok := h.flowSnapshotFromJobs(deploymentID)
			if !ok {
				// Lost the deployment (e.g. wiped) — close the
				// stream so the FE EventSource reconnects and
				// re-runs the snapshot resolver.
				return
			}
			if !emit("snapshot", msg) {
				return
			}
		case <-heartbeat.C:
			if _, werr := w.Write([]byte("event: heartbeat\ndata: {}\n\n")); werr != nil {
				return
			}
			flusher.Flush()
		}
	}
}
