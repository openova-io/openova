// reconciliation_dag_test.go — unit tests for the #3925 surface-B bounded
// Flux DAG assembly. Asserts the success criteria from the ticket §4:
//   - bounded node-set: node count == #HelmReleases + #Kustomizations
//   - ZERO scanner/Job nodes (scanners never enter the reconciler set)
//   - edges == real declared dependsOn (dangling deps pruned)
//   - vocabulary: Reconciled/Reconciling/Drifted/Degraded — never
//     Success/Succeeded/Failed; only a Stalled HR → terminal Degraded
package handler

import (
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

// The state machine: only a Stalled HR is terminal-Degraded; every other
// not-installed state is in-progress (Reconciling/Drifted). This is the
// anti-flapping guarantee (symptom 2).
func TestReconStateForComponent_StickyVocabulary(t *testing.T) {
	cases := []struct {
		name    string
		status  string
		stalled bool
		want    string
	}{
		{"installed → Reconciled", helmwatch.StateInstalled, false, ReconStateReconciled},
		{"pending → Reconciling", helmwatch.StatePending, false, ReconStateReconciling},
		{"installing → Reconciling", helmwatch.StateInstalling, false, ReconStateReconciling},
		{"degraded-not-stalled → Drifted", helmwatch.StateDegraded, false, ReconStateDrifted},
		{"degraded-stalled → Degraded", helmwatch.StateDegraded, true, ReconStateDegraded},
		// The load-bearing anti-flap case: a transient failed (still
		// retrying) must be Reconciling, NOT a terminal Degraded.
		{"failed-not-stalled → Reconciling (no flap)", helmwatch.StateFailed, false, ReconStateReconciling},
		{"failed-stalled → Degraded", helmwatch.StateFailed, true, ReconStateDegraded},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := reconStateForComponent(helmwatch.ComponentSnapshot{Status: c.status, Stalled: c.stalled})
			if got != c.want {
				t.Fatalf("reconStateForComponent(%q,stalled=%v) = %q, want %q", c.status, c.stalled, got, c.want)
			}
		})
	}
}

// A continuous Kustomization is never rendered Success/Failed.
func TestReconStateForReconcileJob_Vocabulary(t *testing.T) {
	cases := map[string]string{
		jobs.StatusSucceeded: ReconStateReconciled,
		jobs.StatusRunning:   ReconStateReconciling,
		jobs.StatusPending:   ReconStateReconciling,
		jobs.StatusFailed:    ReconStateDegraded,
	}
	for status, want := range cases {
		if got := reconStateForReconcileJob(status); got != want {
			t.Fatalf("reconStateForReconcileJob(%q) = %q, want %q", status, got, want)
		}
	}
}

// The DAG is bounded: node count == #HelmReleases + #Kustomizations, edges
// follow declared dependsOn, and the N/M-Reconciled header is correct.
func TestBuildReconciliationDAG_BoundedAndEdges(t *testing.T) {
	components := []helmwatch.ComponentSnapshot{
		{AppID: "cilium", Status: helmwatch.StateInstalled},
		{AppID: "keycloak", Status: helmwatch.StateInstalled, DependsOn: []string{"cilium"}},
		{AppID: "newapi", Status: helmwatch.StateFailed, Stalled: true, DependsOn: []string{"keycloak"}},
		{AppID: "loki", Status: helmwatch.StateInstalling},
	}
	kustomizations := []jobs.Job{
		{JobName: "reconcile-bootstrap-kit", DisplayName: "Bootstrap", Kind: jobs.KindReconcile, Status: jobs.StatusSucceeded},
	}
	dag := buildReconciliationDAG(components, kustomizations, true)

	// Bounded: 4 HRs + 1 Kustomization = 5.
	if dag.Total != 5 || len(dag.Nodes) != 5 {
		t.Fatalf("expected 5 bounded nodes, got total=%d len=%d", dag.Total, len(dag.Nodes))
	}
	// Reconciled count: cilium + keycloak + bootstrap-kit = 3.
	if dag.Reconciled != 3 {
		t.Fatalf("expected 3 Reconciled, got %d", dag.Reconciled)
	}
	if !dag.Watching {
		t.Fatal("watching flag must propagate")
	}

	byID := map[string]ReconciliationNode{}
	for _, n := range dag.Nodes {
		byID[n.ID] = n
	}
	// HR ids are bp-prefixed; dependsOn resolved to bp-prefixed ids.
	kc, ok := byID["bp-keycloak"]
	if !ok {
		t.Fatal("bp-keycloak node missing")
	}
	if len(kc.DependsOn) != 1 || kc.DependsOn[0] != "bp-cilium" {
		t.Fatalf("keycloak dependsOn = %v, want [bp-cilium]", kc.DependsOn)
	}
	// Stalled HR → terminal Degraded.
	if byID["bp-newapi"].State != ReconStateDegraded {
		t.Fatalf("stalled newapi state = %q, want Degraded", byID["bp-newapi"].State)
	}
	// Kustomization node present + Reconciled.
	k, ok := byID["kustomization/bootstrap-kit"]
	if !ok {
		t.Fatal("kustomization/bootstrap-kit node missing")
	}
	if k.Kind != ReconKindKustomization || k.State != ReconStateReconciled {
		t.Fatalf("kustomization node = %+v", k)
	}
}

