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

// Capacity management (DESIGN.md §11). The seed below is built so every
// derived number is exact and every rule has a control:
//
//	now = 2026-09-09 10:30Z → the latest complete hour is 09:00 (a record at
//	10:00, the hour in progress, is written and must be ignored).
//
//	region me-east-215, zones a (default) and b; region eu-west-101 is NOT
//	configured (its usage lands in unmapped_regions).
//
//	vm-1   ecs.m7n.2xlarge.8 (8 vCPU, 64 GiB)   AZ a        all hours 09-01 .. 09-09 09:00
//	vm-2   ecs.s7n.2xlarge.2 (8 vCPU, 16 GiB)   AZ unknown  from 09-05 → default zone a, flagged zone_unknown
//	vol-1  evs.ssd.gb                            AZ b        100 GB on 09-01, +10 GB each day → 180 GB on 09-09
//	eip-1  eip + eip.bandwidth_mbps 10           AZ b
//	nat-1  nat.1                                 no footprint → unmapped_skus (control: counts against no pool)
//	ecs.cpu_util samples on vm-1                 a metric, never a meter (control)
//	k8s.vcpu on a platform source                the platform layer (control)
//	eip-eu on the eu-west-101 source             unmapped region (control)
//
// Pool totals: zone a vcpu 20 (→ 16 consumed, 80 %, warn), memory_gib 100
// (→ 80 consumed, 80 %, warn); zone b block_ssd_gib 1000 (→ 180, ok,
// growing 10/day → 82.0 days to exhaustion), bandwidth_mbps 5 (→ 10
// consumed: clamped to 0 available, overcommit 5, critical), eip_addresses
// left at 0 (unset).

type capacitySeed struct {
	region store.CapacityRegion
	zoneA  store.CapacityZone
	zoneB  store.CapacityZone
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
		{ResourceID: "vm-1", Kind: "ecs", Name: "web-1", Attrs: map[string]any{"flavor": "m7n.2xlarge.8", "availability_zone": "me-east-215a"}, SeenAt: seen},
		{ResourceID: "vm-2", Kind: "ecs", Name: "web-2", Attrs: map[string]any{"flavor": "s7n.2xlarge.2"}, SeenAt: seen},
		{ResourceID: "vol-1", Kind: "evs", Name: "data", Attrs: map[string]any{"availability_zone": "ME-EAST-215B"}, SeenAt: seen},
		{ResourceID: "eip-1", Kind: "eip", Name: "gw", Attrs: map[string]any{"availability_zone": "me-east-215b"}, SeenAt: seen},
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
			rec(src, "vm-1", "ecs", "ecs.m7n.2xlarge.8", "instance-hour", 1, at)
			rec(src, "vm-1", "ecs", store.SKUCPUUtil, "pct-hour-avg", 55, at)
			if d >= 5 {
				rec(src, "vm-2", "ecs", "ecs.s7n.2xlarge.2", "instance-hour", 1, at)
			}
			rec(src, "vol-1", "evs", "evs.ssd.gb", "gb-hour", float64(100+10*(d-1)), at)
			rec(src, "eip-1", "eip", "eip", "hour", 1, at)
			rec(src, "eip-1", "eip", "eip.bandwidth_mbps", "mbps-hour", 10, at)
			rec(src, "nat-1", "nat", "nat.1", "hour", 1, at)
			rec(eu, "eip-eu", "eip", "eip", "hour", 1, at)
			rec(plat, "acme/pod-1", "k8s-pod", store.SKUVCPU, store.UnitVCPU, 40, at)
		}
	}
	// The hour in progress.
	rec(src, "vm-1", "ecs", "ecs.m7n.2xlarge.8", "instance-hour", 1, day(2026, 9, 9).Add(10*time.Hour))
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
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
	set := func(z store.CapacityZone, fam, total string) {
		t.Helper()
		for _, p := range z.Pools {
			if p.Family == fam {
				if _, _, err := st.SetCapacityPoolTotal(ctx, p.ID, store.Decimal(total), "seed", "", "ops@nc.example"); err != nil {
					t.Fatal(err)
				}
				return
			}
		}
		t.Fatalf("zone %s has no %s pool", z.Code, fam)
	}
	set(zoneA, capacity.FamilyVCPU, "20")
	set(zoneA, capacity.FamilyMemoryGiB, "100")
	set(zoneB, capacity.FamilyBlockSSD, "1000")
	set(zoneB, capacity.FamilyBandwidth, "5")
	return capacitySeed{region: region, zoneA: zoneA, zoneB: zoneB, now: now, src: src}
}

