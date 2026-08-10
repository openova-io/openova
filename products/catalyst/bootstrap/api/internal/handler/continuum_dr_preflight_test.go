// continuum_dr_preflight_test.go — regression coverage for the DR runbook
// preflight matrix (POST /api/v1/sovereigns/{id}/dr/runbook/preflight, and the
// same matrix run as step 1 of .../dr/runbook/playback).
//
// WHY THIS FILE EXISTS. runDRPreflight had ZERO tests. It declared all ten
// checks `Status: "Pass"` as their literal initial value and then re-read only
// two of them, so eight checks reported Pass having probed nothing. Measured on
// hw292 (dep 1c56518035a83e03, 2026-08-10): `kubectl get cnpgpairs.dr.openova.io
// -A` returns ZERO instances while the matrix reported
// `preflight-02 cnpgpair-streaming = Pass` — a check that could not fail on the
// exact precondition it names, pointed at the DR path.
//
// The systemic half: no check could ever reach "Fail", so classifyPreflight
// could never return "NotReady", so HandleDRRunbookPlayback's
// `if preOverall == "NotReady" { abort }` branch was UNREACHABLE. The safety
// gate in front of a mutating DR operation could not fire.
//
// Every test below is written to FAIL against that prior implementation:
//   - the Fail cases got "Pass" from the constant;
//   - the zero-CNPGPair case got "Pass" from the constant;
//   - the unmeasured checks got "Pass" from the constant;
//   - NotReady/blockingChecks were unreachable.
//
// TestDRPreflight_HealthyMatrixStillPasses is the vacuity control: it proves
// the probes can still return Pass on a genuinely healthy Sovereign, so this
// file is not simply hardwiring the matrix to Fail.
package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// preflightListKinds — every resource runDRPreflight LISTs. The fake dynamic
// client panics on a LIST whose list-kind it does not know, so cnpgpairs and
// pdms must be registered here even (especially) for the zero-instance cases:
// a panic would otherwise be indistinguishable from the defect under test.
func preflightListKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		ContinuumGVR():  "ContinuumList",
		cnpgClusterGVR:  "ClusterList",
		drCNPGPairGVR(): "CNPGPairList",
		drPDMGVR():      "PDMList",
	}
}

func fakePreflightHandler(t *testing.T, id string, seed ...runtime.Object) (*Handler, string) {
	t.Helper()
	h := NewWithPDM(silentLogger(), &fakePDM{})
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(), preflightListKinds(), seed...)
	h.dynamicFactory = func(_ string) (dynamic.Interface, error) { return client, nil }
	dep := installUserAccessDeployment(t, h, id)
	return h, dep.ID
}

func runPreflight(t *testing.T, h *Handler, depID string) []drRunbookPreflightCheck {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+depID+"/dr/runbook/preflight", nil)
	return h.runDRPreflight(req, depID)
}

func checkByID(t *testing.T, checks []drRunbookPreflightCheck, id string) drRunbookPreflightCheck {
	t.Helper()
	for _, c := range checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("preflight check %q not found in matrix of %d", id, len(checks))
	return drRunbookPreflightCheck{}
}

// newCNPGPairCRFixture — a CNPGPair.dr.openova.io CR (the kind preflight-02
// NAMES). NOT to be confused with a cnpg Cluster named `cnpg-pair-*`.
func newCNPGPairCRFixture(name, namespace string, streaming bool) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("dr.openova.io/v1")
	u.SetKind("CNPGPair")
	u.SetName(name)
	u.SetNamespace(namespace)
	_ = unstructured.SetNestedMap(u.Object, map[string]interface{}{
		"primaryCluster": name + "-primary",
		"replicaCluster": name + "-replica",
		"topology":       "active-hot-standby",
	}, "spec")
	_ = unstructured.SetNestedMap(u.Object, map[string]interface{}{
		"streaming": streaming,
	}, "status")
	return u
}

func newPDMFixture(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("dr.openova.io/v1")
	u.SetKind("PDM")
	u.SetName(name)
	u.SetNamespace("catalyst")
	return u
}

// markCNPGClusterNotReady flips a cnpg Cluster half to the proven region-kill
// shape (#4901): the Cluster is PRESENT but reports Ready=False.
func markCNPGClusterNotReady(c *unstructured.Unstructured) {
	_ = unstructured.SetNestedSlice(c.Object, []interface{}{
		map[string]interface{}{"type": "Ready", "status": "False"},
	}, "status", "conditions")
	_ = unstructured.SetNestedField(c.Object, int64(0), "status", "readyInstances")
}

