// #5929 (UAT row 164) — a Job in a terminal Failed state must produce a leaf
// that reads FAILED, not one that reads "running" because a later run has not
// finished yet.
//
// Measured on hw292 (read-only kubectl): Job syft-grype-bp-syft-grype-29763030
// was Failed/BackoffLimitExceeded on 2026-08-03, while its successor
// ...-29770230 sat two days stale with zero active pods and no conditions at
// all. Both collapse onto the single `task-syft-sbom` scanner row (#3925). The
// failed run WAS recorded correctly as an Execution — but the leaf headline read
// `installing`, so the canvas reported 0 failed leaves out of 183 on a cluster
// that had a failed Job. That is the "invisible failing class" the §5a ingestion
// exists to kill (#3646).
//
// WHERE THE DEFECT IS NOT. Per-Job extraction is sound: jobRunStatus returns
// `failed` for the live object, and the rollup can already emit non-succeeded
// group states. So a test that feeds ONE Failed Job through and asserts the leaf
// is failed passes on the unfixed code and proves nothing. Every case below
// therefore models the real shape — a terminal failure collapsed together with a
// run that never terminated — and TestFailedScanRunSurvives_ControlSingleRun
// pins the weaker test's vacuity so nobody "simplifies" this suite back into it.
package helmwatch

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Live timings from the hw292 pair.
var (
	scanFailedAt = time.Date(2026, 8, 3, 20, 11, 32, 0, time.UTC)
	scanNewerAt  = time.Date(2026, 8, 8, 18, 35, 41, 0, time.UTC)
)

// syftScannerCronJob builds the bp-syft-grype CronJob as it exists on hw292 —
// named syft-grype-bp-syft-grype (NOT the bare release name), carrying the
// blueprint label that isDay2ScannerCronJob keys off. Without that label it
// would mint a separate cron leaf and the scanner row would never be seeded,
// which would change what this test is measuring.
func syftScannerCronJob() *unstructured.Unstructured {
	u := makeCronJob(syftGrypeNamespace, "syft-grype-bp-syft-grype")
	u.SetLabels(map[string]string{
		"catalyst.openova.io/blueprint": "bp-syft-grype",
		"app.kubernetes.io/instance":    syftGrypeCronName,
	})
	return u
}

// scanRun builds one spawned syft-grype run. condType "" models the live
// ...-29770230 shape: started, no terminal condition, no active pods.
func scanRun(name, condType string, at time.Time) *unstructured.Unstructured {
	return makeBatchJob(syftGrypeNamespace, name, "syft-grype-bp-syft-grype",
		condType, metav1.ConditionTrue, at)
}

// findLeaf returns the observation with the given name, or fails the test.
func findLeaf(t *testing.T, obs []ReconcilerObservation, name string) ReconcilerObservation {
	t.Helper()
	for _, o := range obs {
		if o.Name == name {
			return o
		}
	}
	names := make([]string, 0, len(obs))
	for _, o := range obs {
		names = append(names, o.Kind+"-"+o.Name)
	}
	t.Fatalf("no %q leaf in observations; got %v", name, names)
	return ReconcilerObservation{}
}

// TestFailedScanRunIsNotBuriedByUnterminatedSuccessor is the row-164
// reproduction: the exact hw292 pair, in the order the apiserver returned them.
func TestFailedScanRunIsNotBuriedByUnterminatedSuccessor(t *testing.T) {
	obs, err := ListReconcilerObservations(context.Background(), reconcilerFakeClient(
		syftScannerCronJob(),
		scanRun("syft-grype-bp-syft-grype-29763030", "Failed", scanFailedAt),
		scanRun("syft-grype-bp-syft-grype-29770230", "", scanNewerAt),
	))
	if err != nil {
		t.Fatalf("ListReconcilerObservations: %v", err)
	}

	leaf := findLeaf(t, obs, syftScanIdentity)
	if leaf.Status != ObsStatusFailed {
		t.Errorf("collapsed scanner leaf status = %q, want %q — a failed Job produced no failed leaf (0 failed out of 183 on hw292)",
			leaf.Status, ObsStatusFailed)
	}
	if leaf.ObservedAt.IsZero() {
		t.Error("collapsed scanner leaf has no ObservedAt — a terminated failure must carry its finish time")
	}

	// The terminated execution must itself be terminal: failed AND finished.
	var failedRuns int
	for _, e := range leaf.Executions {
		if e.Name != "syft-grype-bp-syft-grype-29763030" {
			continue
		}
		failedRuns++
		if e.Status != ObsStatusFailed {
			t.Errorf("execution %s status = %q, want %q", e.Name, e.Status, ObsStatusFailed)
		}
		if e.FinishedAt.IsZero() {
			t.Errorf("execution %s has a zero FinishedAt — a Failed Job must terminate its execution", e.Name)
		}
	}
	if failedRuns != 1 {
		t.Errorf("expected the failed run to appear exactly once as an Execution, got %d", failedRuns)
	}

	// The collapse itself must still hold: ONE row for the scanner, not one per run.
	var scannerRows int
	for _, o := range obs {
		if o.Name == syftScanIdentity {
			scannerRows++
		}
	}
	if scannerRows != 1 {
		t.Errorf("scanner collapsed into %d rows, want exactly 1 (#3925)", scannerRows)
	}
}

