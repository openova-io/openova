// tenant_deleted_emit_5364_test.go — pins the #5364 emit-on-finalizer fix.
//
// Root cause (#5364): Org teardown is SPLIT across two producers.
//   1. The org-controller finalizer prunes the per-Org vCluster Flux +
//      tenant-networking (this file's Reconciler).
//   2. The provisioning service's `tenant.deleted` consumer prunes the
//      `org-tenants` gitops dir (the <slug> Namespace manifest + tenant
//      HelmReleases from the org-tenants Flux Kustomization inventory).
//
// The tenant-service's DELETE /api/organizations/{id} route publishes
// tenant.deleted → half #2 runs. But a raw `kubectl delete organization`
// runs ONLY the org-controller (half #1) and never publishes tenant.deleted,
// so the org-tenants dir survives and the Kustomization perpetually recreates
// the ns + HRs. The fix makes the org-controller finalizer ALSO publish
// tenant.deleted so BOTH halves fire on ANY Org-CR delete.
//
// These tests pin the three behaviors the fix must guarantee:
//   1. The delete branch publishes a tenant.deleted{slug} event via the
//      injected publisher.
//   2. With a nil publisher (no NATS), the teardown still completes without
//      error (degrade to pre-#5364 behavior).
//   3. A publish error does NOT block finalizer removal (the CR still
//      tombstones — a NATS hiccup must never wedge the delete).
package controller

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
	"github.com/openova-io/openova/core/controllers/pkg/natsbus"

	"k8s.io/apimachinery/pkg/types"
)

// fakeTenantEventPublisher records every Publish call so the tests can assert
// the subject + envelope. When err is set, Publish returns it (to exercise the
// best-effort / non-blocking path). Implements controller.TenantEventPublisher.
type fakeTenantEventPublisher struct {
	mu       sync.Mutex
	subjects []string
	events   []*natsbus.Event
	err      error
}

func (f *fakeTenantEventPublisher) Publish(_ context.Context, subject string, ev *natsbus.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subjects = append(f.subjects, subject)
	f.events = append(f.events, ev)
	return f.err
}

func (f *fakeTenantEventPublisher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

// deleteReq is the reconcile request for the sampleOrg (name == slug == "acme").
var deleteReq = ctrl.Request{NamespacedName: types.NamespacedName{Name: "acme"}}

// driveToDeletion brings the sampleOrg to steady state (adds the
// per-org-realm finalizer so the CR is held through deletion), then marks it
// for deletion. The caller runs the delete-path Reconcile.
func driveToDeletion(t *testing.T, r *Reconciler) {
	t.Helper()
	// PerOrgRealmEnabled=true makes the reconciler add the per-org-realm
	// finalizer — the single finalizer that holds the CR long enough for the
	// deletion branch (and our publish) to run. The fakeKeycloak DeleteRealm is
	// a no-op-safe stub.
	r.PerOrgRealmEnabled = true
	reconcileTwice(t, r, "acme")
	var got orgapi.Organization
	if err := r.Get(context.Background(), client.ObjectKey{Name: "acme"}, &got); err != nil {
		t.Fatalf("get steady: %v", err)
	}
	if !containsFinalizer(got.Finalizers, PerOrgRealmFinalizer) {
		t.Fatalf("precondition: per-org-realm finalizer should hold the CR, got %v", got.Finalizers)
	}
	if err := r.Delete(context.Background(), &got); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

// TestReconcile_Delete_PublishesTenantDeleted pins behavior #1: the deletion
// branch publishes a tenant.deleted event on the canonical subject with the
// Org slug in both the tenant_id and the {id, slug} payload.
func TestReconcile_Delete_PublishesTenantDeleted(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	r, _, _ := makeReconciler(t, org)
	fp := &fakeTenantEventPublisher{}
	r.TenantEventPublisher = fp

	driveToDeletion(t, r)

	if fp.count() != 0 {
		t.Fatalf("no publish should occur before the delete-path reconcile, got %d", fp.count())
	}
	if _, err := r.Reconcile(context.Background(), deleteReq); err != nil {
		t.Fatalf("delete reconcile: %v", err)
	}

	if fp.count() == 0 {
		t.Fatalf("delete branch must publish tenant.deleted, got 0 publishes")
	}
	if fp.subjects[0] != natsbus.SubjectTenantDeleted {
		t.Errorf("publish subject = %q, want %q", fp.subjects[0], natsbus.SubjectTenantDeleted)
	}
	ev := fp.events[0]
	if ev.Type != "tenant.deleted" {
		t.Errorf("event type = %q, want tenant.deleted", ev.Type)
	}
	if ev.Source != "organization-controller" {
		t.Errorf("event source = %q, want organization-controller", ev.Source)
	}
	if ev.TenantID != "acme" {
		t.Errorf("event tenant_id = %q, want acme", ev.TenantID)
	}
	var payload struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal(ev.Data, &payload); err != nil {
		t.Fatalf("unmarshal event data: %v", err)
	}
	if payload.Slug != "acme" {
		t.Errorf("payload.slug = %q, want acme (the provisioning consumer keys teardown off slug)", payload.Slug)
	}
	if payload.ID == "" {
		t.Errorf("payload.id must be non-empty (envelope shape parity with tenant-service)")
	}
}

// TestReconcile_Delete_NilPublisher_NoError pins behavior #2: with NO publisher
// wired (Catalyst-Zero / NATS_URL unset), the teardown still completes without
// error and the CR tombstones — degrade to pre-#5364 behavior, never a wedge.
func TestReconcile_Delete_NilPublisher_NoError(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	r, _, _ := makeReconciler(t, org)
	// r.TenantEventPublisher is nil (default).

	driveToDeletion(t, r)

	if _, err := r.Reconcile(context.Background(), deleteReq); err != nil {
		t.Fatalf("delete reconcile with nil publisher must not error: %v", err)
	}
	// The finalizer must have been dropped → the CR is fully deleted.
	var gone orgapi.Organization
	if err := r.Get(context.Background(), client.ObjectKey{Name: "acme"}, &gone); !apierrors.IsNotFound(err) {
		t.Errorf("CR should tombstone after teardown (finalizer dropped), get err = %v (want NotFound)", err)
	}
}

// TestReconcile_Delete_PublishError_DoesNotBlockFinalizer pins behavior #3: a
// publish failure is best-effort — it is logged + swallowed, the reconcile
// returns no error, and the finalizer teardown proceeds so the CR tombstones.
// A NATS hiccup must never strand the CR in Terminating.
func TestReconcile_Delete_PublishError_DoesNotBlockFinalizer(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	r, _, _ := makeReconciler(t, org)
	fp := &fakeTenantEventPublisher{err: errors.New("broker unreachable")}
	r.TenantEventPublisher = fp

	driveToDeletion(t, r)

	if _, err := r.Reconcile(context.Background(), deleteReq); err != nil {
		t.Fatalf("publish error must not fail the reconcile: %v", err)
	}
	if fp.count() == 0 {
		t.Fatalf("the publish must have been ATTEMPTED before the finalizer dropped, got 0")
	}
	// Despite the publish error, the finalizer teardown proceeded → CR gone.
	var gone orgapi.Organization
	if err := r.Get(context.Background(), client.ObjectKey{Name: "acme"}, &gone); !apierrors.IsNotFound(err) {
		t.Errorf("CR should tombstone even when publish fails (finalizer must not wedge), get err = %v (want NotFound)", err)
	}
}
