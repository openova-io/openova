package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// What the contract did, over the wire (DESIGN.md §15.10): two periods rated
// under one agreement — August under the floor with a true-up, July above it
// without one — read back newest first with the consumption of every line
// and the discount the spend commitment applied; the empty document before
// any run; and the permission table, which is the contract's own.
func TestIntegrationContractPeriodsAPI(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	op, customerID, _ := seedContractFixture(t, h, st, mail)
	ctx := context.Background()

	contractID := op.mustJSON("POST", "/api/v1/contracts", map[string]any{
		"customer_id": customerID, "name": "ACME 2026", "starts_on": "2026-01-01", "currency": "OMR", "status": "active",
	}, 201)["id"].(string)
	items := "/api/v1/contracts/" + contractID + "/items"
	op.mustJSON("POST", items, map[string]any{"kind": "commitment", "sku": "ecs.m7n.2xlarge.8", "unit": "instance-hour", "quantity": "1500", "committed_price": "0.30"}, 201)
	op.mustJSON("POST", items, map[string]any{"kind": "allowance", "sku": "eip.traffic_gb", "unit": "gb", "quantity": "100"}, 201)
	spend := op.mustJSON("POST", items, map[string]any{"kind": "spend", "amount": "600", "discount_pct": "50"}, 201)
	periods := "/api/v1/contracts/" + contractID + "/periods"

	// ── before any run: the empty document, not an error ───────────────
	empty := op.must("GET", periods, 200)
	if rows, _ := empty["periods"].([]any); len(rows) != 0 || empty["contract_id"] != contractID || empty["floor"] != 600.0 {
		t.Fatalf("before any run = %v, want no periods under the 600 floor", empty)
	}

	// ── July: 5,000 h above the committed head, above the floor ────────
	src, err := st.ListSources(ctx, store.OperatorScope, customerID)
	if err != nil || len(src) != 1 {
		t.Fatalf("sources: %v / %v", src, err)
	}
	jul := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if _, err := st.UpsertUsage(ctx, []store.UsageRecord{{
		CustomerID: customerID, SourceID: src[0].ID, ResourceID: "vm-1", ResourceKind: "ecs", SKU: "ecs.m7n.2xlarge.8",
		Quantity: "5000", Unit: "instance-hour", WindowStart: jul, WindowEnd: jul.Add(time.Hour), Region: "me-east-215",
	}}); err != nil {
		t.Fatal(err)
	}
	for _, period := range []string{"2026-07", "2026-08"} {
		run := op.mustJSON("POST", "/api/v1/statements/run", map[string]any{"period": period, "customer_id": customerID}, 200)
		if res := run["results"].([]any)[0].(map[string]any); res["error"] != nil && res["error"] != "" {
			t.Fatalf("run %s: %v", period, res["error"])
		}
	}

	doc := op.must("GET", periods, 200)
	rows, _ := doc["periods"].([]any)
	if len(rows) != 2 {
		t.Fatalf("periods = %v, want July and August", doc["periods"])
	}
	aug, jl := rows[0].(map[string]any), rows[1].(map[string]any)
	if aug["period"] != "2026-08" || jl["period"] != "2026-07" {
		t.Fatalf("order = %v, %v; want the newest period first", aug["period"], jl["period"])
	}

	// August: 1,500 × 0.30 + 500 × 0.50 = 700, 50 % off = 350 net, trued up
	// 250 to the 600 floor (the statement run of the sibling test).
	if aug["true_up"] != 250.0 || aug["net"] != 350.0 || aug["floor"] != 600.0 || aug["subtotal"] != 600.0 || aug["status"] != "draft" || aug["statement_id"] == "" {
		t.Fatalf("August = %v, want true-up 250, net 350, floor 600, subtotal 600, draft", aug)
	}
	commitments, _ := aug["commitments"].([]any)
	if len(commitments) != 1 {
		t.Fatalf("August commitments = %v", aug["commitments"])
	}
	if m := commitments[0].(map[string]any); m["sku"] != "ecs.m7n.2xlarge.8" || m["committed"] != 1500.0 || m["delivered"] != 1500.0 || m["shortfall"] != 0.0 || m["excess"] != 500.0 || m["amount"] != 700.0 || m["committed_price"] != 0.3 {
		t.Fatalf("August commitment = %v, want 1,500 delivered of 1,500, 500 above it, rated 700 at 0.30", m)
	}
	allowances, _ := aug["allowances"].([]any)
	if len(allowances) != 1 {
		t.Fatalf("August allowances = %v", aug["allowances"])
	}
	if a := allowances[0].(map[string]any); a["sku"] != "eip.traffic_gb" || a["included"] != 100.0 || a["used"] != 0.0 || a["quantity"] != 0.0 {
		t.Fatalf("August traffic allowance = %v, want 0 used of 100 (the fixture meters no traffic)", a)
	}
	discounts, _ := aug["discounts"].([]any)
	if len(discounts) != 1 {
		t.Fatalf("August discounts = %v", aug["discounts"])
	}
	if d := discounts[0].(map[string]any); d["discount_id"] != spend["id"] || d["from_contract"] != true || d["amount"] != 350.0 {
		t.Fatalf("August discount = %v, want the spend commitment's 350, marked as the contract's", d)
	}

	// July: 1,500 × 0.30 + 3,500 × 0.50 = 2,200, 50 % off = 1,100 net —
	// above the floor, so no true-up and the floor is the contract's.
	if jl["true_up"] != 0.0 || jl["net"] != 1100.0 || jl["subtotal"] != 1100.0 || jl["floor"] != 600.0 {
		t.Fatalf("July = %v, want no true-up, net 1,100, floor 600", jl)
	}
	if m := jl["commitments"].([]any)[0].(map[string]any); m["delivered"] != 1500.0 || m["excess"] != 3500.0 || m["amount"] != 2200.0 {
		t.Fatalf("July commitment = %v, want 1,500 delivered, 3,500 above it, rated 2,200", m)
	}

	// ── permissions: the contract's own ────────────────────────────────
	other := op.mustJSON("POST", "/api/v1/customers", map[string]any{"slug": "globex", "name": "Globex", "admin_email": "fin@globex.example"}, 201)["id"].(string)
	theirs := op.mustJSON("POST", "/api/v1/contracts", map[string]any{"customer_id": other, "name": "Globex 2026", "starts_on": "2026-01-01", "currency": "OMR"}, 201)["id"].(string)
	cust := &client{t: t, h: h}
	cust.signIn(contractAdmin, mail)
	mine := cust.must("GET", periods, 200)
	if rows, _ := mine["periods"].([]any); len(rows) != 2 {
		t.Fatalf("the customer reads %d periods of its own contract, want 2", len(rows))
	}
	if rec, _ := cust.do("GET", "/api/v1/contracts/"+theirs+"/periods", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("another customer's periods read %d, want 404", rec.Code)
	}
	if rec, _ := op.do("GET", "/api/v1/contracts/no-such/periods", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("an unknown contract's periods read %d, want 404", rec.Code)
	}
}