func poolOf(t *testing.T, z store.CapacityZoneView, fam string) store.CapacityPoolView {
	t.Helper()
	for _, p := range z.Pools {
		if p.Family == fam {
			return p
		}
	}
	t.Fatalf("zone %s: no %s pool in %+v", z.Code, fam, z.Pools)
	return store.CapacityPoolView{}
}

func skuOf(t *testing.T, z store.CapacityZoneView, sku string) store.CapacitySKUView {
	t.Helper()
	for _, s := range z.SKUs {
		if s.SKU == sku {
			return s
		}
	}
	t.Fatalf("zone %s: no sku %s", z.Code, sku)
	return store.CapacitySKUView{}
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

// TestIntegrationCapacityRegionsZonesPools: codes are normalised, the first
// zone is the region's default, a zone is born with the seven pools at 0,
// a total change writes history and reports the previous total, and a bad
// total is refused.
func TestIntegrationCapacityRegionsZonesPools(t *testing.T) {
	st := testdb.Open(t)
	s := seedCapacity(t, st)
	ctx := context.Background()

	if s.region.Code != "me-east-215" || s.region.Name != "Muscat" || s.region.CloudSourceKind != store.SourceKindHuaweiProject {
		t.Fatalf("region = %+v", s.region)
	}
	if !s.zoneA.IsDefault || s.zoneB.IsDefault {
		t.Fatalf("first zone must be the default: a=%v b=%v", s.zoneA.IsDefault, s.zoneB.IsDefault)
	}
	if len(s.zoneA.Pools) != len(capacity.Families) {
		t.Fatalf("zone a has %d pools, want %d", len(s.zoneA.Pools), len(capacity.Families))
	}
	for i, p := range s.zoneA.Pools {
		if p.Family != capacity.Families[i].Key || string(p.Total) != "0.000000" || string(p.Reserved) != "0.000000" || p.Source != capacity.SourceManual {
			t.Fatalf("pool %d = %+v", i, p)
		}
	}
	if _, err := st.CreateCapacityRegion(ctx, "me-east-215", "", ""); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate region = %v, want conflict", err)
	}
	if _, err := st.CreateCapacityZone(ctx, s.region.ID, "ME-EAST-215A", "", false); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate zone = %v, want conflict", err)
	}
	if _, err := st.CreateCapacityRegion(ctx, "x", "", store.SourceKindOrg); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("a platform kind cannot fill cloud capacity: %v", err)
	}
	list, err := st.ListCapacityRegions(ctx)
	if err != nil || len(list) != 1 || len(list[0].Zones) != 2 || list[0].Zones[0].Code != "me-east-215a" {
		t.Fatalf("list = %+v, %v", list, err)
	}

	// Totals: history and the previous value.
	var vcpu store.CapacityPool
	for _, p := range s.zoneA.Pools {
		if p.Family == capacity.FamilyVCPU {
			vcpu = p
		}
	}
	pool, prev, err := st.SetCapacityPoolTotal(ctx, vcpu.ID, "24", "two more hosts", "", "ops@nc.example")
	if err != nil {
		t.Fatal(err)
	}
	if string(prev) != "20.000000" || string(pool.Total) != "24.000000" || pool.Note != "two more hosts" || pool.UpdatedBy != "ops@nc.example" || pool.Source != capacity.SourceManual {
		t.Fatalf("set total = %+v prev %s", pool, prev)
	}
	hist, err := st.ListCapacityPoolHistory(ctx, vcpu.ID, 0)
	if err != nil || len(hist) != 2 || string(hist[0].Total) != "24.000000" || string(hist[1].Total) != "20.000000" || hist[0].ChangedBy != "ops@nc.example" {
		t.Fatalf("history = %+v, %v", hist, err)
	}
	for _, bad := range []string{"-1", "abc", "", "1e3"} {
		if _, _, err := st.SetCapacityPoolTotal(ctx, vcpu.ID, store.Decimal(bad), "", "", "x"); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("total %q = %v, want invalid", bad, err)
		}
	}
	if _, _, err := st.SetCapacityPoolTotal(ctx, "00000000-0000-0000-0000-000000000000", "1", "", "", "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown pool = %v", err)
	}

	// Deleting the default zone promotes the other; deleting the region
	// cascades everything.
	if err := st.DeleteCapacityZone(ctx, s.zoneA.ID); err != nil {
		t.Fatal(err)
	}
	zb, err := st.GetCapacityZone(ctx, s.zoneB.ID)
	if err != nil || !zb.IsDefault || len(zb.Pools) != len(capacity.Families) {
		t.Fatalf("zone b after deleting a = %+v, %v", zb, err)
	}
	if err := st.DeleteCapacityRegion(ctx, s.region.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCapacityZone(ctx, s.zoneB.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("zone survived its region: %v", err)
	}
	var n int
	if err := st.DB().QueryRowContext(ctx, `SELECT count(*) FROM capacity_pools`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("pools after cascade = %d, %v", n, err)
	}
}

