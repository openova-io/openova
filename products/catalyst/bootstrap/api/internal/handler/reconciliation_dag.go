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
//   Suspended   ← spec.suspend=true (ComponentSnapshot.Suspended) — wins
//                 over every other state, matching the drill panel's
//                 manageStateForReady precedence (#5485 defect 4)
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
	"time"

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

// Reconciliation node-kinds. HelmRelease + Kustomization are the Flux
// layer; the rest are the NON-Flux declarative reconcilers (#3958) — they
// carry no spec.dependsOn so they render as edgeless nodes. The Kind on a
// declarative node is the human label the reader emits verbatim (one of
// the ReconKindDeclarative* values below), so the FE can class-filter them.
const (
	ReconKindHelmRelease   = "HelmRelease"
	ReconKindKustomization = "Kustomization"

	// Non-Flux declarative reconciler node-kinds (#3958). These mirror the
	// helmwatch reader's Kind labels 1:1.
	ReconKindCertificate    = "Certificate"    // cert-manager Certificate
	ReconKindCluster        = "Cluster"        // CNPG Cluster
	ReconKindExternalSecret = "ExternalSecret" // External-Secrets ExternalSecret
	ReconKindApplication    = "Application"    // Catalyst Application CR
	ReconKindEnvironment    = "Environment"    // Catalyst Environment CR
	ReconKindOrganization   = "Organization"   // Catalyst Organization CR
	ReconKindContinuum      = "Continuum"      // Catalyst Continuum CR
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
	// does NOT yet include. As of #3958 cert-manager, CNPG, and
	// External-Secrets ARE tracked (they appear as edgeless declarative
	// nodes), so only Crossplane + the generic CRD controllers remain
	// deferred. Surfaced so the operator knows the scope — ticket §2
	// surface-A footnote.
	NotYetTracked []string `json:"notYetTracked"`
}

// notYetTrackedReconcilers — the deferred continuous-reconciler classes
// (ticket §2 surface-A: "list them explicitly in the UI's 'not yet tracked'
// footnote"). #3958 absorbed cert-manager / CNPG / External-Secrets AND the
// Catalyst control-plane CRs as edgeless declarative nodes, so they are no
// longer listed here. What remains deferred: Crossplane managed resources
// (the cloud-join provider plane) and the generic CRD controllers.
var notYetTrackedReconcilers = []string{
	"Crossplane",
	"CRD controllers",
}

