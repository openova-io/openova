package store_test

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// The cost engine (#6867). Seeds a two-customer ledger with a priced and an
// unpriced SKU, a stopped instance, and records across a window boundary,
// then pins every property the explorer promises.

func day(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC) }

type seeded struct {
	a, b   store.Customer
	srcA   store.CostSource
	srcA2  store.CostSource // second source of A (platform)
	srcB   store.CostSource
	bookID string
}

// seedLedger writes, for customer A, hourly ECS + EVS records on 2026-09-01..07
// (7 days), one stopped ECS day, an unpriced k8s SKU on a second source, and
// for customer B one ECS record per day in the same window. The previous
// window (2026-08-25..31) gets half the A volume.
func seedLedger(t *testing.T, st *store.Store) seeded {
	t.Helper()
	ctx := context.Background()
	book, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "list", Currency: "OMR", AnnualDivisor: 8760, BillStopped: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, book.ID, []store.PriceItem{
		{SKU: "ecs.m7n.xlarge.8", Unit: "instance-hour", UnitPrice: "0.5"},
		{SKU: "evs.ssd.gb", Unit: "gb-hour", UnitPrice: "0.001"},
		{SKU: "eip", Unit: "hour", UnitPrice: "0.02"},
	}, true); err != nil {
		t.Fatal(err)
	}
	a, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "acme", Name: "Acme", AdminEmail: "a@acme.example", PriceBookID: book.ID, StartDate: "2026-08-01"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "bravo", Name: "Bravo", AdminEmail: "b@bravo.example", PriceBookID: book.ID, StartDate: "2026-08-01"})
	if err != nil {
		t.Fatal(err)
	}
	srcA, _, err := st.UpsertSource(ctx, a.ID, "huawei-project", "me-east-1", "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	srcA2, _, err := st.UpsertSource(ctx, a.ID, "openova-org", "", "acme")
	if err != nil {
		t.Fatal(err)
	}
	srcB, _, err := st.UpsertSource(ctx, b.ID, "huawei-project", "me-east-1", "proj-b")
	if err != nil {
		t.Fatal(err)
	}
	var recs []store.UsageRecord
	rec := func(c store.Customer, src store.CostSource, res, kind, sku, unit string, qty float64, at time.Time, labels map[string]any) {
		lb, _ := json.Marshal(labels)
		recs = append(recs, store.UsageRecord{CustomerID: c.ID, SourceID: src.ID, ResourceID: res, ResourceKind: kind, SKU: sku,
			Quantity: store.Decimal(strconv.FormatFloat(qty, 'f', 6, 64)), Unit: unit, WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-1", Labels: lb})
	}
	// Current window: 7 days × 24 h for A: one ECS (running), one EVS 100 GB,
	// and on day 3 a second ECS that is SHUTOFF all day (policy none → 0).
	for d := 1; d <= 7; d++ {
		for h := 0; h < 24; h++ {
			at := day(2026, 9, d).Add(time.Duration(h) * time.Hour)
			// Tags as the collectors write them (labels.tags is a map; the
			// enterprise project a plain label): vm-1 team=a env=prod in
			// ep-1, vol-1 team=b, vm-2 untagged, the pod tagged app=wordpress.
			rec(a, srcA, "vm-1", "ecs", "ecs.m7n.xlarge.8", "instance-hour", 1, at, map[string]any{"name": "web-1", "status": "ACTIVE", "tags": map[string]string{"team": "a", "env": "prod"}, "enterprise_project": "ep-1"})
			rec(a, srcA, "vol-1", "evs", "evs.ssd.gb", "gb-hour", 100, at, map[string]any{"name": "vol-1", "server_status": "ACTIVE", "tags": map[string]string{"team": "b"}})
			if d == 3 {
				rec(a, srcA, "vm-2", "ecs", "ecs.m7n.xlarge.8", "instance-hour", 1, at, map[string]any{"name": "batch-2", "status": "SHUTOFF"})
			}
			// Platform usage on the second source: unpriced.
			rec(a, srcA2, "ns/pod-1", "k8s-pod", "k8s.vcpu", "vcpu-hour", 0.5, at, map[string]any{"name": "pod-1", "namespace": "ns", "tier": "organization", "tags": map[string]string{"app": "wordpress"}})
			// The metric sample must never count.
			rec(a, srcA, "vm-1", "ecs", "ecs.cpu_util", "pct-hour-avg", 42, at, map[string]any{"name": "web-1"})
			// Customer B: one EIP hour per hour, tagged team=zulu.
			rec(b, srcB, "eip-1", "eip", "eip", "hour", 1, at, map[string]any{"name": "1.2.3.4", "tags": map[string]string{"team": "zulu"}})
		}
	}
	// Previous window (Aug 25..31): A runs the ECS 12 h a day only.
	for d := 25; d <= 31; d++ {
		for h := 0; h < 12; h++ {
			at := day(2026, 8, d).Add(time.Duration(h) * time.Hour)
			rec(a, srcA, "vm-1", "ecs", "ecs.m7n.xlarge.8", "instance-hour", 1, at, map[string]any{"name": "web-1", "status": "ACTIVE", "tags": map[string]string{"team": "a", "env": "prod"}, "enterprise_project": "ep-1"})
		}
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	return seeded{a: a, b: b, srcA: srcA, srcA2: srcA2, srcB: srcB, bookID: book.ID}
}

func f(d store.Decimal) float64 {
	v, _ := strconv.ParseFloat(string(d), 64)
	return v
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func groupByKey(r store.ExploreResult, key string) *store.CostGroup {
	for i := range r.Groups {
		if r.Groups[i].Key == key {
			return &r.Groups[i]
		}
	}
	return nil
}

func TestIntegrationExploreGroupsFiltersAndPrevious(t *testing.T) {
	st := testdb.Open(t)
	s := seedLedger(t, st)
	ctx := context.Background()
	win := store.CostQuery{From: day(2026, 9, 1), To: day(2026, 9, 8), Granularity: "day", GroupBy: "kind", Metric: "cost"}

	res, err := st.Explore(ctx, store.OperatorScope, win)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Buckets) != 7 || res.Buckets[0] != "2026-09-01" || res.Buckets[6] != "2026-09-07" {
		t.Fatalf("buckets = %v", res.Buckets)
	}
	// ECS: vm-1 7×24 h × 0.5 = 84; vm-2 stopped under policy none → 0.
	ecs := groupByKey(res, "ecs")
	if ecs == nil || !near(f(ecs.Total), 84) {
		t.Fatalf("ecs = %+v (want 84)", ecs)
	}
	if ecs.Label != "Elastic Cloud Server" {
		t.Fatalf("kind label = %q", ecs.Label)
	}
	// Previous window: 7 × 12 × 0.5 = 42 → +100 %.
	if !near(f(ecs.Previous), 42) || ecs.DeltaPct == nil || !near(*ecs.DeltaPct, 100) {
		t.Fatalf("ecs previous/delta = %v / %v", ecs.Previous, ecs.DeltaPct)
	}
	// EVS: 7×24×100 GB × 0.001 = 16.8; EIP (customer B): 168 × 0.02 = 3.36.
	if g := groupByKey(res, "evs"); g == nil || !near(f(g.Total), 16.8) {
		t.Fatalf("evs = %+v", g)
	}
	if g := groupByKey(res, "eip"); g == nil || !near(f(g.Total), 3.36) {
		t.Fatalf("eip = %+v", g)
	}
	// The unpriced platform SKU appears as a group with zero cost and as an
	// unpriced line with its quantity — it is never silently dropped.
	if g := groupByKey(res, "k8s-pod"); g == nil || !near(f(g.Total), 0) {
		t.Fatalf("k8s-pod = %+v", g)
	}
	if len(res.Unpriced) != 1 || res.Unpriced[0].SKU != "k8s.vcpu" || !near(f(res.Unpriced[0].Quantity), 84) {
		t.Fatalf("unpriced = %+v", res.Unpriced)
	}
	// Total = 84 + 16.8 + 3.36 = 104.16; the cpu_util samples add nothing.
	if !near(f(res.Total.Current), 104.16) {
		t.Fatalf("total = %v", res.Total.Current)
	}
	if res.Currency != "OMR" || res.MixedCurrency {
		t.Fatalf("currency = %q mixed=%v", res.Currency, res.MixedCurrency)
	}
	// Day 3 total = ECS 12 + EVS 2.4 + EIP 0.48 = 14.88; the stopped vm-2 adds 0.
	if !near(f(res.TotalsByBucket[2]), 14.88) {
		t.Fatalf("day-3 total = %v", res.TotalsByBucket[2])
	}
	// Ordered by total desc, shares sum to 1.
	if res.Groups[0].Key != "ecs" {
		t.Fatalf("first group = %s", res.Groups[0].Key)
	}
	share := 0.0
	for _, g := range res.Groups {
		share += g.Share
	}
	if !near(share, 1) {
		t.Fatalf("shares sum to %v", share)
	}

	// Include filter narrows; exclude filter removes.
	only := win
	only.Include = map[string][]string{"kind": {"ecs"}}
	r2, err := st.Explore(ctx, store.OperatorScope, only)
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.Groups) != 1 || !near(f(r2.Total.Current), 84) {
		t.Fatalf("include ecs → %+v", r2.Groups)
	}
	without := win
	without.Exclude = map[string][]string{"customer": {s.b.ID}}
	r3, err := st.Explore(ctx, store.OperatorScope, without)
	if err != nil {
		t.Fatal(err)
	}
	if groupByKey(r3, "eip") != nil || !near(f(r3.Total.Current), 100.8) {
		t.Fatalf("exclude B → eip=%v total=%v", groupByKey(r3, "eip"), r3.Total.Current)
	}

	// Top-N folds the tail into Other and Other carries the remainder exactly.
	topped := win
	topped.Limit = 1
	r4, err := st.Explore(ctx, store.OperatorScope, topped)
	if err != nil {
		t.Fatal(err)
	}
	if len(r4.Groups) != 1 || r4.Other == nil || !near(f(r4.Other.Total), 16.8+3.36) {
		t.Fatalf("top-1 → groups=%d other=%+v", len(r4.Groups), r4.Other)
	}
	if !near(f(r4.Groups[0].Total)+f(r4.Other.Total), f(r4.Total.Current)) {
		t.Fatal("top-N groups + Other must equal the total")
	}

	// Month grain gives one bucket for September and the same total.
	monthly := win
	monthly.Granularity = "month"
	r5, err := st.Explore(ctx, store.OperatorScope, monthly)
	if err != nil {
		t.Fatal(err)
	}
	if len(r5.Buckets) != 1 || r5.Buckets[0] != "2026-09" || !near(f(r5.Total.Current), 104.16) {
		t.Fatalf("monthly = %v / %v", r5.Buckets, r5.Total.Current)
	}

	// Usage metric by SKU reports quantities.
	usage := win
	usage.GroupBy, usage.Metric = "sku", "usage"
	r6, err := st.Explore(ctx, store.OperatorScope, usage)
	if err != nil {
		t.Fatal(err)
	}
	if g := groupByKey(r6, "evs.ssd.gb"); g == nil || !near(f(g.Total), 16800) {
		t.Fatalf("usage evs = %+v", g)
	}
	// Grouping by resource labels with the collector's name.
	byRes := win
	byRes.GroupBy = "resource"
	r7, err := st.Explore(ctx, store.OperatorScope, byRes)
	if err != nil {
		t.Fatal(err)
	}
	if g := groupByKey(r7, "vm-1"); g == nil || g.Label != "web-1" {
		t.Fatalf("resource label = %+v", g)
	}
}

func TestIntegrationExploreScopeCannotLeak(t *testing.T) {
	st := testdb.Open(t)
	s := seedLedger(t, st)
	ctx := context.Background()
	win := store.CostQuery{From: day(2026, 9, 1), To: day(2026, 9, 8), GroupBy: "customer"}

	// Customer B asks for everything, and even names A explicitly: only B.
	win.Include = map[string][]string{"customer": {s.a.ID}}
	res, err := st.Explore(ctx, store.CustomerScope(s.b.ID), win)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Groups) != 0 || !near(f(res.Total.Current), 0) {
		t.Fatalf("B naming A must see nothing of A: %+v", res.Groups)
	}
	win.Include = nil
	res, err = st.Explore(ctx, store.CustomerScope(s.b.ID), win)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Groups) != 1 || res.Groups[0].Key != s.b.ID || !near(f(res.Total.Current), 3.36) {
		t.Fatalf("B sees %+v", res.Groups)
	}
	// An empty customer scope is a bug upstream, not a wildcard.
	if _, err := st.Explore(ctx, store.Scope{}, win); err == nil {
		t.Fatal("empty scope must not read the ledger")
	}
	// Dimension values are scoped the same way.
	vals, err := st.DimensionValues(ctx, store.CustomerScope(s.b.ID), win)
	if err != nil {
		t.Fatal(err)
	}
	if len(vals["customer"]) != 1 || vals["customer"][0].Key != s.b.ID || len(vals["kind"]) != 1 || vals["kind"][0].Label != "Elastic IP" {
		t.Fatalf("dimension values for B = %+v", vals)
	}
}

