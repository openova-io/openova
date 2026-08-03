// continuum_dr_extras_5601_test.go — #5601 regression coverage.
//
// hw292 (dep 1c56185…, post-cutover) rendered the DR panel's Switchover
// control DISABLED with "the replica is not promotable — no caught-up standby
// to promote" while the SAME panel showed a healthy hot standby at 0.0s lag,
// and the Continuum CR itself said standbyAvailable=true / phase=Healthy /
// replicationLagSeconds=0 (StandbyAvailable condition True/StandbyReachable).
//
// Root cause: the replication-status readers probed status keys NO live
// producer writes — status.replicaPromotable / status.replicationHealthy /
// status.walLagSeconds (QA-fixture-only spellings) — so found=false and
// ReplicaPromotable silently kept its zero value. The Continuum controller's
// status writer (core/controllers/continuum patchStatus) publishes
// standbyAvailable / phase / replicationLagSeconds instead.
//
// This file locks:
//   - a healthy caught-up CR (the exact hw292 status shape) yields
//     ReplicaPromotable=true with the lag field populated;
//   - the controller's replicationLagSeconds spelling reaches walLagSeconds;
//   - the derivation is NOT a standbyAvailable pass-through: Degraded phase,
//     standbyAvailable=false, and over-threshold lag each disarm it;
//   - the linked-CNPGPair reader derives promotability from the keys the
//     CNPGPair CRD contract carries (streaming + lag) when replicaPromotable
//     is absent, and still honors an explicit replicaPromotable=false.
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// setContinuumControllerStatus stamps the EXACT status keys the Continuum
// controller's patchStatus writes (verified against
// core/controllers/continuum/internal/controller/continuum_controller.go and
// the live hw292 CR capture in #5601) — never the QA-fixture-only spellings.
func setContinuumControllerStatus(cr *unstructured.Unstructured, phase string, standbyAvailable bool, lagSeconds int64) {
	_ = unstructured.SetNestedField(cr.Object, phase, "status", "phase")
	_ = unstructured.SetNestedField(cr.Object, standbyAvailable, "status", "standbyAvailable")
	_ = unstructured.SetNestedField(cr.Object, !standbyAvailable, "status", "hotStandbyAbsent")
	_ = unstructured.SetNestedField(cr.Object, lagSeconds, "status", "replicationLagSeconds")
	_ = unstructured.SetNestedField(cr.Object, false, "status", "switchoverInProgress")
}

func fetchReplicationStatus(t *testing.T, h *Handler, depID, name, ns string) continuumReplicationStatus {
	t.Helper()
	r := chi.NewRouter()
	registerReplicationStatusRoute(r, h)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+depID+"/continuum/"+name+"/replication-status?namespace="+ns, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp continuumReplicationStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

// The hw292 shape: Continuum CR healthy + caught up (standbyAvailable=true,
// phase=Healthy, replicationLagSeconds=0) with the live cnpg pair's replica
// half Ready. The switchover gate MUST arm: replicaPromotable=true.
func TestReplicationStatus_5601_HealthyCaughtUpCRIsPromotable(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured(
		"dr-shared-pg", "shared-data", "shared-pg",
		"me-east-215-a", []string{"me-east-215-b"})
	setContinuumControllerStatus(cr, "Healthy", true, 0)
	primary, replica := newCNPGPairFixture("shared-pg", "shared-data", "me-east-215-a", "me-east-215-b")
	factory, _ := fakeContinuumDynamicFactory(cr, primary, replica)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-5601-healthy-promotable")

	resp := fetchReplicationStatus(t, h, dep.ID, "dr-shared-pg", "shared-data")

	if !resp.ReplicaPromotable {
		t.Errorf("replicaPromotable: got false want true — the CR says standbyAvailable=true/phase=Healthy/lag=0 and the live replica is Ready (the #5601 hw292 contradiction)")
	}
	if resp.StandbyAvailable == nil || !*resp.StandbyAvailable {
		t.Errorf("standbyAvailable: got %v want true", resp.StandbyAvailable)
	}
	if g := gateByName(t, resp, "standby-available"); g.Status != "Pass" {
		t.Errorf("standby-available gate: got %q want Pass (msg=%q)", g.Status, g.Message)
	}
	if resp.WALLagSeconds != 0 {
		t.Errorf("walLagSeconds: got %v want 0 (the CR-reported reading)", resp.WALLagSeconds)
	}
	if resp.Source != "live" {
		t.Errorf("source: got %q want live", resp.Source)
	}
}

// The controller's lag spelling — status.replicationLagSeconds — must reach
// the response's walLagSeconds AND a low reading must keep the pair
// promotable.
func TestReplicationStatus_5601_ControllerLagSpellingPopulatesWALLag(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured(
		"dr-shared-pg", "shared-data", "shared-pg",
		"me-east-215-a", []string{"me-east-215-b"})
	setContinuumControllerStatus(cr, "Healthy", true, 7)
	primary, replica := newCNPGPairFixture("shared-pg", "shared-data", "me-east-215-a", "me-east-215-b")
	factory, _ := fakeContinuumDynamicFactory(cr, primary, replica)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-5601-lag-spelling")

	resp := fetchReplicationStatus(t, h, dep.ID, "dr-shared-pg", "shared-data")

	if resp.WALLagSeconds != 7 {
		t.Errorf("walLagSeconds: got %v want 7 (status.replicationLagSeconds is the controller's spelling)", resp.WALLagSeconds)
	}
	if !resp.ReplicaPromotable {
		t.Errorf("replicaPromotable: got false want true (7s lag is under the 30s threshold)")
	}
}

// Controls — the fix must NOT collapse into a standbyAvailable pass-through.
// Each variant shares the suspect property (a healthy-looking sibling field)
// and must still disarm.
func TestReplicationStatus_5601_DerivationControlsStayDisarmed(t *testing.T) {
	cases := []struct {
		name             string
		phase            string
		standbyAvailable bool
		lagSeconds       int64
	}{
		{"degraded-phase", "Degraded", true, 0},
		{"standby-unavailable", "Healthy", false, 0},
		{"lag-over-threshold", "Healthy", true, 45},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewWithPDM(silentLogger(), &fakePDM{})
			cr := newContinuumUnstructured(
				"dr-shared-pg", "shared-data", "shared-pg",
				"me-east-215-a", []string{"me-east-215-b"})
			setContinuumControllerStatus(cr, tc.phase, tc.standbyAvailable, tc.lagSeconds)
			// No cnpg pair seeded: the live augmentation stays undetermined and
			// must not upgrade the CR-derived verdict.
			factory, _ := fakeContinuumDynamicFactory(cr)
			h.dynamicFactory = factory
			dep := installUserAccessDeployment(t, h, "dep-5601-ctl-"+tc.name)

			resp := fetchReplicationStatus(t, h, dep.ID, "dr-shared-pg", "shared-data")

			if resp.ReplicaPromotable {
				t.Errorf("replicaPromotable: got true want false (%s)", tc.name)
			}
		})
	}
}

