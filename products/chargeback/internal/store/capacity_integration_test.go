package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/capacity"
	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// Capacity management (DESIGN.md §11). The seed below IS the founder's worked
// example, so every figure the pure arithmetic is pinned on is derived here
// end to end from metered usage rather than handed in:
//
//	now = 2026-09-09 10:30Z → the latest complete hour is 09:00 (a record at
//	10:00, the hour in progress, is written and must be ignored).
//
//	POOL m7n-a, zone me-east-215a: 10 servers × 64 vCPU / 512 GiB, N+1 reserve
//	(one server's worth), vCPU 4:1, RAM 1:1, 45 days' lead time.
//	  ecs.m7n.2xlarge.8        guaranteed   40/h now, 32 on 09-01 (+1 a day)
//	  ecs.m7n.2xlarge.8.burst  burstable    32/h flat (the SAME shape, a
//	                                        second SKU at a second price —
//	                                        the class is the placement's)
//	  ecs.s7n.2xlarge.2        spot          4/h, NO availability zone, so it
//	                                        lands in the default zone and is
//	                                        flagged as such
//	→ guaranteed 320 vCPU / 2,560 GiB · burstable 256 / 2,048 · spot 32 / 64
//	→ RAM binds, the pool is full, 192 physical vCPU are stranded, and 64 GiB
//	  of spot must be reclaimed.
//
//	POOL blk-b, zone me-east-215b: 1 × 1,000 GiB block SSD, 10 days' lead time.
//	  evs.ssd.gb  guaranteed  180 GB now, +10 a day → 82 days to the wall,
//	                          order by day 72.
//	  eip + eip.bandwidth_mbps are metered here and NO pool holds their
//	  resources → unplaced, listed by name (the control that a SKU counted
//	  against nothing never reads as spare capacity).
//
//	POOL gpu-c, zone me-east-215c: 2 × 4 gpu_cards — a resource kind NOBODY
//	  SEEDED, which is the whole point of resource kinds being data.
//	  ecs.gpu.large {gpu_cards 1, vcpu 8} guaranteed 3/h → the cards land, the
//	  vCPU has no pool in that zone and is reported as resource-unplaced.
//
//	CONTROLS: nat.1 has no shape at all (unshaped); ecs.cpu_util is a metric
//	and never a meter; k8s.vcpu is the platform layer and would double-count
//	the same hardware; eu-west-101 is a region nobody configured.

type capacitySeed struct {
	region store.CapacityRegion
	zoneA  store.CapacityZone
	zoneB  store.CapacityZone
	zoneC  store.CapacityZone
	poolA  store.CapacityPool
	poolB  store.CapacityPool
	poolC  store.CapacityPool
	now    time.Time
	src    store.CostSource
}

// growthLikeAPI is the store's CapacityGrowth the API passes: the run-rate
// trend of internal/rating over the daily series.
func growthLikeAPI(days []store.CapacityDayPoint) (float64, bool) {
	serie := make([]rating.DayCost, 0, len(days))
	for _, d := range days {
		f, _ := strconv.ParseFloat(string(d.Consumed), 64)
		serie = append(serie, rating.DayCost{Day: d.Day, Cost: f})
	}
	_, trend, ok := rating.RunRate(serie)
	return trend, ok
}

const (
	skuGuaranteed = "ecs.m7n.2xlarge.8"
	skuBurstable  = "ecs.m7n.2xlarge.8.burst"
	skuSpot       = "ecs.s7n.2xlarge.2"
	skuGPU        = "ecs.gpu.large"
	resGPU        = "gpu_cards"
)

