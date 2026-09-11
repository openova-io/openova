// Package access is the authorization policy of the chargeback application
// (DESIGN.md §10 and §Partners): three scope kinds, eleven permissions,
// eight roles that are fixed bundles of permissions, and the one question
// every handler asks — does this session hold permission P at scope S?
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
	// PartnersManage creates and edits partners, tiers and their discounts,
	// assigns customers to partners and sets any partner's retail rule
	// (DESIGN.md §Partners). A Sovereign permission.
	PartnersManage Permission = "partners.manage"
	// PartnerSelfManage is what a partner owner holds on its own partner:
	// its retail rule and its users.
	PartnerSelfManage Permission = "partner.self.manage"
)

// Permissions lists every permission, in a stable order.
var Permissions = []Permission{MeteringRead, RatingManage, CustomersManage, BillingIssue, BillingCollect, AccountTopup, SettingsManage, AuditRead, CustomerSelfManage, PartnersManage, PartnerSelfManage}

// Scope kinds, re-exported so callers need only this package.
const (
	ScopeSovereign = store.ScopeKindSovereign
	ScopeCustomer  = store.ScopeKindCustomer
	ScopePartner   = store.ScopeKindPartner
)

// Roles, re-exported.
const (
	RoleSovereignAdmin  = store.RoleSovereignAdmin
	RoleBillingOperator = store.RoleBillingOperator
	RoleFinanceViewer   = store.RoleFinanceViewer
	RoleCustomerOwner   = store.RoleCustomerOwner
	RoleCustomerBilling = store.RoleCustomerBilling
	RoleCustomerViewer  = store.RoleCustomerViewer
	RolePartnerOwner    = store.RolePartnerOwner
	RolePartnerViewer   = store.RolePartnerViewer
)

// Matrix is the fixed permission bundle of each role. It is the whole
// policy; there is no per-user permission and no custom role.
var Matrix = map[string][]Permission{
	RoleSovereignAdmin:  Permissions,
	RoleBillingOperator: {MeteringRead, RatingManage, CustomersManage, BillingIssue, BillingCollect, AuditRead, PartnersManage},
	RoleFinanceViewer:   {MeteringRead, AuditRead},
	RolePartnerOwner:    {MeteringRead, AccountTopup, PartnerSelfManage},
	RolePartnerViewer:   {MeteringRead},
	RoleCustomerOwner:   {MeteringRead, AccountTopup, CustomerSelfManage},
	RoleCustomerBilling: {MeteringRead, AccountTopup},
	RoleCustomerViewer:  {MeteringRead},
}

// implies is the derivation in the model: the Sovereign-wide
// customers.manage covers what an owner may do on its own customer, and the
// Sovereign-wide partners.manage what a partner owner may do on its own
// partner.
var implies = map[Permission][]Permission{
	CustomersManage: {CustomerSelfManage},
	PartnersManage:  {PartnerSelfManage},
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
	RoleSovereignAdmin:  "Everything, Sovereign-wide: settings, access, rating, customers, partners, billing.",
	RoleBillingOperator: "Runs billing for every customer and partner: rating, customers, partners, issuing, collecting, audit. No settings or access changes.",
	RoleFinanceViewer:   "Reads and exports everything, Sovereign-wide. Changes nothing.",
	RolePartnerOwner:    "One partner: reads its customers' costs and statements and its own account and margin, edits its retail rule, manages its users, tops up its account.",
	RolePartnerViewer:   "One partner: reads its customers' costs and statements and its own account and margin.",
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
// A partner role has no legacy form: it never existed before bindings.
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
// empty set. A partner binding reports the partner's PARTY as its customer,
// so a reader that takes one customer lands on the partner's own account.
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
	switch b.ScopeKind {
	case ScopeSovereign:
		return LegacyRole(b.Role), nil, true
	case ScopePartner:
		if len(b.Customers) > 0 {
			party := b.Customers[0]
			return LegacyRole(b.Role), &party, true
		}
		return LegacyRole(b.Role), nil, true
	}
	return LegacyRole(b.Role), b.CustomerID, true
}

// covers reports whether a binding reaches a customer: a Sovereign binding
// reaches every one, a customer binding its own, a partner binding the
// customers it expanded to (the partner's customers and its party).
func covers(b store.RoleBinding, customerID string) bool {
	switch b.ScopeKind {
	case ScopeSovereign:
		return true
	case ScopePartner:
		for _, id := range b.Customers {
			if id == customerID {
				return true
			}
		}
		return false
	}
	return customerID != "" && b.CustomerID != nil && *b.CustomerID == customerID
}

