// continuum_standby_absent_4901_test.go — #4901 regression coverage.
//
// The G12 region-kill drill on hw232 proved the cnpg-pair Continuum CR
// (`cnpg-pair-bp-cnpg-pair-continuum`) stayed phase=Healthy,
// replicationLagSeconds=0 for the whole outage even though its REQUIRED
// synchronous hot-standby (region-b replica) was fully unreachable and
// writes were stalling on SyncRep. The continuum-controller owns the CR's
// stored status and tracks the witness lease / primary correctly, but it
// derives phase purely from lease-held-ness — so a lost required-sync
// standby is invisible in the CR's phase/lag.
//
// HandleContinuumGet now cross-checks the LIVE cnpg-pair standby and
// reflects a standby-absent posture into the OBSERVED status the DR panel
// renders (Degraded phase + standbyAvailable=false + a StandbyAvailable
// condition), WITHOUT writing the CR and WITHOUT breaking the correct
// lease/primary tracking or false-alarming on mere lag. This file locks
// that behaviour.
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// newCnpgPairContinuumCR composes a Continuum CR shaped like the real
// bp-cnpg-pair chart output (platform/cnpg-pair/chart/templates/continuum.yaml):
// it carries spec.cnpgPair.{name,namespace} referencing the backing pair, and
// a controller-owned status that (correctly) tracks the lease/primary but is
// blind to the standby. That blindness is exactly what #4901 augments.
func newCnpgPairContinuumCR(name, namespace, pairName, primaryRegion, replicaRegion string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("dr.openova.io/v1")
	u.SetKind("Continuum")
	u.SetName(name)
	u.SetNamespace(namespace)
	_ = unstructured.SetNestedMap(u.Object, map[string]interface{}{
		"applicationRef":    "catalyst-platform",
		"primaryRegion":     primaryRegion,
		"hotStandbyRegions": []interface{}{replicaRegion},
		"cnpgPair": map[string]interface{}{
			"name":      pairName,
			"namespace": namespace,
		},
		"leaseClient": map[string]interface{}{"kind": "in-memory"},
	}, "spec")
	// Controller-owned status: lease held on region-a, phase Healthy, zero
	// lag — the exact green-but-blind steady state the drill observed.
	_ = unstructured.SetNestedMap(u.Object, map[string]interface{}{
		"phase":                 "Healthy",
		"primaryRegion":         primaryRegion,
		"leaseHolder":           primaryRegion,
		"currentPrimary":        pairName + "-primary-1",
		"replicationLagSeconds": int64(0),
		"switchoverInProgress":  false,
	}, "status")
	return u
}

// newCnpgClusterHalf builds one cnpg Cluster half of a pair with the
// canonical pair/role/region labels and a configurable readiness (Ready
// condition + readyInstances). A down replica models the region-kill state:
// Ready=False, readyInstances=0.
func newCnpgClusterHalf(pairName, role, region string, replicaEnabled, ready bool, readyInstances int) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("postgresql.cnpg.io/v1")
	u.SetKind("Cluster")
	u.SetName(pairName + "-" + role)
	u.SetNamespace("catalyst-system")
	u.SetLabels(map[string]string{
		cnpgPairLabel:   pairName,
		cnpgRoleLabel:   role,
		cnpgRegionLabel: region,
	})
	_ = unstructured.SetNestedMap(u.Object, map[string]interface{}{
		"instances": int64(2),
		"replica":   map[string]interface{}{"enabled": replicaEnabled},
	}, "spec")
	readyStr := "False"
	if ready {
		readyStr = "True"
	}
	_ = unstructured.SetNestedMap(u.Object, map[string]interface{}{
		"currentPrimary": pairName + "-" + role + "-1",
		"phase":          "Cluster in healthy state",
		"readyInstances": int64(readyInstances),
		"conditions": []interface{}{
			map[string]interface{}{"type": "Ready", "status": readyStr},
		},
	}, "status")
	return u
}

