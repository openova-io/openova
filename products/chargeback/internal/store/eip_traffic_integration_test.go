package store_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// The observed-traffic window against Postgres (#6867).
//
// Two things are proven here that a unit test cannot: that the query runs at
// all, and that the SAMPLED measurement it reads stays out of every cost and
// usage aggregate — which is the whole reason the traffic metric is a
// separate SKU from the traffic meter.

func trafficSeed(t *testing.T, st *store.Store) (store.Customer, store.CostSource) {
	t.Helper()
	ctx := context.Background()
	book, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "list", Currency: "OMR", AnnualDivisor: 8760, BillStopped: "compute"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, book.ID, []store.PriceItem{
		{SKU: "eip", Unit: "hour", UnitPrice: "0.02"},
		{SKU: "eip.bandwidth_mbps", Unit: "mbps-hour", UnitPrice: "0.005"},
	}, true); err != nil {
		t.Fatal(err)
	}
	c, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "traf", Name: "Traffic Co", AdminEmail: "t@traf.example"})
	if err != nil {
		t.Fatal(err)
	}
	src, _, err := st.UpsertSource(ctx, c.ID, "huawei-project", "me-east-215", "proj-t")
	if err != nil {
		t.Fatal(err)
	}
	assignBook(t, st, src.ID, book.ID)

	seen := day(2026, 9, 3).Add(23 * time.Hour)
	if _, err := st.UpsertInventory(ctx, src.ID, []store.InventoryUpsert{
		{ResourceID: "eip-fat", Kind: "eip", Name: "10.0.0.1", SeenAt: seen, Attrs: map[string]any{
			"status": "ACTIVE", "bandwidth_mbps": 300, "bandwidth_id": "bw-fat",
			"bandwidth_charge_mode": "bandwidth", "bandwidth_share_type": "PER"}},
		{ResourceID: "bw-shared", Kind: "bandwidth", Name: "catalyst-shared-bw", SeenAt: seen, Attrs: map[string]any{
			"status": "NORMAL", "bandwidth_mbps": 300, "bandwidth_id": "bw-shared",
			"bandwidth_charge_mode": "bandwidth", "bandwidth_share_type": "WHOLE"}},
	}); err != nil {
		t.Fatal(err)
	}

	var recs []store.UsageRecord
	rec := func(res, kind, sku, unit string, qty float64, at time.Time) {
		recs = append(recs, store.UsageRecord{CustomerID: c.ID, SourceID: src.ID, ResourceID: res, ResourceKind: kind, SKU: sku,
			Quantity: store.Decimal(strconv.FormatFloat(qty, 'f', 6, 64)), Unit: unit, WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-215"})
	}
	// Three days: the reservation is billed every hour, and the observed
	// traffic rides alongside it — 0.5 GB an hour, with one busy hour of 4.
	for d := 1; d <= 3; d++ {
		for h := 0; h < 24; h++ {
			at := day(2026, 9, d).Add(time.Duration(h) * time.Hour)
			rec("eip-fat", "eip", "eip", "hour", 1, at)
			rec("eip-fat", "eip", "eip.bandwidth_mbps", "mbps-hour", 300, at)
			gb := 0.5
			if d == 2 && h == 13 {
				gb = 4
			}
			rec("eip-fat", "eip", store.SKUEIPTrafficObserved, "gb-hour-out", gb, at)
			rec("bw-shared", "bandwidth", store.SKUEIPTrafficObserved, "gb-hour-out", 1, at)
		}
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	return c, src
}

func TestIntegrationEIPTrafficWindowsAggregatesHoursTotalAndPeak(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	c, src := trafficSeed(t, st)

	got, err := st.EIPTrafficWindows(ctx, store.OperatorScope, "", day(2026, 9, 1), day(2026, 9, 4))
	if err != nil {
		t.Fatal(err)
	}
	byRes := map[string]store.EIPTrafficWindow{}
	for _, w := range got {
		byRes[w.ResourceID] = w
	}
	fat, ok := byRes["eip-fat"]
	if !ok {
		t.Fatalf("no window for eip-fat: %+v", got)
	}
	// 72 hours, 71 × 0.5 + 1 × 4 = 39.5 GB, busiest hour 4 GB.
	if fat.Hours != 72 || fat.TotalGB != 39.5 || fat.PeakGB != 4 {
		t.Fatalf("eip-fat window = %+v, want 72 h / 39.5 GB / peak 4", fat)
	}
	if fat.Kind != "eip" || fat.CustomerID != c.ID || fat.SourceID != src.ID {
		t.Fatalf("eip-fat identity = %+v", fat)
	}
	if pipe := byRes["bw-shared"]; pipe.Hours != 72 || pipe.TotalGB != 72 || pipe.Kind != "bandwidth" {
		t.Fatalf("shared pipe window = %+v", pipe)
	}
	// A window that ends before the samples returns nothing, not an error
	// and not the whole history.
	before, err := st.EIPTrafficWindows(ctx, store.OperatorScope, "", day(2026, 8, 1), day(2026, 8, 20))
	if err != nil || len(before) != 0 {
		t.Fatalf("window before the samples = %+v (%v)", before, err)
	}
	// Scope holds: another customer's scope sees none of it.
	other, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "other", Name: "Other", AdminEmail: "o@other.example"})
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := st.EIPTrafficWindows(ctx, store.CustomerScope(other.ID), other.ID, day(2026, 9, 1), day(2026, 9, 4))
	if err != nil || len(scoped) != 0 {
		t.Fatalf("another customer saw %d traffic windows (%v)", len(scoped), err)
	}
}