// TestIntegrationCapacityFootprints: the seed is in place, a manual
// footprint replaces the SKU's rows (PUT semantics), an unknown family and a
// negative amount are refused, and an empty map removes the footprint.
func TestIntegrationCapacityFootprints(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	list, err := st.ListSKUFootprints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 6 {
		t.Fatalf("seeded footprints = %d SKUs, want 6: %+v", len(list), list)
	}
	byS := map[string]store.SKUFootprint{}
	for _, fp := range list {
		byS[fp.SKU] = fp
	}
	if fp := byS["ecs.m7n.2xlarge.8"]; fp.Source != capacity.SourceSeed || string(fp.Families[capacity.FamilyVCPU]) != "8.000000" || string(fp.Families[capacity.FamilyMemoryGiB]) != "64.000000" {
		t.Fatalf("seeded m7n.2xlarge.8 = %+v", fp)
	}
	if fp := byS["eip.bandwidth_mbps"]; string(fp.Families[capacity.FamilyBandwidth]) != "1.000000" {
		t.Fatalf("seeded eip.bandwidth_mbps = %+v", fp)
	}
	fp, err := st.PutSKUFootprint(ctx, "ecs.m7n.2xlarge.8", map[string]store.Decimal{capacity.FamilyVCPU: "8", capacity.FamilyMemoryGiB: "64", capacity.FamilyBlockSSD: "40", capacity.FamilyEIP: "0"})
	if err != nil {
		t.Fatal(err)
	}
	if fp.Source != capacity.SourceManual || len(fp.Families) != 3 || string(fp.Families[capacity.FamilyBlockSSD]) != "40" {
		t.Fatalf("put = %+v", fp)
	}
	list, _ = st.ListSKUFootprints(ctx)
	for _, x := range list {
		if x.SKU == "ecs.m7n.2xlarge.8" && (x.Source != capacity.SourceManual || len(x.Families) != 3) {
			t.Fatalf("stored after put = %+v", x)
		}
	}
	if _, err := st.PutSKUFootprint(ctx, "x", map[string]store.Decimal{"gpu": "1"}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("unknown family = %v", err)
	}
	if _, err := st.PutSKUFootprint(ctx, "x", map[string]store.Decimal{capacity.FamilyVCPU: "-1"}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("negative amount = %v", err)
	}
	if _, err := st.PutSKUFootprint(ctx, " ", map[string]store.Decimal{}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("empty sku = %v", err)
	}
	if _, err := st.PutSKUFootprint(ctx, "eip", map[string]store.Decimal{}); err != nil {
		t.Fatal(err)
	}
	list, _ = st.ListSKUFootprints(ctx)
	if len(list) != 5 {
		t.Fatalf("after removing eip: %d SKUs, want 5", len(list))
	}
}