// The explorer must agree with the bill: for a customer and a month, the
// explore total equals the statement subtotal the rating run writes.
func TestIntegrationExploreReconcilesWithStatement(t *testing.T) {
	st := testdb.Open(t)
	s := seedLedger(t, st)
	ctx := context.Background()
	results, err := rating.Run(ctx, st, "2026-09", s.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Error != "" {
		t.Fatalf("run = %+v", results)
	}
	stmt, err := st.GetStatement(ctx, store.OperatorScope, results[0].StatementID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := st.Explore(ctx, store.OperatorScope, store.CostQuery{From: day(2026, 9, 1), To: day(2026, 10, 1), Granularity: "month", CustomerID: s.a.ID})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Total.Current) != string(stmt.Subtotal) {
		t.Fatalf("explore %s ≠ statement subtotal %s", res.Total.Current, stmt.Subtotal)
	}
	// And the statement's unpriced SKU list matches the explorer's.
	if len(results[0].UnpricedSKUs) != 1 || results[0].UnpricedSKUs[0] != res.Unpriced[0].SKU {
		t.Fatalf("unpriced: run=%v explore=%v", results[0].UnpricedSKUs, res.Unpriced)
	}
}

func TestIntegrationExploreCountsAndLastCollected(t *testing.T) {
	st := testdb.Open(t)
	s := seedLedger(t, st)
	ctx := context.Background()
	now := day(2026, 9, 7)
	if _, err := st.UpsertInventory(ctx, s.srcA.ID, []store.InventoryUpsert{
		{ResourceID: "vm-1", Kind: "ecs", Name: "web-1", SeenAt: now},
		{ResourceID: "vm-2", Kind: "ecs", Name: "batch-2", SeenAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkInventoryDeleted(ctx, s.srcA.ID, []string{"ecs"}, []string{"vm-1"}, now); err != nil {
		t.Fatal(err)
	}
	n, err := st.LiveResourceCount(ctx, store.OperatorScope, "")
	if err != nil || n != 1 {
		t.Fatalf("live = %d err=%v", n, err)
	}
	if n, _ := st.LiveResourceCount(ctx, store.CustomerScope(s.b.ID), s.a.ID); n != 0 {
		t.Fatalf("B asking for A's count got %d", n)
	}
	if err := st.SetSourceCollected(ctx, s.srcB.ID, now); err != nil {
		t.Fatal(err)
	}
	lc, err := st.LastCollectedAt(ctx, store.OperatorScope, "")
	if err != nil || lc == nil || !lc.Equal(now) {
		t.Fatalf("last collected = %v err=%v", lc, err)
	}
	if lc, _ := st.LastCollectedAt(ctx, store.CustomerScope(s.a.ID), ""); lc != nil {
		t.Fatalf("A has no collected source yet, got %v", lc)
	}
}

// Hourly grain: one seeded day is 24 buckets whose sum is the day total, and
// the automatic compare window is the day before.
func TestIntegrationExploreHourlyBuckets(t *testing.T) {
	st := testdb.Open(t)
	seedLedger(t, st)
	ctx := context.Background()
	res, err := st.Explore(ctx, store.OperatorScope, store.CostQuery{From: day(2026, 9, 3), To: day(2026, 9, 4), Granularity: "hour", GroupBy: "kind"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Buckets) != 24 || res.Buckets[0] != "2026-09-03T00" || res.Buckets[23] != "2026-09-03T23" {
		t.Fatalf("hour buckets = %v", res.Buckets)
	}
	if res.Granularity != "hour" || len(res.TotalsByBucket) != 24 || len(res.BucketHasData) != 24 {
		t.Fatalf("shape = %+v", res)
	}
	// Every hour of day 3: ECS 0.5 + EVS 0.1 + EIP 0.02 = 0.62 (the stopped
	// vm-2 adds 0 under policy none); 24 × 0.62 = 14.88, the day total the
	// daily explore reports for the same date.
	sum := 0.0
	for i, v := range res.TotalsByBucket {
		if !near(f(v), 0.62) {
			t.Fatalf("hour %d total = %v, want 0.62", i, v)
		}
		if !res.BucketHasData[i] {
			t.Fatalf("hour %d marked without data", i)
		}
		sum += f(v)
	}
	if !near(sum, 14.88) || !near(f(res.Total.Current), 14.88) {
		t.Fatalf("hour sum %v / total %v, want 14.88", sum, res.Total.Current)
	}
	daily, err := st.Explore(ctx, store.OperatorScope, store.CostQuery{From: day(2026, 9, 3), To: day(2026, 9, 4), Granularity: "day", GroupBy: "kind"})
	if err != nil {
		t.Fatal(err)
	}
	if string(daily.Total.Current) != string(res.Total.Current) {
		t.Fatalf("hourly total %s ≠ daily total %s for the same day", res.Total.Current, daily.Total.Current)
	}
	// Per-group hour values sum to the group's total.
	ecs := groupByKey(res, "ecs")
	if ecs == nil || len(ecs.Values) != 24 || !near(f(ecs.Values[5]), 0.5) || !near(f(ecs.Total), 12) {
		t.Fatalf("ecs hourly = %+v", ecs)
	}
	// The automatic compare window is the day before, reported as such.
	if res.Compare != (store.CompareWindow{From: "2026-09-02", To: "2026-09-03", Label: store.CompareLabelPrevious}) {
		t.Fatalf("compare = %+v", res.Compare)
	}
	if !near(f(res.Total.Previous), 14.88) || res.Total.DeltaPct == nil || !near(*res.Total.DeltaPct, 0) {
		t.Fatalf("previous day = %v Δ%v", res.Total.Previous, res.Total.DeltaPct)
	}
	// A window with no rows at all still enumerates every hour, flagged empty.
	empty, err := st.Explore(ctx, store.OperatorScope, store.CostQuery{From: day(2026, 7, 1), To: day(2026, 7, 2), Granularity: "hour"})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Buckets) != 24 || empty.BucketHasData[0] || !near(f(empty.Total.Current), 0) {
		t.Fatalf("empty hourly window = %d buckets has_data[0]=%v total=%v", len(empty.Buckets), empty.BucketHasData[0], empty.Total.Current)
	}
}

// A custom compare window replaces the automatic previous period for every
// `previous` and delta in the document, and may be any length.
func TestIntegrationExploreCustomCompareWindow(t *testing.T) {
	st := testdb.Open(t)
	s := seedLedger(t, st)
	ctx := context.Background()
	win := store.CostQuery{From: day(2026, 9, 1), To: day(2026, 9, 8), Granularity: "day", GroupBy: "kind"}

	// Naming the automatic window explicitly gives the same numbers, labelled custom.
	same := win
	same.CompareFrom, same.CompareTo = day(2026, 8, 25), day(2026, 9, 1)
	r1, err := st.Explore(ctx, store.OperatorScope, same)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Compare != (store.CompareWindow{From: "2026-08-25", To: "2026-09-01", Label: store.CompareLabelCustom}) {
		t.Fatalf("compare = %+v", r1.Compare)
	}
	ecs := groupByKey(r1, "ecs")
	if ecs == nil || !near(f(ecs.Previous), 42) || ecs.DeltaPct == nil || !near(*ecs.DeltaPct, 100) {
		t.Fatalf("ecs vs explicit previous week = %+v", ecs)
	}
	if !near(f(r1.Total.Previous), 42) {
		t.Fatalf("total previous = %v", r1.Total.Previous)
	}

	// A shorter compare window (3 days × 12 h × 0.5 = 18) is reported as-is:
	// previous 18, delta (84 − 18) / 18 = +366.67 %.
	short := win
	short.CompareFrom, short.CompareTo = day(2026, 8, 25), day(2026, 8, 28)
	r2, err := st.Explore(ctx, store.OperatorScope, short)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Compare.From != "2026-08-25" || r2.Compare.To != "2026-08-28" || r2.Compare.Label != store.CompareLabelCustom {
		t.Fatalf("compare = %+v", r2.Compare)
	}
	ecs = groupByKey(r2, "ecs")
	if ecs == nil || !near(f(ecs.Previous), 18) || ecs.DeltaPct == nil || !near(*ecs.DeltaPct, (84-18)/18.0*100) {
		t.Fatalf("ecs vs 3-day window = %+v", ecs)
	}
	if !near(f(r2.Total.Previous), 18) || !near(f(r2.Total.Current), 104.16) {
		t.Fatalf("totals = %+v", r2.Total)
	}
	// Groups absent from the compare window keep previous 0 and a nil delta.
	if g := groupByKey(r2, "eip"); g == nil || !near(f(g.Previous), 0) || g.DeltaPct != nil {
		t.Fatalf("eip vs August = %+v", g)
	}

	// A compare window overlapping the current one (its first 3 days) is
	// legal: the ECS ran 24 h then, so previous = 3 × 24 × 0.5 = 36.
	overlap := win
	overlap.CompareFrom, overlap.CompareTo = day(2026, 9, 1), day(2026, 9, 4)
	r3, err := st.Explore(ctx, store.OperatorScope, overlap)
	if err != nil {
		t.Fatal(err)
	}
	if g := groupByKey(r3, "ecs"); g == nil || !near(f(g.Previous), 36) {
		t.Fatalf("ecs vs overlapping window = %+v", g)
	}
	// The current-window numbers never move with the compare window.
	if string(r3.Total.Current) != string(r1.Total.Current) || string(r3.TotalsByBucket[2]) != string(r1.TotalsByBucket[2]) {
		t.Fatalf("current changed with compare window: %v vs %v", r3.Total, r1.Total)
	}
	// Top-N Other folds `previous` over the compare window too.
	topped := short
	topped.Limit = 1
	r4, err := st.Explore(ctx, store.OperatorScope, topped)
	if err != nil {
		t.Fatal(err)
	}
	if r4.Other == nil || !near(f(r4.Groups[0].Previous)+f(r4.Other.Previous), f(r4.Total.Previous)) {
		t.Fatalf("top-1 previous: %+v + %+v ≠ %v", r4.Groups[0].Previous, r4.Other, r4.Total.Previous)
	}

	// Half a window, or an empty one, is refused before any query runs.
	half := win
	half.CompareFrom = day(2026, 8, 25)
	if _, err := st.Explore(ctx, store.OperatorScope, half); err == nil {
		t.Fatal("compare_from without compare_to must be refused")
	}
	rev := win
	rev.CompareFrom, rev.CompareTo = day(2026, 8, 28), day(2026, 8, 25)
	if _, err := st.Explore(ctx, store.OperatorScope, rev); err == nil {
		t.Fatal("reversed compare window must be refused")
	}
	// The customer scope applies to the compare window as it does to the
	// current one: B's compare numbers never include A.
	scoped := short
	rB, err := st.Explore(ctx, store.CustomerScope(s.b.ID), scoped)
	if err != nil {
		t.Fatal(err)
	}
	if !near(f(rB.Total.Previous), 0) || groupByKey(rB, "ecs") != nil {
		t.Fatalf("B's compare window leaked A: %+v", rB)
	}
}

// The tag dimension (EPIC #6867 follow-up): `tag:<key>` groups and filters
// like any other dimension, records without the key fall into "(untagged)",
// the enterprise project is its own dimension, tag keys are listed per
// scope, and a key that fails validation never reaches SQL.
func TestIntegrationExploreTagDimensions(t *testing.T) {
	st := testdb.Open(t)
	s := seedLedger(t, st)
	ctx := context.Background()
	win := store.CostQuery{From: day(2026, 9, 1), To: day(2026, 9, 8), Granularity: "day", GroupBy: "tag:team", Metric: "cost"}

	res, err := st.Explore(ctx, store.OperatorScope, win)
	if err != nil {
		t.Fatal(err)
	}
	if res.GroupBy != "tag:team" {
		t.Fatalf("group_by echoed as %q", res.GroupBy)
	}
	want := map[string]float64{"a": 84, "b": 16.8, "zulu": 3.36, store.TagUntagged: 0}
	if len(res.Groups) != len(want) {
		t.Fatalf("groups = %+v", res.Groups)
	}
	for key, total := range want {
		g := groupByKey(res, key)
		if g == nil || !near(f(g.Total), total) || g.Label != key {
			t.Fatalf("tag:team=%s → %+v (want %v)", key, g, total)
		}
	}
	// The untagged group holds the two resources without a team tag.
	if g := groupByKey(res, store.TagUntagged); g.Resources != 2 {
		t.Fatalf("untagged resources = %d", g.Resources)
	}
	// Previous window: team a ran 7×12×0.5 = 42 → +100 %.
	if g := groupByKey(res, "a"); !near(f(g.Previous), 42) || g.DeltaPct == nil || !near(*g.DeltaPct, 100) {
		t.Fatalf("team a previous/delta = %v / %v", g.Previous, g.DeltaPct)
	}
	if !near(f(res.Total.Current), 104.16) {
		t.Fatalf("total = %v", res.Total.Current)
	}

	// Include a tag value; exclude it; select the untagged records.
	only := win
	only.GroupBy, only.Include = "kind", map[string][]string{"tag:team": {"a"}}
	r2, err := st.Explore(ctx, store.OperatorScope, only)
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.Groups) != 1 || r2.Groups[0].Key != "ecs" || !near(f(r2.Total.Current), 84) {
		t.Fatalf("tag:team=a → %+v", r2.Groups)
	}
	without := win
	without.GroupBy, without.Exclude = "kind", map[string][]string{"tag:team": {"a"}}
	r3, err := st.Explore(ctx, store.OperatorScope, without)
	if err != nil {
		t.Fatal(err)
	}
	if !near(f(r3.Total.Current), 104.16-84) {
		t.Fatalf("exclude tag:team=a → total %v", r3.Total.Current)
	}
	untagged := win
	untagged.GroupBy, untagged.Include = "resource", map[string][]string{"tag:team": {store.TagUntagged}}
	r4, err := st.Explore(ctx, store.OperatorScope, untagged)
	if err != nil {
		t.Fatal(err)
	}
	if len(r4.Groups) != 2 || groupByKey(r4, "vm-2") == nil || groupByKey(r4, "ns/pod-1") == nil {
		t.Fatalf("tag:team=(untagged) → %+v", r4.Groups)
	}
	// Two tag filters combine (AND), and a tag filter with a tag group_by.
	both := win
	both.Include = map[string][]string{"tag:team": {"a", "b"}, "tag:env": {"prod"}}
	r5, err := st.Explore(ctx, store.OperatorScope, both)
	if err != nil {
		t.Fatal(err)
	}
	if len(r5.Groups) != 1 || r5.Groups[0].Key != "a" || !near(f(r5.Total.Current), 84) {
		t.Fatalf("team in (a,b) AND env=prod → %+v", r5.Groups)
	}

	// Enterprise project is a static dimension: ep-1 vs (none).
	ep := win
	ep.GroupBy = "enterprise_project"
	r6, err := st.Explore(ctx, store.OperatorScope, ep)
	if err != nil {
		t.Fatal(err)
	}
	if g := groupByKey(r6, "ep-1"); g == nil || !near(f(g.Total), 84) {
		t.Fatalf("enterprise_project ep-1 = %+v", g)
	}
	if g := groupByKey(r6, "(none)"); g == nil || !near(f(g.Total), 20.16) {
		t.Fatalf("enterprise_project (none) = %+v", g)
	}
	epOnly := win
	epOnly.GroupBy, epOnly.Include = "kind", map[string][]string{"enterprise_project": {"ep-1"}}
	if r, err := st.Explore(ctx, store.OperatorScope, epOnly); err != nil || !near(f(r.Total.Current), 84) {
		t.Fatalf("enterprise_project=ep-1 → %v %v", r.Total.Current, err)
	}

	// Tag keys and the grouped tag's values, per scope.
	keys, err := st.TagKeys(ctx, store.OperatorScope, win)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 || keys[0] != "app" || keys[1] != "env" || keys[2] != "team" {
		t.Fatalf("operator tag keys = %v", keys)
	}
	vals, err := st.DimensionValues(ctx, store.OperatorScope, win)
	if err != nil {
		t.Fatal(err)
	}
	teams := vals["tag:team"]
	if len(teams) != 4 || teams[0].Key != store.TagUntagged || teams[1].Key != "a" || teams[2].Key != "b" || teams[3].Key != "zulu" {
		t.Fatalf("tag:team values = %+v", teams)
	}
	if eps := vals["enterprise_project"]; len(eps) != 2 || eps[0].Key != "(none)" || eps[1].Key != "ep-1" {
		t.Fatalf("enterprise_project values = %+v", eps)
	}
	// A tag named only in a filter is listed too (so its chips can resolve).
	vals, err = st.DimensionValues(ctx, store.OperatorScope, both)
	if err != nil {
		t.Fatal(err)
	}
	if envs := vals["tag:env"]; len(envs) != 1 || envs[0].Key != "prod" {
		t.Fatalf("filtered tag:env values = %+v", envs)
	}

	// Scope: B never sees A's tag keys or values, even when it names them.
	bKeys, err := st.TagKeys(ctx, store.CustomerScope(s.b.ID), win)
	if err != nil {
		t.Fatal(err)
	}
	if len(bKeys) != 1 || bKeys[0] != "team" {
		t.Fatalf("B tag keys = %v (A's env/app must not leak)", bKeys)
	}
	bVals, err := st.DimensionValues(ctx, store.CustomerScope(s.b.ID), win)
	if err != nil {
		t.Fatal(err)
	}
	if bt := bVals["tag:team"]; len(bt) != 1 || bt[0].Key != "zulu" {
		t.Fatalf("B tag:team values = %+v", bt)
	}
	leak := win
	leak.Include = map[string][]string{"tag:team": {"a"}}
	bRes, err := st.Explore(ctx, store.CustomerScope(s.b.ID), leak)
	if err != nil {
		t.Fatal(err)
	}
	if len(bRes.Groups) != 0 || !near(f(bRes.Total.Current), 0) {
		t.Fatalf("B naming A's tag value must see nothing: %+v", bRes.Groups)
	}
	bRes, err = st.Explore(ctx, store.CustomerScope(s.b.ID), win)
	if err != nil {
		t.Fatal(err)
	}
	if len(bRes.Groups) != 1 || bRes.Groups[0].Key != "zulu" {
		t.Fatalf("B by tag:team = %+v", bRes.Groups)
	}

	// A key that fails the rule is refused before SQL is built — as group_by,
	// as an include and as an exclude; the store never sees the quote.
	for _, bad := range []string{"tag:team' OR 1=1--", `tag:te"am`, "tag:", "tag:a b", "tag:" + strings.Repeat("k", 129)} {
		q := win
		q.GroupBy = bad
		if _, err := st.Explore(ctx, store.OperatorScope, q); err == nil {
			t.Fatalf("group_by %q must be rejected", bad)
		}
		q = win
		q.Include = map[string][]string{bad: {"x"}}
		if _, err := st.Explore(ctx, store.OperatorScope, q); err == nil {
			t.Fatalf("include %q must be rejected", bad)
		}
		q = win
		q.Exclude = map[string][]string{bad: {"x"}}
		if _, err := st.DimensionValues(ctx, store.OperatorScope, q); err == nil {
			t.Fatalf("exclude %q must be rejected", bad)
		}
	}
	// A VALUE may contain anything — it is data, bound as a parameter.
	q := win
	q.Include = map[string][]string{"tag:team": {"a' OR 1=1--", "a"}}
	if r, err := st.Explore(ctx, store.OperatorScope, q); err != nil || !near(f(r.Total.Current), 84) {
		t.Fatalf("hostile value must be harmless data: %v %v", r.Total.Current, err)
	}
}