// Dangling dependsOn (to a node not in the bounded set) is pruned so the
// DAG never renders an edge to a non-existent node.
func TestBuildReconciliationDAG_PrunesDanglingEdges(t *testing.T) {
	components := []helmwatch.ComponentSnapshot{
		// depends on a host-side component the watch never observed.
		{AppID: "harbor", Status: helmwatch.StateInstalled, DependsOn: []string{"shared-postgres-not-in-set"}},
	}
	dag := buildReconciliationDAG(components, nil, true)
	if len(dag.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(dag.Nodes))
	}
	if dag.Nodes[0].DependsOn != nil {
		t.Fatalf("dangling dep must be pruned, got %v", dag.Nodes[0].DependsOn)
	}
}

// The CRITICAL bound: scanners / cron / task / install / step / group jobs
// must NEVER enter the Reconciliation DAG. Only reconcile-kind leaves do.
func TestReconciliationKustomizations_ExcludesScannersAndNonReconcileJobs(t *testing.T) {
	h, st := newOpStateHandler(t)
	depID := "dep-recon-scope"
	seed := []jobs.Job{
		// reconcile-kind — the ONLY kind that should appear.
		{DeploymentID: depID, JobName: "reconcile-bootstrap-kit", Kind: jobs.KindReconcile, Status: jobs.StatusSucceeded},
		{DeploymentID: depID, JobName: "reconcile-flux-system", Kind: jobs.KindReconcile, Status: jobs.StatusRunning},
		// scanners — must be EXCLUDED.
		{DeploymentID: depID, JobName: "cron-trivy-scan", Kind: jobs.KindCron, Status: jobs.StatusSucceeded},
		{DeploymentID: depID, JobName: "task-syft-sbom", Kind: jobs.KindTask, Status: jobs.StatusSucceeded},
		// install leaf — must be EXCLUDED (HRs come from the watcher, not here).
		{DeploymentID: depID, JobName: "install-cilium", Kind: jobs.KindInstall, Status: jobs.StatusSucceeded},
		// cutover step — must be EXCLUDED.
		{DeploymentID: depID, JobName: "cutover-step-05", Kind: jobs.KindStep, Status: jobs.StatusRunning},
		// group — must be EXCLUDED.
		{DeploymentID: depID, JobName: jobs.GroupBootstrapKit, Type: jobs.JobTypeGroup, Status: jobs.StatusRunning},
	}
	for _, j := range seed {
		if err := st.UpsertJob(j); err != nil {
			t.Fatalf("UpsertJob: %v", err)
		}
	}
	k := h.reconciliationKustomizations(depID)
	if len(k) != 2 {
		names := make([]string, 0, len(k))
		for _, j := range k {
			names = append(names, j.JobName)
		}
		t.Fatalf("expected exactly 2 reconcile leaves, got %d: %v", len(k), names)
	}
	for _, j := range k {
		if !isReconcileKustomization(j) {
			t.Fatalf("non-reconcile job leaked into the DAG scope: %s", j.JobName)
		}
	}

	// And the assembled DAG carries ZERO scanner nodes.
	dag := buildReconciliationDAG(nil, k, false)
	for _, n := range dag.Nodes {
		if n.Kind != ReconKindKustomization {
			t.Fatalf("unexpected non-Kustomization node in scanner-scope DAG: %+v", n)
		}
	}
	if dag.Total != 2 {
		t.Fatalf("expected 2 Kustomization nodes, got %d", dag.Total)
	}
}

// The "not yet tracked" footnote names the deferred reconciler classes.
func TestBuildReconciliationDAG_NotYetTrackedFootnote(t *testing.T) {
	dag := buildReconciliationDAG(nil, nil, false)
	if len(dag.NotYetTracked) == 0 {
		t.Fatal("notYetTracked footnote must be populated")
	}
	for _, want := range []string{"Crossplane", "cert-manager", "CNPG", "External-Secrets"} {
		if !sliceHas(dag.NotYetTracked, want) {
			t.Fatalf("notYetTracked missing %q (got %v)", want, dag.NotYetTracked)
		}
	}
}

func sliceHas(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
