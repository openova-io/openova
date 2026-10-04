package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The contracts API end to end (DESIGN.md §15, EPIC #6867): the lifecycle
// over the wire, the permission table, the renewals-due list, the tiered and
// allowance-bearing price-book item, and the SLA credit note.

const contractAdmin = "fin@acme.example"

// seedContractFixture gives one customer a booked source with usage, so a
// statement can actually be rated and credited.
func seedContractFixture(t *testing.T, h http.Handler, st *store.Store, mail *recMail) (*client, string, string) {
	t.Helper()
	ctx := context.Background()
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)

	book := op.mustJSON("POST", "/api/v1/pricebooks", map[string]any{"name": "Terms 2026", "scope": "cloud", "annual_divisor": 8760}, 201)
	bookID := book["id"].(string)
	op.mustJSON("PUT", "/api/v1/pricebooks/"+bookID+"/items", map[string]any{"items": []map[string]any{
		{"sku": "ecs.m7n.2xlarge.8", "unit": "instance-hour", "unit_price": "0.50"},
	}}, 200)

	c := op.mustJSON("POST", "/api/v1/customers", map[string]any{"slug": "acme", "name": "ACME LLC", "admin_email": contractAdmin}, 201)
	customerID := c["id"].(string)
	src, _, err := st.UpsertSource(ctx, customerID, "huawei-project", "me-east-215", "proj-acme")
	if err != nil {
		t.Fatal(err)
	}
	assignBook(t, st, src.ID, bookID)
	aug := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if _, err := st.UpsertUsage(ctx, []store.UsageRecord{{
		CustomerID: customerID, SourceID: src.ID, ResourceID: "vm-1", ResourceKind: "ecs", SKU: "ecs.m7n.2xlarge.8",
		Quantity: "2000", Unit: "instance-hour", WindowStart: aug, WindowEnd: aug.Add(time.Hour), Region: "me-east-215",
	}}); err != nil {
		t.Fatal(err)
	}
	return op, customerID, bookID
}

