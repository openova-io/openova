// application_controller_5513_test.go — #5513.
//
// active-hot-standby renders/reports a 2-region pair over a singleton:
//
//  1. Task 2 — the Ready status message must derive the region count from the
//     EFFECTIVE materialised per-cluster set (perClusterStatus), never from the
//     DECLARED plan.Regions. A fan-out that collapses to one cluster while
//     spec.placement names two regions must report "installed across 1
//     region(s)", not "2 region(s)".
//
//  2. Task 5 — a downstream HelmRelease Ready=True means "manifests applied",
//     not "workload up". An Application whose backing CNPG Cluster is in the
//     terminal `unrecoverable` state must NOT report Ready.
//
// Both are asserted BOTH directions: the collapsed/broken case (fails pre-fix)
// and the genuinely-healthy case (must stay correct).
package controller

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// makeCNPGCluster returns a CNPG Cluster CR (postgresql.cnpg.io/v1) carrying
// the standard app.kubernetes.io/instance label (the Helm release name that
// installed it) and the given status.phase.
//
// #5955 — the status is built to the FULL live shape, not phase-only. Every one
// of the 11 CNPG Clusters dumped off hw292 publishes
// status.conditions[type=Ready] alongside the phase, and the readiness gate
// treats a CR that publishes NO readiness signal as unobservable (correctly —
// see TestBackingReadiness_NoSignal_IsUnobservable). A phase-only fixture would
// therefore have exercised the unobservable path while claiming to exercise the
// healthy one. Ready mirrors CNPG's own coupling: True only in the healthy
// phase.
func makeCNPGCluster(namespace, name, instance, phase string) *unstructured.Unstructured {
	ready := "False"
	readyInstances := int64(0)
	if phase == "Cluster in healthy state" {
		ready = "True"
		readyInstances = 1
	}
	return makeCNPGClusterWithStatus(namespace, name, instance, map[string]interface{}{
		"phase":          phase,
		"instances":      int64(1),
		"readyInstances": readyInstances,
		"conditions": []interface{}{
			map[string]interface{}{"type": "Ready", "status": ready},
		},
	})
}

// makeCNPGClusterWithStatus returns a CNPG Cluster CR with a verbatim status
// map, so a test can reproduce an exact live status key set — including the
// per-Org shape where status.readyInstances is ABSENT rather than zero.
func makeCNPGClusterWithStatus(namespace, name, instance string, status map[string]interface{}) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("postgresql.cnpg.io/v1")
	u.SetKind("Cluster")
	u.SetNamespace(namespace)
	u.SetName(name)
	u.SetLabels(map[string]string{InstanceLabel: instance})
	u.Object["status"] = status
	return u
}

// TestReconcile_5513_CollapsedFanout_ReportsMaterialisedRegionCount — Task 2,
// the failing-pre-fix direction. A Blueprint whose active-hot-standby variant
// declares ONE cluster (the collapse shape) while the Application declares TWO
// regions materialises a single HelmRelease. When that HR is Ready, the Ready
// message must say "installed across 1 region(s)" — deriving from the
// materialised perCluster set — not "2 region(s)" from the declared plan.
func TestReconcile_5513_CollapsedFanout_ReportsMaterialisedRegionCount(t *testing.T) {
	bp := makeBlueprintWithActiveHotStandbyTopology(
		"bp-postgres", "0.2.6", "mgmt",
		[]string{"mgmt-A"}, // AHS variant collapsed to ONE cluster
	)
	env := makeMultiRegionEnv("acme-prod", "acme", "prod") // 2 regions declared
	org := makeOrg("acme")
	app := makeApp("acme", "obs", "acme-prod", "bp-postgres", "0.2.6",
		"active-hotstandby",
		[]string{"hetzner-fsn-rtz-prod", "hetzner-nbg-rtz-prod"}, // plan.Regions = 2
		nil,
	)
	// The single materialised HR (fan-out names it <app>-<cluster>), Ready.
	hr := readyHR("obs-mgmt-a", "mgmt", "True")

	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp, hr)

	reconcileFromCluster(t, r, "acme", "obs")

	got := readApp(t, r, "acme", "obs")
	phase, _, msg := readPhaseAndReason(t, got)
	if phase != PhaseReady {
		t.Fatalf("phase = %q, want %q (the single per-cluster HR is Ready)", phase, PhaseReady)
	}
	if !strings.Contains(msg, "installed across 1 region(s)") {
		t.Errorf("Ready message = %q, want it to claim 1 region (the materialised count)", msg)
	}
	if strings.Contains(msg, "2 region(s)") {
		t.Errorf("Ready message = %q, still claims 2 region(s) over a single HelmRelease — the #5513 fabricated DR posture", msg)
	}
	// The per-cluster table must carry exactly the one materialised cluster.
	rows, _, _ := unstructured.NestedSlice(got.Object, "status", "perCluster")
	if len(rows) != 1 {
		t.Errorf("status.perCluster has %d rows, want 1 (the single materialised cluster)", len(rows))
	}
}

