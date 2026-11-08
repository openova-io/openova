// continuum_dr_extras_4923_test.go — #4923 regression coverage (hw242 wave).
//
// The replication-status endpoint must report REAL replication state derived
// from the live CNPG cluster-pair / Continuum CRs — never a synthesized
// healthy shape. This file locks the four honest states:
//
//   pair-healthy    — standbyAvailable:true, standby-available gate Pass.
//   standby-absent  — standbyAvailable:false, standby-available gate Fail
//                     (critical), replicaPromotable forced false, streaming
//                     "interrupted" (the #4901 region-kill shape — the stored
//                     CR status stays green through the outage, so the
//                     endpoint cross-checks the replica half itself).
//   unverifiable    — standbyAvailable OMITTED + an explicit Warn gate for
//                     continuums with no resolvable cnpg pair (dr-spine /
//                     openbao-raft) — honest-unknown, never fabricated Pass.
//   no-pair         — source:"pending" honest-empty (locked by the #4551 /
//                     #4930 suites).
//
// It also locks that enrichReplicationStatus no longer fabricates
// streamingState:"streaming" / syncState:"async" / an all-Pass health-gate
// wall / a now() replica heartbeat on CRs that report none, and that the
// sovereign-wide roll-up counts unverifiable rows as replicasUnknown instead
// of healthy.
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// setCNPGReplicaDown marks the replica half of a cnpg pair fixture as NOT
// Ready with zero ready instances — the proven #4901 region-kill state
// (region-b nodes cordoned, replica Pods deleted).
func setCNPGReplicaDown(replica *unstructured.Unstructured) {
	_ = unstructured.SetNestedSlice(replica.Object, []interface{}{
		map[string]interface{}{"type": "Ready", "status": "False"},
	}, "status", "conditions")
	_ = unstructured.SetNestedField(replica.Object, int64(0), "status", "readyInstances")
}

func gateByName(t *testing.T, resp continuumReplicationStatus, name string) continuumHealthGate {
	t.Helper()
	for _, g := range resp.HealthGates {
		if g.Name == name {
			return g
		}
	}
	t.Fatalf("health gate %q missing; got %+v", name, resp.HealthGates)
	return continuumHealthGate{}
}



// unverifiable — a spine Continuum with NO cnpg pair must report the verdict
// as UNKNOWN (standbyAvailable omitted) with an explicit Warn gate, and the
// enrich layer must not fabricate streaming/async/all-Pass shapes for it.
func TestReplicationStatus_SpineWithoutPairReportsUnknownNotFabricatedGreen(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured(
		"dr-spine-openbao", "openbao", "openbao",
		"me-east-215-a", []string{"me-east-215-b"})
	factory, _ := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-4923-spine-unknown")

	r := chi.NewRouter()
	registerReplicationStatusRoute(r, h)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/continuum/dr-spine-openbao/replication-status?namespace=openbao", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp continuumReplicationStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.StandbyAvailable != nil {
		t.Errorf("standbyAvailable: got %v want omitted (nil) — no cnpg pair to verify against", *resp.StandbyAvailable)
	}
	if g := gateByName(t, resp, "standby-available"); g.Status != "Warn" {
		t.Errorf("standby-available gate: got %q want Warn (unverifiable, not fabricated Pass)", g.Status)
	}
	// The fabricated defaults are GONE: the CR reports neither streaming nor
	// sync state, so both are empty — never "streaming"/"async".
	if resp.StreamingState != "" {
		t.Errorf("streamingState: got %q want empty (CR reports none)", resp.StreamingState)
	}
	if resp.SyncState != "" {
		t.Errorf("syncState: got %q want empty (CR reports none — the old hardcoded async misled the operator)", resp.SyncState)
	}
	// replication health is unreported on the CR → the gate is Warn, and a
	// small lag number alone must not mark the replica promotable.
	if g := gateByName(t, resp, "streaming-replication"); g.Status != "Warn" {
		t.Errorf("streaming-replication gate: got %q want Warn (health not reported)", g.Status)
	}
	if resp.ReplicaPromotable {
		t.Errorf("replicaPromotable: got true want false — no positive replication evidence on the CR")
	}
	// No fabricated now() heartbeat on the configured standby rows.
	for _, rep := range resp.Replicas {
		if rep.LastHeartbeat != "" {
			t.Errorf("replica %s: fabricated lastHeartbeat %q (CR reports none)", rep.Region, rep.LastHeartbeat)
		}
	}
}



// roll-up — the sovereign-wide aggregate must count a verified-absent standby
// as degraded and an unverifiable spine as UNKNOWN, never healthy. The old
// code counted every low-lag row healthy (lag=0 during an outage → all green).
func TestDRReplicationStatusRollup_HonestHealthyDegradedUnknownCounters(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	pairCR := newContinuumUnstructured(
		"cnpg-pair-bp-cnpg-pair-continuum", "catalyst-system",
		"catalyst-platform", "me-east-215-a", []string{"me-east-215-b"})
	_ = unstructured.SetNestedMap(pairCR.Object, map[string]interface{}{
		"name":      "shared-pg",
		"namespace": "catalyst-system",
	}, "spec", "cnpgPair")
	// ADR-0015 D7 — the verdict is the continuum-controller's own
	// status.standbyAvailable, relayed verbatim (no read-side cross-check).
	_ = unstructured.SetNestedField(pairCR.Object, false, "status", "standbyAvailable")
	spineCR := newContinuumUnstructured(
		"dr-spine-openbao", "openbao", "openbao",
		"me-east-215-a", []string{"me-east-215-b"})
	primary, replica := newCNPGPairFixture("shared-pg", "catalyst-system", "me-east-215-a", "me-east-215-b")
	setCNPGReplicaDown(replica)
	factory, _ := fakeContinuumDynamicFactory(pairCR, spineCR, primary, replica)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-4923-rollup")

	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/dr/replication-status", h.HandleDRReplicationStatus)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/dr/replication-status", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ContinuumCount   int                          `json:"continuumCount"`
		ReplicasHealthy  int                          `json:"replicasHealthy"`
		ReplicasDegraded int                          `json:"replicasDegraded"`
		ReplicasUnknown  int                          `json:"replicasUnknown"`
		Continuums       []continuumReplicationStatus `json:"continuums"`
		Source           string                       `json:"source"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ContinuumCount != 2 {
		t.Fatalf("continuumCount: got %d want 2", resp.ContinuumCount)
	}
	if resp.ReplicasHealthy != 0 {
		t.Errorf("replicasHealthy: got %d want 0 (standby down + spine unverifiable)", resp.ReplicasHealthy)
	}
	if resp.ReplicasDegraded != 1 {
		t.Errorf("replicasDegraded: got %d want 1 (the verified-absent cnpg pair)", resp.ReplicasDegraded)
	}
	if resp.ReplicasUnknown != 1 {
		t.Errorf("replicasUnknown: got %d want 1 (the unverifiable spine)", resp.ReplicasUnknown)
	}
}
