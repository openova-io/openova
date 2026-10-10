package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The packages and the entitlement matrix, end to end (DESIGN.md §22): the
// features are created through the API, the matrix written cell by cell with
// its rules enforced (optional needs a priced add-on, a quantity feature
// needs its quantity, a feature in use cannot be deleted), a Source takes an
// add-on (refused where the package includes it or does not offer it), the
// statement run bills the add-on and lists the included features at 0.000
// and the included bandwidth as an allowance, and the public document is
// served with no session, cacheable, in the storefront's shape.

func TestIntegrationPackagesMatrixAddonsAndBilling(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	ctx := context.Background()
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)

	plans, _, err := st.EnsurePlanBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bookPath := "/api/v1/pricebooks/" + plans.ID

	// ── features ───────────────────────────────────────────────────────
	backup := op.mustJSON("POST", "/api/v1/features", map[string]any{"key": "backup", "name": "Backup", "blurb": "Daily backups, kept 30 days", "kind": "boolean", "addon_sku": "addon.backup", "sort_order": 4}, 201)
	op.mustJSON("POST", "/api/v1/features", map[string]any{"key": "ssl", "name": "Unlimited free SSL", "kind": "boolean", "sort_order": 6}, 201)
	op.mustJSON("POST", "/api/v1/features", map[string]any{"key": "bandwidth", "name": "Bandwidth", "kind": "quantity", "unit": "Mbps", "addon_sku": "eip.bandwidth_mbps", "sort_order": 15}, 201)
	if rec, out := op.json("POST", "/api/v1/features", map[string]any{"key": "backup", "name": "Backup again"}); rec.Code != 409 || !strings.Contains(out["error"].(string), "already exists") {
		t.Fatalf("duplicate key = %d %v", rec.Code, out)
	}
	if rec, out := op.json("POST", "/api/v1/features", map[string]any{"key": "storage", "name": "Storage", "kind": "quantity"}); rec.Code != 400 || !strings.Contains(out["error"].(string), "needs a unit") {
		t.Fatalf("quantity without unit = %d %v", rec.Code, out)
	}
	if rec, _ := op.json("POST", "/api/v1/features", map[string]any{"key": "Bad Key", "name": "x"}); rec.Code != 400 {
		t.Fatalf("bad key = %d", rec.Code)
	}
	list := op.must("GET", "/api/v1/features", 200)["features"].([]any)
	if len(list) != 3 || list[0].(map[string]any)["key"] != "backup" || list[2].(map[string]any)["key"] != "bandwidth" {
		t.Fatalf("features = %v", list)
	}

	// ── the matrix ─────────────────────────────────────────────────────
	// Optional needs a priced add-on: refused naming the SKU and the book.
	rec, out := op.json("PUT", bookPath+"/packages/plan.s/features/backup", map[string]any{"state": "optional"})
	if rec.Code != 400 || !strings.Contains(out["error"].(string), "addon.backup is not priced in "+store.PlanBookName) {
		t.Fatalf("optional without a price = %d %v", rec.Code, out)
	}
	// Pricing the add-on in the same write: 1.500 OMR/month → annual 18 →
	// 18 / 8760 = 0.00205479 per plan-hour.
	cell := op.mustJSON("PUT", bookPath+"/packages/plan.s/features/backup", map[string]any{"state": "optional", "addon_monthly": "1.500", "note": "7-day retention"}, 200)
	if cell["state"] != "optional" || cell["note"] != "7-day retention" {
		t.Fatalf("cell = %v", cell)
	}
	_, item := getExact(t, op, bookPath, 200)
	var addonPriced bool
	for _, it := range item["items"].([]any) {
		m := it.(map[string]any)
		if m["sku"] == "addon.backup" {
			addonPriced = true
			if m["unit"] != store.PlanUnit || dec(t, m["unit_price"]) != "0.00205479" || dec(t, m["annual_price"]) != "18.00000000" {
				t.Fatalf("add-on item = %v", m)
			}
		}
	}
	if !addonPriced {
		t.Fatal("addon.backup was not priced by the cell write")
	}
	// A note left out keeps the one that is there.
	cell = op.mustJSON("PUT", bookPath+"/packages/plan.s/features/backup", map[string]any{"state": "optional"}, 200)
	if cell["note"] != "7-day retention" {
		t.Fatalf("note after a write without one = %v", cell["note"])
	}
	op.mustJSON("PUT", bookPath+"/packages/plan.m/features/backup", map[string]any{"state": "optional"}, 200)
	op.mustJSON("PUT", bookPath+"/packages/plan.l/features/backup", map[string]any{"state": "optional"}, 200)
	op.mustJSON("PUT", bookPath+"/packages/plan.xl/features/backup", map[string]any{"state": "included"}, 200)
	for _, p := range []string{"s", "m", "l", "xl"} {
		op.mustJSON("PUT", bookPath+"/packages/plan."+p+"/features/ssl", map[string]any{"state": "included"}, 200)
	}
	// A quantity feature marked included needs its quantity.
	if rec, out := op.json("PUT", bookPath+"/packages/plan.s/features/bandwidth", map[string]any{"state": "included"}); rec.Code != 400 || !strings.Contains(out["error"].(string), "how much the S package includes") {
		t.Fatalf("quantity without quantity = %d %v", rec.Code, out)
	}
	op.mustJSON("PUT", bookPath+"/packages/plan.s/features/bandwidth", map[string]any{"state": "included", "included_quantity": "50"}, 200)
	op.mustJSON("PUT", bookPath+"/packages/plan.xl/features/bandwidth", map[string]any{"state": "included", "included_quantity": "1000"}, 200)
	// Not a package: a cloud SKU, an unpriced plan, flexi.
	if rec, _ := op.json("PUT", bookPath+"/packages/ecs.s7n.small.1/features/backup", map[string]any{"state": "included"}); rec.Code != 400 {
		t.Fatalf("a cloud SKU as a package = %d", rec.Code)
	}
	if rec, _ := op.json("PUT", bookPath+"/packages/plan.flexi/features/backup", map[string]any{"state": "included"}); rec.Code != 400 {
		t.Fatalf("flexi as a package = %d", rec.Code)
	}
	// The bandwidth meter has to be priced in the plans book for the excess
	// over the included quantity to be billed.
	op.mustJSON("POST", bookPath+"/items", map[string]any{"sku": "eip.bandwidth_mbps", "unit": "mbps-hour", "unit_price": "0.01716667"}, 201)

	// ── the document, authenticated and public, identical ──────────────
	authed, authDoc := getExact(t, op, bookPath+"/packages", 200)
	anon := anonClient(t, h)
	public, pubDoc := getExact(t, anon, "/api/v1/public/packages", 200)
	if authed.Body.String() != public.Body.String() {
		t.Fatalf("the console and the public documents differ:\n%s\n%s", authed.Body.String(), public.Body.String())
	}
	if cc := public.Header().Get("Cache-Control"); cc != "public, max-age=60" {
		t.Fatalf("public Cache-Control = %q", cc)
	}
	if cc := authed.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("console Cache-Control = %q", cc)
	}
	if pubDoc["currency"] != "OMR" || pubDoc["price_book"] != store.PlanBookName {
		t.Fatalf("doc = %v", pubDoc)
	}
	pk := pubDoc["packages"].([]any)
	if len(pk) != 4 || pk[0].(map[string]any)["sku"] != "plan.s" || pk[3].(map[string]any)["sku"] != "plan.xl" || dec(t, pk[0].(map[string]any)["price_month"]) != `"5.000"` {
		t.Fatalf("packages = %v", pk)
	}
	if inc := pk[0].(map[string]any)["includes"].(map[string]any); dec(t, inc["vcpu"]) != "2" || dec(t, inc["memory_gb"]) != "4" || dec(t, inc["bandwidth_mbps"]) != "50" {
		t.Fatalf("S includes = %v", inc)
	}
	fs := pubDoc["features"].([]any)
	if len(fs) != 3 || fs[0].(map[string]any)["key"] != "backup" {
		t.Fatalf("features = %v", fs)
	}
	bk := fs[0].(map[string]any)["cells"].(map[string]any)
	if c := bk["plan.s"].(map[string]any); c["state"] != "optional" || c["addon_sku"] != "addon.backup" || dec(t, c["price_month"]) != `"1.500"` || c["included_from"] != "plan.xl" {
		t.Fatalf("backup on S = %v", c)
	}
	if c := bk["plan.xl"].(map[string]any); c["state"] != "included" || c["included_from"] != nil {
		t.Fatalf("backup on XL = %v", c)
	}
	if c := fs[2].(map[string]any)["cells"].(map[string]any)["plan.m"].(map[string]any); c["state"] != "not_offered" {
		t.Fatalf("bandwidth on M (no cell) = %v, want not_offered", c)
	}
	_ = authDoc

	// ── a Source takes an add-on ───────────────────────────────────────
	cust := op.mustJSON("POST", "/api/v1/customers", map[string]any{"slug": "nizwa", "name": "Nizwa Fintech", "admin_email": "ops@nizwa.example", "kind": "organization", "org_slug": "nizwa", "plan_slug": "m", "billing_mode": "chargeback"}, 201)
	cid := cust["id"].(string)
	op.mustJSON("PATCH", "/api/v1/customers/"+cid, map[string]any{"status": "active"}, 200)
	src, _, err := st.UpsertSource(ctx, cid, store.SourceKindOrg, "", "nizwa")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSourceVerified(ctx, src.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSourcePriceBook(ctx, src.ID, plans.ID); err != nil {
		t.Fatal(err)
	}
	addonsPath := "/api/v1/customers/" + cid + "/sources/" + src.ID + "/addons"
	// Not offered on M (no cell): refused naming the feature and the package.
	if rec, out := op.json("PUT", addonsPath, map[string]any{"addons": []string{"bandwidth"}}); rec.Code != 400 || !strings.Contains(out["error"].(string), "Bandwidth is not offered on the M package") {
		t.Fatalf("not offered = %d %v", rec.Code, out)
	}
	// Included on M: redundant.
	if rec, out := op.json("PUT", addonsPath, map[string]any{"addons": []string{"ssl"}}); rec.Code != 400 || !strings.Contains(out["error"].(string), "Unlimited free SSL is included in the M package") {
		t.Fatalf("redundant = %d %v", rec.Code, out)
	}
	if rec, _ := op.json("PUT", addonsPath, map[string]any{"addons": []string{"nope"}}); rec.Code != 400 {
		t.Fatalf("unknown feature = %d", rec.Code)
	}
	taken := op.mustJSON("PUT", addonsPath, map[string]any{"addons": []string{"backup"}}, 200)
	if got := taken["addons"].([]any); len(got) != 1 || got[0] != "backup" {
		t.Fatalf("addons = %v", taken["addons"])
	}
	// The Source document carries them from now on.
	srcDoc := op.must("GET", "/api/v1/customers/"+cid+"/sources/"+src.ID, 200)
	if got := srcDoc["addons"].([]any); len(got) != 1 || got[0] != "backup" {
		t.Fatalf("source addons = %v", srcDoc["addons"])
	}
	// A cloud source's document says none, never a missing key.
	if _, has := srcDoc["addons"]; !has {
		t.Fatal("addons key missing")
	}

	// ── the bill ───────────────────────────────────────────────────────
	// July 2026 on M around the clock: 744 plan-hours, bandwidth 70 Mbps
	// always on on a package that includes 50 (written below), the add-on taken.
	op.mustJSON("PUT", bookPath+"/packages/plan.m/features/bandwidth", map[string]any{"state": "included", "included_quantity": "50"}, 200)
	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	var recs []store.UsageRecord
	for hr := 0; hr < 744; hr++ {
		at := from.Add(time.Duration(hr) * time.Hour)
		recs = append(recs,
			store.UsageRecord{CustomerID: cid, SourceID: src.ID, ResourceID: "plan/m", ResourceKind: store.PlanKind, SKU: "plan.m", Quantity: "1", Unit: store.PlanUnit, WindowStart: at, WindowEnd: at.Add(time.Hour)},
			store.UsageRecord{CustomerID: cid, SourceID: src.ID, ResourceID: "eip/nizwa", ResourceKind: "eip", SKU: "eip.bandwidth_mbps", Quantity: "70", Unit: "mbps-hour", WindowStart: at, WindowEnd: at.Add(time.Hour)},
		)
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	run := postExact(t, op, "/api/v1/statements/run", map[string]any{"period": "2026-07", "customer_id": cid}, 200)
	results := run["results"].([]any)
	if len(results) != 1 || results[0].(map[string]any)["error"] != nil {
		t.Fatalf("run = %v", run)
	}
	res := results[0].(map[string]any)
	stID := res["statement_id"].(string)
	_, stmt := getExact(t, op, "/api/v1/statements/"+stID, 200)
	lines := map[string]map[string]any{}
	for _, l := range stmt["lines"].([]any) {
		m := l.(map[string]any)
		lines[m["sku"].(string)] = m
	}
	// The plan line: 744 × 0.01232877 = 9.172605.
	if l := lines["plan.m"]; l == nil || dec(t, l["amount"]) != "9.172605" {
		t.Fatalf("plan line = %v", l)
	}
	// The add-on: 744 plan-hours at 0.00205479 = 1.528764, named.
	if l := lines["addon.backup"]; l == nil || dec(t, l["amount"]) != "1.528764" || dec(t, l["quantity"]) != "744.000000" || l["unit"] != store.PlanUnit || l["description"] != "Backup — add-on to M plan" {
		t.Fatalf("add-on line = %v", l)
	}
	// SSL included: a 0.000 line under the plan, described.
	if l := lines["plan.m.ssl"]; l == nil || dec(t, l["amount"]) != "0.000000" || dec(t, l["unit_price"]) != "0.00000000" || l["description"] != "Unlimited free SSL — included in M plan" {
		t.Fatalf("included line = %v", l)
	}
	// Bandwidth: 52,080 mbps-hours used, 37,200 included → 14,880 priced at
	// 0.01716667 = 255.440050; the breakdown reports the allowance.
	if l := lines["eip.bandwidth_mbps"]; l == nil || dec(t, l["amount"]) != "255.440050" {
		t.Fatalf("bandwidth line = %v", l)
	}
	var sawAllowance bool
	for _, br := range res["applied_terms"].([]any) {
		m := br.(map[string]any)
		if m["sku"] == "eip.bandwidth_mbps" {
			sawAllowance = true
			if dec(t, m["allowance"]) != "37200.000000" || dec(t, m["excess"]) != "14880.000000" {
				t.Fatalf("bandwidth breakdown = %v", m)
			}
		}
	}
	if !sawAllowance {
		t.Fatalf("applied terms %v carry no bandwidth allowance", res["applied_terms"])
	}
	// 9.172605 + 1.528764 + 0 + 255.440050 = 266.141419
	if dec(t, stmt["subtotal"]) != "266.141419" {
		t.Fatalf("subtotal = %s", dec(t, stmt["subtotal"]))
	}

	// ── a feature in use cannot be deleted; one that is not, can ───────
	rec, out = op.do("DELETE", "/api/v1/features/"+backup["id"].(string), "", nil)
	if rec.Code != 409 || !strings.Contains(out["error"].(string), "backup is still used by 4 package cell(s) of "+store.PlanBookName+" and 1 source(s)") {
		t.Fatalf("delete in use = %d %v", rec.Code, out)
	}
	if det := out["details"].(map[string]any); det["cells"] != float64(4) || det["sources"] != float64(1) {
		t.Fatalf("details = %v", det)
	}
	spare := op.mustJSON("POST", "/api/v1/features", map[string]any{"key": "spare", "name": "Spare"}, 201)
	op.mustJSON("PUT", bookPath+"/packages/plan.s/features/spare", map[string]any{"state": "not_offered"}, 200)
	if rec, _ := op.do("DELETE", "/api/v1/features/spare", "", nil); rec.Code != 409 {
		t.Fatalf("delete with a not_offered cell = %d (a cell is a dependency whatever its state)", rec.Code)
	}
	op.must("DELETE", bookPath+"/packages/plan.s/features/spare", 200)
	op.must("DELETE", "/api/v1/features/"+spare["id"].(string), 200)
	// Renaming a key is refused; a kind change on a feature in the matrix too.
	if rec, _ := op.json("PATCH", "/api/v1/features/backup", map[string]any{"key": "backups"}); rec.Code != 400 {
		t.Fatalf("rename = %d", rec.Code)
	}
	if rec, out := op.json("PATCH", "/api/v1/features/backup", map[string]any{"kind": "quantity", "unit": "GB"}); rec.Code != 409 || !strings.Contains(out["error"].(string), "kind cannot change") {
		t.Fatalf("kind change in use = %d %v", rec.Code, out)
	}
	// Clearing the add-on SKU while packages offer the feature as optional is refused.
	if rec, out := op.json("PATCH", "/api/v1/features/backup", map[string]any{"addon_sku": ""}); rec.Code != 409 || !strings.Contains(out["error"].(string), "needs an add-on SKU") {
		t.Fatalf("clear addon_sku = %d %v", rec.Code, out)
	}
	patched := op.mustJSON("PATCH", "/api/v1/features/backup", map[string]any{"blurb": "Nightly, kept 30 days"}, 200)
	if patched["blurb"] != "Nightly, kept 30 days" {
		t.Fatalf("patched = %v", patched)
	}

	// ── a clone carries the matrix; deleting the clone drops its cells ──
	clone := op.mustJSON("POST", bookPath+"/clone", map[string]any{"name": "Nizwa negotiated packages"}, 201)
	cloneDoc := op.must("GET", "/api/v1/pricebooks/"+clone["id"].(string)+"/packages", 200)
	if len(cloneDoc["features"].([]any)) != 3 {
		t.Fatalf("clone features = %v", cloneDoc["features"])
	}
	op.must("DELETE", "/api/v1/pricebooks/"+clone["id"].(string), 200)
	if n := cellCount(t, st, `SELECT count(*) FROM package_entitlements`); n != 11 {
		t.Fatalf("%d cells after deleting the clone, want the plans book's 11", n)
	}

	// ── who may ────────────────────────────────────────────────────────
	if rec, _ := anon.json("POST", "/api/v1/features", map[string]any{"key": "x", "name": "x"}); rec.Code != 401 {
		t.Fatalf("anonymous create = %d", rec.Code)
	}
	if rec, _ := anon.do("GET", bookPath+"/packages", "", nil); rec.Code != 401 {
		t.Fatalf("anonymous console document = %d", rec.Code)
	}
	if rec, _ := anon.do("GET", "/api/v1/features", "", nil); rec.Code != 401 {
		t.Fatalf("anonymous features = %d", rec.Code)
	}
}

// The public document answers 404 while no plans book exists, and never
// reads a session.
func TestIntegrationPublicPackagesWithoutPlansBook(t *testing.T) {
	h, _, _, _, _ := setupAPI(t)
	anon := anonClient(t, h)
	rec, out := anon.do("GET", "/api/v1/public/packages", "", nil)
	if rec.Code != 404 || out["error"] != "no packages are published yet" {
		t.Fatalf("no plans book = %d %v", rec.Code, out)
	}
}

// An add-on in a public estimate is a whole month like the plan it extends:
// it takes months, never hours, and prices per plan-hour × 730 × months.
func TestIntegrationPublicEstimatePricesAddonsByTheMonth(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	ctx := context.Background()
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)
	list := op.mustJSON("POST", "/api/v1/pricebooks", map[string]any{"name": "NC list 2026", "annual_divisor": 8760}, 201)
	op.mustJSON("PUT", "/api/v1/pricebooks/"+list["id"].(string)+"/items", map[string]any{"items": []map[string]any{{"sku": "ecs.s6.large.2", "unit": "instance-hour", "unit_price": "0.10000000"}}}, 200)
	op.mustJSON("PUT", "/api/v1/pricebooks/"+list["id"].(string)+"/public", map[string]any{"public": true}, 200)
	plans, _, err := st.EnsurePlanBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	op.mustJSON("POST", "/api/v1/features", map[string]any{"key": "backup", "name": "Backup", "addon_sku": "addon.backup"}, 201)
	op.mustJSON("PUT", "/api/v1/pricebooks/"+plans.ID+"/packages/plan.m/features/backup", map[string]any{"state": "optional", "addon_monthly": "1.500"}, 200)

	anon := anonClient(t, h)
	est := postExact(t, anon, "/api/v1/public/estimates?preview=1", map[string]any{"lines": []map[string]any{
		{"plan": "m", "quantity": "1", "months": 3},
		{"sku": "addon.backup", "quantity": "1", "months": 3},
	}}, 200)
	lines := est["lines"].([]any)
	if len(lines) != 2 {
		t.Fatalf("lines = %v", lines)
	}
	addon := lines[1].(map[string]any)
	// 3 months × 730 h × 0.00205479 = 4.499990
	if dec(t, addon["rated_quantity"]) != "2190.000000" || dec(t, addon["amount"]) != "4.499990" || dec(t, addon["months"]) != "3" {
		t.Fatalf("add-on line = %v", addon)
	}
	// The month: the plan over 3 months + the add-on over 3 months, taxed.
	// (9.000002 + 1.499997) × 1.05 = 11.024999
	if dec(t, est["monthly"]) != "11.024999" {
		t.Fatalf("monthly = %s", dec(t, est["monthly"]))
	}
	if rec, out := anon.json("POST", "/api/v1/public/estimates?preview=1", map[string]any{"lines": []map[string]any{{"sku": "addon.backup", "quantity": "1", "hours_per_month": "100"}}}); rec.Code != 400 || !strings.Contains(out["error"].(string), "an add-on is a whole month") {
		t.Fatalf("add-on with hours = %d %v", rec.Code, out)
	}
	if rec, out := anon.json("POST", "/api/v1/public/estimates?preview=1", map[string]any{"lines": []map[string]any{{"sku": "addon.backup", "quantity": "1", "months": 13}}}); rec.Code != 400 || !strings.Contains(out["error"].(string), "months must be between 1 and 12") {
		t.Fatalf("add-on with 13 months = %d %v", rec.Code, out)
	}
	// A metered SKU still refuses months, as before.
	if rec, out := anon.json("POST", "/api/v1/public/estimates?preview=1", map[string]any{"lines": []map[string]any{{"sku": "ecs.s6.large.2", "quantity": "1", "months": 2}}}); rec.Code != 400 || !strings.Contains(out["error"].(string), "months applies to a plan line") {
		t.Fatalf("metered SKU with months = %d %v", rec.Code, out)
	}
}

func cellCount(t *testing.T, st *store.Store, q string) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}