func withContinuumStatus(cr *unstructured.Unstructured, phase string, lagSeconds interface{}) *unstructured.Unstructured {
	_ = unstructured.SetNestedField(cr.Object, phase, "status", "phase")
	if lagSeconds != nil {
		_ = unstructured.SetNestedField(cr.Object, lagSeconds, "status", "replicationLagSeconds")
	}
	return cr
}

// ── preflight-02: the #4986 shape ────────────────────────────────────

// THE hw292 CASE. The CNPGPair CRD is installed and holds ZERO instances, and
// no cnpg Cluster carries the pair label. `cnpgpair-streaming` must NOT report
// Pass — there is nothing to have observed streaming.
func TestDRPreflight_CNPGPairStreamingIsNotPassWhenNoPairExists(t *testing.T) {
	cr := newContinuumUnstructured("dr-shared-pg", "shared-data", "shared-pg",
		"me-east-215-a", []string{"me-east-215-b"})
	h, depID := fakePreflightHandler(t, "dep-preflight-nopair", cr)

	c := checkByID(t, runPreflight(t, h, depID), "preflight-02")

	if c.Status == preflightPass {
		t.Errorf("cnpgpair-streaming: got Pass with ZERO CNPGPair CRs and ZERO labelled cnpg pairs — "+
			"that is the hw292 #4986 reading: a check that cannot fail on the precondition it names (msg=%q)", c.Message)
	}
	if c.Message == "" {
		t.Error("cnpgpair-streaming: a non-Pass verdict must say what was (not) measured")
	}
}

// A CNPGPair CR that exists and reports streaming=false is a hard Fail, not a
// Warn and certainly not the Pass the constant asserted.
func TestDRPreflight_CNPGPairNotStreamingIsCriticalFail(t *testing.T) {
	h, depID := fakePreflightHandler(t, "dep-preflight-pair-down",
		newCNPGPairCRFixture("shared-pg", "shared-data", false))

	c := checkByID(t, runPreflight(t, h, depID), "preflight-02")

	if c.Status != preflightFail {
		t.Errorf("cnpgpair-streaming: got %q want Fail — the CNPGPair CR reports status.streaming=false (msg=%q)",
			c.Status, c.Message)
	}
	if c.Severity != "critical" {
		t.Errorf("cnpgpair-streaming severity: got %q want critical — a non-streaming DR pair must block a playback", c.Severity)
	}
}

// The region-kill shape (#4901): no CNPGPair CR, but the cnpg Cluster halves
// are labelled and the REPLICA half is present-but-not-Ready.
func TestDRPreflight_ReplicaHalfPresentButNotReadyIsFail(t *testing.T) {
	primary, replica := newCNPGPairFixture("shared-pg", "shared-data", "me-east-215-a", "me-east-215-b")
	markCNPGClusterNotReady(replica)
	h, depID := fakePreflightHandler(t, "dep-preflight-replica-down", primary, replica)

	c := checkByID(t, runPreflight(t, h, depID), "preflight-02")

	if c.Status != preflightFail {
		t.Errorf("cnpgpair-streaming: got %q want Fail — the replica half is present and NOT ready, "+
			"which is the proven region-kill state (msg=%q)", c.Status, c.Message)
	}
}

// The per-region split (#5511 class): this client sees only the primary half
// because the replica Cluster lives in the peer region. Honest verdict is
// UNVERIFIED — neither a Pass nor a Fail.
func TestDRPreflight_ReplicaHalfInvisibleIsWarnNotPass(t *testing.T) {
	primary, _ := newCNPGPairFixture("shared-pg", "shared-data", "me-east-215-a", "me-east-215-b")
	h, depID := fakePreflightHandler(t, "dep-preflight-split", primary)

	c := checkByID(t, runPreflight(t, h, depID), "preflight-02")

	if c.Status != preflightWarn {
		t.Errorf("cnpgpair-streaming: got %q want Warn — only the primary half is visible from this "+
			"cluster's API, so streaming cannot be observed here (msg=%q)", c.Status, c.Message)
	}
}

