// reconciliation_dag.go — the bounded Flux dependency DAG that backs the
// Convergence-Monitor Reconciliation page (#3925 surface B).
//
// # Why this is NOT the Jobs/Flow snapshot
//
// The Flow snapshot (flow_snapshot_local.go) ingests EVERY Job — install
// leaves, Phase-0 tofu chain, cutover steps, Day-2 scanners — into one flat
// graph. That's the Jobs view's job. The Reconciliation view answers a
// different question: "is the declared Flux/GitOps DESIRED state converged,
// and if not, where is it stuck?". Its node-set is therefore the BOUNDED,
// declared set of continuous reconcilers:
//
//   - HelmRelease nodes — one per bp-* HelmRelease (the layer with an
//     explicit spec.dependsOn DAG). Sourced from the helmwatch informer's
//     ComponentSnapshot (AppID, Status, DependsOn, Stalled).
//   - Kustomization nodes — one per Flux Kustomization reconcile leaf
//     (jobs.KindReconcile, "reconcile-<name>"), the bootstrap-kit tiers.
//
// Scanners / one-shot Jobs / CronJobs are NEVER in this set — they are
// finite jobs, not reconcilers, so they live on the Jobs page. The DAG is
// bounded by construction: node count == (#HelmReleases + #Kustomizations),
// edges == real declared dependsOn, ZERO scanner/Job nodes.
//
// # Vocabulary — never Success/Failed (those imply a finite end)
//
//   Reconciled  ← helmwatch `installed`            (in-sync, not a finite win)
//   Reconciling ← `pending` / `installing`, OR a transient `failed`/`degraded`
//                 that is NOT Stalled (Flux is still retrying)
//   Drifted     ← `degraded` that is NOT Stalled    (out of sync, self-healing)
//   Degraded    ← `failed` AND Stalled              (Flux exhausted retries)
//   Suspended   ← Flux-suspended reconciler          (reserved; not emitted yet)
//
// The STICKY rule (kills the flapping #3916 / ticket symptom 2): only a
// Flux `Stalled` HelmRelease maps to a terminal `Degraded`; every other
// `Ready=False` is `Reconciling`, which holds a spinner and never flips to a
// red terminal.
package handler

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

// Reconciliation node-state vocabulary (wire contract — the FE renders
// these verbatim; NEVER Success/Succeeded/Failed).
const (
	ReconStateReconciled  = "Reconciled"
	ReconStateReconciling = "Reconciling"
	ReconStateDrifted     = "Drifted"
	ReconStateDegraded    = "Degraded"
	ReconStateSuspended   = "Suspended"
)

// Reconciliation node-kinds.
const (
	ReconKindHelmRelease   = "HelmRelease"
	ReconKindKustomization = "Kustomization"
)

// ReconciliationNode is one node in the bounded Flux DAG.
type ReconciliationNode struct {
	// ID — stable, unique within the DAG. For HelmReleases it's the
	// bp-prefixed AppID ("bp-keycloak"); for Kustomizations the tier slug
	// ("bootstrap-kit").
	ID string `json:"id"`
	// Label — the human-readable name shown on the node.
	Label string `json:"label"`
	// Kind — HelmRelease | Kustomization.
	Kind string `json:"kind"`
	// State — the Reconciliation vocabulary (Reconciled/Reconciling/…).
	State string `json:"state"`
	// DependsOn — node IDs this node declares a Flux dependsOn against.
	// Edges are derived from these by exact-ID match (dangling deps are
	// dropped so the DAG never renders an edge to a non-existent node).
	DependsOn []string `json:"dependsOn,omitempty"`
	// Message — the Flux Ready-condition message, when available.
	Message string `json:"message,omitempty"`
}

// ReconciliationDAG is the GET /reconciliation response envelope.
type ReconciliationDAG struct {
	// Nodes — the bounded declared set (HelmReleases + Kustomizations).
	Nodes []ReconciliationNode `json:"nodes"`
	// Reconciled / Total — the N/M-Reconciled header counts.
	Reconciled int `json:"reconciled"`
	Total      int `json:"total"`
	// Watching — true while a live helmwatch informer is attached (the
	// counts track convergence live); false once Phase-1 ended (counts
	// come from the frozen / live-re-read snapshot).
	Watching bool `json:"watching"`
	// NotYetTracked — the deferred continuous-reconciler classes this view
	// does NOT yet include (Crossplane, cert-manager, CNPG, External-Secrets,
	// the CRD controllers). Surfaced so the operator knows the scope —
	// ticket §2 surface-A footnote.
	NotYetTracked []string `json:"notYetTracked"`
}

