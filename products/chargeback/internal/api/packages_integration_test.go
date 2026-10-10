package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The packages and the entitlement matrix, end to end (DESIGN.md §22): the
// features are created through the API in their four kinds, the matrix
// written cell by cell with its rules enforced (optional needs a priced
// add-on, a metered overage needs a priced meter, a level indexes into its
// levels, an access door is never optional, a floor item has no cell, a
// feature in use cannot be deleted), the package settings written and the
// step-up rule computed, a Source takes an add-on (refused where the package
// includes it or does not offer it), the statement run bills the add-on,
// lists the included features at 0.000, applies the metered bandwidth
// allowance and the hard-capped disk, and the public document is served
// with no session, cacheable, in the storefront's shape.

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

	// ── features, one per kind ─────────────────────────────────────────
	backup := op.mustJSON("POST", "/api/v1/features", map[string]any{"key": "backup", "name": "Backup", "blurb": "Daily backups, kept 30 days", "kind": "boolean", "group": "resilience", "addon_sku": "addon.backup", "sort_order": 4}, 201)
	if backup["group"] != "resilience" || backup["teaser"] != false {
		t.Fatalf("feature = %v", backup)
	}
	op.mustJSON("POST", "/api/v1/features", map[string]any{"key": "ssl", "name": "Unlimited free SSL", "kind": "boolean", "group": "floor", "sort_order": 1}, 201)
	op.mustJSON("POST", "/api/v1/features", map[string]any{"key": "bandwidth", "name": "Bandwidth", "kind": "quantity", "group": "capacity", "unit": "Mbps", "addon_sku": "eip.bandwidth_mbps", "sort_order": 2}, 201)
	op.mustJSON("POST", "/api/v1/features", map[string]any{"key": "disk", "name": "Disk", "kind": "quantity", "group": "capacity", "unit": "GB", "addon_sku": "k8s.pvc_gb", "sort_order": 3}, 201)
	dr := op.mustJSON("POST", "/api/v1/features", map[string]any{"key": "dr", "name": "DR topology", "kind": "level", "group": "resilience", "addon_sku": "addon.dr", "levels": []string{"single region", "active-passive"}, "sort_order": 5}, 201)
	if lv := dr["levels"].([]any); len(lv) != 2 || lv[1] != "active-passive" {
		t.Fatalf("level feature = %v", dr)
	}
	op.mustJSON("POST", "/api/v1/features", map[string]any{"key": "gitea_iac", "name": "Gitea + IaC", "kind": "access", "group": "access", "sort_order": 6}, 201)
	op.mustJSON("POST", "/api/v1/features", map[string]any{"key": "vuln", "name": "Vulnerability dashboard", "kind": "boolean", "group": "ops", "teaser": true, "sort_order": 7}, 201)
	// The shape rules: a duplicate key, a quantity without a unit, a bad key,
	// a level with one label, an access door with an add-on, a floor item
	// with an add-on, an unknown group.
	if rec, out := op.json("POST", "/api/v1/features", map[string]any{"key": "backup", "name": "Backup again"}); rec.Code != 409 || !strings.Contains(out["error"].(string), "already exists") {
		t.Fatalf("duplicate key = %d %v", rec.Code, out)
	}
	if rec, out := op.json("POST", "/api/v1/features", map[string]any{"key": "storage", "name": "Storage", "kind": "quantity"}); rec.Code != 400 || !strings.Contains(out["error"].(string), "needs a unit") {
		t.Fatalf("quantity without unit = %d %v", rec.Code, out)
	}
	if rec, _ := op.json("POST", "/api/v1/features", map[string]any{"key": "Bad Key", "name": "x"}); rec.Code != 400 {
		t.Fatalf("bad key = %d", rec.Code)
	}
	if rec, out := op.json("POST", "/api/v1/features", map[string]any{"key": "sla", "name": "SLA", "kind": "level", "levels": []string{"99.9 %"}}); rec.Code != 400 || !strings.Contains(out["error"].(string), "at least two levels") {
		t.Fatalf("level with one label = %d %v", rec.Code, out)
	}
	if rec, out := op.json("POST", "/api/v1/features", map[string]any{"key": "door", "name": "Door", "kind": "access", "addon_sku": "addon.door"}); rec.Code != 400 || !strings.Contains(out["error"].(string), "no add-on SKU") {
		t.Fatalf("access with add-on = %d %v", rec.Code, out)
	}
	if rec, out := op.json("POST", "/api/v1/features", map[string]any{"key": "sso", "name": "SSO", "group": "floor", "addon_sku": "addon.sso"}); rec.Code != 400 || !strings.Contains(out["error"].(string), "never priced") {
		t.Fatalf("floor with add-on = %d %v", rec.Code, out)
	}
	if rec, out := op.json("POST", "/api/v1/features", map[string]any{"key": "x", "name": "X", "group": "misc"}); rec.Code != 400 || !strings.Contains(out["error"].(string), "group must be") {
		t.Fatalf("unknown group = %d %v", rec.Code, out)
	}
	list := op.must("GET", "/api/v1/features", 200)
	if fl := list["features"].([]any); len(fl) != 7 || fl[0].(map[string]any)["key"] != "ssl" || fl[1].(map[string]any)["key"] != "bandwidth" {
		t.Fatalf("features = %v", fl)
	}
	if gr := list["groups"].([]any); len(gr) != 7 || gr[0].(map[string]any)["key"] != "capacity" {
		t.Fatalf("groups = %v", list["groups"])
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
	// A floor item has no cell.
	if rec, out := op.json("PUT", bookPath+"/packages/plan.s/features/ssl", map[string]any{"state": "included"}); rec.Code != 400 || !strings.Contains(out["error"].(string), "floor item") {
		t.Fatalf("a cell on a floor item = %d %v", rec.Code, out)
	}
	// A quantity feature marked included needs its quantity …
	if rec, out := op.json("PUT", bookPath+"/packages/plan.s/features/bandwidth", map[string]any{"state": "included", "overage": "hard_cap"}); rec.Code != 400 || !strings.Contains(out["error"].(string), "how much the S package includes") {
		t.Fatalf("quantity without quantity = %d %v", rec.Code, out)
	}
	// … is never optional …
	if rec, out := op.json("PUT", bookPath+"/packages/plan.s/features/bandwidth", map[string]any{"state": "optional", "included_quantity": "50"}); rec.Code != 400 || !strings.Contains(out["error"].(string), "never") && !strings.Contains(out["error"].(string), "not as an add-on") {
		t.Fatalf("quantity optional = %d %v", rec.Code, out)
	}
	// … a METERED overage needs the meter priced; a HARD CAP does not.
	if rec, out := op.json("PUT", bookPath+"/packages/plan.s/features/bandwidth", map[string]any{"state": "included", "included_quantity": "50"}); rec.Code != 400 || !strings.Contains(out["error"].(string), "eip.bandwidth_mbps is not priced in "+store.PlanBookName) {
		t.Fatalf("metered without a priced meter = %d %v", rec.Code, out)
	}
	if rec, out := op.json("PUT", bookPath+"/packages/plan.s/features/bandwidth", map[string]any{"state": "included", "included_quantity": "50", "overage": "capped"}); rec.Code != 400 || !strings.Contains(out["error"].(string), "overage must be") {
		t.Fatalf("bad overage = %d %v", rec.Code, out)
	}
	cell = op.mustJSON("PUT", bookPath+"/packages/plan.s/features/bandwidth", map[string]any{"state": "included", "included_quantity": "50", "overage": "hard_cap"}, 200)
	if cell["overage"] != "hard_cap" {
		t.Fatalf("cell = %v", cell)
	}
	// The bandwidth meter priced in the plans book: the excess over the
	// included quantity on a METERED package is billed at it.
	op.mustJSON("POST", bookPath+"/items", map[string]any{"sku": "eip.bandwidth_mbps", "unit": "mbps-hour", "unit_price": "0.01716667"}, 201)
	op.mustJSON("PUT", bookPath+"/packages/plan.xl/features/bandwidth", map[string]any{"state": "included", "included_quantity": "1000", "overage": "metered"}, 200)
	// An overage left out keeps the one that is there; a cell written
	// before the ladder reads metered.
	cell = op.mustJSON("PUT", bookPath+"/packages/plan.s/features/bandwidth", map[string]any{"state": "included", "included_quantity": "50"}, 200)
	if cell["overage"] != "hard_cap" {
		t.Fatalf("overage after a write without one = %v", cell["overage"])
	}
	// The disk: priced per GB-hour in the plans book for the metered
	// packages, hard-capped on M.
	op.mustJSON("POST", bookPath+"/items", map[string]any{"sku": "k8s.pvc_gb", "unit": "gb-hour", "unit_price": "0.00022831"}, 201)
	op.mustJSON("PUT", bookPath+"/packages/plan.m/features/disk", map[string]any{"state": "included", "included_quantity": "50", "overage": "hard_cap"}, 200)
	op.mustJSON("PUT", bookPath+"/packages/plan.xl/features/disk", map[string]any{"state": "included", "overage": "unlimited"}, 200)
	// A level cell: the level indexes into the levels; optional means the
	// next level is purchasable — there must be one, and the add-on priced.
	if rec, out := op.json("PUT", bookPath+"/packages/plan.s/features/dr", map[string]any{"state": "included"}); rec.Code != 400 || !strings.Contains(out["error"].(string), "which level") {
		t.Fatalf("level without level = %d %v", rec.Code, out)
	}
	if rec, out := op.json("PUT", bookPath+"/packages/plan.s/features/dr", map[string]any{"state": "included", "level": 2}); rec.Code != 400 || !strings.Contains(out["error"].(string), "2 is not one of them") {
		t.Fatalf("level out of range = %d %v", rec.Code, out)
	}
	if rec, out := op.json("PUT", bookPath+"/packages/plan.xl/features/dr", map[string]any{"state": "optional", "level": 1}); rec.Code != 400 || !strings.Contains(out["error"].(string), "already at the top level") {
		t.Fatalf("optional at the top = %d %v", rec.Code, out)
	}
	if rec, out := op.json("PUT", bookPath+"/packages/plan.s/features/dr", map[string]any{"state": "optional", "level": 0}); rec.Code != 400 || !strings.Contains(out["error"].(string), "addon.dr is not priced") {
		t.Fatalf("next level unpriced = %d %v", rec.Code, out)
	}
	cell = op.mustJSON("PUT", bookPath+"/packages/plan.s/features/dr", map[string]any{"state": "optional", "level": 0, "addon_monthly": "8.000"}, 200)
	if cell["level"] != float64(0) || cell["state"] != "optional" {
		t.Fatalf("level cell = %v", cell)
	}
	op.mustJSON("PUT", bookPath+"/packages/plan.m/features/dr", map[string]any{"state": "included", "level": 0}, 200)
	op.mustJSON("PUT", bookPath+"/packages/plan.xl/features/dr", map[string]any{"state": "included", "level": 1}, 200)
	// An access door is included or not offered, never an add-on.
	if rec, out := op.json("PUT", bookPath+"/packages/plan.m/features/gitea_iac", map[string]any{"state": "optional"}); rec.Code != 400 || !strings.Contains(out["error"].(string), "never an add-on") {
		t.Fatalf("access optional = %d %v", rec.Code, out)
	}
	op.mustJSON("PUT", bookPath+"/packages/plan.s/features/gitea_iac", map[string]any{"state": "not_offered"}, 200)
	op.mustJSON("PUT", bookPath+"/packages/plan.m/features/gitea_iac", map[string]any{"state": "included", "note": "read"}, 200)
	// The teaser: included from M.
	op.mustJSON("PUT", bookPath+"/packages/plan.m/features/vuln", map[string]any{"state": "included"}, 200)
	// Not a package: a cloud SKU, an unpriced plan, flexi.
	if rec, _ := op.json("PUT", bookPath+"/packages/ecs.s7n.small.1/features/backup", map[string]any{"state": "included"}); rec.Code != 400 {
		t.Fatalf("a cloud SKU as a package = %d", rec.Code)
	}
	if rec, _ := op.json("PUT", bookPath+"/packages/plan.flexi/features/backup", map[string]any{"state": "included"}); rec.Code != 400 {
		t.Fatalf("flexi as a package = %d", rec.Code)
	}

	// ── the package settings ───────────────────────────────────────────
	if rec, out := op.json("PUT", bookPath+"/packages/plan.m/settings", map[string]any{"annual_months_free": 13}); rec.Code != 400 || !strings.Contains(out["error"].(string), "between 0 and 12") {
		t.Fatalf("13 months free = %d %v", rec.Code, out)
	}
	if rec, out := op.json("PUT", bookPath+"/packages/plan.m/settings", map[string]any{"vcpu": "-1"}); rec.Code != 400 || !strings.Contains(out["error"].(string), "cannot be negative") {
		t.Fatalf("negative shape = %d %v", rec.Code, out)
	}
	if rec, _ := op.json("PUT", bookPath+"/packages/plan.flexi/settings", map[string]any{"tagline": "x"}); rec.Code != 400 {
		t.Fatalf("settings on flexi = %d", rec.Code)
	}
	ps := op.mustJSON("PUT", bookPath+"/packages/plan.m/settings", map[string]any{"tagline": "Business", "recommended": true, "annual_months_free": 2, "vcpu": "2", "memory_gb": "4", "vcpu_guaranteed": "0.33", "memory_gb_guaranteed": "1.33", "disk_gb": "50"}, 200)
	if ps["tagline"] != "Business" || ps["recommended"] != true || ps["annual_months_free"] != float64(2) || dec(t, ps["vcpu_guaranteed"]) != "0.33" {
		t.Fatalf("settings = %v", ps)
	}

	// ── the document, authenticated and public, identical ──────────────
	authed, _ := getExact(t, op, bookPath+"/packages", 200)
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
	if gr := pubDoc["groups"].([]any); len(gr) != 7 || gr[6].(map[string]any)["name"] != "Service level" {
		t.Fatalf("groups = %v", gr)
	}
	if fl := pubDoc["floor"].([]any); len(fl) != 1 || fl[0].(map[string]any)["key"] != "ssl" {
		t.Fatalf("floor = %v", fl)
	}
	pk := pubDoc["packages"].([]any)
	if len(pk) != 4 || pk[0].(map[string]any)["sku"] != "plan.s" || pk[3].(map[string]any)["sku"] != "plan.xl" || dec(t, pk[0].(map[string]any)["price_month"]) != `"5.000"` {
		t.Fatalf("packages = %v", pk)
	}
	pS, pM := pk[0].(map[string]any), pk[1].(map[string]any)
	if inc := pS["includes"].(map[string]any); dec(t, inc["vcpu"]) != "2" || dec(t, inc["memory_gb"]) != "4" || dec(t, inc["bandwidth_mbps"]) != "50" {
		t.Fatalf("S includes = %v", inc)
	}
	if sh := pM["shape"].(map[string]any); dec(t, sh["vcpu_guaranteed"]) != "0.33" || dec(t, sh["disk_gb"]) != "50" || pM["recommended"] != true || pM["tagline"] != "Business" || dec(t, pM["annual_months_free"]) != "2" {
		t.Fatalf("M = %v", pM)
	}
	if sh := pS["shape"].(map[string]any); sh["vcpu_guaranteed"] != nil || dec(t, sh["vcpu"]) != "2" || pS["recommended"] != false {
		t.Fatalf("S shape (no settings) = %v", sh)
	}
	// The step-up rule at the catalog's prices: S → M gap 4.000, DR's next
	// level (8.000) is purchasable on S but M is still at level 0 — nothing
	// bundled, the rule holds trivially; L → XL gap 14.000, backup (1.500)
	// bundled, the rule does not hold.
	su := pS["step_up"].(map[string]any)
	if su["next_sku"] != "plan.m" || dec(t, su["gap_month"]) != `"4.000"` || len(su["bundled_addon_keys"].([]any)) != 0 || su["rule_holds"] != true {
		t.Fatalf("S step-up = %v", su)
	}
	su = pk[2].(map[string]any)["step_up"].(map[string]any)
	if su["next_sku"] != "plan.xl" || dec(t, su["gap_month"]) != `"14.000"` || dec(t, su["bundled_addons_sum_month"]) != `"1.500"` || su["rule_holds"] != false {
		t.Fatalf("L step-up = %v", su)
	}
	fs := pubDoc["features"].([]any)
	byKey := map[string]map[string]any{}
	for _, f := range fs {
		m := f.(map[string]any)
		byKey[m["key"].(string)] = m
	}
	if len(fs) != 6 || fs[0].(map[string]any)["key"] != "bandwidth" {
		t.Fatalf("features = %d first %v", len(fs), fs[0])
	}
	bk := byKey["backup"]["cells"].(map[string]any)
	if c := bk["plan.s"].(map[string]any); c["state"] != "optional" || c["addon_sku"] != "addon.backup" || dec(t, c["price_month"]) != `"1.500"` || c["included_from"] != "plan.xl" {
		t.Fatalf("backup on S = %v", c)
	}
	if c := bk["plan.xl"].(map[string]any); c["state"] != "included" || c["included_from"] != nil {
		t.Fatalf("backup on XL = %v", c)
	}
	bw := byKey["bandwidth"]["cells"].(map[string]any)
	if c := bw["plan.s"].(map[string]any); c["state"] != "included" || c["overage"] != "hard_cap" || dec(t, c["quantity"]) != "50" {
		t.Fatalf("bandwidth on S = %v", c)
	}
	if c := bw["plan.m"].(map[string]any); c["state"] != "not_offered" {
		t.Fatalf("bandwidth on M (no cell) = %v, want not_offered", c)
	}
	if c := byKey["disk"]["cells"].(map[string]any)["plan.xl"].(map[string]any); c["overage"] != "unlimited" || c["quantity"] != nil {
		t.Fatalf("disk on XL = %v", c)
	}
	drc := byKey["dr"]["cells"].(map[string]any)
	if c := drc["plan.s"].(map[string]any); c["state"] != "included" || dec(t, c["level"]) != "0" || c["included_from"] != "plan.xl" || c["next_level_addon"].(map[string]any)["addon_sku"] != "addon.dr" || dec(t, c["next_level_addon"].(map[string]any)["price_month"]) != `"8.000"` {
		t.Fatalf("dr on S = %v", c)
	}
	if c := byKey["gitea_iac"]["cells"].(map[string]any)["plan.m"].(map[string]any); c["state"] != "included" || c["note"] != "read" {
		t.Fatalf("gitea on M = %v", c)
	}
	if c := byKey["vuln"]["cells"].(map[string]any)["plan.s"].(map[string]any); c["state"] != "teaser" || c["included_from"] != "plan.m" {
		t.Fatalf("vuln on S = %v, want a teaser from M", c)
	}

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
	// Included on M: redundant. A level at its level on M: no level above.
	if rec, out := op.json("PUT", addonsPath, map[string]any{"addons": []string{"vuln"}}); rec.Code != 400 || !strings.Contains(out["error"].(string), "Vulnerability dashboard is included in the M package") {
		t.Fatalf("redundant = %d %v", rec.Code, out)
	}
	if rec, out := op.json("PUT", addonsPath, map[string]any{"addons": []string{"dr"}}); rec.Code != 400 || !strings.Contains(out["error"].(string), "offers no level above") {
		t.Fatalf("level with no next = %d %v", rec.Code, out)
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
	if _, has := srcDoc["addons"]; !has {
		t.Fatal("addons key missing")
	}

	// ── the bill ───────────────────────────────────────────────────────
	// July 2026 on M around the clock: 744 plan-hours, bandwidth 70 Mbps
	// always on on a package that includes 50 METERED (written below), the
	// disk at 100 GB on a package that hard-caps 50, the add-on taken.
	op.mustJSON("PUT", bookPath+"/packages/plan.m/features/bandwidth", map[string]any{"state": "included", "included_quantity": "50", "overage": "metered"}, 200)
	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	var recs []store.UsageRecord
	for hr := 0; hr < 744; hr++ {
		at := from.Add(time.Duration(hr) * time.Hour)
		recs = append(recs,
			store.UsageRecord{CustomerID: cid, SourceID: src.ID, ResourceID: "plan/m", ResourceKind: store.PlanKind, SKU: "plan.m", Quantity: "1", Unit: store.PlanUnit, WindowStart: at, WindowEnd: at.Add(time.Hour)},
			store.UsageRecord{CustomerID: cid, SourceID: src.ID, ResourceID: "eip/nizwa", ResourceKind: "eip", SKU: "eip.bandwidth_mbps", Quantity: "70", Unit: "mbps-hour", WindowStart: at, WindowEnd: at.Add(time.Hour)},
			store.UsageRecord{CustomerID: cid, SourceID: src.ID, ResourceID: "pvc/data", ResourceKind: "pvc", SKU: "k8s.pvc_gb", Quantity: "100", Unit: "gb-hour", WindowStart: at, WindowEnd: at.Add(time.Hour)},
			store.UsageRecord{CustomerID: cid, SourceID: src.ID, ResourceID: "ns/nizwa", ResourceKind: "namespace", SKU: "k8s.vcpu", Quantity: "2", Unit: "vcpu-hour", WindowStart: at, WindowEnd: at.Add(time.Hour)},
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
	// The included boolean feature: a 0.000 line under the plan, described;
	// the level and the access door: no line at all; the floor: no line.
	if l := lines["plan.m.vuln"]; l == nil || dec(t, l["amount"]) != "0.000000" || dec(t, l["unit_price"]) != "0.00000000" || l["description"] != "Vulnerability dashboard — included in M plan" {
		t.Fatalf("included line = %v", l)
	}
	for _, sku := range []string{"plan.m.dr", "plan.m.gitea_iac", "plan.m.ssl", "plan.m.disk"} {
		if lines[sku] != nil {
			t.Fatalf("%s rendered a line: %v", sku, lines[sku])
		}
	}
	// Bandwidth: the cell is METERED, but the Source is in the default
	// CAPPED mode (DESIGN.md §22.11) — the customer's mode decides, so
	// 52,080 mbps-hours used, 37,200 included, the 14,880 excess reported
	// and billed at NOTHING. Grow mode, which meters it, is
	// TestIntegrationGrowModeBillsAboveTheAllowance.
	if l := lines["eip.bandwidth_mbps"]; l == nil || dec(t, l["amount"]) != "0.000000" || dec(t, l["quantity"]) != "52080.000000" {
		t.Fatalf("bandwidth line = %v", l)
	}
	// Disk, hard-capped: 74,400 gb-hours used, 37,200 included, the excess
	// reported and billed at NOTHING.
	if l := lines["k8s.pvc_gb"]; l == nil || dec(t, l["amount"]) != "0.000000" || dec(t, l["quantity"]) != "74400.000000" {
		t.Fatalf("disk line = %v", l)
	}
	var sawAllowance, sawCap bool
	for _, br := range res["applied_terms"].([]any) {
		m := br.(map[string]any)
		switch m["sku"] {
		case "eip.bandwidth_mbps":
			sawAllowance = true
			if dec(t, m["allowance"]) != "37200.000000" || dec(t, m["excess"]) != "14880.000000" || m["capped"] != true {
				t.Fatalf("bandwidth breakdown = %v", m)
			}
		case "k8s.pvc_gb":
			sawCap = true
			if m["capped"] != true || dec(t, m["allowance"]) != "37200.000000" || dec(t, m["excess"]) != "37200.000000" || dec(t, m["amount"]) != "0.000000" {
				t.Fatalf("disk breakdown = %v", m)
			}
		}
	}
	if !sawAllowance || !sawCap {
		t.Fatalf("applied terms %v carry no bandwidth allowance or no disk cap", res["applied_terms"])
	}
	// The vCPU meter under a plans book that prices the disk meter is still
	// the allocation basis — not sold per use, never "unpriced".
	if up := res["unpriced_skus"]; up != nil && len(up.([]any)) != 0 {
		t.Fatalf("unpriced = %v, want none", up)
	}
	if ns := res["not_sold_per_use"]; ns == nil || len(ns.([]any)) != 1 || ns.([]any)[0] != "k8s.vcpu" {
		t.Fatalf("not sold per use = %v, want k8s.vcpu", ns)
	}
	// 9.172605 + 1.528764 + 0 + 0 + 0 = 10.701369 — capped: the package
	// and its add-on, nothing beyond.
	if dec(t, stmt["subtotal"]) != "10.701369" {
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
	// Renaming a key is refused; a kind change on a feature in the matrix
	// too; so is shortening a level list below a cell's level, and moving a
	// feature with cells to the floor.
	if rec, _ := op.json("PATCH", "/api/v1/features/backup", map[string]any{"key": "backups"}); rec.Code != 400 {
		t.Fatalf("rename = %d", rec.Code)
	}
	if rec, out := op.json("PATCH", "/api/v1/features/backup", map[string]any{"kind": "quantity", "unit": "GB"}); rec.Code != 409 || !strings.Contains(out["error"].(string), "kind cannot change") {
		t.Fatalf("kind change in use = %d %v", rec.Code, out)
	}
	if rec, out := op.json("PATCH", "/api/v1/features/dr", map[string]any{"levels": []string{"single region"}}); rec.Code != 400 && rec.Code != 409 || !strings.Contains(out["error"].(string), "level") {
		t.Fatalf("shorten levels = %d %v", rec.Code, out)
	}
	if rec, out := op.json("PATCH", "/api/v1/features/backup", map[string]any{"group": "floor"}); rec.Code != 409 || !strings.Contains(out["error"].(string), "floor item has no cell") {
		t.Fatalf("to the floor with cells = %d %v", rec.Code, out)
	}
	// Clearing the add-on SKU while packages offer the feature as optional is refused.
	if rec, out := op.json("PATCH", "/api/v1/features/backup", map[string]any{"addon_sku": ""}); rec.Code != 409 || !strings.Contains(out["error"].(string), "needs an add-on SKU") {
		t.Fatalf("clear addon_sku = %d %v", rec.Code, out)
	}
	patched := op.mustJSON("PATCH", "/api/v1/features/backup", map[string]any{"blurb": "Nightly, kept 30 days", "teaser": true}, 200)
	if patched["blurb"] != "Nightly, kept 30 days" || patched["teaser"] != true {
		t.Fatalf("patched = %v", patched)
	}

	// ── a clone carries the matrix and the settings; deleting it drops them ──
	before := cellCount(t, st, `SELECT count(*) FROM package_entitlements`)
	clone := op.mustJSON("POST", bookPath+"/clone", map[string]any{"name": "Nizwa negotiated packages"}, 201)
	cloneDoc := op.must("GET", "/api/v1/pricebooks/"+clone["id"].(string)+"/packages", 200)
	if len(cloneDoc["features"].([]any)) != 6 || cloneDoc["packages"].([]any)[1].(map[string]any)["recommended"] != true {
		t.Fatalf("clone = %v", cloneDoc)
	}
	if c := cloneDoc["features"].([]any)[0].(map[string]any)["cells"].(map[string]any)["plan.s"].(map[string]any); c["overage"] != "hard_cap" {
		t.Fatalf("the clone's bandwidth on S = %v, want the overage copied", c)
	}
	op.must("DELETE", "/api/v1/pricebooks/"+clone["id"].(string), 200)
	if n := cellCount(t, st, `SELECT count(*) FROM package_entitlements`); n != before {
		t.Fatalf("%d cells after deleting the clone, want the plans book's %d", n, before)
	}
	if n := cellCount(t, st, `SELECT count(*) FROM package_settings`); n != 1 {
		t.Fatalf("%d settings rows after deleting the clone, want the plans book's 1", n)
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
	if rec, _ := anon.json("PUT", bookPath+"/packages/plan.m/settings", map[string]any{"tagline": "x"}); rec.Code != 401 {
		t.Fatalf("anonymous settings = %d", rec.Code)
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
	// The package settings name M's shape as the workbook has it (2 vCPU ·
	// 4 GB); the catalog's plan deck says the same, not the constants' 4 · 8.
	op.mustJSON("PUT", "/api/v1/pricebooks/"+plans.ID+"/packages/plan.m/settings", map[string]any{"vcpu": "2", "memory_gb": "4", "vcpu_guaranteed": "0.33", "memory_gb_guaranteed": "1.33", "disk_gb": "50", "recommended": true}, 200)

	anon := anonClient(t, h)
	_, cat := getExact(t, anon, "/api/v1/public/catalog", 200)
	var planM, planS map[string]any
	for _, p := range cat["plans"].([]any) {
		m := p.(map[string]any)
		switch m["slug"] {
		case "m":
			planM = m
		case "s":
			planS = m
		}
	}
	if planM == nil || dec(t, planM["vcpu"]) != "2" || dec(t, planM["memory_gib"]) != "4" || planM["display_name"] != "M plan · 2 vCPU · 4 GB" {
		t.Fatalf("catalog plan M = %v, want the settings' shape", planM)
	}
	if planS == nil || dec(t, planS["vcpu"]) != "2" || planS["display_name"] != "S plan · 2 vCPU · 4 GB" {
		t.Fatalf("catalog plan S (no settings) = %v, want the constants' shape", planS)
	}
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
