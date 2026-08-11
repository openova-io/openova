// continuum_dr_extras_6114_test.go — #6114 regression coverage.
//
// THE DEFECT: the DR panel prints an em-dash for replication lag while the
// response carries a REAL measurement.
//
// HandleContinuumReplicationStatus builds its health gates from the Continuum
// CR (enrichReplicationStatus -> walLagHealthGate). walLagHealthGate returns
// Warn/"replication lag not reported by the Continuum CR; unverified" when the
// CR carries neither status.walLagSeconds nor status.replicationLagSeconds —
// correct and deliberate, because an absent lag is unknown, not a healthy zero
// (#4901 kept lag pinned at 0 through an outage; #4923 made absence Warn).
//
// The handler THEN reads the linked CNPGPair and overwrites the number:
//
//	if lag > 0 { resp.WALLagSeconds = lag }        (continuum_dr_extras.go)
//
// ...and never re-derives the gate. So the response ships a genuine numeric
// lag alongside a stale "unverified" Warn. The console treats that Warn as
// proof the number is not a measurement and suppresses it:
//
//	AppDetail/TopologyTab.tsx  — lagUnverified => render '—'
//
// Which is exactly what UAT rows R12/R13 and the 51/52/55/56/62/64/65/66/67/
// 69/70 topology block assert must never happen: "a live numeric
// replication-lag, never a hardcoded —".
//
// The fix re-derives the gate ONLY from a positive measurement a real producer
// wrote. It cannot manufacture a green from absence, and the tests below pin
// both directions plus the absence case.
package handler

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// linkedPairLagFixture — a Continuum CR that reports NO lag key of its own
// (so the CR-derived gate is the "unverified" Warn) linked to a CNPGPair that
// DOES carry a reading. This is the hw293/hw292 dr-shared-pg shape: the
// Continuum controller publishes the pair link, the pair publishes the lag.
func linkedPairLagFixture(t *testing.T, h *Handler, depID string, pairStatus map[string]interface{}) continuumReplicationStatus {
	t.Helper()
	cr := newContinuumUnstructured(
		"dr-shared-pg", "shared-data", "shared-pg",
		"me-east-215-a", []string{"me-east-215-b"})
	_ = unstructured.SetNestedMap(cr.Object, map[string]interface{}{
		"name":      "shared-pg-pair",
		"namespace": "shared-data",
	}, "spec", "cnpgPair")
	pair := newCNPGPairCRUnstructured("shared-pg-pair", "shared-data", pairStatus)
	factory, _ := fakeContinuumDynamicFactory(cr, pair)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, depID)
	return fetchReplicationStatus(t, h, dep.ID, "dr-shared-pg", "shared-data")
}

// THE ROW. A real, under-threshold measurement from the linked pair must leave
// the wal-lag gate PASSING, so the console renders the number instead of '—'.
func TestReplicationStatus_6114_RealPairLagIsNotReportedUnverified(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	resp := linkedPairLagFixture(t, h, "dep-6114-real-lag", map[string]interface{}{
		"phase":                 "Streaming",
		"streaming":             true,
		"replicationLagSeconds": int64(3),
	})

	if resp.WALLagSeconds != 3 {
		t.Fatalf("walLagSeconds: got %v want 3 — fixture no longer exercises the overwrite, every assertion below is vacuous",
			resp.WALLagSeconds)
	}
	g := gateByName(t, resp, "wal-lag-under-rpo")
	if g.Status != "Pass" {
		t.Errorf("wal-lag-under-rpo: got %q (msg=%q) want Pass.\n"+
			"The response carries a REAL 3s measurement from the linked CNPGPair, but the gate "+
			"was derived from the Continuum CR (which carries no lag key) and never re-derived. "+
			"The console reads that Warn as 'not a measurement' and renders an em-dash over a "+
			"live number — the exact thing UAT rows R12/R13 assert must never happen.",
			g.Status, g.Message)
	}
}

// CONTROL — same code path, opposite expectation. An OVER-threshold
// measurement must still Warn, and the message must name the threshold. Without
// this, the fix above could be "always Pass", which would be worse than the
// defect: a 5-minute lag would render as a healthy green number.
func TestReplicationStatus_6114_OverThresholdPairLagStillWarns(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	resp := linkedPairLagFixture(t, h, "dep-6114-over-threshold", map[string]interface{}{
		"phase":                 "Streaming",
		"streaming":             true,
		"replicationLagSeconds": int64(45),
	})

	if resp.WALLagSeconds != 45 {
		t.Fatalf("walLagSeconds: got %v want 45 (CONTROL fixture broken)", resp.WALLagSeconds)
	}
	g := gateByName(t, resp, "wal-lag-under-rpo")
	if g.Status != "Warn" {
		t.Errorf("wal-lag-under-rpo: got %q want Warn for a 45s lag — re-deriving the gate must not "+
			"become a blanket Pass; an over-RPO lag has to stay visible", g.Status)
	}
	if g.Message == "" {
		t.Errorf("wal-lag-under-rpo Warn carries no message naming the breach")
	}
}

// CONTROL — the #4901/#4923 invariant the fix must NOT erode. When neither the
// CR nor the pair reports any lag, the gate must REMAIN the "unverified" Warn.
// Absent lag is unknown, not a healthy zero: #4901's outage kept lag pinned at
// 0, and a gate that reads absence as Pass is a verdict from absent evidence.
func TestReplicationStatus_6114_AbsentLagStaysUnverified(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	resp := linkedPairLagFixture(t, h, "dep-6114-absent-lag", map[string]interface{}{
		"phase":     "Streaming",
		"streaming": true,
		// no lag key on either object
	})

	if resp.WALLagSeconds != 0 {
		t.Fatalf("walLagSeconds: got %v want 0 (nothing reported one)", resp.WALLagSeconds)
	}
	g := gateByName(t, resp, "wal-lag-under-rpo")
	if g.Status != "Warn" {
		t.Errorf("wal-lag-under-rpo: got %q want Warn when NOTHING reports a lag. "+
			"A zero that no producer wrote must never read as a healthy zero (#4901/#4923).",
			g.Status)
	}
}

// The CNPGPair CRD's own spelling must work identically to the controller's.
// #5601 already proved the NUMBER is read from both keys; this proves the GATE
// is too, so the fix is not wired to one spelling.
func TestReplicationStatus_6114_WalLagSecondsSpellingAlsoPasses(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	resp := linkedPairLagFixture(t, h, "dep-6114-wallag-spelling", map[string]interface{}{
		"phase":         "Streaming",
		"streaming":     true,
		"walLagSeconds": int64(2),
	})

	if resp.WALLagSeconds != 2 {
		t.Fatalf("walLagSeconds: got %v want 2 (CNPGPair CRD spelling)", resp.WALLagSeconds)
	}
	if g := gateByName(t, resp, "wal-lag-under-rpo"); g.Status != "Pass" {
		t.Errorf("wal-lag-under-rpo: got %q want Pass — the gate must follow BOTH lag spellings, "+
			"not just the controller's (#5601 class)", g.Status)
	}
}