// newCNPGPairCRUnstructured composes a dr.openova.io CNPGPair CR carrying the
// given status keys.
func newCNPGPairCRUnstructured(name, namespace string, status map[string]interface{}) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("dr.openova.io/v1")
	u.SetKind("CNPGPair")
	u.SetName(name)
	u.SetNamespace(namespace)
	_ = unstructured.SetNestedMap(u.Object, map[string]interface{}{
		"primaryCluster": name + "-primary",
		"replicaCluster": name + "-replica",
		"primaryRegion":  "me-east-215-a",
		"replicaRegion":  "me-east-215-b",
	}, "spec")
	_ = unstructured.SetNestedMap(u.Object, status, "status")
	return u
}

// Linked-CNPGPair reader — when the pair CR reports the keys its CRD contract
// actually carries (streaming bool + a lag axis) but NO replicaPromotable,
// the reader must derive promotability instead of silently keeping false.
func TestReplicationStatus_5601_LinkedPairDerivesPromotableFromStreaming(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured(
		"dr-shared-pg", "shared-data", "shared-pg",
		"me-east-215-a", []string{"me-east-215-b"})
	// The Continuum CR itself reports no health keys — only the linked pair
	// carries the reading, so a promotable=true can only come from the pair.
	_ = unstructured.SetNestedMap(cr.Object, map[string]interface{}{
		"name":      "shared-pg-pair",
		"namespace": "shared-data",
	}, "spec", "cnpgPair")
	pair := newCNPGPairCRUnstructured("shared-pg-pair", "shared-data", map[string]interface{}{
		"phase":                 "Streaming",
		"streaming":             true,
		"replicationLagSeconds": int64(3),
	})
	factory, _ := fakeContinuumDynamicFactory(cr, pair)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-5601-pair-streaming")

	resp := fetchReplicationStatus(t, h, dep.ID, "dr-shared-pg", "shared-data")

	if !resp.ReplicaPromotable {
		t.Errorf("replicaPromotable: got false want true (pair streaming=true, lag 3s under threshold)")
	}
	if resp.WALLagSeconds != 3 {
		t.Errorf("walLagSeconds: got %v want 3 (pair status.replicationLagSeconds)", resp.WALLagSeconds)
	}
}

// An EXPLICIT replicaPromotable=false on the pair CR must still win over the
// streaming-derived verdict (both directions honest — never fail-open).
func TestReplicationStatus_5601_LinkedPairExplicitFalseStillWins(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured(
		"dr-shared-pg", "shared-data", "shared-pg",
		"me-east-215-a", []string{"me-east-215-b"})
	_ = unstructured.SetNestedMap(cr.Object, map[string]interface{}{
		"name":      "shared-pg-pair",
		"namespace": "shared-data",
	}, "spec", "cnpgPair")
	pair := newCNPGPairCRUnstructured("shared-pg-pair", "shared-data", map[string]interface{}{
		"phase":             "Streaming",
		"streaming":         true,
		"walLagSeconds":     int64(2),
		"replicaPromotable": false,
	})
	factory, _ := fakeContinuumDynamicFactory(cr, pair)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-5601-pair-explicit-false")

	resp := fetchReplicationStatus(t, h, dep.ID, "dr-shared-pg", "shared-data")

	if resp.ReplicaPromotable {
		t.Errorf("replicaPromotable: got true want false — an explicit producer-written false must never be overridden by the derived reading")
	}
}

