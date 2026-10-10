package main

import (
	"context"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/synth"
)

// The showcase package matrix, end to end against a real API (DESIGN.md §7,
// §22): the seeder writes the fifteen features and every cell of the baseline
// onto the plans book and prices the add-ons there, a second run writes
// nothing, Nizwa Fintech's Source takes the backup add-on, a July statement
// carries the add-on line and the included 0.000 lines, and the purge takes
// the matrix out again with the rest of the showcase.
func TestShowcasePackagesAreSeededBilledAndPurged(t *testing.T) {
	s, db := setupSeeder(t, synth.DefaultWindow())
	if err := s.ensureBooks(false, true, "", ""); err != nil {
		t.Fatalf("books: %v", err)
	}
	planBook := s.books[s.sc.PlanBookName]
	if err := s.ensurePackages(planBook); err != nil {
		t.Fatalf("packages: %v", err)
	}

	// ── the matrix, exactly the baseline ───────────────────────────────
	if n := scalar[int](t, db, `SELECT count(*) FROM features`); n != 15 {
		t.Fatalf("%d features, want 15", n)
	}
	if n := scalar[int](t, db, `SELECT count(*) FROM package_entitlements WHERE price_book_id = $1`, planBook); n != 60 {
		t.Fatalf("%d cells, want 15 features × 4 packages = 60", n)
	}
	state := func(plan, key string) string {
		return scalar[string](t, db, `SELECT e.state FROM package_entitlements e JOIN features f ON f.id = e.feature_id WHERE e.price_book_id = $1 AND e.plan_sku = $2 AND f.key = $3`, planBook, plan, key)
	}
	for _, tc := range []struct{ plan, key, want string }{
		{"plan.s", "backup", "optional"}, {"plan.l", "backup", "optional"}, {"plan.xl", "backup", "included"},
		{"plan.s", "dedicated_ip", "optional"}, {"plan.xl", "dedicated_ip", "optional"},
		{"plan.s", "ssl", "included"}, {"plan.m", "domain", "optional"}, {"plan.xl", "domain", "included"},
		{"plan.s", "bandwidth", "included"},
	} {
		if got := state(tc.plan, tc.key); got != tc.want {
			t.Fatalf("%s on %s = %s, want %s", tc.key, tc.plan, got, tc.want)
		}
	}
	if q := scalar[string](t, db, `SELECT e.included_quantity::text FROM package_entitlements e JOIN features f ON f.id = e.feature_id WHERE e.price_book_id = $1 AND e.plan_sku = 'plan.l' AND f.key = 'bandwidth'`, planBook); !near(f(t, q), 250) {
		t.Fatalf("bandwidth on L = %s, want 250", q)
	}
	// The add-ons priced per plan-hour: 1.500/month → 18/yr → 0.00205479.
	if p := scalar[string](t, db, `SELECT unit_price::text FROM price_items WHERE price_book_id = $1 AND sku = 'addon.backup'`, planBook); !near(f(t, p), 0.00205479) {
		t.Fatalf("addon.backup = %s, want 0.00205479 per plan-hour", p)
	}
	if u := scalar[string](t, db, `SELECT unit FROM price_items WHERE price_book_id = $1 AND sku = 'addon.ai_builder'`, planBook); u != "plan-hour" {
		t.Fatalf("addon.ai_builder unit = %s", u)
	}
	if n := scalar[int](t, db, `SELECT count(*) FROM price_items WHERE price_book_id = $1 AND sku = 'eip.bandwidth_mbps'`, planBook); n != 1 {
		t.Fatal("the bandwidth meter is not priced in the plans book; the excess over the included quantity would rate to nothing")
	}

	// ── a second run writes nothing ────────────────────────────────────
	audits := scalar[int](t, db, `SELECT count(*) FROM audit_log WHERE action LIKE 'feature.%' OR action LIKE 'pricebook.package.%' OR action = 'pricebook.items.put'`)
	updated := scalar[time.Time](t, db, `SELECT max(updated_at) FROM package_entitlements`)
	if err := s.ensurePackages(planBook); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := scalar[int](t, db, `SELECT count(*) FROM audit_log WHERE action LIKE 'feature.%' OR action LIKE 'pricebook.package.%' OR action = 'pricebook.items.put'`); got != audits {
		t.Fatalf("a second run wrote %d audit entries; an unchanged matrix must write nothing", got-audits)
	}
	if got := scalar[time.Time](t, db, `SELECT max(updated_at) FROM package_entitlements`); !got.Equal(updated) {
		t.Fatal("a second run touched a cell that was already as the matrix has it")
	}
	// A cell an operator changed is brought BACK — that is a write.
	mustExec(t, db, `UPDATE package_entitlements e SET state = 'not_offered' FROM features f WHERE f.id = e.feature_id AND f.key = 'ssl' AND e.plan_sku = 'plan.s' AND e.price_book_id = $1`, planBook)
	if err := s.ensurePackages(planBook); err != nil {
		t.Fatalf("repair run: %v", err)
	}
	if got := state("plan.s", "ssl"); got != "included" {
		t.Fatalf("ssl on S after the repair = %s, want included", got)
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
	stID := run.Results[0].StatementID
	// 744 plan-hours on M × 0.00205479 = 1.528764, the add-on named.
	var addonAmount, addonDesc string
	if err := db.QueryRow(`SELECT amount::text, description FROM rated_lines WHERE statement_id = $1 AND sku = 'addon.backup'`, stID).Scan(&addonAmount, &addonDesc); err != nil {
		t.Fatalf("add-on line: %v", err)
	}
	if !near(f(t, addonAmount), 1.528764) || addonDesc != "Backup — add-on to M plan" {
		t.Fatalf("add-on line = %s %q", addonAmount, addonDesc)
	}
	// The included features: one 0.000 line each, nine of them on M
	// (applications, databases, mail, ssl, sso, ddos, malware_scanner, waf,
	// support); bandwidth is an allowance, not a line; the optional ones
	// not taken are not on the bill.
	if n := scalar[int](t, db, `SELECT count(*) FROM rated_lines WHERE statement_id = $1 AND sku LIKE 'plan.m.%' AND amount = 0`, stID); n != 9 {
		t.Fatalf("%d included lines, want 9", n)
	}
	if n := scalar[int](t, db, `SELECT count(*) FROM rated_lines WHERE statement_id = $1 AND sku IN ('addon.domain', 'addon.ai_seo', 'addon.ai_builder', 'addon.dedicated_ip')`, stID); n != 0 {
		t.Fatalf("%d add-on lines for features Nizwa did not take", n)
	}
	if d := scalar[string](t, db, `SELECT description FROM rated_lines WHERE statement_id = $1 AND sku = 'plan.m.ssl'`, stID); d != "Unlimited free SSL — included in M plan" {
		t.Fatalf("ssl line description = %q", d)
	}

	// ── the purge takes the matrix out with the rest ───────────────────
	counts, err := purge(context.Background(), db)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if counts.PackageCells != 60 || counts.AddonRates != 6 || counts.Features != 15 || counts.Customers != 1 {
		t.Fatalf("purge removed %d cells, %d add-on rates, %d features, %d customers; want 60, 6, 15, 1", counts.PackageCells, counts.AddonRates, counts.Features, counts.Customers)
	}
	if n := scalar[int](t, db, `SELECT count(*) FROM features`) + scalar[int](t, db, `SELECT count(*) FROM package_entitlements`) + scalar[int](t, db, `SELECT count(*) FROM source_addons`); n != 0 {
		t.Fatalf("%d package rows survived the purge", n)
	}
	// The plans themselves are the product's and stay priced.
	if n := scalar[int](t, db, `SELECT count(*) FROM price_items WHERE price_book_id = $1 AND sku LIKE 'plan.%'`, planBook); n != 4 {
		t.Fatalf("%d plan items after the purge, want the 4 untouched", n)
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
	if counts.PackageCells != 60 || counts.Features != 0 {
		t.Fatalf("purge removed %d cells and %d features; the clone still carries every feature, so none may go", counts.PackageCells, counts.Features)
	}
	if n := scalar[int](t, db, `SELECT count(*) FROM features`); n != 15 {
		t.Fatalf("%d features after the purge, want 15 kept for the clone", n)
	}
	if n := scalar[int](t, db, `SELECT count(*) FROM package_entitlements WHERE price_book_id = $1`, clone.ID); n != 60 {
		t.Fatalf("%d cells on the clone after the purge, want 60", n)
	}
}
