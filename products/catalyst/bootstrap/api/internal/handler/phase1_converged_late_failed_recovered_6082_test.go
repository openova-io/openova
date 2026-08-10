package handler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// #6082 — the LATCH shape, measured live on hw293 (dep a0077ba47e3720e5).
//
// Phase-1 terminated with exactly one FAILED component out of 67 — and that
// component was `self-sovereign-cutover` itself, at chart 0.1.177, on the
// 1 MiB Helm release-Secret limit (#6004). markPhase1Done's `failed > 0` arm
// stamped Status="failed" + Phase1Outcome="failed". Flux then retried on its
// own infinite loop and installed 0.1.179 Ready=True three hours later, so the
// cluster is fully converged — but nothing re-reads the cluster:
//
//   - fireHandover runs only under `OutcomeReady && finalStatus=="ready"`,
//   - MintHandoverToken 409s for any status outside {ready, adopted},
//   - and those two are the ONLY producers of the Sovereign-side handover
//     marker secret/catalyst/tofu-phase0-archive.
//
// Without that marker /internal/cutover/trigger answers 425 handover-incomplete
// forever and the level-triggered cutover reconciler returns silently on
// `!sealed` every 120s. The record can never leave "failed", so the gate asks a
// question that can no longer be answered yes.
//
// The rescue is the only heal path and it pre-filtered on the FROZEN
// classification, never on the live cluster. These guards lock the extension:
// an OutcomeFailed record is a rescue CANDIDATE, and the live census decides —
// with POSITIVE per-component proof, not the floor+ratio backstop alone.