func seedCapacity(t *testing.T, st *store.Store) capacitySeed {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 9, 10, 30, 0, 0, time.UTC)
	cust, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "acme", Name: "Acme", AdminEmail: "a@acme.example", StartDate: "2026-08-01"})
	if err != nil {
		t.Fatal(err)
	}
	src, _, err := st.UpsertSource(ctx, cust.ID, store.SourceKindHuaweiProject, "me-east-215", "proj-acme")
	if err != nil {
		t.Fatal(err)
	}
	eu, _, err := st.UpsertSource(ctx, cust.ID, store.SourceKindHuaweiProject, "eu-west-101", "proj-eu")
	if err != nil {
		t.Fatal(err)
	}
	plat, _, err := st.UpsertSource(ctx, cust.ID, store.SourceKindOrg, "", "acme")
	if err != nil {
		t.Fatal(err)
	}
	seen := now
	if _, err := st.UpsertInventory(ctx, src.ID, []store.InventoryUpsert{
		{ResourceID: "vm-g", Kind: "ecs", Name: "guaranteed", Attrs: map[string]any{"flavor": "m7n.2xlarge.8", "availability_zone": "me-east-215a"}, SeenAt: seen},
		{ResourceID: "vm-b", Kind: "ecs", Name: "burstable", Attrs: map[string]any{"flavor": "m7n.2xlarge.8", "availability_zone": "ME-EAST-215A"}, SeenAt: seen},
		{ResourceID: "vm-s", Kind: "ecs", Name: "spot", Attrs: map[string]any{"flavor": "s7n.2xlarge.2"}, SeenAt: seen},
		{ResourceID: "vol-1", Kind: "evs", Name: "data", Attrs: map[string]any{"availability_zone": "me-east-215b"}, SeenAt: seen},
		{ResourceID: "eip-1", Kind: "eip", Name: "gw", Attrs: map[string]any{"availability_zone": "me-east-215b"}, SeenAt: seen},
		{ResourceID: "gpu-1", Kind: "ecs", Name: "trainer", Attrs: map[string]any{"availability_zone": "me-east-215c"}, SeenAt: seen},
	}); err != nil {
		t.Fatal(err)
	}

	var recs []store.UsageRecord
	rec := func(s store.CostSource, res, kind, sku, unit string, qty float64, at time.Time) {
		lb, _ := json.Marshal(map[string]any{"name": res})
		recs = append(recs, store.UsageRecord{CustomerID: cust.ID, SourceID: s.ID, ResourceID: res, ResourceKind: kind, SKU: sku,
			Quantity: store.Decimal(strconv.FormatFloat(qty, 'f', 6, 64)), Unit: unit, WindowStart: at, WindowEnd: at.Add(time.Hour), Region: s.Region, Labels: lb})
	}
	for d := 1; d <= 9; d++ {
		hours := 24
		if d == 9 {
			hours = 10 // 00:00 .. 09:00 complete; 10:00 is written below and must be ignored
		}
		for h := 0; h < hours; h++ {
			at := day(2026, 9, d).Add(time.Duration(h) * time.Hour)
			rec(src, "vm-g", "ecs", skuGuaranteed, "instance-hour", float64(31+d), at)
			rec(src, "vm-b", "ecs", skuBurstable, "instance-hour", 32, at)
			rec(src, "vm-s", "ecs", skuSpot, "instance-hour", 4, at)
			rec(src, "vm-g", "ecs", store.SKUCPUUtil, "pct-hour-avg", 55, at)
			rec(src, "vol-1", "evs", "evs.ssd.gb", "gb-hour", float64(100+10*(d-1)), at)
			rec(src, "eip-1", "eip", "eip", "hour", 1, at)
			rec(src, "eip-1", "eip", "eip.bandwidth_mbps", "mbps-hour", 10, at)
			rec(src, "gpu-1", "ecs", skuGPU, "instance-hour", 3, at)
			rec(src, "nat-1", "nat", "nat.1", "hour", 1, at)
			rec(eu, "eip-eu", "eip", "eip", "hour", 1, at)
			rec(plat, "acme/pod-1", "k8s-pod", store.SKUVCPU, store.UnitVCPU, 40, at)
		}
	}
	// The hour in progress.
	rec(src, "vm-g", "ecs", skuGuaranteed, "instance-hour", 999, day(2026, 9, 9).Add(10*time.Hour))
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}

	// The shapes that are not seeded: the burstable twin of the guaranteed
	// SKU, and a GPU flavour whose vector names a resource kind nobody seeded.
	if _, err := st.PutCapacityShape(ctx, skuBurstable, map[string]store.Decimal{capacity.ResourceVCPU: "8", capacity.ResourceMemoryGiB: "64"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCapacityShape(ctx, skuGPU, map[string]store.Decimal{resGPU: "1", capacity.ResourceVCPU: "8"}); err != nil {
		t.Fatal(err)
	}

	region, err := st.CreateCapacityRegion(ctx, "ME-East-215", "Muscat", "")
	if err != nil {
		t.Fatal(err)
	}
	zoneA, err := st.CreateCapacityZone(ctx, region.ID, "me-east-215a", "AZ 1", false)
	if err != nil {
		t.Fatal(err)
	}
	zoneB, err := st.CreateCapacityZone(ctx, region.ID, "me-east-215b", "AZ 2", false)
	if err != nil {
		t.Fatal(err)
	}
	zoneC, err := st.CreateCapacityZone(ctx, region.ID, "me-east-215c", "AZ 3", false)
	if err != nil {
		t.Fatal(err)
	}

	poolA, err := st.CreateCapacityPool(ctx, zoneA.ID, store.CapacityPoolInput{
		Name: "m7n-a", Machines: "10", LeadTimeDays: 45, Note: "batch one",
		Resources: []store.CapacityPoolResource{
			{Resource: capacity.ResourceVCPU, PerMachine: "64", Reserve: "64", OvercommitRatio: "4"},
			{Resource: capacity.ResourceMemoryGiB, PerMachine: "512", Reserve: "512", OvercommitRatio: "1"},
		},
	}, "ops@nc.example")
	if err != nil {
		t.Fatal(err)
	}
	poolB, err := st.CreateCapacityPool(ctx, zoneB.ID, store.CapacityPoolInput{
		Name: "blk-b", Machines: "1", LeadTimeDays: 10,
		Resources: []store.CapacityPoolResource{{Resource: capacity.ResourceBlockSSD, PerMachine: "1000", OvercommitRatio: "1"}},
	}, "ops@nc.example")
	if err != nil {
		t.Fatal(err)
	}
	poolC, err := st.CreateCapacityPool(ctx, zoneC.ID, store.CapacityPoolInput{
		Name: "gpu-c", Machines: "2", LeadTimeDays: 90,
		Resources: []store.CapacityPoolResource{{Resource: resGPU, PerMachine: "4", OvercommitRatio: "1"}},
	}, "ops@nc.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, pl := range []struct{ pool, sku, class string }{
		{poolA.ID, skuGuaranteed, capacity.ClassGuaranteed},
		{poolA.ID, skuBurstable, capacity.ClassBurstable},
		{poolA.ID, skuSpot, capacity.ClassSpot},
		{poolB.ID, "evs.ssd.gb", capacity.ClassGuaranteed},
		{poolC.ID, skuGPU, capacity.ClassGuaranteed},
	} {
		if _, err := st.PutCapacityPlacement(ctx, pl.pool, pl.sku, pl.class, "ops@nc.example"); err != nil {
			t.Fatalf("place %s on %s: %v", pl.sku, pl.pool, err)
		}
	}
	return capacitySeed{region: region, zoneA: zoneA, zoneB: zoneB, zoneC: zoneC, poolA: poolA, poolB: poolB, poolC: poolC, now: now, src: src}
}

func zoneOf(t *testing.T, ov store.CapacityOverview, region, zone string) store.CapacityZoneView {
	t.Helper()
	for _, r := range ov.Regions {
		if r.Code != region {
			continue
		}
		for _, z := range r.Zones {
			if z.Code == zone {
				return z
			}
		}
	}
	t.Fatalf("no zone %s/%s in %+v", region, zone, ov.Regions)
	return store.CapacityZoneView{}
}

func poolOf(t *testing.T, z store.CapacityZoneView, name string) store.CapacityPoolView {
	t.Helper()
	for _, p := range z.Pools {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("zone %s: no pool %s in %+v", z.Code, name, z.Pools)
	return store.CapacityPoolView{}
}

func resOf(t *testing.T, p store.CapacityPoolView, resource string) store.CapacityResourceView {
	t.Helper()
	for _, r := range p.Resources {
		if r.Resource == resource {
			return r
		}
	}
	t.Fatalf("pool %s: no resource %s", p.Name, resource)
	return store.CapacityResourceView{}
}

// days renders a *float64 day count for a failure message: the VALUE, never
// the pointer — a message that reads "0xc00021e518" says nothing.
func days(p *float64) any {
	if p == nil {
		return "nil"
	}
	return *p
}

func capDec(t *testing.T, name string, got store.Decimal, want string) {
	t.Helper()
	if string(got) != want {
		t.Errorf("%s = %s, want %s", name, got, want)
	}
}

// A pool is a NAMED SET OF MACHINES: it is created whole, several pools of
// the same resource kind live in one zone, a zone is born with NO pools, and
// every size change writes one history row per resource.
func TestIntegrationCapacityPoolsAreNamedSetsOfMachines(t *testing.T) {
	st := testdb.Open(t)
	s := seedCapacity(t, st)
	ctx := context.Background()

	if s.region.Code != "me-east-215" || s.region.Name != "Muscat" || s.region.CloudSourceKind != store.SourceKindHuaweiProject {
		t.Fatalf("region = %+v", s.region)
	}
	if !s.zoneA.IsDefault || s.zoneB.IsDefault {
		t.Fatalf("first zone must be the default: a=%v b=%v", s.zoneA.IsDefault, s.zoneB.IsDefault)
	}
	// A ZONE IS BORN EMPTY. The old model made seven pools per zone whether
	// or not anybody had bought a machine, which is what made "unset" read
	// like a defect instead of a fact.
	fresh, err := st.CreateCapacityZone(ctx, s.region.ID, "me-east-215z", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh.Pools) != 0 {
		t.Fatalf("a new zone must hold no pools, got %+v", fresh.Pools)
	}

	if string(s.poolA.Machines) != "10.000000" || s.poolA.LeadTimeDays != 45 || s.poolA.Note != "batch one" || len(s.poolA.Resources) != 2 {
		t.Fatalf("pool m7n-a = %+v", s.poolA)
	}
	cpu := s.poolA.Resources[0]
	if cpu.Resource != capacity.ResourceVCPU || string(cpu.PerMachine) != "64.000000" || string(cpu.Reserve) != "64.000000" || string(cpu.OvercommitRatio) != "4.000000" || cpu.Label != "vCPU" || cpu.Unit != "vCPU" {
		t.Fatalf("m7n-a vcpu = %+v", cpu)
	}

	// SEVERAL POOLS OF THE SAME RESOURCE KIND COEXIST IN ONE ZONE. This is
	// the constraint the old UNIQUE (zone_id, family) forbade outright.
	second, err := st.CreateCapacityPool(ctx, s.zoneA.ID, store.CapacityPoolInput{
		Name: "m7n-b", Machines: "5",
		Resources: []store.CapacityPoolResource{
			{Resource: capacity.ResourceVCPU, PerMachine: "64", OvercommitRatio: "4"},
			{Resource: capacity.ResourceMemoryGiB, PerMachine: "512", OvercommitRatio: "1"},
		},
	}, "ops@nc.example")
	if err != nil {
		t.Fatalf("a second vCPU pool in the same zone must be allowed: %v", err)
	}
	pools, err := st.ListCapacityPools(ctx, s.zoneA.ID)
	if err != nil || len(pools) != 2 {
		t.Fatalf("zone a pools = %+v, %v", pools, err)
	}
	// The NAME is what is unique in a zone, not the resource kind.
	if _, err := st.CreateCapacityPool(ctx, s.zoneA.ID, store.CapacityPoolInput{
		Name: "M7N-A", Machines: "1", Resources: []store.CapacityPoolResource{{Resource: capacity.ResourceVCPU, PerMachine: "8"}},
	}, "x"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("a duplicate pool name = %v, want conflict", err)
	}

	// Resizing: "add two servers" is one field, and every resource moves with
	// it because they are in the same chassis.
	grown, prev, err := st.SetCapacityPool(ctx, s.poolA.ID, store.CapacityPoolInput{
		Name: "m7n-a", Machines: "12", LeadTimeDays: 45, Note: "two more hosts",
		Resources: []store.CapacityPoolResource{
			{Resource: capacity.ResourceVCPU, PerMachine: "64", Reserve: "64", OvercommitRatio: "4"},
			{Resource: capacity.ResourceMemoryGiB, PerMachine: "512", Reserve: "512", OvercommitRatio: "1.5"},
		},
	}, "ops@nc.example")
	if err != nil {
		t.Fatal(err)
	}
	if string(prev.Machines) != "10.000000" || string(grown.Machines) != "12.000000" || grown.Note != "two more hosts" {
		t.Fatalf("resize = %+v (was %+v)", grown, prev)
	}
	if string(resOfPool(t, grown, capacity.ResourceMemoryGiB).OvercommitRatio) != "1.500000" {
		t.Fatalf("the RAM ratio must be per (pool, resource): %+v", grown.Resources)
	}
	hist, err := st.ListCapacityPoolHistory(ctx, s.poolA.ID, 0)
	if err != nil || len(hist) != 4 {
		t.Fatalf("history = %d rows, want 4 (two resources at create, two at resize): %+v %v", len(hist), hist, err)
	}
	byRes := map[string][]store.CapacityPoolChange{}
	for _, h := range hist {
		byRes[h.Resource] = append(byRes[h.Resource], h)
	}
	if got := byRes[capacity.ResourceVCPU]; len(got) != 2 || string(got[0].Total) != "768.000000" || string(got[1].Total) != "640.000000" {
		t.Fatalf("vcpu history (newest first) = %+v", got)
	}

	// Refusals.
	bad := []struct {
		name string
		in   store.CapacityPoolInput
	}{
		{"no name", store.CapacityPoolInput{Machines: "1", Resources: []store.CapacityPoolResource{{Resource: "vcpu", PerMachine: "1"}}}},
		{"no resources", store.CapacityPoolInput{Name: "x", Machines: "1"}},
		{"negative machines", store.CapacityPoolInput{Name: "x", Machines: "-1", Resources: []store.CapacityPoolResource{{Resource: "vcpu", PerMachine: "1"}}}},
		{"machines not a number", store.CapacityPoolInput{Name: "x", Machines: "lots", Resources: []store.CapacityPoolResource{{Resource: "vcpu", PerMachine: "1"}}}},
		{"ratio zero", store.CapacityPoolInput{Name: "x", Machines: "1", Resources: []store.CapacityPoolResource{{Resource: "vcpu", PerMachine: "1", OvercommitRatio: "0"}}}},
		{"resource twice", store.CapacityPoolInput{Name: "x", Machines: "1", Resources: []store.CapacityPoolResource{{Resource: "vcpu", PerMachine: "1"}, {Resource: "VCPU", PerMachine: "2"}}}},
		{"resource with no key", store.CapacityPoolInput{Name: "x", Machines: "1", Resources: []store.CapacityPoolResource{{PerMachine: "1"}}}},
	}
	for _, c := range bad {
		if _, err := st.CreateCapacityPool(ctx, s.zoneA.ID, c.in, "x"); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("%s = %v, want invalid", c.name, err)
		}
	}
	if _, err := st.CreateCapacityPool(ctx, "00000000-0000-0000-0000-000000000000", store.CapacityPoolInput{
		Name: "x", Machines: "1", Resources: []store.CapacityPoolResource{{Resource: "vcpu", PerMachine: "1"}},
	}, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown zone = %v", err)
	}

	// Deleting the pool takes its resources, placements and history; deleting
	// the default zone promotes the next; deleting the region cascades.
	if err := st.DeleteCapacityPool(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteCapacityZone(ctx, s.zoneA.ID); err != nil {
		t.Fatal(err)
	}
	zb, err := st.GetCapacityZone(ctx, s.zoneB.ID)
	if err != nil || !zb.IsDefault {
		t.Fatalf("zone b after deleting a = %+v, %v", zb, err)
	}
	if err := st.DeleteCapacityRegion(ctx, s.region.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"capacity_pools", "capacity_pool_resources", "capacity_placements", "capacity_pool_history"} {
		var n int
		if err := st.DB().QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s after cascade = %d, %v", table, n, err)
		}
	}
}

func resOfPool(t *testing.T, p store.CapacityPool, resource string) store.CapacityPoolResource {
	t.Helper()
	for _, r := range p.Resources {
		if r.Resource == resource {
			return r
		}
	}
	t.Fatalf("pool %s: no resource %s", p.Name, resource)
	return store.CapacityPoolResource{}
}

// The worked example, derived end to end from the usage ledger: the vector,
// the class split, sellable, the binding resource, the stranded vCPU, the
// spot that must be reclaimed, and the basket that answers 0.
func TestIntegrationCapacityOverviewIsTheWorkedExample(t *testing.T) {
	st := testdb.Open(t)
	s := seedCapacity(t, st)
	ctx := context.Background()

	ov, err := st.CapacityOverview(ctx, s.now, "", growthLikeAPI)
	if err != nil {
		t.Fatal(err)
	}
	if ov.AsOf == nil || !ov.AsOf.Equal(day(2026, 9, 9).Add(9*time.Hour)) {
		t.Fatalf("as_of = %v, want 2026-09-09 09:00Z (the hour in progress is ignored)", ov.AsOf)
	}
	if ov.Sources != 2 || ov.LaggingSources != 0 {
		t.Fatalf("sources = %d lagging %d, want 2 / 0 (the platform source is not cloud)", ov.Sources, ov.LaggingSources)
	}
	if len(ov.Classes) != 3 || len(ov.ResourceKinds) < 8 {
		t.Fatalf("classes = %d, kinds = %d (7 seeded + gpu_cards)", len(ov.Classes), len(ov.ResourceKinds))
	}

	a := zoneOf(t, ov, "me-east-215", "me-east-215a")
	p := poolOf(t, a, "m7n-a")
	cpu, ram := resOf(t, p, capacity.ResourceVCPU), resOf(t, p, capacity.ResourceMemoryGiB)

	// The hardware.
	capDec(t, "vcpu raw", cpu.Raw, "640.000000")
	capDec(t, "vcpu usable", cpu.Usable, "576.000000")
	capDec(t, "ram usable", ram.Usable, "4608.000000")

	// The class split, derived from three SKUs on three placements.
	capDec(t, "vcpu guaranteed", cpu.Guaranteed, "320.000000")
	capDec(t, "vcpu burstable", cpu.Burstable, "256.000000")
	capDec(t, "vcpu burstable physical", cpu.BurstablePhysical, "64.000000")
	capDec(t, "vcpu spot", cpu.Spot, "32.000000")
	capDec(t, "ram guaranteed", ram.Guaranteed, "2560.000000")
	capDec(t, "ram burstable", ram.Burstable, "2048.000000")
	capDec(t, "ram spot", ram.Spot, "64.000000")

	// sellable = G + (usable − G) × ratio.
	capDec(t, "vcpu sellable", cpu.Sellable, "1344.000000")
	capDec(t, "ram sellable", ram.Sellable, "4608.000000")
	capDec(t, "vcpu physical used", cpu.PhysicalUsed, "384.000000")
	capDec(t, "vcpu physical free", cpu.PhysicalFree, "192.000000")
	capDec(t, "ram physical free", ram.PhysicalFree, "0.000000")

	// RAM BINDS AND THE POOL IS FULL; the 192 free vCPU are STRANDED.
	if p.Binding != capacity.ResourceMemoryGiB {
		t.Fatalf("binding resource = %q, want memory_gib", p.Binding)
	}
	if !cpu.Stranded || ram.Stranded {
		t.Fatalf("stranded: vcpu=%v ram=%v — free vCPU behind a bound RAM is stranded, and the binding resource itself never is", cpu.Stranded, ram.Stranded)
	}
	if p.Status != capacity.StatusCritical || p.UtilisationPct == nil || *p.UtilisationPct != 100 {
		t.Fatalf("pool status = %s at %v %%, want critical at 100 (the binding resource's)", p.Status, p.UtilisationPct)
	}
	// Spot never refused anything, and 64 GiB of it is now reclaimable.
	capDec(t, "ram spot room", ram.SpotRoom, "0.000000")
	capDec(t, "ram spot to reclaim", ram.SpotReclaim, "64.000000")
	capDec(t, "vcpu spot to reclaim", cpu.SpotReclaim, "0.000000")
	if ov.Summary.SpotToReclaim != 1 {
		t.Fatalf("spot_to_reclaim = %d, want 1", ov.Summary.SpotToReclaim)
	}

	// The zone of vm-s was never recorded, so its usage landed in the default
	// zone and the pool says so rather than pretending it was measured.
	if !p.ZoneUnknown {
		t.Fatal("a pool fed by usage with no availability zone must say so")
	}

	// The basket: the mix currently selling, scaled to its largest line.
	// Nothing more fits, and the binding resource is named.
	if p.Basket.Units == nil || *p.Basket.Units != 0 || p.Basket.Binding != capacity.ResourceMemoryGiB {
		t.Fatalf("basket = %v on %q, want 0 on memory_gib: %+v", p.Basket.Units, p.Basket.Binding, p.Basket)
	}
	if len(p.Basket.Items) != 3 {
		t.Fatalf("the default basket is the mix selling: %+v", p.Basket.Items)
	}
	for _, it := range p.Basket.Items {
		if it.SKU == skuGuaranteed && string(it.Units) != "1.000000" {
			t.Fatalf("the largest line of the default basket is one unit: %+v", it)
		}
		if it.SKU == skuBurstable && string(it.Units) != "0.800000" { // 32 of 40
			t.Fatalf("the mix keeps its proportions: %+v", it)
		}
	}

	// The placements, with what each is consuming here.
	if len(p.Placements) != 3 {
		t.Fatalf("placements = %+v", p.Placements)
	}
	for _, pl := range p.Placements {
		switch pl.SKU {
		case skuGuaranteed:
			if pl.Class != capacity.ClassGuaranteed || string(pl.Units) != "40.000000" || pl.ShapeSource != capacity.SourceSeed {
				t.Fatalf("guaranteed placement = %+v", pl)
			}
		case skuBurstable:
			if pl.Class != capacity.ClassBurstable || string(pl.Units) != "32.000000" || pl.ShapeSource != capacity.SourceManual {
				t.Fatalf("burstable placement = %+v", pl)
			}
		case skuSpot:
			if pl.Class != capacity.ClassSpot || string(pl.Units) != "4.000000" {
				t.Fatalf("spot placement = %+v", pl)
			}
		}
	}
}

// The trend view: the series is SPLIT BY CLASS, both walls are projected from
// the classes that reach them, and the ORDER-BY DATE is the wall minus the
// procurement lead time — the date an alert must actually fire on.
func TestIntegrationCapacityWallsAndOrderByDate(t *testing.T) {
	st := testdb.Open(t)
	s := seedCapacity(t, st)
	ctx := context.Background()

	ov, err := st.CapacityOverview(ctx, s.now, "", growthLikeAPI)
	if err != nil {
		t.Fatal(err)
	}
	a := zoneOf(t, ov, "me-east-215", "me-east-215a")
	p := poolOf(t, a, "m7n-a")
	cpu := resOf(t, p, capacity.ResourceVCPU)

	// Three classes, three series, eight complete days each.
	if len(cpu.Series) != 3 || cpu.HistoryDays != 8 {
		t.Fatalf("series = %d classes over %d days, want 3 / 8: %+v", len(cpu.Series), cpu.HistoryDays, cpu.Series)
	}
	byClass := map[string]store.CapacityClassSeries{}
	for _, c := range cpu.Series {
		byClass[c.Class] = c
	}
	g := byClass[capacity.ClassGuaranteed]
	if g.Days[0].Day != "2026-09-01" || string(g.Days[0].Consumed) != "256.000000" || string(g.Days[7].Consumed) != "312.000000" {
		t.Fatalf("guaranteed series = %+v", g.Days)
	}
	if g.GrowthPerDay == nil || *g.GrowthPerDay < 7.999999 || *g.GrowthPerDay > 8.000001 {
		t.Fatalf("guaranteed growth = %v, want 8 vCPU/day (one 2xlarge a day)", g.GrowthPerDay)
	}
	if b := byClass[capacity.ClassBurstable]; b.GrowthPerDay == nil || *b.GrowthPerDay != 0 {
		t.Fatalf("burstable is flat: %v", b.GrowthPerDay)
	}
	// A BLENDED line would have hidden this: guaranteed is growing while
	// burstable and spot are flat, and only the first of those buys hardware.
	if sp := byClass[capacity.ClassSpot]; sp.GrowthPerDay == nil || *sp.GrowthPerDay != 0 {
		t.Fatalf("spot is flat: %v", sp.GrowthPerDay)
	}

	// soft: remaining 768 ÷ (0 + 8 × 4) = 24 days — guaranteed growth pulls
	// the soft wall in FOUR TIMES its own size, because each guaranteed vCPU
	// removes ratio × worth of oversubscribed room.
	// hard: (576 − 320) ÷ 8 = 32 days.
	if cpu.SoftWallDays == nil || *cpu.SoftWallDays != 24 || cpu.HardWallDays == nil || *cpu.HardWallDays != 32 {
		t.Fatalf("vcpu walls = soft %v / hard %v, want 24 / 32", days(cpu.SoftWallDays), days(cpu.HardWallDays))
	}
	if cpu.SoftWallDate == nil || *cpu.SoftWallDate != "2026-10-03" {
		t.Fatalf("soft wall date = %v, want 2026-10-03 (24 days after the measured hour)", cpu.SoftWallDate)
	}
	// 24 days to the wall against 45 days of lead time: the order is already
	// 21 days late, and an alert on the wall itself would fire 21 days after
	// it was too late.
	if cpu.OrderByDays == nil || *cpu.OrderByDays != -21 || !cpu.Late || cpu.OrderByWall != capacity.WallSoft {
		t.Fatalf("vcpu order-by = %v days late=%v wall=%s", days(cpu.OrderByDays), cpu.Late, cpu.OrderByWall)
	}
	if cpu.OrderByDate == nil || *cpu.OrderByDate != "2026-08-19" {
		t.Fatalf("order-by date = %v, want 2026-08-19", cpu.OrderByDate)
	}
	// The pool reports its NEAREST order-by and which resource owes it. RAM is
	// already at the wall, so it owes the order 45 days ago.
	if p.OrderByResource != capacity.ResourceMemoryGiB || p.OrderByDays == nil || *p.OrderByDays != -45 || !p.Late {
		t.Fatalf("pool order-by = %v on %q late=%v", days(p.OrderByDays), p.OrderByResource, p.Late)
	}
	// Two pools owe an order — m7n-a and blk-b. gpu-c is flat, so it has no
	// wall at all and owes nothing: a pool that is not growing must not be
	// given a date.
	if ov.Summary.PoolsToOrder != 2 || ov.Summary.PoolsOrderLate != 1 {
		t.Fatalf("summary orders = %d to order, %d late; want 2 / 1", ov.Summary.PoolsToOrder, ov.Summary.PoolsOrderLate)
	}
	if gpu := poolOf(t, zoneOf(t, ov, "me-east-215", "me-east-215c"), "gpu-c"); gpu.OrderByDays != nil {
		t.Fatalf("a flat pool has no order-by date: %v", days(gpu.OrderByDays))
	}

	// Zone b: 180 GiB growing 10 a day against 1,000 usable → 82 days, and
	// with 10 days' lead time the order goes in on day 72.
	b := zoneOf(t, ov, "me-east-215", "me-east-215b")
	ssd := resOf(t, poolOf(t, b, "blk-b"), capacity.ResourceBlockSSD)
	capDec(t, "ssd guaranteed", ssd.Guaranteed, "180.000000")
	capDec(t, "ssd remaining", ssd.Remaining, "820.000000")
	if ssd.Status != capacity.StatusOK {
		t.Fatalf("18 %% used is ok, got %s", ssd.Status)
	}
	if ssd.SoftWallDays == nil || *ssd.SoftWallDays != 82 {
		t.Fatalf("ssd soft wall = %v, want 82 days", days(ssd.SoftWallDays))
	}
	if ssd.OrderByDays == nil || *ssd.OrderByDays != 72 || ssd.Late {
		t.Fatalf("ssd order-by = %v days, late=%v; want 72 and not late", days(ssd.OrderByDays), ssd.Late)
	}
	if ssd.OrderByDate == nil || *ssd.OrderByDate != "2026-11-20" {
		t.Fatalf("ssd order-by date = %v, want 2026-11-20", ssd.OrderByDate)
	}
}

// Everything the derivation must REFUSE to attribute, listed by name rather
// than summed away: a SKU with no shape, a SKU no pool takes, a resource of a
// placed SKU that the pool does not hold, usage in a region nobody
// configured, a metric that is not a meter, and the platform layer.
func TestIntegrationCapacityUnplacedAndUnshapedAreNamed(t *testing.T) {
	st := testdb.Open(t)
	s := seedCapacity(t, st)
	ctx := context.Background()

	ov, err := st.CapacityOverview(ctx, s.now, "", growthLikeAPI)
	if err != nil {
		t.Fatal(err)
	}

	// nat.1 has no shape, stored or derived: it counts against nothing, and
	// the page names it instead of quietly losing it.
	if len(ov.UnshapedSKUs) != 1 || ov.UnshapedSKUs[0].SKU != "nat.1" || ov.UnshapedSKUs[0].Resources != 1 {
		t.Fatalf("unshaped = %+v, want nat.1 only", ov.UnshapedSKUs)
	}
	if ov.UnshapedSKUs[0].Regions[0] != "me-east-215" {
		t.Fatalf("unshaped regions = %+v", ov.UnshapedSKUs[0].Regions)
	}

	// eip and eip.bandwidth_mbps are metered in zone b, have shapes, and no
	// pool there holds their resources.
	b := zoneOf(t, ov, "me-east-215", "me-east-215b")
	got := map[string]store.CapacityUnplacedSKU{}
	for _, u := range b.UnplacedSKUs {
		got[u.SKU] = u
	}
	if len(got) != 2 || got["eip"].Reason != "no-placement" || got["eip.bandwidth_mbps"].Reason != "no-placement" {
		t.Fatalf("zone b unplaced = %+v", b.UnplacedSKUs)
	}
	if string(got["eip.bandwidth_mbps"].Units) != "10.000000" {
		t.Fatalf("an unplaced SKU carries its quantity: %+v", got["eip.bandwidth_mbps"])
	}

	// The GPU flavour IS placed, on a pool that holds its cards but not its
	// vCPU: the cards land and the vCPU is reported as unplaced against that
	// resource, by name.
	c := zoneOf(t, ov, "me-east-215", "me-east-215c")
	gpu := poolOf(t, c, "gpu-c")
	capDec(t, "gpu cards guaranteed", resOf(t, gpu, resGPU).Guaranteed, "3.000000")
	if len(c.UnplacedSKUs) != 1 || c.UnplacedSKUs[0].Reason != "resource-unplaced" || c.UnplacedSKUs[0].Resource != capacity.ResourceVCPU {
		t.Fatalf("zone c unplaced = %+v", c.UnplacedSKUs)
	}
	if string(c.UnplacedSKUs[0].Units) != "24.000000" { // 3 instances × 8 vCPU
		t.Fatalf("the unplaced amount is in the resource's units: %+v", c.UnplacedSKUs[0])
	}
	// gpu_cards is a resource kind NOBODY SEEDED, and it works exactly like
	// the seeded ones: that is what "resource kinds are data" buys.
	kinds := map[string]bool{}
	for _, k := range ov.ResourceKinds {
		kinds[k.Key] = true
	}
	if !kinds[resGPU] {
		t.Fatalf("a pool's own resource kind must appear in the catalogue: %+v", ov.ResourceKinds)
	}

	// A region nobody added; a metric that is not a meter; the platform layer.
	if len(ov.UnmappedRegion) != 1 || ov.UnmappedRegion[0].Region != "eu-west-101" || ov.UnmappedRegion[0].Reason != "no-region" {
		t.Fatalf("unmapped regions = %+v", ov.UnmappedRegion)
	}
	for _, u := range ov.UnshapedSKUs {
		if u.SKU == store.SKUCPUUtil || u.SKU == store.SKUVCPU {
			t.Fatalf("%s is not a meter and must never reach capacity at all", u.SKU)
		}
	}

	// The region filter narrows what is listed but never what is attributed.
	filtered, err := st.CapacityOverview(ctx, s.now, "ME-EAST-215", growthLikeAPI)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Regions) != 1 || len(filtered.UnmappedRegion) != 1 {
		t.Fatalf("filtered = %d regions, %d unmapped", len(filtered.Regions), len(filtered.UnmappedRegion))
	}
}

