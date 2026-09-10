package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// Pay per use for flexi Organizations (EPIC #6867, founder direction
// 2026-09-10). Two shapes, and the whole point is that they never overlap:
//
//   - a SIZED Organization (s/m/l/xl) sits on the "OpenOva plans" book and
//     pays one flat plan.<slug> line; its k8s.* meters carry no rate there,
//     so it is never charged per vCPU on top of the plan it already bought.
//   - a FLEXI Organization has no quota ceiling, so there is no bundle to
//     sell: the collector emits no plan line at all, it sits on the
//     "Organization PAYG" book, and it pays for exactly what it ran.
//
// Before this change a flexi Organization was billed NOTHING: it got no plan
// line by design and its meters were unpriced under the only book the sync
// ever assigned.

// TestIntegrationEnsurePAYGBookIdempotent: the pay-per-use book is created
// once, at PLATFORM scope (a cloud book could never be assigned to a
// platform source), with the three derived rates and the derivation on the
// book itself — and is never re-created or re-priced afterwards.
func TestIntegrationEnsurePAYGBookIdempotent(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	pb, created, err := st.EnsurePAYGBook(ctx)
	if err != nil || !created {
		t.Fatalf("first ensure: created=%v err=%v", created, err)
	}
	if pb.Name != store.PAYGBookName || pb.Currency != "OMR" || pb.AnnualDivisor != store.PAYGBookDivisor {
		t.Fatalf("book = %+v", pb)
	}
	if pb.Scope != store.LayerPlatform {
		t.Fatalf("scope = %q, want platform: k8s.* are platform SKUs and a cloud book can never be assigned to a platform source", pb.Scope)
	}
	if !strings.Contains(pb.Description, "2.75") || !strings.Contains(pb.Description, "0.375") {
		t.Fatalf("the book carries no derivation the operator can read: %q", pb.Description)
	}
	want := map[string][2]string{
		"k8s.vcpu":   {"24.00000000", "0.00273973"},
		"k8s.mem_gb": {"4.50000000", "0.00051370"},
		"k8s.pvc_gb": {"2.62800000", "0.00030000"},
	}
	if len(pb.Items) != len(want) {
		t.Fatalf("items = %+v", pb.Items)
	}
	for _, it := range pb.Items {
		w, ok := want[it.SKU]
		if !ok || it.AnnualPrice == nil || string(*it.AnnualPrice) != w[0] || string(it.UnitPrice) != w[1] {
			t.Fatalf("item %s = annual %v price %s, want %v", it.SKU, it.AnnualPrice, it.UnitPrice, w)
		}
		if it.Description == "" {
			t.Fatalf("item %s has no description stating the arithmetic", it.SKU)
		}
	}
	// No plan line here: a flexi Organization emits none, and pricing one
	// under this book is exactly the double charge to avoid.
	for _, sku := range []string{"plan.s", "plan.m", "plan.l", "plan.xl", "plan.flexi"} {
		if _, err := st.GetPriceItem(ctx, pb.ID, sku); err == nil {
			t.Fatalf("%s must not be priced in the pay-per-use book", sku)
		}
	}

	// The operator negotiates the vCPU rate down; a second ensure keeps it.
	negotiated := store.Decimal("0.002")
	if _, err := st.UpdatePriceItem(ctx, pb.ID, "k8s.vcpu", store.PriceItemPatch{UnitPrice: &negotiated}); err != nil {
		t.Fatal(err)
	}
	again, created, err := st.EnsurePAYGBook(ctx)
	if err != nil || created || again.ID != pb.ID {
		t.Fatalf("second ensure: id %s→%s created=%v err=%v", pb.ID, again.ID, created, err)
	}
	it, err := st.GetPriceItem(ctx, pb.ID, "k8s.vcpu")
	if err != nil || string(it.UnitPrice) != "0.00200000" {
		t.Fatalf("operator edit lost: %+v err=%v", it, err)
	}

	// Both platform books stand side by side, each with its own scope and
	// description — this is what the operator sees on the Price books page.
	plans, _, err := st.EnsurePlanBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if plans.Scope != store.LayerPlatform || plans.Description == "" {
		t.Fatalf("plans book = %+v", plans)
	}
	books, err := st.ListPriceBooks(ctx)
	if err != nil || len(books) != 2 {
		t.Fatalf("books = %d err=%v, want the two platform books", len(books), err)
	}
}