// TestShouldConvergedLateRescue_FailedOutcomeIsCandidate_6082 locks the gate
// change: a failed+OutcomeFailed record with no fired handover qualifies for
// the census. Everything else about the gate is unchanged.
func TestShouldConvergedLateRescue_FailedOutcomeIsCandidate_6082(t *testing.T) {
	dir := t.TempDir()
	h := &Handler{log: silentLogger(), kubeconfigsDir: dir}
	const depID = "hw293latch"
	if err := os.WriteFile(filepath.Join(dir, depID+".yaml"), []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	dep := &Deployment{
		ID:     depID,
		Status: "failed",
		Request: provisioner.Request{
			Regions: []provisioner.RegionSpec{{Provider: "huawei"}, {Provider: "huawei"}},
		},
		Result: &provisioner.Result{
			Phase1Outcome:   helmwatch.OutcomeFailed,
			ComponentStates: map[string]string{"self-sovereign-cutover": helmwatch.StateFailed},
		},
	}
	if !h.shouldConvergedLateRescue(dep) {
		t.Fatalf("failed+OutcomeFailed+unfired must be a rescue CANDIDATE (#6082, the hw293 latch) — the live census decides, not the frozen classification")
	}
}

// TestRunConvergedLateRescue_FailedComponentRecovered_6082 is the hw293 shape
// end-to-end: the one component the record recorded as failed is Ready=True on
// the live cluster now, so the record flips ready and the producer chain fires.
func TestRunConvergedLateRescue_FailedComponentRecovered_6082(t *testing.T) {
	dir := t.TempDir()
	h := &Handler{log: silentLogger(), kubeconfigsDir: dir}
	const depID = "hw293recovered"
	if err := os.WriteFile(filepath.Join(dir, depID+".yaml"), []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}

	orig := censusHelmReleases
	// The live hw293 numbers: 71 of 77 Ready, and bp-self-sovereign-cutover
	// among the Ready set. The 6 non-Ready are provider-inapplicable Hetzner
	// charts + per-Org walk apps, none of which the record recorded as failed.
	censusHelmReleases = func(string) (int, int, map[string]bool, error) {
		return 71, 77, map[string]bool{"self-sovereign-cutover": true, "cilium": true}, nil
	}
	defer func() { censusHelmReleases = orig }()

	dep := &Deployment{
		ID:     depID,
		Status: "failed",
		Error:  "Phase 1 finished with 1 failed component(s); see ComponentStates for the per-component breakdown",
		Request: provisioner.Request{
			Regions: []provisioner.RegionSpec{{Provider: "huawei"}, {Provider: "huawei"}},
		},
		Result: &provisioner.Result{
			Phase1Outcome: helmwatch.OutcomeFailed,
			ComponentStates: map[string]string{
				"self-sovereign-cutover": helmwatch.StateFailed,
				"cilium":                 helmwatch.StateInstalled,
			},
		},
	}

	h.runConvergedLateRescue(dep)

	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.Status != "ready" {
		t.Fatalf("a record whose ONLY failed component is Ready=True on the live cluster must flip ready, got %q", dep.Status)
	}
	if dep.Error != "" {
		t.Fatalf("rescued record must not keep the stale Phase-1 failure text on dep.Error (it renders as a FailureCard), got %q", dep.Error)
	}
	if dep.Result.Phase1FinishedAt == nil {
		t.Fatalf("rescue must stamp Phase1FinishedAt")
	}
}

// TestRunConvergedLateRescue_FailedComponentStillFailed_6082 is the control
// that shares the suspect property: same outcome, same status, same census
// floor + ratio — the ONLY difference is that the recorded-failed component is
// still not Ready. It must NOT rescue.
//
// This is what makes the floor+ratio backstop insufficient on its own for this
// arm: 71/77 clears both the absolute floor (45) and the 90% ratio (710 >= 693)
// while a genuinely-failed component sits inside the tolerated 10%.
func TestRunConvergedLateRescue_FailedComponentStillFailed_6082(t *testing.T) {
	dir := t.TempDir()
	h := &Handler{log: silentLogger(), kubeconfigsDir: dir}
	const depID = "hw293stillfailed"
	if err := os.WriteFile(filepath.Join(dir, depID+".yaml"), []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}

	orig := censusHelmReleases
	censusHelmReleases = func(string) (int, int, map[string]bool, error) {
		// Identical counts to the recovered case — only the Ready SET differs.
		return 71, 77, map[string]bool{"cilium": true}, nil
	}
	defer func() { censusHelmReleases = orig }()

	dep := &Deployment{
		ID:     depID,
		Status: "failed",
		Request: provisioner.Request{
			Regions: []provisioner.RegionSpec{{Provider: "huawei"}, {Provider: "huawei"}},
		},
		Result: &provisioner.Result{
			Phase1Outcome: helmwatch.OutcomeFailed,
			ComponentStates: map[string]string{
				"self-sovereign-cutover": helmwatch.StateFailed,
				"cilium":                 helmwatch.StateInstalled,
			},
		},
	}

	h.runConvergedLateRescue(dep)

	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.Status != "failed" {
		t.Fatalf("a record whose recorded-failed component is STILL not Ready must stay failed, got %q — the floor+ratio backstop tolerates ~10%% non-Ready and cannot carry this arm alone", dep.Status)
	}
}

// TestRunConvergedLateRescue_FailedComponentAbsentFromCluster_6082 pins the
// evidence direction: a recorded-failed component that the census does not
// observe Ready is NOT treated as recovered. Absence of the HelmRelease is
// absence of evidence, and this gate requires POSITIVE evidence.
func TestRunConvergedLateRescue_FailedComponentAbsentFromCluster_6082(t *testing.T) {
	dir := t.TempDir()
	h := &Handler{log: silentLogger(), kubeconfigsDir: dir}
	const depID = "hw293absent"
	if err := os.WriteFile(filepath.Join(dir, depID+".yaml"), []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}

	orig := censusHelmReleases
	censusHelmReleases = func(string) (int, int, map[string]bool, error) {
		return 71, 77, map[string]bool{}, nil
	}
	defer func() { censusHelmReleases = orig }()

	dep := &Deployment{
		ID:     depID,
		Status: "failed",
		Request: provisioner.Request{
			Regions: []provisioner.RegionSpec{{Provider: "huawei"}},
		},
		Result: &provisioner.Result{
			Phase1Outcome:   helmwatch.OutcomeFailed,
			ComponentStates: map[string]string{"self-sovereign-cutover": helmwatch.StateFailed},
		},
	}

	h.runConvergedLateRescue(dep)

	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.Status != "failed" {
		t.Fatalf("a recorded-failed component with no POSITIVE Ready observation must not rescue, got %q", dep.Status)
	}
}

// TestRunConvergedLateRescue_TimeoutArmSkipsComponentProof_6082 keeps the
// #3319 contract intact: a TIMEOUT record has no hard-failed component by
// definition, so the per-component proof must not apply to it and the
// floor+ratio census alone still rescues.
func TestRunConvergedLateRescue_TimeoutArmSkipsComponentProof_6082(t *testing.T) {
	dir := t.TempDir()
	h := &Handler{log: silentLogger(), kubeconfigsDir: dir}
	const depID = "hw130timeout"
	if err := os.WriteFile(filepath.Join(dir, depID+".yaml"), []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}

	orig := censusHelmReleases
	censusHelmReleases = func(string) (int, int, map[string]bool, error) {
		return 66, 66, map[string]bool{}, nil
	}
	defer func() { censusHelmReleases = orig }()

	dep := &Deployment{
		ID:     depID,
		Status: "failed",
		Request: provisioner.Request{
			Regions: []provisioner.RegionSpec{{Provider: "huawei"}},
		},
		Result: &provisioner.Result{
			Phase1Outcome:   helmwatch.OutcomeTimeout,
			ComponentStates: map[string]string{"keycloak": helmwatch.StateInstalling},
		},
	}

	h.runConvergedLateRescue(dep)

	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.Status != "ready" {
		t.Fatalf("#3319 TIMEOUT rescue must be unchanged by the #6082 per-component proof, got %q", dep.Status)
	}
}
