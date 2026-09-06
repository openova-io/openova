package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// CRUD gaps of EPIC #6867 (DESIGN.md §3.8). Every endpoint is exercised
// through the HTTP handler with real sessions, including the refusal paths
// (409 / 404) and the scope walls: customer B can never touch A's source,
// discount or view.

const (
	acmeAdmin   = "admin@acme.example"
	acmeViewer  = "viewer@acme.example"
	bravoAdmin  = "admin@bravo.example"
	crudDivisor = 8760
)

type crudSeed struct {
	bookID      string
	acme, bravo store.Customer
	srcA, srcB  store.CostSource
}

// seedCRUD writes one list book (three SKUs, one with an annual price), two
// customers on it with one source each, August 2026 usage for statements,
// and last-48h usage for acme (priced ECS, unpriced k8s, and the cpu_util
// metric that must never count) for coverage.
func seedCRUD(t *testing.T, st *store.Store) crudSeed {
	t.Helper()
	ctx := context.Background()
	annual := store.Decimal("175.20")
	book, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "List 2026", Currency: "OMR", AnnualDivisor: crudDivisor, BillStopped: "compute"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, book.ID, []store.PriceItem{
		{SKU: "ecs.m7n.xlarge.8", Unit: "instance-hour", UnitPrice: "0.5", Description: "4 vCPU"},
		{SKU: "evs.ssd.gb", Unit: "gb-hour", UnitPrice: "0.001"},
		{SKU: "eip", Unit: "hour", UnitPrice: "0.02000000", AnnualPrice: &annual},
	}, true); err != nil {
		t.Fatal(err)
	}
	acme, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "acme", Name: "Acme", AdminEmail: acmeAdmin, PriceBookID: book.ID, StartDate: "2026-08-01"})
	if err != nil {
		t.Fatal(err)
	}
	bravo, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "bravo", Name: "Bravo", AdminEmail: bravoAdmin, PriceBookID: book.ID, StartDate: "2026-08-01"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertCustomerUser(ctx, acme.ID, acmeViewer, "viewer"); err != nil {
		t.Fatal(err)
	}
	srcA, _, err := st.UpsertSource(ctx, acme.ID, "huawei-project", "me-east-215", "ok-a")
	if err != nil {
		t.Fatal(err)
	}
	srcB, _, err := st.UpsertSource(ctx, bravo.ID, "huawei-project", "me-east-215", "ok-b")
	if err != nil {
		t.Fatal(err)
	}
	var recs []store.UsageRecord
	rec := func(c store.Customer, src store.CostSource, res, kind, sku, unit string, at time.Time) {
		recs = append(recs, store.UsageRecord{CustomerID: c.ID, SourceID: src.ID, ResourceID: res, ResourceKind: kind, SKU: sku, Quantity: "1.000000", Unit: unit,
			WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-215", Labels: []byte(`{"status":"ACTIVE"}`)})
	}
	aug := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for h := 0; h < 100; h++ {
		rec(acme, srcA, "vm-a", "ecs", "ecs.m7n.xlarge.8", "instance-hour", aug.Add(time.Duration(h)*time.Hour))
	}
	for h := 0; h < 40; h++ {
		rec(bravo, srcB, "vm-b", "ecs", "ecs.m7n.xlarge.8", "instance-hour", aug.Add(time.Duration(h)*time.Hour))
	}
	start := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	for h := 0; h < 48; h++ {
		at := start.Add(time.Duration(h) * time.Hour)
		rec(acme, srcA, "vm-live", "ecs", "ecs.m7n.xlarge.8", "instance-hour", at)
		rec(acme, srcA, "pod-live", "k8s-pod", "k8s.vcpu", "vcpu-hour", at)
		rec(acme, srcA, "vm-live", "ecs", "ecs.cpu_util", "pct-hour-avg", at)
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	return crudSeed{bookID: book.ID, acme: acme, bravo: bravo, srcA: srcA, srcB: srcB}
}

func auditActions(t *testing.T, st *store.Store, action string) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRowContext(context.Background(), `SELECT count(*) FROM audit_log WHERE action = $1`, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func statementFor(t *testing.T, run map[string]any, customerID string) string {
	t.Helper()
	for _, r := range run["results"].([]any) {
		m := r.(map[string]any)
		if m["customer_id"] == customerID {
			id, _ := m["statement_id"].(string)
			return id
		}
	}
	t.Fatalf("no result for %s in %+v", customerID, run)
	return ""
}

func TestIntegrationCustomerDeleteRefusedWhileIssued(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	seed := seedCRUD(t, st)
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)
	acme := &client{t: t, h: h}
	acme.signIn(acmeAdmin, mail)

	run := op.mustJSON("POST", "/api/v1/statements/run", map[string]string{"period": "2026-08"}, 200)
	op.mustJSON("POST", "/api/v1/statements/"+statementFor(t, run, seed.acme.ID)+"/issue", nil, 200)

	// Operator-only.
	acme.must("DELETE", "/api/v1/customers/"+seed.acme.ID, 403)
	acme.must("DELETE", "/api/v1/customers/"+seed.bravo.ID, 403)

	// Issued statement → 409 with a message that says why.
	rec, out := op.do("DELETE", "/api/v1/customers/"+seed.acme.ID, "", nil)
	if rec.Code != 409 || !strings.Contains(out["error"].(string), "issued statement") {
		t.Fatalf("delete with issued statement = %d %s", rec.Code, rec.Body.String())
	}
	op.must("GET", "/api/v1/customers/"+seed.acme.ID, 200)

	// Draft only → deleted, and the cascade takes the source, usage and draft.
	if out := op.must("DELETE", "/api/v1/customers/"+seed.bravo.ID, 200); out["deleted"] != true || out["slug"] != "bravo" {
		t.Fatalf("delete = %+v", out)
	}
	op.must("GET", "/api/v1/customers/"+seed.bravo.ID, 404)
	op.must("DELETE", "/api/v1/customers/"+seed.bravo.ID, 404)
	if _, err := st.GetSource(context.Background(), store.OperatorScope, seed.srcB.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("source survived the customer: %v", err)
	}
	if n, _ := st.UsageCount(context.Background(), seed.srcB.ID); n != 0 {
		t.Fatalf("usage survived the customer: %d", n)
	}
	if n := auditActions(t, st, "customer.delete"); n != 1 {
		t.Fatalf("customer.delete audit entries = %d", n)
	}
	var slug, name string
	_ = st.DB().QueryRowContext(context.Background(), `SELECT details->>'slug', details->>'name' FROM audit_log WHERE action = 'customer.delete'`).Scan(&slug, &name)
	if slug != "bravo" || name != "Bravo" {
		t.Fatalf("audit details = slug %q name %q", slug, name)
	}
}

func TestIntegrationPriceBookCloneItemsExportCoverageDelete(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	seed := seedCRUD(t, st)
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)
	acme := &client{t: t, h: h}
	acme.signIn(acmeAdmin, mail)

	// Clone: header + every item, annual price preserved.
	clone := op.mustJSON("POST", "/api/v1/pricebooks/"+seed.bookID+"/clone", map[string]string{"name": "Acme negotiated"}, 201)
	cloneID := clone["id"].(string)
	if clone["name"] != "Acme negotiated" || clone["currency"] != "OMR" || clone["annual_divisor"].(float64) != crudDivisor || clone["bill_stopped"] != "compute" {
		t.Fatalf("clone header = %+v", clone)
	}
	items := clone["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("clone items = %d", len(items))
	}
	var sawAnnual bool
	for _, it := range items {
		m := it.(map[string]any)
		if m["sku"] == "eip" {
			sawAnnual = m["annual_price"].(float64) == 175.2 && m["unit_price"].(float64) == 0.02
		}
		if m["sku"] == "ecs.m7n.xlarge.8" && m["description"] != "4 vCPU" {
			t.Fatalf("clone lost description: %+v", m)
		}
	}
	if !sawAnnual {
		t.Fatalf("clone lost the annual price: %+v", items)
	}
	op.mustJSON("POST", "/api/v1/pricebooks/"+seed.bookID+"/clone", map[string]string{"name": "Acme negotiated"}, 409)
	op.mustJSON("POST", "/api/v1/pricebooks/"+seed.bookID+"/clone", map[string]string{"name": ""}, 400)
	op.mustJSON("POST", "/api/v1/pricebooks/00000000-0000-0000-0000-000000000000/clone", map[string]string{"name": "x"}, 404)
	acme.mustJSON("POST", "/api/v1/pricebooks/"+seed.bookID+"/clone", map[string]string{"name": "mine"}, 403)

	// Add an item from an annual price: unit = annual ÷ divisor.
	it := op.mustJSON("POST", "/api/v1/pricebooks/"+cloneID+"/items", map[string]any{"sku": "nat.1", "unit": "hour", "annual_price": 438, "description": "NAT small"}, 201)
	if it["unit_price"].(float64) != 0.05 || it["annual_price"].(float64) != 438 || it["description"] != "NAT small" {
		t.Fatalf("added item = %+v", it)
	}
	op.mustJSON("POST", "/api/v1/pricebooks/"+cloneID+"/items", map[string]any{"sku": "nat.1", "unit": "hour", "unit_price": 1}, 409)
	op.mustJSON("POST", "/api/v1/pricebooks/"+cloneID+"/items", map[string]any{"sku": "nat.2", "unit": "hour"}, 400)
	op.mustJSON("POST", "/api/v1/pricebooks/"+cloneID+"/items", map[string]any{"sku": "", "unit": "hour", "unit_price": 1}, 400)
	op.mustJSON("POST", "/api/v1/pricebooks/00000000-0000-0000-0000-000000000000/items", map[string]any{"sku": "x", "unit": "hour", "unit_price": 1}, 404)
	acme.mustJSON("POST", "/api/v1/pricebooks/"+cloneID+"/items", map[string]any{"sku": "free", "unit": "hour", "unit_price": 0}, 403)

	// Patch: annual → derived unit; direct unit → annual cleared; 404 on an
	// unknown SKU; 400 on an empty patch.
	it = op.mustJSON("PATCH", "/api/v1/pricebooks/"+cloneID+"/items/nat.1", map[string]any{"annual_price": "876"}, 200)
	if it["unit_price"].(float64) != 0.1 || it["annual_price"].(float64) != 876 {
		t.Fatalf("patched (annual) = %+v", it)
	}
	it = op.mustJSON("PATCH", "/api/v1/pricebooks/"+cloneID+"/items/nat.1", map[string]any{"unit_price": 0.2, "description": "NAT small, negotiated"}, 200)
	if it["unit_price"].(float64) != 0.2 || it["annual_price"] != nil || it["description"] != "NAT small, negotiated" {
		t.Fatalf("patched (unit) = %+v", it)
	}
	it = op.mustJSON("PATCH", "/api/v1/pricebooks/"+cloneID+"/items/nat.1", map[string]any{"unit": "gateway-hour"}, 200)
	if it["unit"] != "gateway-hour" || it["unit_price"].(float64) != 0.2 {
		t.Fatalf("patched (unit name) = %+v", it)
	}
	op.mustJSON("PATCH", "/api/v1/pricebooks/"+cloneID+"/items/nope", map[string]any{"unit_price": 1}, 404)
	op.mustJSON("PATCH", "/api/v1/pricebooks/"+cloneID+"/items/nat.1", map[string]any{}, 400)
	op.mustJSON("PATCH", "/api/v1/pricebooks/"+cloneID+"/items/nat.1", map[string]any{"unit_price": -1}, 400)
	op.mustJSON("PATCH", "/api/v1/pricebooks/"+cloneID+"/items/nat.1", map[string]any{"annual_price": "-5"}, 400)
	acme.mustJSON("PATCH", "/api/v1/pricebooks/"+cloneID+"/items/nat.1", map[string]any{"unit_price": 0}, 403)
	// The list book is untouched by edits to the clone.
	if got := op.must("GET", "/api/v1/pricebooks/"+seed.bookID, 200); len(got["items"].([]any)) != 3 {
		t.Fatalf("list book changed: %+v", got["items"])
	}

	// Export: importable layout, round-trips through the existing import.
	rec, _ := op.do("GET", "/api/v1/pricebooks/"+cloneID+"/export.csv", "", nil)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") || !strings.HasPrefix(rec.Body.String(), "sku,unit,annual_price,unit_price,description\n") {
		t.Fatalf("export = %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), `pricebook-acme-negotiated.csv`) {
		t.Fatalf("export filename = %s", rec.Header().Get("Content-Disposition"))
	}
	exported := rec.Body.String()
	if !strings.Contains(exported, "eip,hour,175.20000000,0.02000000,") || !strings.Contains(exported, "nat.1,gateway-hour,,0.20000000,\"NAT small, negotiated\"") {
		t.Fatalf("export rows:\n%s", exported)
	}
	target := op.mustJSON("POST", "/api/v1/pricebooks", map[string]any{"name": "Re-imported", "annual_divisor": crudDivisor}, 201)
	targetID := target["id"].(string)
	rec, out := op.do("POST", "/api/v1/pricebooks/"+targetID+"/import", "text/csv", strings.NewReader(exported))
	if rec.Code != 200 || out["imported"].(float64) != 4 || len(out["errors"].([]any)) != 0 {
		t.Fatalf("re-import = %d %s", rec.Code, rec.Body.String())
	}
	want := map[string]float64{}
	for _, x := range op.must("GET", "/api/v1/pricebooks/"+cloneID, 200)["items"].([]any) {
		m := x.(map[string]any)
		want[m["sku"].(string)] = m["unit_price"].(float64)
	}
	for _, x := range op.must("GET", "/api/v1/pricebooks/"+targetID, 200)["items"].([]any) {
		m := x.(map[string]any)
		if want[m["sku"].(string)] != m["unit_price"].(float64) {
			t.Fatalf("round trip changed %s: %v vs %v", m["sku"], m["unit_price"], want[m["sku"].(string)])
		}
	}
	// Unpriced 404 for an unknown book; any signed-in role may export.
	op.must("GET", "/api/v1/pricebooks/00000000-0000-0000-0000-000000000000/export.csv", 404)
	if rec, _ := acme.do("GET", "/api/v1/pricebooks/"+cloneID+"/export.csv", "", nil); rec.Code != 200 {
		t.Fatalf("customer export = %d", rec.Code)
	}

	// Delete item.
	op.must("DELETE", "/api/v1/pricebooks/"+cloneID+"/items/nat.1", 200)
	op.must("DELETE", "/api/v1/pricebooks/"+cloneID+"/items/nat.1", 404)
	acme.must("DELETE", "/api/v1/pricebooks/"+cloneID+"/items/eip", 403)
	if got := op.must("GET", "/api/v1/pricebooks/"+cloneID, 200); len(got["items"].([]any)) != 3 {
		t.Fatalf("clone items after delete = %+v", got["items"])
	}

	// Coverage of the list book: acme's last-48h usage has one priced SKU,
	// one unpriced, and the cpu_util metric which must not appear.
	acme.must("GET", "/api/v1/pricebooks/"+seed.bookID+"/coverage", 403)
	cov := op.must("GET", "/api/v1/pricebooks/"+seed.bookID+"/coverage", 200)
	custs := cov["customers"].([]any)
	if len(custs) != 2 || custs[0].(map[string]any)["name"] != "Acme" || custs[0].(map[string]any)["slug"] != "acme" || custs[1].(map[string]any)["name"] != "Bravo" {
		t.Fatalf("coverage customers = %+v", custs)
	}
	skus := cov["skus_in_use"].([]any)
	if len(skus) != 2 || cov["coverage_pct"].(float64) != 50 || cov["unpriced_count"].(float64) != 1 {
		t.Fatalf("coverage = %+v", cov)
	}
	for _, x := range skus {
		m := x.(map[string]any)
		switch m["sku"] {
		case "ecs.m7n.xlarge.8":
			if m["priced"] != true || m["unit_price"].(float64) != 0.5 || m["quantity_30d"].(float64) != 48 || m["resources"].(float64) != 1 || m["unit"] != "instance-hour" {
				t.Fatalf("priced sku = %+v", m)
			}
		case "k8s.vcpu":
			if m["priced"] != false || m["unit_price"] != nil || m["quantity_30d"].(float64) != 48 || m["unit"] != "vcpu-hour" {
				t.Fatalf("unpriced sku = %+v", m)
			}
		default:
			t.Fatalf("unexpected sku in coverage: %+v", m)
		}
	}
	// A book nobody uses is fully covered, not 0 %.
	empty := op.must("GET", "/api/v1/pricebooks/"+targetID+"/coverage", 200)
	if empty["coverage_pct"].(float64) != 100 || len(empty["customers"].([]any)) != 0 || len(empty["skus_in_use"].([]any)) != 0 || empty["unpriced_count"].(float64) != 0 {
		t.Fatalf("empty coverage = %+v", empty)
	}
	op.must("GET", "/api/v1/pricebooks/00000000-0000-0000-0000-000000000000/coverage", 404)

	// Delete: refused while assigned, naming the customers; allowed after
	// they are moved to the clone.
	acme.must("DELETE", "/api/v1/pricebooks/"+seed.bookID, 403)
	rec, out = op.do("DELETE", "/api/v1/pricebooks/"+seed.bookID, "", nil)
	if rec.Code != 409 {
		t.Fatalf("delete assigned book = %d %s", rec.Code, rec.Body.String())
	}
	names := out["details"].(map[string]any)["customers"].([]any)
	if len(names) != 2 || names[0] != "Acme" || names[1] != "Bravo" || !strings.Contains(out["error"].(string), "2 customer(s)") {
		t.Fatalf("409 details = %+v", out)
	}
	op.must("GET", "/api/v1/pricebooks/"+seed.bookID, 200)
	for _, cid := range []string{seed.acme.ID, seed.bravo.ID} {
		op.mustJSON("PATCH", "/api/v1/customers/"+cid, map[string]any{"price_book_id": cloneID}, 200)
	}
	op.must("DELETE", "/api/v1/pricebooks/"+seed.bookID, 200)
	op.must("GET", "/api/v1/pricebooks/"+seed.bookID, 404)
	op.must("DELETE", "/api/v1/pricebooks/"+seed.bookID, 404)
	// Coverage now follows the clone, which prices the same SKU.
	if cov := op.must("GET", "/api/v1/pricebooks/"+cloneID+"/coverage", 200); len(cov["customers"].([]any)) != 2 || cov["coverage_pct"].(float64) != 50 {
		t.Fatalf("clone coverage = %+v", cov)
	}
	for _, a := range []string{"pricebook.clone", "pricebook.item.add", "pricebook.item.update", "pricebook.item.delete", "pricebook.delete"} {
		if auditActions(t, st, a) == 0 {
			t.Fatalf("no audit entry for %s", a)
		}
	}
}

func TestIntegrationDiscountsGlobalAndCRUD(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	seed := seedCRUD(t, st)
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)
	acme := &client{t: t, h: h}
	acme.signIn(acmeAdmin, mail)

	// Global campaign: customer_id absent → null, no customer_name.
	rec, launch := op.json("POST", "/api/v1/discounts", map[string]any{"name": "Launch", "kind": "percent", "value": 10})
	if rec.Code != 201 {
		t.Fatalf("global create = %d %s", rec.Code, rec.Body.String())
	}
	if v, present := launch["customer_id"]; !present || v != nil {
		t.Fatalf("global discount customer_id must be present and null: %s", rec.Body.String())
	}
	if _, present := launch["customer_name"]; present {
		t.Fatalf("global discount must carry no customer_name: %s", rec.Body.String())
	}
	if launch["value"].(float64) != 10 || launch["active"] != true || launch["kind"] != "percent" {
		t.Fatalf("global discount = %+v", launch)
	}
	launchID := launch["id"].(string)

	// Per-customer through the global route, and through the customer route
	// with a string value (the pre-#6867 body shape still works).
	deal := op.mustJSON("POST", "/api/v1/discounts", map[string]any{"customer_id": seed.acme.ID, "name": "Acme deal", "kind": "fixed", "value": "5"}, 201)
	if deal["customer_id"] != seed.acme.ID || deal["customer_name"] != "Acme" || deal["value"].(float64) != 5 {
		t.Fatalf("customer discount = %+v", deal)
	}
	bravoDeal := op.mustJSON("POST", "/api/v1/customers/"+seed.bravo.ID+"/discounts", map[string]any{"name": "Bravo intro", "kind": "percent", "value": "20", "sku": "eip", "starts_at": "2026-08-01", "ends_at": "2026-12-31"}, 201)
	if bravoDeal["customer_id"] != seed.bravo.ID || bravoDeal["customer_name"] != "Bravo" || bravoDeal["sku"] != "eip" {
		t.Fatalf("bravo discount = %+v", bravoDeal)
	}
	bravoID := bravoDeal["id"].(string)

	// Validation is shared by every route.
	op.mustJSON("POST", "/api/v1/discounts", map[string]any{"customer_id": "00000000-0000-0000-0000-000000000000", "name": "x", "kind": "percent", "value": 1}, 404)
	op.mustJSON("POST", "/api/v1/discounts", map[string]any{"name": "x", "kind": "percent", "value": 150}, 400)
	op.mustJSON("POST", "/api/v1/discounts", map[string]any{"name": "x", "kind": "fixed", "value": -1}, 400)
	op.mustJSON("POST", "/api/v1/discounts", map[string]any{"name": "x", "kind": "coupon", "value": 1}, 400)
	op.mustJSON("POST", "/api/v1/discounts", map[string]any{"name": "", "kind": "percent", "value": 1}, 400)
	op.mustJSON("POST", "/api/v1/discounts", map[string]any{"name": "x", "kind": "percent", "value": 1, "starts_at": "2026-12-01", "ends_at": "2026-01-01"}, 400)
	op.mustJSON("POST", "/api/v1/customers/"+seed.acme.ID+"/discounts", map[string]any{"customer_id": seed.bravo.ID, "name": "x", "kind": "percent", "value": 1}, 400)

	// Operator list: everything, newest first.
	all := op.must("GET", "/api/v1/discounts", 200)["discounts"].([]any)
	if len(all) != 3 {
		t.Fatalf("all discounts = %+v", all)
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].(map[string]any)["created_at"].(string) < all[i].(map[string]any)["created_at"].(string) {
			t.Fatalf("all discounts not newest first: %+v", all)
		}
	}
	if got := op.must("GET", "/api/v1/discounts/"+launchID, 200); got["name"] != "Launch" {
		t.Fatalf("get = %+v", got)
	}
	op.must("GET", "/api/v1/discounts/00000000-0000-0000-0000-000000000000", 404)

	// A customer sees its own discounts AND the global campaign, flagged by
	// the null customer_id — and never another customer's.
	mine := acme.must("GET", "/api/v1/customers/"+seed.acme.ID+"/discounts", 200)["discounts"].([]any)
	if len(mine) != 2 {
		t.Fatalf("acme discounts = %+v", mine)
	}
	var sawGlobal, sawOwn bool
	for _, d := range mine {
		m := d.(map[string]any)
		if m["id"] == launchID && m["customer_id"] == nil {
			sawGlobal = true
		}
		if m["id"] == deal["id"] && m["customer_id"] == seed.acme.ID {
			sawOwn = true
		}
		if m["id"] == bravoID {
			t.Fatal("acme can see bravo's discount")
		}
	}
	if !sawGlobal || !sawOwn {
		t.Fatalf("acme list = %+v", mine)
	}
	acme.must("GET", "/api/v1/customers/"+seed.bravo.ID+"/discounts", 404)

	// The operator-only wall: list, get, create, update, toggle, delete.
	acme.must("GET", "/api/v1/discounts", 403)
	acme.must("GET", "/api/v1/discounts/"+launchID, 403)
	acme.mustJSON("POST", "/api/v1/discounts", map[string]any{"name": "self", "kind": "percent", "value": 100}, 403)
	acme.mustJSON("POST", "/api/v1/customers/"+seed.acme.ID+"/discounts", map[string]any{"name": "self", "kind": "percent", "value": 100}, 403)
	acme.mustJSON("PUT", "/api/v1/discounts/"+bravoID, map[string]any{"name": "x", "kind": "percent", "value": 1}, 403)
	acme.mustJSON("PATCH", "/api/v1/discounts/"+bravoID, map[string]any{"active": false}, 403)
	acme.must("DELETE", "/api/v1/discounts/"+bravoID, 403)

	// PUT replaces every field: scope to a customer, then back to global.
	upd := op.mustJSON("PUT", "/api/v1/discounts/"+launchID, map[string]any{"customer_id": seed.acme.ID, "name": "Launch (Acme)", "kind": "percent", "value": 15, "sku": "eip", "active": false, "starts_at": "2026-09-01T00:00:00Z"}, 200)
	if upd["customer_id"] != seed.acme.ID || upd["customer_name"] != "Acme" || upd["value"].(float64) != 15 || upd["sku"] != "eip" || upd["active"] != false || upd["starts_at"] == nil || upd["ends_at"] != nil {
		t.Fatalf("put = %+v", upd)
	}
	upd = op.mustJSON("PUT", "/api/v1/discounts/"+launchID, map[string]any{"customer_id": nil, "name": "Launch", "kind": "percent", "value": 10}, 200)
	if upd["customer_id"] != nil || upd["sku"] != nil || upd["starts_at"] != nil || upd["active"] != false {
		t.Fatalf("put back to global = %+v", upd)
	}
	op.mustJSON("PUT", "/api/v1/discounts/"+launchID, map[string]any{"name": "Launch", "kind": "percent", "value": 500}, 400)
	op.mustJSON("PUT", "/api/v1/discounts/00000000-0000-0000-0000-000000000000", map[string]any{"name": "x", "kind": "percent", "value": 1}, 404)
	// The existing toggle route still works and reads back.
	op.mustJSON("PATCH", "/api/v1/discounts/"+launchID, map[string]any{"active": true}, 200)
	if got := op.must("GET", "/api/v1/discounts/"+launchID, 200); got["active"] != true {
		t.Fatalf("toggle did not stick: %+v", got)
	}

	// Delete.
	op.must("DELETE", "/api/v1/discounts/"+bravoID, 200)
	op.must("GET", "/api/v1/discounts/"+bravoID, 404)
	op.must("DELETE", "/api/v1/discounts/"+bravoID, 404)
	if left := op.must("GET", "/api/v1/discounts", 200)["discounts"].([]any); len(left) != 2 {
		t.Fatalf("after delete = %+v", left)
	}
	for _, a := range []string{"discount.create", "discount.update", "discount.delete", "discount.active"} {
		if auditActions(t, st, a) == 0 {
			t.Fatalf("no audit entry for %s", a)
		}
	}
}

