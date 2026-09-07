package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// Multi-currency conversion against Postgres (#6867 follow-up, DESIGN.md
// §3.10): three customers on three books — OMR (the reporting currency),
// USD with a stored rate, EUR without one — and every reader of cost.

type fxSeed struct {
	omr, usd, eur store.Customer
}

// seedCurrencies writes, for 2026-09-01..07 (168 h), one ECS hour per hour
// per customer: OMR at 0.5/h → 84 OMR; USD at 0.5/h → 84 USD; EUR at
// 0.4/h → 67.2 EUR. The USD rate is 2.6 per OMR; EUR has none.
func seedCurrencies(t *testing.T, st *store.Store) fxSeed {
	t.Helper()
	ctx := context.Background()
	mkBook := func(name, currency, price string) string {
		b, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: name, Currency: currency, AnnualDivisor: 8760, BillStopped: "compute"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.PutPriceItems(ctx, b.ID, []store.PriceItem{{SKU: "ecs.m7n.xlarge.8", Unit: "instance-hour", UnitPrice: store.Decimal(price)}}, true); err != nil {
			t.Fatal(err)
		}
		return b.ID
	}
	mkCustomer := func(slug, book string) (store.Customer, store.CostSource) {
		c, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: slug, Name: slug, AdminEmail: slug + "@x.example", PriceBookID: book, StartDate: "2026-08-01"})
		if err != nil {
			t.Fatal(err)
		}
		src, _, err := st.UpsertSource(ctx, c.ID, "huawei-project", "me-east-1", "proj-"+slug)
		if err != nil {
			t.Fatal(err)
		}
		// The resources view joins the inventory: one live ECS per customer.
		if _, err := st.UpsertInventory(ctx, src.ID, []store.InventoryUpsert{{ResourceID: "vm-" + slug, Kind: "ecs", Name: "vm-" + slug,
			Attrs: map[string]any{"status": "ACTIVE", "flavor": "m7n.xlarge.8"}, Created: day(2026, 8, 20), SeenAt: day(2026, 9, 7).Add(23 * time.Hour)}}); err != nil {
			t.Fatal(err)
		}
		return c, src
	}
	omr, srcO := mkCustomer("omr-co", mkBook("omr-list", "OMR", "0.5"))
	usd, srcU := mkCustomer("usd-co", mkBook("usd-list", "USD", "0.5"))
	eur, srcE := mkCustomer("eur-co", mkBook("eur-list", "EUR", "0.4"))
	var recs []store.UsageRecord
	rec := func(c store.Customer, src store.CostSource, at time.Time) {
		lb, _ := json.Marshal(map[string]any{"name": "vm-" + c.Slug, "status": "ACTIVE"})
		recs = append(recs, store.UsageRecord{CustomerID: c.ID, SourceID: src.ID, ResourceID: "vm-" + c.Slug, ResourceKind: "ecs", SKU: "ecs.m7n.xlarge.8",
			Quantity: "1.000000", Unit: "instance-hour", WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-1", Labels: lb})
	}
	for d := 1; d <= 7; d++ {
		for h := 0; h < 24; h++ {
			at := day(2026, 9, d).Add(time.Duration(h) * time.Hour)
			rec(omr, srcO, at)
			rec(usd, srcU, at)
			rec(eur, srcE, at)
		}
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCurrencyRate(ctx, "usd", "2.6", ""); err != nil {
		t.Fatal(err)
	}
	return fxSeed{omr: omr, usd: usd, eur: eur}
}

// rat6 renders an exact rational at the schema's 6-decimal scale.
func rat6(r *big.Rat) store.Decimal { return store.Decimal(r.FloatString(6)) }

func rat(s string) *big.Rat {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		panic(s)
	}
	return r
}

func TestIntegrationExploreConvertsToReportingCurrency(t *testing.T) {
	st := testdb.Open(t)
	s := seedCurrencies(t, st)
	ctx := context.Background()
	win := store.CostQuery{From: day(2026, 9, 1), To: day(2026, 9, 8), Granularity: "day", GroupBy: "customer", Metric: "cost"}

	// Expected figures, exactly: 84 OMR + 84 USD / 2.6; EUR has no rate.
	usdInOMR := new(big.Rat).Quo(rat("84"), rat("2.6"))
	wantTotal := rat6(new(big.Rat).Add(rat("84"), usdInOMR))
	if wantTotal != "116.307692" {
		t.Fatalf("arithmetic check: %s", wantTotal)
	}

	res, err := st.Explore(ctx, store.OperatorScope, win)
	if err != nil {
		t.Fatal(err)
	}
	if res.Currency != "OMR" || !res.MixedCurrency {
		t.Fatalf("currency = %q mixed = %v (want OMR, mixed because EUR is unconverted)", res.Currency, res.MixedCurrency)
	}
	if res.Total.Current != wantTotal {
		t.Fatalf("total = %s, want %s (84 OMR + 84 USD ÷ 2.6; a build that multiplies says 302.4)", res.Total.Current, wantTotal)
	}
	if g := groupByKey(res, s.usd.ID); g == nil || g.Total != rat6(usdInOMR) {
		t.Fatalf("usd group = %+v, want %s", g, rat6(usdInOMR))
	}
	if g := groupByKey(res, s.omr.ID); g == nil || g.Total != "84.000000" {
		t.Fatalf("omr group = %+v", g)
	}
	// The EUR customer is a group (its usage exists) at 0: nothing of it is
	// in any total, and the unconverted list says what was left out.
	if g := groupByKey(res, s.eur.ID); g == nil || g.Total != "0.000000" {
		t.Fatalf("eur group = %+v", g)
	}
	if len(res.Unconverted) != 1 || res.Unconverted[0].Currency != "EUR" || res.Unconverted[0].Records != 168 || res.Unconverted[0].Cost != "67.200000" {
		t.Fatalf("unconverted = %+v, want EUR 168 records 67.200000", res.Unconverted)
	}
	// Day buckets convert too: one day of USD is 12 / 2.6, plus 12 OMR.
	wantDay := rat6(new(big.Rat).Add(rat("12"), new(big.Rat).Quo(rat("12"), rat("2.6"))))
	if res.TotalsByBucket[2] != wantDay {
		t.Fatalf("day-3 total = %s, want %s", res.TotalsByBucket[2], wantDay)
	}

	// Scope: the USD customer sees its own converted figure in OMR and
	// nothing unconverted; the EUR customer sees a 0 total and its own
	// unconverted usage — never another customer's.
	rU, err := st.Explore(ctx, store.CustomerScope(s.usd.ID), win)
	if err != nil {
		t.Fatal(err)
	}
	if rU.Total.Current != rat6(usdInOMR) || rU.Currency != "OMR" || rU.MixedCurrency || len(rU.Unconverted) != 0 {
		t.Fatalf("usd scope = total %s %s mixed %v unconverted %+v", rU.Total.Current, rU.Currency, rU.MixedCurrency, rU.Unconverted)
	}
	rE, err := st.Explore(ctx, store.CustomerScope(s.eur.ID), win)
	if err != nil {
		t.Fatal(err)
	}
	if rE.Total.Current != "0.000000" || !rE.MixedCurrency || len(rE.Unconverted) != 1 || rE.Unconverted[0].Currency != "EUR" || rE.Unconverted[0].Records != 168 {
		t.Fatalf("eur scope = total %s mixed %v unconverted %+v", rE.Total.Current, rE.MixedCurrency, rE.Unconverted)
	}

	// Adding the EUR rate (0.42 EUR per OMR: 67.2 EUR = 160 OMR) brings the
	// third customer into the total and clears the warning.
	if _, err := st.PutCurrencyRate(ctx, "EUR", "0.42", "manual"); err != nil {
		t.Fatal(err)
	}
	res, err = st.Explore(ctx, store.OperatorScope, win)
	if err != nil {
		t.Fatal(err)
	}
	wantAll := rat6(new(big.Rat).Add(rat(string(wantTotal)), rat("160")))
	if res.Total.Current != wantAll || res.MixedCurrency || len(res.Unconverted) != 0 {
		t.Fatalf("with EUR rate: total %s (want %s) mixed %v unconverted %+v", res.Total.Current, wantAll, res.MixedCurrency, res.Unconverted)
	}
	if g := groupByKey(res, s.eur.ID); g == nil || g.Total != "160.000000" {
		t.Fatalf("eur group = %+v", g)
	}

	// Every other reader of cost agrees with the explorer.
	rl, err := st.ListResources(ctx, store.OperatorScope, store.ResourceQuery{From: day(2026, 9, 1), To: day(2026, 9, 8)})
	if err != nil {
		t.Fatal(err)
	}
	if rl.SumCost != wantAll || rl.Currency != "OMR" || rl.MixedCurrency {
		t.Fatalf("resources sum = %s %s mixed %v, explorer %s", rl.SumCost, rl.Currency, rl.MixedCurrency, wantAll)
	}
	for _, r := range rl.Rows {
		if r.Unconverted || r.Currency != "OMR" {
			t.Fatalf("resource %s: unconverted=%v currency=%s", r.ResourceID, r.Unconverted, r.Currency)
		}
		if r.ResourceID == "vm-usd-co" && r.Cost != rat6(usdInOMR) {
			t.Fatalf("usd resource cost = %s, want %s", r.Cost, rat6(usdInOMR))
		}
	}
	daily, err := st.DailyCostByCustomerKind(ctx, store.OperatorScope, s.usd.ID, day(2026, 9, 1), day(2026, 9, 8))
	if err != nil {
		t.Fatal(err)
	}
	if len(daily) != 7 || daily[0].Cost != rat6(new(big.Rat).Quo(rat("12"), rat("2.6"))) {
		t.Fatalf("anomaly daily series = %+v", daily)
	}
	books, err := st.CustomerBooks(ctx, store.OperatorScope, "")
	if err != nil {
		t.Fatal(err)
	}
	rateOf := map[string]store.Decimal{}
	for _, b := range books {
		rateOf[b.Currency] = b.RateToBase
	}
	if rateOf["OMR"] != "1" || rateOf["USD"] != "2.6000000000" || rateOf["EUR"] != "0.4200000000" {
		t.Fatalf("book rates = %v", rateOf)
	}

	// Removing a rate makes its currency unconverted again — and a resource
	// priced in it reports cost 0 with the flag, the list mixed.
	if err := st.DeleteCurrencyRate(ctx, "eur"); err != nil {
		t.Fatal(err)
	}
	rl, err = st.ListResources(ctx, store.OperatorScope, store.ResourceQuery{From: day(2026, 9, 1), To: day(2026, 9, 8)})
	if err != nil {
		t.Fatal(err)
	}
	if rl.SumCost != wantTotal || !rl.MixedCurrency {
		t.Fatalf("resources without EUR rate: sum %s mixed %v", rl.SumCost, rl.MixedCurrency)
	}
	for _, r := range rl.Rows {
		if r.ResourceID == "vm-eur-co" && (!r.Unconverted || r.Cost != "0.000000") {
			t.Fatalf("eur resource = %+v", r)
		}
	}
	if err := st.DeleteCurrencyRate(ctx, "EUR"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
}

// The reporting currency's rate is 1 whatever the table says, and a
// currency without a rate is unconverted — including the FORMER reporting
// currency after the operator changes it.
func TestIntegrationReportingCurrencyChangeRebasesEveryRate(t *testing.T) {
	st := testdb.Open(t)
	s := seedCurrencies(t, st)
	ctx := context.Background()
	win := store.CostQuery{From: day(2026, 9, 1), To: day(2026, 9, 8), Granularity: "month", GroupBy: "customer", Metric: "cost"}

	settings, err := st.GetAllocationSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.Currency = "USD"
	if _, err := st.UpdateAllocationSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if cur, _ := st.ReportingCurrency(ctx); cur != "USD" {
		t.Fatalf("reporting currency = %q", cur)
	}
	res, err := st.Explore(ctx, store.OperatorScope, win)
	if err != nil {
		t.Fatal(err)
	}
	// USD is now 1 by definition (its stored 2.6 row is ignored); OMR has
	// no row and is unconverted; EUR still has none.
	if res.Currency != "USD" || res.Total.Current != "84.000000" {
		t.Fatalf("total = %s %s, want 84.000000 USD", res.Total.Current, res.Currency)
	}
	if len(res.Unconverted) != 2 || res.Unconverted[0].Currency != "EUR" || res.Unconverted[1].Currency != "OMR" || res.Unconverted[1].Cost != "84.000000" {
		t.Fatalf("unconverted = %+v", res.Unconverted)
	}
	if g := groupByKey(res, s.omr.ID); g == nil || g.Total != "0.000000" {
		t.Fatalf("omr group = %+v", g)
	}
	// A rate for the (new) reporting currency is refused; one for the old
	// reporting currency is now welcome.
	if _, err := st.PutCurrencyRate(ctx, "USD", "1", ""); !errors.Is(err, store.ErrReportingCurrency) || !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("rate for the reporting currency = %v", err)
	}
	if _, err := st.PutCurrencyRate(ctx, "OMR", "0.3846", ""); err != nil {
		t.Fatal(err)
	}
	res, err = st.Explore(ctx, store.OperatorScope, win)
	if err != nil {
		t.Fatal(err)
	}
	want := rat6(new(big.Rat).Add(rat("84"), new(big.Rat).Quo(rat("84"), rat("0.3846"))))
	if res.Total.Current != want || len(res.Unconverted) != 1 {
		t.Fatalf("total = %s (want %s) unconverted %+v", res.Total.Current, want, res.Unconverted)
	}
}

func TestIntegrationCurrencyRateValidationAndCRUD(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()

	if cur, err := st.ReportingCurrency(ctx); err != nil || cur != "OMR" {
		t.Fatalf("reporting = %q %v", cur, err)
	}
	if list, err := st.ListCurrencyRates(ctx); err != nil || len(list) != 0 {
		t.Fatalf("list = %v %v", list, err)
	}
	// Lower-case code and a numeric string are normalised; source defaults.
	r, err := st.PutCurrencyRate(ctx, " usd ", " 2.6 ", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Code != "USD" || r.PerBase != "2.6000000000" || r.Source != "manual" || r.UpdatedAt.IsZero() {
		t.Fatalf("put = %+v", r)
	}
	first := r.UpdatedAt
	time.Sleep(5 * time.Millisecond)
	// A second put replaces the rate and the source and moves updated_at.
	r, err = st.PutCurrencyRate(ctx, "USD", "2.5", "ecb")
	if err != nil {
		t.Fatal(err)
	}
	if r.PerBase != "2.5000000000" || r.Source != "ecb" || !r.UpdatedAt.After(first) {
		t.Fatalf("replaced = %+v (first %s)", r, first)
	}
	if got, err := st.GetCurrencyRate(ctx, "usd"); err != nil || got.PerBase != "2.5000000000" {
		t.Fatalf("get = %+v %v", got, err)
	}
	if _, err := st.GetCurrencyRate(ctx, "EUR"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get missing = %v", err)
	}
	if _, err := st.GetCurrencyRate(ctx, "not-a-code"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get malformed = %v", err)
	}
	if rate, err := st.RateToBase(ctx, "USD"); err != nil || rate != "2.5000000000" {
		t.Fatalf("RateToBase(USD) = %q %v", rate, err)
	}
	if rate, err := st.RateToBase(ctx, "omr"); err != nil || rate != "1" {
		t.Fatalf("RateToBase(reporting) = %q %v", rate, err)
	}
	if rate, err := st.RateToBase(ctx, "EUR"); err != nil || rate != "" {
		t.Fatalf("RateToBase(missing) = %q %v", rate, err)
	}

	bad := []struct {
		code, per string
		want      error
	}{
		{"US", "2.6", store.ErrInvalid},
		{"USDX", "2.6", store.ErrInvalid},
		{"U$D", "2.6", store.ErrInvalid},
		{"", "2.6", store.ErrInvalid},
		{"EUR", "0", store.ErrInvalid},
		{"EUR", "-1", store.ErrInvalid},
		{"EUR", "", store.ErrInvalid},
		{"EUR", "abc", store.ErrInvalid},
		{"EUR", "1e3", store.ErrInvalid},
		{"OMR", "1", store.ErrReportingCurrency},
		{"omr", "2", store.ErrReportingCurrency},
	}
	for _, c := range bad {
		if _, err := st.PutCurrencyRate(ctx, c.code, store.Decimal(c.per), ""); !errors.Is(err, c.want) {
			t.Fatalf("Put(%q, %q) = %v, want %v", c.code, c.per, err, c.want)
		}
	}
	if list, err := st.ListCurrencyRates(ctx); err != nil || len(list) != 1 || list[0].Code != "USD" {
		t.Fatalf("rejected writes were stored: %v %v", list, err)
	}
	if _, err := st.PutCurrencyRate(ctx, "AED", "9.55", ""); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.ListCurrencyRates(ctx); len(list) != 2 || list[0].Code != "AED" || list[1].Code != "USD" {
		t.Fatalf("list order = %v", list)
	}
	if err := st.DeleteCurrencyRate(ctx, "aed"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteCurrencyRate(ctx, "AED"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete twice = %v", err)
	}
	if err := st.DeleteCurrencyRate(ctx, "??"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete malformed = %v", err)
	}
	// The wipe between tests must cover the new table: the next Open finds
	// it empty (checked by the first assertion of this test on rerun).
}

// Allocation reads the explorer, so its pool and revenue are in the
// reporting currency and its unconverted list is the explorer's.
func TestIntegrationAllocationCarriesUnconverted(t *testing.T) {
	st := testdb.Open(t)
	seedCurrencies(t, st)
	ctx := context.Background()
	settings, err := st.GetAllocationSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.Pool, settings.ManualAmount = store.PoolManual, "100"
	if _, err := st.UpdateAllocationSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	// No platform meters were seeded, so there are no rows; the pool is
	// manual and the unconverted list is empty because no revenue query ran.
	res, err := st.Allocation(ctx, store.OperatorScope, day(2026, 9, 1), day(2026, 9, 8))
	if err != nil {
		t.Fatal(err)
	}
	if res.Pool.Currency != "OMR" || res.Pool.Amount != "100.000000" || res.Unconverted == nil {
		t.Fatalf("allocation = pool %+v unconverted %v", res.Pool, res.Unconverted)
	}
	if _, err := json.Marshal(res); err != nil {
		t.Fatal(err)
	}
}
