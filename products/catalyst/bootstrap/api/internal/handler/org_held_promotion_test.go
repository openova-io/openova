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

// TestPipeline_NeverWritesOverADeleteThatLandedDuringTheRun: the pass reads a
// held record, and a DELETE lands before its run persists. The DELETE wrote
// STSDeleted; the run must not write tenant_registered (or done) over it, or
// the deleted Organization comes back and stays at tenant_registered for ever.
func TestPipeline_NeverWritesOverADeleteThatLandedDuringTheRun(t *testing.T) {
	h, _ := newOrgPipelineHandlerWithCRs(t)
	ctx := context.Background()
	_, created := postCreateOrg(t, h, `{"subdomain":"racecorp","admin_email":"admin@racecorp.test","domain_mode":"free-subdomain"}`)
	held, ok := h.orgTenantDeps.Store.Get(created.OrganizationID)
	if !ok || held.State != store.STSTenantRegistered {
		t.Fatalf("precondition: held at tenant_registered, got %q", held.State)
	}

	if w := deleteOrgViaHandler(t, h, created.OrganizationID); w.Code != 204 {
		t.Fatalf("delete: want 204, got %d (%s)", w.Code, w.Body.String())
	}

	// The run that read the record before the DELETE now persists.
	got := h.runOrganizationPipeline(ctx, held)
	rec, _ := h.orgTenantDeps.Store.Get(created.OrganizationID)
	if rec.State != store.STSDeleted || got.State != store.STSDeleted {
		t.Fatalf("a DELETE during the run must stand: stored %q, returned %q", rec.State, got.State)
	}
}

// TestPipeline_TerminatingCRIsNeverReady: a CR being deleted keeps its last
// status, which may still say Ready. The pipeline must not promote the record
// to done on it.
func TestPipeline_TerminatingCRIsNeverReady(t *testing.T) {
	h, dyn := newOrgPipelineHandlerWithCRs(t)
	ctx := context.Background()
	_, created := postCreateOrg(t, h, `{"subdomain":"leavingcorp","admin_email":"admin@leavingcorp.test","domain_mode":"free-subdomain"}`)
	held, _ := h.orgTenantDeps.Store.Get(created.OrganizationID)

	cr, err := dyn.Resource(organizationGVR()).Get(ctx, "leavingcorp", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get minted CR: %v", err)
	}
	now := metav1.Now()
	cr.SetFinalizers([]string{"orgs.openova.io/tenant-networking"})
	cr.SetDeletionTimestamp(&now)
	cr.Object["status"] = map[string]any{"vcluster": map[string]any{"phase": "Ready"}}
	if _, err := dyn.Resource(organizationGVR()).Update(ctx, cr, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("stamp terminating Ready: %v", err)
	}

	got := h.runOrganizationPipeline(ctx, held)
	if got.State != store.STSTenantRegistered || got.BoundaryPhase != "Deleting" {
		t.Fatalf("a terminating CR is not a Ready boundary: state %q, boundary %q", got.State, got.BoundaryPhase)
	}
}
