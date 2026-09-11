package access

import (
	"reflect"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

func ptr(s string) *string { return &s }

// TestMatrixEveryRoleEveryPermission pins the whole policy: every role ×
// every permission, at both scope kinds. A change to Matrix must change this
// table on purpose.
func TestMatrixEveryRoleEveryPermission(t *testing.T) {
	want := map[string]map[Permission]bool{
		RoleSovereignAdmin:  {MeteringRead: true, RatingManage: true, CustomersManage: true, BillingIssue: true, BillingCollect: true, AccountTopup: true, SettingsManage: true, AuditRead: true, CustomerSelfManage: true, PartnersManage: true, PartnerSelfManage: true},
		RoleBillingOperator: {MeteringRead: true, RatingManage: true, CustomersManage: true, BillingIssue: true, BillingCollect: true, AuditRead: true, CustomerSelfManage: true, PartnersManage: true, PartnerSelfManage: true},
		RoleFinanceViewer:   {MeteringRead: true, AuditRead: true},
		RolePartnerOwner:    {MeteringRead: true, AccountTopup: true, PartnerSelfManage: true},
		RolePartnerViewer:   {MeteringRead: true},
		RoleCustomerOwner:   {MeteringRead: true, AccountTopup: true, CustomerSelfManage: true},
		RoleCustomerBilling: {MeteringRead: true, AccountTopup: true},
		RoleCustomerViewer:  {MeteringRead: true},
	}
	if len(want) != len(store.Roles) {
		t.Fatalf("table covers %d roles, store.Roles has %d", len(want), len(store.Roles))
	}
	for _, role := range store.Roles {
		for _, perm := range Permissions {
			if got := RoleGrants(role, perm); got != want[role][perm] {
				t.Errorf("%s × %s = %v, want %v", role, perm, got, want[role][perm])
			}
		}
	}
}

// TestScopesSovereignCoversEveryCustomerCustomerCoversOnlyItsOwn is the scope
// rule: a Sovereign binding answers at the Sovereign and on any customer; a
// customer binding answers on its customer only and never at the Sovereign.
func TestScopesSovereignCoversEveryCustomerCustomerCoversOnlyItsOwn(t *testing.T) {
	a, b := "aaaaaaaa-0000-0000-0000-000000000001", "bbbbbbbb-0000-0000-0000-000000000002"
	for _, role := range []string{RoleSovereignAdmin, RoleBillingOperator, RoleFinanceViewer} {
		bs := []store.RoleBinding{{Role: role, ScopeKind: ScopeSovereign}}
		for _, perm := range Matrix[role] {
			if !Has(bs, perm, "") || !Has(bs, perm, a) || !Has(bs, perm, b) {
				t.Errorf("%s: %s must hold at the Sovereign and on every customer", role, perm)
			}
		}
	}
	for _, role := range []string{RoleCustomerOwner, RoleCustomerBilling, RoleCustomerViewer} {
		bs := []store.RoleBinding{{Role: role, ScopeKind: ScopeCustomer, CustomerID: ptr(a)}}
		for _, perm := range Matrix[role] {
			if !Has(bs, perm, a) {
				t.Errorf("%s: %s must hold on its own customer", role, perm)
			}
			if Has(bs, perm, b) {
				t.Errorf("%s: %s must not hold on another customer", role, perm)
			}
			if Has(bs, perm, "") {
				t.Errorf("%s: %s must not hold at the Sovereign", role, perm)
			}
		}
	}
}

// TestDiscriminatingCasePerRole names the one decision that tells each role
// from its neighbours.
func TestDiscriminatingCasePerRole(t *testing.T) {
	a, b := "aaaaaaaa-0000-0000-0000-000000000001", "bbbbbbbb-0000-0000-0000-000000000002"
	p := "pppppppp-0000-0000-0000-000000000003"
	sov := func(role string) []store.RoleBinding {
		return []store.RoleBinding{{Role: role, ScopeKind: ScopeSovereign}}
	}
	cust := func(role string) []store.RoleBinding {
		return []store.RoleBinding{{Role: role, ScopeKind: ScopeCustomer, CustomerID: ptr(a)}}
	}
	// A partner binding carries its EXPANSION: the customers of the partner
	// plus the partner's own party (DESIGN.md §Partners).
	partner := func(role, customer string) []store.RoleBinding {
		return []store.RoleBinding{{Role: role, ScopeKind: ScopePartner, PartnerID: ptr(p), Customers: []string{customer}}}
	}
	cases := []struct {
		name     string
		bindings []store.RoleBinding
		perm     Permission
		customer string
		want     bool
	}{
		{"sovereign-admin edits access", sov(RoleSovereignAdmin), SettingsManage, "", true},
		{"billing-operator cannot edit access", sov(RoleBillingOperator), SettingsManage, "", false},
		{"billing-operator issues", sov(RoleBillingOperator), BillingIssue, "", true},
		{"billing-operator manages any customer's users through customers.manage", sov(RoleBillingOperator), CustomerSelfManage, a, true},
		{"finance-viewer reads", sov(RoleFinanceViewer), MeteringRead, a, true},
		{"finance-viewer cannot issue", sov(RoleFinanceViewer), BillingIssue, "", false},
		{"finance-viewer cannot change a rate", sov(RoleFinanceViewer), RatingManage, "", false},
		{"customer-owner manages own users", cust(RoleCustomerOwner), CustomerSelfManage, a, true},
		{"customer-owner tops up", cust(RoleCustomerOwner), AccountTopup, a, true},
		{"customer-owner cannot record a payment on its own behalf", cust(RoleCustomerOwner), BillingCollect, a, false},
		{"customer-owner cannot manage customers Sovereign-wide", cust(RoleCustomerOwner), CustomersManage, a, false},
		{"customer-billing tops up", cust(RoleCustomerBilling), AccountTopup, a, true},
		{"customer-billing cannot manage users", cust(RoleCustomerBilling), CustomerSelfManage, a, false},
		{"customer-viewer reads", cust(RoleCustomerViewer), MeteringRead, a, true},
		{"customer-viewer cannot top up", cust(RoleCustomerViewer), AccountTopup, a, false},
		{"partner-owner reads its customer", partner(RolePartnerOwner, a), MeteringRead, a, true},
		{"partner-owner cannot read another partner's customer", partner(RolePartnerOwner, a), MeteringRead, b, false},
		{"partner-owner cannot manage partners Sovereign-wide", partner(RolePartnerOwner, a), PartnersManage, a, false},
		{"partner-owner cannot change a rate", partner(RolePartnerOwner, a), RatingManage, a, false},
		{"partner-owner cannot issue", partner(RolePartnerOwner, a), BillingIssue, a, false},
		{"partner-viewer reads", partner(RolePartnerViewer, a), MeteringRead, a, true},
		{"partner-viewer cannot edit the retail rule", partner(RolePartnerViewer, a), PartnerSelfManage, a, false},
		{"partner-viewer cannot top up", partner(RolePartnerViewer, a), AccountTopup, a, false},
		{"billing-operator manages any partner's rule through partners.manage", sov(RoleBillingOperator), PartnerSelfManage, a, true},
		{"no bindings hold nothing", nil, MeteringRead, a, false},
		{"an unknown role holds nothing", []store.RoleBinding{{Role: "root", ScopeKind: ScopeSovereign}}, MeteringRead, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Has(c.bindings, c.perm, c.customer); got != c.want {
				t.Fatalf("Has(%s, %q) = %v, want %v", c.perm, c.customer, got, c.want)
			}
		})
	}
}