// TestFailedScanRunSurvivesEitherListOrder — the apiserver guarantees no
// chronological order, so the verdict must not depend on which run is seen
// first. Guarding only the failed-then-running direction would leave the
// running-then-failed direction reading "running" through the recency rule.
func TestFailedScanRunSurvivesEitherListOrder(t *testing.T) {
	failed := scanRun("syft-grype-bp-syft-grype-29763030", "Failed", scanFailedAt)
	running := scanRun("syft-grype-bp-syft-grype-29770230", "", scanNewerAt)

	for _, tc := range []struct {
		name string
		objs []*unstructured.Unstructured
	}{
		{"failed run listed first", []*unstructured.Unstructured{failed, running}},
		{"unterminated run listed first", []*unstructured.Unstructured{running, failed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obs, err := ListReconcilerObservations(context.Background(), reconcilerFakeClient(
				syftScannerCronJob(), tc.objs[0], tc.objs[1],
			))
			if err != nil {
				t.Fatalf("ListReconcilerObservations: %v", err)
			}
			if got := findLeaf(t, obs, syftScanIdentity).Status; got != ObsStatusFailed {
				t.Errorf("scanner leaf status = %q, want %q", got, ObsStatusFailed)
			}
		})
	}
}

// TestSucceededRunClearsAnEarlierFailure — the counter-case. The fix must not
// pin a leaf red forever: a run that actually TERMINATES successfully after the
// failure resolves it. Without this, returning a constant "failed" would satisfy
// every assertion above.
func TestSucceededRunClearsAnEarlierFailure(t *testing.T) {
	obs, err := ListReconcilerObservations(context.Background(), reconcilerFakeClient(
		syftScannerCronJob(),
		scanRun("syft-grype-bp-syft-grype-29763030", "Failed", scanFailedAt),
		scanRun("syft-grype-bp-syft-grype-29770230", "Complete", scanNewerAt),
	))
	if err != nil {
		t.Fatalf("ListReconcilerObservations: %v", err)
	}
	if got := findLeaf(t, obs, syftScanIdentity).Status; got != ObsStatusSucceeded {
		t.Errorf("scanner leaf status = %q, want %q — a later SUCCEEDED run must clear the failure", got, ObsStatusSucceeded)
	}
}

// TestStaleSucceededRunStillCannotOverwriteAFailure — the pre-existing recency
// contract must survive the change: an OLDER Succeeded run arriving after a
// newer Failed one still must not overwrite the failure.
func TestStaleSucceededRunStillCannotOverwriteAFailure(t *testing.T) {
	obs, err := ListReconcilerObservations(context.Background(), reconcilerFakeClient(
		syftScannerCronJob(),
		scanRun("syft-grype-bp-syft-grype-29770230", "Failed", scanNewerAt),
		scanRun("syft-grype-bp-syft-grype-29763030", "Complete", scanFailedAt),
	))
	if err != nil {
		t.Fatalf("ListReconcilerObservations: %v", err)
	}
	if got := findLeaf(t, obs, syftScanIdentity).Status; got != ObsStatusFailed {
		t.Errorf("scanner leaf status = %q, want %q — a stale Succeeded run overwrote a newer failure", got, ObsStatusFailed)
	}
}

