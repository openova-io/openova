package main

import (
	"context"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/synth"
)

// The showcase package ladder, end to end against a real API (DESIGN.md §7,
// §22.7): the seeder writes the ladder's features and every cell onto the
// plans book, prices the add-ons and the two meters there, converges the
// four plan prices to the pricing workbook's, writes each package's shape;
// the published document carries the sheet's prices and shapes and the
// STEP-UP RULE holds on every gap; a second run writes nothing; Nizwa
// Fintech's Source takes the backup add-on; a July statement carries the
// add-on line, the included 0.000 lines, the hard-capped disk at nothing;
// and the purge takes the ladder out again with the rest of the showcase.
func TestShowcasePackagesAreSeededBilledAndPurged(t *testing.T) {
	s, db := setupSeeder(t, synth.DefaultWindow())
	if err := s.ensureBooks(false, true, "", ""); err != nil {
		t.Fatalf("books: %v", err)
	}
	planBook := s.books[s.sc.PlanBookName]
	if err := s.ensurePackages(planBook); err != nil {
		t.Fatalf("packages: %v", err)
	}

	// ── the ladder, exactly ────────────────────────────────────────────
	if n := scalar[int](t, db, `SELECT count(*) FROM features`); n != 27 {
		t.Fatalf("%d features, want 27 (9 on the floor, 18 with cells)", n)
	}
	if n := scalar[int](t, db, `SELECT count(*) FROM features WHERE feature_group = 'floor'`); n != 9 {
		t.Fatalf("%d floor items, want 9", n)
	}
	if n := scalar[int](t, db, `SELECT count(*) FROM package_entitlements WHERE price_book_id = $1`, planBook); n != 72 {
		t.Fatalf("%d cells, want 18 features × 4 packages = 72", n)
	}
	if n := scalar[int](t, db, `SELECT count(*) FROM package_entitlements e JOIN features f ON f.id = e.feature_id WHERE f.feature_group = 'floor'`); n != 0 {
		t.Fatalf("%d cells on floor items, want none", n)
	}
	state := func(plan, key string) string {
		return scalar[string](t, db, `SELECT e.state FROM package_entitlements e JOIN features f ON f.id = e.feature_id WHERE e.price_book_id = $1 AND e.plan_sku = $2 AND f.key = $3`, planBook, plan, key)
	}
	for _, tc := range []struct{ plan, key, want string }{
		{"plan.s", "backup", "optional"}, {"plan.l", "backup", "optional"}, {"plan.xl", "backup", "included"},
		{"plan.s", "dedicated_ip", "optional"}, {"plan.xl", "dedicated_ip", "optional"},
		{"plan.m", "domain", "optional"}, {"plan.xl", "domain", "included"},
		{"plan.s", "bandwidth", "included"}, {"plan.s", "disk", "included"},
		{"plan.s", "gitea_iac", "not_offered"}, {"plan.m", "gitea_iac", "included"}, {"plan.xl", "kube_api", "included"}, {"plan.l", "kube_api", "not_offered"},
		{"plan.s", "vuln_dashboard", "not_offered"}, {"plan.m", "vuln_dashboard", "included"},
		{"plan.s", "dr_topology", "included"}, {"plan.xl", "dr_topology", "included"},
	} {
		if got := state(tc.plan, tc.key); got != tc.want {
			t.Fatalf("%s on %s = %s, want %s", tc.key, tc.plan, got, tc.want)
		}
	}
	if o := scalar[string](t, db, `SELECT e.overage FROM package_entitlements e JOIN features f ON f.id = e.feature_id WHERE e.price_book_id = $1 AND e.plan_sku = 'plan.m' AND f.key = 'bandwidth'`, planBook); o != "hard_cap" {
		t.Fatalf("bandwidth on M overage = %s, want hard_cap", o)
	}
	if o := scalar[string](t, db, `SELECT e.overage FROM package_entitlements e JOIN features f ON f.id = e.feature_id WHERE e.price_book_id = $1 AND e.plan_sku = 'plan.l' AND f.key = 'disk'`, planBook); o != "metered" {
		t.Fatalf("disk on L overage = %s, want metered", o)
	}
	if q := scalar[string](t, db, `SELECT e.included_quantity::text FROM package_entitlements e JOIN features f ON f.id = e.feature_id WHERE e.price_book_id = $1 AND e.plan_sku = 'plan.l' AND f.key = 'bandwidth'`, planBook); !near(f(t, q), 250) {
		t.Fatalf("bandwidth on L = %s, want 250", q)
	}
	if l := scalar[int](t, db, `SELECT e.level FROM package_entitlements e JOIN features f ON f.id = e.feature_id WHERE e.price_book_id = $1 AND e.plan_sku = 'plan.xl' AND f.key = 'dr_topology'`, planBook); l != 1 {
		t.Fatalf("DR on XL level = %d, want 1 (active-passive)", l)
	}
	if n := scalar[string](t, db, `SELECT e.note FROM package_entitlements e JOIN features f ON f.id = e.feature_id WHERE e.price_book_id = $1 AND e.plan_sku = 'plan.s' AND f.key = 'ai_seo'`, planBook); n != synth.DerivedNote {
		t.Fatalf("ai_seo on S note = %q, want the derived-price note", n)
	}
	if n := scalar[string](t, db, `SELECT e.note FROM package_entitlements e JOIN features f ON f.id = e.feature_id WHERE e.price_book_id = $1 AND e.plan_sku = 'plan.xl' AND f.key = 'dedicated_ip'`, planBook); n != synth.DedicatedIPNote {
		t.Fatalf("dedicated IP on XL note = %q", n)
	}
	// The plan prices, converged to the workbook: 2.490 / 4.490 / 7.990 /
	// 13.990 a month = 29.88 / 53.88 / 95.88 / 167.88 a year.
	for _, tc := range []struct {
		sku    string
		annual float64
	}{{"plan.s", 29.88}, {"plan.m", 53.88}, {"plan.l", 95.88}, {"plan.xl", 167.88}} {
		if a := scalar[string](t, db, `SELECT annual_price::text FROM price_items WHERE price_book_id = $1 AND sku = $2`, planBook, tc.sku); !near(f(t, a), tc.annual) {
			t.Fatalf("%s = %s OMR/year, want %v (the workbook's Target)", tc.sku, a, tc.annual)
		}
	}
	// The add-ons priced per plan-hour: 1.500/month → 18/yr → 0.00205479;
	// the dedicated IP at the rate card's 250/yr; the two meters.
	if p := scalar[string](t, db, `SELECT unit_price::text FROM price_items WHERE price_book_id = $1 AND sku = 'addon.backup'`, planBook); !near(f(t, p), 0.00205479) {
		t.Fatalf("addon.backup = %s, want 0.00205479 per plan-hour", p)
	}
	if a := scalar[string](t, db, `SELECT annual_price::text FROM price_items WHERE price_book_id = $1 AND sku = 'addon.dedicated_ip'`, planBook); !near(f(t, a), 250) {
		t.Fatalf("addon.dedicated_ip = %s OMR/year, want 250", a)
	}
	if u := scalar[string](t, db, `SELECT unit FROM price_items WHERE price_book_id = $1 AND sku = 'addon.ai_builder'`, planBook); u != "plan-hour" {
		t.Fatalf("addon.ai_builder unit = %s", u)
	}
	if n := scalar[int](t, db, `SELECT count(*) FROM price_items WHERE price_book_id = $1 AND sku IN ('eip.bandwidth_mbps', 'k8s.pvc_gb')`, planBook); n != 2 {
		t.Fatal("the bandwidth and disk meters are not both priced in the plans book; a metered overage would rate to nothing")
	}
	// The settings: M recommended, every shape from the sheet.
	if n := scalar[int](t, db, `SELECT count(*) FROM package_settings WHERE price_book_id = $1`, planBook); n != 4 {
		t.Fatalf("%d package settings, want 4", n)
	}
	if r := scalar[bool](t, db, `SELECT recommended FROM package_settings WHERE price_book_id = $1 AND plan_sku = 'plan.m'`, planBook); !r {
		t.Fatal("M is not recommended")
	}
	if v := scalar[string](t, db, `SELECT vcpu_guaranteed::text FROM package_settings WHERE price_book_id = $1 AND plan_sku = 'plan.xl'`, planBook); !near(f(t, v), 1.33) {
		t.Fatalf("XL guaranteed vCPU = %s, want 1.33", v)
	}

	// ── the document: the sheet's prices and shapes, the step-up on every gap ──
	doc, err := s.api.getPackages(planBook)
	if err != nil {
		t.Fatalf("document: %v", err)
	}
	if len(doc.Packages) != 4 {
		t.Fatalf("packages = %+v", doc.Packages)
	}
	for i, want := range []struct {
		sku, price, gap, sum string
		vcpu, disk           float64
	}{
		{"plan.s", "2.490", "2.000", "0.000", 1, 25},
		{"plan.m", "4.490", "3.500", "0.000", 2, 50},
		{"plan.l", "7.990", "6.000", "6.000", 4, 100},
		{"plan.xl", "13.990", "", "", 8, 250},
	} {
		p := doc.Packages[i]
		if p.SKU != want.sku || p.PriceMonth != want.price || !near(f(t, p.Shape.VCPU.text()), want.vcpu) || !near(f(t, p.Shape.DiskGB.text()), want.disk) {
			t.Fatalf("package %d = %+v, want %+v", i, p, want)
		}
		if want.gap == "" {
			if p.StepUp != nil {
				t.Fatalf("XL carries a step-up: %+v", p.StepUp)
			}
			continue
		}
		if p.StepUp == nil || p.StepUp.GapMonth != want.gap || p.StepUp.Sum != want.sum || !p.StepUp.RuleHolds {
			t.Fatalf("%s step-up = %+v, want gap %s bundled %s holding", p.SKU, p.StepUp, want.gap, want.sum)
		}
	}
	if !doc.Packages[1].Recommended || doc.Packages[0].Recommended {
		t.Fatalf("recommended = S %v M %v", doc.Packages[0].Recommended, doc.Packages[1].Recommended)
	}

	// ── a second run writes nothing ────────────────────────────────────
	auditFilter := `action LIKE 'feature.%' OR action LIKE 'pricebook.package.%' OR action = 'pricebook.items.put' OR action = 'pricebook.item.update'`
	audits := scalar[int](t, db, `SELECT count(*) FROM audit_log WHERE `+auditFilter)
	updated := scalar[time.Time](t, db, `SELECT max(updated_at) FROM package_entitlements`)
	settingsAt := scalar[time.Time](t, db, `SELECT max(updated_at) FROM package_settings`)
	if err := s.ensurePackages(planBook); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := scalar[int](t, db, `SELECT count(*) FROM audit_log WHERE `+auditFilter); got != audits {
		t.Fatalf("a second run wrote %d audit entries; an unchanged ladder must write nothing", got-audits)
	}
	if got := scalar[time.Time](t, db, `SELECT max(updated_at) FROM package_entitlements`); !got.Equal(updated) {
		t.Fatal("a second run touched a cell that was already as the ladder has it")
	}
	if got := scalar[time.Time](t, db, `SELECT max(updated_at) FROM package_settings`); !got.Equal(settingsAt) {
		t.Fatal("a second run touched a package's settings that were already as the ladder has them")
	}
	// A cell an operator changed is brought BACK — that is a write.
	mustExec(t, db, `UPDATE package_entitlements e SET state = 'not_offered' FROM features f WHERE f.id = e.feature_id AND f.key = 'vuln_dashboard' AND e.plan_sku = 'plan.m' AND e.price_book_id = $1`, planBook)
	if err := s.ensurePackages(planBook); err != nil {
		t.Fatalf("repair run: %v", err)
	}
	if got := state("plan.m", "vuln_dashboard"); got != "included" {
		t.Fatalf("vuln_dashboard on M after the repair = %s, want included", got)
	}

	// ── Nizwa takes the backup add-on, and is billed for it ────────────
	nizwa := s.sc.Customer("nizwa-fintech")
	customerID, sourceID := onboard(t, s, nizwa)
	if err := s.ensureAddons(customerID, sourceID, nizwa); err != nil {
		t.Fatalf("add-ons: %v", err)
	}
	if n := scalar[int](t, db, `SELECT count(*) FROM source_addons WHERE source_id = $1`, sourceID); n != 1 {
		t.Fatalf("%d add-ons on Nizwa's source, want 1 (backup)", n)
	}
	addonAudits := scalar[int](t, db, `SELECT count(*) FROM audit_log WHERE action = 'source.addons'`)
	if err := s.ensureAddons(customerID, sourceID, nizwa); err != nil {
		t.Fatalf("add-ons again: %v", err)
	}
	if got := scalar[int](t, db, `SELECT count(*) FROM audit_log WHERE action = 'source.addons'`); got != addonAudits {
		t.Fatal("a second run re-wrote the add-ons that were already taken")
	}
	// July 2026: Nizwa is on M all month (the scenario switches S → M on 1 July).
	july := synth.DefaultScenario(synth.Window{From: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)}, synth.DefaultSeed)
	out := july.Generate(july.Customer("nizwa-fintech"))
	if _, err := s.writeUsage(customerID, sourceID, out); err != nil {
		t.Fatalf("usage: %v", err)
	}
	var run struct {
		Results []struct {
			apiRunResult
		} `json:"results"`
	}
	if err := s.api.do("POST", "/api/v1/statements/run", map[string]any{"period": "2026-07", "customer_id": customerID}, &run); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(run.Results) != 1 || run.Results[0].Error != "" {
		t.Fatalf("run results = %+v", run.Results)
	}
	if up := run.Results[0].Unpriced; len(up) != 0 {
		t.Fatalf("unpriced %v, want none: the vCPU and memory meters are the basis under a plans book that prices the disk meter", up)
	}
	stID := run.Results[0].StatementID
	// The plan at the workbook's price: 744 × 0.00615068 = 4.576106.
	if a := scalar[string](t, db, `SELECT amount::text FROM rated_lines WHERE statement_id = $1 AND sku = 'plan.m'`, stID); !near(f(t, a), 4.576106) {
		t.Fatalf("plan line = %s, want 4.576106 (744 h at 4.490 a month)", a)
	}
	// 744 plan-hours on M × 0.00205479 = 1.528764, the add-on named.
	var addonAmount, addonDesc string
	if err := db.QueryRow(`SELECT amount::text, description FROM rated_lines WHERE statement_id = $1 AND sku = 'addon.backup'`, stID).Scan(&addonAmount, &addonDesc); err != nil {
		t.Fatalf("add-on line: %v", err)
	}
	if !near(f(t, addonAmount), 1.528764) || addonDesc != "Backup — add-on to M plan" {
		t.Fatalf("add-on line = %s %q", addonAmount, addonDesc)
	}
	// The included BOOLEAN features of M: one 0.000 line each — three
	// (vulnerability dashboard, audit log, cost explorer). The floor, the
	// access doors and the DR level render no line; the quantities are
	// allowances; the optional ones not taken are not on the bill.
	if n := scalar[int](t, db, `SELECT count(*) FROM rated_lines WHERE statement_id = $1 AND sku LIKE 'plan.m.%' AND amount = 0`, stID); n != 3 {
		t.Fatalf("%d included lines, want 3", n)
	}
	if n := scalar[int](t, db, `SELECT count(*) FROM rated_lines WHERE statement_id = $1 AND sku IN ('addon.domain', 'addon.ai_seo', 'addon.ai_builder', 'addon.dedicated_ip', 'plan.m.ssl', 'plan.m.gitea_iac', 'plan.m.dr_topology')`, stID); n != 0 {
		t.Fatalf("%d lines for features Nizwa did not take, floor items, doors or levels", n)
	}
	if d := scalar[string](t, db, `SELECT description FROM rated_lines WHERE statement_id = $1 AND sku = 'plan.m.vuln_dashboard'`, stID); d != "Vulnerability dashboard — included in M plan" {
		t.Fatalf("included line description = %q", d)
	}
	// The disk on M is HARD-CAPPED at 50 GB: Nizwa's volumes run far above
	// it, the line is on the bill at nothing.
	var diskQty, diskAmount string
	if err := db.QueryRow(`SELECT sum(quantity)::text, sum(amount)::text FROM rated_lines WHERE statement_id = $1 AND sku = 'k8s.pvc_gb'`, stID).Scan(&diskQty, &diskAmount); err != nil {
		t.Fatalf("disk lines: %v", err)
	}
	if f(t, diskQty) <= 50*744 || !near(f(t, diskAmount), 0) {
		t.Fatalf("disk = %s gb-hours at %s, want above the cap and billed at 0", diskQty, diskAmount)
	}

	// ── the purge takes the ladder out with the rest ───────────────────
	counts, err := purge(context.Background(), db)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if counts.PackageCells != 72 || counts.AddonRates != 7 || counts.Features != 27 || counts.Customers != 1 {
		t.Fatalf("purge removed %d cells, %d rates, %d features, %d customers; want 72, 7, 27, 1", counts.PackageCells, counts.AddonRates, counts.Features, counts.Customers)
	}
	if n := scalar[int](t, db, `SELECT count(*) FROM features`) + scalar[int](t, db, `SELECT count(*) FROM package_entitlements`) + scalar[int](t, db, `SELECT count(*) FROM source_addons`) + scalar[int](t, db, `SELECT count(*) FROM package_settings`); n != 0 {
		t.Fatalf("%d package rows survived the purge", n)
	}
	// The plans themselves are the product's and stay priced — at the
	// workbook's prices, which the purge does not undo.
	if n := scalar[int](t, db, `SELECT count(*) FROM price_items WHERE price_book_id = $1 AND sku LIKE 'plan.%'`, planBook); n != 4 {
		t.Fatalf("%d plan items after the purge, want the 4 untouched", n)
	}
	if a := scalar[string](t, db, `SELECT annual_price::text FROM price_items WHERE price_book_id = $1 AND sku = 'plan.m'`, planBook); !near(f(t, a), 53.88) {
		t.Fatalf("plan.m after the purge = %s, want 53.88 kept", a)
	}
}

