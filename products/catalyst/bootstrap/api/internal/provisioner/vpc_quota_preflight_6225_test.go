package provisioner

import (
	"errors"
	"testing"
)

// errQuotaAPI stands in for the transient HCS quota-API failures the
// pre-flight is documented to tolerate: a 5xx from
// GET /v1/{project}/quotas?type=vpc, or a response with no `type: vpc`
// resource in it (both produce a non-nil error from Provider.VPCQuota,
// internal/providers/huawei/provider.go:2395-2424).
var errQuotaAPI = errors.New("VPC quota GET: status 503")

// ─────────────────────────────────────────────────────────────────────
// THE DEFECT (#6225, UAT row 228)
// ─────────────────────────────────────────────────────────────────────

// TestVPCPreflight_ProceedsWhenPostReclaimQuotaUnknown is the row-228
// regression, and the one case that was RED before the fix.
//
// The scenario is precisely the row's clause — a re-prov immediately
// after a wipe:
//
//	project at 5/5 (the Kom4DC me-east-215 cap), of which the wiped
//	predecessor's orphaned catalyst-* VPCs are the occupants; the
//	2-region prov needs 2; the in-band reclaim RUNS AND SUCCEEDS; the
//	quota re-read taken straight afterwards flakes.
//
// The reclaim worked. The project is NOT known to be insufficient — it
// is un-measured. The documented contract (provisioner.go:2042-2046, "we
// never block on quota uncertainty") says proceed and let tofu apply
// arbitrate, which is exactly what a flaky FIRST read already did.
//
// Before the fix the re-read was an assign-on-success with no else, so
// the stale PRE-reclaim 5/5 survived into the refusal and the prov died
// with "a concurrent Sovereign genuinely occupies the project" — on a
// project whose orphans had just been deleted. That is the false-fail
// row 228 forbids.
func TestVPCPreflight_ProceedsWhenPostReclaimQuotaUnknown(t *testing.T) {
	first := quotaReading{used: 5, limit: 5}
	after := quotaReading{err: errQuotaAPI}

	if vpcPreflightRefuses(2, first, true, after) {
		t.Fatal("pre-flight REFUSED a re-prov whose orphan reclaim succeeded but whose " +
			"post-reclaim quota re-read flaked — the project is un-measured, not known-full. " +
			"This is the UAT row 228 false-fail: the wiped predecessor's orphans were " +
			"reclaimed and the prov was killed on the stale pre-reclaim reading anyway")
	}
}

// TestVPCPreflight_FirstAndReReadUncertaintyAgree pins the SYMMETRY that
// the defect broke. A failed first read and a failed post-reclaim
// re-read are the same epistemic state — "we asked and did not find
// out" — and must produce the same verdict. Before the fix they
// produced opposite ones, which is what made the bug so easy to miss:
// the tolerant path was right there, three lines above the intolerant
// one.
func TestVPCPreflight_FirstAndReReadUncertaintyAgree(t *testing.T) {
	unknownFirst := vpcPreflightRefuses(2, quotaReading{err: errQuotaAPI}, false, quotaReading{})
	unknownAfter := vpcPreflightRefuses(2, quotaReading{used: 5, limit: 5}, true, quotaReading{err: errQuotaAPI})

	if unknownFirst != unknownAfter {
		t.Fatalf("uncertainty must yield the same verdict wherever it occurs: "+
			"failed FIRST read refuses=%v, failed POST-RECLAIM re-read refuses=%v", unknownFirst, unknownAfter)
	}
	if unknownFirst {
		t.Fatal("neither uncertain reading may block the prov (provisioner.go:2042-2046)")
	}
}

// ─────────────────────────────────────────────────────────────────────
// THE CONTROLS — without these, the fix above is indistinguishable from
// a pre-flight that waves everything through, which would turn a
// false-fail into a strictly worse false-pass.
// ─────────────────────────────────────────────────────────────────────

// TestVPCPreflight_StillRefusesGenuineConflict is THE control.
//
// A genuinely-conflicting VPC that is NOT an orphan: a concurrent live
// Sovereign shares the HCS project, so reclaimProtectSet correctly
// protects its VPCs from the reclaim (#4614 — the 2026-06-28 incident
// where an under-seeded protect-set cascade-deleted 12 production
// nodes). The reclaim therefore frees nothing, and the post-reclaim
// re-read SUCCEEDS and still reports the project full.
//
// That state IS "known to be insufficient" and must still REFUSE. If
// this case ever passes the gate, the fix has widened the pre-flight
// rather than corrected it: the prov would proceed, sink ~3 minutes into
// tofu apply with CP + EIPs + SGs allocated, and HCS would refuse the
// VPC create anyway.
func TestVPCPreflight_StillRefusesGenuineConflict(t *testing.T) {
	first := quotaReading{used: 5, limit: 5}
	after := quotaReading{used: 5, limit: 5} // reclaim freed nothing; re-read SUCCEEDED

	if !vpcPreflightRefuses(2, first, true, after) {
		t.Fatal("pre-flight ADMITTED a prov into a project a live concurrent Sovereign " +
			"genuinely fills (post-reclaim re-read succeeded and still reads 5/5). " +
			"Known-insufficient must refuse — admitting it converts row 228's false-fail " +
			"into a false-pass that dies ~3min into tofu apply instead")
	}
}

