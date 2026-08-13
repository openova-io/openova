package handler

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// #6156 CALL-SITE coverage.
//
// The fix for #6156 shipped with five tests, and every one of them calls
// continuumStandbyForPairs directly — the test file says so itself: "These
// exercise continuumStandbyForPairs, the seam that decides it." The helper is
// therefore well covered and the chain it feeds is not. Change
// `check.Status = preflightFail` to preflightWarn at the call site in
// continuum_dr_extras.go and all five keep passing, because none of them ever
// reach preflightCNPGPairStreaming.
//
// That matters here more than usual, because the issue's clause is not "the
// helper returns the right list". It is "a genuine standby outage BLOCKS a
// mutating DR playback". Blocking is three hops past the helper:
//
//	continuumStandbyForPairs -> check.Status=Fail + Severity=critical
//	  -> classifyPreflight -> "NotReady" -> HandleDRRunbookPlayback aborts
//
// Only the first hop was pinned. These tests pin the rest, driving the real
// preflight through the fake dynamic client so the switch branch and the
// severity are both load-bearing.

// withStandbyAvailable stamps the continuum-controller's own pgprobe verdict —
// the field preflight-02 falls back to when the replica half lives in the peer
// region and is invisible from this cluster's API.
func withStandbyAvailable(cr *unstructured.Unstructured, available bool) *unstructured.Unstructured {
	_ = unstructured.SetNestedField(cr.Object, available, "status", "standbyAvailable")
	return cr
}

// THE #6156 CASE, end to end. Region A sees only the primary half (the 2-region
// steady state that used to pin this check at Warn forever), and the Continuum
// controller reports the standby GONE. preflight-02 must Fail as critical, and
// the matrix must roll up to NotReady with preflight-02 named as blocking —
// that rollup is what HandleDRRunbookPlayback refuses to proceed past.
func TestDRPreflight02_StandbyOutageBlocksPlayback_6156(t *testing.T) {
	primary, _ := newCNPGPairFixture("shared-pg", "shared-data", "me-east-215-a", "me-east-215-b")
	cont := withStandbyAvailable(
		newCnpgPairContinuumCR("shared-pg", "shared-data", "shared-pg", "me-east-215-a", "me-east-215-b"),
		false)

	h, depID := fakePreflightHandler(t, "dep-6156-outage", primary, cont)
	checks := runPreflight(t, h, depID)
	c := checkByID(t, checks, "preflight-02")

	if c.Status != preflightFail {
		t.Errorf("preflight-02: got %q want Fail — the Continuum controller reports the hot standby "+
			"UNAVAILABLE, so replication has no standby leg (msg=%q)", c.Status, c.Message)
	}
	// Severity is not decoration: classifyPreflight only sets NotReady when a
	// Fail is critical. A Fail at severity "warning" would leave the playback
	// unblocked while the check still reads red on the console.
	if c.Severity != "critical" {
		t.Errorf("preflight-02: got severity %q want critical — classifyPreflight gates NotReady on "+
			"criticality, so a non-critical Fail is a red check that blocks nothing", c.Severity)
	}

	overall, blocking := classifyPreflight(checks)
	if overall != "NotReady" {
		t.Errorf("classifyPreflight: got %q want NotReady — a standby outage must abort a mutating "+
			"DR playback, which is the whole clause of #6156", overall)
	}
	found := false
	for _, b := range blocking {
		if b == "cnpgpair-streaming" {
			found = true
		}
	}
	if !found {
		t.Errorf("classifyPreflight: cnpgpair-streaming missing from blocking=%v — the operator is "+
			"told the playback is blocked without being told which gate blocked it", blocking)
	}
}

// CONTROL sharing the suspect property: same invisible-replica geometry, same
// Continuum CR, same code path — only the verdict flips. Without this, the test
// above passes for a handler that fails preflight-02 unconditionally.
func TestDRPreflight02_HealthyStandbyDoesNotBlock_6156(t *testing.T) {
	primary, _ := newCNPGPairFixture("shared-pg", "shared-data", "me-east-215-a", "me-east-215-b")
	cont := withStandbyAvailable(
		newCnpgPairContinuumCR("shared-pg", "shared-data", "shared-pg", "me-east-215-a", "me-east-215-b"),
		true)

	h, depID := fakePreflightHandler(t, "dep-6156-healthy", primary, cont)
	c := checkByID(t, runPreflight(t, h, depID), "preflight-02")

	if c.Status == preflightFail {
		t.Errorf("preflight-02: got Fail on a HEALTHY standby — the check would block every playback "+
			"on a 2-region Sovereign, trading the old always-Warn for an always-Fail (msg=%q)", c.Message)
	}
}

// The honest-unknown case must survive the fix. A pair with no Continuum
// verdict at all is not evidence of health OR of outage: it stays Warn, so it
// neither fabricates a Pass nor blocks a playback on absent evidence.
func TestDRPreflight02_NoVerdictStillWarnsNotBlocks_6156(t *testing.T) {
	primary, _ := newCNPGPairFixture("shared-pg", "shared-data", "me-east-215-a", "me-east-215-b")

	h, depID := fakePreflightHandler(t, "dep-6156-silent", primary)
	checks := runPreflight(t, h, depID)
	c := checkByID(t, checks, "preflight-02")

	if c.Status != preflightWarn {
		t.Errorf("preflight-02: got %q want Warn — no Continuum CR carries a standbyAvailable verdict, "+
			"and absence of evidence is neither health nor outage (msg=%q)", c.Status, c.Message)
	}
	if _, blocking := classifyPreflight(checks); blockedBy(blocking, "cnpgpair-streaming") {
		t.Error("classifyPreflight: an unmeasured pair blocked the playback — that turns a 2-region " +
			"Sovereign with no Continuum CR into a permanently un-runnable DR runbook")
	}
}

func blockedBy(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