// A feature an operator's own book still carries survives the purge: the
// seeder removes what it made, never a matrix that explains someone else's
// invoice.
func TestPurgeKeepsAFeatureAnotherBookStillCarries(t *testing.T) {
	s, db := setupSeeder(t, synth.DefaultWindow())
	if err := s.ensureBooks(false, true, "", ""); err != nil {
		t.Fatalf("books: %v", err)
	}
	planBook := s.books[s.sc.PlanBookName]
	if err := s.ensurePackages(planBook); err != nil {
		t.Fatalf("packages: %v", err)
	}
	// An operator's negotiated clone carries the whole matrix.
	var clone struct {
		ID string `json:"id"`
	}
	if err := s.api.do("POST", "/api/v1/pricebooks/"+planBook+"/clone", map[string]any{"name": "Acme negotiated packages"}, &clone); err != nil {
		t.Fatalf("clone: %v", err)
	}
	counts, err := purge(context.Background(), db)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if counts.PackageCells != 72 || counts.Features != 9 {
		t.Fatalf("purge removed %d cells and %d features; the clone still carries every cell, so only the 9 floor items (no cell anywhere) may go", counts.PackageCells, counts.Features)
	}
	if n := scalar[int](t, db, `SELECT count(*) FROM features`); n != 18 {
		t.Fatalf("%d features after the purge, want the 18 kept for the clone", n)
	}
	if n := scalar[int](t, db, `SELECT count(*) FROM package_entitlements WHERE price_book_id = $1`, clone.ID); n != 72 {
		t.Fatalf("%d cells on the clone after the purge, want 72", n)
	}
}

