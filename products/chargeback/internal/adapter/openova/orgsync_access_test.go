package openova

import (
	"context"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The Organization's owner becomes the customer-owner of its customer
// (DESIGN.md §10): granted on the first sync, idempotent on the next, and
// never revoked by the sync — an operator-granted binding survives a resync,
// and so does a previous owner's.
func TestOrgSyncGrantsOwnerBindingAndNeverRevokes(t *testing.T) {
	repo := newFakeRepo()
	s := &OrgSync{Repo: repo, Keys: testKeys(t)}
	ctx := context.Background()

	if err := s.SyncOrganization(ctx, orgUnstructured("acme", nil)); err != nil {
		t.Fatal(err)
	}
	c, err := repo.GetCustomerBySlug(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	got := repo.bindingsFor(c.ID)
	if len(got) != 1 || got[0].Role != store.RoleCustomerOwner || got[0].SubjectEmail != "ceo@acme.example" || got[0].ScopeKind != store.ScopeKindCustomer || got[0].GrantedBy != "org-sync" {
		t.Fatalf("after first sync bindings = %+v, want one customer-owner for ceo@acme.example granted by org-sync", got)
	}

	// A resync grants nothing new.
	if err := s.SyncOrganization(ctx, orgUnstructured("acme", nil)); err != nil {
		t.Fatal(err)
	}
	if got := repo.bindingsFor(c.ID); len(got) != 1 {
		t.Fatalf("resync duplicated the owner binding: %+v", got)
	}

	// The operator grants a viewer; the owner changes on the CR. Both the
	// operator's grant and the previous owner survive; the new owner is added.
	if _, err := repo.UpsertRoleBinding(ctx, store.RoleBinding{SubjectEmail: "auditor@acme.example", Role: store.RoleCustomerViewer, CustomerID: &c.ID, GrantedBy: "ops"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncOrganization(ctx, orgUnstructured("acme", func(spec map[string]any) {
		spec["owners"] = []any{map[string]any{"email": "cfo@acme.example", "role": "owner"}}
	})); err != nil {
		t.Fatal(err)
	}
	got = repo.bindingsFor(c.ID)
	want := map[string]string{"ceo@acme.example": store.RoleCustomerOwner, "auditor@acme.example": store.RoleCustomerViewer, "cfo@acme.example": store.RoleCustomerOwner}
	if len(got) != len(want) {
		t.Fatalf("bindings after owner change = %+v, want %v", got, want)
	}
	for _, b := range got {
		if want[b.SubjectEmail] != b.Role {
			t.Fatalf("binding %s=%s, want %s", b.SubjectEmail, b.Role, want[b.SubjectEmail])
		}
	}
	// And the customer's admin_email followed the CR.
	if c, _ = repo.GetCustomerBySlug(ctx, "acme"); c.AdminEmail != "cfo@acme.example" {
		t.Fatalf("admin_email = %q", c.AdminEmail)
	}
}

// An Organization with no owner on its roster gets a customer and no binding;
// the sync must not invent one for an empty address.
func TestOrgSyncWithoutOwnerGrantsNoBinding(t *testing.T) {
	repo := newFakeRepo()
	s := &OrgSync{Repo: repo, Keys: testKeys(t)}
	ctx := context.Background()
	if err := s.SyncOrganization(ctx, orgUnstructured("ghost", func(spec map[string]any) { spec["owners"] = []any{} })); err != nil {
		t.Fatal(err)
	}
	c, err := repo.GetCustomerBySlug(ctx, "ghost")
	if err != nil {
		t.Fatal(err)
	}
	if got := repo.bindingsFor(c.ID); len(got) != 0 {
		t.Fatalf("bindings for an ownerless Organization = %+v, want none", got)
	}
}

// The Sovereign's own Organization is not a customer and grants nothing.
func TestOrgSyncInternalOrganizationGrantsNoBinding(t *testing.T) {
	repo := newFakeRepo()
	s := &OrgSync{Repo: repo, Keys: testKeys(t)}
	if err := s.SyncOrganization(context.Background(), orgUnstructured("openova", func(spec map[string]any) { spec["kind"] = "internal" })); err != nil {
		t.Fatal(err)
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.bindings) != 0 {
		t.Fatalf("internal Organization granted %+v", repo.bindings)
	}
}