func TestIntegrationContractsAPI(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	op, customerID, _ := seedContractFixture(t, h, st, mail)

	// ── create ───────────────────────────────────────────────────────────
	c := op.mustJSON("POST", "/api/v1/contracts", map[string]any{
		"customer_id": customerID, "name": "ACME 2026", "starts_on": "2026-01-01", "term_months": 12,
		"minimum_commitment": "1000", "currency": "OMR", "status": "active", "auto_renew": true,
		"renewal_notice_days": 30, "po_reference": "PO-4411",
	}, 201)
	contractID := c["id"].(string)
	if c["ends_on"] != "2026-12-31" || c["renewal_date"] != "2027-01-01" || c["notice_from"] != "2026-12-01" {
		t.Fatalf("derived dates = %v / %v / %v", c["ends_on"], c["renewal_date"], c["notice_from"])
	}
	// A create with no customer is refused before it reaches the database.
	if rec, _ := op.json("POST", "/api/v1/contracts", map[string]any{"name": "orphan", "starts_on": "2026-01-01"}); rec.Code != 400 {
		t.Fatalf("a contract with no customer = %d, want 400", rec.Code)
	}

	// ── items ────────────────────────────────────────────────────────────
	got := op.mustJSON("PUT", "/api/v1/contracts/"+contractID+"/items", map[string]any{"items": []map[string]any{
		{"kind": "commitment", "sku": "ecs.m7n.2xlarge.8", "unit": "instance-hour", "quantity": "7440", "discount_pct": "30"},
		{"kind": "allowance", "sku": "eip.traffic_gb", "unit": "gb", "quantity": "100", "rollover": true},
	}}, 200)
	items, _ := got["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %v", got["items"])
	}
	// A committed-use line at list is refused with a message that says why.
	rec, body := op.json("PUT", "/api/v1/contracts/"+contractID+"/items", map[string]any{"items": []map[string]any{
		{"kind": "commitment", "sku": "ecs.m7n.2xlarge.8", "quantity": "10"},
	}})
	if rec.Code != 400 {
		t.Fatalf("a commitment at list = %d, want 400", rec.Code)
	}
	if msg, _ := body["error"].(string); msg == "" {
		t.Fatalf("the refusal carried no message: %v", body)
	}

	// ── patch ────────────────────────────────────────────────────────────
	patched := op.mustJSON("PATCH", "/api/v1/contracts/"+contractID, map[string]any{"term_months": 24}, 200)
	if patched["ends_on"] != "2027-12-31" {
		t.Fatalf("a 24-month term ends %v, want 2027-12-31", patched["ends_on"])
	}
	// An explicit null clears the minimum; omitting the key would keep it.
	cleared := op.mustJSON("PATCH", "/api/v1/contracts/"+contractID, map[string]any{"minimum_commitment": nil}, 200)
	if _, present := cleared["minimum_commitment"]; present {
		t.Fatalf("minimum_commitment survived an explicit null: %v", cleared["minimum_commitment"])
	}
	restored := op.mustJSON("PATCH", "/api/v1/contracts/"+contractID, map[string]any{"po_reference": "PO-9000"}, 200)
	if _, present := restored["minimum_commitment"]; present {
		t.Fatalf("an unrelated patch put the minimum back: %v", restored["minimum_commitment"])
	}

	// ── lists ────────────────────────────────────────────────────────────
	list := op.must("GET", "/api/v1/contracts", 200)
	if n := len(list["contracts"].([]any)); n != 1 {
		t.Fatalf("the directory lists %d contracts, want 1", n)
	}
	byCustomer := op.must("GET", "/api/v1/customers/"+customerID+"/contracts", 200)
	if n := len(byCustomer["contracts"].([]any)); n != 1 {
		t.Fatalf("the customer's Contract tab lists %d, want 1", n)
	}
	// The renewals-due list: the notice window of a 24-month term from
	// 2026-01-01 opens 30 days before 2027-12-31.
	if due := op.must("GET", "/api/v1/contracts/renewals?on=2027-11-01", 200); len(due["contracts"].([]any)) != 0 {
		t.Fatalf("a contract 60 days from its end is already due")
	}
	due := op.must("GET", "/api/v1/contracts/renewals?on=2027-12-15", 200)
	if n := len(due["contracts"].([]any)); n != 1 {
		t.Fatalf("renewals due in the window = %d, want 1", n)
	}
	// `renewals` is a path, not an id: it must not be read as a contract.
	if rec, _ := op.do("GET", "/api/v1/contracts/renewals?on=not-a-date", "", nil); rec.Code != 400 {
		t.Fatalf("a bad date = %d, want 400", rec.Code)
	}

	// ── delete ───────────────────────────────────────────────────────────
	op.must("DELETE", "/api/v1/contracts/"+contractID, 200)
	if rec, _ := op.do("GET", "/api/v1/contracts/"+contractID, "", nil); rec.Code != 404 {
		t.Fatalf("a deleted contract reads %d, want 404", rec.Code)
	}
}