// A pool nobody has sized NEVER reads ok, and a basket with no sized resource
// SAYS so instead of showing a number. This is the defect class this module
// already shipped once: a figure read from a structurally empty field renders
// as good news.
func TestIntegrationCapacityUnsizedNeverReadsOK(t *testing.T) {
	st := testdb.Open(t)
	s := seedCapacity(t, st)
	ctx := context.Background()

	empty, err := st.CreateCapacityPool(ctx, s.zoneB.ID, store.CapacityPoolInput{
		Name: "planned-c", Machines: "0",
		Resources: []store.CapacityPoolResource{{Resource: capacity.ResourceBlockSSD, PerMachine: "0", OvercommitRatio: "1"}},
	}, "ops@nc.example")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCapacityPlacement(ctx, empty.ID, "evs.ssd.gb", capacity.ClassGuaranteed, "ops@nc.example"); err != nil {
		t.Fatal(err)
	}
	ov, err := st.CapacityOverview(ctx, s.now, "", growthLikeAPI)
	if err != nil {
		t.Fatal(err)
	}
	b := zoneOf(t, ov, "me-east-215", "me-east-215b")
	p := poolOf(t, b, "planned-c")
	r := resOf(t, p, capacity.ResourceBlockSSD)
	if r.Sized || r.Status != capacity.StatusUnset || r.UtilisationPct != nil {
		t.Fatalf("an unsized resource = %+v, want unset with no percentage", r)
	}
	if p.Status != capacity.StatusUnset || p.UtilisationPct != nil || p.Binding != "" {
		t.Fatalf("an unsized pool = status %s, %v %%, binding %q — it must never read ok", p.Status, p.UtilisationPct, p.Binding)
	}
	if r.SoftWallDays != nil || r.HardWallDays != nil || r.OrderByDays != nil {
		t.Fatalf("an unsized resource has no wall and no order-by: %+v", r)
	}
	// The basket must SAY why, not print 0.
	if p.Basket.Units != nil {
		t.Fatalf("a basket with no sized resource must report nothing, got %v", *p.Basket.Units)
	}
	if p.Basket.Reason == "" {
		t.Fatal("a basket that cannot be measured must say why in words")
	}
	if ov.Summary.PoolsSized != 3 {
		t.Fatalf("pools_sized = %d, want 3 of 4", ov.Summary.PoolsSized)
	}
}

