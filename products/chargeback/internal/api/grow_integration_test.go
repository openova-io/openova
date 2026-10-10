package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Capped or grow, end to end (DESIGN.md §22.11): the package's grow settings
// written and refused, the document publishing packages[].grow with the
// package's OWN compute rates beside the book's flat disk and bandwidth, a
// grow-only DR cell, a Source's overage mode set and refused through
// PUT /customers/{id}/sources/{sid}/overage, and the statement run billing a
// grow Source the usage above its allowance and a capped Source nothing
// beyond its package — to the digit.

func TestIntegrationGrowModeBillsAboveTheAllowance(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	ctx := context.Background()
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)

	plans, _, err := st.EnsurePlanBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bookPath := "/api/v1/pricebooks/" + plans.ID
	// The workbook's M at 4.490 a month (annual 53.88), the founder's flat
	// disk and bandwidth rates (0.423 per GB-year, 15.038 per Mbps-year).
	op.mustJSON("PATCH", bookPath+"/items/plan.m", map[string]any{"annual_price": "53.88"}, 200)
	op.mustJSON("PUT", bookPath+"/items?merge=true", map[string]any{"items": []map[string]any{
		{"sku": "k8s.pvc_gb", "unit": "gb-hour", "annual_price": "0.423"},
		{"sku": "eip.bandwidth_mbps", "unit": "mbps-hour", "annual_price": "15.038"},
	}}, 200)

	op.mustJSON("POST", "/api/v1/features", map[string]any{"key": "bandwidth", "name": "Bandwidth", "kind": "quantity", "group": "capacity", "unit": "Mbps", "addon_sku": "eip.bandwidth_mbps", "sort_order": 1}, 201)
	op.mustJSON("POST", "/api/v1/features", map[string]any{"key": "disk", "name": "Disk", "kind": "quantity", "group": "capacity", "unit": "GB", "addon_sku": "k8s.pvc_gb", "sort_order": 2}, 201)
	op.mustJSON("POST", "/api/v1/features", map[string]any{"key": "dr_topology", "name": "DR topology", "kind": "level", "group": "resilience", "levels": []string{"single region", "active-passive"}, "sort_order": 3}, 201)
	op.mustJSON("PUT", bookPath+"/packages/plan.m/features/bandwidth", map[string]any{"state": "included", "included_quantity": "100", "overage": "metered"}, 200)
	op.mustJSON("PUT", bookPath+"/packages/plan.m/features/disk", map[string]any{"state": "included", "included_quantity": "50", "overage": "metered"}, 200)
	op.mustJSON("PUT", bookPath+"/packages/plan.xl/features/dr_topology", map[string]any{"state": "included", "level": 1}, 200)

	// ── a grow-only cell ────────────────────────────────────────────────
	// Optional with no add-on SKU and no price: refused unless grow-only.
	if rec, out := op.json("PUT", bookPath+"/packages/plan.m/features/dr_topology", map[string]any{"state": "optional", "level": 0}); rec.Code != 400 || !strings.Contains(out["error"].(string), "no add-on SKU") {
		t.Fatalf("optional level without an add-on = %d %v", rec.Code, out)
	}
	if rec, out := op.json("PUT", bookPath+"/packages/plan.m/features/dr_topology", map[string]any{"state": "included", "level": 0, "grow_only": true}); rec.Code != 400 || !strings.Contains(out["error"].(string), "grow-only cell is optional") {
		t.Fatalf("grow-only included = %d %v", rec.Code, out)
	}
	if rec, out := op.json("PUT", bookPath+"/packages/plan.m/features/disk", map[string]any{"state": "optional", "included_quantity": "50", "grow_only": true}); rec.Code != 400 || !strings.Contains(out["error"].(string), "only a boolean or a level") {
		t.Fatalf("grow-only quantity = %d %v", rec.Code, out)
	}
	cell := op.mustJSON("PUT", bookPath+"/packages/plan.m/features/dr_topology", map[string]any{"state": "optional", "level": 0, "grow_only": true}, 200)
	if cell["grow_only"] != true || cell["state"] != "optional" {
		t.Fatalf("grow-only cell = %v", cell)
	}

	// ── the package's grow settings ─────────────────────────────────────
	settingsPath := bookPath + "/packages/plan.m/settings"
	base := map[string]any{"vcpu": "2", "memory_gb": "4", "disk_gb": "50", "grow_allowed": true,
		"grow_ceiling_vcpu": "8", "grow_ceiling_memory_gb": "16", "grow_ceiling_disk_gb": "250", "grow_ceiling_bandwidth_mbps": "1000",
		"overage_vcpu_month": "1.796", "overage_mem_gb_month": "0.337"}
	with := func(k string, v any) map[string]any {
		out := map[string]any{}
		for kk, vv := range base {
			out[kk] = vv
		}
		if v == nil {
			delete(out, k)
		} else {
			out[k] = v
		}
		return out
	}
	if rec, out := op.json("PUT", settingsPath, with("overage_vcpu_month", nil)); rec.Code != 400 || !strings.Contains(out["error"].(string), "overage_vcpu_month is needed") {
		t.Fatalf("grow without a rate = %d %v", rec.Code, out)
	}
	if rec, out := op.json("PUT", settingsPath, with("grow_ceiling_vcpu", "1")); rec.Code != 400 || !strings.Contains(out["error"].(string), "below the package's own vcpu") {
		t.Fatalf("ceiling below the shape = %d %v", rec.Code, out)
	}
	if rec, out := op.json("PUT", settingsPath, with("grow_ceiling_bandwidth_mbps", "50")); rec.Code != 400 || !strings.Contains(out["error"].(string), "below the 100 Mbps the package includes") {
		t.Fatalf("bandwidth ceiling below the cell = %d %v", rec.Code, out)
	}
	ps := op.mustJSON("PUT", settingsPath, base, 200)
	if ps["grow_allowed"] != true || dec(t, ps["overage_vcpu_month"]) != "1.796" || dec(t, ps["grow_ceiling_disk_gb"]) != "250" {
		t.Fatalf("settings = %v", ps)
	}

	// ── the document ───────────────────────────────────────────────────
	_, doc := getExact(t, op, bookPath+"/packages", 200)
	var m, s map[string]any
	for _, p := range doc["packages"].([]any) {
		pm := p.(map[string]any)
		switch pm["sku"] {
		case "plan.m":
			m = pm
		case "plan.s":
			s = pm
		}
	}
	if _, has := s["grow"]; has {
		t.Fatalf("S allows no grow but publishes %v", s["grow"])
	}
	g := m["grow"].(map[string]any)
	ceil := g["ceiling"].(map[string]any)
	if g["allowed"] != true || dec(t, ceil["vcpu"]) != "8" || dec(t, ceil["memory_gb"]) != "16" || dec(t, ceil["disk_gb"]) != "250" || dec(t, ceil["bandwidth_mbps"]) != "1000" {
		t.Fatalf("grow = %v", g)
	}
	want := []struct{ key, sku, unit, month string }{
		{"vcpu", "k8s.vcpu", "vCPU", "1.796"}, {"memory", "k8s.mem_gb", "GB", "0.337"},
		{"disk", "k8s.pvc_gb", "GB", "0.035"}, {"bandwidth", "eip.bandwidth_mbps", "Mbps", "1.253"},
	}
	rates := g["overage_rates"].([]any)
	if len(rates) != len(want) {
		t.Fatalf("overage_rates = %v", rates)
	}
	for i, w := range want {
		r := rates[i].(map[string]any)
		if r["key"] != w.key || r["sku"] != w.sku || r["unit"] != w.unit || r["price_month"] != w.month {
			t.Fatalf("overage_rates[%d] = %v, want %+v", i, r, w)
		}
	}
	for _, f := range doc["features"].([]any) {
		fm := f.(map[string]any)
		if fm["key"] != "dr_topology" {
			continue
		}
		c := fm["cells"].(map[string]any)["plan.m"].(map[string]any)
		if c["state"] != "optional" || c["grow_only"] != true || dec(t, c["level"]) != "0" || c["included_from"] != "plan.xl" || c["next_level_addon"] != nil || c["price_month"] != nil {
			t.Fatalf("dr on M = %v", c)
		}
		if c := fm["cells"].(map[string]any)["plan.xl"].(map[string]any); c["state"] != "included" || c["grow_only"] != nil {
			t.Fatalf("dr on XL = %v", c)
		}
	}

	// ── two Sources on M: one grows, one stays capped ──────────────────
	newSource := func(slug string) (string, store.CostSource) {
		cust := op.mustJSON("POST", "/api/v1/customers", map[string]any{"slug": slug, "name": slug, "admin_email": "ops@" + slug + ".example", "kind": "organization", "org_slug": slug, "plan_slug": "m", "billing_mode": "chargeback"}, 201)
		cid := cust["id"].(string)
		op.mustJSON("PATCH", "/api/v1/customers/"+cid, map[string]any{"status": "active"}, 200)
		src, _, err := st.UpsertSource(ctx, cid, store.SourceKindOrg, "", slug)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetSourceVerified(ctx, src.ID, ""); err != nil {
			t.Fatal(err)
		}
		if err := st.SetSourcePriceBook(ctx, src.ID, plans.ID); err != nil {
			t.Fatal(err)
		}
		return cid, src
	}
	growCID, growSrc := newSource("sohar")
	capCID, capSrc := newSource("sur")
	if growSrc.OverageMode != store.OverageModeCapped {
		t.Fatalf("a new Source reads %q, want capped by default", growSrc.OverageMode)
	}
	overagePath := "/api/v1/customers/" + growCID + "/sources/" + growSrc.ID + "/overage"
	for _, c := range []struct {
		body map[string]any
		want string
	}{
		{map[string]any{"overage_mode": "unlimited"}, "capped or grow"},
		{map[string]any{"overage_mode": "capped", "grow_ceiling": map[string]any{"vcpu": 4}}, "grow ceiling applies in grow mode"},
		{map[string]any{"overage_mode": "capped", "spend_limit_month": "25"}, "spend limit applies to what grow bills"},
		{map[string]any{"overage_mode": "grow", "grow_ceiling": map[string]any{"vcpu": 1}}, "below the M package's 2"},
		{map[string]any{"overage_mode": "grow", "grow_ceiling": map[string]any{"vcpu": 9}}, "above the M package's ceiling 8"},
		{map[string]any{"overage_mode": "grow", "spend_limit_month": "0"}, "above zero"},
	} {
		if rec, out := op.json("PUT", overagePath, c.body); rec.Code != 400 || !strings.Contains(out["error"].(string), c.want) {
			t.Fatalf("%v = %d %v, want %q", c.body, rec.Code, out, c.want)
		}
	}
	// The grow-only DR is not an add-on.
	if rec, out := op.json("PUT", "/api/v1/customers/"+growCID+"/sources/"+growSrc.ID+"/addons", map[string]any{"addons": []string{"dr_topology"}}); rec.Code != 400 || !strings.Contains(out["error"].(string), "comes with grow mode") {
		t.Fatalf("dr as an add-on = %d %v", rec.Code, out)
	}
	set := op.mustJSON("PUT", overagePath, map[string]any{"overage_mode": "grow", "grow_ceiling": map[string]any{"vcpu": 4}, "spend_limit_month": "25.000"}, 200)
	gc := set["grow_ceiling"].(map[string]any)
	if set["overage_mode"] != "grow" || dec(t, gc["vcpu"]) != "4" || dec(t, gc["memory_gb"]) != "16" || dec(t, gc["disk_gb"]) != "250" || dec(t, gc["bandwidth_mbps"]) != "1000" || dec(t, set["spend_limit_month"]) != "25" {
		t.Fatalf("grow source = %v", set)
	}

	// ── October on M, the same usage on both Sources ────────────────────
	// 744 h at 3 vCPU / 6 GB of LIMITS (1 vCPU + 2 GB above the headline),
	// 70 GB of disk (20 above the 50 included), 100 Mbps (at the allowance),
	// and a 1 vCPU request meter that is the allocation basis.
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var recs []store.UsageRecord
	for _, src := range []store.CostSource{growSrc, capSrc} {
		for hr := 0; hr < 744; hr++ {
			at := from.Add(time.Duration(hr) * time.Hour)
			u := func(rid, kind, sku, qty, unit string) store.UsageRecord {
				return store.UsageRecord{CustomerID: src.CustomerID, SourceID: src.ID, ResourceID: rid, ResourceKind: kind, SKU: sku, Quantity: store.Decimal(qty), Unit: unit, WindowStart: at, WindowEnd: at.Add(time.Hour)}
			}
			recs = append(recs,
				u("plan/m", store.PlanKind, "plan.m", "1", store.PlanUnit),
				u("pod/web", "k8s-pod", store.SKUVCPU, "1", store.UnitVCPU),
				u("pod/web", "k8s-pod", store.SKUVCPULimit, "3", store.UnitVCPU),
				u("pod/web", "k8s-pod", store.SKUMemLimit, "6", store.UnitMem),
				u("pvc/data", "k8s-pvc", store.SKUPVC, "70", store.UnitPVC),
				u("eip/web", "eip", store.SKUBandwidth, "100", "mbps-hour"),
			)
		}
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	bill := func(cid string) (map[string]map[string]any, map[string]any, map[string]any) {
		run := postExact(t, op, "/api/v1/statements/run", map[string]any{"period": "2026-10", "customer_id": cid}, 200)
		res := run["results"].([]any)[0].(map[string]any)
		if res["error"] != nil {
			t.Fatalf("run = %v", run)
		}
		_, stmt := getExact(t, op, "/api/v1/statements/"+res["statement_id"].(string), 200)
		lines := map[string]map[string]any{}
		for _, l := range stmt["lines"].([]any) {
			lm := l.(map[string]any)
			lines[lm["sku"].(string)] = lm
		}
		return lines, stmt, res
	}

	// GROW. The arithmetic (unit = monthly × 12 ÷ 8760, rounded to 8):
	//   plan.m        744 plan-h  × 0.00615068              = 4.576106
	//   k8s.vcpu      2,232 used − 2 × 744 = 744 vcpu-h
	//                 × (1.796 × 12 ÷ 8760 =) 0.00246027    = 1.830441
	//   k8s.mem_gb    4,464 used − 4 × 744 = 1,488 gib-h
	//                 × (0.337 × 12 ÷ 8760 =) 0.00046164    = 0.686920
	//   k8s.pvc_gb    52,080 used − 50 × 744 = 14,880 gb-h
	//                 × (0.423 ÷ 8760 =) 0.00004829         = 0.718555
	//   eip.bandwidth 74,400 used − 100 × 744 = 0           = 0.000000
	//   subtotal                                            = 7.812022
	// (The disk and bandwidth lines carry the whole quantity; the allowance
	// path takes the included part off and the line's amount is the excess
	// at the book's price — its unit price then reads as the average.)
	lines, stmt, res := bill(growCID)
	for sku, w := range map[string][3]string{
		"plan.m":           {"744.000000", "0.00615068", "4.576106"},
		store.SKUVCPU:      {"744.000000", "0.00246027", "1.830441"},
		store.SKUMem:       {"1488.000000", "0.00046164", "0.686920"},
		store.SKUPVC:       {"52080.000000", "", "0.718555"},
		store.SKUBandwidth: {"74400.000000", "", "0.000000"},
	} {
		l := lines[sku]
		if l == nil || dec(t, l["quantity"]) != w[0] || (w[1] != "" && dec(t, l["unit_price"]) != w[1]) || dec(t, l["amount"]) != w[2] {
			t.Fatalf("grow %s = %v, want qty %s × %s = %s", sku, l, w[0], w[1], w[2])
		}
	}
	if d, _ := lines[store.SKUVCPU]["description"].(string); !strings.Contains(d, "vCPU above the M package, grow") || !strings.Contains(d, "1.796 OMR per vCPU a month") {
		t.Fatalf("vcpu line description = %q", d)
	}
	if lines[store.SKUVCPULimit] != nil || lines[store.SKUMemLimit] != nil {
		t.Fatal("a limit meter reached the statement; it is a measurement, never a line")
	}
	if dec(t, stmt["subtotal"]) != "7.812022" {
		t.Fatalf("grow subtotal = %s, want 7.812022", dec(t, stmt["subtotal"]))
	}
	if up := res["unpriced_skus"]; up != nil && len(up.([]any)) != 0 {
		t.Fatalf("unpriced = %v, want none", up)
	}

	if lines[rating.SpendLimitSKU] != nil {
		t.Fatalf("a 25.000 limit above 3.235916 of usage wrote %v", lines[rating.SpendLimitSKU])
	}

	// THE SPEND LIMIT caps the usage above the package, not the bill: at
	// 3.000 the usage (1.830441 + 0.686920 + 0.718555 = 3.235916) is
	// brought down by one named line of −0.235916; the plan is untouched.
	op.mustJSON("PUT", overagePath, map[string]any{"overage_mode": "grow", "grow_ceiling": map[string]any{"vcpu": 4}, "spend_limit_month": "3.000"}, 200)
	lines, stmt, _ = bill(growCID)
	if l := lines[rating.SpendLimitSKU]; l == nil || dec(t, l["amount"]) != "-0.235916" || !strings.Contains(l["description"].(string), "capped at the 3 OMR limit") {
		t.Fatalf("spend limit line = %v", l)
	}
	if dec(t, stmt["subtotal"]) != "7.576106" {
		t.Fatalf("subtotal under the limit = %s, want 4.576106 + 3.000 = 7.576106", dec(t, stmt["subtotal"]))
	}

	// CAPPED. The package and nothing beyond: no compute line at all, the
	// disk excess reported and billed at nothing.
	lines, stmt, _ = bill(capCID)
	if lines[store.SKUVCPU] != nil || lines[store.SKUMem] != nil {
		t.Fatalf("capped billed compute: %v %v", lines[store.SKUVCPU], lines[store.SKUMem])
	}
	if l := lines[store.SKUPVC]; l == nil || dec(t, l["amount"]) != "0.000000" {
		t.Fatalf("capped disk = %v", l)
	}
	if dec(t, stmt["subtotal"]) != "4.576106" {
		t.Fatalf("capped subtotal = %s, want the plan alone, 4.576106", dec(t, stmt["subtotal"]))
	}

	// Back to capped clears the ceiling and the spend limit.
	back := op.mustJSON("PUT", overagePath, map[string]any{"overage_mode": "capped"}, 200)
	if back["overage_mode"] != "capped" || back["grow_ceiling"] != nil || back["spend_limit_month"] != nil {
		t.Fatalf("back to capped = %v", back)
	}
}
