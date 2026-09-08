package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// The two-layer ownership model (DESIGN.md §2, founder direction
// 2026-09-08). A customer owns sources; a price book is assigned PER SOURCE
// and must match the source's layer; a customer's statement is the sum of
// its sources, each rated by its own book; the Sovereign itself is not a
// customer and its footprint lives on the internal source, which only
// Allocation reads.
//
// Every number below is exact: the cloud source runs one ECS at 0.5/h for
// 7 days (84.000000 OMR) and the platform source one plan.m hour per hour
// at 0.25/h (42.000000 OMR), so the statement subtotal is 126.000000.

type twoLayerSeed struct {
	cust      store.Customer
	cloudSrc  store.CostSource
	platSrc   store.CostSource
	internal  store.CostSource
	cloudBook store.PriceBook
	planBook  store.PriceBook
}

func seedTwoLayer(t *testing.T, st *store.Store) twoLayerSeed {
	t.Helper()
	ctx := context.Background()
	cloudBook, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "National Cloud list", Scope: store.LayerCloud, Currency: "OMR", AnnualDivisor: 8760, BillStopped: "compute"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, cloudBook.ID, []store.PriceItem{{SKU: "ecs.m7n.xlarge.8", Unit: "instance-hour", UnitPrice: "0.5"}}, true); err != nil {
		t.Fatal(err)
	}
	planBook, _, err := st.EnsurePlanBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The plan book prices plan.<slug> only; give plan.m the round 0.25 so
	// the arithmetic below is exact to the last digit.
	price := store.Decimal("0.25")
	if _, err := st.UpdatePriceItem(ctx, planBook.ID, "plan.m", store.PriceItemPatch{UnitPrice: &price}); err != nil {
		t.Fatal(err)
	}
	cust, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "acme", Name: "Acme", AdminEmail: "a@acme.example", Kind: "organization", OrgSlug: "acme", PlanSlug: "m", StartDate: "2026-08-01"})
	if err != nil {
		t.Fatal(err)
	}
	cloudSrc, _, err := st.UpsertSource(ctx, cust.ID, store.SourceKindHuaweiProject, "me-east-215", "proj-acme")
	if err != nil {
		t.Fatal(err)
	}
	platSrc, _, err := st.UpsertSource(ctx, cust.ID, store.SourceKindOrg, "", "acme")
	if err != nil {
		t.Fatal(err)
	}
	internal, _, err := st.EnsureInternalSource(ctx, "hw307-omani-works")
	if err != nil {
		t.Fatal(err)
	}
	assignBook(t, st, cloudSrc.ID, cloudBook.ID)
	assignBook(t, st, platSrc.ID, planBook.ID)

	var recs []store.UsageRecord
	rec := func(customerID string, src store.CostSource, res, kind, sku, unit string, qty float64, at time.Time, labels map[string]any) {
		lb, _ := json.Marshal(labels)
		recs = append(recs, store.UsageRecord{CustomerID: customerID, SourceID: src.ID, ResourceID: res, ResourceKind: kind, SKU: sku,
			Quantity: store.Decimal(strconv.FormatFloat(qty, 'f', 6, 64)), Unit: unit, WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-215", Labels: lb})
	}
	for d := 1; d <= 7; d++ {
		for h := 0; h < 24; h++ {
			at := day(2026, 8, d).Add(time.Duration(h) * time.Hour)
			rec(cust.ID, cloudSrc, "vm-1", "ecs", "ecs.m7n.xlarge.8", "instance-hour", 1, at, map[string]any{"name": "web-1", "status": "ACTIVE"})
			rec(cust.ID, platSrc, "plan/m", store.PlanKind, "plan.m", store.PlanUnit, 1, at, map[string]any{"name": "M plan", "plan": "m"})
			rec(cust.ID, platSrc, "acme/pod-1", "k8s-pod", "k8s.vcpu", "vcpu-hour", 2, at, map[string]any{"namespace": "acme", "tier": "organization"})
			// The Sovereign's own footprint: the internal source, no customer.
			rec("", internal, "gitea/pod-1", "k8s-pod", "k8s.vcpu", "vcpu-hour", 4, at, map[string]any{"namespace": "gitea", "tier": "platform-overhead"})
		}
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	return twoLayerSeed{cust: cust, cloudSrc: cloudSrc, platSrc: platSrc, internal: internal, cloudBook: cloudBook, planBook: planBook}
}

// TestIntegrationTwoLayerStatementSumsBothSources: one customer, a cloud
// source on the cloud book and a platform source on the plans book. The
// explorer total and the statement subtotal are both the SUM of the two
// ratings — 84 (cloud) + 42 (plan) = 126 — which is only true when each
// source is priced by its OWN book. On the previous model, where the book
// hung off the customer, one of the two layers rated to zero.
func TestIntegrationTwoLayerStatementSumsBothSources(t *testing.T) {
	st := testdb.Open(t)
	s := seedTwoLayer(t, st)
	ctx := context.Background()

	ex, err := st.Explore(ctx, store.OperatorScope, store.CostQuery{From: day(2026, 8, 1), To: day(2026, 8, 8), Granularity: "day", GroupBy: "source", Metric: "cost"})
	if err != nil {
		t.Fatal(err)
	}
	if string(ex.Total.Current) != "126.000000" {
		t.Fatalf("explorer total = %s, want 126.000000 (84 cloud + 42 plan)", ex.Total.Current)
	}
	got := map[string]string{}
	for _, g := range ex.Groups {
		got[g.Key] = string(g.Total)
	}
	if got[s.cloudSrc.ID] != "84.000000" || got[s.platSrc.ID] != "42.000000" {
		t.Fatalf("per-source totals = %v, want cloud 84 / platform 42", got)
	}
	if _, seen := got[s.internal.ID]; seen {
		t.Fatalf("the internal platform source appears in a customer-facing explorer: %v", got)
	}

	// k8s.vcpu on a platform book that prices no platform meter is "not sold
	// per use" — the allocation basis — never an unpriced SKU to add a rate for.
	if len(ex.Unpriced) != 0 {
		t.Fatalf("unpriced = %+v, want none", ex.Unpriced)
	}
	if len(ex.NotSoldPerUse) != 1 || ex.NotSoldPerUse[0].SKU != "k8s.vcpu" {
		t.Fatalf("not_sold_per_use = %+v, want the k8s.vcpu basis meter", ex.NotSoldPerUse)
	}

	// The statement: one draft, both sources' lines, subtotal 126.
	results, err := rating.Run(ctx, st, "2026-08", s.cust.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Error != "" {
		t.Fatalf("run = %+v", results)
	}
	if len(results[0].NotSoldPerUse) != 1 || results[0].NotSoldPerUse[0] != "k8s.vcpu" || len(results[0].UnpricedSKUs) != 0 {
		t.Fatalf("run detail = unpriced %v not-sold %v", results[0].UnpricedSKUs, results[0].NotSoldPerUse)
	}
	stmt, err := st.GetStatement(ctx, store.OperatorScope, results[0].StatementID)
	if err != nil {
		t.Fatal(err)
	}
	if string(stmt.Subtotal) != "126.000000" || stmt.Currency != "OMR" {
		t.Fatalf("statement = %s %s, want 126.000000 OMR", stmt.Subtotal, stmt.Currency)
	}
	lines := map[string]string{}
	for _, l := range stmt.Lines {
		if l.SourceID == nil {
			t.Fatalf("rated line without a source: %+v", l)
		}
		lines[l.SKU] = string(l.Amount)
	}
	if lines["ecs.m7n.xlarge.8"] != "84.000000" || lines["plan.m"] != "42.000000" || len(lines) != 2 {
		t.Fatalf("statement lines = %v", lines)
	}
	// Explorer ↔ statement reconciliation across both layers.
	if string(ex.Total.Current) != string(stmt.Subtotal) {
		t.Fatalf("explore %s ≠ statement subtotal %s", ex.Total.Current, stmt.Subtotal)
	}
}

// TestIntegrationMixedCurrencySourcesRefuseTheStatement: a second cloud
// source priced in USD makes the customer's statement ambiguous — a
// statement is issued in ONE currency and nothing converts money on a bill —
// so the run is refused with a message naming both currencies.
func TestIntegrationMixedCurrencySourcesRefuseTheStatement(t *testing.T) {
	st := testdb.Open(t)
	s := seedTwoLayer(t, st)
	ctx := context.Background()

	usd, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "USD list", Scope: store.LayerCloud, Currency: "USD", AnnualDivisor: 8760, BillStopped: "compute"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, usd.ID, []store.PriceItem{{SKU: "ecs.m7n.xlarge.8", Unit: "instance-hour", UnitPrice: "1"}}, true); err != nil {
		t.Fatal(err)
	}
	second, _, err := st.UpsertSource(ctx, s.cust.ID, store.SourceKindHuaweiProject, "me-east-216", "proj-acme-2")
	if err != nil {
		t.Fatal(err)
	}
	assignBook(t, st, second.ID, usd.ID)

	_, err = rating.Run(ctx, st, "2026-08", s.cust.ID)
	if !errors.Is(err, rating.ErrMixedCurrency) {
		t.Fatalf("run = %v, want ErrMixedCurrency", err)
	}
	for _, want := range []string{"OMR", "USD", "one currency"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("message %q must name %q", err.Error(), want)
		}
	}
	// An all-customer run does not fail: the affected customer carries the
	// message in its own result and every other customer is still rated.
	other, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "bravo", Name: "Bravo", AdminEmail: "b@bravo.example", StartDate: "2026-08-01"})
	if err != nil {
		t.Fatal(err)
	}
	src, _, err := st.UpsertSource(ctx, other.ID, store.SourceKindHuaweiProject, "me-east-215", "proj-bravo")
	if err != nil {
		t.Fatal(err)
	}
	assignBook(t, st, src.ID, s.cloudBook.ID)
	results, err := rating.Run(ctx, st, "2026-08", "")
	if err != nil {
		t.Fatal(err)
	}
	byCustomer := map[string]string{}
	for _, r := range results {
		byCustomer[r.CustomerID] = r.Error
	}
	if !strings.Contains(byCustomer[s.cust.ID], "one currency") {
		t.Fatalf("mixed-currency customer result = %q", byCustomer[s.cust.ID])
	}
	if byCustomer[other.ID] != "" {
		t.Fatalf("a healthy customer was refused too: %q", byCustomer[other.ID])
	}
}