// A basket the operator names: how many more of THAT mix fit, with the
// binding resource — never a per-SKU maximum per resource, which is the
// answer that silently assumes everything else sells zero.
func TestIntegrationCapacityNamedBasket(t *testing.T) {
	st := testdb.Open(t)
	s := seedCapacity(t, st)
	ctx := context.Background()

	// blk-b: 1,000 GiB usable, 180 sold guaranteed, so 820 left and a 1 GiB
	// shape — 820 more.
	pv, err := st.CapacityPoolBasket(ctx, s.poolB.ID, s.now, store.ParseBasket("evs.ssd.gb:1"), growthLikeAPI)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Basket.Units == nil || *pv.Basket.Units != 820 || pv.Basket.Binding != capacity.ResourceBlockSSD {
		t.Fatalf("basket = %v on %q, want 820 on block_ssd_gib", pv.Basket.Units, pv.Basket.Binding)
	}
	// Ten at a time: 82 baskets, and the per-basket cost is on the wire.
	pv, err = st.CapacityPoolBasket(ctx, s.poolB.ID, s.now, store.ParseBasket("evs.ssd.gb:10"), growthLikeAPI)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Basket.Units == nil || *pv.Basket.Units != 82 {
		t.Fatalf("ten-at-a-time basket = %v, want 82", pv.Basket.Units)
	}
	if len(pv.Basket.Resources) != 1 || string(pv.Basket.Resources[0].PerBasket) != "10.000000" {
		t.Fatalf("basket working = %+v", pv.Basket.Resources)
	}

	// A SKU with no shape cannot be priced into a basket, and the basket says
	// which one rather than quietly leaving it out of the arithmetic.
	pv, err = st.CapacityPoolBasket(ctx, s.poolB.ID, s.now, store.ParseBasket("nat.1:1"), growthLikeAPI)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Basket.Units != nil || len(pv.Basket.UnshapedSKUs) != 1 || pv.Basket.UnshapedSKUs[0] != "nat.1" {
		t.Fatalf("an unshaped basket = %+v", pv.Basket)
	}

	// On m7n-a nothing more fits, of any mix that needs RAM.
	pv, err = st.CapacityPoolBasket(ctx, s.poolA.ID, s.now, store.ParseBasket(skuGuaranteed), growthLikeAPI)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Basket.Units == nil || *pv.Basket.Units != 0 || pv.Basket.Binding != capacity.ResourceMemoryGiB {
		t.Fatalf("one more 2xlarge on a full pool = %v on %q", pv.Basket.Units, pv.Basket.Binding)
	}

	if _, err := st.CapacityPoolBasket(ctx, "00000000-0000-0000-0000-000000000000", s.now, nil, growthLikeAPI); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown pool = %v", err)
	}
}