// The row-level line routes (#6946): one line added, edited and removed at a
// time, under the whole-list validation; the SKU select's source; and the
// SPEND COMMITMENT (DESIGN.md §15.3a) over the wire — the fixture's 2,000 h at
// 0.50 is 1,000 at list, so spend{600, 50 %} discounts 500, nets 500, trues up
// 100 to the 600 floor, and taxes that: 630.
func TestIntegrationContractItemRowCRUDAndSpend(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	op, customerID, _ := seedContractFixture(t, h, st, mail)
	contractID := op.mustJSON("POST", "/api/v1/contracts", map[string]any{
		"customer_id": customerID, "name": "ACME 2026", "starts_on": "2026-01-01", "currency": "OMR", "status": "active",
	}, 201)["id"].(string)
	items := "/api/v1/contracts/" + contractID + "/items"

	// ── the SKU select: what the customer's books price ────────────────
	skus := op.must("GET", "/api/v1/customers/"+customerID+"/skus", 200)
	list, _ := skus["skus"].([]any)
	if len(list) != 1 {
		t.Fatalf("the customer's SKUs = %v, want the one SKU its book prices", skus["skus"])
	}
	sku := list[0].(map[string]any)
	if sku["sku"] != "ecs.m7n.2xlarge.8" || sku["unit"] != "instance-hour" || sku["price_book_name"] != "Terms 2026" || sku["currency"] != "OMR" {
		t.Fatalf("sku option = %v", sku)
	}
	if p, _ := sku["unit_price"].(float64); p != 0.5 {
		t.Fatalf("unit_price = %v, want 0.5", sku["unit_price"])
	}

	// ── add, one line at a time ────────────────────────────────────────
	commit := op.mustJSON("POST", items, map[string]any{"kind": "commitment", "sku": "ecs.m7n.2xlarge.8", "unit": "instance-hour", "quantity": "1000", "discount_pct": "30"}, 201)
	commitID := commit["id"].(string)
	allow := op.mustJSON("POST", items, map[string]any{"kind": "allowance", "sku": "eip.traffic_gb", "unit": "gb", "quantity": "100", "rollover": true}, 201)
	spend := op.mustJSON("POST", items, map[string]any{"kind": "spend", "amount": "600", "discount_pct": "50"}, 201)
	if spend["amount"] != 600.0 || spend["discount_pct"] != 50.0 || spend["sku"] != "" {
		t.Fatalf("spend line = %v", spend)
	}
	c := op.must("GET", "/api/v1/contracts/"+contractID, 200)
	if n := len(c["items"].([]any)); n != 3 {
		t.Fatalf("the contract carries %d lines after three adds", n)
	}
	// The refusals, each with a message: a second spend commitment, a spend
	// line naming a SKU, a 100 % spend discount, the same SKU twice as the
	// same kind, and a commitment at list.
	for _, bad := range []map[string]any{
		{"kind": "spend", "amount": "700", "discount_pct": "10"},
		{"kind": "spend", "sku": "ecs.m7n.2xlarge.8", "amount": "700", "discount_pct": "10"},
		{"kind": "spend", "amount": "700", "discount_pct": "100"},
		{"kind": "spend", "amount": "0", "discount_pct": "10"},
		{"kind": "commitment", "sku": "ecs.m7n.2xlarge.8", "quantity": "10", "discount_pct": "5"},
		{"kind": "commitment", "sku": "ecs.other", "quantity": "10"},
		{"kind": "allowance", "sku": "x", "quantity": "10", "amount": "5"},
	} {
		rec, body := op.json("POST", items, bad)
		if rec.Code != 400 && rec.Code != 409 {
			t.Fatalf("adding %v = %d, want a refusal", bad, rec.Code)
		}
		if msg, _ := body["error"].(string); msg == "" {
			t.Fatalf("the refusal of %v carried no message: %v", bad, body)
		}
	}

	// ── edit one line: the keys sent overlay the row ───────────────────
	patched := op.mustJSON("PATCH", items+"/"+commitID, map[string]any{"quantity": "1500", "committed_price": "0.30", "discount_pct": nil}, 200)
	if patched["quantity"] != 1500.0 || patched["committed_price"] != 0.3 || patched["discount_pct"] != nil || patched["sku"] != "ecs.m7n.2xlarge.8" {
		t.Fatalf("patched line = %v, want quantity 1500 at 0.30 with the percentage cleared and the SKU kept", patched)
	}
	// Clearing the rate without giving another is a commitment at list.
	if rec, _ := op.json("PATCH", items+"/"+commitID, map[string]any{"committed_price": nil}); rec.Code != 400 {
		t.Fatalf("a commitment left with no rate = %d, want 400", rec.Code)
	}
	// A line of another contract is not found through this one.
	other := op.mustJSON("POST", "/api/v1/contracts", map[string]any{"customer_id": customerID, "name": "ACME other", "starts_on": "2026-01-01", "currency": "OMR"}, 201)["id"].(string)
	if rec, _ := op.json("PATCH", "/api/v1/contracts/"+other+"/items/"+commitID, map[string]any{"quantity": "1"}); rec.Code != 404 {
		t.Fatalf("a line reached through another contract = %d, want 404", rec.Code)
	}

	// ── the spend commitment rates the period ─────────────────────────
	run := op.mustJSON("POST", "/api/v1/statements/run", map[string]any{"period": "2026-08", "customer_id": customerID}, 200)
	res := run["results"].([]any)[0].(map[string]any)
	if res["error"] != nil && res["error"] != "" {
		t.Fatalf("run: %v", res["error"])
	}
	// 1,000 at list, less the committed head (1,500 > 2,000? no: the
	// commitment of 1,500 h at 0.30 covers 1,500 of the 2,000 hours and the
	// rest rates at 0.50): 1,500 × 0.30 + 500 × 0.50 = 700. Then 50 % off
	// = 350 net, below the 600 floor: true-up 250, subtotal 600, tax 30.
	if res["true_up"] != "250.000000" {
		t.Fatalf("true-up = %v, want 250.000000 (net 350 brought to the 600 spend floor)", res["true_up"])
	}
	stmt := op.must("GET", "/api/v1/statements/"+res["statement_id"].(string), 200)
	if stmt["subtotal"] != 600.0 || stmt["discount_total"] != 350.0 || stmt["total"] != 630.0 {
		t.Fatalf("subtotal/discount/total = %v / %v / %v, want 600 / 350 / 630", stmt["subtotal"], stmt["discount_total"], stmt["total"])
	}
	detail, _ := stmt["discount_detail"].([]any)
	if len(detail) != 1 || detail[0].(map[string]any)["discount_id"] != spend["id"] {
		t.Fatalf("discount detail = %v, want the spend line's discount, attributed to the line", stmt["discount_detail"])
	}
	if name, _ := detail[0].(map[string]any)["name"].(string); name != "Spend commitment of 600 OMR a month under ACME 2026" {
		t.Fatalf("the applied discount is named %q", name)
	}

	// ── delete, one line at a time; the last may go ───────────────────
	op.must("DELETE", items+"/"+allow["id"].(string), 200)
	op.must("DELETE", items+"/"+spend["id"].(string), 200)
	op.must("DELETE", items+"/"+commitID, 200)
	if rec, _ := op.do("DELETE", items+"/"+commitID, "", nil); rec.Code != 404 {
		t.Fatalf("deleting a deleted line = %d, want 404", rec.Code)
	}
	c = op.must("GET", "/api/v1/contracts/"+contractID, 200)
	if n := len(c["items"].([]any)); n != 0 {
		t.Fatalf("%d lines survived three deletes", n)
	}
	// The statement already rated under the deleted lines is untouched.
	stmt = op.must("GET", "/api/v1/statements/"+res["statement_id"].(string), 200)
	if stmt["subtotal"] != 600.0 {
		t.Fatalf("deleting the lines changed a rated statement to %v", stmt["subtotal"])
	}
	// Every write left its entry, as the PUT does.
	audit := op.must("GET", "/api/v1/customers/"+customerID+"/audit", 200)
	actions := map[string]int{}
	for _, e := range audit["entries"].([]any) {
		actions[e.(map[string]any)["action"].(string)]++
	}
	if actions["contract.items.add"] != 3 || actions["contract.items.update"] != 1 || actions["contract.items.delete"] != 3 {
		t.Fatalf("audit = %v, want 3 adds, 1 update, 3 deletes", actions)
	}
}

