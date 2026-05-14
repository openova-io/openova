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
	// hrDeps is the Layer-2 fallback used when a Job row's stored
	// DependsOn is empty (e.g., the seed hook hadn't fired yet at the
	// moment a Job was auto-created by OnHelmReleaseEvent's first-event
	// path). Keys are bare AppIDs (primary watcher) AND region-prefixed
	// AppIDs (secondary watchers, "<region>/<chart>") so the Layer-2
	// lookup in the install-* leaf block below can find deps for any
	// region's Job. After fix #1467 (mergeJob preserves prev.DependsOn
	// + SeedJobsFromInformerList attached to secondary watchers) the
	// stored DependsOn IS the canonical source and this fallback is
	// effectively a safety net for hot-shipped charts.
	hrDeps := map[string][]string{}
	var secondaryWatchers map[string]*helmwatch.Watcher
	if val, ok := h.deployments.Load(deploymentID); ok {
		if dep, ok := val.(*Deployment); ok && dep != nil {
			dep.mu.Lock()
			w := dep.liveWatcher
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
			// Layer-2 enrichment from secondary watchers — each
			// secondary's HR.spec.dependsOn carries bare chart names,
			// so we region-prefix them BOTH on the map key AND the
			// list entries so the lookup below (lookupAppID built off
			// j.AppID which is already region-prefixed for secondaries)
			// finds the right region-bounded sibling.
			for region, sw := range secondaryWatchers {
				if sw == nil {
					continue
				}
				for _, cs := range sw.SnapshotComponents() {
					if len(cs.DependsOn) == 0 {
						continue
					}
					regionKey := region + ":" + cs.AppID
					rescoped := make([]string, 0, len(cs.DependsOn))
					for _, d := range cs.DependsOn {
						rescoped = append(rescoped, region+":"+d)
					}
					hrDeps[regionKey] = rescoped
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
	// Track secondary-region keys discovered from persisted Job names
	// like "install-nbg1-1/cilium" — emitted as `<region>/<chart>` by
	// spawnSecondaryRegionWatchers' callback in phase1_watch.go:457.
	// We synthesize one bootstrap-kit sub-group per region AFTER the
	// per-Job loop so the canvas can fold each region independently
	// AND so the temporal-endpoint cascade in flowLayoutOrganic.ts
	// stops at region boundaries (not at the flat bootstrap-kit set
	// of 135 leaves). Caught on prov #59 (a43364f11c10cde3, 2026-05-13):
	// after phase 1 terminates, dep.secondaryWatchers clears, the
	// multi-region snapshot block below (line ~363) becomes a no-op,
	// and all 135 secondary install Jobs render flat as direct children
	// of bootstrap-kit with the provision-hetzner→bootstrap-kit edge
	// fanning M×N onto every leaf. The persisted Job rows are the
	// durable source of truth — derive region from them.
	regionsFromJobs := map[string]bool{}
	bootstrapID := jobs.JobID(deploymentID, jobs.GroupBootstrapKit)
	cutoverID := jobs.JobID(deploymentID, jobs.GroupCutover)
	handoverID := jobs.JobID(deploymentID, jobs.GroupHandover)
	appsID := jobs.JobID(deploymentID, jobs.GroupApps)

	// phaseForChart classifies an install-* job's chart name into one of
	// the five canonical Sovereign-lifecycle phases. The composer uses
	// this to reparent self-sovereign-cutover under the cutover group
	// instead of bootstrap-kit, so the canvas surfaces cutover as a
	// distinct first-class phase rather than burying it inside the
	// 45-HR install fan-out. Charts NOT classified here fall through to
	// bootstrap-kit (the default phase for the install wave).
	//
	// Chart name is the post-`install-` slug (and post-`<region>/` for
	// secondary regions), so the caller must strip both before calling.
	phaseForChart := func(chart string) string {
		switch chart {
		case "self-sovereign-cutover":
			return jobs.GroupCutover
		}
		return jobs.GroupBootstrapKit
	}

	// Primary region key from deployment request — primary install jobs
	// have NO "/" prefix in their JobName (they're written by the
	// primary helmwatch.Bridge with bare bp-<chart> names), so we MUST
	// derive the primary region label from dep.Request.Region. Without
	// this the primary's 45 install jobs render as bare leaves directly
	// under bootstrap-kit while secondaries are nested in region groups
	// → asymmetric canvas (caught on prov #65, 2026-05-13). With this,
	// primary's install jobs get a <region> tag + reparent under a
	// `<deploymentId>:<primaryRegion>:bootstrap-kit` group so all
	// regions render symmetrically as siblings under bootstrap-kit.
	primaryRegion := ""
	if val, ok := h.deployments.Load(deploymentID); ok {
		if dep, ok := val.(*Deployment); ok && dep != nil {
			primaryRegion = dep.Request.Region
		}
	}

	for _, j := range js {
		// Derive region prefix from JobName of the form
		// "install-<region>:<chart>". The ":" separator is the
		// canonical multi-region marker emitted by phase1_watch.go.
		// Primary install jobs have no `:` in AppID — fall through to
		// the primary region from dep.Request below. (NB. the legacy
		// "/" separator was retired post-prov #73 because TanStack
		// Router decodes %2F → / and splits the route param.)
		jobRegion := ""
		if j.Type == jobs.JobTypeInstall && j.AppID != "" {
			if sep := strings.IndexByte(j.AppID, ':'); sep > 0 {
				jobRegion = j.AppID[:sep]
				regionsFromJobs[jobRegion] = true
			} else if primaryRegion != "" {
				// Primary region install job — reparent into the
				// primary region group for symmetry with secondaries.
				jobRegion = primaryRegion
				regionsFromJobs[jobRegion] = true
			}
		}
		n := flowSnapshotLocalNode{
			ID:     j.ID,
			FlowID: deploymentID,
			Label:  jobDisplayLabel(j),
			Status: jobStatusToFlowStatus(j.Status),
			Family: jobFamilyForJob(j),
		}
		if jobRegion != "" {
			rs := jobRegion
			n.Region = &rs
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
		//
		// For multi-region install Jobs (JobName "install-<region>/<chart>"),
		// REPARENT under the synthesized "<deploymentId>:<region>:bootstrap-kit"
		// group so the canvas's organic layout renders each region as
		// its own sub-bubble. The region groups themselves are
		// synthesised after this loop and `contains`-edged under
		// bootstrap-kit, so the tree shape is:
		//   bootstrap-kit
		//   ├── 45 primary install-* (legacy parent)
		//   ├── <region-A>:bootstrap-kit ── 45 install-*
		//   └── <region-B>:bootstrap-kit ── 45 install-*
		// The temporal-endpoint cascade in flowLayoutOrganic then
		// finds region-bounded initials/terminals instead of fanning
		// out M×N across all 135 leaves.
		parentID := j.ParentID
		// Classify install-* Jobs by the phase their chart belongs to.
		// Cutover-owning chart (self-sovereign-cutover) reparents under
		// the cutover phase group so the canvas surfaces cutover as a
		// distinct top-level phase, not buried in bootstrap-kit's
		// 45-HR install fan-out. For multi-region cutover charts, we
		// also keep them out of the bootstrap-kit region group (they're
		// a separate phase that runs once per region).
		phaseSlug := jobs.GroupBootstrapKit
		if j.Type == jobs.JobTypeInstall && j.AppID != "" {
			// Strip region prefix to get bare chart name for classification.
			bareChart := j.AppID
			if sep := strings.IndexByte(bareChart, ':'); sep > 0 {
				bareChart = bareChart[sep+1:]
			}
			phaseSlug = phaseForChart(bareChart)
		}
		if jobRegion != "" && phaseSlug == jobs.GroupBootstrapKit {
			parentID = deploymentID + ":" + jobRegion + ":bootstrap-kit"
		} else if jobRegion != "" && phaseSlug != jobs.GroupBootstrapKit {
			// Cutover/handover/apps go under per-region phase sub-group
			// (e.g. `<dep>:hel1-2:cutover`) so canvas surfaces each
			// region's cutover progress independently — region-bounded
			// just like bootstrap-kit.
			parentID = deploymentID + ":" + jobRegion + ":" + phaseSlug
		}
		if parentID != "" {
			rels = append(rels, flowSnapshotLocalRelationship{
				FromID: j.ID,
				ToID:   parentID,
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
		// Dep-region helper: if this Job is regional (jobRegion != ""),
		// every dep we emit MUST be in the SAME region. The bridge
		// persists secondary-region Jobs with bare dep names like
		// "install-cilium" (NOT "install-<region>/cilium") because
		// HR.spec.dependsOn carries chart names, not region-prefixed
		// names. Without the region prefix injection below, the
		// normaliser produces `<dep>:install-cilium` which is the
		// PRIMARY's cilium job → every secondary install renders a
		// cross-region edge to primary. Caught on prov #66 (2026-05-13)
		// where hel1-2 install jobs all pointed at primary's installs.
		// Per NAMING-CONVENTION §1.3 secondary regions are independent
		// fault domains — NO cross-region edges. Fix: when jobRegion
		// is non-empty AND the dep name starts with "install-" without
		// a region prefix, inject the region into the dep id.
		// isSecondaryRegionJob: true when this Job is a secondary
		// region install (j.AppID contains "<region>:<chart>"). False
		// for primary install jobs even though they are now rendered
		// under a primary region sub-group (jobRegion derived from
		// dep.Request.Region for symmetric layout). The distinction
		// matters because primary jobs have BARE JobNames
		// ("install-cilium") with bare-form DependsOn entries
		// ("install-cilium"), while secondary jobs have prefixed
		// JobNames ("install-hel1-2:cilium") with prefixed-form
		// DependsOn entries ("install-hel1-2:cilium"). Region-
		// prefixing a primary dep produces a phantom JobID that no
		// node matches.
		isSecondaryRegionJob := strings.IndexByte(j.AppID, ':') > 0

		regionalise := func(depName string) string {
			if !isSecondaryRegionJob {
				return depName // primary job — JobName is bare, deps stay bare
			}
			// Secondary region. If depName is already region-prefixed
			// ("install-<region>:<chart>") leave it; otherwise inject
			// the region prefix so the dep resolves to the same
			// region's sibling install Job.
			if strings.HasPrefix(depName, jobs.JobNamePrefix) {
				rest := strings.TrimPrefix(depName, jobs.JobNamePrefix)
				if strings.IndexByte(rest, ':') > 0 {
					return depName // already region-prefixed
				}
				return jobs.JobNamePrefix + jobRegion + ":" + rest
			}
			return depName
		}

		seenDep := map[string]bool{}
		// fullJobIDPrefix is "<deploymentID>:" — used to detect whether
		// a stored dep entry is already a fully-qualified JobID. The
		// previous heuristic `!strings.Contains(dep, ":")` mis-classified
		// region-prefixed JobNames ("install-hel1-2:cilium") as
		// already-full-JobIDs because they contain a colon — yielding
		// invalid FromIDs without the deploymentID prefix.
		fullJobIDPrefix := deploymentID + ":"
		for _, dep := range j.DependsOn {
			if dep == "" {
				continue
			}
			normalised := dep
			if !strings.HasPrefix(dep, fullJobIDPrefix) {
				// Bare jobName form ("install-cilium" or
				// "install-hel1-2:cilium") — apply region scoping then
				// prefix with deploymentID.
				normalised = jobs.JobID(deploymentID, regionalise(dep))
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
		// Layer-2 dependency derivation — fallback used when the Job
		// row's stored DependsOn is empty (e.g., a chart was hot-shipped
		// post-seed, or the seed hook hadn't fired yet at the moment a
		// Job was auto-created by OnHelmReleaseEvent). Reads from
		// hrDeps which is populated from BOTH the primary watcher
		// (bare AppID keys, bare AppID values) AND every secondary
		// watcher (region-prefixed keys + region-prefixed values).
		//
		// Skipped for group jobs (j.AppID empty).
		if j.AppID != "" {
			// hrDeps now keys regional entries under "<region>/<chart>"
			// directly, so j.AppID is the correct lookup string for
			// both primary ("cilium") and secondary ("hel1-2/cilium").
			for _, depAppID := range hrDeps[j.AppID] {
				if depAppID == "" {
					continue
				}
				// For PRIMARY (j.AppID is bare), depAppID is also bare —
				// regionalise() is a no-op so the install-<chart> form
				// is final. For SECONDARY (j.AppID is "<region>/chart"),
				// depAppID is already "<region>/<dep-chart>" so
				// regionalise() detects the "/" and returns the value
				// unchanged. Either branch yields the correct region-
				// bounded sibling JobID.
				depJobID := jobs.JobID(deploymentID, regionalise(jobs.JobNamePrefix+depAppID))
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

	// Synthesise one phase sub-group per discovered region for each of
	// bootstrap-kit + cutover. These node IDs match the parent IDs the
	// per-Job loop above re-parented multi-region install Jobs to.
	// Without this post-loop append, those re-parent edges would
	// dangle (the FE adapter's `contains` index would point at IDs
	// with no corresponding node row).
	//
	// Status "running" is a placeholder — the FE rolls per-group
	// status up from descendants on the read path.
	{
		regionFamily := "group"
		phaseLabels := map[string]string{
			jobs.GroupBootstrapKit: "Bootstrap",
			jobs.GroupCutover:      "Cutover",
			jobs.GroupHandover:     "Handover",
			jobs.GroupApps:         "Apps",
		}
		phaseRoots := map[string]string{
			jobs.GroupBootstrapKit: bootstrapID,
			jobs.GroupCutover:      cutoverID,
			jobs.GroupHandover:     handoverID,
			jobs.GroupApps:         appsID,
		}
		// Emit per-region sub-groups for every phase, so the canvas
		// shows symmetric region bubbles inside each phase.
		for region := range regionsFromJobs {
			for phaseSlug, phaseLabel := range phaseLabels {
				regionGroupID := deploymentID + ":" + region + ":" + phaseSlug
				regionStr := region
				nodes = append(nodes, flowSnapshotLocalNode{
					ID:     regionGroupID,
					FlowID: deploymentID,
					Label:  phaseLabel + " (" + region + ")",
					Status: "running",
					Family: &regionFamily,
					Region: &regionStr,
				})
				rels = append(rels, flowSnapshotLocalRelationship{
					FromID: regionGroupID,
					ToID:   phaseRoots[phaseSlug],
					Type:   "contains",
				})
			}
		}
	}

	// Synthesise the three additional top-level phase groups
	// (cutover, handover, apps) so the canvas always shows the full
	// five-phase lifecycle (provisioner + bootstrap-kit + cutover +
	// handover + apps) at depth=1. Sequential edges below wire the
	// canonical ordering. Empty phase groups (e.g. handover/apps for a
	// fresh prov that hasn't reached those phases yet) render as
	// pending bubbles waiting for content — far more honest than
	// hiding them entirely.
	{
		groupFamily := "group"
		extraPhases := []struct {
			id      string
			slug    string
			display string
		}{
			{cutoverID, jobs.GroupCutover, jobs.GroupCutoverDisplay},
			{handoverID, jobs.GroupHandover, jobs.GroupHandoverDisplay},
			{appsID, jobs.GroupApps, jobs.GroupAppsDisplay},
		}
		// Skip group nodes already present (the Bridge may have
		// materialised one already via ensureGroupJob).
		existingIDs := map[string]bool{}
		for _, n := range nodes {
			existingIDs[n.ID] = true
		}
		for _, gp := range extraPhases {
			if existingIDs[gp.id] {
				continue
			}
			nodes = append(nodes, flowSnapshotLocalNode{
				ID:     gp.id,
				FlowID: deploymentID,
				Label:  gp.display,
				Status: "pending",
				Family: &groupFamily,
			})
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
	hasProvisioner := false
	for _, j := range js {
		if j.ID == provisionerID {
			hasProvisioner = true
		}
	}
	// Sequential phase chain: provisioner → bootstrap-kit → cutover →
	// handover → apps. Each edge corresponds to a real temporal
	// dependency in the Sovereign lifecycle:
	//   - bootstrap-kit installs 45 HRs in dep order (cilium → ... →
	//     catalyst-platform); cannot start until tofu provisioner has
	//     created the cluster.
	//   - cutover (bp-self-sovereign-cutover) flips the Sovereign from
	//     bootstrap-credentials to its own per-Sovereign identity;
	//     depends on bootstrap-kit having installed gitea, harbor,
	//     keycloak, and the cutover chart itself.
	//   - handover mints the operator-facing JWT and redirects the
	//     wizard to console.<sovereign-fqdn>; depends on cutover having
	//     reached cutoverComplete=true.
	//   - apps is where operator-authored Application CRs reconcile;
	//     depends on the wizard reaching the post-handover console.
	phaseChain := []string{
		provisionerID,
		bootstrapID,
		cutoverID,
		handoverID,
		appsID,
	}
	if hasProvisioner {
		for i := 0; i+1 < len(phaseChain); i++ {
			rels = append(rels, flowSnapshotLocalRelationship{
				FromID: phaseChain[i],
				ToID:   phaseChain[i+1],
				Type:   "finish-to-start",
			})
		}
	}

	// Group-status roll-up — synthetic group nodes (provisioner, bootstrap-
	// kit, per-region sub-groups, cutover/handover/apps phase groups)
	// carry hardcoded placeholder statuses (`running` / `pending`) when
	// emitted above. Now that ALL leaf install-* nodes + their statuses
	// are present, walk the contains tree bottom-up and replace each
	// group's placeholder with a value derived from its descendants:
	//
	//   - all descendants succeeded → succeeded
	//   - any descendant failed     → failed
	//   - any descendant running    → running
	//   - else (all pending or no descendants) → pending
	//
	// Without this, the canvas shows cutover/handover/apps bubbles
	// permanently "running" or "pending" even when their underlying HRs
	// (or in handover/apps' case, no HRs at all yet) are succeeded.
	// Founder reported on prov #76: "I dont think they are true" — same
	// concern.
	{
		childrenOf := map[string][]string{}
		parentOf := map[string]string{}
		for _, r := range rels {
			if r.Type == "contains" {
				childrenOf[r.ToID] = append(childrenOf[r.ToID], r.FromID)
				parentOf[r.FromID] = r.ToID
			}
		}
		// Collect group + bootstrap nodes (these are the parents that
		// need status roll-up). Leaves keep their stored status.
		groupNodeIdx := map[string]int{}
		for i, n := range nodes {
			fam := ""
			if n.Family != nil {
				fam = *n.Family
			}
			if fam == "group" || fam == "bootstrap" {
				groupNodeIdx[n.ID] = i
			}
		}
		// Post-order traversal: a group's roll-up requires its children
		// (which may themselves be groups) to be resolved first.
		var resolve func(id string) string
		memo := map[string]string{}
		resolve = func(id string) string {
			if s, ok := memo[id]; ok {
				return s
			}
			// Leaf node — use stored status.
			if _, isGroup := groupNodeIdx[id]; !isGroup {
				for _, n := range nodes {
					if n.ID == id {
						memo[id] = n.Status
						return n.Status
					}
				}
				memo[id] = "pending"
				return "pending"
			}
			kids := childrenOf[id]
			if len(kids) == 0 {
				memo[id] = "pending"
				return "pending"
			}
			anyFailed, anyRunning, allSucceeded := false, false, true
			for _, k := range kids {
				s := resolve(k)
				switch s {
				case "failed":
					anyFailed = true
					allSucceeded = false
				case "running":
					anyRunning = true
					allSucceeded = false
				case "succeeded":
					// keeps allSucceeded true
				default:
					allSucceeded = false
				}
			}
			out := "pending"
			if anyFailed {
				out = "failed"
			} else if anyRunning {
				out = "running"
			} else if allSucceeded {
				out = "succeeded"
			}
			memo[id] = out
			return out
		}
		for id, idx := range groupNodeIdx {
			nodes[idx].Status = resolve(id)
		}
	}

	// NOTE — the legacy live-watcher multi-region composition block was
	// removed in PR #1455 (2026-05-13). The per-Job loop above already
	// derives region from `j.AppID` containing `/` and synthesises the
	// region sub-groups from persisted Job rows, which is the durable
	// source of truth. The old code path read dep.secondaryWatchers
	// (transient — cleared by stopSecondaries() when phase 1
	// terminates) AND emitted nodes with the SAME region-group IDs as
	// the new path, double-counting children + duplicating the region
	// group bubbles during phase 1 (caught on prov #61: 90 children
	// per region group, not 45). Reading secondaryWatchers earlier
	// (lines ~182-205) still serves the hrDeps lookup for the primary
	// region's intra-cluster dep edges; that path is unchanged.
	// _ = secondaryWatchers — intentionally unused after this rewrite.
	_ = secondaryWatchers

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
