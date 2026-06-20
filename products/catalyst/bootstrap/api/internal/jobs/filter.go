package jobs

// filter.go — the `/jobs` view selector (issue #3996 follow-up).
//
// # Why /jobs is FINITE-only
//
// The Jobs page is a list of FINITE work — things that start, run, and
// end: provision steps, cutover steps, batch Jobs, CronJob runs, and the
// one-shot Day-2 mutations. The CONTINUOUS reconcilers — the Flux
// HelmRelease installs that never stop reconciling, the Flux
// Kustomization reconcile leaves, and the long-running reconciler
// Deployments — are NOT finite work. They run forever by design, so
// listing them on /jobs drowns the finite rows in an ever-growing wall of
// "running" reconcilers (the exact symptom the founder sees on
// console.<sov>/jobs).
//
// Those continuous reconcilers now have their OWN home: the Cloud surface
// Reconciliation lens + the lightweight ArgoCD-like reconciler-management
// surface (#3996), which reads them LIVE from the cluster (independent of
// this jobs store). So removing them from /jobs loses nothing — it moves
// them to the surface that's actually built for "always-on" objects.
//
// # What stays
//
//   - KindStep      — provision / cutover / DR-switchover steps (finite).
//   - KindTask      — standalone / owned batch Jobs (finite).
//   - KindCron      — recurring CronJob runs (each run is a finite
//                     Execution; the row is the job-history).
//   - KindMutation  — one-shot Day-2 Crossplane XRC submissions (finite).
//   - KindLifecycle — Phase-0 / lifecycle leaves (tofu-init etc., finite).
//
// # What's removed
//
//   - KindInstall    — a Flux HelmRelease install leaf. Flux reconciles it
//                      forever; it never "finishes". CONTINUOUS.
//   - KindReconcile  — a Flux Kustomization reconcile leaf. CONTINUOUS.
//   - KindReconciler — a long-running reconciler Deployment. CONTINUOUS.
//
// # Group pruning
//
// Removing the continuous leaves can leave a synthesised group Job with no
// remaining children (e.g. the `reconcilers` group when every member was a
// Kustomization/reconciler, or `bootstrap-kit` when it only ever held
// install leaves). An empty group is noise, so we drop any group that has
// no surviving descendant after the leaf filter and re-derive the tree
// view so ChildIDs / rollups stay honest.

// continuousReconcilerKinds is the set of Job kinds that represent
// always-on reconcilers rather than finite work. They are surfaced on the
// Cloud Reconciliation lens / reconciler-management surface (#3996), never
// on /jobs.
var continuousReconcilerKinds = map[string]bool{
	KindInstall:    true,
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

// FilterFiniteJobs returns the subset of `in` that represents FINITE work
// for the /jobs list: continuous-reconciler leaves (HelmRelease installs,
// Flux Kustomization reconciles, reconciler Deployments) are dropped, and
// any group Job left with no surviving descendant is pruned. The returned
// slice is re-run through deriveTreeView so ChildIDs and group rollups
// reflect only the surviving rows.
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
