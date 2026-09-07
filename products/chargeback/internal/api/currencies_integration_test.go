package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Currency rates over HTTP (#6867 follow-up, DESIGN.md §3.10): the CRUD
// routes with their authz and validation, the audit trail, and the
// `unconverted` list the explorer, summary and recommendations carry.

// currencyNow is the 8th, so 2026-09-01..07 are complete days of the
// current month: the summary's month-to-date is the seeded window.
var currencyNow = time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)

type fxAPISeed struct {
	omr, usd store.Customer
}

// seedCurrencyAPI: an OMR customer and a USD customer, one ECS hour per
// hour at 0.5 for 2026-09-01..07 → 84 OMR and 84 USD. No rate is stored.
func seedCurrencyAPI(t *testing.T, st *store.Store) fxAPISeed {
	t.Helper()
	ctx := context.Background()
	mk := func(slug, currency string) store.Customer {
		b, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: slug + "-book", Currency: currency, AnnualDivisor: 8760, BillStopped: "compute"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.PutPriceItems(ctx, b.ID, []store.PriceItem{{SKU: "ecs.m7n.xlarge.8", Unit: "instance-hour", UnitPrice: "0.5"}}, true); err != nil {
			t.Fatal(err)
		}
		c, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: slug, Name: strings.ToUpper(slug), AdminEmail: slug + "@x.example", PriceBookID: b.ID, StartDate: "2026-08-01"})
		if err != nil {
			t.Fatal(err)
		}
		src, _, err := st.UpsertSource(ctx, c.ID, "huawei-project", "me-east-1", "proj-"+slug)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.UpsertInventory(ctx, src.ID, []store.InventoryUpsert{{ResourceID: "vm-" + slug, Kind: "ecs", Name: "vm-" + slug,
			Attrs: map[string]any{"status": "ACTIVE", "flavor": "m7n.xlarge.8"}, Created: dayAt(2026, 8, 20), SeenAt: dayAt(2026, 9, 7).Add(23 * time.Hour)}}); err != nil {
			t.Fatal(err)
		}
		var recs []store.UsageRecord
		for d := 1; d <= 7; d++ {
			for h := 0; h < 24; h++ {
				at := dayAt(2026, 9, d).Add(time.Duration(h) * time.Hour)
				lb, _ := json.Marshal(map[string]any{"name": "vm-" + slug, "status": "ACTIVE"})
				recs = append(recs, store.UsageRecord{CustomerID: c.ID, SourceID: src.ID, ResourceID: "vm-" + slug, ResourceKind: "ecs", SKU: "ecs.m7n.xlarge.8",
					Quantity: "1.000000", Unit: "instance-hour", WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-1", Labels: lb})
			}
		}
		if _, err := st.UpsertUsage(ctx, recs); err != nil {
			t.Fatal(err)
		}
		return c
	}
	return fxAPISeed{omr: mk("acme", "OMR"), usd: mk("bravo", "USD")}
}

func unconvertedOf(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()
	raw, ok := doc["unconverted"].([]any)
	if !ok {
		t.Fatalf("document lacks an unconverted list: %v", doc)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.(map[string]any))
	}
	return out
}