// Two pools of the same resource kind in one zone, which the old model could
// not express at all. A SKU placed on both is attributed BY POOL SIZE: BSS
// does not know which machine an instance landed on, and pool size is the
// only weighting the operator's own data supports.
func TestIntegrationCapacityTwoPoolsOfTheSameKindSplitBySize(t *testing.T) {
	st := testdb.Open(t)
	s := seedCapacity(t, st)
	ctx := context.Background()

	// Zone c holds only GPU cards so far. Give it two vCPU pools, 2:1 in size.
	big, err := st.CreateCapacityPool(ctx, s.zoneC.ID, store.CapacityPoolInput{
		Name: "cpu-big", Machines: "2",
		Resources: []store.CapacityPoolResource{{Resource: capacity.ResourceVCPU, PerMachine: "64", OvercommitRatio: "1"}},
	}, "ops@nc.example")
	if err != nil {
		t.Fatal(err)
	}
	small, err := st.CreateCapacityPool(ctx, s.zoneC.ID, store.CapacityPoolInput{
		Name: "cpu-small", Machines: "1",
		Resources: []store.CapacityPoolResource{{Resource: capacity.ResourceVCPU, PerMachine: "64", OvercommitRatio: "1"}},
	}, "ops@nc.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{big.ID, small.ID} {
		if _, err := st.PutCapacityPlacement(ctx, id, skuGPU, capacity.ClassGuaranteed, "ops@nc.example"); err != nil {
			t.Fatal(err)
		}
	}
	ov, err := st.CapacityOverview(ctx, s.now, "", growthLikeAPI)
	if err != nil {
		t.Fatal(err)
	}
	c := zoneOf(t, ov, "me-east-215", "me-east-215c")
	// 3 instances × 8 vCPU = 24, split 128:64 usable → 16 and 8.
	capDec(t, "cpu-big vcpu", resOf(t, poolOf(t, c, "cpu-big"), capacity.ResourceVCPU).Guaranteed, "16.000000")
	capDec(t, "cpu-small vcpu", resOf(t, poolOf(t, c, "cpu-small"), capacity.ResourceVCPU).Guaranteed, "8.000000")
	// The cards still land whole on the one pool that holds them.
	capDec(t, "gpu cards", resOf(t, poolOf(t, c, "gpu-c"), resGPU).Guaranteed, "3.000000")
	// And nothing is unplaced any more: every resource of the shape has a home.
	if len(c.UnplacedSKUs) != 0 {
		t.Fatalf("zone c unplaced = %+v", c.UnplacedSKUs)
	}
}