// Has answers the question. A Sovereign-scoped binding that carries the
// permission grants it at the Sovereign AND on every customer; a
// customer-scoped binding grants it on that customer only; a partner-scoped
// binding on the customers its partner expands to. customerID "" asks at
// the Sovereign scope, which only a Sovereign binding answers.
func Has(bindings []store.RoleBinding, perm Permission, customerID string) bool {
	for _, b := range bindings {
		if !RoleGrants(b.Role, perm) {
			continue
		}
		if b.ScopeKind == ScopeSovereign {
			return true
		}
		if customerID != "" && covers(b, customerID) {
			return true
		}
	}
	return false
}

// HasPartner asks at a PARTNER scope: a Sovereign binding with the
// permission, or a partner binding on that partner with it.
func HasPartner(bindings []store.RoleBinding, perm Permission, partnerID string) bool {
	for _, b := range bindings {
		if !RoleGrants(b.Role, perm) {
			continue
		}
		if b.ScopeKind == ScopeSovereign {
			return true
		}
		if partnerID != "" && b.ScopeKind == ScopePartner && b.PartnerID != nil && *b.PartnerID == partnerID {
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

// IsPartner reports whether the set holds a partner-scoped binding and no
// Sovereign one — the partner lens (DESIGN.md §Partners).
func IsPartner(bindings []store.RoleBinding) bool {
	if IsSovereign(bindings) {
		return false
	}
	for _, b := range bindings {
		if b.ScopeKind == ScopePartner && store.ValidRole(b.Role) {
			return true
		}
	}
	return false
}

// PartnerIDs lists the partners the set is bound to, in binding order.
func PartnerIDs(bindings []store.RoleBinding) []string {
	seen := map[string]bool{}
	var out []string
	for _, b := range bindings {
		if b.ScopeKind == ScopePartner && b.PartnerID != nil && store.ValidRole(b.Role) && !seen[*b.PartnerID] {
			seen[*b.PartnerID] = true
			out = append(out, *b.PartnerID)
		}
	}
	return out
}

// OnCustomer reports whether the set holds any binding covering the
// customer: a Sovereign binding, a customer binding on that id, or a
// partner binding whose partner the customer belongs to.
func OnCustomer(bindings []store.RoleBinding, customerID string) bool {
	for _, b := range bindings {
		if !store.ValidRole(b.Role) {
			continue
		}
		if covers(b, customerID) {
			return true
		}
	}
	return false
}

// OnPartner reports whether the set holds any binding covering the partner:
// a Sovereign binding, or a partner binding on that id.
func OnPartner(bindings []store.RoleBinding, partnerID string) bool {
	for _, b := range bindings {
		if !store.ValidRole(b.Role) {
			continue
		}
		if b.ScopeKind == ScopeSovereign {
			return true
		}
		if b.ScopeKind == ScopePartner && b.PartnerID != nil && *b.PartnerID == partnerID {
			return true
		}
	}
	return false
}

// ScopeKey names a scope the way /me reports it: "sovereign",
// "customer:<id>" or "partner:<id>".
func ScopeKey(scopeKind string, customerID *string) string {
	if scopeKind == ScopeSovereign || customerID == nil {
		return ScopeSovereign
	}
	return ScopeCustomer + ":" + *customerID
}

// BindingScopeKey is ScopeKey for a whole binding, partner scopes included.
func BindingScopeKey(b store.RoleBinding) string {
	if b.ScopeKind == ScopePartner && b.PartnerID != nil {
		return ScopePartner + ":" + *b.PartnerID
	}
	return ScopeKey(b.ScopeKind, b.CustomerID)
}

// Effective is the permission list per scope key the console hides and
// shows by. A Sovereign-scoped permission is listed under "sovereign" only —
// the console knows it covers every customer; a partner-scoped one under
// "partner:<id>" — the console knows it covers the partner's customers.
func Effective(bindings []store.RoleBinding) map[string][]Permission {
	sets := map[string]map[Permission]bool{}
	for _, b := range bindings {
		if !store.ValidRole(b.Role) {
			continue
		}
		key := BindingScopeKey(b)
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

// Scopes lists the scope keys of a set, "sovereign" first, then partners,
// then customers by id.
func Scopes(bindings []store.RoleBinding) []string {
	seen := map[string]bool{}
	var out []string
	for _, b := range bindings {
		if !store.ValidRole(b.Role) {
			continue
		}
		k := BindingScopeKey(b)
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	rank := func(k string) int {
		switch {
		case k == ScopeSovereign:
			return 0
		case len(k) > len(ScopePartner) && k[:len(ScopePartner)+1] == ScopePartner+":":
			return 1
		}
		return 2
	}
	sort.Slice(out, func(i, j int) bool {
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		return out[i] < out[j]
	})
	return out
}