func TestPrimaryPicksTheHighestPowerBindingInLegacyVocabulary(t *testing.T) {
	a, b := "aaaaaaaa-0000-0000-0000-000000000001", "bbbbbbbb-0000-0000-0000-000000000002"
	cases := []struct {
		name     string
		bindings []store.RoleBinding
		role     string
		customer *string
		ok       bool
	}{
		{"none", nil, "", nil, false},
		{"viewer then owner on two customers → owner's customer", []store.RoleBinding{
			{Role: RoleCustomerViewer, ScopeKind: ScopeCustomer, CustomerID: ptr(b)},
			{Role: RoleCustomerOwner, ScopeKind: ScopeCustomer, CustomerID: ptr(a)},
		}, store.RoleCustomerAdmin, ptr(a), true},
		{"finance-viewer beats a customer-owner", []store.RoleBinding{
			{Role: RoleCustomerOwner, ScopeKind: ScopeCustomer, CustomerID: ptr(a)},
			{Role: RoleFinanceViewer, ScopeKind: ScopeSovereign},
		}, RoleFinanceViewer, nil, true},
		{"sovereign-admin reads as operator", []store.RoleBinding{{Role: RoleSovereignAdmin, ScopeKind: ScopeSovereign}}, store.RoleOperator, nil, true},
		{"customer-billing has no legacy name", []store.RoleBinding{{Role: RoleCustomerBilling, ScopeKind: ScopeCustomer, CustomerID: ptr(a)}}, RoleCustomerBilling, ptr(a), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			role, cust, ok := Primary(c.bindings)
			if ok != c.ok || role != c.role || !reflect.DeepEqual(cust, c.customer) {
				t.Fatalf("Primary = (%q, %v, %v), want (%q, %v, %v)", role, deref(cust), ok, c.role, deref(c.customer), c.ok)
			}
		})
	}
}