// reconStateForComponent maps a helmwatch ComponentSnapshot to the
// Reconciliation vocabulary. This is the STICKY state machine: only a
// Stalled HelmRelease is terminal-Degraded; every other not-installed
// state is in-progress (Reconciling/Drifted), so the view never flaps a
// transient Ready=False into a red Failed.
func reconStateForComponent(cs helmwatch.ComponentSnapshot) string {
	// #5485 defect 4 — suspension wins over every other state, exactly
	// like manageStateForReady on the drill-panel path. helmwatch keeps
	// Status=StateInstalled for suspended HRs (Wave 5.103 #2447 —
	// Phase-1 readiness must not block on them), so without this check
	// the graph node rendered "healthy" while the drill panel showed
	// SUSPENDED for the same object.
	if cs.Suspended {
		return ReconStateSuspended
	}
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

// reconStateForDeclarative maps a non-Flux declarative reconciler's Obs*
// lifecycle status (#3958) onto the Reconciliation vocabulary. The reader
// emits the helmwatch Obs* string values ("installed"/"failed"/"pending"/
// "installing"); a continuous declarative reconciler is never rendered
// Success/Failed:
//
//	Succeeded(installed) → Reconciled · Failed(failed) → Degraded ·
//	Pending/installing/anything-else → Reconciling
//
// NOTE: this is a SEPARATE mapper from reconStateForReconcileJob — that one
// keys off the jobs.Status* enum (pending/running/succeeded/failed) whereas
// the Obs* constants are the helmwatch State* strings
// (pending/installing/installed/failed), so the inputs do NOT match and the
// jobs mapper cannot be reused.
func reconStateForDeclarative(obsStatus string) string {
	switch strings.ToLower(strings.TrimSpace(obsStatus)) {
	case helmwatch.ObsStatusSucceeded:
		return ReconStateReconciled
	case helmwatch.ObsStatusFailed:
		return ReconStateDegraded
	default:
		// ObsStatusPending / ObsStatusRunning / anything unexpected — a
		// declarative reconciler still converging holds a spinner, never a
		// red terminal (the same anti-flap rule the HR mapper uses).
		return ReconStateReconciling
	}
}

// reconciliationDeclarative reads the deployment's NON-Flux declarative
// reconcilers (cert-manager Certificates, CNPG Clusters, ESO
// ExternalSecrets, Catalyst control-plane CRs — #3958) via the unified
// both-contexts dynamic client. Best-effort: on any client error it returns
// nil so the Flux DAG is never blocked (mirrors the reconciliationComponents
// sovereign-self-console fallback style). A 10s timeout bounds the list.
func (h *Handler) reconciliationDeclarative(ctx context.Context, dep *Deployment) []helmwatch.DeclarativeReconciler {
	dyn, err := h.sovereignDynamicClient(dep)
	if err != nil {
		return nil
	}
	listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	decls, err := helmwatch.ListDeclarativeReconcilers(listCtx, dyn)
	if err != nil {
		return nil
	}
	return decls
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
	declaratives := h.reconciliationDeclarative(r.Context(), dep)

	dag := buildReconciliationDAG(components, kustomizations, declaratives, watching)
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
	// Sovereign self-console path. liveComponentsSnapshot reads a kubeconfig
	// FILE (resolvePrimaryKubeconfigPath — the MOTHERSHIP pattern, where the
	// mothership holds the Sovereign's posted-back kubeconfig). The Sovereign's
	// OWN catalyst-api has no such file (it runs in-cluster), so the read above
	// fails and the Reconciliation DAG previously rendered only the 5
	// Kustomization tiers — zero HelmRelease nodes. sovereignDynamicClient
	// routes through rest.InClusterConfig when SOVEREIGN_FQDN matches this
	// deployment, so this lists the Sovereign's own bp-* HelmReleases WITH their
	// spec.dependsOn edges (ListAndSnapshotHelmReleases → extractDependsOn), and
	// the DAG renders the full dependency graph live on the Sovereign console.
	if dyn, derr := h.sovereignDynamicClient(dep); derr == nil {
		listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		snap, lerr := helmwatch.ListAndSnapshotHelmReleases(listCtx, dyn)
		cancel()
		if lerr == nil && len(snap) > 0 {
			return snap, true
		}
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
// component snapshots + the Kustomization reconcile leaves + the non-Flux
// declarative reconcilers (#3958). Edges are derived from declared
// dependsOn by exact-ID match; dangling deps (to a node not in the set) are
// dropped. The declarative reconcilers carry NO dependsOn, so they always
// render as edgeless nodes. Nodes are sorted for a stable render
// (Kustomizations first, then HelmReleases, then declaratives; within each
// class by ID). Exported-shape so the FE + tests share one contract.
func buildReconciliationDAG(
	components []helmwatch.ComponentSnapshot,
	kustomizations []jobs.Job,
	declaratives []helmwatch.DeclarativeReconciler,
	watching bool,
) ReconciliationDAG {
	nodes := make([]ReconciliationNode, 0, len(components)+len(kustomizations)+len(declaratives))
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

	// Non-Flux declarative reconciler nodes (#3958) — cert-manager
	// Certificates, CNPG Clusters, ESO ExternalSecrets, Catalyst CRs. These
	// carry NO Flux spec.dependsOn, so they are always edgeless: present +
	// coloured by their own Ready/phase status, but with no DependsOn. The
	// node ID is "<kindlower>/<namespace>/<name>" — stable + unique across
	// kinds and namespaces (Organization is cluster-scoped so its namespace
	// segment is empty, e.g. "organization//acme").
	for _, d := range declaratives {
		kind := strings.TrimSpace(d.Kind)
		name := strings.TrimSpace(d.Name)
		if kind == "" || name == "" {
			continue
		}
		id := strings.ToLower(kind) + "/" + d.Namespace + "/" + name
		if _, dup := idSet[id]; dup {
			continue
		}
		nodes = append(nodes, ReconciliationNode{
			ID:    id,
			Label: name,
			Kind:  kind,
			State: reconStateForDeclarative(d.Status),
			// No dependsOn — declarative reconcilers carry no Flux DAG edge.
			Message: d.Message,
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

	// Render order: Kustomization tiers first, then the HelmRelease DAG,
	// then the edgeless declarative reconcilers (#3958). Within each class,
	// by ID. The declaratives sort by (kind,id) naturally because their IDs
	// are "<kindlower>/<ns>/<name>" — same-kind nodes cluster together.
	sort.SliceStable(nodes, func(i, j int) bool {
		ri, rj := reconNodeClassRank(nodes[i].Kind), reconNodeClassRank(nodes[j].Kind)
		if ri != rj {
			return ri < rj
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

// reconNodeClassRank groups node kinds into the three render tiers so the
// sort keeps the spine deterministic: Kustomizations (0) above the
// HelmRelease DAG (1) above the edgeless declarative reconcilers (2). Any
// unknown kind sorts last with the declaratives.
func reconNodeClassRank(kind string) int {
	switch kind {
	case ReconKindKustomization:
		return 0
	case ReconKindHelmRelease:
		return 1
	default:
		return 2
	}
}
