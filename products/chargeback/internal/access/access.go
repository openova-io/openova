// Package access is the authorization policy of the chargeback application
// (DESIGN.md §10): two scope kinds, nine permissions, six roles that are
// fixed bundles of permissions, and the one question every handler asks —
// does this session hold permission P at scope S?
//
// It is pure: no database, no HTTP. The store carries the bindings
// (store.RoleBinding) and the API layer turns a refusal into 401/403/404.
package access

import (
	"sort"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Permission is one thing a principal may do.
type Permission string

// The permissions.
const (
	// MeteringRead reads usage, cost, resources, statements, the account —
	// every read surface. At the Sovereign scope it spans all customers.
	MeteringRead Permission = "metering.read"
	// RatingManage edits price books, discounts and currency rates.
	RatingManage Permission = "rating.manage"
	// CustomersManage creates, edits, invites and deletes customers, their
	// sources, budgets and report schedules, for ANY customer.
	CustomersManage Permission = "customers.manage"
	// BillingIssue runs, issues, sends, cancels and deletes statements and
	// issues credit notes.
	BillingIssue Permission = "billing.issue"
	// BillingCollect records and allocates payments on the customer's behalf,
	// applies credit, runs collections and suspends or resumes at the platform.
	BillingCollect Permission = "billing.collect"
	// AccountTopup asks the gateway to collect a top-up (a checkout) for the
	// customer's OWN account.
	AccountTopup Permission = "account.topup"
	// SettingsManage edits the Sovereign-wide settings: billing, allocation,
	// and who holds which role (bindings and group mappings).
	SettingsManage Permission = "settings.manage"
	// AuditRead reads audit trails.
	AuditRead Permission = "audit.read"
	// CustomerSelfManage is the customer-scoped subset of CustomersManage an
	// owner holds on its own customer: its users, its sources' credentials
	// and scope, its PO reference and tax registration.
	CustomerSelfManage Permission = "customer.self.manage"
)

// Permissions lists every permission, in a stable order.
var Permissions = []Permission{MeteringRead, RatingManage, CustomersManage, BillingIssue, BillingCollect, AccountTopup, SettingsManage, AuditRead, CustomerSelfManage}

// Scope kinds, re-exported so callers need only this package.
const (
	ScopeSovereign = store.ScopeKindSovereign
	ScopeCustomer  = store.ScopeKindCustomer
)

// Roles, re-exported.
const (
	RoleSovereignAdmin  = store.RoleSovereignAdmin
	RoleBillingOperator = store.RoleBillingOperator
	RoleFinanceViewer   = store.RoleFinanceViewer
	RoleCustomerOwner   = store.RoleCustomerOwner
	RoleCustomerBilling = store.RoleCustomerBilling
	RoleCustomerViewer  = store.RoleCustomerViewer
)

// Matrix is the fixed permission bundle of each role. It is the whole
// policy; there is no per-user permission and no custom role.
var Matrix = map[string][]Permission{
	RoleSovereignAdmin:  Permissions,
	RoleBillingOperator: {MeteringRead, RatingManage, CustomersManage, BillingIssue, BillingCollect, AuditRead},
	RoleFinanceViewer:   {MeteringRead, AuditRead},
	RoleCustomerOwner:   {MeteringRead, AccountTopup, CustomerSelfManage},
	RoleCustomerBilling: {MeteringRead, AccountTopup},
	RoleCustomerViewer:  {MeteringRead},
}

// implies is the one derivation in the model: the Sovereign-wide
// customers.manage covers what an owner may do on its own customer.
var implies = map[Permission][]Permission{
	CustomersManage: {CustomerSelfManage},
}

// RoleGrants reports whether a role's bundle carries the permission, either
// directly or through implies.
func RoleGrants(role string, perm Permission) bool {
	for _, p := range Matrix[role] {
		if p == perm {
			return true
		}
		for _, q := range implies[p] {
			if q == perm {
				return true
			}
		}
	}
	return false
}

// Describe is the operator-facing one-liner of each role, for the console.
var Describe = map[string]string{
	RoleSovereignAdmin:  "Everything, Sovereign-wide: settings, access, rating, customers, billing.",
	RoleBillingOperator: "Runs billing for every customer: rating, customers, issuing, collecting, audit. No settings or access changes.",
	RoleFinanceViewer:   "Reads and exports everything, Sovereign-wide. Changes nothing.",
	RoleCustomerOwner:   "One customer: reads its costs and invoices, tops up its account, manages its users, PO reference and tax registration.",
	RoleCustomerBilling: "One customer: reads its costs and invoices and tops up its account.",
	RoleCustomerViewer:  "One customer: reads its costs and invoices.",
}

// Rank orders roles by power, lower is more powerful; an unknown role ranks
// last. Used to pick the binding the legacy `role` key describes.
func Rank(role string) int {
	for i, r := range store.Roles {
		if r == role {
			return i
		}
	}
	return len(store.Roles)
}

// LegacyRole is the name an older reader expects for a role: the three that
// existed before bindings map exactly; the new roles are reported as
// themselves, because no legacy name means the same thing.
func LegacyRole(role string) string {
	switch role {
	case RoleSovereignAdmin:
		return store.RoleOperator
	case RoleCustomerOwner:
		return store.RoleCustomerAdmin
	case RoleCustomerViewer:
		return store.RoleCustomerViewer
	}
	return role
}

// FromLegacy turns a pre-binding session (Role + CustomerID) into the one
// binding it stood for, so a Session built the old way — tests, and any
// caller that never learned about Roles — is authorised by the same policy.
func FromLegacy(role string, customerID *string) (store.RoleBinding, bool) {
	switch role {
	case store.RoleOperator, RoleSovereignAdmin:
		return store.RoleBinding{Role: RoleSovereignAdmin, ScopeKind: ScopeSovereign}, true
	case RoleBillingOperator, RoleFinanceViewer:
		return store.RoleBinding{Role: role, ScopeKind: ScopeSovereign}, true
	case store.RoleCustomerAdmin, RoleCustomerOwner:
		if customerID == nil {
			return store.RoleBinding{}, false
		}
		return store.RoleBinding{Role: RoleCustomerOwner, ScopeKind: ScopeCustomer, CustomerID: customerID}, true
	case RoleCustomerBilling, RoleCustomerViewer:
		if customerID == nil {
			return store.RoleBinding{}, false
		}
		return store.RoleBinding{Role: role, ScopeKind: ScopeCustomer, CustomerID: customerID}, true
	}
	return store.RoleBinding{}, false
}

// Bindings is what a session holds: Roles when resolved, otherwise the one
// binding its legacy Role + CustomerID stand for.
func Bindings(s store.Session) []store.RoleBinding {
	if len(s.Roles) > 0 {
		return s.Roles
	}
	if b, ok := FromLegacy(s.Role, s.CustomerID); ok {
		return []store.RoleBinding{b}
	}
	return nil
}

// Primary is the highest-power binding of a set (Sovereign scope wins over
// customer scope at equal role rank, which the rank order already encodes),
// as the legacy `role` + `customer_id` the session reports. ok=false for an
// empty set.
func Primary(bindings []store.RoleBinding) (role string, customerID *string, ok bool) {
	best := -1
	for i, b := range bindings {
		if !store.ValidRole(b.Role) {
			continue
		}
		if best < 0 || Rank(b.Role) < Rank(bindings[best].Role) {
			best = i
		}
	}
	if best < 0 {
		return "", nil, false
	}
	b := bindings[best]
	if b.ScopeKind == ScopeSovereign {
		return LegacyRole(b.Role), nil, true
	}
	return LegacyRole(b.Role), b.CustomerID, true
}

// Has answers the question. A Sovereign-scoped binding that carries the
// permission grants it at the Sovereign AND on every customer; a
// customer-scoped binding grants it on that customer only. customerID ""
// asks at the Sovereign scope.
func Has(bindings []store.RoleBinding, perm Permission, customerID string) bool {
	for _, b := range bindings {
		if !RoleGrants(b.Role, perm) {
			continue
		}
		if b.ScopeKind == ScopeSovereign {
			return true
		}
		if customerID != "" && b.CustomerID != nil && *b.CustomerID == customerID {
			return true
		}
	}
	return false
}

// HasAny reports whether any of the permissions is held at the scope.
func HasAny(bindings []store.RoleBinding, customerID string, perms ...Permission) bool {
	for _, p := range perms {
		if Has(bindings, p, customerID) {
			return true
		}
	}
	return false
}

// IsSovereign reports whether the set holds any Sovereign-scoped binding.
func IsSovereign(bindings []store.RoleBinding) bool {
	for _, b := range bindings {
		if b.ScopeKind == ScopeSovereign && store.ValidRole(b.Role) {
			return true
		}
	}
	return false
}

// OnCustomer reports whether the set holds any binding covering the
// customer: a Sovereign binding, or a customer binding on that id.
func OnCustomer(bindings []store.RoleBinding, customerID string) bool {
	for _, b := range bindings {
		if !store.ValidRole(b.Role) {
			continue
		}
		if b.ScopeKind == ScopeSovereign {
			return true
		}
		if b.CustomerID != nil && *b.CustomerID == customerID {
			return true
		}
	}
	return false
}

// ScopeKey names a scope the way /me reports it: "sovereign" or
// "customer:<id>".
func ScopeKey(scopeKind string, customerID *string) string {
	if scopeKind == ScopeSovereign || customerID == nil {
		return ScopeSovereign
	}
	return ScopeCustomer + ":" + *customerID
}

// Effective is the permission list per scope key the console hides and
// shows by. A Sovereign-scoped permission is listed under "sovereign" only —
// the console knows it covers every customer.
func Effective(bindings []store.RoleBinding) map[string][]Permission {
	sets := map[string]map[Permission]bool{}
	for _, b := range bindings {
		if !store.ValidRole(b.Role) {
			continue
		}
		key := ScopeKey(b.ScopeKind, b.CustomerID)
		if sets[key] == nil {
			sets[key] = map[Permission]bool{}
		}
		for _, p := range Permissions {
			if RoleGrants(b.Role, p) {
				sets[key][p] = true
			}
		}
	}
	out := make(map[string][]Permission, len(sets))
	for key, set := range sets {
		var list []Permission
		for _, p := range Permissions {
			if set[p] {
				list = append(list, p)
			}
		}
		out[key] = list
	}
	return out
}

// Scopes lists the scope keys of a set, "sovereign" first, then customers by id.
func Scopes(bindings []store.RoleBinding) []string {
	seen := map[string]bool{}
	var out []string
	for _, b := range bindings {
		if !store.ValidRole(b.Role) {
			continue
		}
		k := ScopeKey(b.ScopeKind, b.CustomerID)
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i] == ScopeSovereign {
			return true
		}
		if out[j] == ScopeSovereign {
			return false
		}
		return out[i] < out[j]
	})
	return out
}