// ── preflight-01 + preflight-03 ──────────────────────────────────────

func TestDRPreflight_DegradedContinuumIsCriticalFail(t *testing.T) {
	healthy := withContinuumStatus(newContinuumUnstructured(
		"dr-spine-gitea", "catalyst", "spine-gitea", "me-east-215-a", []string{"me-east-215-b"}),
		"Healthy", int64(0))
	degraded := withContinuumStatus(newContinuumUnstructured(
		"dr-shared-pg", "shared-data", "shared-pg", "me-east-215-a", []string{"me-east-215-b"}),
		"Degraded", int64(0))
	h, depID := fakePreflightHandler(t, "dep-preflight-degraded", healthy, degraded)

	c := checkByID(t, runPreflight(t, h, depID), "preflight-01")

	if c.Status != preflightFail {
		t.Errorf("continuum-cr-ready: got %q want Fail — dr-shared-pg reports phase=Degraded (msg=%q)",
			c.Status, c.Message)
	}
}

// FailedOver is the post-switchover STEADY state and must stay ready, or the
// preflight would block the failback of an already-failed-over Sovereign.
func TestDRPreflight_FailedOverPhaseIsStillReady(t *testing.T) {
	cr := withContinuumStatus(newContinuumUnstructured(
		"dr-shared-pg", "shared-data", "shared-pg", "me-east-215-a", []string{"me-east-215-b"}),
		"FailedOver", int64(0))
	h, depID := fakePreflightHandler(t, "dep-preflight-failedover", cr)

	c := checkByID(t, runPreflight(t, h, depID), "preflight-01")

	if c.Status != preflightPass {
		t.Errorf("continuum-cr-ready: got %q want Pass — FailedOver is the post-switchover steady state "+
			"and blocking it strands the failback (msg=%q)", c.Status, c.Message)
	}
}

func TestDRPreflight_WALLagOverRTOIsCriticalFail(t *testing.T) {
	cr := withContinuumStatus(newContinuumUnstructured(
		"dr-shared-pg", "shared-data", "shared-pg", "me-east-215-a", []string{"me-east-215-b"}),
		"Healthy", int64(320))
	_ = unstructured.SetNestedField(cr.Object, "30s", "spec", "rto")
	h, depID := fakePreflightHandler(t, "dep-preflight-lag", cr)

	c := checkByID(t, runPreflight(t, h, depID), "preflight-03")

	if c.Status != preflightFail {
		t.Errorf("wal-lag-under-rto: got %q want Fail — 320s lag against a 30s RTO budget (msg=%q)",
			c.Status, c.Message)
	}
}

// THE VACUITY TRAP THIS GUARDS. A CR that omits status.replicationLagSeconds
// must read "not reported", never a silent zero that passes the RTO
// comparison — absent evidence and a perfectly caught-up standby are opposite
// readings of the same field.
func TestDRPreflight_AbsentLagKeyIsNotAMeasuredZero(t *testing.T) {
	cr := newContinuumUnstructured("dr-spine-openbao", "catalyst", "spine-openbao",
		"me-east-215-a", []string{"me-east-215-b"})
	// deliberately no status.replicationLagSeconds
	h, depID := fakePreflightHandler(t, "dep-preflight-nolag", cr)

	c := checkByID(t, runPreflight(t, h, depID), "preflight-03")

	if c.Status == preflightPass {
		t.Errorf("wal-lag-under-rto: got Pass from a CR that reports NO lag key — an absent field was "+
			"read as a measured 0s (msg=%q)", c.Message)
	}
}

// ── the six checks with no probe on this path ────────────────────────

func TestDRPreflight_UnprobedChecksDoNotReportPass(t *testing.T) {
	cr := withContinuumStatus(newContinuumUnstructured(
		"dr-shared-pg", "shared-data", "shared-pg", "me-east-215-a", []string{"me-east-215-b"}),
		"Healthy", int64(0))
	h, depID := fakePreflightHandler(t, "dep-preflight-unprobed", cr)
	checks := runPreflight(t, h, depID)

	for _, id := range []string{"preflight-05", "preflight-06", "preflight-07",
		"preflight-08", "preflight-09", "preflight-10"} {
		c := checkByID(t, checks, id)
		if c.Status == preflightPass {
			t.Errorf("%s (%s): got Pass, but this endpoint runs no probe for it — "+
				"a verdict from absent evidence", c.ID, c.Name)
		}
		if c.Message == "" {
			t.Errorf("%s (%s): an unmeasured check must name what was not measured", c.ID, c.Name)
		}
	}
}