// TestIntegrationCoverageIsPerSourceBook: the coverage of a book lists the
// SOURCES assigned to it and only the SKUs those sources used — a platform
// meter can never appear under a cloud book, which is what the founder's
// hw307 screenshot showed before this change.
func TestIntegrationCoverageIsPerSourceBook(t *testing.T) {
	st := testdb.Open(t)
	s := seedTwoLayer(t, st)
	ctx := context.Background()
	from, to := day(2026, 8, 1), day(2026, 8, 8)

	cloud, err := st.PriceBookCoverage(ctx, s.cloudBook.ID, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if cloud.Scope != store.LayerCloud {
		t.Fatalf("cloud coverage scope = %q", cloud.Scope)
	}
	if len(cloud.Sources) != 1 || cloud.Sources[0].SourceID != s.cloudSrc.ID || cloud.Sources[0].Layer != store.LayerCloud || cloud.Sources[0].Label != "proj-acme" {
		t.Fatalf("cloud coverage sources = %+v", cloud.Sources)
	}
	if len(cloud.Customers) != 1 || cloud.Customers[0].ID != s.cust.ID {
		t.Fatalf("cloud coverage customers = %+v", cloud.Customers)
	}
	if len(cloud.SKUsInUse) != 1 || cloud.SKUsInUse[0].SKU != "ecs.m7n.xlarge.8" || !cloud.SKUsInUse[0].Priced {
		t.Fatalf("cloud coverage SKUs = %+v (a platform meter must never appear here)", cloud.SKUsInUse)
	}
	if cloud.CoveragePct != 100 || cloud.UnpricedCount != 0 {
		t.Fatalf("cloud coverage = %v %% unpriced=%d", cloud.CoveragePct, cloud.UnpricedCount)
	}

	plan, err := st.PriceBookCoverage(ctx, s.planBook.ID, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Scope != store.LayerPlatform || len(plan.Sources) != 1 || plan.Sources[0].SourceID != s.platSrc.ID || plan.Sources[0].Layer != store.LayerPlatform {
		t.Fatalf("plan coverage sources = %+v scope=%q", plan.Sources, plan.Scope)
	}
	seen := map[string]store.CoverageSKU{}
	for _, k := range plan.SKUsInUse {
		seen[k.SKU] = k
	}
	if !seen["plan.m"].Priced || seen["k8s.vcpu"].Priced {
		t.Fatalf("plan coverage SKUs = %+v", plan.SKUsInUse)
	}
	// The basis meter is "not sold per use", not a hole in the book: it is
	// out of both sides of the percentage, and coverage reads 100 %.
	if !seen["k8s.vcpu"].NotSoldPerUse || plan.NotSoldCount != 1 || plan.UnpricedCount != 0 || plan.CoveragePct != 100 {
		t.Fatalf("plan coverage = %+v not-sold=%d unpriced=%d pct=%v", plan.SKUsInUse, plan.NotSoldCount, plan.UnpricedCount, plan.CoveragePct)
	}
}

// TestIntegrationInternalSourceIsInvisibleToCustomersButCountedByAllocation:
// the Sovereign's own footprint never reaches a customer-facing number —
// explorer, resources count, statements — and is exactly the
// platform-overhead row of the allocation report.
func TestIntegrationInternalSourceIsInvisibleToCustomersButCountedByAllocation(t *testing.T) {
	st := testdb.Open(t)
	s := seedTwoLayer(t, st)
	ctx := context.Background()
	from, to := day(2026, 8, 1), day(2026, 8, 8)

	// Operator-wide explorer: no internal row, no group for it.
	ex, err := st.Explore(ctx, store.OperatorScope, store.CostQuery{From: from, To: to, Granularity: "day", GroupBy: "tier", Metric: "cost"})
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range ex.Groups {
		if g.Key == store.OverheadTier {
			t.Fatalf("platform-overhead reached a customer-facing explorer: %+v", g)
		}
	}
	// The customer's own lens sees only its two sources' usage.
	own, err := st.Explore(ctx, store.CustomerScope(s.cust.ID), store.CostQuery{From: from, To: to, Granularity: "day", GroupBy: "none", Metric: "cost"})
	if err != nil {
		t.Fatal(err)
	}
	if string(own.Total.Current) != "126.000000" {
		t.Fatalf("customer explorer total = %s", own.Total.Current)
	}
	// The statement never carries it either.
	results, err := rating.Run(ctx, st, "2026-08", s.cust.ID)
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := st.GetStatement(ctx, store.OperatorScope, results[0].StatementID)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range stmt.Lines {
		if l.SourceID != nil && *l.SourceID == s.internal.ID {
			t.Fatalf("the internal source was billed to a customer: %+v", l)
		}
	}
	// Allocation, the one reader that opts in: the overhead row is the
	// internal source's 7 × 24 × 4 = 672 vCPU-hours, with no customer.
	res, err := st.Allocation(ctx, store.OperatorScope, from, to)
	if err != nil {
		t.Fatal(err)
	}
	o := allocRow(res, "", store.OverheadTier)
	if o == nil {
		t.Fatalf("no platform-overhead row: %+v", res.Rows)
	}
	if string(o.VCPUHours) != "672.000000" || o.CustomerName != store.OverheadName {
		t.Fatalf("overhead row = %+v", *o)
	}
	a := allocRow(res, s.cust.ID, "organization")
	if a == nil || string(a.VCPUHours) != "336.000000" {
		t.Fatalf("organization row = %+v", a)
	}
	if res.PlatformOverhead != 1 || res.OrganizationRows != 1 {
		t.Fatalf("rows = %d overhead / %d org", res.PlatformOverhead, res.OrganizationRows)
	}
	// The Organization's rated revenue is its own statement figure — the
	// two layers meet only here, in the report.
	if string(a.RatedRevenue) != "126.000000" {
		t.Fatalf("rated revenue = %s, want the 126.000000 the customer is billed", a.RatedRevenue)
	}
}

// TestIntegrationSourceBookScopeMustMatchLayer: a cloud book on a platform
// source (and the reverse) is refused with ErrInvalid naming both, and the
// internal source takes no book at all.
func TestIntegrationSourceBookScopeMustMatchLayer(t *testing.T) {
	st := testdb.Open(t)
	s := seedTwoLayer(t, st)
	ctx := context.Background()

	err := st.SetSourcePriceBook(ctx, s.platSrc.ID, s.cloudBook.ID)
	if !errors.Is(err, store.ErrInvalid) || !strings.Contains(err.Error(), "scope cloud does not match source layer platform") {
		t.Fatalf("cloud book on a platform source = %v", err)
	}
	err = st.SetSourcePriceBook(ctx, s.cloudSrc.ID, s.planBook.ID)
	if !errors.Is(err, store.ErrInvalid) || !strings.Contains(err.Error(), "scope platform does not match source layer cloud") {
		t.Fatalf("platform book on a cloud source = %v", err)
	}
	if err := st.SetSourcePriceBook(ctx, s.internal.ID, s.planBook.ID); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("the internal source accepted a book: %v", err)
	}
	// The refused assignments changed nothing.
	src, err := st.GetSource(ctx, store.OperatorScope, s.platSrc.ID)
	if err != nil || src.PriceBookID == nil || *src.PriceBookID != s.planBook.ID {
		t.Fatalf("platform source book after refusals = %+v err=%v", src, err)
	}
	// Clearing a book is legitimate; the SKUs then rate to zero.
	if err := st.SetSourcePriceBook(ctx, s.cloudSrc.ID, ""); err != nil {
		t.Fatal(err)
	}
	if src, _ := st.GetSource(ctx, store.OperatorScope, s.cloudSrc.ID); src.PriceBookID != nil {
		t.Fatalf("book not cleared: %+v", src)
	}
	ex, err := st.Explore(ctx, store.OperatorScope, store.CostQuery{From: day(2026, 8, 1), To: day(2026, 8, 8), Granularity: "day", GroupBy: "none", Metric: "cost"})
	if err != nil {
		t.Fatal(err)
	}
	if string(ex.Total.Current) != "42.000000" {
		t.Fatalf("total after clearing the cloud book = %s, want the plan line alone", ex.Total.Current)
	}
	if len(ex.Unpriced) != 1 || ex.Unpriced[0].SKU != "ecs.m7n.xlarge.8" {
		t.Fatalf("unpriced after clearing = %+v", ex.Unpriced)
	}
	// A book still assigned to a source cannot be deleted, and the refusal
	// names the source, not just the customer.
	assignBook(t, st, s.cloudSrc.ID, s.cloudBook.ID)
	assigned, err := st.DeletePriceBook(ctx, s.cloudBook.ID)
	if !errors.Is(err, store.ErrConflict) || len(assigned) != 1 || assigned[0].SourceID != s.cloudSrc.ID {
		t.Fatalf("delete assigned book = %v %+v", err, assigned)
	}
}