func TestIntegrationSourcePatchScopeAndReverify(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	seed := seedCRUD(t, st)
	ctx := context.Background()
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)
	acme := &client{t: t, h: h}
	acme.signIn(acmeAdmin, mail)
	bravo := &client{t: t, h: h}
	bravo.signIn(bravoAdmin, mail)
	viewer := &client{t: t, h: h}
	viewer.signIn(acmeViewer, mail)

	if err := st.SetSourceVerified(ctx, seed.srcA.ID, "dom-old"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSourceError(ctx, seed.srcA.ID, "collector: throttled"); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/sources/" + seed.srcA.ID

	// Customer admin of the owner: scope_token only; status untouched.
	out := acme.mustJSON("PATCH", path, map[string]any{"scope_token": "dep-1234"}, 200)
	if out["scope_token"] != "dep-1234" || out["status"] != "verified" || out["last_error"] != "collector: throttled" {
		t.Fatalf("customer scope patch = %+v", out)
	}
	acme.mustJSON("PATCH", path, map[string]any{"region": "me-east-216"}, 403)
	acme.mustJSON("PATCH", path, map[string]any{"project_id": "ok-z"}, 403)
	acme.mustJSON("PATCH", path, map[string]any{"domain_id": "d"}, 403)
	acme.mustJSON("PATCH", path, map[string]any{"scope_token": "x", "region": "me-east-216"}, 403)
	// Another customer: not even confirmed to exist. A viewer: forbidden.
	bravo.mustJSON("PATCH", path, map[string]any{"scope_token": "steal"}, 404)
	viewer.mustJSON("PATCH", path, map[string]any{"scope_token": "x"}, 403)
	if src, _ := st.GetSource(ctx, store.OperatorScope, seed.srcA.ID); src.ScopeToken != "dep-1234" {
		t.Fatalf("scope token changed by a refused call: %+v", src)
	}

	// Operator: region change resets verification.
	out = op.mustJSON("PATCH", path, map[string]any{"region": "me-east-216"}, 200)
	if out["region"] != "me-east-216" || out["status"] != "pending" || out["last_error"] != nil || out["verified_at"] != nil || out["collecting"] != false || out["scope_token"] != "dep-1234" {
		t.Fatalf("operator region patch = %+v", out)
	}
	out = op.mustJSON("PATCH", path, map[string]any{"domain_id": "dom-new", "scope_token": ""}, 200)
	if out["domain_id"] != "dom-new" || out["status"] != "pending" || out["scope_token"] != nil {
		t.Fatalf("operator domain patch = %+v", out)
	}
	op.mustJSON("PATCH", path, map[string]any{}, 400)
	op.mustJSON("PATCH", path, map[string]any{"project_id": ""}, 400)
	op.mustJSON("PATCH", path, map[string]any{"region": " "}, 400)
	op.mustJSON("PATCH", path, map[string]any{"colour": "red"}, 400)
	op.mustJSON("PATCH", "/api/v1/sources/00000000-0000-0000-0000-000000000000", map[string]any{"region": "x"}, 404)
	// Moving onto another source's (region, project) of the same customer
	// collides with the unique key.
	if _, _, err := st.UpsertSource(ctx, seed.acme.ID, "huawei-project", "me-east-215", "ok-a2"); err != nil {
		t.Fatal(err)
	}
	op.mustJSON("PATCH", path, map[string]any{"region": "me-east-215", "project_id": "ok-a2"}, 409)

	audit := op.must("GET", "/api/v1/customers/"+seed.acme.ID+"/audit", 200)
	auditJSON, _ := json.Marshal(audit)
	for _, needle := range []string{`"source.update"`, `"fields":["scope_token"]`, `"fields":["region"]`, `"fields":["scope_token","domain_id"]`} {
		if !strings.Contains(string(auditJSON), needle) {
			t.Fatalf("audit lacks %s: %s", needle, auditJSON)
		}
	}
}

