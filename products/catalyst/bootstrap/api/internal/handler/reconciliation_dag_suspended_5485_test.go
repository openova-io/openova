// reconciliation_dag_suspended_5485_test.go — #5485 defect 4: a
// suspended HelmRelease must surface ReconStateSuspended on the
// Reconciliation DAG (the graph node), never Reconciled/"healthy".
//
// Live-observed on hw291 (2026-07-30): all 4 suspended HRs carried
// data-node-status="healthy" on the graph while the reconciler drill
// panel honestly showed SUSPENDED with a Resume button — two surfaces
// disagreeing about one object. Root cause: helmwatch reports
// suspended HRs as StateInstalled (Wave 5.103 #2447 — Phase-1
// readiness must not block on intentionally-suspended HRs), and
// reconStateForComponent only read Status, so the DAG mapped them to
// Reconciled. The fix carries spec.suspend as its own
// ComponentSnapshot.Suspended flag and lets it win in the DAG mapper —
// the SAME precedence ListManagedReconcilers (the drill panel's
// source) applies via manageStateForReady.
//
// Per the whole-map pin rule (a partial guard hides broken siblings —
// see the #5505 action-map lesson): TestReconStateForComponent_WholeMap
// enumerates EVERY status × stalled × suspended combination, not just
// the flipped one.
package handler

import (
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
)

// TestReconStateForComponent_WholeMap pins the COMPLETE mapping.
// Suspension wins over every status; with suspension off, the sticky
// anti-flap vocabulary from #3916 is byte-identical to before.
func TestReconStateForComponent_WholeMap(t *testing.T) {
	// The full status vocabulary the helmwatch snapshot can emit, plus
	// "" and an unexpected value for the default branch.
	statuses := []string{
		helmwatch.StateInstalled,
		helmwatch.StatePending,
		helmwatch.StateInstalling,
		helmwatch.StateDegraded,
		helmwatch.StateFailed,
		"",
		"weird-future-state",
	}
	// unsuspendedWant is the pre-existing sticky mapping (status ×
	// stalled) — pinned in full so no sibling silently drifts.
	unsuspendedWant := map[string]map[bool]string{
		helmwatch.StateInstalled:  {false: ReconStateReconciled, true: ReconStateReconciled},
		helmwatch.StatePending:    {false: ReconStateReconciling, true: ReconStateReconciling},
		helmwatch.StateInstalling: {false: ReconStateReconciling, true: ReconStateReconciling},
		helmwatch.StateDegraded:   {false: ReconStateDrifted, true: ReconStateDegraded},
		helmwatch.StateFailed:     {false: ReconStateReconciling, true: ReconStateDegraded},
		"":                        {false: ReconStateReconciling, true: ReconStateReconciling},
		"weird-future-state":      {false: ReconStateReconciling, true: ReconStateReconciling},
	}
	for _, status := range statuses {
		for _, stalled := range []bool{false, true} {
			for _, suspended := range []bool{false, true} {
				want := unsuspendedWant[status][stalled]
				if suspended {
					// #5485 defect 4 — suspension wins over EVERYTHING.
					want = ReconStateSuspended
				}
				got := reconStateForComponent(helmwatch.ComponentSnapshot{
					Status:    status,
					Stalled:   stalled,
					Suspended: suspended,
				})
				if got != want {
					t.Errorf("reconStateForComponent(status=%q stalled=%v suspended=%v) = %q, want %q",
						status, stalled, suspended, got, want)
				}
			}
		}
	}
}

// A suspended HR in the assembled DAG carries State=Suspended on its
// node and is EXCLUDED from the reconciled count — matching the drill
// panel, which only counts ManageStateReconciled rows.
func TestBuildReconciliationDAG_SuspendedNode(t *testing.T) {
	components := []helmwatch.ComponentSnapshot{
		// The live hw291 shape: suspended HR reported installed by the
		// Wave 5.103 readiness rule, with the suspension flagged.
		{AppID: "velero", Status: helmwatch.StateInstalled, Suspended: true},
		{AppID: "cilium", Status: helmwatch.StateInstalled},
	}
	dag := buildReconciliationDAG(components, nil, nil, true)
	if dag.Total != 2 {
		t.Fatalf("total: want 2, got %d", dag.Total)
	}
	byID := map[string]ReconciliationNode{}
	for _, n := range dag.Nodes {
		byID[n.ID] = n
	}
	if got := byID["bp-velero"].State; got != ReconStateSuspended {
		t.Errorf("suspended HR node state: want %q, got %q", ReconStateSuspended, got)
	}
	if got := byID["bp-cilium"].State; got != ReconStateReconciled {
		t.Errorf("healthy HR node state: want %q, got %q", ReconStateReconciled, got)
	}
	// Suspended is not Reconciled — the N/M header must not count it,
	// exactly as the drill panel's reconciled counter already behaves.
	if dag.Reconciled != 1 {
		t.Errorf("reconciled count: want 1 (suspended excluded), got %d", dag.Reconciled)
	}
}