// Shapes and placements: PUT semantics on a shape, and the two refusals that
// would otherwise read as a silent zero.
func TestIntegrationCapacityShapesAndPlacementRefusals(t *testing.T) {
	st := testdb.Open(t)
	s := seedCapacity(t, st)
	ctx := context.Background()

	list, err := st.ListCapacityShapes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byS := map[string]store.CapacityShape{}
	for _, sh := range list {
		byS[sh.SKU] = sh
	}
	// Six seeded + the two the test wrote.
	if len(list) != 8 {
		t.Fatalf("shapes = %d SKUs, want 8: %+v", len(list), list)
	}
	if sh := byS[skuGuaranteed]; sh.Source != capacity.SourceSeed || string(sh.Resources[capacity.ResourceVCPU]) != "8.000000" || string(sh.Resources[capacity.ResourceMemoryGiB]) != "64.000000" {
		t.Fatalf("seeded m7n.2xlarge.8 = %+v", sh)
	}

	// PUT semantics: the given resources become THE shape; 0 removes one.
	sh, err := st.PutCapacityShape(ctx, skuGuaranteed, map[string]store.Decimal{
		capacity.ResourceVCPU: "8", capacity.ResourceMemoryGiB: "64", capacity.ResourceBlockSSD: "40", capacity.ResourceEIP: "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sh.Source != capacity.SourceManual || len(sh.Resources) != 3 || string(sh.Resources[capacity.ResourceBlockSSD]) != "40" {
		t.Fatalf("put = %+v", sh)
	}
	if _, err := st.PutCapacityShape(ctx, "x", map[string]store.Decimal{capacity.ResourceVCPU: "-1"}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("negative amount = %v", err)
	}
	if _, err := st.PutCapacityShape(ctx, " ", map[string]store.Decimal{}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("empty sku = %v", err)
	}
	// A resource nobody seeded is a perfectly good resource: no enum refuses it.
	if _, err := st.PutCapacityShape(ctx, "x", map[string]store.Decimal{"fpga_slots": "2"}); err != nil {
		t.Fatalf("an operator's own resource kind must be accepted: %v", err)
	}
	if _, err := st.PutCapacityShape(ctx, "eip", map[string]store.Decimal{}); err != nil {
		t.Fatal(err)
	}

	// Placement refusals.
	// 1. The shape and the pool share no resource: it would consume nothing.
	if _, err := st.PutCapacityPlacement(ctx, s.poolB.ID, "eip", capacity.ClassGuaranteed, "x"); err == nil || !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("placing an EIP on a block-storage pool = %v, want invalid", err)
	}
	// 2. A SKU with no shape says nothing about what it consumes.
	if _, err := st.PutCapacityPlacement(ctx, s.poolB.ID, "nat.1", capacity.ClassGuaranteed, "x"); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("placing an unshaped SKU = %v, want invalid", err)
	}
	// 3. A SKU is ONE product at ONE price and carries ONE class.
	second, err := st.CreateCapacityPool(ctx, s.zoneA.ID, store.CapacityPoolInput{
		Name: "m7n-b", Machines: "5",
		Resources: []store.CapacityPoolResource{{Resource: capacity.ResourceVCPU, PerMachine: "64", OvercommitRatio: "4"}},
	}, "ops@nc.example")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCapacityPlacement(ctx, second.ID, skuGuaranteed, capacity.ClassSpot, "x"); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("the same SKU at two classes = %v, want invalid", err)
	}
	if _, err := st.PutCapacityPlacement(ctx, second.ID, skuGuaranteed, capacity.ClassGuaranteed, "x"); err != nil {
		t.Fatalf("the same SKU on a second pool at its own class must be allowed: %v", err)
	}
	if _, err := st.PutCapacityPlacement(ctx, s.poolA.ID, skuGuaranteed, "reserved", "x"); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("an unknown class = %v, want invalid", err)
	}

	pls, err := st.ListCapacityPlacements(ctx)
	if err != nil || len(pls) != 6 {
		t.Fatalf("placements = %d, want 6: %v", len(pls), err)
	}
	if err := st.DeleteCapacityPlacement(ctx, second.ID, skuGuaranteed); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteCapacityPlacement(ctx, second.ID, skuGuaranteed); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("removing a placement twice = %v", err)
	}

	// Resource kinds carry words, and an operator can supply them.
	k, err := st.PutCapacityResourceKind(ctx, " FPGA_slots ", "FPGA slots", "slots")
	if err != nil || k.Key != "fpga_slots" || k.Label != "FPGA slots" || k.Unit != "slots" {
		t.Fatalf("resource kind = %+v, %v", k, err)
	}
	if _, err := st.PutCapacityResourceKind(ctx, "  ", "x", "y"); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("an empty resource key = %v", err)
	}
}

