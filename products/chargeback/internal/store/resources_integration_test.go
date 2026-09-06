package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// Resources against Postgres (#6867): inventory joined with window cost,
// filters, sorting, paging, scope, and the detail's daily series.

type resSeed struct {
	a, b       store.Customer
	srcA, srcB store.CostSource
}

// seedResources writes for customer A (source in me-east-1) on 2026-09-01..03:
//   - vm-1  ecs  ACTIVE   1 h × 0.5     × 72 h = 36.0
//   - vm-2  ecs  SHUTOFF  1 h × 0.5     × 72 h = 36.0 (policy compute: still billed)
//   - vol-1 evs  100 GB × 0.001 × 72 h          = 7.2, deleted on day 3
//   - pod   k8s-pod unpriced
//
// and for customer B (me-east-2): eip-1 at 0.02 × 72 h = 1.44.
func seedResources(t *testing.T, st *store.Store) resSeed {
	t.Helper()
	ctx := context.Background()
	book, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "list", Currency: "OMR", AnnualDivisor: 8760, BillStopped: "compute"})
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
	a, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "acme", Name: "Acme", AdminEmail: "a@acme.example", PriceBookID: book.ID})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "bravo", Name: "Bravo", AdminEmail: "b@bravo.example", PriceBookID: book.ID})
	if err != nil {
		t.Fatal(err)
	}
	srcA, _, err := st.UpsertSource(ctx, a.ID, "huawei-project", "me-east-1", "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	srcB, _, err := st.UpsertSource(ctx, b.ID, "huawei-project", "me-east-2", "proj-b")
	if err != nil {
		t.Fatal(err)
	}
	seen := day(2026, 9, 3).Add(23 * time.Hour)
	if _, err := st.UpsertInventory(ctx, srcA.ID, []store.InventoryUpsert{
		{ResourceID: "vm-1", Kind: "ecs", Name: "web-1", Attrs: map[string]any{"status": "ACTIVE", "flavor": "m7n.xlarge.8", "transitions": []map[string]any{{"at": "2026-09-01T00:00:00Z", "from": "", "to": "ACTIVE"}}}, Created: day(2026, 8, 20), SeenAt: seen},
		{ResourceID: "vm-2", Kind: "ecs", Name: "batch-2", Attrs: map[string]any{"status": "SHUTOFF", "flavor": "m7n.xlarge.8"}, Created: day(2026, 8, 25), SeenAt: seen},
		{ResourceID: "vol-1", Kind: "evs", Name: "data-vol", Attrs: map[string]any{"size_gb": 100, "attached_to": "vm-1"}, Created: day(2026, 8, 20), SeenAt: seen},
		{ResourceID: "ns/pod-1", Kind: "k8s-pod", Name: "pod-1", Attrs: map[string]any{}, SeenAt: seen},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetInventoryBounds(ctx, srcA.ID, "vol-1", nil, tp(day(2026, 9, 3).Add(12*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertInventory(ctx, srcB.ID, []store.InventoryUpsert{
		{ResourceID: "eip-1", Kind: "eip", Name: "1.2.3.4", Attrs: map[string]any{"status": "ACTIVE"}, SeenAt: seen},
	}); err != nil {
		t.Fatal(err)
	}
	var recs []store.UsageRecord
	rec := func(c store.Customer, src store.CostSource, res, kind, sku, unit string, qty float64, at time.Time, region string, labels map[string]any) {
		lb, _ := json.Marshal(labels)
		recs = append(recs, store.UsageRecord{CustomerID: c.ID, SourceID: src.ID, ResourceID: res, ResourceKind: kind, SKU: sku,
			Quantity: store.Decimal(strconv.FormatFloat(qty, 'f', 6, 64)), Unit: unit, WindowStart: at, WindowEnd: at.Add(time.Hour), Region: region, Labels: lb})
	}
	for d := 1; d <= 3; d++ {
		for h := 0; h < 24; h++ {
			at := day(2026, 9, d).Add(time.Duration(h) * time.Hour)
			rec(a, srcA, "vm-1", "ecs", "ecs.m7n.xlarge.8", "instance-hour", 1, at, "me-east-1", map[string]any{"name": "web-1", "status": "ACTIVE"})
			rec(a, srcA, "vm-1", "ecs", "ecs.cpu_util", "pct-hour-avg", 40, at, "me-east-1", nil)
			rec(a, srcA, "vm-2", "ecs", "ecs.m7n.xlarge.8", "instance-hour", 1, at, "me-east-1", map[string]any{"name": "batch-2", "status": "SHUTOFF"})
			rec(a, srcA, "vol-1", "evs", "evs.ssd.gb", "gb-hour", 100, at, "me-east-1", map[string]any{"name": "data-vol"})
			rec(a, srcA, "ns/pod-1", "k8s-pod", "k8s.vcpu", "vcpu-hour", 0.5, at, "", map[string]any{"name": "pod-1"})
			rec(b, srcB, "eip-1", "eip", "eip", "hour", 1, at, "me-east-2", map[string]any{"name": "1.2.3.4"})
		}
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	return resSeed{a: a, b: b, srcA: srcA, srcB: srcB}
}

func tp(t time.Time) *time.Time { return &t }

func resByID(l store.ResourceList, id string) *store.ResourceRow {
	for i := range l.Rows {
		if l.Rows[i].ResourceID == id {
			return &l.Rows[i]
		}
	}
	return nil
}

func TestIntegrationResourcesListFiltersSortsAndPages(t *testing.T) {
	st := testdb.Open(t)
	s := seedResources(t, st)
	ctx := context.Background()
	win := store.ResourceQuery{From: day(2026, 9, 1), To: day(2026, 9, 4)}

	all, err := st.ListResources(ctx, store.OperatorScope, win)
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 5 || len(all.Rows) != 5 || all.Limit != store.DefaultResourceLimit || all.Currency != "OMR" || all.MixedCurrency {
		t.Fatalf("all = total %d rows %d limit %d currency %q mixed %v", all.Total, len(all.Rows), all.Limit, all.Currency, all.MixedCurrency)
	}
	// 36 + 36 + 7.2 + 0 + 1.44 = 80.64 across every customer.
	if all.SumCost != "80.640000" {
		t.Fatalf("sum_cost = %s", all.SumCost)
	}
	// Default sort: cost desc; the two ECS tie at 36 and order by name.
	if all.Rows[0].ResourceID != "vm-2" || all.Rows[1].ResourceID != "vm-1" || all.Rows[4].ResourceID != "ns/pod-1" {
		t.Fatalf("cost-desc order = %s %s … %s", all.Rows[0].ResourceID, all.Rows[1].ResourceID, all.Rows[4].ResourceID)
	}
	vm1 := resByID(all, "vm-1")
	if vm1.Cost != "36.000000" || vm1.Status != "live" || vm1.Region != "me-east-1" || vm1.CustomerName != "Acme" || vm1.Currency != "OMR" || vm1.Kind != "ecs" || vm1.Name != "web-1" {
		t.Fatalf("vm-1 = %+v", *vm1)
	}
	if !vm1.FirstSeen.Equal(day(2026, 8, 20)) || vm1.DeletedAt != nil || vm1.Attrs != nil {
		t.Fatalf("vm-1 bounds/attrs = %+v", *vm1)
	}
	// Lines: the metric sample never appears; quantity and cost per SKU.
	if len(vm1.Lines) != 1 || vm1.Lines[0].SKU != "ecs.m7n.xlarge.8" || vm1.Lines[0].Quantity != "72.000000" || vm1.Lines[0].Cost != "36.000000" || vm1.Lines[0].Unit != "instance-hour" {
		t.Fatalf("vm-1 lines = %+v", vm1.Lines)
	}
	if vm2 := resByID(all, "vm-2"); vm2.Status != "stopped" {
		t.Fatalf("vm-2 status = %s", vm2.Status)
	}
	vol := resByID(all, "vol-1")
	if vol.Status != "deleted" || vol.DeletedAt == nil || vol.Cost != "7.200000" {
		t.Fatalf("vol-1 = %+v", *vol)
	}
	// Unpriced platform usage: a row with zero cost and its quantity on the line.
	pod := resByID(all, "ns/pod-1")
	if pod.Cost != "0.000000" || len(pod.Lines) != 1 || pod.Lines[0].Quantity != "36.000000" || pod.Lines[0].Cost != "0.000000" {
		t.Fatalf("pod = %+v", *pod)
	}
	// Region falls back to the source's region for a record with none.
	if pod.Region != "me-east-1" {
		t.Fatalf("pod region = %q", pod.Region)
	}

	// kind filter.
	q := win
	q.Kind = "ecs"
	ecs, err := st.ListResources(ctx, store.OperatorScope, q)
	if err != nil || ecs.Total != 2 || ecs.SumCost != "72.000000" {
		t.Fatalf("kind=ecs → %+v err=%v", ecs, err)
	}
	// status filter.
	for status, want := range map[string]int{"live": 3, "stopped": 1, "deleted": 1, "all": 5} {
		q := win
		q.Status = status
		l, err := st.ListResources(ctx, store.OperatorScope, q)
		if err != nil || l.Total != want {
			t.Fatalf("status=%s → %d (want %d) err=%v", status, l.Total, want, err)
		}
	}
	// q matches name or id, case-insensitively, with LIKE metacharacters literal.
	q = win
	q.Q = "WEB"
	if l, _ := st.ListResources(ctx, store.OperatorScope, q); l.Total != 1 || l.Rows[0].ResourceID != "vm-1" {
		t.Fatalf("q=WEB → %+v", l.Rows)
	}
	q.Q = "vm-"
	if l, _ := st.ListResources(ctx, store.OperatorScope, q); l.Total != 2 {
		t.Fatalf("q=vm- → %d", l.Total)
	}
	q.Q = "%"
	if l, _ := st.ListResources(ctx, store.OperatorScope, q); l.Total != 0 {
		t.Fatalf("q=%% matched %d rows — LIKE metacharacters must be literal", l.Total)
	}
	// region + customer filters.
	q = win
	q.Region = "me-east-2"
	if l, _ := st.ListResources(ctx, store.OperatorScope, q); l.Total != 1 || l.Rows[0].ResourceID != "eip-1" {
		t.Fatalf("region → %+v", l.Rows)
	}
	q = win
	q.CustomerID = s.b.ID
	if l, _ := st.ListResources(ctx, store.OperatorScope, q); l.Total != 1 || l.SumCost != "1.440000" {
		t.Fatalf("customer=B → %+v", l)
	}

	// Sort by cost asc and by name; paging reports the whole count.
	q = win
	q.Sort, q.Order = "cost", "asc"
	// Ascending: pod (0) first; the tied ECS pair ends the list ordered by
	// name, batch-2 (vm-2) before web-1 (vm-1).
	if l, _ := st.ListResources(ctx, store.OperatorScope, q); l.Rows[0].ResourceID != "ns/pod-1" || l.Rows[3].ResourceID != "vm-2" || l.Rows[4].ResourceID != "vm-1" {
		t.Fatalf("cost asc = %s … %s", l.Rows[0].ResourceID, l.Rows[4].ResourceID)
	}
	q.Sort, q.Order = "name", "asc"
	if l, _ := st.ListResources(ctx, store.OperatorScope, q); l.Rows[0].Name != "1.2.3.4" || l.Rows[1].Name != "batch-2" {
		t.Fatalf("name asc = %s, %s", l.Rows[0].Name, l.Rows[1].Name)
	}
	q = win
	q.Limit, q.Offset = 2, 2
	page, err := st.ListResources(ctx, store.OperatorScope, q)
	if err != nil || len(page.Rows) != 2 || page.Total != 5 || page.SumCost != "80.640000" || page.Limit != 2 || page.Offset != 2 {
		t.Fatalf("page 2 = %+v err=%v", page, err)
	}
	if page.Rows[0].ResourceID != "vol-1" || page.Rows[1].ResourceID != "eip-1" {
		t.Fatalf("page 2 rows = %s, %s", page.Rows[0].ResourceID, page.Rows[1].ResourceID)
	}
	q.Offset = 10
	if l, _ := st.ListResources(ctx, store.OperatorScope, q); len(l.Rows) != 0 || l.Total != 0 {
		// Past the end there are no rows; the window aggregates ride on rows,
		// so they read 0 — the page before it already carried the count.
		t.Fatalf("beyond the end = %+v", l)
	}
	// Bad sort / status are rejected, not silently defaulted.
	q = win
	q.Sort = "colour"
	if _, err := st.ListResources(ctx, store.OperatorScope, q); err == nil {
		t.Fatal("bad sort accepted")
	}
	q = win
	q.Status = "gone"
	if _, err := st.ListResources(ctx, store.OperatorScope, q); err == nil {
		t.Fatal("bad status accepted")
	}
}

func TestIntegrationResourcesScopeCannotLeak(t *testing.T) {
	st := testdb.Open(t)
	s := seedResources(t, st)
	ctx := context.Background()
	win := store.ResourceQuery{From: day(2026, 9, 1), To: day(2026, 9, 4)}

	// B lists, even naming A: only B's row.
	q := win
	q.CustomerID = s.a.ID
	l, err := st.ListResources(ctx, store.CustomerScope(s.b.ID), q)
	if err != nil {
		t.Fatal(err)
	}
	if l.Total != 1 || l.Rows[0].CustomerID != s.b.ID || l.SumCost != "1.440000" {
		t.Fatalf("B naming A saw %+v", l)
	}
	// B fetches A's resource by its real ids: not found.
	if _, err := st.GetResource(ctx, store.CustomerScope(s.b.ID), s.srcA.ID, "vm-1", win.From, win.To); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("B read A's resource: %v", err)
	}
	// An empty scope is a bug upstream, never a wildcard.
	if _, err := st.ListResources(ctx, store.Scope{}, win); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("empty scope listed resources: %v", err)
	}
	if _, err := st.GetResource(ctx, store.Scope{}, s.srcA.ID, "vm-1", win.From, win.To); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("empty scope read a resource: %v", err)
	}
	// A reads its own.
	d, err := st.GetResource(ctx, store.CustomerScope(s.a.ID), s.srcA.ID, "vm-1", win.From, win.To)
	if err != nil || d.ResourceID != "vm-1" {
		t.Fatalf("A reading its own resource: %+v err=%v", d, err)
	}
}

func TestIntegrationResourceDetailDailySumsToTheRow(t *testing.T) {
	st := testdb.Open(t)
	s := seedResources(t, st)
	ctx := context.Background()

	// A window wider than the data: days without records are flagged.
	d, err := st.GetResource(ctx, store.OperatorScope, s.srcA.ID, "vm-1", day(2026, 8, 31), day(2026, 9, 5))
	if err != nil {
		t.Fatal(err)
	}
	if d.Cost != "36.000000" || d.Status != "live" || d.From != "2026-08-31" || d.To != "2026-09-05" {
		t.Fatalf("detail = %+v", d.ResourceRow)
	}
	if len(d.Daily) != 5 {
		t.Fatalf("daily buckets = %d, want 5 uniform days", len(d.Daily))
	}
	var sum store.Decimal = "0"
	for i, dc := range d.Daily {
		wantData := i >= 1 && i <= 3
		if dc.HasData != wantData {
			t.Fatalf("day %s has_data=%v", dc.Day, dc.HasData)
		}
		if wantData && dc.Cost != "12.000000" {
			t.Fatalf("day %s cost = %s", dc.Day, dc.Cost)
		}
		sum, _ = rating.Sum(sum, dc.Cost)
	}
	if sum != d.Cost {
		t.Fatalf("daily Σ %s ≠ row cost %s", sum, d.Cost)
	}
	if len(d.Lines) != 1 || d.Lines[0].Cost != "36.000000" {
		t.Fatalf("lines = %+v", d.Lines)
	}
	// Attributes, transitions and the newest raw records come along.
	var attrs map[string]any
	if err := json.Unmarshal(d.Attrs, &attrs); err != nil || attrs["flavor"] != "m7n.xlarge.8" {
		t.Fatalf("attrs = %s err=%v", d.Attrs, err)
	}
	if len(d.Transitions) != 1 || d.Transitions[0]["to"] != "ACTIVE" {
		t.Fatalf("transitions = %+v", d.Transitions)
	}
	// 72 meter records + 72 cpu_util samples exist; the newest 48 come back
	// newest first, and the sample is included here (it is a record).
	if len(d.RecordsRecent) != 48 || !d.RecordsRecent[0].WindowStart.After(d.RecordsRecent[47].WindowStart) {
		t.Fatalf("records_recent = %d, first %v last %v", len(d.RecordsRecent), d.RecordsRecent[0].WindowStart, d.RecordsRecent[47].WindowStart)
	}
	// A resource with no transitions reports an empty list, not null.
	d2, err := st.GetResource(ctx, store.OperatorScope, s.srcB.ID, "eip-1", day(2026, 9, 1), day(2026, 9, 4))
	if err != nil || d2.Transitions == nil || len(d2.Transitions) != 0 || d2.Cost != "1.440000" {
		t.Fatalf("eip detail = %+v err=%v", d2, err)
	}
	// Unknown resource.
	if _, err := st.GetResource(ctx, store.OperatorScope, s.srcA.ID, "nope", day(2026, 9, 1), day(2026, 9, 4)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown resource: %v", err)
	}
}

// The resources of a customer must sum to the explorer total for the same
// window — both use costPricedExpr, and this is the test that keeps it so.
func TestIntegrationResourcesReconcileWithExplore(t *testing.T) {
	st := testdb.Open(t)
	s := seedResources(t, st)
	ctx := context.Background()
	l, err := st.ListResources(ctx, store.OperatorScope, store.ResourceQuery{From: day(2026, 9, 1), To: day(2026, 9, 4), CustomerID: s.a.ID})
	if err != nil {
		t.Fatal(err)
	}
	ex, err := st.Explore(ctx, store.OperatorScope, store.CostQuery{From: day(2026, 9, 1), To: day(2026, 9, 4), Granularity: "month", CustomerID: s.a.ID})
	if err != nil {
		t.Fatal(err)
	}
	if string(l.SumCost) != string(ex.Total.Current) {
		t.Fatalf("resources Σ %s ≠ explore %s", l.SumCost, ex.Total.Current)
	}
}
