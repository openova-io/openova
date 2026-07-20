package controller

import (
	"context"
	"testing"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/apimachinery/pkg/types"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// #5305 (hw282) — a funnel customer's PURCHASED app never deployed: the funnel
// cart-install's commit to the per-Org `<slug>/catalyst-tenant` repo lost the
// compare-and-swap to the organization-controller on EVERY attempt, so the app
// manifest never landed in `vcluster/apps/` and the Org boundary held only the
// NP baseline.
//
// Root cause: the reconciler watches the Organization with the DEFAULT
// (all-events) filter AND patchStatus wrote status unconditionally with a Ready
// condition whose LastTransitionTime was stamped now() every pass. That status
// write re-enqueued the reconcile from its own output — a sub-second HOT LOOP
// the whole time the Org provisioned. Each pass re-ran step-3's per-Org repo
// PutFile burst (incl. the shared `vcluster/apps/kustomization.yaml`
// merge-write), turning the controller into a CONTINUOUS competing writer on the
// branch HEAD that the funnel's finite retry budget could never out-run.
//
// The fix makes the status write CONVERGE-IDEMPOTENT (carry LastTransitionTime
// forward when the condition is unchanged; skip the write when status is
// byte-equal), so the controller re-fires only on a real state change or the
// explicit 30s provisioning requeue — collapsing its per-Org repo writes to a
// FINITE seed burst. These tests lock in that the controller is no longer a
// continuous writer.

// readyCondition returns the "Ready" condition of an Organization, or nil.
func readyCondition(org *orgapi.Organization) *orgapi.Condition {
	for i := range org.Status.Conditions {
		if org.Status.Conditions[i].Type == "Ready" {
			return &org.Status.Conditions[i]
		}
	}
	return nil
}

// TestReconcile_ConvergeIdempotent_NoStatusChurn_NoReburst_5305 proves the
// controller does NOT re-write status (the hot-loop trigger) NOR re-burst the
// per-Org repo once its state matches desired. A steady-state reconcile must be
// a genuine no-op: stable object resourceVersion (no self-triggering status
// write), unchanged Ready-condition LastTransitionTime (no churn), and zero
// additional Gitea file writes (no re-burst on the shared branch HEAD).
func TestReconcile_ConvergeIdempotent_NoStatusChurn_NoReburst_5305(t *testing.T) {
	org := sampleOrg()
	r, gs, _ := makeReconciler(t, org)
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "acme"}}

	// Two warmup reconciles to reach steady state (seed the repo, add the
	// finalizer, settle status). The Org never goes Ready here (no vCluster HR
	// is seeded), so it sits stably at Ready=False:VClusterProvisioning.
	for i := 0; i < 2; i++ {
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatalf("warmup reconcile %d: %v", i, err)
		}
	}

	before := sampleOrg()
	if err := r.Get(ctx, client.ObjectKey{Name: "acme"}, before); err != nil {
		t.Fatalf("get before: %v", err)
	}
	rvBefore := before.ResourceVersion
	rcBefore := readyCondition(before)
	if rcBefore == nil {
		t.Fatalf("no Ready condition after warmup: %+v", before.Status)
	}
	writesBefore := gs.createFiles + gs.updateFiles

	// One more reconcile with NOTHING changed. Pre-#5305 this wrote status again
	// (LastTransitionTime=now()), bumping resourceVersion and re-enqueueing the
	// reconcile — the hot loop that made step-3 a continuous branch writer.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("steady-state reconcile: %v", err)
	}

	after := sampleOrg()
	if err := r.Get(ctx, client.ObjectKey{Name: "acme"}, after); err != nil {
		t.Fatalf("get after: %v", err)
	}
	if after.ResourceVersion != rvBefore {
		t.Errorf("#5305 regression: steady-state reconcile mutated the Organization (resourceVersion %s → %s) — the self-triggering status write hot-loops the reconciler into a continuous branch writer",
			rvBefore, after.ResourceVersion)
	}
	rcAfter := readyCondition(after)
	if rcAfter == nil {
		t.Fatalf("no Ready condition after steady-state reconcile")
	}
	if !rcAfter.LastTransitionTime.Equal(&rcBefore.LastTransitionTime) {
		t.Errorf("#5305 regression: Ready condition LastTransitionTime churned (%v → %v) with no state change — this is the now()-every-pass stamp that defeated the byte-equal status skip",
			rcBefore.LastTransitionTime, rcAfter.LastTransitionTime)
	}
	if writesAfter := gs.createFiles + gs.updateFiles; writesAfter != writesBefore {
		t.Errorf("#5305 regression: steady-state reconcile re-burst the per-Org repo (%d → %d writes) — the controller is a continuous writer again, starving the funnel's compare-and-swap",
			writesBefore, writesAfter)
	}
}

// TestReconcile_ConcurrentWriterCASLoss_SoftSkips_NoFail_5305 proves that when
// the funnel (a concurrent writer) wins the compare-and-swap on the shared apps
// index — modelled as a one-shot 409 git-ref-lock on that path's write — the
// controller SOFT-SKIPS the lost CAS instead of failing the whole reconcile and
// re-bursting the entire PutFile set (the amplification that starved the funnel
// on hw282). The Org must not be parked at GitopsWriteFailed, and a subsequent
// reconcile (collision consumed) must seed the index cleanly.
func TestReconcile_ConcurrentWriterCASLoss_SoftSkips_NoFail_5305(t *testing.T) {
	org := sampleOrg()
	r, gs, _ := makeReconciler(t, org)
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "acme"}}

	const appsIndexKey = "acme/catalyst-tenant/vcluster/apps/kustomization.yaml"
	gs.collideOnce[appsIndexKey] = true

	// The reconcile must NOT return an error and must NOT park the Org at
	// GitopsWriteFailed — the lost CAS means the funnel converged, which is the
	// desired end state, not a controller failure.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile returned an error on a concurrent-writer CAS loss (should soft-skip): %v", err)
	}
	got := sampleOrg()
	if err := r.Get(ctx, client.ObjectKey{Name: "acme"}, got); err != nil {
		t.Fatalf("get org: %v", err)
	}
	for _, c := range got.Status.Conditions {
		if c.Reason == "GitopsWriteFailed" {
			t.Fatalf("#5305 regression: reconcile FAILED the Org on a concurrent-writer CAS loss instead of soft-skipping — this re-bursts the whole PutFile set and starves the funnel: %+v", c)
		}
	}
	// The controller soft-skipped its own index write (the funnel owns it now).
	if _, seeded := gs.files[appsIndexKey]; seeded {
		t.Errorf("expected the controller to soft-skip the collided index write (leaving it to the funnel), but it wrote %s", appsIndexKey)
	}

	// A subsequent reconcile (collision consumed) seeds the index cleanly and
	// still does not fail — the soft-skip self-heals on the next pass.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("follow-up reconcile after soft-skip: %v", err)
	}
	if _, seeded := gs.files[appsIndexKey]; !seeded {
		t.Errorf("index still absent after the collision was consumed — the controller did not re-seed it on the next reconcile")
	}
}
