// stale_provision_reap_test.go — #3898.
//
// The dedup collision path (startProvisioning) reaps an in-flight provision
// row that has gone stale — abandoned by a provisioning Pod crash/roll — so the
// in-flight uniqueness guard in CreateProvisionIfAbsent (an application-level
// check-then-insert; FerretDB can't express the partial unique index that once
// backed it) releases and the tenant can re-provision instead of wedging out of
// the #3376 funnel forever.
//
// The reap is only SAFE if the staleness threshold is strictly larger than the
// longest gap a HEALTHY workflow can leave between updated_at refreshes. The
// workflow stamps updated_at on every markStep / completeStep, and its longest
// single blocking wait is 10 minutes (waitForHelmRelease / waitForVclusterApp).
// If staleProvisionTimeout were ≤ that wait, a perfectly healthy provision that
// happened to be mid-wait at the moment of a duplicate dispatch could be reaped
// out from under its own running goroutine — forking a second workflow and
// re-introducing the exact #3744 self-race the dedup exists to prevent.
//
// This test pins that invariant in code so a future tightening of the threshold
// can't silently re-open the race.

package handlers

import (
	"testing"
	"time"
)

// maxSingleStepWait is the longest single blocking wait in
// runProvisioningWorkflow (waitForHelmRelease + each waitForVclusterApp are all
// 10 minutes). Kept here as the asserted reference; if the workflow ever grows
// a longer wait, this constant — and staleProvisionTimeout — must grow with it.
const maxSingleStepWait = 10 * time.Minute

func TestStaleProvisionTimeout_ExceedsLongestHealthyStepWait(t *testing.T) {
	if staleProvisionTimeout <= maxSingleStepWait {
		t.Fatalf("staleProvisionTimeout (%s) must be strictly greater than the longest healthy single-step wait (%s); "+
			"otherwise the #3898 reaper could kill a healthy in-flight provision mid-wait and re-open the #3744 self-race",
			staleProvisionTimeout, maxSingleStepWait)
	}
	// A comfortable margin (≥2×) guards against clock skew between the worker
	// stamping updated_at and the reaper computing its cutoff.
	if staleProvisionTimeout < 2*maxSingleStepWait {
		t.Errorf("staleProvisionTimeout (%s) leaves under a 2× margin over the longest step wait (%s) — "+
			"prefer more headroom so clock skew can't trip a premature reap",
			staleProvisionTimeout, maxSingleStepWait)
	}
}