// notYetTrackedReconcilers — the deferred continuous-reconciler classes
// (ticket §2 surface-A: "list them explicitly in the UI's 'not yet tracked'
// footnote"). Flux-only to start; these absorb later as node-kinds.
var notYetTrackedReconcilers = []string{
	"Crossplane",
	"cert-manager",
	"CNPG",
	"External-Secrets",
	"CRD controllers",
}

// reconStateForComponent maps a helmwatch ComponentSnapshot to the
// Reconciliation vocabulary. This is the STICKY state machine: only a
// Stalled HelmRelease is terminal-Degraded; every other not-installed
// state is in-progress (Reconciling/Drifted), so the view never flaps a
// transient Ready=False into a red Failed.
func reconStateForComponent(cs helmwatch.ComponentSnapshot) string {
	switch strings.ToLower(strings.TrimSpace(cs.Status)) {
	case helmwatch.StateInstalled:
		return ReconStateReconciled
	case helmwatch.StatePending, helmwatch.StateInstalling:
		return ReconStateReconciling
	case helmwatch.StateDegraded:
		// Degraded-but-not-stalled = drifted/out-of-sync, self-healing on
		// the next reconcile. A stalled degraded escalates to Degraded.
		if cs.Stalled {
			return ReconStateDegraded
		}
		return ReconStateDrifted
	case helmwatch.StateFailed:
		// Only a STABLY-failed (Stalled) HR is terminal-Degraded; a
		// still-retrying failure is Reconciling (kills the flapping).
		if cs.Stalled {
			return ReconStateDegraded
		}
		return ReconStateReconciling
	default:
		return ReconStateReconciling
	}
}

// reconStateForReconcileJob maps a Flux Kustomization reconcile leaf
// (jobs.KindReconcile) status to the Reconciliation vocabulary. The jobs
// store status enum is pending/running/succeeded/failed; a Kustomization
// is continuous so we never render Success/Failed:
//
//	succeeded → Reconciled · running/pending → Reconciling · failed → Degraded
func reconStateForReconcileJob(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case jobs.StatusSucceeded:
		return ReconStateReconciled
	case jobs.StatusFailed:
		return ReconStateDegraded
	default:
		return ReconStateReconciling
	}
}

// GetReconciliationDAG handles GET
// /api/v1/deployments/{depId}/reconciliation.
//
// Builds the bounded Flux DAG (HelmReleases + Kustomizations) coloured by
// live reconcile state. Read-only, stateless: it reuses the same
// component-snapshot read path GetComponentsState uses (live watcher →
// one-shot live re-read → frozen persisted fallback) plus the jobs store's
// Kustomization reconcile leaves.
func (h *Handler) GetReconciliationDAG(w http.ResponseWriter, r *http.Request) {
	depID := strings.TrimSpace(chi.URLParam(r, "depId"))
	if depID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing-depId"})
		return
	}
	val, ok := h.deployments.Load(depID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "deployment-not-found"})
		return
	}
	dep := val.(*Deployment)

	components, watching := h.reconciliationComponents(r.Context(), dep)
	kustomizations := h.reconciliationKustomizations(depID)

	dag := buildReconciliationDAG(components, kustomizations, watching)
	writeJSON(w, http.StatusOK, dag)
}

// reconciliationComponents reads the deployment's bp-* HelmReleases through
// the same three-tier path GetComponentsState uses, returning the snapshot
// + a `watching` flag. Live watcher first (counts track convergence), then
// a one-shot live re-read, then the frozen persisted fallback.
func (h *Handler) reconciliationComponents(ctx context.Context, dep *Deployment) ([]helmwatch.ComponentSnapshot, bool) {
	dep.mu.Lock()
	watcher := dep.liveWatcher
	var fallbackStates map[string]string
	if dep.Result != nil {
		fallbackStates = dep.Result.ComponentStates
	}
	dep.mu.Unlock()

	if watcher != nil {
		return watcher.SnapshotComponents(), true
	}
	if live, ok := h.liveComponentsSnapshot(ctx, dep); ok {
		return live, false
	}
	out := make([]helmwatch.ComponentSnapshot, 0, len(fallbackStates))
	for appID, state := range fallbackStates {
		out = append(out, helmwatch.ComponentSnapshot{
			AppID:           appID,
			Status:          state,
			HelmReleaseName: "bp-" + appID,
			Namespace:       helmwatch.FluxNamespace,
		})
	}
	return out, false
}