// The observed-traffic metric must be invisible to money. It is not rated,
// it is not "unpriced usage" the operator is nagged about, and it is not in
// the explorer's usage totals — exactly like the CPU sample.
func TestIntegrationObservedTrafficIsAMetricNotAMeter(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	c, src := trafficSeed(t, st)
	from, to := day(2026, 9, 1), day(2026, 9, 4)

	rows, err := st.UsageForRating(ctx, c.ID, from, to)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.SKU == store.SKUEIPTrafficObserved || r.SKU == store.SKUCPUUtil {
			t.Fatalf("the rating run sees the metric %q: it would report it as unpriced revenue", r.SKU)
		}
	}
	// The two real meters ARE there, so the exclusion is not just an empty
	// result set.
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.SKU] = true
	}
	if !seen["eip"] || !seen["eip.bandwidth_mbps"] {
		t.Fatalf("the reservation meters are missing from rating: %+v", rows)
	}

	unpriced, err := st.UnpricedUsageByCustomer(ctx, store.OperatorScope, "", from, to)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range unpriced {
		if u.SKU == store.SKUEIPTrafficObserved || u.SKU == store.SKUCPUUtil {
			t.Fatalf("the operator is told to price the metric %q", u.SKU)
		}
	}

	res, err := st.Explore(ctx, store.OperatorScope, store.CostQuery{From: from, To: to, Granularity: "day", GroupBy: "sku", Metric: "usage"})
	if err != nil {
		t.Fatal(err)
	}
	skus := map[string]bool{}
	for _, g := range res.Groups {
		skus[g.Key] = true
		if g.Key == store.SKUEIPTrafficObserved || g.Key == store.SKUCPUUtil {
			t.Fatalf("the explorer totals the metric %q as usage", g.Key)
		}
	}
	if !skus["eip.bandwidth_mbps"] {
		t.Fatalf("the explorer returned no meter groups at all, so the exclusion proves nothing: %+v", res.Groups)
	}
	// A boundary recompute deletes the METERS of the affected hours and
	// leaves the sampled measurement alone — re-fetching a sample would be
	// a second monitoring call the collector should not have to make.
	deleted, err := st.DeleteUsageInRange(ctx, src.ID, "eip-fat", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 144 {
		t.Fatalf("deleted %d rows, want the 144 meter rows (72 h × 2 SKUs)", deleted)
	}
	after, err := st.EIPTrafficWindows(ctx, store.OperatorScope, "", from, to)
	if err != nil {
		t.Fatal(err)
	}
	byRes := map[string]store.EIPTrafficWindow{}
	for _, w := range after {
		byRes[w.ResourceID] = w
	}
	if got := byRes["eip-fat"]; got.Hours != 72 {
		t.Fatalf("the recompute took the traffic samples with it: %+v", got)
	}
}