func TestIntegrationStatementDeleteDraftsOnly(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	seed := seedCRUD(t, st)
	ctx := context.Background()
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)
	acme := &client{t: t, h: h}
	acme.signIn(acmeAdmin, mail)

	run := op.mustJSON("POST", "/api/v1/statements/run", map[string]string{"period": "2026-08"}, 200)
	acmeStmt, bravoStmt := statementFor(t, run, seed.acme.ID), statementFor(t, run, seed.bravo.ID)
	if n := len(op.must("GET", "/api/v1/statements/"+acmeStmt, 200)["lines"].([]any)); n != 1 {
		t.Fatalf("draft lines = %d", n)
	}
	acme.must("DELETE", "/api/v1/statements/"+acmeStmt, 403)
	op.must("DELETE", "/api/v1/statements/"+acmeStmt, 200)
	op.must("GET", "/api/v1/statements/"+acmeStmt, 404)
	op.must("DELETE", "/api/v1/statements/"+acmeStmt, 404)
	var lines int
	if err := st.DB().QueryRowContext(ctx, `SELECT count(*) FROM rated_lines WHERE statement_id = $1`, acmeStmt).Scan(&lines); err != nil || lines != 0 {
		t.Fatalf("rated lines after delete = %d err=%v", lines, err)
	}
	// The period is free again: a re-run writes a fresh draft.
	run = op.mustJSON("POST", "/api/v1/statements/run", map[string]string{"period": "2026-08", "customer_id": seed.acme.ID}, 200)
	if fresh := statementFor(t, run, seed.acme.ID); fresh == "" || fresh == acmeStmt {
		t.Fatalf("re-run after delete = %+v", run)
	}
	// Issued → 409, and it is still there.
	op.mustJSON("POST", "/api/v1/statements/"+bravoStmt+"/issue", nil, 200)
	rec, out := op.do("DELETE", "/api/v1/statements/"+bravoStmt, "", nil)
	if rec.Code != 409 || !strings.Contains(out["error"].(string), "issued") {
		t.Fatalf("delete issued = %d %s", rec.Code, rec.Body.String())
	}
	if got := op.must("GET", "/api/v1/statements/"+bravoStmt, 200); got["status"] != "issued" {
		t.Fatalf("issued statement after refused delete = %+v", got)
	}
	if auditActions(t, st, "statement.delete") != 1 {
		t.Fatal("statement.delete not audited")
	}
}

