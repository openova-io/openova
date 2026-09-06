package store_test

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
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
			rec(a, srcA, "vm-1", "ecs", "ecs.m7n.xlarge.8", "instance-hour", 1, at, map[string]any{"name": "web-1", "status": "ACTIVE"})
			rec(a, srcA, "vol-1", "evs", "evs.ssd.gb", "gb-hour", 100, at, map[string]any{"name": "vol-1", "server_status": "ACTIVE"})
			if d == 3 {
				rec(a, srcA, "vm-2", "ecs", "ecs.m7n.xlarge.8", "instance-hour", 1, at, map[string]any{"name": "batch-2", "status": "SHUTOFF"})
			}
			// Platform usage on the second source: unpriced.
			rec(a, srcA2, "ns/pod-1", "k8s-pod", "k8s.vcpu", "vcpu-hour", 0.5, at, map[string]any{"name": "pod-1", "namespace": "ns", "tier": "organization"})
			// The metric sample must never count.
			rec(a, srcA, "vm-1", "ecs", "ecs.cpu_util", "pct-hour-avg", 42, at, map[string]any{"name": "web-1"})
			// Customer B: one EIP hour per hour.
			rec(b, srcB, "eip-1", "eip", "eip", "hour", 1, at, map[string]any{"name": "1.2.3.4"})
		}
	}
	// Previous window (Aug 25..31): A runs the ECS 12 h a day only.
	for d := 25; d <= 31; d++ {
		for h := 0; h < 12; h++ {
			at := day(2026, 8, d).Add(time.Duration(h) * time.Hour)
			rec(a, srcA, "vm-1", "ecs", "ecs.m7n.xlarge.8", "instance-hour", 1, at, map[string]any{"name": "web-1", "status": "ACTIVE"})
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
