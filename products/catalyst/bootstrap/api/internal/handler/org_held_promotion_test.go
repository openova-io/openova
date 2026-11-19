package handler

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// An Organization created through POST /api/v1/organizations is held at
// tenant_registered because its boundary is not Ready yet. Only
// ReconcileAllPending ever ran the pipeline again, and nothing in production
// calls it, so on hw307 every such Organization stayed at tenant_registered for
// ever (gate check NET-G07, 19 November 2026). These tests drive the pass that
// does run in production — the tenant-registry pass main.go starts — and so
// fail on a tree where that pass never re-runs a held record.

// TestTenantRegistryPass_PromotesHeldOrganizationOnceBoundaryIsReady fails on
// main: the record stays tenant_registered after the pass.
func TestTenantRegistryPass_PromotesHeldOrganizationOnceBoundaryIsReady(t *testing.T) {
	h, dyn := newOrgPipelineHandlerWithCRs(t)
	ctx := context.Background()
	_, created := postCreateOrg(t, h, `{"subdomain":"heldcorp","admin_email":"admin@heldcorp.test","domain_mode":"free-subdomain"}`)
	if created.State != store.STSTenantRegistered {
		t.Fatalf("precondition: a fresh Organization is held at tenant_registered, got %q", created.State)
	}

	// A pass while the boundary is still provisioning keeps the hold.
	h.reconcileTenantRegistryOnce(ctx)
	if rec, _ := h.orgTenantDeps.Store.Get(created.OrganizationID); rec.State != store.STSTenantRegistered {
		t.Fatalf("boundary not Ready: the record must stay tenant_registered, got %q", rec.State)
	}

	// The organization-controller reports the vCluster Ready.
	cr, err := dyn.Resource(organizationGVR()).Get(ctx, "heldcorp", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get minted CR: %v", err)
	}
	cr.Object["status"] = map[string]any{"vcluster": map[string]any{"phase": "Ready"}}
	if _, err := dyn.Resource(organizationGVR()).Update(ctx, cr, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("stamp Ready: %v", err)
	}

	h.reconcileTenantRegistryOnce(ctx)
	rec, _ := h.orgTenantDeps.Store.Get(created.OrganizationID)
	if rec.State != store.STSDone {
		t.Fatalf("the tenant-registry pass must promote a held Organization whose boundary is Ready to done, got %q (boundary %q)", rec.State, rec.BoundaryPhase)
	}
}

// TestTenantRegistryPass_NeverRecreatesADeletedOrganization: step 7 creates
// the Organization CR when it is missing, so a held record whose CR is gone
// must not be run again — that would bring a deleted Organization back.
func TestTenantRegistryPass_NeverRecreatesADeletedOrganization(t *testing.T) {
	h, dyn := newOrgPipelineHandlerWithCRs(t)
	ctx := context.Background()
	_, created := postCreateOrg(t, h, `{"subdomain":"gonecorp","admin_email":"admin@gonecorp.test","domain_mode":"free-subdomain"}`)
	if created.State != store.STSTenantRegistered {
		t.Fatalf("precondition: held at tenant_registered, got %q", created.State)
	}
	if err := dyn.Resource(organizationGVR()).Delete(ctx, "gonecorp", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete CR: %v", err)
	}

	h.reconcileTenantRegistryOnce(ctx)
	if _, err := dyn.Resource(organizationGVR()).Get(ctx, "gonecorp", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the pass recreated a deleted Organization CR (err=%v)", err)
	}
	if rec, _ := h.orgTenantDeps.Store.Get(created.OrganizationID); rec.State != store.STSTenantRegistered {
		t.Fatalf("a record whose CR is gone stays where it is, got %q", rec.State)
	}
}
