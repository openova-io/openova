package api

import (
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The six roles against the handlers, decided before any query runs (nil
// store): each refusal is the permission the handler asks for (DESIGN.md
// §10 table). Grants are proven with a database in access_integration_test.go.
func TestAuthorizationRefusalsPerRole(t *testing.T) {
	h := newAuthzHandler()
	a, b := "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	sov := func(role string) *store.Session {
		return &store.Session{Email: role + "@nc.example", Role: role, Roles: []store.RoleBinding{{Role: role, ScopeKind: store.ScopeKindSovereign}}}
	}
	cust := func(role string) *store.Session {
		return &store.Session{Email: role + "@a.example", Role: role, CustomerID: &a, Roles: []store.RoleBinding{{Role: role, ScopeKind: store.ScopeKindCustomer, CustomerID: &a}}}
	}
	billingOp, finance := sov(store.RoleBillingOperator), sov(store.RoleFinanceViewer)
	owner, billing, viewer := cust(store.RoleCustomerOwner), cust(store.RoleCustomerBilling), cust(store.RoleCustomerViewer)

	cases := []struct {
		name   string
		sess   *store.Session
		method string
		path   string
		want   int
		perm   string
	}{
		{"billing-operator edits billing settings", billingOp, "PUT", "/api/v1/billing-settings", 403, "settings.manage"},
		{"billing-operator edits allocation settings", billingOp, "PUT", "/api/v1/allocation/settings", 403, "settings.manage"},
		{"billing-operator grants a role", billingOp, "POST", "/api/v1/access/bindings", 403, "settings.manage"},
		{"billing-operator maps a group", billingOp, "PUT", "/api/v1/access/group-mappings", 403, "settings.manage"},
		{"finance-viewer creates a price book", finance, "POST", "/api/v1/pricebooks", 403, "rating.manage"},
		{"finance-viewer creates a customer", finance, "POST", "/api/v1/customers", 403, "customers.manage"},
		{"finance-viewer runs statements", finance, "POST", "/api/v1/statements/run", 403, "billing.issue"},
		{"finance-viewer records a payment", finance, "POST", "/api/v1/payments", 403, "billing.collect"},
		{"finance-viewer suspends a customer", finance, "POST", "/api/v1/customers/" + a + "/suspend", 403, "billing.collect"},
		{"finance-viewer sets a currency rate", finance, "PUT", "/api/v1/currencies/USD", 403, "rating.manage"},
		{"finance-viewer creates a budget", finance, "POST", "/api/v1/budgets", 403, "customers.manage"},
		{"finance-viewer lists bindings", finance, "GET", "/api/v1/access/bindings", 403, "settings.manage"},
		{"customer-owner records a payment on itself", owner, "POST", "/api/v1/customers/" + a + "/payments", 403, "billing.collect"},
		{"customer-owner applies credit", owner, "POST", "/api/v1/customers/" + a + "/account/apply-credit", 403, "billing.collect"},
		{"customer-owner issues a statement", owner, "POST", "/api/v1/statements/x/issue", 403, "billing.issue"},
		{"customer-owner reads its audit", owner, "GET", "/api/v1/customers/" + a + "/audit", 403, "audit.read"},
		{"customer-owner reads another customer", owner, "GET", "/api/v1/customers/" + b, 404, ""},
		{"customer-owner adds a user elsewhere", owner, "POST", "/api/v1/customers/" + b + "/users", 404, ""},
		{"customer-owner reads the overview", owner, "GET", "/api/v1/overview", 403, "metering.read"},
		{"customer-billing adds a user", billing, "POST", "/api/v1/customers/" + a + "/users", 403, "customer.self.manage"},
		{"customer-billing adds a source", billing, "POST", "/api/v1/customers/" + a + "/sources", 403, "customer.self.manage"},
		{"customer-viewer requests a checkout", viewer, "POST", "/api/v1/customers/" + a + "/payment-intents", 403, "account.topup"},
		{"customer-viewer adds a user", viewer, "POST", "/api/v1/customers/" + a + "/users", 403, "customer.self.manage"},
		{"customer-viewer reads another customer's account", viewer, "GET", "/api/v1/customers/" + b + "/account", 404, ""},
		// Capacity (DESIGN.md §11): Sovereign reads only, capacity.manage to write.
		{"finance-viewer sets a pool total", finance, "PUT", "/api/v1/capacity/pools/x", 403, "capacity.manage"},
		{"finance-viewer creates a region", finance, "POST", "/api/v1/capacity/regions", 403, "capacity.manage"},
		{"finance-viewer writes a footprint", finance, "PUT", "/api/v1/capacity/footprints/ecs.x", 403, "capacity.manage"},
		{"finance-viewer sets a cap", finance, "PUT", "/api/v1/capacity/caps", 403, "capacity.manage"},
		{"customer-owner reads capacity", owner, "GET", "/api/v1/capacity/overview", 403, "metering.read"},
		{"customer-owner lists regions", owner, "GET", "/api/v1/capacity/regions", 403, "metering.read"},
		{"customer-owner sets a pool total", owner, "PUT", "/api/v1/capacity/pools/x", 403, "capacity.manage"},
		{"customer-viewer reads footprints", viewer, "GET", "/api/v1/capacity/footprints", 403, "metering.read"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(t, h, c.sess, c.method, c.path, "{}")
			if rec.Code != c.want {
				t.Fatalf("%s %s = %d (%s), want %d", c.method, c.path, rec.Code, rec.Body.String(), c.want)
			}
			if c.perm != "" && !strings.Contains(rec.Body.String(), c.perm) {
				t.Fatalf("%s %s refusal %q does not name %s", c.method, c.path, rec.Body.String(), c.perm)
			}
		})
	}
}