// TestIntegrationCapacityOverviewDerivesConsumption is the derivation
// (DESIGN.md §11): the latest complete hour through the footprints, zone
// attribution by the inventory's availability zone with the default zone
// for the unknown, the clamp, the thresholds, headroom with its binding
// family, the cap, time to exhaustion by the run rate, and every control.
func TestIntegrationCapacityOverviewDerivesConsumption(t *testing.T) {
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
	if ov.Thresholds.WarnPct != 70 || ov.Thresholds.CriticalPct != 85 || len(ov.Families) != 7 {
		t.Fatalf("thresholds/families = %+v %d", ov.Thresholds, len(ov.Families))
	}
	if len(ov.Regions) != 1 || len(ov.Regions[0].Zones) != 2 {
		t.Fatalf("regions = %+v", ov.Regions)
	}
	a := zoneOf(t, ov, "me-east-215", "me-east-215a")
	b := zoneOf(t, ov, "me-east-215", "me-east-215b")
	if !a.IsDefault || b.IsDefault {
		t.Fatalf("default flags a=%v b=%v", a.IsDefault, b.IsDefault)
	}

	// Zone a vCPU: vm-1 (8, known zone) + vm-2 (8, unknown zone → default).
	vcpu := poolOf(t, a, capacity.FamilyVCPU)
	if string(vcpu.Total) != "20.000000" || string(vcpu.Consumed) != "16.000000" || string(vcpu.Available) != "4.000000" || string(vcpu.ZoneUnknown) != "8.000000" {
		t.Fatalf("zone a vcpu = %+v", vcpu)
	}
	if vcpu.UtilisationPct == nil || *vcpu.UtilisationPct != 80 || vcpu.Status != capacity.StatusWarn || vcpu.Clamped || string(vcpu.Reserved) != "0.000000" || vcpu.Label != "vCPU" || vcpu.Unit != "vCPU" {
		t.Fatalf("zone a vcpu derived = %+v", vcpu)
	}
	mem := poolOf(t, a, capacity.FamilyMemoryGiB)
	if string(mem.Consumed) != "80.000000" || string(mem.Available) != "20.000000" || mem.Status != capacity.StatusWarn {
		t.Fatalf("zone a memory = %+v", mem)
	}
	// vm-2 joined on the 5th: the vCPU series steps 8 → 16, so its trend is
	// positive and finite; the exact figure is the run rate's business.
	if vcpu.HistoryDays != 8 || vcpu.GrowthPerDay == nil || *vcpu.GrowthPerDay <= 0 || vcpu.ExhaustionDays == nil || *vcpu.ExhaustionDays <= 0 {
		t.Fatalf("zone a vcpu growth = %+v", vcpu)
	}
	if len(vcpu.Series) != 8 || vcpu.Series[0].Day != "2026-09-01" || string(vcpu.Series[0].Consumed) != "8.000000" || vcpu.Series[7].Day != "2026-09-08" || string(vcpu.Series[7].Consumed) != "16.000000" {
		t.Fatalf("zone a vcpu series = %+v", vcpu.Series)
	}
	// Nothing metered against these families: consumed 0, and with no
	// total the status is unset, not ok.
	for _, fam := range []string{capacity.FamilyBlockSSD, capacity.FamilyBlockHDD, capacity.FamilyObject, capacity.FamilyEIP, capacity.FamilyBandwidth} {
		p := poolOf(t, a, fam)
		if string(p.Consumed) != "0.000000" || p.Status != capacity.StatusUnset || p.UtilisationPct != nil || p.ExhaustionDays != nil {
			t.Fatalf("zone a %s = %+v", fam, p)
		}
	}

	// Zone b block SSD: 180 GB now, +10 GB a day → 820 available, 82 days.
	ssd := poolOf(t, b, capacity.FamilyBlockSSD)
	if string(ssd.Consumed) != "180.000000" || string(ssd.Available) != "820.000000" || ssd.Status != capacity.StatusOK {
		t.Fatalf("zone b ssd = %+v", ssd)
	}
	if ssd.GrowthPerDay == nil || *ssd.GrowthPerDay < 9.999999 || *ssd.GrowthPerDay > 10.000001 || ssd.ExhaustionDays == nil || *ssd.ExhaustionDays != 82 {
		t.Fatalf("zone b ssd growth = %v exhaustion = %v, want 10/day, 82.0 days", ssd.GrowthPerDay, ssd.ExhaustionDays)
	}
	// Zone b bandwidth: 10 Mbps reserved against a total of 5 → clamped.
	bw := poolOf(t, b, capacity.FamilyBandwidth)
	if string(bw.Consumed) != "10.000000" || string(bw.Available) != "0.000000" || !bw.Clamped || string(bw.Overcommit) != "5.000000" || bw.Status != capacity.StatusCritical || bw.UtilisationPct == nil || *bw.UtilisationPct != 200 {
		t.Fatalf("zone b bandwidth = %+v", bw)
	}
	// A flat series has no growth: exhaustion is not a number.
	if bw.ExhaustionDays != nil || bw.GrowthPerDay == nil || *bw.GrowthPerDay != 0 {
		t.Fatalf("flat bandwidth series must report growth 0 and no exhaustion: %+v", bw)
	}
	// Zone b EIPs: metered (1 address) but no total → unset, available 0 —
	// and NOT over-committed: nobody sized it, so there is nothing to be over.
	eip := poolOf(t, b, capacity.FamilyEIP)
	if string(eip.Consumed) != "1.000000" || eip.Status != capacity.StatusUnset || string(eip.Available) != "0.000000" || eip.Clamped || string(eip.Overcommit) != "0.000000" || eip.UtilisationPct != nil {
		t.Fatalf("zone b eip = %+v", eip)
	}

	// Headroom. Zone a: m7n.2xlarge.8 needs 8 vCPU (4 left → 0) and 64 GiB
	// (20 left → 0): 0, bound by vCPU (first in family order at the tie).
	// m7n.xlarge.8 needs 4 vCPU (→ 1) and 32 GiB (→ 0): memory binds.
	big := skuOf(t, a, "ecs.m7n.2xlarge.8")
	if big.HeadroomUnits == nil || string(*big.HeadroomUnits) != "0" || big.BindingFamily != capacity.FamilyVCPU || string(big.ConsumedUnits) != "1.000000" || big.Resources != 1 || big.FootprintSource != capacity.SourceSeed {
		t.Fatalf("zone a m7n.2xlarge.8 = %+v", big)
	}
	small := skuOf(t, a, "ecs.m7n.xlarge.8")
	if small.HeadroomUnits == nil || string(*small.HeadroomUnits) != "0" || small.BindingFamily != capacity.FamilyMemoryGiB || string(small.ConsumedUnits) != "0" {
		t.Fatalf("zone a m7n.xlarge.8 = %+v", small)
	}
	// s7n.2xlarge.2 (8 vCPU, 16 GiB): vCPU 0, memory 1 → 0 by vCPU; and its
	// one instance is the unknown-zone vm-2.
	s7 := skuOf(t, a, "ecs.s7n.2xlarge.2")
	if string(*s7.HeadroomUnits) != "0" || s7.BindingFamily != capacity.FamilyVCPU || string(s7.ConsumedUnits) != "1.000000" {
		t.Fatalf("zone a s7n.2xlarge.2 = %+v", s7)
	}
	// Zone b: 820 GiB left → 820 more GB of SSD; a family with no total
	// (eip) gives nil headroom.
	ssdSKU := skuOf(t, b, "evs.ssd.gb")
	if ssdSKU.HeadroomUnits == nil || string(*ssdSKU.HeadroomUnits) != "820" || ssdSKU.BindingFamily != capacity.FamilyBlockSSD || string(ssdSKU.ConsumedUnits) != "180.000000" || ssdSKU.Cap != nil {
		t.Fatalf("zone b evs.ssd.gb = %+v", ssdSKU)
	}
	if e := skuOf(t, b, "eip"); e.HeadroomUnits != nil || e.BindingFamily != "" {
		t.Fatalf("zone b eip headroom must be unknown without a total: %+v", e)
	}
	// Zone b m7n.2xlarge.8: no vCPU/memory totals there → nil.
	if x := skuOf(t, b, "ecs.m7n.2xlarge.8"); x.HeadroomUnits != nil {
		t.Fatalf("zone b m7n.2xlarge.8 headroom = %v, want nil (no totals)", *x.HeadroomUnits)
	}

	// Unmapped: nat.1 has no footprint (stored or derived) and counts against
	// no pool; eu-west-101 is not a configured region.
	if len(ov.UnmappedSKUs) != 1 || ov.UnmappedSKUs[0].SKU != "nat.1" || string(ov.UnmappedSKUs[0].Quantity) != "1.000000" || ov.UnmappedSKUs[0].Resources != 1 || len(ov.UnmappedSKUs[0].Regions) != 1 || ov.UnmappedSKUs[0].Regions[0] != "me-east-215" {
		t.Fatalf("unmapped skus = %+v", ov.UnmappedSKUs)
	}
	if len(ov.UnmappedRegion) != 1 || ov.UnmappedRegion[0].Region != "eu-west-101" || ov.UnmappedRegion[0].Reason != "no-region" || ov.UnmappedRegion[0].SKUs != 1 || string(ov.UnmappedRegion[0].Quantity) != "1.000000" {
		t.Fatalf("unmapped regions = %+v", ov.UnmappedRegion)
	}
	// The metric sample and the platform meter reached no pool: zone a's
	// vCPU is 16, not 16 + 40, and cpu_util is neither a SKU nor unmapped.
	for _, sk := range a.SKUs {
		if sk.SKU == store.SKUCPUUtil || sk.SKU == store.SKUVCPU {
			t.Fatalf("%s must not be listed as a capacity SKU", sk.SKU)
		}
	}
	if ov.Summary.Regions != 1 || ov.Summary.Zones != 2 || ov.Summary.Pools != 14 || ov.Summary.PoolsWithTotal != 4 || ov.Summary.PoolsWarn != 2 || ov.Summary.PoolsCritical != 1 || ov.Summary.PoolsBelowThreshold != 3 || ov.Summary.UnmappedSKUs != 1 || ov.Summary.SKUs != 6 {
		t.Fatalf("summary = %+v", ov.Summary)
	}

	// A cap below the pool headroom binds; equal or above it does not.
	if _, err := st.PutSKUCap(ctx, s.zoneB.ID, "evs.ssd.gb", "500", "ops@nc.example"); err != nil {
		t.Fatal(err)
	}
	ov, _ = st.CapacityOverview(ctx, s.now, "", growthLikeAPI)
	capped := skuOf(t, zoneOf(t, ov, "me-east-215", "me-east-215b"), "evs.ssd.gb")
	if capped.Cap == nil || string(*capped.Cap) != "500.000000" || string(*capped.HeadroomUnits) != "320" || capped.BindingFamily != "cap" {
		t.Fatalf("capped evs.ssd.gb = %+v", capped)
	}
	if _, err := st.PutSKUCap(ctx, s.zoneB.ID, "evs.ssd.gb", "5000", "ops@nc.example"); err != nil {
		t.Fatal(err)
	}
	ov, _ = st.CapacityOverview(ctx, s.now, "", growthLikeAPI)
	loose := skuOf(t, zoneOf(t, ov, "me-east-215", "me-east-215b"), "evs.ssd.gb")
	if string(*loose.HeadroomUnits) != "820" || loose.BindingFamily != capacity.FamilyBlockSSD {
		t.Fatalf("cap above the pool must not bind: %+v", loose)
	}
	caps, _ := st.ListSKUCaps(ctx)
	if len(caps) != 1 || caps[0].ZoneCode != "me-east-215b" || caps[0].RegionCode != "me-east-215" || string(caps[0].Total) != "5000.000000" {
		t.Fatalf("caps = %+v", caps)
	}
	if err := st.DeleteSKUCap(ctx, s.zoneB.ID, "evs.ssd.gb"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSKUCap(ctx, s.zoneB.ID, "evs.ssd.gb"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
	if _, err := st.PutSKUCap(ctx, "00000000-0000-0000-0000-000000000000", "x", "1", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cap on unknown zone = %v", err)
	}

	// A manual footprint for nat.1 moves it from unmapped into the pools.
	if _, err := st.PutSKUFootprint(ctx, "nat.1", map[string]store.Decimal{capacity.FamilyEIP: "1"}); err != nil {
		t.Fatal(err)
	}
	ov, _ = st.CapacityOverview(ctx, s.now, "", growthLikeAPI)
	if len(ov.UnmappedSKUs) != 0 {
		t.Fatalf("nat.1 still unmapped: %+v", ov.UnmappedSKUs)
	}
	// nat-1 has no inventory row → default zone a → its address counts there.
	if p := poolOf(t, zoneOf(t, ov, "me-east-215", "me-east-215a"), capacity.FamilyEIP); string(p.Consumed) != "1.000000" || string(p.ZoneUnknown) != "1.000000" {
		t.Fatalf("zone a eip after nat.1 footprint = %+v", p)
	}
	if n := skuOf(t, zoneOf(t, ov, "me-east-215", "me-east-215a"), "nat.1"); n.FootprintSource != capacity.SourceManual {
		t.Fatalf("nat.1 source = %+v", n)
	}

	// Region filter lists one region; the unmapped lists stay complete.
	ov, _ = st.CapacityOverview(ctx, s.now, "ME-EAST-215", growthLikeAPI)
	if len(ov.Regions) != 1 || len(ov.UnmappedRegion) != 1 {
		t.Fatalf("filtered = %d regions, %d unmapped regions", len(ov.Regions), len(ov.UnmappedRegion))
	}
	ov, _ = st.CapacityOverview(ctx, s.now, "nowhere", growthLikeAPI)
	if len(ov.Regions) != 0 || ov.Summary.Regions != 0 {
		t.Fatalf("unknown filter must list nothing: %+v", ov.Summary)
	}
	// Without a growth function nothing is projected, and nothing else changes.
	ov, _ = st.CapacityOverview(ctx, s.now, "", nil)
	if p := poolOf(t, zoneOf(t, ov, "me-east-215", "me-east-215b"), capacity.FamilyBlockSSD); p.ExhaustionDays != nil || p.GrowthPerDay != nil || string(p.Consumed) != "180.000000" || len(p.Series) != 8 {
		t.Fatalf("no growth func: %+v", p)
	}
}

// TestIntegrationCapacityOverviewEmpty: with no regions the document is
// still complete — empty lists, not nulls — and with no cloud usage as_of
// is null.
func TestIntegrationCapacityOverviewEmpty(t *testing.T) {
	st := testdb.Open(t)
	ov, err := st.CapacityOverview(context.Background(), time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC), "", growthLikeAPI)
	if err != nil {
		t.Fatal(err)
	}
	if ov.AsOf != nil || ov.Sources != 0 || len(ov.Regions) != 0 || len(ov.UnmappedSKUs) != 0 || len(ov.UnmappedRegion) != 0 {
		t.Fatalf("empty overview = %+v", ov)
	}
	// The seeded footprints are listed even before anything is metered.
	if ov.Summary.SKUs != 6 || ov.Summary.Pools != 0 {
		t.Fatalf("summary = %+v", ov.Summary)
	}
	b, _ := json.Marshal(ov)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"as_of", "sources", "lagging_sources", "thresholds", "families", "regions", "unmapped_skus", "unmapped_regions", "summary"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("overview lacks %q", k)
		}
	}
	if m["regions"] == nil || m["unmapped_skus"] == nil {
		t.Fatalf("lists must be [] not null: %s", b)
	}
}

// TestIntegrationCapacityMigrationVersion pins the capacity migration as the
// last entry and its locator.
func TestIntegrationCapacityMigrationVersion(t *testing.T) {
	st := testdb.Open(t)
	var n int
	if err := st.DB().QueryRowContext(context.Background(), `SELECT max(version) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != store.MigrationCapacity {
		t.Fatalf("applied version %d, MigrationCapacity = %d", n, store.MigrationCapacity)
	}
	if store.MigrationCapacity <= store.MigrationRoleBindings {
		t.Fatalf("capacity (%d) must come after role bindings (%d)", store.MigrationCapacity, store.MigrationRoleBindings)
	}
}