// TestVPCPreflight_RefusesWhenNoReclaimPathExists keeps the pre-#4431
// baseline. With no VPCReclaimHook registered there is nothing that
// could free room, so a known-over project must refuse immediately.
// Pairs with the control above so "reclaimAttempted" cannot be turned
// into a blanket escape hatch.
func TestVPCPreflight_RefusesWhenNoReclaimPathExists(t *testing.T) {
	if !vpcPreflightRefuses(2, quotaReading{used: 4, limit: 5}, false, quotaReading{}) {
		t.Fatal("known-over project with NO reclaim path available must refuse")
	}
}

// TestVPCPreflight_ProceedsWhenReclaimFreedRoom is the happy path row
// 228 describes when the re-read works: the wiped predecessor's orphans
// are reclaimed, the re-read succeeds, the project now fits, the prov
// proceeds. Proves the fix did not make the gate unconditionally
// refuse-on-reclaim either.
func TestVPCPreflight_ProceedsWhenReclaimFreedRoom(t *testing.T) {
	first := quotaReading{used: 5, limit: 5}
	after := quotaReading{used: 1, limit: 5} // 4 orphaned catalyst-* VPCs reclaimed

	if vpcPreflightRefuses(2, first, true, after) {
		t.Fatal("pre-flight refused after the reclaim measurably freed room (5/5 -> 1/5, need 2)")
	}
}

// TestVPCPreflight_ProceedsWhenProjectAlreadyFits guards the ordinary
// prov: a project with room is admitted and no reclaim is even
// attempted. This is the case that must NOT regress into "sweep on every
// prov".
func TestVPCPreflight_ProceedsWhenProjectAlreadyFits(t *testing.T) {
	if vpcPreflightRefuses(2, quotaReading{used: 1, limit: 5}, false, quotaReading{}) {
		t.Fatal("pre-flight refused a prov that fits comfortably (1/5, need 2)")
	}
}

// TestVPCPreflight_ExactFitAdmitted pins the boundary. A 2-region prov
// into a project at 3/5 lands exactly on the cap. The comparison is
// used+needed <= limit, so it is admitted: the pre-flight refuses what
// cannot fit, never what merely fills the project. An off-by-one here
// would false-fail every prov that exactly consumes its quota — the same
// class of defect as row 228, one slot earlier.
func TestVPCPreflight_ExactFitAdmitted(t *testing.T) {
	if vpcPreflightRefuses(2, quotaReading{used: 3, limit: 5}, false, quotaReading{}) {
		t.Fatal("exact fit (3/5 + 2 = 5) must be admitted, not refused")
	}
	// One more than an exact fit must still refuse — proves the boundary
	// test above is not passing because the predicate is inert.
	if !vpcPreflightRefuses(3, quotaReading{used: 3, limit: 5}, false, quotaReading{}) {
		t.Fatal("3/5 + 3 = 6 exceeds the cap and must refuse")
	}
}

// TestEffectiveReading_QuotesTheReadingThatDroveTheRefusal checks the
// operator-facing message quotes the right numbers: the post-reclaim
// reading when one was successfully taken, else the original. A refusal
// that printed stale pre-reclaim numbers is what made the original
// defect read as "the project is full" when it was not.
func TestEffectiveReading_QuotesTheReadingThatDroveTheRefusal(t *testing.T) {
	first := quotaReading{used: 5, limit: 5}
	after := quotaReading{used: 4, limit: 5}

	if got := effectiveReading(first, true, after); got.used != 4 {
		t.Errorf("after a successful re-read the message must quote it: used=%d, want 4", got.used)
	}
	if got := effectiveReading(first, true, quotaReading{err: errQuotaAPI}); got.used != 5 {
		t.Errorf("with no usable re-read the message falls back to the first reading: used=%d, want 5", got.used)
	}
	if got := effectiveReading(first, false, quotaReading{}); got.used != 5 {
		t.Errorf("with no reclaim attempted the message quotes the first reading: used=%d, want 5", got.used)
	}
}