// reconciliationKustomizations pulls the Flux Kustomization reconcile leaves
// (jobs.KindReconcile, "reconcile-<name>") from the jobs store. These are the
// bootstrap-kit dependency tiers — the OTHER half of the bounded declared
// set. Returns nil when the store is unconfigured / empty (the DAG then
// carries HelmReleases only, still bounded).
//
// IMPORTANT: ONLY reconcile-kind leaves are returned — install leaves,
// cron/task/scanner leaves, step leaves, group jobs are all filtered out, so
// no scanner or Job ever leaks into the Reconciliation DAG.
func (h *Handler) reconciliationKustomizations(depID string) []jobs.Job {
	st := h.jobsStore()
	if st == nil {
		return nil
	}
	js, err := st.ListJobs(depID)
	if err != nil || len(js) == 0 {
		return nil
	}
	out := make([]jobs.Job, 0)
	for _, j := range js {
		if isReconcileKustomization(j) {
			out = append(out, j)
		}
	}
	return out
}

// isReconcileKustomization reports whether a Job is a Flux Kustomization
// reconcile leaf — the ONLY job kind that belongs in the Reconciliation DAG.
// Matches the persisted Kind OR the "reconcile-" name prefix (back-compat
// with rows persisted before Kind existed). A group job is never a leaf.
func isReconcileKustomization(j jobs.Job) bool {
	if j.Type == jobs.JobTypeGroup {
		return false
	}
	if j.Kind == jobs.KindReconcile {
		return true
	}
	if j.Kind == "" && strings.HasPrefix(j.JobName, jobs.ReconcileJobPrefix) {
		return true
	}
	return false
}

// buildReconciliationDAG assembles the bounded DAG from the HelmRelease
// component snapshots + the Kustomization reconcile leaves. Edges are
// derived from declared dependsOn by exact-ID match; dangling deps (to a
// node not in the set) are dropped. Nodes are sorted by (kind, id) for a
// stable render. Exported-shape so the FE + tests share one contract.
func buildReconciliationDAG(
	components []helmwatch.ComponentSnapshot,
	kustomizations []jobs.Job,
	watching bool,
) ReconciliationDAG {
	nodes := make([]ReconciliationNode, 0, len(components)+len(kustomizations))
	idSet := make(map[string]struct{})

	// Kustomization nodes first (the tier spine sits above HRs).
	for _, j := range kustomizations {
		id := strings.TrimPrefix(j.JobName, jobs.ReconcileJobPrefix)
		if id == "" {
			id = j.JobName
		}
		label := j.DisplayName
		if label == "" {
			label = id
		}
		nodes = append(nodes, ReconciliationNode{
			ID:    "kustomization/" + id,
			Label: label,
			Kind:  ReconKindKustomization,
			State: reconStateForReconcileJob(j.Status),
			// Kustomization dependsOn is not threaded through the jobs store
			// today; the tier ordering is implicit. Edges left empty.
		})
		idSet["kustomization/"+id] = struct{}{}
	}

	// HelmRelease nodes — bp-prefixed id so dependsOn (also bp-prefixed on
	// the wire? no — ComponentSnapshot.DependsOn is bp-STRIPPED) resolves.
	for _, cs := range components {
		appID := strings.TrimSpace(cs.AppID)
		if appID == "" {
			continue
		}
		id := "bp-" + strings.TrimPrefix(appID, "bp-")
		deps := make([]string, 0, len(cs.DependsOn))
		for _, d := range cs.DependsOn {
			d = strings.TrimSpace(d)
			if d == "" {
				continue
			}
			deps = append(deps, "bp-"+strings.TrimPrefix(d, "bp-"))
		}
		nodes = append(nodes, ReconciliationNode{
			ID:        id,
			Label:     id,
			Kind:      ReconKindHelmRelease,
			State:     reconStateForComponent(cs),
			DependsOn: deps,
			Message:   cs.Message,
		})
		idSet[id] = struct{}{}
	}

	// Prune dangling dependsOn edges (a dep whose target isn't in the set —
	// e.g. a dep on a host-side component the watch never observed). Keeps
	// the DAG honest: every rendered edge connects two real nodes.
	for i := range nodes {
		if len(nodes[i].DependsOn) == 0 {
			continue
		}
		kept := nodes[i].DependsOn[:0]
		for _, d := range nodes[i].DependsOn {
			if _, ok := idSet[d]; ok {
				kept = append(kept, d)
			}
		}
		if len(kept) == 0 {
			nodes[i].DependsOn = nil
		} else {
			nodes[i].DependsOn = kept
		}
	}

	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Kind != nodes[j].Kind {
			// Kustomizations render above HelmReleases.
			return nodes[i].Kind == ReconKindKustomization
		}
		return nodes[i].ID < nodes[j].ID
	})

	reconciled := 0
	for _, n := range nodes {
		if n.State == ReconStateReconciled {
			reconciled++
		}
	}

	return ReconciliationDAG{
		Nodes:         nodes,
		Reconciled:    reconciled,
		Total:         len(nodes),
		Watching:      watching,
		NotYetTracked: notYetTrackedReconcilers,
	}
}