// TestIntegrationSizedAndFlexiAreNeverDoubleCharged is the money proof, over
// one known 7-day window, to the exact minor unit:
//
//   - the Organization on plan m bills 7 × 24 × (9/730) and NOTHING else,
//     even though its pods metered 4 vCPU + 8 GiB the whole week;
//   - the flexi Organization running the same 4 vCPU + 8 GiB bills
//     7 × 24 × (4 × k8s.vcpu + 8 × k8s.mem_gb) and carries NO plan line.
//
// It cannot pass on the code before this change: there the flexi
// Organization was on the plans book, its meters were unpriced, and its
// whole bill was 0.
func TestIntegrationSizedAndFlexiAreNeverDoubleCharged(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	plans, _, err := st.EnsurePlanBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	payg, _, err := st.EnsurePAYGBook(ctx)
	if err != nil {
		t.Fatal(err)
	}

	sized, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "sized", Name: "Sized", AdminEmail: "sized@x.example", Kind: "organization", OrgSlug: "sized", PlanSlug: "m"})
	if err != nil {
		t.Fatal(err)
	}
	flexi, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "flexi", Name: "Flexi", AdminEmail: "flexi@x.example", Kind: "organization", OrgSlug: "flexi", PlanSlug: store.PlanFlexi})
	if err != nil {
		t.Fatal(err)
	}
	sizedSrc, _, err := st.UpsertSource(ctx, sized.ID, "openova-org", "", "sized")
	if err != nil {
		t.Fatal(err)
	}
	flexiSrc, _, err := st.UpsertSource(ctx, flexi.ID, "openova-org", "", "flexi")
	if err != nil {
		t.Fatal(err)
	}
	assignBook(t, st, sizedSrc.ID, plans.ID)
	assignBook(t, st, flexiSrc.ID, payg.ID)

	// One week of hours. BOTH Organizations run the identical shape — 4 vCPU
	// and 8 GiB of requests — so any difference in the bill is the billing
	// shape and nothing else. Only the sized one also carries a plan line;
	// the collector emits none for flexi (billablePlan).
	from, to := day(2026, 9, 1), day(2026, 9, 8)
	planLabels, _ := json.Marshal(map[string]any{"name": "M plan", "plan": "m"})
	podLabels, _ := json.Marshal(map[string]any{"tier": "organization"})
	var recs []store.UsageRecord
	for h := 0; h < 7*24; h++ {
		at := from.Add(time.Duration(h) * time.Hour)
		end := at.Add(time.Hour)
		for _, o := range []struct {
			cust, src, pod string
		}{{sized.ID, sizedSrc.ID, "sized/pod-1"}, {flexi.ID, flexiSrc.ID, "flexi/pod-1"}} {
			recs = append(recs,
				store.UsageRecord{CustomerID: o.cust, SourceID: o.src, ResourceID: o.pod, ResourceKind: "k8s-pod", SKU: store.SKUVCPU, Quantity: "4.000000", Unit: store.UnitVCPU, WindowStart: at, WindowEnd: end, Labels: podLabels},
				store.UsageRecord{CustomerID: o.cust, SourceID: o.src, ResourceID: o.pod, ResourceKind: "k8s-pod", SKU: store.SKUMem, Quantity: "8.000000", Unit: store.UnitMem, WindowStart: at, WindowEnd: end, Labels: podLabels},
			)
		}
		recs = append(recs, store.UsageRecord{CustomerID: sized.ID, SourceID: sizedSrc.ID, ResourceID: "plan/m", ResourceKind: store.PlanKind, SKU: "plan.m", Quantity: "1.000000", Unit: store.PlanUnit, WindowStart: at, WindowEnd: end, Labels: planLabels})
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}

	hours := big.NewRat(7*24, 1)
	rateOf := func(bookID, sku string) *big.Rat {
		t.Helper()
		it, err := st.GetPriceItem(ctx, bookID, sku)
		if err != nil {
			t.Fatalf("rate %s in book %s: %v", sku, bookID, err)
		}
		r, ok := new(big.Rat).SetString(string(it.UnitPrice))
		if !ok {
			t.Fatalf("rate %s = %q is not a number", sku, it.UnitPrice)
		}
		return r
	}
	money := func(r *big.Rat) string { return r.FloatString(6) }

	// ---- the sized Organization: its plan and nothing else ---------------
	wantPlan := new(big.Rat).Mul(rateOf(plans.ID, "plan.m"), hours)
	bySKU := skuTotals(t, st, sized.ID, from, to)
	if got := bySKU["plan.m"]; got != money(wantPlan) {
		t.Fatalf("plan.m = %s, want %s (7 × 24 × 9/730)", got, money(wantPlan))
	}
	for _, sku := range store.PlatformMeterSKUs {
		if got, ok := bySKU[sku]; ok && got != "0.000000" {
			t.Fatalf("the plan m Organization was charged %s for %s ON TOP of its plan — that is the double charge", got, sku)
		}
	}
	if len(bySKU) != 3 || bySKU["k8s.vcpu"] != "0.000000" || bySKU["k8s.mem_gb"] != "0.000000" {
		t.Fatalf("sized Organization SKUs = %v, want plan.m priced and the two meters at zero", bySKU)
	}
	sizedTotal := exploreTotal(t, st, sized.ID, from, to)
	if sizedTotal != money(wantPlan) {
		t.Fatalf("sized Organization total = %s, want exactly its plan line %s", sizedTotal, money(wantPlan))
	}

	// ---- the flexi Organization: what it ran, and no plan line -----------
	wantVCPU := new(big.Rat).Mul(new(big.Rat).Mul(rateOf(payg.ID, store.SKUVCPU), big.NewRat(4, 1)), hours)
	wantMem := new(big.Rat).Mul(new(big.Rat).Mul(rateOf(payg.ID, store.SKUMem), big.NewRat(8, 1)), hours)
	wantFlexi := new(big.Rat).Add(wantVCPU, wantMem)
	bySKU = skuTotals(t, st, flexi.ID, from, to)
	if got := bySKU[store.SKUVCPU]; got != money(wantVCPU) {
		t.Fatalf("k8s.vcpu = %s, want %s (7 × 24 × 4 × rate)", got, money(wantVCPU))
	}
	if got := bySKU[store.SKUMem]; got != money(wantMem) {
		t.Fatalf("k8s.mem_gb = %s, want %s (7 × 24 × 8 × rate)", got, money(wantMem))
	}
	for sku := range bySKU {
		if strings.HasPrefix(sku, store.PlanSKUPrefix) {
			t.Fatalf("the flexi Organization carries a plan line %s — it buys no plan", sku)
		}
	}
	flexiTotal := exploreTotal(t, st, flexi.ID, from, to)
	if flexiTotal != money(wantFlexi) {
		t.Fatalf("flexi Organization total = %s, want exactly its metered use %s", flexiTotal, money(wantFlexi))
	}
	if flexiTotal == "0.000000" {
		t.Fatal("the flexi Organization was billed nothing — this is the hole the pay-per-use book closes")
	}

	// Pay per use over a full week of the same shape costs more than the
	// committed plan of that shape: no commitment, so a premium.
	if wantFlexi.Cmp(wantPlan) <= 0 {
		t.Fatalf("pay per use (%s) does not sit above the committed M plan (%s) for the same 4 vCPU + 8 GiB", money(wantFlexi), money(wantPlan))
	}

	// The meters under the plans book are "not sold per use", not a hole in
	// the rate card; under the pay-per-use book they ARE the bill.
	cov, err := st.PriceBookCoverage(ctx, plans.ID, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if cov.NotSoldCount != 2 || cov.UnpricedCount != 0 {
		t.Fatalf("plans book coverage = %+v, want the two meters not-sold-per-use and nothing unpriced", cov)
	}
	cov, err = st.PriceBookCoverage(ctx, payg.ID, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if cov.NotSoldCount != 0 || cov.UnpricedCount != 0 || cov.CoveragePct != 100 {
		t.Fatalf("pay-per-use book coverage = %+v, want every meter in use priced", cov)
	}
}

// skuTotals is the customer's window cost per SKU, as the explorer reports it.
func skuTotals(t *testing.T, st *store.Store, customerID string, from, to time.Time) map[string]string {
	t.Helper()
	res, err := st.Explore(context.Background(), store.OperatorScope, store.CostQuery{From: from, To: to, GroupBy: "sku", CustomerID: customerID})
	if err != nil {
		t.Fatalf("explore %s: %v", customerID, err)
	}
	out := map[string]string{}
	for _, g := range res.Groups {
		out[g.Key] = string(g.Total)
	}
	return out
}

// exploreTotal is the customer's whole window cost.
func exploreTotal(t *testing.T, st *store.Store, customerID string, from, to time.Time) string {
	t.Helper()
	res, err := st.Explore(context.Background(), store.OperatorScope, store.CostQuery{From: from, To: to, CustomerID: customerID})
	if err != nil {
		t.Fatalf("explore %s: %v", customerID, err)
	}
	return string(res.Total.Current)
}

// ---------------------------------------------------------------------------
// The one-time normalisation migration
// ---------------------------------------------------------------------------

// openPAYGUnmigrated stands a private schema at the shape BEFORE the
// pay-per-use migration, so the migration can be applied over the rows a
// Sovereign really carries (the hw307 shape: an "Organization PAYG" book at
// CLOUD scope, holding placeholder rates, assigned to nothing).
func openPAYGUnmigrated(t *testing.T, schema string) *sql.DB {
	t.Helper()
	dsn := os.Getenv(testdb.EnvVar)
	if dsn == "" {
		t.Skipf("%s not set; skipping integration test", testdb.EnvVar)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1) // the shape lives in a session-local search_path
	for _, stmt := range []string{`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`, `CREATE SCHEMA ` + schema} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := db.ExecContext(ctx, `SET search_path = `+schema+`, public`); err != nil {
		t.Fatalf("search_path: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = db.ExecContext(c, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})
	return db
}

// TestIntegrationPAYGBookMigration applies the appended migration over the
// book as it shipped and asserts every clause of it.
func TestIntegrationPAYGBookMigration(t *testing.T) {
	t.Run("unassigned book is corrected", func(t *testing.T) {
		db := openPAYGUnmigrated(t, "payg_migration")
		ctx := context.Background()
		st := store.New(db)
		if err := st.MigrateUpTo(ctx, store.MigrationPAYGPlatformBooks-1); err != nil {
			t.Fatalf("migrate to the pre-change shape: %v", err)
		}
		var hasDescription bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'payg_migration' AND table_name = 'price_books' AND column_name = 'description')`).Scan(&hasDescription); err != nil {
			t.Fatal(err)
		}
		if hasDescription {
			t.Fatal("price_books.description already exists before the migration under test — the test proves nothing")
		}
		// The hw307 rows: both platform books present, the pay-per-use one
		// at CLOUD scope (so no platform source could ever be pointed at it)
		// carrying placeholder rates an order of magnitude out.
		var paygID, plansID string
		if err := db.QueryRowContext(ctx, `INSERT INTO price_books (name, scope, currency, annual_divisor, bill_stopped) VALUES ($1, 'cloud', 'OMR', 8760, 'compute') RETURNING id`, store.PAYGBookName).Scan(&paygID); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `INSERT INTO price_books (name, scope, currency, annual_divisor, bill_stopped) VALUES ($1, 'platform', 'OMR', 8760, 'compute') RETURNING id`, store.PlanBookName).Scan(&plansID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO price_items (price_book_id, sku, unit, unit_price, description) VALUES
			($1, 'k8s.vcpu', 'vcpu-hour', 0.02589041, 'placeholder'),
			($1, 'k8s.mem_gb', 'gib-hour', 0.00345205, 'placeholder'),
			($1, 'k8s.pvc_gb', 'gb-hour', 0.00022831, 'placeholder')`, paygID); err != nil {
			t.Fatal(err)
		}

		if err := st.Migrate(ctx); err != nil {
			t.Fatalf("apply the pay-per-use migration: %v", err)
		}

		pb, err := st.GetPriceBook(ctx, paygID)
		if err != nil {
			t.Fatal(err)
		}
		if pb.Scope != store.LayerPlatform {
			t.Fatalf("scope = %q, want platform — a cloud book can never be assigned to a platform source", pb.Scope)
		}
		if pb.Description != store.PAYGBookDescription {
			t.Fatalf("description = %q", pb.Description)
		}
		want := map[string]string{}
		for _, it := range store.PAYGBookItems() {
			want[it.SKU] = string(it.UnitPrice)
		}
		if len(pb.Items) != len(want) {
			t.Fatalf("items = %+v", pb.Items)
		}
		for _, it := range pb.Items {
			if string(it.UnitPrice) != want[it.SKU] {
				t.Fatalf("%s = %s, want the derived %s (the placeholder was 18.90 OMR per vCPU-month)", it.SKU, it.UnitPrice, want[it.SKU])
			}
			if it.Description == "placeholder" {
				t.Fatalf("%s kept its placeholder description", it.SKU)
			}
		}
		plans, err := st.GetPriceBook(ctx, plansID)
		if err != nil {
			t.Fatal(err)
		}
		if plans.Description != store.PlanBookDescription {
			t.Fatalf("the plans book description was not backfilled: %q", plans.Description)
		}
		// Idempotent: a second Migrate is a no-op, and re-running the
		// statements changes nothing.
		if err := st.Migrate(ctx); err != nil {
			t.Fatalf("second migrate: %v", err)
		}
	})

	t.Run("a book that already rates a source keeps its rates and its words", func(t *testing.T) {
		db := openPAYGUnmigrated(t, "payg_migration_assigned")
		ctx := context.Background()
		st := store.New(db)
		if err := st.MigrateUpTo(ctx, store.MigrationPAYGPlatformBooks-1); err != nil {
			t.Fatalf("migrate to the pre-change shape: %v", err)
		}
		var paygID, custID, srcID string
		if err := db.QueryRowContext(ctx, `INSERT INTO price_books (name, scope, currency, annual_divisor, bill_stopped) VALUES ($1, 'platform', 'OMR', 8760, 'compute') RETURNING id`, store.PAYGBookName).Scan(&paygID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO price_items (price_book_id, sku, unit, unit_price, description) VALUES ($1, 'k8s.vcpu', 'vcpu-hour', 0.00100000, 'negotiated with the customer')`, paygID); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `INSERT INTO customers (slug, name, admin_email, kind, org_slug, billing_mode, status, plan_slug) VALUES ('acme', 'Acme', 'a@acme.example', 'organization', 'acme', 'chargeback', 'active', 'flexi') RETURNING id`).Scan(&custID); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `INSERT INTO cost_sources (customer_id, kind, region, project_id, status, price_book_id) VALUES ($1, 'openova-org', '', 'acme', 'verified', $2) RETURNING id`, custID, paygID).Scan(&srcID); err != nil {
			t.Fatal(err)
		}

		if err := st.Migrate(ctx); err != nil {
			t.Fatalf("apply the pay-per-use migration: %v", err)
		}

		pb, err := st.GetPriceBook(ctx, paygID)
		if err != nil {
			t.Fatal(err)
		}
		if len(pb.Items) != 1 || string(pb.Items[0].UnitPrice) != "0.00100000" || pb.Items[0].Description != "negotiated with the customer" {
			t.Fatalf("a book that already rates a source was re-priced: %+v", pb.Items)
		}
		if pb.Description != store.PAYGBookDescription {
			t.Fatalf("the description is a note, not a rate — it is still backfilled when empty: %q", pb.Description)
		}
	})
}