// TestCronLeafFailureSurvivesUnterminatedSuccessor — the same defect lived in
// the cron leaf's copy of the recency block, which is the ORIGINAL #3646
// concern (a Failed openbao-snapshot-save hiding behind a green row). Fixing
// only the scanner path would have left this one live.
func TestCronLeafFailureSurvivesUnterminatedSuccessor(t *testing.T) {
	obs, err := ListReconcilerObservations(context.Background(), reconcilerFakeClient(
		makeCronJob("openbao", "openbao-snapshot-save"),
		makeBatchJob("openbao", "openbao-snapshot-save-29763030", "openbao-snapshot-save",
			"Failed", metav1.ConditionTrue, scanFailedAt),
		makeBatchJob("openbao", "openbao-snapshot-save-29770230", "openbao-snapshot-save",
			"", metav1.ConditionTrue, scanNewerAt),
	))
	if err != nil {
		t.Fatalf("ListReconcilerObservations: %v", err)
	}
	leaf := findLeaf(t, obs, "openbao-snapshot-save")
	if leaf.Kind != ReconcilerKindCron {
		t.Fatalf("leaf kind = %q, want %q", leaf.Kind, ReconcilerKindCron)
	}
	if leaf.Status != ObsStatusFailed {
		t.Errorf("cron leaf status = %q, want %q — a failing snapshot CronJob is hiding behind an in-flight run",
			leaf.Status, ObsStatusFailed)
	}
}

// TestTaskLeafFailureSurvivesUnterminatedSuccessor — third copy of the same
// block, on the standalone task leaf (#3916 collapse).
func TestTaskLeafFailureSurvivesUnterminatedSuccessor(t *testing.T) {
	// Run suffixes must carry a digit: isRunSuffix deliberately treats an
	// all-letters 5-char tail as a real word, so "-abcde" would NOT collapse
	// and the two runs would land on separate leaves.
	obs, err := ListReconcilerObservations(context.Background(), reconcilerFakeClient(
		makeBatchJob("catalyst", "db-migrate-a1b2c", "", "Failed", metav1.ConditionTrue, scanFailedAt),
		makeBatchJob("catalyst", "db-migrate-d3e4f", "", "", metav1.ConditionTrue, scanNewerAt),
	))
	if err != nil {
		t.Fatalf("ListReconcilerObservations: %v", err)
	}
	leaf := findLeaf(t, obs, "db-migrate")
	if leaf.Status != ObsStatusFailed {
		t.Errorf("task leaf status = %q, want %q", leaf.Status, ObsStatusFailed)
	}
}

// TestFailedScanRunSurvives_ControlSingleRun documents WHY the cases above are
// shaped the way they are. A lone Failed Job already produced a failed leaf on
// the UNFIXED code — so this assertion passes either way and is worthless as a
// regression guard. It is kept only to pin that fact.
func TestFailedScanRunSurvives_ControlSingleRun(t *testing.T) {
	obs, err := ListReconcilerObservations(context.Background(), reconcilerFakeClient(
		syftScannerCronJob(),
		scanRun("syft-grype-bp-syft-grype-29763030", "Failed", scanFailedAt),
	))
	if err != nil {
		t.Fatalf("ListReconcilerObservations: %v", err)
	}
	if got := findLeaf(t, obs, syftScanIdentity).Status; got != ObsStatusFailed {
		t.Errorf("single failed run: leaf status = %q, want %q", got, ObsStatusFailed)
	}
	// If this ever becomes the ONLY case in this file, the row-164 defect can
	// return undetected.
}

// TestAdoptRunHeadlineVerdictRule exercises the shared helper directly, so the
// three call sites above are pinned by behaviour AND the rule itself is pinned
// by table.
func TestAdoptRunHeadlineVerdictRule(t *testing.T) {
	older := scanFailedAt
	newer := scanNewerAt

	for _, tc := range []struct {
		name       string
		start      ReconcilerObservation
		status     string
		runAt      time.Time
		wantStatus string
	}{
		{"in-flight run cannot clear a failure",
			ReconcilerObservation{Status: ObsStatusFailed, ObservedAt: older},
			ObsStatusRunning, newer, ObsStatusFailed},
		{"failure displaces a pending seed",
			ReconcilerObservation{Status: ObsStatusPending},
			ObsStatusFailed, older, ObsStatusFailed},
		{"failure displaces an in-flight headline even when older",
			ReconcilerObservation{Status: ObsStatusRunning, ObservedAt: newer},
			ObsStatusFailed, older, ObsStatusFailed},
		{"newer success clears a failure",
			ReconcilerObservation{Status: ObsStatusFailed, ObservedAt: older},
			ObsStatusSucceeded, newer, ObsStatusSucceeded},
		{"older success does not clear a failure",
			ReconcilerObservation{Status: ObsStatusFailed, ObservedAt: newer},
			ObsStatusSucceeded, older, ObsStatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.start
			adoptRunHeadline(&c, tc.status, "msg", tc.runAt)
			if c.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", c.Status, tc.wantStatus)
			}
		})
	}
}