// The permission table of DESIGN.md §15.7, asserted rather than described:
// a customer principal READS its own contract and writes nothing; another
// customer's contract is a 404, not a 403, so its id is not confirmed.
func TestIntegrationContractPermissions(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	op, customerID, _ := seedContractFixture(t, h, st, mail)
	other := op.mustJSON("POST", "/api/v1/customers", map[string]any{"slug": "globex", "name": "Globex", "admin_email": "fin@globex.example"}, 201)
	otherID := other["id"].(string)

	mine := op.mustJSON("POST", "/api/v1/contracts", map[string]any{
		"customer_id": customerID, "name": "ACME 2026", "starts_on": "2026-01-01", "currency": "OMR", "status": "active",
	}, 201)["id"].(string)
	theirs := op.mustJSON("POST", "/api/v1/contracts", map[string]any{
		"customer_id": otherID, "name": "Globex 2026", "starts_on": "2026-01-01", "currency": "OMR", "status": "active",
	}, 201)["id"].(string)

	// The customer's own owner, signed in through its admin email.
	cust := &client{t: t, h: h}
	cust.signIn(contractAdmin, mail)

	got := cust.must("GET", "/api/v1/contracts/"+mine, 200)
	if got["name"] != "ACME 2026" {
		t.Fatalf("the customer cannot read its own contract: %v", got)
	}
	if list := cust.must("GET", "/api/v1/contracts", 200); len(list["contracts"].([]any)) != 1 {
		t.Fatalf("the customer's list = %v, want only its own", list["contracts"])
	}
	if rec, _ := cust.do("GET", "/api/v1/contracts/"+theirs, "", nil); rec.Code != 404 {
		t.Fatalf("another customer's contract reads %d, want 404", rec.Code)
	}
	// Read-only: every write is refused.
	for _, w := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/v1/contracts", map[string]any{"customer_id": customerID, "name": "self-signed", "starts_on": "2026-01-01"}},
		{"PATCH", "/api/v1/contracts/" + mine, map[string]any{"minimum_commitment": "1"}},
		{"PUT", "/api/v1/contracts/" + mine + "/items", map[string]any{"items": []any{}}},
		{"DELETE", "/api/v1/contracts/" + mine, nil},
		{"POST", "/api/v1/contracts/" + mine + "/sla-credit", map[string]any{"statement_id": "x", "pct": "10"}},
	} {
		var rec = func() int {
			if w.body == nil {
				r, _ := cust.do(w.method, w.path, "", nil)
				return r.Code
			}
			r, _ := cust.json(w.method, w.path, w.body)
			return r.Code
		}()
		if rec != 403 {
			t.Fatalf("%s %s by a customer principal = %d, want 403", w.method, w.path, rec)
		}
	}
}