// getContinuumStatus drives GET /continuums/{name} and returns the decoded
// status map.
func getContinuumStatus(t *testing.T, h *Handler, depID, name string) (int, map[string]interface{}) {
	t.Helper()
	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+depID+"/continuums/"+name+"?namespace=catalyst-system", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp continuumGetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	return rec.Code, resp.Status
}

func findCondition(status map[string]interface{}, condType string) (map[string]interface{}, bool) {
	conds, _, _ := unstructured.NestedSlice(status, "conditions")
	for _, c := range conds {
		if m, ok := c.(map[string]interface{}); ok {
			if t, _, _ := unstructured.NestedString(m, "type"); t == condType {
				return m, true
			}
		}
	}
	return nil, false
}

// #4901 — the required sync standby (region-b replica) is UNREACHABLE:
// its Cluster is present but Ready=False / readyInstances=0. The Continuum
// GET must now flip phase Healthy→Degraded, set standbyAvailable=false, and
// carry a StandbyAvailable=False / StandbyUnreachable condition — while
// preserving the correct lease/primary tracking.
func TestHandleContinuumGet_StandbyAbsent_SurfacesDegraded(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newCnpgPairContinuumCR(
		"cnpg-pair-bp-cnpg-pair-continuum", "catalyst-system",
		"bp-cnpg-pair", "hz-fsn-rtz-prod", "hz-hel-rtz-prod")
	primary := newCnpgClusterHalf("bp-cnpg-pair", cnpgRolePrimary, "hz-fsn-rtz-prod", false, true, 2)
	// Region-b killed: replica half present but NOT Ready, zero ready instances.
	replica := newCnpgClusterHalf("bp-cnpg-pair", cnpgRoleReplica, "hz-hel-rtz-prod", true, false, 0)
	factory, _ := fakeContinuumDynamicFactory(cr, primary, replica)
	h.dynamicFactory = factory
	fixed := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	h.SetContinuumClock(func() time.Time { return fixed })
	dep := installUserAccessDeployment(t, h, "dep-4901-absent")

	_, status := getContinuumStatus(t, h, dep.ID, "cnpg-pair-bp-cnpg-pair-continuum")

	if phase, _, _ := unstructured.NestedString(status, "phase"); phase != "Degraded" {
		t.Errorf("phase: got %q want Degraded (standby unreachable)", phase)
	}
	if avail, found, _ := unstructured.NestedBool(status, "standbyAvailable"); !found || avail {
		t.Errorf("standbyAvailable: got %v (found=%v) want false", avail, found)
	}
	if absent, _, _ := unstructured.NestedBool(status, "hotStandbyAbsent"); !absent {
		t.Errorf("hotStandbyAbsent: got false want true")
	}
	cond, ok := findCondition(status, "StandbyAvailable")
	if !ok {
		t.Fatalf("missing StandbyAvailable condition; status=%+v", status)
	}
	if s, _, _ := unstructured.NestedString(cond, "status"); s != "False" {
		t.Errorf("condition status: got %q want False", s)
	}
	if reason, _, _ := unstructured.NestedString(cond, "reason"); reason != "StandbyUnreachable" {
		t.Errorf("condition reason: got %q want StandbyUnreachable", reason)
	}
	// Lease/primary tracking must be preserved verbatim — the fix must not
	// break the (correct) autoFailover=false, lease-pinned-region-a behaviour.
	if lh, _, _ := unstructured.NestedString(status, "leaseHolder"); lh != "hz-fsn-rtz-prod" {
		t.Errorf("leaseHolder clobbered: got %q want hz-fsn-rtz-prod", lh)
	}
	if cp, _, _ := unstructured.NestedString(status, "currentPrimary"); cp != "bp-cnpg-pair-primary-1" {
		t.Errorf("currentPrimary clobbered: got %q", cp)
	}
}

