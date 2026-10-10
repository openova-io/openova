// commerce_roundtrip_test.go — #6971 item 8. The organization-controller does
// NOT reconcile spec.commerce, but it MUST tolerate it: the first reconcile
// pass adds finalizers through a typed `r.Update(ctx, &org)`, and a typed
// round-trip erases every spec field the Go type does not model (the #4471
// clientSecretRef trap, repeated for costSources). This pins that an
// Organization carrying the purchase keeps it — and keeps it as the SAME
// values — after the controller has written the CR back, so the chargeback
// OpenOva adapter reads what the provisioning consumer stamped, not a
// stripped CR. It also pins the converse: an Organization WITHOUT the block
// does not acquire `commerce: {}` from the round-trip.
package controller

import (
	"context"
	"reflect"
	"testing"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"

	"k8s.io/apimachinery/pkg/types"
)

func TestReconcile_SpecCommerceSurvivesTheTypedRoundTrip(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	purchase := func() *orgapi.OrganizationCommerce {
		return &orgapi.OrganizationCommerce{
			PackageSKU:  "plan.m",
			Addons:      []string{"addon.backup", "addon.dedicated-ip"},
			PriceSource: "bss:OpenOva plans@2026-09-11",
			OrderID:     "4f7c2a1e-9b3d-4c5e-8f6a-1d2e3f4a5b6c",
		}
	}
	want := purchase()
	org.Spec.Commerce = purchase() // a separate value, so aliasing cannot fake a pass
	r, _, _ := makeReconciler(t, org)
	r.PerOrgRealmEnabled = true // the finalizer-add path = a typed client.Update

	reconcileTwice(t, r, "acme")

	var got orgapi.Organization
	if err := r.Get(context.Background(), client.ObjectKey{Name: "acme"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Finalizers) == 0 {
		t.Fatalf("precondition: the reconcile did not write the CR back (no finalizer) — this test would prove nothing")
	}
	if got.Spec.Commerce == nil {
		t.Fatalf("spec.commerce was erased by the controller's typed round-trip (#4471 class) — the BSS adapter would read a stripped CR")
	}
	if !reflect.DeepEqual(got.Spec.Commerce, want) {
		t.Errorf("spec.commerce after reconcile = %+v, want %+v", got.Spec.Commerce, want)
	}
	// The purchase is read-only to this controller: planSlug (which SIZES the
	// boundary) is still what the CR said, not derived from the package.
	if got.Spec.PlanSlug != "m" {
		t.Errorf("spec.planSlug = %q, want m (commerce must not rewrite it)", got.Spec.PlanSlug)
	}
}

func TestReconcile_NoSpecCommerceStaysAbsent(t *testing.T) {
	t.Parallel()
	org := sampleOrg() // the sovereign-admin door / a legacy Organization
	r, _, _ := makeReconciler(t, org)
	r.PerOrgRealmEnabled = true

	reconcileTwice(t, r, "acme")

	var got orgapi.Organization
	if err := r.Get(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "acme"}}.NamespacedName, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Spec.Commerce != nil {
		t.Errorf("an Organization without a purchase acquired spec.commerce=%+v from the round-trip, want absent", got.Spec.Commerce)
	}
}