// An SLA credit over the wire: a real credit note against a named statement,
// carrying the percentage and the availability measured.
func TestIntegrationSLACreditAPI(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	op, customerID, _ := seedContractFixture(t, h, st, mail)
	contractID := op.mustJSON("POST", "/api/v1/contracts", map[string]any{
		"customer_id": customerID, "name": "ACME 2026", "starts_on": "2026-01-01", "currency": "OMR", "status": "active",
	}, 201)["id"].(string)

	run := op.mustJSON("POST", "/api/v1/statements/run", map[string]any{"period": "2026-08"}, 200)
	results := run["results"].([]any)
	statementID := results[0].(map[string]any)["statement_id"].(string)
	op.mustJSON("POST", "/api/v1/statements/"+statementID+"/issue", map[string]any{}, 200)

	note := op.mustJSON("POST", "/api/v1/contracts/"+contractID+"/sla-credit", map[string]any{
		"statement_id": statementID, "pct": "10", "measured_availability": "99.2",
		"reason": "availability 99.2 % against the 99.9 % commitment for August",
	}, 201)
	// 2,000 h × 0.50 = 1,000.00 + 5 % tax = 1,050.00; 10 % of it is 105.00.
	if note["total"] != 105.0 {
		t.Fatalf("SLA credit total = %v, want 105", note["total"])
	}
	if note["number"] == "" || note["number"] == nil {
		t.Fatalf("the SLA credit was not numbered: %v", note)
	}
	// It is on the invoice and in the customer's credit-note list — the one
	// machinery, not a parallel one.
	stmt := op.must("GET", "/api/v1/statements/"+statementID, 200)
	if stmt["credited_total"] != 105.0 {
		t.Fatalf("credited on the invoice = %v, want 105", stmt["credited_total"])
	}
	notes := op.must("GET", "/api/v1/customers/"+customerID+"/credit-notes", 200)
	if n := len(notes["credit_notes"].([]any)); n != 1 {
		t.Fatalf("the customer's credit notes = %d, want 1", n)
	}
	// The refusals: no statement named, and a percentage out of range.
	if rec, _ := op.json("POST", "/api/v1/contracts/"+contractID+"/sla-credit", map[string]any{"pct": "10"}); rec.Code != 400 {
		t.Fatalf("an SLA credit with no statement = %d, want 400", rec.Code)
	}
	if rec, _ := op.json("POST", "/api/v1/contracts/"+contractID+"/sla-credit", map[string]any{"statement_id": statementID, "pct": "0"}); rec.Code != 400 {
		t.Fatalf("a 0 %% SLA credit = %d, want 400", rec.Code)
	}
}