func TestIntegrationSavedViewsPerUser(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	seedCRUD(t, st)
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)
	acme := &client{t: t, h: h}
	acme.signIn(acmeAdmin, mail)
	viewer := &client{t: t, h: h}
	viewer.signIn(acmeViewer, mail)
	anon := &client{t: t, h: h}

	anon.must("GET", "/api/v1/views", 401)
	anon.mustJSON("POST", "/api/v1/views", map[string]any{"name": "x"}, 401)

	params := map[string]any{"group_by": "kind", "from": "2026-09-01", "to": "2026-10-01", "kind": []string{"ecs", "evs"}}
	v := acme.mustJSON("POST", "/api/v1/views", map[string]any{"name": "Compute this month", "params": params}, 201)
	if v["page"] != "explore" || v["owner_email"] != acmeAdmin || v["params"].(map[string]any)["group_by"] != "kind" || v["id"] == "" {
		t.Fatalf("view = %+v", v)
	}
	viewID := v["id"].(string)
	acme.mustJSON("POST", "/api/v1/views", map[string]any{"name": "Compute this month", "params": params}, 409)
	acme.mustJSON("POST", "/api/v1/views", map[string]any{"name": "compute this month", "page": "explore", "params": params}, 201) // names are case-sensitive
	acme.mustJSON("POST", "/api/v1/views", map[string]any{"name": "Compute this month", "page": "resources", "params": map[string]any{"kind": "ecs"}}, 201)
	acme.mustJSON("POST", "/api/v1/views", map[string]any{"name": "", "params": params}, 400)
	acme.mustJSON("POST", "/api/v1/views", map[string]any{"name": "list", "params": []int{1, 2}}, 400)
	acme.mustJSON("POST", "/api/v1/views", map[string]any{"name": "no params"}, 201)

	got := acme.must("GET", "/api/v1/views", 200)["views"].([]any)
	if len(got) != 3 {
		t.Fatalf("explore views = %+v", got)
	}
	var sawView bool
	for _, x := range got {
		sawView = sawView || x.(map[string]any)["id"] == viewID
	}
	if !sawView {
		t.Fatalf("explore views lack the first one: %+v", got)
	}
	if got := acme.must("GET", "/api/v1/views?page=resources", 200)["views"].([]any); len(got) != 1 {
		t.Fatalf("resources views = %+v", got)
	}
	// Views are per user: the same customer's viewer and the operator see
	// none of them, and cannot delete them (404, not confirmed to exist).
	if got := viewer.must("GET", "/api/v1/views", 200)["views"].([]any); len(got) != 0 {
		t.Fatalf("viewer sees admin's views: %+v", got)
	}
	if got := op.must("GET", "/api/v1/views", 200)["views"].([]any); len(got) != 0 {
		t.Fatalf("operator sees customer's views: %+v", got)
	}
	op.must("DELETE", "/api/v1/views/"+viewID, 404)
	viewer.must("DELETE", "/api/v1/views/"+viewID, 404)
	// The operator's own views are separate.
	opView := op.mustJSON("POST", "/api/v1/views", map[string]any{"name": "Compute this month", "params": params}, 201)
	if got := op.must("GET", "/api/v1/views", 200)["views"].([]any); len(got) != 1 || got[0].(map[string]any)["id"] != opView["id"] {
		t.Fatalf("operator views = %+v", got)
	}
	acme.must("DELETE", "/api/v1/views/"+viewID, 200)
	acme.must("DELETE", "/api/v1/views/"+viewID, 404)
	if got := acme.must("GET", "/api/v1/views", 200)["views"].([]any); len(got) != 2 {
		t.Fatalf("after delete = %+v", got)
	}
	// Five creates succeeded (four for acme, one for the operator); the 409
	// and 400 paths write no audit entry.
	if auditActions(t, st, "view.create") != 5 || auditActions(t, st, "view.delete") != 1 {
		t.Fatalf("view audit = create %d delete %d", auditActions(t, st, "view.create"), auditActions(t, st, "view.delete"))
	}
}
