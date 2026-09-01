// suspended_dormant_treemap_test.go — a SUSPENDED (spec.suspend=true)
// reconciler must render Dormant, never Pending or Succeeded, so the
// sovereign-admin dashboard treemap routes it into its Dormant progress
// bucket instead of masquerading as a healthy green leaf (install HR) or a
// queued Pending leaf (reconciler). This pins the "suspension wins over every
// other state" precedence — the same rule reconciliation_dag.go applies —
// onto the /jobs→treemap seed path (both the install-HR InformerSeed leg and
// the §5a ReconcilerObservation leg).
package jobs

import (
	"testing"
	"time"
)

// A suspended HelmRelease reports State=installed (Wave 5.103 #2447) so it
// never blocks Phase-1 readiness; the InformerSeed's Suspended flag is the
// only signal the seed retains, and it must produce a Dormant leaf — not the
// green Succeeded leaf State=installed would otherwise map to.
func TestSeedJobsFromInformerList_SuspendedRendersDormant(t *testing.T) {
	st, br, depID := newBridgeFixture(t)

	seeds := []InformerSeed{
		// The tethered cutover chart: installed but parked (suspended).
		{Component: "self-sovereign-cutover", State: HelmStateInstalled, Suspended: true, Message: "spec.suspend=true"},
		// Control: a genuinely-installed, unsuspended HR stays Succeeded.
		{Component: "cilium", State: HelmStateInstalled, Message: "Helm install succeeded"},
	}
	if _, _, err := br.SeedJobsFromInformerList(seeds); err != nil {
		t.Fatalf("SeedJobsFromInformerList: %v", err)
	}

	got := leafJobs(mustList(t, st, depID))

	dormant := leafByName(t, got, JobNamePrefix+"self-sovereign-cutover")
	if dormant.Status != StatusDormant {
		t.Errorf("suspended HR leaf: want Status %q, got %q (a suspended HR must NOT render Succeeded/Pending)", StatusDormant, dormant.Status)
	}
	// A parked HR is Job-only — no synthetic Execution that would
	// FinishExecution-stamp the leaf back to Succeeded and clobber Dormant.
	if dormant.LatestExecutionID != "" {
		t.Errorf("suspended (dormant) HR leaf must be Job-only, got LatestExecutionID %q", dormant.LatestExecutionID)
	}

	control := leafByName(t, got, JobNamePrefix+"cilium")
	if control.Status != StatusSucceeded {
		t.Errorf("unsuspended installed HR leaf: want %q, got %q", StatusSucceeded, control.Status)
	}
}

// The §5a reconciler leg: a suspended Flux Kustomization and a suspended
// CronJob (even one with a SUCCEEDED historical run) both render Dormant —
// suspension wins over the Ready condition and over the latest run.
func TestSeedReconcilerObservations_SuspendedRendersDormant(t *testing.T) {
	st, br, depID := newBridgeFixture(t)

	obs := []ReconcilerObservation{
		// Suspended Kustomization that is otherwise Ready=True (→ succeeded).
		{Kind: ObsKindReconcile, Name: "flux-system", Status: HelmStateInstalled, Suspended: true, Message: "suspended"},
		// Suspended CronJob whose latest run SUCCEEDED — Dormant still wins
		// over the run-derived headline (would be Succeeded without suspend).
		{
			Kind:      ObsKindCron,
			Name:      "openbao-snapshot-save",
			Status:    HelmStatePending,
			Suspended: true,
			Executions: []ReconcilerExecutionObservation{
				{Name: "openbao-snapshot-save-28001", Status: HelmStateInstalled, StartedAt: time.Now().Add(-2 * time.Minute), FinishedAt: time.Now().Add(-time.Minute)},
			},
		},
		// Control: an unsuspended Ready Kustomization stays Succeeded.
		{Kind: ObsKindReconcile, Name: "infra", Status: HelmStateInstalled},
	}
	if _, _, err := br.SeedReconcilerObservations(obs); err != nil {
		t.Fatalf("SeedReconcilerObservations: %v", err)
	}

	got := mustList(t, st, depID)

	susKustomization := leafByName(t, got, ReconcileJobPrefix+"flux-system")
	if susKustomization.Status != StatusDormant {
		t.Errorf("suspended Kustomization leaf: want %q, got %q", StatusDormant, susKustomization.Status)
	}

	susCron := leafByName(t, got, CronJobPrefix+"openbao-snapshot-save")
	if susCron.Status != StatusDormant {
		t.Errorf("suspended CronJob leaf (succeeded run present): want %q, got %q (suspension must win over the run headline)", StatusDormant, susCron.Status)
	}

	control := leafByName(t, got, ReconcileJobPrefix+"infra")
	if control.Status != StatusSucceeded {
		t.Errorf("unsuspended Ready Kustomization leaf: want %q, got %q", StatusSucceeded, control.Status)
	}
}