func TestIntegrationCurrencyRatesAPI(t *testing.T) {
	h, st, mail := setupAPIAt(t, currencyNow)
	s := seedCurrencyAPI(t, st)
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)
	cust := customerClient(t, h, st, "fin@bravo.example", store.RoleCustomerAdmin, s.usd.ID)
	anon := &client{t: t, h: h}

	// Empty to begin with, and the reporting currency is named.
	doc := op.must("GET", "/api/v1/currencies", 200)
	if doc["reporting_currency"] != "OMR" || len(doc["rates"].([]any)) != 0 {
		t.Fatalf("initial = %v", doc)
	}
	// Before any rate: the USD customer's 84 USD is unconverted, out of the
	// total, and the explorer says so.
	ex := op.must("GET", "/api/v1/cost/explore?from=2026-09-01&to=2026-09-08&group_by=customer", 200)
	if ex["currency"] != "OMR" || ex["mixed_currency"] != true || ex["total"].(map[string]any)["current"] != 84.0 {
		t.Fatalf("explore before rate = currency %v mixed %v total %v", ex["currency"], ex["mixed_currency"], ex["total"])
	}
	if u := unconvertedOf(t, ex); len(u) != 1 || u[0]["currency"] != "USD" || u[0]["records"] != 168.0 || u[0]["cost"] != 84.0 {
		t.Fatalf("explore unconverted = %v", u)
	}
	sum := op.must("GET", "/api/v1/cost/summary", 200)
	if u := unconvertedOf(t, sum); len(u) != 1 || u[0]["currency"] != "USD" || sum["mixed_currency"] != true || sum["currency"] != "OMR" {
		t.Fatalf("summary = currency %v mixed %v unconverted %v", sum["currency"], sum["mixed_currency"], u)
	}
	if sum["mtd"].(map[string]any)["cost"] != 84.0 {
		t.Fatalf("mtd before rate = %v", sum["mtd"])
	}

	// Validation, every branch a 400 with a message that names the rule.
	bad := []struct {
		path string
		body any
		msg  string
	}{
		{"/api/v1/currencies/OMR", map[string]any{"per_base": 1}, "reporting currency"},
		{"/api/v1/currencies/omr", map[string]any{"per_base": 2.6}, "reporting currency"},
		{"/api/v1/currencies/US", map[string]any{"per_base": 2.6}, "3-letter"},
		{"/api/v1/currencies/USDX", map[string]any{"per_base": 2.6}, "3-letter"},
		{"/api/v1/currencies/USD", map[string]any{"per_base": 0}, "per_base must be a number > 0"},
		{"/api/v1/currencies/USD", map[string]any{"per_base": -2.6}, "per_base must be a number > 0"},
		{"/api/v1/currencies/USD", map[string]any{"per_base": "abc"}, "invalid body"},
		{"/api/v1/currencies/USD", map[string]any{}, "per_base is required"},
		{"/api/v1/currencies/USD", map[string]any{"per_base": 2.6, "colour": "red"}, "invalid body"},
	}
	for _, c := range bad {
		rec, out := op.json("PUT", c.path, c.body)
		if rec.Code != 400 || !strings.Contains(out["error"].(string), c.msg) {
			t.Fatalf("PUT %s %v = %d %s (want 400 %q)", c.path, c.body, rec.Code, rec.Body.String(), c.msg)
		}
	}
	if doc := op.must("GET", "/api/v1/currencies", 200); len(doc["rates"].([]any)) != 0 {
		t.Fatalf("a rejected rate was stored: %v", doc)
	}

	// Authz: operator-only, 401 anonymous, 403 for a customer admin.
	for _, m := range []string{"GET", "PUT", "DELETE"} {
		path := "/api/v1/currencies/USD"
		if rec, _ := anon.json(m, path, map[string]any{"per_base": 2.6}); rec.Code != 401 {
			t.Fatalf("anonymous %s = %d", m, rec.Code)
		}
		if rec, _ := cust.json(m, path, map[string]any{"per_base": 2.6}); rec.Code != 403 {
			t.Fatalf("customer %s = %d", m, rec.Code)
		}
	}
	if rec, _ := cust.do("GET", "/api/v1/currencies", "", nil); rec.Code != 403 {
		t.Fatalf("customer list = %d", rec.Code)
	}

	// PUT stores (lower-case path, numeric body), GET reads it back, the
	// reporting currency answers 1.
	r := op.mustJSON("PUT", "/api/v1/currencies/usd", map[string]any{"per_base": 2.6}, 200)
	if r["code"] != "USD" || r["per_base"] != 2.6 || r["source"] != "manual" || r["updated_at"] == nil {
		t.Fatalf("put = %v", r)
	}
	if g := op.must("GET", "/api/v1/currencies/USD", 200); g["per_base"] != 2.6 {
		t.Fatalf("get = %v", g)
	}
	if g := op.must("GET", "/api/v1/currencies/OMR", 200); g["per_base"] != 1.0 || g["source"] != "reporting" {
		t.Fatalf("get reporting = %v", g)
	}
	op.must("GET", "/api/v1/currencies/EUR", 404)
	op.must("GET", "/api/v1/currencies/nope", 404)
	// A string per_base and a source are accepted too.
	if r := op.mustJSON("PUT", "/api/v1/currencies/USD", map[string]any{"per_base": "2.5", "source": "cbo"}, 200); r["per_base"] != 2.5 || r["source"] != "cbo" {
		t.Fatalf("replace = %v", r)
	}
	if doc := op.must("GET", "/api/v1/currencies", 200); len(doc["rates"].([]any)) != 1 {
		t.Fatalf("list = %v", doc)
	}

	// With the rate the USD customer is converted: 84 + 84 / 2.5 = 117.6.
	ex = op.must("GET", "/api/v1/cost/explore?from=2026-09-01&to=2026-09-08&group_by=customer", 200)
	if ex["mixed_currency"] != false || ex["total"].(map[string]any)["current"] != 117.6 || len(unconvertedOf(t, ex)) != 0 {
		t.Fatalf("explore with rate = mixed %v total %v unconverted %v", ex["mixed_currency"], ex["total"], ex["unconverted"])
	}
	// The customer lens converts the same way and reports the reporting
	// currency, not its book's.
	cx := cust.must("GET", "/api/v1/customers/"+s.usd.ID+"/cost/explore?from=2026-09-01&to=2026-09-08", 200)
	if cx["currency"] != "OMR" || cx["total"].(map[string]any)["current"] != 33.6 {
		t.Fatalf("customer explore = %v %v", cx["currency"], cx["total"])
	}
	sum = op.must("GET", "/api/v1/cost/summary", 200)
	if sum["mtd"].(map[string]any)["cost"] != 117.6 || sum["mixed_currency"] != false || len(unconvertedOf(t, sum)) != 0 {
		t.Fatalf("summary with rate = %v %v %v", sum["mtd"], sum["mixed_currency"], sum["unconverted"])
	}
	// Recommendations carry the reporting currency and an unconverted list.
	rec := op.must("GET", "/api/v1/recommendations", 200)
	if rec["currency"] != "OMR" || rec["unconverted"] == nil {
		t.Fatalf("recommendations = currency %v unconverted %v", rec["currency"], rec["unconverted"])
	}
	// Resources: one figure per row in the reporting currency.
	rl := op.must("GET", "/api/v1/resources?from=2026-09-01&to=2026-09-08", 200)
	if rl["currency"] != "OMR" || rl["sum_cost"] != 117.6 {
		t.Fatalf("resources = %v %v", rl["currency"], rl["sum_cost"])
	}

	// Budgets: the amount is in the reporting currency — another currency
	// is refused naming it, none defaults to it, and `actual` is converted.
	rc, out := op.json("POST", "/api/v1/budgets", map[string]any{"name": "cap", "amount": "1000", "currency": "USD"})
	if rc.Code != 400 || !strings.Contains(out["error"].(string), "reporting currency (OMR)") {
		t.Fatalf("USD budget = %d %s", rc.Code, rc.Body.String())
	}
	b := op.mustJSON("POST", "/api/v1/budgets", map[string]any{"name": "cap", "amount": "1000"}, 201)
	if b["currency"] != "OMR" {
		t.Fatalf("budget currency = %v", b["currency"])
	}
	bs := op.must("GET", "/api/v1/budgets/"+b["id"].(string)+"/status", 200)
	if bs["actual"] != 117.6 || bs["currency"] != "OMR" {
		t.Fatalf("budget status = actual %v %v", bs["actual"], bs["currency"])
	}

	// DELETE removes the rate; the currency is unconverted again.
	if d := op.must("DELETE", "/api/v1/currencies/usd", 200); d["deleted"] != true || d["code"] != "USD" {
		t.Fatalf("delete = %v", d)
	}
	op.must("DELETE", "/api/v1/currencies/USD", 404)
	op.must("GET", "/api/v1/currencies/USD", 404)
	ex = op.must("GET", "/api/v1/cost/explore?from=2026-09-01&to=2026-09-08", 200)
	if ex["mixed_currency"] != true || len(unconvertedOf(t, ex)) != 1 {
		t.Fatalf("explore after delete = %v %v", ex["mixed_currency"], ex["unconverted"])
	}

	// Audit: every write is one currency.rate entry by the operator; the
	// rejected ones wrote nothing.
	rows, err := st.DB().QueryContext(context.Background(), `SELECT actor, details::text FROM audit_log WHERE action = 'currency.rate' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var entries []string
	for rows.Next() {
		var actor, details string
		if err := rows.Scan(&actor, &details); err != nil {
			t.Fatal(err)
		}
		if actor != opEmail {
			t.Fatalf("audit actor = %q", actor)
		}
		entries = append(entries, details)
	}
	if len(entries) != 3 {
		t.Fatalf("audit entries = %d: %v", len(entries), entries)
	}
	if !strings.Contains(entries[0], `"per_base": "2.6000000000"`) || strings.Contains(entries[0], "previous_per_base") {
		t.Fatalf("first write audit = %s", entries[0])
	}
	if !strings.Contains(entries[1], `"previous_per_base": "2.6000000000"`) || !strings.Contains(entries[1], `"source": "cbo"`) {
		t.Fatalf("replace audit = %s", entries[1])
	}
	if !strings.Contains(entries[2], `"deleted": true`) || !strings.Contains(entries[2], `"previous_per_base": "2.5000000000"`) {
		t.Fatalf("delete audit = %s", entries[2])
	}
}