// TestReconcile_5513_HealthyTwoRegion_ReportsTwoRegions — Task 2, the healthy
// direction. A genuine 2-cluster active-hot-standby whose BOTH HRs are Ready
// must still report "installed across 2 region(s)". Guards the fix from
// under-counting a real multi-region install.
func TestReconcile_5513_HealthyTwoRegion_ReportsTwoRegions(t *testing.T) {
	bp := makeBlueprintWithActiveHotStandbyTopology(
		"bp-postgres", "0.2.6", "mgmt",
		[]string{"mgmt-A", "mgmt-B"}, // genuine 2-cluster variant
	)
	env := makeMultiRegionEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "obs", "acme-prod", "bp-postgres", "0.2.6",
		"active-hotstandby",
		[]string{"hetzner-fsn-rtz-prod", "hetzner-nbg-rtz-prod"},
		nil,
	)
	// Both per-cluster HRs materialised + Ready (fan-out authors both in mgmt).
	hrA := readyHR("obs-mgmt-a", "mgmt", "True")
	hrB := readyHR("obs-mgmt-b", "mgmt", "True")

	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp, hrA, hrB)

	reconcileFromCluster(t, r, "acme", "obs")

	got := readApp(t, r, "acme", "obs")
	phase, _, msg := readPhaseAndReason(t, got)
	if phase != PhaseReady {
		t.Fatalf("phase = %q, want %q (both per-cluster HRs are Ready)", phase, PhaseReady)
	}
	if !strings.Contains(msg, "installed across 2 region(s)") {
		t.Errorf("Ready message = %q, want it to claim 2 region(s) (both clusters materialised)", msg)
	}
}

// TestReconcile_5513_ReadyHR_UnrecoverableCNPG_NotReady — Task 5, the
// failing-pre-fix direction. An Application whose HelmRelease is Ready
// (manifests applied) but whose backing CNPG Cluster is in the terminal
// `unrecoverable` state must NOT report Ready — it downgrades to Degraded with
// reason BackingClusterUnrecoverable.
func TestReconcile_5513_ReadyHR_UnrecoverableCNPG_NotReady(t *testing.T) {
	bp := makeBlueprintWithActiveHotStandbyTopology(
		"bp-postgres", "0.2.6", "mgmt",
		[]string{"mgmt-A"},
	)
	env := makeEnv("acme-prod", "acme", "prod") // single-region → singleton
	org := makeOrg("acme")
	app := makeApp("acme", "obs", "acme-prod", "bp-postgres", "0.2.6",
		"single-region",
		[]string{"hetzner-fsn-rtz-prod"},
		nil,
	)
	// HelmRelease Ready=True (Helm applied the chart) ...
	hr := readyHR("obs-mgmt-a", "mgmt", "True")
	// ... but the backing CNPG Cluster it installed is unrecoverable. The
	// chart stamps app.kubernetes.io/instance = the HR/release name.
	cnpg := makeCNPGCluster("mgmt", "postgres", "obs-mgmt-a",
		"Cluster is unrecoverable and needs manual intervention")

	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp, hr, cnpg)

	reconcileFromCluster(t, r, "acme", "obs")

	got := readApp(t, r, "acme", "obs")
	phase, reason, msg := readPhaseAndReason(t, got)
	if phase == PhaseReady {
		t.Fatalf("phase = Ready over an unrecoverable backing CNPG — the #5513 fabricated posture (msg=%q)", msg)
	}
	if phase != PhaseDegraded {
		t.Fatalf("phase = %q, want %q (backing CNPG unrecoverable)", phase, PhaseDegraded)
	}
	if reason != ReasonBackingUnrecoverable {
		t.Errorf("Ready-condition reason = %q, want %q", reason, ReasonBackingUnrecoverable)
	}
	// And the Ready condition must be False, not True.
	if readyStatus := readyConditionStatus(t, got); readyStatus == "True" {
		t.Errorf("Ready condition status = True over an unrecoverable CNPG; want False")
	}
}

