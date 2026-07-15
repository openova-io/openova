package jobs

// filter.go — the `/jobs` view selector (issue #3996 follow-up, install
// lens restored per issue #5019).
//
// # What /jobs shows
//
// The Jobs page lists work with a completion semantics — things that
// start, run, and reach a result: provision steps, cutover steps, batch
// Jobs, CronJob runs, one-shot Day-2 mutations, AND the bootstrap-kit
// HelmRelease installs. The truly OPEN-ENDED reconcilers — the Flux
// Kustomization reconcile leaves and the long-running reconciler
// Deployments — are NOT such work. They run forever by design, so listing
// them on /jobs drowns the result-bearing rows in an ever-growing wall of
// "running" reconcilers (the exact symptom the founder saw on
// console.<sov>/jobs, #3996).
//
// Those open-ended reconcilers have their OWN home: the Cloud surface
// Reconciliation lens + the lightweight ArgoCD-like reconciler-management
// surface (#3996), which reads them LIVE from the cluster (independent of
// this jobs store).
//
// # Why installs are back (issue #5019)
//
// The original #3996 filter also dropped KindInstall, on the reasoning
// that a HelmRelease reconciles forever. That erased ALL ~65 bootstrap-kit
// install rows from /jobs — the Kind filter had no "install" option and
// class assertions like "install-openbao green" became unwalkable (UAT
// row 170). Unlike a Kustomization reconcile, an install leaf carries a
// real per-attempt result (chart applied → Ready condition), the set is
// BOUNDED by the bootstrap-kit catalog (it never grows unattended), and
// the operator's primary question — "did every install land?" — is a
// /jobs question. So KindInstall is a first-class /jobs kind again; only
// KindReconcile + KindReconciler stay on the recon lens exclusively.
//
// # What stays
//
//   - KindInstall   — a HelmRelease install leaf ("install-<chart>",
//                     bounded bootstrap-kit set; status = Ready result).
//   - KindStep      — provision / cutover / DR-switchover steps (finite).
//   - KindTask      — standalone / owned batch Jobs (finite).
//   - KindCron      — recurring CronJob runs (each run is a finite
//                     Execution; the row is the job-history).
//   - KindMutation  — one-shot Day-2 Crossplane XRC submissions (finite).
//   - KindLifecycle — Phase-0 / lifecycle leaves (tofu-init etc., finite).
//
// # What's removed
//
//   - KindReconcile  — a Flux Kustomization reconcile leaf. CONTINUOUS.
//   - KindReconciler — a long-running reconciler Deployment. CONTINUOUS.
//
// # Group pruning
//
// Removing the continuous leaves can leave a synthesised group Job with no
// remaining children (e.g. the `reconcilers` group when every member was a
// Kustomization/reconciler). An empty group is noise, so we drop any group
// that has no surviving descendant after the leaf filter and re-derive the
// tree view so ChildIDs / rollups stay honest.

// continuousReconcilerKinds is the set of Job kinds that represent
// open-ended, always-on reconcilers rather than result-bearing work. They
// are surfaced on the Cloud Reconciliation lens / reconciler-management
// surface (#3996), never on /jobs. KindInstall is deliberately NOT here —
// the bounded bootstrap-kit install rows are first-class /jobs rows
// (issue #5019).
var continuousReconcilerKinds = map[string]bool{
	KindReconcile:  true,
	KindReconciler: true,
}

// IsContinuousReconciler reports whether a Job's kind is one of the
// always-on reconciler kinds excluded from the finite /jobs list. Group
// jobs (KindGroup) are never themselves continuous — they're pruned only
// when empty, by FilterFiniteJobs.
func IsContinuousReconciler(kind string) bool {
	return continuousReconcilerKinds[kind]
}

// FilterFiniteJobs returns the subset of `in` that the /jobs list shows:
// open-ended continuous-reconciler leaves (Flux Kustomization reconciles,
// reconciler Deployments) are dropped — HelmRelease install leaves are
// KEPT (issue #5019) — and any group Job left with no surviving
// descendant is pruned. The returned slice is re-run through
// deriveTreeView so ChildIDs and group rollups reflect only the surviving
// rows.
//
// The input is expected to be a deriveTreeView-shaped slice (the output of
// Store.ListJobs): leaf rows + synthesised group rows, ParentID linking
// children to groups. RunCount and other read-derived fields on the
// survivors are preserved (deriveTreeView leaves leaf fields untouched and
// only recomputes ChildIDs + group rollups).
func FilterFiniteJobs(in []Job) []Job {
	if len(in) == 0 {
		return in
	}

	// Pass 1 — keep every non-group leaf that is NOT a continuous
	// reconciler; defer the group keep/drop decision to pass 3 (a group
	// survives only if at least one descendant survives).
	type node struct {
		job      Job
		isGroup  bool
		survives bool
	}
	nodes := make([]node, 0, len(in))
	byID := make(map[string]int, len(in))
	for _, j := range in {
		isGroup := j.Type == JobTypeGroup || j.Kind == KindGroup
		survives := false
		if !isGroup {
			// A leaf survives iff it is finite (not a continuous
			// reconciler). Back-fill the kind from the name for legacy
			// rows so a row persisted before the Kind field existed is
			// classified honestly.
			kind := j.Kind
			if kind == "" {
				kind = kindForLeaf(j.JobName)
			}
			survives = !IsContinuousReconciler(kind)
		}
		byID[j.ID] = len(nodes)
		nodes = append(nodes, node{job: j, isGroup: isGroup, survives: survives})
	}

	// Pass 2 — propagate leaf survival up the parent chain so a group with
	// at least one surviving descendant is itself kept. Walk leaves and
	// mark every ancestor.
	for i := range nodes {
		if nodes[i].isGroup || !nodes[i].survives {
			continue
		}
		parentID := nodes[i].job.ParentID
		for parentID != "" {
			pi, ok := byID[parentID]
			if !ok {
				break
			}
			if nodes[pi].survives {
				break // ancestor already marked; chain above it too
			}
			nodes[pi].survives = true
			parentID = nodes[pi].job.ParentID
		}
	}

	// Pass 3 — collect survivors in input order, stripping derived fields
	// so deriveTreeView recomputes ChildIDs / rollups against only the
	// surviving set.
	out := make([]Job, 0, len(nodes))
	for i := range nodes {
		if !nodes[i].survives {
			continue
		}
		j := nodes[i].job
		j.ChildIDs = nil
		out = append(out, j)
	}
	if len(out) == 0 {
		return []Job{}
	}
	return deriveTreeView(out)
}