// An unresolvable deployment must not yield a clean matrix — nothing was read.
func TestDRPreflight_UnknownDeploymentIsAllUnmeasured(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereigns/nope/dr/runbook/preflight", nil)

	checks := h.runDRPreflight(req, "nope")

	for _, c := range checks {
		if c.Status == preflightPass {
			t.Errorf("%s (%s): got Pass with no deployment and no cluster client at all", c.ID, c.Name)
		}
	}
	if overall, _ := classifyPreflight(checks); overall == "Ready" {
		t.Error("classifyPreflight: an all-unmeasured matrix rolled up to Ready — a clean bill of health from zero evidence")
	}
}

// ── the rollup: the playback abort branch must be reachable ──────────

// HandleDRRunbookPlayback aborts only on overall == "NotReady". Before this
// change no check could reach Fail, so that branch was dead code: the safety
// gate in front of a mutating DR operation could never fire.
func TestDRPreflight_CriticalFailReachesNotReadyAndBlocks(t *testing.T) {
	degraded := withContinuumStatus(newContinuumUnstructured(
		"dr-shared-pg", "shared-data", "shared-pg", "me-east-215-a", []string{"me-east-215-b"}),
		"Degraded", int64(0))
	h, depID := fakePreflightHandler(t, "dep-preflight-notready", degraded)

	overall, blocking := classifyPreflight(runPreflight(t, h, depID))

	if overall != "NotReady" {
		t.Errorf("overall: got %q want NotReady — a Degraded Continuum must abort the runbook playback", overall)
	}
	if len(blocking) == 0 {
		t.Error("blockingChecks: empty on a NotReady matrix — the operator is told to abort without being told why")
	}
}

// classifyPreflight must never report Ready while blockingChecks is non-empty.
func TestClassifyPreflight_NonCriticalFailIsNotReadyVerdict(t *testing.T) {
	checks := []drRunbookPreflightCheck{
		{ID: "preflight-01", Name: "continuum-cr-ready", Status: preflightPass, Severity: "info"},
		{ID: "preflight-02", Name: "cnpgpair-streaming", Status: preflightFail, Severity: "warning"},
	}

	overall, blocking := classifyPreflight(checks)

	if len(blocking) != 1 {
		t.Fatalf("blockingChecks: got %v want 1 entry", blocking)
	}
	if overall == "Ready" {
		t.Errorf("overall: got Ready with %d blocking check(s) — self-contradictory", len(blocking))
	}
}

// ── vacuity control ──────────────────────────────────────────────────

// The probes must still be able to say Pass. A genuinely healthy Sovereign —
// Continuum CRs Healthy and caught up, both cnpg pair halves visible and
// Ready, three PDM witnesses — must report the four probed checks Pass and
// must NOT roll up to NotReady. Without this, every assertion above could be
// satisfied by hardwiring the matrix to Fail.
func TestDRPreflight_HealthyMatrixStillPasses(t *testing.T) {
	cr := withContinuumStatus(newContinuumUnstructured(
		"dr-shared-pg", "shared-data", "shared-pg", "me-east-215-a", []string{"me-east-215-b"}),
		"Healthy", int64(0))
	primary, replica := newCNPGPairFixture("shared-pg", "shared-data", "me-east-215-a", "me-east-215-b")
	h, depID := fakePreflightHandler(t, "dep-preflight-healthy",
		cr, primary, replica,
		newPDMFixture("pdm-a"), newPDMFixture("pdm-b"), newPDMFixture("pdm-c"))

	checks := runPreflight(t, h, depID)

	for _, id := range []string{"preflight-01", "preflight-02", "preflight-03", "preflight-04"} {
		if c := checkByID(t, checks, id); c.Status != preflightPass {
			t.Errorf("%s (%s): got %q want Pass on a genuinely healthy Sovereign (msg=%q)",
				c.ID, c.Name, c.Status, c.Message)
		}
	}
	if overall, blocking := classifyPreflight(checks); overall == "NotReady" {
		t.Errorf("overall: got NotReady on a healthy Sovereign (blocking=%v)", blocking)
	}
}
