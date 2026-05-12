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

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
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

	// Pull spec.dependsOn from the PRIMARY region's live helmwatch
	// .Watcher's informer cache. jobs.Store does NOT persist
	// Job.DependsOn for Phase-1 install-* Jobs today (only the Phase-0
	// tofu chain + cluster-bootstrap gets dep wiring). Without this
	// every primary install-* bubble renders disconnected.
	//
	// Multi-region: separately, secondary regions each have their own
	// helmwatch.Watcher (dep.secondaryWatchers) whose components are
	// emitted below as REGION-PREFIXED FlowNodes so the canvas shows
	// install-* HRs from EVERY region. They do NOT contribute to
	// hrDeps used for the primary-leaf-only dep derivation — each
	// region's intra-cluster deps are computed against its OWN
	// snapshot inside the per-region block below.
	hrDeps := map[string][]string{}
	var secondaryWatchers map[string]*helmwatch.Watcher
	if val, ok := h.deployments.Load(deploymentID); ok {
		if dep, ok := val.(*Deployment); ok && dep != nil {
			dep.mu.Lock()
			w := dep.liveWatcher
			// Snapshot the secondaryWatchers map under the lock; the
			// per-watcher SnapshotComponents() calls below are
			// individually goroutine-safe.
			if len(dep.secondaryWatchers) > 0 {
				secondaryWatchers = make(map[string]*helmwatch.Watcher, len(dep.secondaryWatchers))
				for r, sw := range dep.secondaryWatchers {
					secondaryWatchers[r] = sw
				}
			}
			dep.mu.Unlock()
			if w != nil {
				for _, cs := range w.SnapshotComponents() {
					if len(cs.DependsOn) > 0 {
						hrDeps[cs.AppID] = cs.DependsOn
					}
				}
			}
		}
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
		// Hierarchy edge — `contains` per OpenovaFlow canon (see
		// products/openova-flow/core/src/types.ts line 112):
		// "`toId` (parent) contains `fromId` (child)". So the child
		// id goes in FromID and the parent in ToID — NOT the
		// intuitive "parent → child" reading. Skipped for top-level
		// Jobs whose ParentID is empty (root group jobs themselves).
		if j.ParentID != "" {
			rels = append(rels, flowSnapshotLocalRelationship{
				FromID: j.ID,
				ToID:   j.ParentID,
				Type:   "contains",
			})
		}
		// Dependency edges — finish-to-start arrows from upstream
		// installs to this job. helmwatch.Bridge currently writes
		// SOME Job.DependsOn entries as bare names ("install-flux")
		// rather than the canonical JobID form
		// ("<deploymentId>:install-flux"). Either form is valid as
		// the persistence-side key the Bridge uses internally, but
		// the canvas reducer matches FlowNode.id by exact string, so
		// a bare-name fromId becomes a phantom edge to a non-existent
		// node — which the layout then routes through the nearest
		// real bubbles, manifesting as spurious 5-edge fan-outs from
		// Phase-0 tofu jobs to every install-* bubble. Normalise
		// every dep id to the canonical
		// jobs.JobID(deploymentID, jobName) form before emitting.
		seenDep := map[string]bool{}
		for _, dep := range j.DependsOn {
			if dep == "" {
				continue
			}
			normalised := dep
			if !strings.Contains(dep, ":") {
				normalised = jobs.JobID(deploymentID, dep)
			}
			if normalised == j.ID || seenDep[normalised] {
				continue
			}
			seenDep[normalised] = true
			rels = append(rels, flowSnapshotLocalRelationship{
				FromID: normalised,
				ToID:   j.ID,
				Type:   "finish-to-start",
			})
		}
		// Layer-2 dependency derivation — helmwatch.Bridge does NOT
		// persist Job.DependsOn for Phase-1 install-* Jobs today, but
		// the live HR informer cache HAS the data (HR.spec.dependsOn).
		// For each install-<chart> Job, look up the chart's AppID and
		// emit finish-to-start edges to its sibling install-* Jobs.
		// Skipped for group jobs (j.AppID empty) and when the live
		// watcher hasn't attached yet.
		if j.AppID != "" {
			for _, depAppID := range hrDeps[j.AppID] {
				if depAppID == "" {
					continue
				}
				depJobID := jobs.JobID(deploymentID, jobs.JobNamePrefix+depAppID)
				if depJobID == j.ID || seenDep[depJobID] {
					continue
				}
				seenDep[depJobID] = true
				rels = append(rels, flowSnapshotLocalRelationship{
					FromID: depJobID,
					ToID:   j.ID,
					Type:   "finish-to-start",
				})
			}
		}
	}

	// Group-level sequential edge — `provisioner` (Phase-0 tofu chain)
	// must complete before `bootstrap-kit` (Phase-1 Flux reconcile)
	// starts. This is the real temporal relationship between the two
	// top-level groups. At ?depth=1 both groups render folded and this
	// edge correctly shows the ordering between them. At ?depth=all
	// the FE layout would normally LIFT this edge onto every child of
	// each elided group (flowLayoutOrganic.ts lines 414-442), causing
	// the spurious M×N fan-out the operator originally reported. The
	// matching FE fix in flowLayoutOrganic skips the lift when BOTH
	// endpoints of the elided-group edge are elided — so this edge is
	// safe to emit unconditionally.
	provisionerID := jobs.JobID(deploymentID, jobs.GroupProvisioner)
	bootstrapID := jobs.JobID(deploymentID, jobs.GroupBootstrapKit)
	hasProvisioner := false
	hasBootstrap := false
	for _, j := range js {
		if j.ID == provisionerID {
			hasProvisioner = true
		}
		if j.ID == bootstrapID {
			hasBootstrap = true
		}
	}
	if hasProvisioner && hasBootstrap {
		rels = append(rels, flowSnapshotLocalRelationship{
			FromID: provisionerID,
			ToID:   bootstrapID,
			Type:   "finish-to-start",
		})
	}

	// Multi-region — append one synthetic group bubble per secondary
	// region + one FlowNode per HR observed in that region's watcher
	// + contains edges parent→child + finish-to-start edges between
	// siblings (same-region only). Region tag is set so the canvas
	// can colour-group by region.
	if len(secondaryWatchers) > 0 {
		statusToFlow := func(state string) string {
			switch state {
			case "installed":
				return "succeeded"
			case "failed":
				return "failed"
			case "installing", "pending", "degraded":
				return "running"
			}
			return "pending"
		}
		for region, sw := range secondaryWatchers {
			if sw == nil {
				continue
			}
			snap := sw.SnapshotComponents()
			if len(snap) == 0 {
				continue
			}
			regionGroupID := deploymentID + ":" + region + ":bootstrap-kit"
			regionStr := region
			regionFamily := "group"
			nodes = append(nodes, flowSnapshotLocalNode{
				ID:     regionGroupID,
				FlowID: deploymentID,
				Label:  "Bootstrap (" + region + ")",
				Status: "running",
				Family: &regionFamily,
				Region: &regionStr,
			})
			// Hierarchy: this region's group is contained by the
			// top-level bootstrap-kit (so the canvas can fold all
			// regions under one parent).
			rels = append(rels, flowSnapshotLocalRelationship{
				FromID: regionGroupID,
				ToID:   bootstrapID,
				Type:   "contains",
			})

			// Index this region's components for intra-region dep edges.
			regionAppIDs := map[string]string{} // appID → full node id
			for _, cs := range snap {
				appID := cs.AppID
				if appID == "" {
					continue
				}
				nodeID := deploymentID + ":" + region + ":install-" + appID
				regionAppIDs[appID] = nodeID
			}

			installFamily := "install"
			for _, cs := range snap {
				appID := cs.AppID
				if appID == "" {
					continue
				}
				nodeID := regionAppIDs[appID]
				startedAt := int64(0)
				if !cs.LastTransitionAt.IsZero() {
					startedAt = cs.LastTransitionAt.Unix()
				}
				node := flowSnapshotLocalNode{
					ID:     nodeID,
					FlowID: deploymentID,
					Label:  "install-" + appID,
					Status: statusToFlow(cs.Status),
					Family: &installFamily,
					Region: &regionStr,
				}
				if startedAt > 0 {
					node.StartedAt = &startedAt
				}
				nodes = append(nodes, node)
				// Hierarchy
				rels = append(rels, flowSnapshotLocalRelationship{
					FromID: nodeID,
					ToID:   regionGroupID,
					Type:   "contains",
				})
				// Intra-region deps (same region only — DO NOT cross
				// region edges, since each region is an independent
				// fault domain per NAMING-CONVENTION §1.3).
				for _, depApp := range cs.DependsOn {
					depID, ok := regionAppIDs[depApp]
					if !ok {
						continue
					}
					rels = append(rels, flowSnapshotLocalRelationship{
						FromID: depID,
						ToID:   nodeID,
						Type:   "finish-to-start",
					})
				}
			}
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