func deref(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// A session built the old way (Role + CustomerID, no Roles) is authorised by
// the same policy, so nothing that constructs sessions that way regresses.
func TestBindingsFromLegacySession(t *testing.T) {
	a := "aaaaaaaa-0000-0000-0000-000000000001"
	op := store.Session{Role: store.RoleOperator}
	if !Has(Bindings(op), SettingsManage, "") {
		t.Fatal("legacy operator session lost settings.manage")
	}
	adm := store.Session{Role: store.RoleCustomerAdmin, CustomerID: &a}
	if !Has(Bindings(adm), CustomerSelfManage, a) || Has(Bindings(adm), CustomersManage, "") {
		t.Fatal("legacy customer-admin session maps wrong")
	}
	view := store.Session{Role: store.RoleCustomerViewer, CustomerID: &a}
	if Has(Bindings(view), AccountTopup, a) || !Has(Bindings(view), MeteringRead, a) {
		t.Fatal("legacy customer-viewer session maps wrong")
	}
	orphan := store.Session{Role: store.RoleCustomerViewer}
	if len(Bindings(orphan)) != 0 {
		t.Fatal("a customer role without a customer must bind nothing")
	}
	// Resolved Roles win over the legacy pair when both are present.
	both := store.Session{Role: store.RoleOperator, Roles: []store.RoleBinding{{Role: RoleFinanceViewer, ScopeKind: ScopeSovereign}}}
	if Has(Bindings(both), SettingsManage, "") {
		t.Fatal("resolved bindings must take precedence over the legacy role")
	}
}

func TestEffectiveAndScopes(t *testing.T) {
	a := "aaaaaaaa-0000-0000-0000-000000000001"
	bs := []store.RoleBinding{
		{Role: RoleCustomerOwner, ScopeKind: ScopeCustomer, CustomerID: ptr(a)},
		{Role: RoleFinanceViewer, ScopeKind: ScopeSovereign},
		{Role: RoleCustomerViewer, ScopeKind: ScopeCustomer, CustomerID: ptr(a)}, // duplicate coverage, must not duplicate output
	}
	eff := Effective(bs)
	if got := eff[ScopeSovereign]; !reflect.DeepEqual(got, []Permission{MeteringRead, AuditRead}) {
		t.Fatalf("sovereign permissions = %v", got)
	}
	if got := eff["customer:"+a]; !reflect.DeepEqual(got, []Permission{MeteringRead, AccountTopup, CustomerSelfManage}) {
		t.Fatalf("customer permissions = %v", got)
	}
	if got := Scopes(bs); !reflect.DeepEqual(got, []string{ScopeSovereign, "customer:" + a}) {
		t.Fatalf("scopes = %v", got)
	}
	if !IsSovereign(bs) || !OnCustomer(bs, "other") {
		t.Fatal("a Sovereign binding covers every customer")
	}
	only := []store.RoleBinding{{Role: RoleCustomerViewer, ScopeKind: ScopeCustomer, CustomerID: ptr(a)}}
	if IsSovereign(only) || OnCustomer(only, "other") || !OnCustomer(only, a) {
		t.Fatal("a customer binding covers its customer only")
	}
}

// Every role's scope kind is fixed, and the store agrees with the policy.
func TestEveryRoleHasExactlyOneScopeKind(t *testing.T) {
	for _, role := range store.Roles {
		kind := store.ScopeKindOfRole(role)
		if kind != ScopeSovereign && kind != ScopeCustomer && kind != ScopePartner {
			t.Fatalf("%s has no scope kind", role)
		}
		if _, ok := Describe[role]; !ok {
			t.Fatalf("%s has no description", role)
		}
	}
	if store.ScopeKindOfRole("root") != "" || store.ValidRole("root") {
		t.Fatal("an unknown role must be invalid")
	}
}
