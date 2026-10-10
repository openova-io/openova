package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The package is a prepaid monthly commitment (DESIGN.md §22.11, founder
// direction 2026-10-10) — the worked example of the design document, end to
// end through the statement run, to the digit:
//
//	Month 1 — October (744 h). The M package is ordered on 10 October; the
//	checkout collects 4.490. The Organization runs within its package.
//	  plan.m           10 to 31 October, 528 h × (4.490 ÷ 744 = 0.00603495)  =  3.186454
//	  plan.m           November, in advance                                  =  4.490000
//	  checkout.credit  paid at checkout                                      = −4.490000
//	  subtotal                                                               =  3.186454
//
//	Month 2 — November (720 h), grow: 3 vCPU / 6 GB of limits all month
//	(1 vCPU and 2 GB above M's 2 / 4), 70 GB of disk (20 above the 50).
//	November was paid on October's statement.
//	  plan.m           December, in advance                                  =  4.490000
//	  k8s.vcpu         2,160 − 2 × 720 = 720 vcpu-h × 0.00246027             =  1.771394
//	  k8s.mem_gb       4,320 − 4 × 720 = 1,440 gib-h × 0.00046164            =  0.664762
//	  k8s.pvc_gb       50,400 − 50 × 720 = 14,400 gb-h × 0.00004829          =  0.695376
//	  subtotal                                                               =  7.621532
//
//	Month 3 — December (744 h): the same usage, and an upgrade to L on 16
//	December. December was paid at M; the difference is billed for the 384
//	hours left, and January in advance at L. L's headline (4 / 8 / 100)
//	absorbs the usage: the allowances are 2 × 360 + 4 × 384 = 2,256 vcpu-h
//	≥ 2,232 used, 4 × 360 + 8 × 384 = 4,512 gib-h ≥ 4,464, and 50 × 360 +
//	100 × 384 = 56,400 gb-h ≥ 52,080 — no usage above the package.
//	  plan.l           upgrade M → L, 384 h × (3.500 ÷ 744 = 0.00470430)     =  1.806451
//	  plan.l           January, in advance                                   =  7.990000
//	  subtotal                                                               =  9.796451
func TestIntegrationPrepaidPackageWorkedExample(t *testing.T) {
	h, st, mail, _, _ := setupAPI(t)
	ctx := context.Background()
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)

	plans, _, err := st.EnsurePlanBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bookPath := "/api/v1/pricebooks/" + plans.ID
	op.mustJSON("PATCH", bookPath+"/items/plan.m", map[string]any{"annual_price": "53.88"}, 200)
	op.mustJSON("PATCH", bookPath+"/items/plan.l", map[string]any{"annual_price": "95.88"}, 200)
	op.mustJSON("PUT", bookPath+"/items?merge=true", map[string]any{"items": []map[string]any{
		{"sku": "k8s.pvc_gb", "unit": "gb-hour", "annual_price": "0.423"},
	}}, 200)
	op.mustJSON("POST", "/api/v1/features", map[string]any{"key": "disk", "name": "Disk", "kind": "quantity", "group": "capacity", "unit": "GB", "addon_sku": "k8s.pvc_gb", "sort_order": 1}, 201)
	op.mustJSON("PUT", bookPath+"/packages/plan.m/features/disk", map[string]any{"state": "included", "included_quantity": "50", "overage": "metered"}, 200)
	op.mustJSON("PUT", bookPath+"/packages/plan.l/features/disk", map[string]any{"state": "included", "included_quantity": "100", "overage": "metered"}, 200)
	op.mustJSON("PUT", bookPath+"/packages/plan.m/settings", map[string]any{"vcpu": "2", "memory_gb": "4", "disk_gb": "50", "grow_allowed": true,
		"grow_ceiling_vcpu": "8", "grow_ceiling_memory_gb": "16", "grow_ceiling_disk_gb": "250", "grow_ceiling_bandwidth_mbps": "1000",
		"overage_vcpu_month": "1.796", "overage_mem_gb_month": "0.337"}, 200)
	op.mustJSON("PUT", bookPath+"/packages/plan.l/settings", map[string]any{"vcpu": "4", "memory_gb": "8", "disk_gb": "100", "grow_allowed": true,
		"grow_ceiling_vcpu": "8", "grow_ceiling_memory_gb": "16", "grow_ceiling_disk_gb": "250", "grow_ceiling_bandwidth_mbps": "1000",
		"overage_vcpu_month": "1.598", "overage_mem_gb_month": "0.300"}, 200)

	cust := op.mustJSON("POST", "/api/v1/customers", map[string]any{"slug": "nizwa", "name": "Nizwa Dates", "admin_email": "ops@nizwa.example", "kind": "organization", "org_slug": "nizwa", "plan_slug": "m", "billing_mode": "chargeback"}, 201)
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
	// What the checkout collected (spec.commerce.prepaidAmount).
	if err := st.SetSourcePrepaid(ctx, src.ID, "4.490"); err != nil {
		t.Fatal(err)
	}
	op.mustJSON("PUT", "/api/v1/customers/"+cid+"/sources/"+src.ID+"/overage", map[string]any{"overage_mode": "grow"}, 200)

	var recs []store.UsageRecord
	hourly := func(from, to time.Time, plan string, vcpuLimit, memLimit, disk string) {
		for at := from; at.Before(to); at = at.Add(time.Hour) {
			u := func(rid, kind, sku, qty, unit string) store.UsageRecord {
				return store.UsageRecord{CustomerID: cid, SourceID: src.ID, ResourceID: rid, ResourceKind: kind, SKU: sku, Quantity: store.Decimal(qty), Unit: unit, WindowStart: at, WindowEnd: at.Add(time.Hour)}
			}
			recs = append(recs,
				u("plan/"+plan, store.PlanKind, "plan."+plan, "1", store.PlanUnit),
				u("pod/web", "k8s-pod", store.SKUVCPULimit, vcpuLimit, store.UnitVCPU),
				u("pod/web", "k8s-pod", store.SKUMemLimit, memLimit, store.UnitMem),
				u("pvc/data", "k8s-pvc", store.SKUPVC, disk, store.UnitPVC),
			)
		}
	}
	d := func(m time.Month, day int) time.Time { return time.Date(2026, m, day, 0, 0, 0, 0, time.UTC) }
	hourly(d(10, 10), d(11, 1), "m", "2", "4", "30")                                    // October from the order, within the package
	hourly(d(11, 1), d(12, 1), "m", "3", "6", "70")                                     // November, growing
	hourly(d(12, 1), d(12, 16), "m", "3", "6", "70")                                    // December on M ...
	hourly(d(12, 16), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), "l", "3", "6", "70") // ... and L from the 16th
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}

	type line struct{ sku, qty, unit, amount, svc, desc string }
	statement := func(period, subtotal string, want []line) {
		t.Helper()
		run := postExact(t, op, "/api/v1/statements/run", map[string]any{"period": period, "customer_id": cid}, 200)
		res := run["results"].([]any)[0].(map[string]any)
		if res["error"] != nil {
			t.Fatalf("%s run = %v", period, run)
		}
		_, stmt := getExact(t, op, "/api/v1/statements/"+res["statement_id"].(string), 200)
		var got []map[string]any
		for _, l := range stmt["lines"].([]any) {
			lm := l.(map[string]any)
			if dec(t, lm["amount"]) == "0.000000" {
				continue // the disk line within its allowance
			}
			got = append(got, lm)
		}
		if len(got) != len(want) {
			t.Fatalf("%s lines = %v, want %d", period, got, len(want))
		}
		for _, w := range want {
			found := false
			for _, l := range got {
				svc, _ := l["service_period_start"].(string)
				if len(svc) >= 10 {
					svc = svc[:10]
				}
				desc, _ := l["description"].(string)
				if l["sku"] == w.sku && dec(t, l["quantity"]) == w.qty && l["unit"] == w.unit && dec(t, l["amount"]) == w.amount && svc == w.svc && strings.Contains(desc, w.desc) {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s: no line %+v in %v", period, w, got)
			}
		}
		if dec(t, stmt["subtotal"]) != subtotal {
			t.Fatalf("%s subtotal = %s, want %s", period, dec(t, stmt["subtotal"]), subtotal)
		}
	}
	statement("2026-10", "3.186454", []line{
		{"plan.m", "528.000000", "hour", "3.186454", "2026-10-10", "M package — 10 to 31 October 2026, 528 of 744 hours"},
		{"plan.m", "1.000000", "month", "4.490000", "2026-11-01", "M package — November 2026, billed in advance"},
		{"checkout.credit", "1.000000", "period", "-4.490000", "", "Paid at checkout — 4.490 OMR for the M package, credited"},
	})
	statement("2026-11", "7.621532", []line{
		{"plan.m", "1.000000", "month", "4.490000", "2026-12-01", "M package — December 2026, billed in advance"},
		{store.SKUVCPU, "720.000000", store.UnitVCPU, "1.771394", "", "vCPU above the M package, grow"},
		{store.SKUMem, "1440.000000", store.UnitMem, "0.664762", "", "Memory above the M package, grow"},
		{store.SKUPVC, "50400.000000", store.UnitPVC, "0.695376", "", ""},
	})
	statement("2026-12", "9.796451", []line{
		{"plan.l", "384.000000", "hour", "1.806451", "2026-12-16", "Upgrade M → L — 16 to 31 December 2026, 384 of 744 hours at the 3.500 monthly difference"},
		{"plan.l", "1.000000", "month", "7.990000", "2027-01-01", "L package — January 2027, billed in advance"},
	})
}