// TestReconcile_5513_ReadyHR_HealthyCNPG_StaysReady — Task 5, the healthy
// direction. The same shape but with a healthy backing CNPG must report Ready.
// Guards the guard from over-rotating every CNPG-backed app to Degraded.
func TestReconcile_5513_ReadyHR_HealthyCNPG_StaysReady(t *testing.T) {
	bp := makeBlueprintWithActiveHotStandbyTopology(
		"bp-postgres", "0.2.6", "mgmt",
		[]string{"mgmt-A"},
	)
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "obs", "acme-prod", "bp-postgres", "0.2.6",
		"single-region",
		[]string{"hetzner-fsn-rtz-prod"},
		nil,
	)
	hr := readyHR("obs-mgmt-a", "mgmt", "True")
	cnpg := makeCNPGCluster("mgmt", "postgres", "obs-mgmt-a", "Cluster in healthy state")

	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp, hr, cnpg)

	reconcileFromCluster(t, r, "acme", "obs")

	got := readApp(t, r, "acme", "obs")
	phase, _, msg := readPhaseAndReason(t, got)
	if phase != PhaseReady {
		t.Fatalf("phase = %q, want %q (backing CNPG is healthy — Ready must not be suppressed) msg=%q", phase, PhaseReady, msg)
	}
}

// TestCNPGClusterUnrecoverable_KeepsItsOwnReason — the terminal `unrecoverable`
// phase keeps the specific ReasonBackingUnrecoverable that #5513 shipped, even
// though #5955 generalised the gate to every backing kind. Operator tooling and
// the #5513 acceptance assert on that exact reason string.
func TestCNPGClusterUnrecoverable_KeepsItsOwnReason(t *testing.T) {
	for _, phase := range []string{
		"Cluster is unrecoverable and needs manual intervention",
		"Cluster in unrecoverable state",
	} {
		cr := makeCNPGCluster("mgmt", "postgres", "obs-mgmt-a", phase)
		state, detail := cnpgClusterReadiness(cr)
		if state != backingNotReady {
			t.Errorf("cnpgClusterReadiness(phase=%q) state = %v, want backingNotReady", phase, state)
		}
		if got := backingNotReadyReason(detail); got != ReasonBackingUnrecoverable {
			t.Errorf("reason for phase=%q = %q, want %q", phase, got, ReasonBackingUnrecoverable)
		}
	}
	// A healthy cluster is not swept up by the unrecoverable branch.
	state, _ := cnpgClusterReadiness(makeCNPGCluster("mgmt", "postgres", "obs-mgmt-a", "Cluster in healthy state"))
	if state != backingReady {
		t.Errorf("healthy cluster state = %v, want backingReady", state)
	}
	if state, _ := cnpgClusterReadiness(nil); state != backingUnobservable {
		t.Errorf("cnpgClusterReadiness(nil) state = %v, want backingUnobservable", state)
	}
}

// readyConditionStatus returns the status ("True"/"False"/…) of the Ready
// condition on an Application.
func readyConditionStatus(t *testing.T, app *unstructured.Unstructured) string {
	t.Helper()
	conds, _, _ := unstructured.NestedSlice(app.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if typ, _ := cm["type"].(string); typ == "Ready" {
			status, _ := cm["status"].(string)
			return status
		}
	}
	return ""
}
