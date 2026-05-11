// rollup_test.go — covers the worst-of-children rollup logic the
// adapter applies to synthetic group nodes (region root + phase
// columns).
//
// Palette ordering: failed > running > pending > succeeded
package test

import (
	"testing"

	"github.com/openova-io/openova/products/openova-flow/adapter-flux/internal/informer"
)

func TestRollup_Empty(t *testing.T) {
	if got := informer.RollupStatus(nil); got != "pending" {
		t.Fatalf("rollup(nil) = %s want pending", got)
	}
	if got := informer.RollupStatus([]string{}); got != "pending" {
		t.Fatalf("rollup([]) = %s want pending", got)
	}
}

func TestRollup_AllSucceeded(t *testing.T) {
	got := informer.RollupStatus([]string{"succeeded", "succeeded", "succeeded"})
	if got != "succeeded" {
		t.Fatalf("rollup(all succeeded) = %s want succeeded", got)
	}
}

func TestRollup_OneRunningRestSucceeded(t *testing.T) {
	got := informer.RollupStatus([]string{"succeeded", "running", "succeeded"})
	if got != "running" {
		t.Fatalf("got %s want running", got)
	}
}

func TestRollup_OneFailedBeatsAll(t *testing.T) {
	got := informer.RollupStatus([]string{"succeeded", "failed", "running"})
	if got != "failed" {
		t.Fatalf("got %s want failed", got)
	}
}

func TestRollup_PendingBeatsSucceeded(t *testing.T) {
	got := informer.RollupStatus([]string{"succeeded", "pending", "succeeded"})
	if got != "pending" {
		t.Fatalf("got %s want pending", got)
	}
}

func TestRollup_RunningBeatsPending(t *testing.T) {
	got := informer.RollupStatus([]string{"pending", "running", "pending"})
	if got != "running" {
		t.Fatalf("got %s want running", got)
	}
}

func TestRollup_UnknownNormalizesToRunning(t *testing.T) {
	// Unknown statuses default to "running" precedence per the
	// rollup contract (keeps a noisy upstream from accidentally
	// bubbling up "succeeded").
	got := informer.RollupStatus([]string{"succeeded", "bogus"})
	if got != "running" {
		t.Fatalf("got %s want running", got)
	}
}

func TestStatusTracker_Lifecycle(t *testing.T) {
	tr := informer.NewStatusTracker()

	tr.Record("fsn1", "fsn1/bp-cilium", "succeeded")
	tr.Record("fsn1", "fsn1/bp-cert-manager", "running")
	if got := tr.Rollup("fsn1"); got != "running" {
		t.Fatalf("after 2 records: %s want running", got)
	}

	tr.Record("fsn1", "fsn1/bp-keycloak", "failed")
	if got := tr.Rollup("fsn1"); got != "failed" {
		t.Fatalf("after failed: %s want failed", got)
	}

	tr.Forget("fsn1", "fsn1/bp-keycloak")
	if got := tr.Rollup("fsn1"); got != "running" {
		t.Fatalf("after forget failed: %s want running", got)
	}

	// Forget remaining children — group collapses to pending.
	tr.Forget("fsn1", "fsn1/bp-cilium")
	tr.Forget("fsn1", "fsn1/bp-cert-manager")
	if got := tr.Rollup("fsn1"); got != "pending" {
		t.Fatalf("after forget all: %s want pending", got)
	}
}

func TestStatusTracker_PerGroupIsolation(t *testing.T) {
	tr := informer.NewStatusTracker()
	tr.Record("fsn1", "fsn1/bp-cilium", "succeeded")
	tr.Record("fsn1/phase-1", "fsn1/bp-cilium", "succeeded")
	tr.Record("fsn1/phase-2", "fsn1/bp-cutover", "failed")

	if got := tr.Rollup("fsn1"); got != "succeeded" {
		t.Fatalf("fsn1 rollup: %s want succeeded", got)
	}
	if got := tr.Rollup("fsn1/phase-1"); got != "succeeded" {
		t.Fatalf("phase-1 rollup: %s want succeeded", got)
	}
	if got := tr.Rollup("fsn1/phase-2"); got != "failed" {
		t.Fatalf("phase-2 rollup: %s want failed", got)
	}
}
