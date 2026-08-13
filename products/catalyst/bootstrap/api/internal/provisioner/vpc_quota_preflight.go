package provisioner

// The HCS VPC-quota pre-flight decision (#6225, UAT row 228).
//
// WHY THIS IS A SEPARATE, PURE FUNCTION
// ─────────────────────────────────────
// The decision used to live inline inside Provision — a ~1000-line
// function that needs a live HCS project, a tofu workdir and a running
// mothership to reach. That is why the defect below shipped and survived:
// there was no seam at which "does the pre-flight refuse?" could be asked
// without provisioning a Sovereign. `quotaReading` + vpcPreflightRefuses
// are that seam; Provision now only gathers the readings and reports the
// verdict.
//
// THE CONTRACT (stated at the call site since #4431, honoured here)
// ─────────────────────────────────────────────────────────────────
// The pre-flight is BEST-EFFORT. `tofu apply` is the canonical authority
// on whether the project has room; the pre-flight exists only to turn a
// KNOWN-hopeless prov into a fast, legible refusal instead of a failure
// ~3 minutes into apply with CP + EIPs + SGs already allocated. So it
// blocks ONLY on quota known to be insufficient — never on quota
// uncertainty.
//
// THE DEFECT THIS FIXES (#6225)
// ─────────────────────────────
// The FIRST quota read honoured that contract: a transient API error
// skipped the gate and let the prov proceed. The RE-READ taken after the
// orphan reclaim did not. It was written as an assign-on-success with no
// else branch, so a flaky re-read left the STALE, PRE-RECLAIM used/limit
// in place and the refusal then fired on those stale numbers — killing a
// prov whose orphaned catalyst-* VPCs had just been deleted successfully,
// under a message blaming "a concurrent Sovereign". Same uncertainty as a
// flaky first read, opposite verdict.
//
// That is UAT row 228's false-fail: a re-prov AFTER a wipe dying on the
// leftovers of the wiped predecessor even though the reclaim worked.
//
// WHAT IS DELIBERATELY *NOT* CHANGED
// ──────────────────────────────────
// A SUCCESSFUL post-reclaim re-read that still shows the project over cap
// must still REFUSE. That is the genuine-conflict case: a concurrent
// Sovereign really does occupy the project, its VPCs are correctly
// protected from the reclaim by reclaimProtectSet, and there is no room.
// Widening the gate to wave that through would convert a false-fail into
// a false-pass — the prov would sink ~3 minutes into apply before HCS
// refused the VPC create anyway. A false-pass here is strictly worse than
// the bug being fixed, so the refusal path is preserved exactly and is
// pinned by its own control test.

// quotaReading is one observation of the HCS per-project VPC quota.
//
// `err != nil` means the observation FAILED and carries no usable
// numbers — used/limit must not be read in that state. This is the
// distinction the inline code lost: it had no way to represent "we asked
// and did not find out", so a failed re-read was indistinguishable from
// the previous successful one.
type quotaReading struct {
	used  int
	limit int
	err   error
}

// known reports whether this reading carries usable numbers.
func (q quotaReading) known() bool { return q.err == nil }

// fits reports whether `needed` additional VPCs sit within the observed
// cap. Only meaningful when known() is true.
//
// The comparison is `used+needed <= limit`, so an exact fit (a 2-region
// prov into a project at 3/5) is admitted — the pre-flight refuses only
// what genuinely cannot fit, never what merely fills the project.
func (q quotaReading) fits(needed int) bool { return q.used+needed <= q.limit }

// vpcPreflightRefuses is the whole pre-flight verdict: true = REFUSE the
// prov before tofu init, false = proceed and let tofu apply arbitrate.
//
// Parameters:
//   - needed: VPCs this prov will create (one per region).
//   - first: the pre-reclaim quota observation.
//   - reclaimAttempted: whether the in-band orphan-VPC reclaim actually
//     ran. False when the project already fit, or when no VPCReclaimHook
//     is registered (e.g. a non-huawei build or CI).
//   - after: the post-reclaim observation. Read ONLY when
//     reclaimAttempted is true, and the caller assigns it in the same
//     branch that sets that flag.
//
// The four exits, in order:
//
//  1. first unknown          → PROCEED. A transient quota-API error must
//     never wedge the create path (#4431).
//  2. first fits             → PROCEED. Nothing to reclaim, nothing to
//     refuse.
//  3. no reclaim attempted   → REFUSE. Known-over with no reclaim path
//     available; this is the pre-#4431 behaviour and stays intact.
//  4. after unknown          → PROCEED (#6225). A reclaim ran and we
//     could not re-measure. The project is NOT known to be
//     insufficient, so the contract forbids blocking here.
//     Symmetric with exit 1.
//  5. otherwise              → refuse iff the post-reclaim reading still
//     does not fit. The genuine-conflict control.
func vpcPreflightRefuses(needed int, first quotaReading, reclaimAttempted bool, after quotaReading) bool {
	if !first.known() {
		return false
	}
	if first.fits(needed) {
		return false
	}
	if !reclaimAttempted {
		return true
	}
	if !after.known() {
		return false
	}
	return !after.fits(needed)
}

// effectiveReading returns the reading the operator-facing refusal
// message should quote: the post-reclaim one when a reclaim ran and
// re-measured, else the original. Keeps the error text describing the
// state that actually drove the refusal.
func effectiveReading(first quotaReading, reclaimAttempted bool, after quotaReading) quotaReading {
	if reclaimAttempted && after.known() {
		return after
	}
	return first
}