// The price-book item editor's two new shapes, over the wire: the bands and
// the allowance round-trip, a bad ladder is refused with a message, and an
// unrelated edit never drops a ladder.
func TestIntegrationPriceItemShapesAPI(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	op, _, bookID := seedContractFixture(t, h, st, mail)

	it := op.mustJSON("POST", "/api/v1/pricebooks/"+bookID+"/items", map[string]any{
		"sku": "object_gib", "unit": "gb-month", "unit_price": "0.010", "tier_mode": "graduated",
		"tiers": []map[string]any{{"up_to": "10240", "price": "0.010"}, {"up_to": "102400", "price": "0.008"}, {"up_to": nil, "price": "0.006"}},
	}, 201)
	if it["tier_mode"] != "graduated" {
		t.Fatalf("tier_mode = %v", it["tier_mode"])
	}
	if n := len(it["tiers"].([]any)); n != 3 {
		t.Fatalf("tiers = %v", it["tiers"])
	}

	// An allowance, with rollover off by default.
	it = op.mustJSON("POST", "/api/v1/pricebooks/"+bookID+"/items", map[string]any{
		"sku": "eip.traffic_gb", "unit": "gb", "unit_price": "0.05", "allowance": "50",
	}, 201)
	if it["allowance"] != 50.0 {
		t.Fatalf("allowance = %v, want 50", it["allowance"])
	}
	if it["allowance_rollover"] == true {
		t.Fatal("an allowance rolled over without being asked to")
	}

	// A descending ladder is refused while the operator is typing.
	rec, body := op.json("PATCH", "/api/v1/pricebooks/"+bookID+"/items/object_gib", map[string]any{
		"tiers": []map[string]any{{"up_to": "100", "price": "1"}, {"up_to": "50", "price": "0.5"}},
	})
	if rec.Code != 400 {
		t.Fatalf("a descending ladder = %d, want 400", rec.Code)
	}
	if msg, _ := body["error"].(string); msg == "" {
		t.Fatalf("the refusal carried no message: %v", body)
	}

	// An unrelated edit keeps the ladder.
	it = op.mustJSON("PATCH", "/api/v1/pricebooks/"+bookID+"/items/object_gib", map[string]any{"description": "object storage"}, 200)
	if n := len(it["tiers"].([]any)); n != 3 {
		t.Fatalf("a description edit left %v tiers", it["tiers"])
	}
	// An empty band list clears the ladder and the mode with it.
	it = op.mustJSON("PATCH", "/api/v1/pricebooks/"+bookID+"/items/object_gib", map[string]any{"tiers": []any{}}, 200)
	if it["tier_mode"] != nil && it["tier_mode"] != "" {
		t.Fatalf("tier_mode survived the bands: %v", it["tier_mode"])
	}
	if ts, ok := it["tiers"].([]any); ok && len(ts) != 0 {
		t.Fatalf("tiers = %v, want cleared", it["tiers"])
	}
}
