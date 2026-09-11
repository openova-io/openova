package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/access"
	"github.com/openova-io/openova/products/chargeback/internal/config"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The access model end to end (DESIGN.md §10): who gets in, with which
// bindings, and what each binding lets them do.

func strs(v any) []string {
	var out []string
	if list, ok := v.([]any); ok {
		for _, x := range list {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func permsOf(me map[string]any, scope string) []string {
	p, _ := me["permissions"].(map[string]any)
	return strs(p[scope])
}

// notRefused fails the test when a status is an authorization refusal. Used
// where the happy path's own outcome depends on wiring the test does not
// set up (a payment gateway) but the authorization decision is the point.
func notRefused(t *testing.T, what string, rec *httptest.ResponseRecorder) {
	t.Helper()
	switch rec.Code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		t.Fatalf("%s was refused with %d: %s", what, rec.Code, rec.Body.String())
	}
}

// An OPERATOR_EMAILS address holds an implicit sovereign-admin binding: it
// needs no row, and /me says so — the legacy role, the binding with its
// source, every permission at the Sovereign, and the scope list.
func TestIntegrationAccessOperatorEmailImplicitBindingAndMeShape(t *testing.T) {
	h, _, mail, _, _ := setupAPI(t)
	op := &client{t: t, h: h}
	me := op.signIn(opEmail, mail)

	if me["role"] != store.RoleOperator {
		t.Fatalf("legacy role = %v, want operator", me["role"])
	}
	roles, _ := me["roles"].([]any)
	if len(roles) != 1 {
		t.Fatalf("roles = %v, want the one implicit binding", me["roles"])
	}
	b := roles[0].(map[string]any)
	if b["role"] != store.RoleSovereignAdmin || b["scope_kind"] != store.ScopeKindSovereign || b["source"] != store.BindingSourceConfig {
		t.Fatalf("implicit binding = %v", b)
	}
	sov := permsOf(me, "sovereign")
	if len(sov) != len(access.Permissions) || !has(sov, "settings.manage") || !has(sov, "customer.self.manage") || !has(sov, "partners.manage") {
		t.Fatalf("sovereign permissions = %v", sov)
	}
	if got := strs(me["scopes"]); len(got) != 1 || got[0] != "sovereign" {
		t.Fatalf("scopes = %v", me["scopes"])
	}
	for _, k := range []string{"email", "role", "roles", "permissions", "scopes", "expires_at", "profile", "version"} {
		if _, ok := me[k]; !ok {
			t.Fatalf("/me lacks %q", k)
		}
	}
	// The same document at /api/v1/me.
	if alias := op.must("GET", "/api/v1/me", 200); alias["email"] != opEmail || alias["role"] != store.RoleOperator {
		t.Fatalf("/api/v1/me = %v", alias)
	}
	// The Access page sees the implicit binding, marked as such.
	out := op.must("GET", "/api/v1/access/bindings", 200)
	implicit, _ := out["implicit"].([]any)
	if len(implicit) != 1 || implicit[0].(map[string]any)["subject_email"] != opEmail {
		t.Fatalf("implicit bindings = %v", out["implicit"])
	}
	if explicit, _ := out["bindings"].([]any); len(explicit) != 0 {
		t.Fatalf("explicit bindings = %v, want none", out["bindings"])
	}
	// The roles document names every role with its permissions.
	roleDoc := op.must("GET", "/api/v1/access/roles", 200)
	if list, _ := roleDoc["roles"].([]any); len(list) != len(store.Roles) {
		t.Fatalf("roles document = %v", roleDoc)
	}
}

// doGate issues one request as the SSO gate would: identity and groups on
// their trusted headers, no cookie.
func doGate(t *testing.T, h http.Handler, method, path, email, groups string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if email != "" {
		req.Header.Set("X-Forwarded-Email", email)
	}
	if groups != "" {
		req.Header.Set("X-Forwarded-Groups", groups)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec, out
}

// A directory group mapped to finance-viewer lets its members read
// everything and change nothing; the same identity without the group, or
// with an unmapped group, is not admitted at all.
func TestIntegrationAccessGroupMappingGrantsFinanceViewerReadOnly(t *testing.T) {
	h, _ := setupGateAPI(t, "X-Forwarded-Email")

	// The operator maps the group.
	rec, out := doGate(t, h, "PUT", "/api/v1/access/group-mappings", opEmail, "", map[string]any{
		"mappings": []map[string]any{{"group_name": "finance", "role": store.RoleFinanceViewer}},
	})
	if rec.Code != 200 {
		t.Fatalf("PUT group-mappings = %d: %s", rec.Code, rec.Body.String())
	}
	if ms, _ := out["mappings"].([]any); len(ms) != 1 || ms[0].(map[string]any)["scope_kind"] != store.ScopeKindSovereign {
		t.Fatalf("mappings = %v", out["mappings"])
	}
	if out["groups_header"] != "X-Forwarded-Groups" {
		t.Fatalf("groups_header = %v", out["groups_header"])
	}

	fin := "fin@nc.example"
	// Reads.
	for _, path := range []string{"/api/v1/overview", "/api/v1/customers", "/api/v1/cost/summary", "/api/v1/statements", "/api/v1/billing-settings"} {
		if rec, _ := doGate(t, h, "GET", path, fin, "finance", nil); rec.Code != 200 {
			t.Fatalf("finance-viewer GET %s = %d: %s", path, rec.Code, rec.Body.String())
		}
	}
	// /me tells where the binding came from.
	_, me := doGate(t, h, "GET", "/api/v1/me", fin, "finance", nil)
	if me["role"] != store.RoleFinanceViewer {
		t.Fatalf("role = %v", me["role"])
	}
	roles, _ := me["roles"].([]any)
	if len(roles) != 1 || roles[0].(map[string]any)["source"] != "group:finance" {
		t.Fatalf("roles = %v", me["roles"])
	}
	if sov := permsOf(me, "sovereign"); !has(sov, "metering.read") || has(sov, "billing.issue") || has(sov, "settings.manage") {
		t.Fatalf("finance-viewer permissions = %v", sov)
	}
	// Writes: every one a 403 that names the permission.
	writes := []struct {
		method, path string
		body         any
		perm         string
	}{
		{"POST", "/api/v1/pricebooks", map[string]any{"name": "x"}, "rating.manage"},
		{"PUT", "/api/v1/billing-settings", map[string]any{}, "settings.manage"},
		{"POST", "/api/v1/customers", map[string]any{"slug": "x", "name": "x", "admin_email": "x@x.example"}, "customers.manage"},
		{"POST", "/api/v1/statements/run", map[string]any{"period": "2026-08"}, "billing.issue"},
		{"POST", "/api/v1/collections/run", nil, "billing.collect"},
		{"GET", "/api/v1/access/bindings", nil, "settings.manage"},
		{"PUT", "/api/v1/access/group-mappings", map[string]any{"mappings": []any{}}, "settings.manage"},
	}
	for _, wr := range writes {
		rec, _ := doGate(t, h, wr.method, wr.path, fin, "finance", wr.body)
		if rec.Code != 403 || !strings.Contains(rec.Body.String(), wr.perm) {
			t.Fatalf("finance-viewer %s %s = %d %s, want 403 naming %s", wr.method, wr.path, rec.Code, rec.Body.String(), wr.perm)
		}
	}
	// Without the group, or with an unmapped one, the identity holds nothing.
	if rec, _ := doGate(t, h, "GET", "/api/v1/customers", fin, "", nil); rec.Code != 401 {
		t.Fatalf("no groups header = %d, want 401", rec.Code)
	}
	if rec, _ := doGate(t, h, "GET", "/api/v1/customers", fin, "sales, marketing", nil); rec.Code != 401 {
		t.Fatalf("unmapped groups = %d, want 401", rec.Code)
	}
	// Replacing the set with an empty one revokes the group's access at once.
	if rec, _ := doGate(t, h, "PUT", "/api/v1/access/group-mappings", opEmail, "", map[string]any{"mappings": []any{}}); rec.Code != 200 {
		t.Fatalf("clear mappings = %d", rec.Code)
	}
	if rec, _ := doGate(t, h, "GET", "/api/v1/customers", fin, "finance", nil); rec.Code != 401 {
		t.Fatalf("after clearing the mapping = %d, want 401", rec.Code)
	}
}

// The groups header is inert when the identity header is not configured:
// nobody can grant themselves a role by naming a group.
func TestIntegrationAccessGroupsHeaderInertWithoutTheGate(t *testing.T) {
	h, st := setupGateAPI(t, "")
	if _, err := st.ReplaceGroupRoleMappings(t.Context(), []store.GroupRoleMapping{{GroupName: "finance", Role: store.RoleSovereignAdmin}}); err != nil {
		t.Fatal(err)
	}
	if rec, _ := doGate(t, h, "GET", "/api/v1/customers", "anyone@example.com", "finance", nil); rec.Code != 401 {
		t.Fatalf("groups header honoured without a trusted identity header: %d", rec.Code)
	}
}

// A customer-owner reads and tops up its own account, manages its own users
// and its PO reference, and nothing more; a customer-viewer only reads;
// neither sees another customer.
func TestIntegrationAccessCustomerOwnerBillingAndViewer(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)
	a := op.mustJSON("POST", "/api/v1/customers", map[string]any{"slug": "acme", "name": "Acme", "admin_email": "owner@acme.example"}, 201)["id"].(string)
	b := op.mustJSON("POST", "/api/v1/customers", map[string]any{"slug": "bravo", "name": "Bravo", "admin_email": "owner@bravo.example"}, 201)["id"].(string)

	owner := &client{t: t, h: h}
	me := owner.signIn("owner@acme.example", mail)
	if me["role"] != store.RoleCustomerAdmin || me["customer_id"] != a {
		t.Fatalf("owner /me = %v", me)
	}
	roles, _ := me["roles"].([]any)
	if len(roles) != 1 || roles[0].(map[string]any)["role"] != store.RoleCustomerOwner || roles[0].(map[string]any)["customer_id"] != a {
		t.Fatalf("owner roles = %v", me["roles"])
	}
	own := permsOf(me, "customer:"+a)
	if !has(own, "account.topup") || !has(own, "customer.self.manage") || has(own, "billing.collect") || has(own, "customers.manage") {
		t.Fatalf("owner permissions = %v", own)
	}
	if _, ok := me["permissions"].(map[string]any)["sovereign"]; ok {
		t.Fatal("a customer-owner must hold nothing at the Sovereign")
	}

	// Reads its own account; another customer is 404, not 403.
	owner.must("GET", "/api/v1/customers/"+a, 200)
	owner.must("GET", "/api/v1/customers/"+a+"/account", 200)
	owner.must("GET", "/api/v1/customers/"+b, 404)
	owner.must("GET", "/api/v1/customers/"+b+"/account", 404)
	if list, _ := owner.must("GET", "/api/v1/customers", 200)["customers"].([]any); len(list) != 1 {
		t.Fatalf("owner sees %d customers, want its own only", len(list))
	}

	// Tops up its OWN account (a checkout through the gateway seam): the
	// authorization passes; whether a gateway is wired is not this test's
	// question. Another customer's account is 404.
	rec, _ := owner.json("POST", "/api/v1/customers/"+a+"/payment-intents", map[string]any{"purpose": "checkout", "amount": "10"})
	notRefused(t, "owner checkout on own account", rec)
	if rec, _ := owner.json("POST", "/api/v1/customers/"+b+"/payment-intents", map[string]any{"purpose": "checkout", "amount": "10"}); rec.Code != 404 {
		t.Fatalf("owner checkout on another customer = %d, want 404", rec.Code)
	}
	// But it cannot RECORD money, suspend itself, or read the audit trail:
	// those are the operator's billing.collect / audit.read.
	for _, wr := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/v1/customers/" + a + "/payments", map[string]any{"amount": "10"}},
		{"POST", "/api/v1/customers/" + a + "/account/apply-credit", map[string]any{}},
		{"POST", "/api/v1/customers/" + a + "/suspend", map[string]any{"reason": "x"}},
		{"POST", "/api/v1/customers/" + a + "/resume", map[string]any{}},
		{"GET", "/api/v1/customers/" + a + "/audit", nil},
		{"POST", "/api/v1/customers/" + a + "/discounts", map[string]any{"name": "x", "kind": "percent", "value": "10"}},
		{"POST", "/api/v1/statements/run", map[string]any{"period": "2026-08", "customer_id": a}},
	} {
		var rec *httptest.ResponseRecorder
		if wr.body != nil {
			rec, _ = owner.json(wr.method, wr.path, wr.body)
		} else {
			rec, _ = owner.do(wr.method, wr.path, "", nil)
		}
		if rec.Code != 403 {
			t.Fatalf("owner %s %s = %d %s, want 403", wr.method, wr.path, rec.Code, rec.Body.String())
		}
	}

	// Sets its own PO reference and tax registration; nothing else.
	if c := owner.mustJSON("PATCH", "/api/v1/customers/"+a, map[string]any{"po_reference": "PO-2026-1", "tax_registration_number": "OM123"}, 200); c["po_reference"] != "PO-2026-1" {
		t.Fatalf("owner PO patch = %v", c["po_reference"])
	}
	if rec, _ := owner.json("PATCH", "/api/v1/customers/"+a, map[string]any{"name": "Acme Renamed"}); rec.Code != 403 || !strings.Contains(rec.Body.String(), "customers.manage") {
		t.Fatalf("owner renaming itself = %d %s, want 403 naming customers.manage", rec.Code, rec.Body.String())
	}
	if rec, _ := owner.json("PATCH", "/api/v1/customers/"+a, map[string]any{"status": "active"}); rec.Code != 403 {
		t.Fatalf("owner activating itself = %d, want 403", rec.Code)
	}

	// Manages its own users, in either vocabulary.
	u := owner.mustJSON("POST", "/api/v1/customers/"+a+"/users", map[string]any{"email": "reader@acme.example", "role": "customer-viewer"}, 201)
	if u["role"] != "viewer" || u["binding_role"] != store.RoleCustomerViewer {
		t.Fatalf("added viewer = %v", u)
	}
	u = owner.mustJSON("POST", "/api/v1/customers/"+a+"/users", map[string]any{"email": "pay@acme.example", "role": store.RoleCustomerBilling}, 201)
	if u["role"] != "viewer" || u["binding_role"] != store.RoleCustomerBilling {
		t.Fatalf("added billing user = %v", u)
	}
	u = owner.mustJSON("POST", "/api/v1/customers/"+a+"/users", map[string]any{"email": "second@acme.example", "role": "admin"}, 201)
	if u["role"] != "admin" || u["binding_role"] != store.RoleCustomerOwner {
		t.Fatalf("added legacy admin = %v", u)
	}
	if rec, _ := owner.json("POST", "/api/v1/customers/"+a+"/users", map[string]any{"email": "x@acme.example", "role": "sovereign-admin"}); rec.Code != 400 {
		t.Fatalf("a Sovereign role through the users endpoint = %d, want 400", rec.Code)
	}
	users, _ := owner.must("GET", "/api/v1/customers/"+a+"/users", 200)["users"].([]any)
	if len(users) != 4 {
		t.Fatalf("users = %v, want owner + 3", users)
	}
	if rec, _ := owner.json("POST", "/api/v1/customers/"+b+"/users", map[string]any{"email": "x@acme.example", "role": "viewer"}); rec.Code != 404 {
		t.Fatalf("owner adding a user to another customer = %d, want 404", rec.Code)
	}
	// customer_users still answers an older SQL reader, as a view.
	var n int
	if err := st.DB().QueryRow(`SELECT count(*) FROM customer_users WHERE customer_id = $1`, a).Scan(&n); err != nil || n != 4 {
		t.Fatalf("customer_users view count = %d err=%v", n, err)
	}

	// The billing user tops up but manages nobody.
	pay := &client{t: t, h: h}
	pme := pay.signIn("pay@acme.example", mail)
	if pme["role"] != store.RoleCustomerBilling {
		t.Fatalf("billing user role = %v", pme["role"])
	}
	rec, _ = pay.json("POST", "/api/v1/customers/"+a+"/payment-intents", map[string]any{"purpose": "checkout", "amount": "10"})
	notRefused(t, "billing user checkout", rec)
	if rec, _ := pay.json("POST", "/api/v1/customers/"+a+"/users", map[string]any{"email": "x@acme.example", "role": "viewer"}); rec.Code != 403 {
		t.Fatalf("billing user adding a user = %d, want 403", rec.Code)
	}

	// The viewer reads and does nothing else.
	viewer := &client{t: t, h: h}
	vme := viewer.signIn("reader@acme.example", mail)
	if vme["role"] != store.RoleCustomerViewer {
		t.Fatalf("viewer role = %v", vme["role"])
	}
	viewer.must("GET", "/api/v1/customers/"+a+"/account", 200)
	viewer.must("GET", "/api/v1/customers/"+a+"/statements", 200)
	if rec, _ := viewer.json("POST", "/api/v1/customers/"+a+"/payment-intents", map[string]any{"purpose": "checkout", "amount": "10"}); rec.Code != 403 || !strings.Contains(rec.Body.String(), "account.topup") {
		t.Fatalf("viewer top-up = %d %s, want 403 naming account.topup", rec.Code, rec.Body.String())
	}
	if rec, _ := viewer.json("POST", "/api/v1/customers/"+a+"/users", map[string]any{"email": "x@acme.example", "role": "viewer"}); rec.Code != 403 {
		t.Fatalf("viewer adding a user = %d, want 403", rec.Code)
	}
	if rec, _ := viewer.json("PATCH", "/api/v1/customers/"+a, map[string]any{"po_reference": "PO"}); rec.Code != 403 {
		t.Fatalf("viewer patching = %d, want 403", rec.Code)
	}

	// Revocation takes effect at the viewer's next request: the cookie is
	// still there, the binding is not.
	owner.must("DELETE", "/api/v1/customers/"+a+"/users/reader%40acme.example", 200)
	viewer.must("GET", "/api/v1/auth/me", 401)
	// And every change left an audit trail on the customer.
	entries, _ := op.must("GET", "/api/v1/customers/"+a+"/audit", 200)["entries"].([]any)
	grants, revokes := 0, 0
	for _, e := range entries {
		m := e.(map[string]any)
		if m["action"] != "access.binding" {
			continue
		}
		d, _ := m["details"].(map[string]any)
		switch d["op"] {
		case "grant":
			grants++
		case "revoke":
			revokes++
		}
	}
	if grants < 3 || revokes != 1 {
		t.Fatalf("access.binding audit: %d grants, %d revokes", grants, revokes)
	}
}

// The bindings API: a billing-operator granted by the operator runs billing
// but cannot touch settings or access; revoking the binding ends the session
// at once and stops the PIN mail.
func TestIntegrationAccessBindingsAPIBillingOperator(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)

	b := op.mustJSON("POST", "/api/v1/access/bindings", map[string]any{"subject_email": "BO@nc.example", "role": store.RoleBillingOperator}, 201)
	if b["subject_email"] != "bo@nc.example" || b["scope_kind"] != store.ScopeKindSovereign || b["granted_by"] != opEmail {
		t.Fatalf("binding = %v", b)
	}
	// Idempotent: the same grant is 200 with the same row.
	if again := op.mustJSON("POST", "/api/v1/access/bindings", map[string]any{"subject_email": "bo@nc.example", "role": store.RoleBillingOperator}, 200); again["id"] != b["id"] {
		t.Fatalf("re-grant = %v", again)
	}
	// A customer role needs a customer; a Sovereign role refuses one; an
	// unknown role and an unknown customer are refused.
	for _, bad := range []map[string]any{
		{"subject_email": "x@nc.example", "role": store.RoleCustomerOwner},
		{"subject_email": "x@nc.example", "role": store.RoleSovereignAdmin, "customer_id": "11111111-1111-1111-1111-111111111111"},
		{"subject_email": "x@nc.example", "role": "root"},
		{"subject_email": "not-an-email", "role": store.RoleFinanceViewer},
	} {
		if rec, _ := op.json("POST", "/api/v1/access/bindings", bad); rec.Code != 400 && rec.Code != 404 {
			t.Fatalf("binding %v = %d %s, want 400/404", bad, rec.Code, rec.Body.String())
		}
	}
	out := op.must("GET", "/api/v1/access/bindings", 200)
	if list, _ := out["bindings"].([]any); len(list) != 1 {
		t.Fatalf("bindings = %v", out["bindings"])
	}
	if list, _ := op.must("GET", "/api/v1/access/bindings?email=bo@nc.example", 200)["bindings"].([]any); len(list) != 1 {
		t.Fatal("filter by email")
	}

	bo := &client{t: t, h: h}
	bme := bo.signIn("bo@nc.example", mail)
	if bme["role"] != store.RoleBillingOperator {
		t.Fatalf("billing-operator role = %v", bme["role"])
	}
	if sov := permsOf(bme, "sovereign"); !has(sov, "billing.issue") || !has(sov, "rating.manage") || has(sov, "settings.manage") || has(sov, "account.topup") {
		t.Fatalf("billing-operator permissions = %v", sov)
	}
	bo.must("GET", "/api/v1/overview", 200)
	bo.mustJSON("POST", "/api/v1/pricebooks", map[string]any{"name": "BO list", "scope": "cloud", "annual_divisor": 8760, "bill_stopped": "storage-only"}, 201)
	c := bo.mustJSON("POST", "/api/v1/customers", map[string]any{"slug": "delta", "name": "Delta", "admin_email": "owner@delta.example"}, 201)
	// customers.manage covers a customer's users too.
	bo.mustJSON("POST", "/api/v1/customers/"+c["id"].(string)+"/users", map[string]any{"email": "v@delta.example", "role": "viewer"}, 201)
	for _, wr := range []struct {
		method, path string
		body         any
		perm         string
	}{
		{"PUT", "/api/v1/billing-settings", map[string]any{}, "settings.manage"},
		{"PUT", "/api/v1/allocation/settings", map[string]any{}, "settings.manage"},
		{"GET", "/api/v1/access/bindings", nil, "settings.manage"},
		{"POST", "/api/v1/access/bindings", map[string]any{"subject_email": "me@nc.example", "role": store.RoleSovereignAdmin}, "settings.manage"},
	} {
		var rec *httptest.ResponseRecorder
		if wr.body != nil {
			rec, _ = bo.json(wr.method, wr.path, wr.body)
		} else {
			rec, _ = bo.do(wr.method, wr.path, "", nil)
		}
		if rec.Code != 403 || !strings.Contains(rec.Body.String(), wr.perm) {
			t.Fatalf("billing-operator %s %s = %d %s, want 403 naming %s", wr.method, wr.path, rec.Code, rec.Body.String(), wr.perm)
		}
	}

	// Revoke: the live cookie session is over at its next request, and the
	// email is unknown to the PIN flow again (202, no mail).
	op.must("DELETE", "/api/v1/access/bindings/"+b["id"].(string), 200)
	bo.must("GET", "/api/v1/auth/me", 401)
	before := len(mail.msgs)
	// The throttle window from the sign-in above still covers this address;
	// clear it so the request reaches the "known principal" check.
	if _, err := st.DB().Exec(`DELETE FROM pins WHERE email = 'bo@nc.example'`); err != nil {
		t.Fatal(err)
	}
	op.mustJSON("POST", "/api/v1/auth/pin/request", map[string]string{"email": "bo@nc.example"}, 202)
	if len(mail.msgs) != before {
		t.Fatal("a revoked email was still sent a PIN")
	}
	// Both changes are in the audit log.
	var n int
	if err := st.DB().QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'access.binding' AND customer_id IS NULL`).Scan(&n); err != nil || n < 2 {
		t.Fatalf("access.binding audit rows = %d err=%v", n, err)
	}
}

// The last sovereign-admin cannot be revoked when nothing in the
// configuration backs it up; with OPERATOR_EMAILS set it can.
func TestIntegrationAccessLastSovereignAdminGuard(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)
	b := op.mustJSON("POST", "/api/v1/access/bindings", map[string]any{"subject_email": "root@nc.example", "role": store.RoleSovereignAdmin}, 201)
	// OPERATOR_EMAILS is set in this harness, so the explicit one may go.
	op.must("DELETE", "/api/v1/access/bindings/"+b["id"].(string), 200)

	// Without OPERATOR_EMAILS, the guard holds.
	bare := New(Deps{Store: st, Mail: mail, Config: h2cfg(), Version: "test"})
	root := &client{t: t, h: bare}
	sess, err := st.CreateSession(t.Context(), "root@nc.example", store.RoleSovereignAdmin, nil, sessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRoleBinding(t.Context(), store.RoleBinding{SubjectEmail: "root@nc.example", Role: store.RoleSovereignAdmin}); err != nil {
		t.Fatal(err)
	}
	root.cookies = []*http.Cookie{{Name: sessionCookie, Value: sess.Token}}
	only, _ := root.must("GET", "/api/v1/access/bindings", 200)["bindings"].([]any)
	if len(only) != 1 {
		t.Fatalf("bindings = %v", only)
	}
	id := only[0].(map[string]any)["id"].(string)
	if rec, _ := root.do("DELETE", "/api/v1/access/bindings/"+id, "", nil); rec.Code != 409 {
		t.Fatalf("revoking the last sovereign-admin = %d %s, want 409", rec.Code, rec.Body.String())
	}
}

// h2cfg is the harness configuration without OPERATOR_EMAILS.
func h2cfg() config.Config {
	return config.Config{PublicURL: "https://billing.t99.omani.works", Profile: "operator-central"}
}