// The migration is positional and comes after the first capacity migration.
func TestIntegrationCapacityPoolsMigrationVersion(t *testing.T) {
	st := testdb.Open(t)
	var n int
	if err := st.DB().QueryRowContext(context.Background(), `SELECT max(version) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if store.MigrationCapacityPools > n {
		t.Fatalf("MigrationCapacityPools = %d is beyond the applied version %d", store.MigrationCapacityPools, n)
	}
	if store.MigrationCapacityPools <= store.MigrationCapacity {
		t.Fatalf("the pool migration (%d) must come after the first capacity migration (%d)", store.MigrationCapacityPools, store.MigrationCapacity)
	}
	var applied bool
	if err := st.DB().QueryRowContext(context.Background(), `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, store.MigrationCapacityPools).Scan(&applied); err != nil || !applied {
		t.Fatalf("version %d is not recorded as applied: %v", store.MigrationCapacityPools, err)
	}
	// The retired table is gone and the renamed one is here.
	for _, q := range []struct {
		table string
		want  bool
	}{{"sku_caps", false}, {"sku_footprints", false}, {"sku_shapes", true}, {"capacity_placements", true}, {"capacity_pool_resources", true}, {"capacity_resource_kinds", true}} {
		var exists bool
		if err := st.DB().QueryRowContext(context.Background(), `SELECT to_regclass($1) IS NOT NULL`, q.table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != q.want {
			t.Errorf("table %s exists = %v, want %v", q.table, exists, q.want)
		}
	}
}
