// continuum_dr_extras_row62_71_test.go — UAT rows 62 + 71 regression locks.
//
// Both rows fail on the SAME rendered symptom (the DR panel cannot show a live
// replication-lag number, and shows no Continuum phase / lease holder) and both
// bottom out in this handler, not in the console:
//
//   1. THE STREAMING GATE COULD NEVER PASS. replicationHealthGate keyed on
//      `status.replicationHealthy`, a spelling with ZERO producers (grep over
//      core/controllers/continuum + core/pkg/apis returns nothing; all 8 live
//      Continuums on hw292 omit it). So every real CR got Warn "unverified" —
//      a gate that structurally cannot pass — and since #5508 the console reads
//      that Warn as "the lag is not a measurement" and prints "—".
//
//   2. THE STANDBY LEG WAS PERMANENTLY "UNVERIFIABLE" ON A 2-REGION SOVEREIGN.
//      This handler's dynamic client is scoped to the REGION-A cluster; the
//      replica half of a pair is a cnpg Cluster in REGION B. Measured on hw292
//      2026-08-10: `kubectl -n shared-data get clusters.postgresql.cnpg.io`
//      returns ONLY `openova.io/cnpg-role=primary` halves, so
//      cnpgPairStandbyForContinuum finds no replica and findCNPGPairForApp
//      requires both halves — leaving the standby-available gate on Warn
//      forever, and `standbyAvailable` omitted, which makes the console's
//      #4923/#4901 red "Standby absent" banner UNREACHABLE.
//
//   3. PHASE + LEASE HOLDER WERE READ AND THEN DROPPED. enrichReplicationStatus
//      already reads status.leaseHolder (to correct CurrentPrimary) but never
//      put it, or status.phase, on the wire — so row 62's "live Continuum
//      status (Ready / lease holder / standby)" clause had no data to render.
//
// Every assertion below is on the VALUE, and each fix carries a CONTROL that
// must move the other way, so none of these gates can pass vacuously.
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// fetchReplicationStatusRaw decodes the response into a generic map so the
// assertions pin the WIRE KEY NAMES the console consumes, not just Go struct
// fields (a wrong json tag would sail through a typed decode).
func fetchReplicationStatusRaw(t *testing.T, h *Handler, depID, name, ns string) map[string]interface{} {
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
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// gateByName lives in continuum_dr_extras_4923_test.go — reused here.

// setCnpgPairRef stamps spec.cnpgPair so cnpgPairStandbyForContinuum is the
// path exercised (the live dr-shared-pg shape).
func setCnpgPairRef(cr *unstructured.Unstructured, name, ns string) {
	_ = unstructured.SetNestedMap(cr.Object, map[string]interface{}{
		"name":      name,
		"namespace": ns,
	}, "spec", "cnpgPair")
}

// ── Row 71: the streaming-replication gate must be able to PASS ──────────

// The hw292 dr-shared-pg shape with BOTH cnpg halves visible: phase=Healthy,
// standbyAvailable=true, lag 0. The streaming-replication gate must read Pass.
// Before the fix it read Warn "replication health not reported ... unverified"
// because the only key consulted (status.replicationHealthy) has no producer.
func TestReplicationStatus_Row71_StreamingGatePassesOnControllerStandbyProbe(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured(
		"dr-shared-pg", "shared-data", "shared-pg",
		"me-east-215-a", []string{"me-east-215-b"})
	setContinuumControllerStatus(cr, "Healthy", true, 0)
	primary, replica := newCNPGPairFixture("shared-pg", "shared-data", "me-east-215-a", "me-east-215-b")
	factory, _ := fakeContinuumDynamicFactory(cr, primary, replica)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-row71-streaming-pass")

	resp := fetchReplicationStatus(t, h, dep.ID, "dr-shared-pg", "shared-data")
	g := gateByName(t, resp, "streaming-replication")
	if g.Status != "Pass" {
		t.Fatalf("streaming-replication gate: got %q (%q), want Pass — the gate keyed on a status field with no producer, so it could never pass on a live CR", g.Status, g.Message)
	}
}

// CONTROL for the above: the gate must still FAIL on a negative standby probe
// and still WARN when the CR reports neither key. Without these the Pass above
// would prove only that the gate was hardwired open.
func TestReplicationStatus_Row71_StreamingGateControls(t *testing.T) {
	t.Run("standby probe false -> Fail", func(t *testing.T) {
		h := NewWithPDM(silentLogger(), &fakePDM{})
		cr := newContinuumUnstructured(
			"dr-shared-pg", "shared-data", "shared-pg",
			"me-east-215-a", []string{"me-east-215-b"})
		setContinuumControllerStatus(cr, "Healthy", false, 0)
		primary, replica := newCNPGPairFixture("shared-pg", "shared-data", "me-east-215-a", "me-east-215-b")
		factory, _ := fakeContinuumDynamicFactory(cr, primary, replica)
		h.dynamicFactory = factory
		dep := installUserAccessDeployment(t, h, "dep-row71-streaming-fail")

		resp := fetchReplicationStatus(t, h, dep.ID, "dr-shared-pg", "shared-data")
		if g := gateByName(t, resp, "streaming-replication"); g.Status != "Fail" {
			t.Fatalf("streaming-replication gate on a false standby probe: got %q want Fail", g.Status)
		}
	})

	t.Run("no evidence at all -> Warn, never Pass", func(t *testing.T) {
		h := NewWithPDM(silentLogger(), &fakePDM{})
		cr := newContinuumUnstructured(
			"dr-spine-openbao", "catalyst", "spine-openbao",
			"me-east-215-a", nil)
		// Deliberately NO standbyAvailable / replicationHealthy — the raft
		// spine shape. phase stays the fixture's Healthy.
		factory, _ := fakeContinuumDynamicFactory(cr)
		h.dynamicFactory = factory
		dep := installUserAccessDeployment(t, h, "dep-row71-streaming-warn")

		resp := fetchReplicationStatus(t, h, dep.ID, "dr-spine-openbao", "catalyst")
		if g := gateByName(t, resp, "streaming-replication"); g.Status != "Warn" {
			t.Fatalf("streaming-replication gate with no evidence: got %q want Warn (a Pass here would be a fabricated health claim)", g.Status)
		}
	})
}

// ── Row 62: the per-region split must not report "unverifiable" ──────────

// The live hw292 shape: dr-shared-pg carries spec.cnpgPair and the controller's
// standby probe says the leg is available, but ONLY the primary half is visible
// from this region's cluster (the replica Cluster CR lives in region B). The
// endpoint must relay the probe verdict instead of reporting unknown.
func TestReplicationStatus_Row62_PerRegionSplitRelaysStandbyProbe(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured(
		"dr-shared-pg", "shared-data", "shared-pg",
		"me-east-215-a", []string{"me-east-215-b"})
	setContinuumControllerStatus(cr, "Healthy", true, 0)
	setCnpgPairRef(cr, "shared-pg", "shared-data")
	// ONLY the primary half — the region-B replica is invisible from here.
	primary, _ := newCNPGPairFixture("shared-pg", "shared-data", "me-east-215-a", "me-east-215-b")
	factory, _ := fakeContinuumDynamicFactory(cr, primary)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-row62-split-available")

	resp := fetchReplicationStatus(t, h, dep.ID, "dr-shared-pg", "shared-data")
	if resp.StandbyAvailable == nil {
		t.Fatalf("standbyAvailable omitted: the replica half is not visible from region A, so the endpoint reported unknown and the console's tri-state read has nothing to consume")
	}
	if !*resp.StandbyAvailable {
		t.Fatalf("standbyAvailable: got false want true (the controller's probe says the leg is available)")
	}
	if g := gateByName(t, resp, "standby-available"); g.Status != "Pass" {
		t.Fatalf("standby-available gate: got %q (%q), want Pass", g.Status, g.Message)
	}
}

// CONTROL — the same region-split, probe says the standby is GONE. This is the
// path that makes the console's red "Standby absent" banner reachable at all:
// standbyAvailable must be an explicit false (not omitted), the gate must Fail,
// the stream must read interrupted and the replica must not be promotable.
func TestReplicationStatus_Row62_PerRegionSplitSurfacesAbsentStandby(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured(
		"dr-shared-pg", "shared-data", "shared-pg",
		"me-east-215-a", []string{"me-east-215-b"})
	setContinuumControllerStatus(cr, "Healthy", false, 0)
	setCnpgPairRef(cr, "shared-pg", "shared-data")
	primary, _ := newCNPGPairFixture("shared-pg", "shared-data", "me-east-215-a", "me-east-215-b")
	factory, _ := fakeContinuumDynamicFactory(cr, primary)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-row62-split-absent")

	resp := fetchReplicationStatus(t, h, dep.ID, "dr-shared-pg", "shared-data")
	if resp.StandbyAvailable == nil || *resp.StandbyAvailable {
		t.Fatalf("standbyAvailable: got %v want an explicit false — a lost standby that reports 'unknown' leaves the red banner unreachable", resp.StandbyAvailable)
	}
	if g := gateByName(t, resp, "standby-available"); g.Status != "Fail" {
		t.Fatalf("standby-available gate: got %q want Fail", g.Status)
	}
	if resp.StreamingState != "interrupted" {
		t.Fatalf("streamingState: got %q want interrupted", resp.StreamingState)
	}
	if resp.ReplicaPromotable {
		t.Fatalf("replicaPromotable: got true, want false — nothing to promote when the standby leg is gone")
	}
}

// CONTROL — honest unknown SURVIVES. A Continuum with no cnpg pair and no
// standby probe (the dr-spine-openbao raft shape) must still report unknown,
// never a relayed Pass. This is what stops the fix above from becoming a
// blanket green.
func TestReplicationStatus_Row62_NoProbeStaysUnknown(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured(
		"dr-spine-openbao", "catalyst", "spine-openbao",
		"me-east-215-a", nil)
	factory, _ := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-row62-unknown")

	resp := fetchReplicationStatus(t, h, dep.ID, "dr-spine-openbao", "catalyst")
	if resp.StandbyAvailable != nil {
		t.Fatalf("standbyAvailable: got %v want omitted (no probe, no pair — unknown is the honest answer)", *resp.StandbyAvailable)
	}
	if g := gateByName(t, resp, "standby-available"); g.Status != "Warn" {
		t.Fatalf("standby-available gate: got %q want Warn", g.Status)
	}
}

// ── Row 62: phase + lease holder must reach the wire ─────────────────────

// Row 62's clause is "shows the live Continuum status (Ready / lease holder /
// standby) from the live API, not a static badge". The handler read leaseHolder
// and dropped it; the console therefore had nothing to render. Assert on the
// WIRE KEYS and on their VALUES.
func TestReplicationStatus_Row62_EmitsPhaseAndLeaseHolder(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured(
		"dr-shared-pg", "shared-data", "shared-pg",
		"hw-me-east-215-a-rtz-prod", []string{"hw-me-east-215-b-rtz-prod"})
	setContinuumControllerStatus(cr, "Healthy", true, 0)
	_ = unstructured.SetNestedField(cr.Object, "hw-me-east-215-a-rtz-prod", "status", "leaseHolder")
	_ = unstructured.SetNestedField(cr.Object, "2026-08-10T05:54:58Z", "status", "leaseExpiresAt")
	primary, replica := newCNPGPairFixture("shared-pg", "shared-data",
		"hw-me-east-215-a-rtz-prod", "hw-me-east-215-b-rtz-prod")
	factory, _ := fakeContinuumDynamicFactory(cr, primary, replica)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-row62-phase-lease")

	raw := fetchReplicationStatusRaw(t, h, dep.ID, "dr-shared-pg", "shared-data")
	for key, want := range map[string]string{
		"phase":          "Healthy",
		"leaseHolder":    "hw-me-east-215-a-rtz-prod",
		"leaseExpiresAt": "2026-08-10T05:54:58Z",
	} {
		got, ok := raw[key]
		if !ok {
			t.Fatalf("wire key %q absent — the console cannot render the live Continuum status it does not receive", key)
		}
		if got != want {
			t.Fatalf("wire key %q: got %v want %q", key, got, want)
		}
	}
}

// CONTROL — a CR that reports no lease holder must NOT get a fabricated one.
// `omitempty` means the key is simply absent, which is what lets the console
// distinguish "no lease observed" from a lease it can name.
func TestReplicationStatus_Row62_NoLeaseHolderIsOmittedNotInvented(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured(
		"dr-shared-pg", "shared-data", "shared-pg",
		"me-east-215-a", []string{"me-east-215-b"})
	// Blank the fixture's leaseHolder — the CR observes none.
	_ = unstructured.SetNestedField(cr.Object, "", "status", "leaseHolder")
	factory, _ := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-row62-no-lease")

	raw := fetchReplicationStatusRaw(t, h, dep.ID, "dr-shared-pg", "shared-data")
	if v, ok := raw["leaseHolder"]; ok {
		t.Fatalf("leaseHolder present as %v on a CR that reports none — that is an invented lease", v)
	}
}