// The ladder converges a book that carries the EARLIER baseline — the
// fifteen boolean features with cells on every package, as 0.1.57 seeded
// hw307: the nine that are now floor items lose their cells and move to the
// floor, backup keeps its optional cells, bandwidth gains its overage, and
// the rest of the ladder arrives.
func TestLadderConvergesTheEarlierBaseline(t *testing.T) {
	s, db := setupSeeder(t, synth.DefaultWindow())
	if err := s.ensureBooks(false, true, "", ""); err != nil {
		t.Fatalf("books: %v", err)
	}
	planBook := s.books[s.sc.PlanBookName]
	// The earlier baseline, by hand: boolean features, included everywhere.
	for i, key := range []string{"applications", "ssl", "waf", "support", "backup"} {
		body := map[string]any{"key": key, "name": key, "kind": "boolean", "sort_order": i + 1}
		if key == "backup" {
			body["addon_sku"] = "addon.backup"
		}
		if _, err := s.api.createFeature(body); err != nil {
			t.Fatalf("feature %s: %v", key, err)
		}
	}
	if err := s.api.putPriceItems(planBook, []apiPriceItem{{SKU: "addon.backup", Unit: "plan-hour", AnnualPrice: "18"}}); err != nil {
		t.Fatal(err)
	}
	for _, p := range synth.PlanSlugs {
		for _, key := range []string{"applications", "ssl", "waf", "support"} {
			if err := s.api.putPackageCell(planBook, "plan."+p, key, map[string]any{"state": "included"}); err != nil {
				t.Fatalf("cell %s %s: %v", p, key, err)
			}
		}
		st := "optional"
		if p == "xl" {
			st = "included"
		}
		if err := s.api.putPackageCell(planBook, "plan."+p, "backup", map[string]any{"state": st}); err != nil {
			t.Fatalf("backup on %s: %v", p, err)
		}
	}
	if n := scalar[int](t, db, `SELECT count(*) FROM package_entitlements`); n != 20 {
		t.Fatalf("baseline cells = %d", n)
	}
	if err := s.ensurePackages(planBook); err != nil {
		t.Fatalf("converge: %v", err)
	}
	if n := scalar[int](t, db, `SELECT count(*) FROM package_entitlements WHERE price_book_id = $1`, planBook); n != 72 {
		t.Fatalf("%d cells after converging, want 72", n)
	}
	if g := scalar[string](t, db, `SELECT feature_group FROM features WHERE key = 'waf'`); g != "floor" {
		t.Fatalf("waf group = %s, want floor", g)
	}
	if n := scalar[int](t, db, `SELECT count(*) FROM package_entitlements e JOIN features f ON f.id = e.feature_id WHERE f.key IN ('applications', 'ssl', 'waf', 'support')`); n != 0 {
		t.Fatalf("%d cells left on floor items", n)
	}
	if g := scalar[string](t, db, `SELECT feature_group FROM features WHERE key = 'backup'`); g != "resilience" {
		t.Fatalf("backup group = %s", g)
	}
	if n := scalar[int](t, db, `SELECT count(*) FROM features`); n != 27 {
		t.Fatalf("%d features, want 27", n)
	}
}
