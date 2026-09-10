package openova

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// UpsertRoleBinding mirrors store.UpsertRoleBinding's contract: normalised,
// validated, idempotent on (email, role, scope, customer).
func (f *fakeRepo) UpsertRoleBinding(_ context.Context, b store.RoleBinding) (store.RoleBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b.SubjectEmail = strings.ToLower(strings.TrimSpace(b.SubjectEmail))
	if b.SubjectEmail == "" || !strings.Contains(b.SubjectEmail, "@") {
		return store.RoleBinding{}, fmt.Errorf("%w: subject_email must be an email address", store.ErrInvalid)
	}
	if !store.ValidRole(b.Role) {
		return store.RoleBinding{}, fmt.Errorf("%w: unknown role %q", store.ErrInvalid, b.Role)
	}
	b.ScopeKind = store.ScopeKindOfRole(b.Role)
	if b.ScopeKind == store.ScopeKindCustomer {
		if b.CustomerID == nil || *b.CustomerID == "" {
			return store.RoleBinding{}, fmt.Errorf("%w: a %s binding needs a customer_id", store.ErrInvalid, b.Role)
		}
		if _, ok := f.customers[*b.CustomerID]; !ok {
			return store.RoleBinding{}, store.ErrNotFound
		}
	} else {
		b.CustomerID = nil
	}
	for _, x := range f.bindings {
		if x.SubjectEmail == b.SubjectEmail && x.Role == b.Role && x.ScopeKind == b.ScopeKind && ptrEq(x.CustomerID, b.CustomerID) {
			return x, nil
		}
	}
	b.ID = f.nextID("bind")
	now := time.Now().UTC()
	b.GrantedAt = &now
	b.Source = store.BindingSourceExplicit
	f.bindings = append(f.bindings, b)
	return b, nil
}

func ptrEq(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// bindingsFor lists the fake's bindings on a customer.
func (f *fakeRepo) bindingsFor(customerID string) []store.RoleBinding {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.RoleBinding
	for _, b := range f.bindings {
		if b.CustomerID != nil && *b.CustomerID == customerID {
			out = append(out, b)
		}
	}
	return out
}
