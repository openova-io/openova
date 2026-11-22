// provisioning_postconditions_every_region_test.go — Ready means every region.
//
// The Organization's boundary (namespace, vCluster HelmRelease, LimitRange,
// ResourceQuotas) is delivered to each region by that region's OWN Flux from
// its OWN Gitea. verifyProvisioned used to read it back through r.Client only,
// the controller's own region, and only the console listener pair per region:
// an Organization with no boundary at all in region B read Ready=True. And an
// Organization with no pool domain was never read in region B at all.
package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openova-io/openova/core/controllers/organization/internal/gitops"
)

// convergedTwoRegionFixture is a 2-region Sovereign on which every artifact,
// listeners included, is present in both regions: verifyProvisioned is
// complete until a test removes one piece of region B's boundary.
func convergedTwoRegionFixture(t *testing.T) multiRegionFixture {
	t.Helper()
	f := newMultiRegionFixture(t, true, true, regionGatewayListeners(9443, 9080))
	for _, c := range []client.Client{f.r.Client, f.region} {
		if _, err := f.r.ensureConsoleOrgListener(context.Background(), c, f.names); err != nil {
			t.Fatalf("converge listeners: %v", err)
		}
		syncGatewayStatus(t, c)
	}
	if got := f.r.verifyProvisioned(context.Background(), f.org); !got.complete() {
		t.Fatalf("control: a converged 2-region Organization reads NOT provisioned. missing=%v unverifiable=%v", got.Missing, got.Unverifiable)
	}
	return f
}

func deleteFromRegion(t *testing.T, c client.Client, obj client.Object) {
	t.Helper()
	if err := c.Delete(context.Background(), obj); err != nil {
		t.Fatalf("delete %T %s: %v", obj, obj.GetName(), err)
	}
}

func regionHR(slug string) *unstructured.Unstructured {
	hr := &unstructured.Unstructured{}
	hr.SetGroupVersionKind(helmReleaseGVK)
	hr.SetNamespace(slug)
	hr.SetName("vcluster")
	return hr
}

func assertNotProvisionedNaming(t *testing.T, got provisioningPostconditions, want ...string) {
	t.Helper()
	if got.complete() {
		t.Fatalf("the Organization reads fully provisioned (Ready=True) while region %s has no boundary for it — a verdict from one region applied to two. unverifiable=%v",
			secondaryRegionKey, got.Unverifiable)
	}
	msg := got.message()
	for _, w := range append([]string{secondaryRegionKey}, want...) {
		if !strings.Contains(msg, w) {
			t.Errorf("the NOT-provisioned message does not name %q; an operator cannot act on it.\ngot: %s", w, msg)
		}
	}
}

// Region B's Flux never applied ./vcluster: no vCluster HelmRelease there.
func TestVerifyProvisioned_RegionBWithoutVClusterIsNotProvisioned(t *testing.T) {
	f := convergedTwoRegionFixture(t)
	deleteFromRegion(t, f.region, regionHR(f.org.Spec.Slug))
	assertNotProvisionedNaming(t, f.r.verifyProvisioned(context.Background(), f.org), f.org.Spec.Slug+"/vcluster")
}

// Region B's vCluster HelmRelease exists but failed there.
func TestVerifyProvisioned_RegionBVClusterNotReadyIsNotProvisioned(t *testing.T) {
	f := convergedTwoRegionFixture(t)
	hr := regionHR(f.org.Spec.Slug)
	if err := f.region.Get(context.Background(), client.ObjectKeyFromObject(hr), hr); err != nil {
		t.Fatalf("read region HR: %v", err)
	}
	hr.Object["status"] = map[string]any{"conditions": []any{
		map[string]any{"type": "Ready", "status": "False", "reason": "InstallFailed"},
	}}
	if err := f.region.Update(context.Background(), hr); err != nil {
		t.Fatalf("fail region HR: %v", err)
	}
	assertNotProvisionedNaming(t, f.r.verifyProvisioned(context.Background(), f.org), "not Ready")
}

// Region B carries the vCluster but not the quota the customer pays for.
func TestVerifyProvisioned_RegionBWithoutQuotaIsNotProvisioned(t *testing.T) {
	f := convergedTwoRegionFixture(t)
	if !gitops.PlanRendersResourceQuota(f.org.Spec.PlanSlug) {
		t.Skipf("plan %q renders no ResourceQuota", f.org.Spec.PlanSlug)
	}
	rq := &corev1.ResourceQuota{}
	rq.SetNamespace(f.org.Spec.Slug)
	rq.SetName(gitops.BoundaryResourceQuotaName)
	deleteFromRegion(t, f.region, rq)
	assertNotProvisionedNaming(t, f.r.verifyProvisioned(context.Background(), f.org), "ResourceQuota "+f.org.Spec.Slug+"/"+gitops.BoundaryResourceQuotaName)
}

// An Organization with no pool domain authors no console listener, so the
// region fan-out was never consulted for it at all.
func TestVerifyProvisioned_NoPoolDomainOrgIsReadInEveryRegion(t *testing.T) {
	f := convergedTwoRegionFixture(t)
	f.org.Spec.TenantPublic.ParentDomain = ""
	if _, ok := orgConsoleTLSNamesForOrg(f.org); ok {
		t.Fatalf("fixture: the Organization still engages the console up-path")
	}
	if got := f.r.verifyProvisioned(context.Background(), f.org); !got.complete() {
		t.Fatalf("control: with the boundary in both regions it reads NOT provisioned. missing=%v", got.Missing)
	}
	deleteFromRegion(t, f.region, regionHR(f.org.Spec.Slug))
	assertNotProvisionedNaming(t, f.r.verifyProvisioned(context.Background(), f.org), f.org.Spec.Slug+"/vcluster")
}

// A meshed region with no credential: the boundary is unproven there too,
// including for an Organization with no pool domain.
func TestVerifyProvisioned_UnwiredRegionHoldsBoundaryBack(t *testing.T) {
	f := newMultiRegionFixture(t, true, false, regionGatewayListeners(9443, 9080))
	f.org.Spec.TenantPublic.ParentDomain = ""
	got := f.r.verifyProvisioned(context.Background(), f.org)
	if got.complete() {
		t.Fatalf("an Organization reads fully provisioned while a meshed region has no credential to read its boundary. missing=%v", got.Missing)
	}
	if !strings.Contains(got.message(), "boundary of "+f.org.Spec.Slug) {
		t.Errorf("the message does not name the unproven boundary.\ngot: %s", got.message())
	}

	// CONTROL — a single-region Sovereign stays clean.
	single := newMultiRegionFixture(t, false, false, regionGatewayListeners(9443, 9080))
	single.org.Spec.TenantPublic.ParentDomain = ""
	if sgot := single.r.verifyProvisioned(context.Background(), single.org); !sgot.complete() {
		t.Fatalf("control: a single-region Organization reads NOT provisioned. missing=%v", sgot.Missing)
	}
}