// A healthy, following standby (Ready=True, ready instances present) keeps
// the Continuum green: phase Healthy, standbyAvailable=true. No false alarm.
func TestHandleContinuumGet_StandbyPresent_StaysHealthy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newCnpgPairContinuumCR(
		"cnpg-pair-bp-cnpg-pair-continuum", "catalyst-system",
		"bp-cnpg-pair", "hz-fsn-rtz-prod", "hz-hel-rtz-prod")
	primary := newCnpgClusterHalf("bp-cnpg-pair", cnpgRolePrimary, "hz-fsn-rtz-prod", false, true, 2)
	replica := newCnpgClusterHalf("bp-cnpg-pair", cnpgRoleReplica, "hz-hel-rtz-prod", true, true, 2)
	factory, _ := fakeContinuumDynamicFactory(cr, primary, replica)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-4901-present")

	_, status := getContinuumStatus(t, h, dep.ID, "cnpg-pair-bp-cnpg-pair-continuum")

	if phase, _, _ := unstructured.NestedString(status, "phase"); phase != "Healthy" {
		t.Errorf("phase: got %q want Healthy (standby reachable)", phase)
	}
	if avail, found, _ := unstructured.NestedBool(status, "standbyAvailable"); !found || !avail {
		t.Errorf("standbyAvailable: got %v (found=%v) want true", avail, found)
	}
	if absent, _, _ := unstructured.NestedBool(status, "hotStandbyAbsent"); absent {
		t.Errorf("hotStandbyAbsent: got true want false")
	}
}

// A Ready-but-LAGGING standby (Ready=True, ready instances present, high lag)
// must NOT be flagged standby-absent — lag rides its own field. This guards
// the explicit no-false-alarm rule.
func TestHandleContinuumGet_StandbyLagging_NotFlagged(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newCnpgPairContinuumCR(
		"cnpg-pair-bp-cnpg-pair-continuum", "catalyst-system",
		"bp-cnpg-pair", "hz-fsn-rtz-prod", "hz-hel-rtz-prod")
	primary := newCnpgClusterHalf("bp-cnpg-pair", cnpgRolePrimary, "hz-fsn-rtz-prod", false, true, 2)
	replica := newCnpgClusterHalf("bp-cnpg-pair", cnpgRoleReplica, "hz-hel-rtz-prod", true, true, 2)
	// Model lag on the replica half — still Ready, so still AVAILABLE.
	_ = unstructured.SetNestedField(replica.Object, int64(45), "status", "lag")
	factory, _ := fakeContinuumDynamicFactory(cr, primary, replica)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-4901-lag")

	_, status := getContinuumStatus(t, h, dep.ID, "cnpg-pair-bp-cnpg-pair-continuum")

	if phase, _, _ := unstructured.NestedString(status, "phase"); phase != "Healthy" {
		t.Errorf("phase: got %q want Healthy (lagging standby is NOT absent)", phase)
	}
	if avail, found, _ := unstructured.NestedBool(status, "standbyAvailable"); !found || !avail {
		t.Errorf("standbyAvailable: got %v (found=%v) want true (lag is not absence)", avail, found)
	}
}

// A dr-spine / raft Continuum has no spec.cnpgPair (no synchronous cnpg
// standby). Its status must pass through UNTOUCHED — the fix is scoped to
// the cnpg-pair path only.
func TestHandleContinuumGet_NoCNPGPairRef_Untouched(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	// newContinuumUnstructured sets NO spec.cnpgPair (dr-spine shape).
	cr := newContinuumUnstructured(
		"bp-openbao-continuum", "catalyst-system",
		"spine-openbao", "hz-fsn-rtz-prod", []string{"hz-hel-rtz-prod"})
	factory, _ := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-4901-spine")

	_, status := getContinuumStatus(t, h, dep.ID, "bp-openbao-continuum")

	if phase, _, _ := unstructured.NestedString(status, "phase"); phase != "Healthy" {
		t.Errorf("phase: got %q want Healthy (dr-spine untouched)", phase)
	}
	if _, found, _ := unstructured.NestedBool(status, "standbyAvailable"); found {
		t.Errorf("standbyAvailable should be ABSENT for a non-cnpg-pair continuum")
	}
	if _, ok := findCondition(status, "StandbyAvailable"); ok {
		t.Errorf("StandbyAvailable condition should NOT be added for a dr-spine continuum")
	}
}
