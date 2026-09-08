package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// gateJSON sends a JSON body with the gate identity header (any method).
func gateJSON(t *testing.T, h http.Handler, method, path, email string, v any) (int, map[string]any) {
	t.Helper()
	var body *bytes.Reader
	if v != nil {
		b, _ := json.Marshal(v)
		body = bytes.NewReader(b)
	} else {
		body = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, path, body)
	r.Header.Set("Content-Type", "application/json")
	if email != "" {
		r.Header.Set("X-Forwarded-Email", email)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// resetBillingSettingsAPI restores the default rule when the test ends; the
// settings row is configuration and survives testdb's per-test wipe.
func resetBillingSettingsAPI(t *testing.T, st *store.Store) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := st.UpdateBillingSettings(context.Background(), store.DefaultBillingSettings()); err != nil {
			t.Errorf("reset billing settings: %v", err)
		}
	})
}

// GET/PUT /billing-settings: default, every rule, validation with the
// accepted values named, operator-only, audited (DESIGN.md §2.11).
func TestBillingSettingsRoundTripValidationAndAudit(t *testing.T) {
	h, st := setupGateAPI(t, "X-Forwarded-Email")
	resetBillingSettingsAPI(t, st)
	seedCRUD(t, st)

	code, body := getJSON(t, h, "/api/v1/billing-settings", opEmail)
	if code != 200 || body["discount_rule"] != store.DiscountRuleMostSpecific || body["updated_at"] == nil {
		t.Fatalf("default settings: %d %+v", code, body)
	}
	for _, rule := range store.DiscountRules {
		code, body := putJSON(t, h, "/api/v1/billing-settings", opEmail, map[string]any{"discount_rule": rule})
		if code != 200 || body["discount_rule"] != rule {
			t.Fatalf("put %s: %d %+v", rule, code, body)
		}
		if _, body := getJSON(t, h, "/api/v1/billing-settings", opEmail); body["discount_rule"] != rule {
			t.Fatalf("read back after %s: %+v", rule, body)
		}
	}
	// Unknown rule, empty rule, unknown field: 400 with a message; the
	// unknown-rule message lists what is accepted.
	code, body = putJSON(t, h, "/api/v1/billing-settings", opEmail, map[string]any{"discount_rule": "average"})
	if code != 400 || !strings.Contains(body["error"].(string), store.DiscountRuleMostSpecific) || !strings.Contains(body["error"].(string), store.DiscountRuleCompound) {
		t.Fatalf("unknown rule: %d %+v — the 400 must name the accepted rules", code, body)
	}
	for _, bad := range []map[string]any{{}, {"discount_rule": ""}, {"colour": "blue"}, {"discount_rule": "stack", "extra": 1}} {
		if code, body := putJSON(t, h, "/api/v1/billing-settings", opEmail, bad); code != 400 || body["error"] == "" {
			t.Fatalf("%+v: %d %+v, want 400", bad, code, body)
		}
	}
	// A rejected update changed nothing (the last accepted rule was compound).
	if _, body := getJSON(t, h, "/api/v1/billing-settings", opEmail); body["discount_rule"] != store.DiscountRuleCompound {
		t.Fatalf("settings after rejected updates = %+v", body)
	}
	// Operator-only, both verbs; unauthenticated is 401.
	if code, _ := getJSON(t, h, "/api/v1/billing-settings", acmeAdmin); code != 403 {
		t.Fatalf("customer read = %d, want 403", code)
	}
	if code, _ := putJSON(t, h, "/api/v1/billing-settings", acmeAdmin, map[string]any{"discount_rule": "stack"}); code != 403 {
		t.Fatalf("customer write = %d, want 403", code)
	}
	if code, _ := getJSON(t, h, "/api/v1/billing-settings", ""); code != 401 {
		t.Fatalf("unauthenticated read = %d, want 401", code)
	}
	// Audited once per accepted change, with the previous value.
	var n int
	if err := st.DB().QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'billing.settings' AND actor = $1 AND details->>'previous' IS NOT NULL`, opEmail).Scan(&n); err != nil || n != len(store.DiscountRules) {
		t.Fatalf("audit entries = %d err=%v, want %d", n, err, len(store.DiscountRules))
	}
}

// Discounts accept and echo `stackable`; PATCH flips it alone; a statement
// run records the rule and the frozen breakdown marks the superseded
// discount, all over the wire.
func TestDiscountStackableAndStatementCarriesTheRule(t *testing.T) {
	h, st := setupGateAPI(t, "X-Forwarded-Email")
	resetBillingSettingsAPI(t, st)
	seed := seedCRUD(t, st)

	code, global := gateJSON(t, h, "POST", "/api/v1/discounts", opEmail, map[string]any{"name": "Everyone 30%", "kind": "percent", "value": 30})
	if code != 201 || global["stackable"] != false {
		t.Fatalf("global discount: %d %+v — stackable must default to false and be on the wire", code, global)
	}
	code, ecs := gateJSON(t, h, "POST", "/api/v1/customers/"+seed.acme.ID+"/discounts", opEmail, map[string]any{"name": "ECS 20%", "kind": "percent", "value": 20, "sku": "ecs.m7n.xlarge.8", "stackable": true})
	if code != 201 || ecs["stackable"] != true {
		t.Fatalf("stackable discount: %d %+v", code, ecs)
	}
	ecsID, globalID := ecs["id"].(string), global["id"].(string)

	// PATCH one flag at a time.
	if code, body := gateJSON(t, h, "PATCH", "/api/v1/discounts/"+ecsID, opEmail, map[string]any{"stackable": false}); code != 200 || body["stackable"] != false {
		t.Fatalf("patch stackable: %d %+v", code, body)
	}
	if _, body := getJSON(t, h, "/api/v1/discounts/"+ecsID, opEmail); body["stackable"] != false || body["active"] != true || body["name"] != "ECS 20%" {
		t.Fatalf("patch touched other fields or did not stick: %+v", body)
	}
	for _, bad := range []map[string]any{{}, {"active": true, "stackable": true}, {"stackable": "yes"}} {
		if code, _ := gateJSON(t, h, "PATCH", "/api/v1/discounts/"+ecsID, opEmail, bad); code != 400 {
			t.Fatalf("patch %+v = %d, want 400", bad, code)
		}
	}
	if code, _ := gateJSON(t, h, "PATCH", "/api/v1/discounts/"+ecsID, acmeAdmin, map[string]any{"stackable": true}); code != 403 {
		t.Fatalf("customer flipped stackable: %d", code)
	}
	if auditActions(t, st, "discount.stackable") != 1 {
		t.Fatal("no audit entry for discount.stackable")
	}
	// PUT replaces every field: the flag rides along.
	if code, body := putJSON(t, h, "/api/v1/discounts/"+ecsID, opEmail, map[string]any{"customer_id": seed.acme.ID, "name": "ECS 20%", "kind": "percent", "value": 20, "sku": "ecs.m7n.xlarge.8", "stackable": true}); code != 200 || body["stackable"] != true {
		t.Fatalf("put with stackable: %d %+v", code, body)
	}
	if code, body := putJSON(t, h, "/api/v1/discounts/"+ecsID, opEmail, map[string]any{"customer_id": seed.acme.ID, "name": "ECS 20%", "kind": "percent", "value": 20, "sku": "ecs.m7n.xlarge.8"}); code != 200 || body["stackable"] != false {
		t.Fatalf("put without stackable must clear it: %d %+v", code, body)
	}

	// Acme in 2026-08: 100 h × 0.5 = 50 on the one ECS SKU. Under highest
	// the global 30 % (15) wins and the SKU discount is superseded.
	putJSON(t, h, "/api/v1/billing-settings", opEmail, map[string]any{"discount_rule": store.DiscountRuleHighest})
	code, run := gateJSON(t, h, "POST", "/api/v1/statements/run", opEmail, map[string]any{"period": "2026-08", "customer_id": seed.acme.ID})
	if code != 200 {
		t.Fatalf("run: %d %+v", code, run)
	}
	stmtID := run["results"].([]any)[0].(map[string]any)["statement_id"].(string)
	code, stmt := getJSON(t, h, "/api/v1/statements/"+stmtID, opEmail)
	if code != 200 || stmt["discount_rule"] != store.DiscountRuleHighest || stmt["discount_total"].(float64) != 15 {
		t.Fatalf("statement under highest: %d rule=%v total=%v", code, stmt["discount_rule"], stmt["discount_total"])
	}
	detail := map[string]map[string]any{}
	for _, d := range stmt["discount_detail"].([]any) {
		m := d.(map[string]any)
		detail[m["discount_id"].(string)] = m
	}
	if detail[globalID]["amount"].(float64) != 15 || detail[ecsID]["superseded_by"] != globalID || detail[ecsID]["amount"].(float64) != 0 {
		t.Fatalf("highest detail = %+v", detail)
	}
	// The list carries the rule too.
	if _, list := getJSON(t, h, "/api/v1/statements?period=2026-08", opEmail); list["statements"].([]any)[0].(map[string]any)["discount_rule"] != store.DiscountRuleHighest {
		t.Fatalf("list = %+v", list)
	}

	// Switch to most-specific and re-run: the SKU discount (10) wins, the
	// global one is the superseded entry, and the statement says so.
	putJSON(t, h, "/api/v1/billing-settings", opEmail, map[string]any{"discount_rule": store.DiscountRuleMostSpecific})
	gateJSON(t, h, "POST", "/api/v1/statements/run", opEmail, map[string]any{"period": "2026-08", "customer_id": seed.acme.ID})
	_, stmt = getJSON(t, h, "/api/v1/statements/"+stmtID, opEmail)
	if stmt["discount_rule"] != store.DiscountRuleMostSpecific || stmt["discount_total"].(float64) != 10 {
		t.Fatalf("statement under most-specific: rule=%v total=%v", stmt["discount_rule"], stmt["discount_total"])
	}
	detail = map[string]map[string]any{}
	for _, d := range stmt["discount_detail"].([]any) {
		m := d.(map[string]any)
		detail[m["discount_id"].(string)] = m
	}
	if detail[ecsID]["amount"].(float64) != 10 || detail[globalID]["superseded_by"] != ecsID {
		t.Fatalf("most-specific detail = %+v", detail)
	}
	// A customer reads its own statement with the rule, and cannot see the
	// settings endpoint.
	if code, mine := getJSON(t, h, "/api/v1/statements/"+stmtID, acmeAdmin); code != 200 || mine["discount_rule"] != store.DiscountRuleMostSpecific {
		t.Fatalf("customer statement: %d %+v", code, mine["discount_rule"])
	}
}
