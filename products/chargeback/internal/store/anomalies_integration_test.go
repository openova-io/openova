package store_test

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// Anomaly readers (#6867, DESIGN.md §3.6): a 30-day ledger for customer A
// with one ECS all month, a second ECS for one day (the spike), a flat EVS
// volume, an unpriced platform SKU and the CPU metric; customer B has a
// flat EIP.

type anomalySeed struct {
	a, b       store.Customer
	srcA, srcB store.CostSource
}

func seedSpike(t *testing.T, st *store.Store) anomalySeed {
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
	for d := 1; d <= 30; d++ {
		for h := 0; h < 24; h++ {
			at := day(2026, 8, d).Add(time.Duration(h) * time.Hour)
			rec(a, srcA, "vm-1", "ecs", "ecs.m7n.xlarge.8", "instance-hour", 1, at, map[string]any{"name": "web-1", "status": "ACTIVE"})
			if d == 20 {
				rec(a, srcA, "vm-2", "ecs", "ecs.m7n.xlarge.8", "instance-hour", 1, at, map[string]any{"name": "batch-2", "status": "ACTIVE"})
			}
			rec(a, srcA, "vol-1", "evs", "evs.ssd.gb", "gb-hour", 100, at, map[string]any{"name": "vol-1", "server_status": "ACTIVE"})
			rec(a, srcA2, "ns/pod-1", "k8s-pod", "k8s.vcpu", "vcpu-hour", 0.5, at, map[string]any{"name": "pod-1"})
			rec(a, srcA, "vm-1", "ecs", "ecs.cpu_util", "pct-hour-avg", 42, at, map[string]any{"name": "web-1"})
			rec(b, srcB, "eip-1", "eip", "eip", "hour", 1, at, map[string]any{"name": "1.2.3.4"})
		}
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	return anomalySeed{a: a, b: b, srcA: srcA, srcB: srcB}
}

func TestIntegrationDailyCostByCustomerKind(t *testing.T) {
	st := testdb.Open(t)
	s := seedSpike(t, st)
	ctx := context.Background()
	rows, err := st.DailyCostByCustomerKind(ctx, store.OperatorScope, "", day(2026, 8, 1), day(2026, 8, 31))
	if err != nil {
		t.Fatal(err)
	}
	// A: ecs 30 days + evs 30 days; B: eip 30 days. The unpriced platform
	// SKU and the CPU metric contribute no rows at all.
	if len(rows) != 90 {
		t.Fatalf("rows = %d", len(rows))
	}
	byKey := map[string]store.DailyKindCost{}
	for _, r := range rows {
		if r.ResourceKind == "k8s-pod" {
			t.Fatalf("unpriced kind in daily cost: %+v", r)
		}
		byKey[r.CustomerID+"/"+r.ResourceKind+"/"+r.Day] = r
	}
	if r := byKey[s.a.ID+"/ecs/2026-08-19"]; r.Cost != "12.000000" || r.CustomerName != "Acme" {
		t.Fatalf("ecs 08-19 = %+v", r)
	}
	if r := byKey[s.a.ID+"/ecs/2026-08-20"]; r.Cost != "24.000000" {
		t.Fatalf("ecs 08-20 = %+v", r)
	}
	if r := byKey[s.a.ID+"/evs/2026-08-20"]; r.Cost != "2.400000" {
		t.Fatalf("evs 08-20 = %+v", r)
	}
	if r := byKey[s.b.ID+"/eip/2026-08-01"]; r.Cost != "0.480000" || r.CustomerName != "Bravo" {
		t.Fatalf("eip 08-01 = %+v", r)
	}
	// One customer only.
	rows, err = st.DailyCostByCustomerKind(ctx, store.OperatorScope, s.b.ID, day(2026, 8, 1), day(2026, 8, 31))
	if err != nil || len(rows) != 30 || rows[0].CustomerID != s.b.ID {
		t.Fatalf("B only: %d rows err=%v", len(rows), err)
	}
	// Scope: B asking for A sees B; an empty scope is refused.
	rows, err = st.DailyCostByCustomerKind(ctx, store.CustomerScope(s.b.ID), s.a.ID, day(2026, 8, 1), day(2026, 8, 31))
	if err != nil || len(rows) != 30 || rows[0].CustomerID != s.b.ID {
		t.Fatalf("B naming A: %d rows err=%v", len(rows), err)
	}
	if _, err := st.DailyCostByCustomerKind(ctx, store.Scope{}, "", day(2026, 8, 1), day(2026, 8, 31)); err == nil {
		t.Fatal("empty scope must not read the ledger")
	}
}

func TestIntegrationDayDrivers(t *testing.T) {
	st := testdb.Open(t)
	s := seedSpike(t, st)
	ctx := context.Background()
	// The spike day: vm-2 appears (12 against a 7-day mean of 0) and the
	// SKU carries the same +12; vm-1 is unchanged and is not a driver.
	drivers, err := st.DayDrivers(ctx, store.OperatorScope, s.a.ID, "ecs", "2026-08-20")
	if err != nil {
		t.Fatal(err)
	}
	if len(drivers) != 2 {
		t.Fatalf("drivers = %+v", drivers)
	}
	if d := drivers[0]; d.Kind != "resource" || d.Key != "vm-2" || d.Label != "batch-2" || d.Delta != "12.000000" {
		t.Fatalf("first driver = %+v", d)
	}
	if d := drivers[1]; d.Kind != "sku" || d.Key != "ecs.m7n.xlarge.8" || d.Delta != "12.000000" {
		t.Fatalf("second driver = %+v", d)
	}
	// The day after: vm-2 is gone, so it is a negative driver against its
	// 7-day mean of 12/7, and so is the SKU.
	drivers, err = st.DayDrivers(ctx, store.OperatorScope, s.a.ID, "ecs", "2026-08-21")
	if err != nil {
		t.Fatal(err)
	}
	if len(drivers) != 2 || drivers[0].Key != "vm-2" || drivers[0].Delta != "-1.714286" || drivers[1].Delta != "-1.714286" {
		t.Fatalf("day-after drivers = %+v", drivers)
	}
	// The EVS kind is flat: no drivers at all.
	drivers, err = st.DayDrivers(ctx, store.OperatorScope, s.a.ID, "evs", "2026-08-20")
	if err != nil || len(drivers) != 0 {
		t.Fatalf("evs drivers = %+v err=%v", drivers, err)
	}
	// Scope: B asking about A's spike gets B's (empty) ecs picture.
	drivers, err = st.DayDrivers(ctx, store.CustomerScope(s.b.ID), s.a.ID, "ecs", "2026-08-20")
	if err != nil || len(drivers) != 0 {
		t.Fatalf("B on A = %+v err=%v", drivers, err)
	}
	if _, err := st.DayDrivers(ctx, store.OperatorScope, s.a.ID, "ecs", "20 Aug"); err == nil {
		t.Fatal("bad day must be rejected")
	}
	if _, err := st.DayDrivers(ctx, store.OperatorScope, "", "ecs", "2026-08-20"); err == nil {
		t.Fatal("drivers need a customer")
	}
}
