package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/capacity"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// Classes per pool, one SKU at three classes, the guaranteed floor and the
// reclaim list (DESIGN.md §11, founder direction 2026-09-20), end to end from
// metered usage.
//
//	POOL k8s-a: 1 machine × 100 vCPU, enforces all three classes, vCPU 2.5:1,
//	guaranteed floor 60 — the founder's 60 / 40 case.
//
//	vm.one {vcpu 1} is placed on it THREE TIMES, once per class. Which class a
//	running resource counts at is said by the resource:
//	  vm-1  no tag, no override   50  → guaranteed (the conservative default)
//	  vm-2  lifecycle=burstable   50  → burstable
//	  vm-3  lifecycle=spot        30  → spot, first seen 48 h ago
//	  vm-4  lifecycle=spot        25  → spot, first seen 1 h ago (the newest)
//	  vm-5  override spot         10  → spot, first seen 24 h ago
//	vm.two {vcpu 1} is placed guaranteed ONLY:
//	  vm-6  lifecycle=spot         5  → counts guaranteed, named as a mismatch
//	fam.* is a FAMILY placed guaranteed:
//	  vm-7  fam.a.small {vcpu 2}   3  → 6 vCPU guaranteed through the family
//
//	G = 50 + 5 + 6 = 61 · B = 50 · spot = 65
//	held = max(61, 60) = 61 · envelope = 39 × 2.5 = 97.5 · sellable 158.5
//	physical used = 61 + 50/2.5 = 81 · free 19 · spot room 47.5
//	→ 17.5 of spot to reclaim, and the newest spot resource (vm-4, 25) covers it.

type classSeed struct {
	pool store.CapacityPool
	zone store.CapacityZone
	src  store.CostSource
	now  time.Time
}

func seedClasses(t *testing.T, st *store.Store) classSeed {
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
	for _, inv := range []struct {
		id  string
		age time.Duration
	}{{"vm-1", 72 * time.Hour}, {"vm-2", 72 * time.Hour}, {"vm-3", 48 * time.Hour}, {"vm-4", time.Hour}, {"vm-5", 24 * time.Hour}, {"vm-6", 72 * time.Hour}, {"vm-7", 72 * time.Hour}} {
		if _, err := st.UpsertInventory(ctx, src.ID, []store.InventoryUpsert{{ResourceID: inv.id, Kind: "ecs", Name: "host-" + inv.id, SeenAt: now.Add(-inv.age)}}); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 9, 9, 9, 0, 0, 0, time.UTC)
	var recs []store.UsageRecord
	rec := func(res, sku string, qty float64, lifecycle string) {
		labels := map[string]any{"name": res}
		if lifecycle != "" {
			labels["tags"] = map[string]any{capacity.ClassTagKey: lifecycle, "app": "shop"}
		}
		lb, _ := json.Marshal(labels)
		recs = append(recs, store.UsageRecord{CustomerID: cust.ID, SourceID: src.ID, ResourceID: res, ResourceKind: "ecs", SKU: sku,
			Quantity: store.Decimal(strconv.FormatFloat(qty, 'f', 6, 64)), Unit: "instance-hour", WindowStart: at, WindowEnd: at.Add(time.Hour), Region: src.Region, Labels: lb})
	}
	rec("vm-1", "vm.one", 50, "")
	rec("vm-2", "vm.one", 50, "Burstable")
	rec("vm-3", "vm.one", 30, "spot")
	rec("vm-4", "vm.one", 25, "spot")
	rec("vm-5", "vm.one", 10, "production") // a lifecycle tag that is not a class says nothing
	rec("vm-6", "vm.two", 5, "spot")
	rec("vm-7", "fam.a.small", 3, "")
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	for sku, vcpu := range map[string]string{"vm.one": "1", "vm.two": "1", "fam.a.small": "2"} {
		if _, err := st.PutCapacityShape(ctx, sku, map[string]store.Decimal{capacity.ResourceVCPU: store.Decimal(vcpu)}); err != nil {
			t.Fatal(err)
		}
	}
	region, err := st.CreateCapacityRegion(ctx, "me-east-215", "Muscat", "")
	if err != nil {
		t.Fatal(err)
	}
	zone, err := st.CreateCapacityZone(ctx, region.ID, "me-east-215a", "AZ 1", true)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := st.CreateCapacityPool(ctx, zone.ID, store.CapacityPoolInput{
		Name: "k8s-a", Machines: "1", Classes: allClasses,
		Resources: []store.CapacityPoolResource{{Resource: capacity.ResourceVCPU, PerMachine: "100", OvercommitRatio: "2.5", GuaranteedFloor: "60"}},
	}, "ops@nc.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, pl := range []struct{ sku, class string }{
		{"vm.one", capacity.ClassGuaranteed}, {"vm.one", capacity.ClassBurstable}, {"vm.one", capacity.ClassSpot},
		{"vm.two", capacity.ClassGuaranteed},
		{"fam.*", capacity.ClassGuaranteed},
	} {
		if _, err := st.PutCapacityPlacement(ctx, pool.ID, pl.sku, pl.class, "ops@nc.example"); err != nil {
			t.Fatalf("place %s as %s: %v", pl.sku, pl.class, err)
		}
	}
	if _, err := st.SetCapacityResourceClass(ctx, src.ID, "vm-5", capacity.ClassSpot, "ops@nc.example"); err != nil {
		t.Fatal(err)
	}
	return classSeed{pool: pool, zone: zone, src: src, now: now}
}

func TestIntegrationCapacityOneSKUAtThreeClassesWithAFloor(t *testing.T) {
	st := testdb.Open(t)
	s := seedClasses(t, st)
	ctx := context.Background()

	ov, err := st.CapacityOverview(ctx, s.now, "", growthLikeAPI)
	if err != nil {
		t.Fatal(err)
	}
	z := zoneOf(t, ov, "me-east-215", "me-east-215a")
	p := poolOf(t, z, "k8s-a")
	if !reflect.DeepEqual(p.Classes, allClasses) {
		t.Fatalf("pool classes = %v", p.Classes)
	}
	cpu := resOf(t, p, capacity.ResourceVCPU)
	capDec(t, "guaranteed", cpu.Guaranteed, "61.000000")
	capDec(t, "burstable", cpu.Burstable, "50.000000")
	capDec(t, "spot", cpu.Spot, "65.000000")
	capDec(t, "guaranteed_floor", cpu.GuaranteedFloor, "60.000000")
	capDec(t, "floor_free", cpu.FloorFree, "0.000000")
	capDec(t, "burstable_envelope", cpu.BurstableEnvelope, "97.500000")
	capDec(t, "burstable_room", cpu.BurstableRoom, "47.500000")
	capDec(t, "sellable", cpu.Sellable, "158.500000")
	capDec(t, "spot_room", cpu.SpotRoom, "47.500000")
	capDec(t, "spot_reclaim", cpu.SpotReclaim, "17.500000")

	// One SKU, three placement rows, each with ITS class's units.
	got := map[string]string{}
	for _, pl := range p.Placements {
		got[pl.SKU+"/"+pl.Class] = string(pl.Units)
		if pl.MatchedSKUs == nil {
			t.Errorf("%s: matched_skus must never be null", pl.SKU)
		}
		if pl.SKU == "fam.*" && (!pl.Family || !reflect.DeepEqual(pl.MatchedSKUs, []string{"fam.a.small"})) {
			t.Errorf("family row = %+v", pl)
		}
	}
	want := map[string]string{"vm.one/guaranteed": "50.000000", "vm.one/burstable": "50.000000", "vm.one/spot": "65.000000", "vm.two/guaranteed": "5.000000", "fam.*/guaranteed": "3.000000"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("placement units = %v, want %v", got, want)
	}

	// A tag naming a class the SKU is not placed at still COUNTS, and is named.
	if len(z.ClassMismatches) != 1 {
		t.Fatalf("class mismatches = %+v", z.ClassMismatches)
	}
	if m := z.ClassMismatches[0]; m.SKU != "vm.two" || m.Asked != capacity.ClassSpot || m.CountedAs != capacity.ClassGuaranteed || string(m.Units) != "5.000000" || m.Resources != 1 {
		t.Fatalf("mismatch = %+v", m)
	}
	if ov.Summary.ClassMismatches != 1 || ov.Summary.SpotToReclaim != 1 || ov.Summary.UnplacedSKUs != 0 {
		t.Fatalf("summary = %+v", ov.Summary)
	}

	// The basket is the mix that is SELLING, per metered SKU and class — the
	// family's member appears under its own name and shape.
	items := map[string]string{}
	for _, it := range p.Basket.Items {
		items[it.SKU+"/"+it.Class] = string(it.Units)
	}
	if _, ok := items["fam.a.small/guaranteed"]; !ok || len(items) != 5 {
		t.Fatalf("basket items = %v", items)
	}

	// Headroom by class: burstable is sold out of the envelope, guaranteed
	// past the floor costs ratio.
	for basket, want := range map[string]int64{"vm.one:1:burstable": 47, "vm.one:1:guaranteed": 19, "vm.one:1": 19} {
		pv, err := st.CapacityPoolBasket(ctx, s.pool.ID, s.now, store.ParseBasket(basket), nil)
		if err != nil {
			t.Fatalf("%s: %v", basket, err)
		}
		if pv.Basket.Units == nil || *pv.Basket.Units != want {
			t.Errorf("headroom %s = %v, want %d", basket, pv.Basket.Units, want)
		}
	}
}

func TestIntegrationCapacityRunningResourcesAndReclaim(t *testing.T) {
	st := testdb.Open(t)
	s := seedClasses(t, st)
	ctx := context.Background()

	run, err := st.CapacityPoolResources(ctx, s.pool.ID, s.now)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		class, source, asked string
		reclaim              bool
	}
	got := map[string]row{}
	for _, r := range run.Resources {
		got[r.ResourceID] = row{r.Class, r.ClassSource, r.Asked, r.Reclaim}
		if r.SharedWith == nil || r.PlacedClasses == nil {
			t.Errorf("%s: shared_with and placed_classes must never be null", r.ResourceID)
		}
	}
	want := map[string]row{
		"vm-1": {capacity.ClassGuaranteed, capacity.ClassFromDefault, "", false},
		"vm-2": {capacity.ClassBurstable, capacity.ClassFromTag, "", false},
		"vm-3": {capacity.ClassSpot, capacity.ClassFromTag, "", false},
		"vm-4": {capacity.ClassSpot, capacity.ClassFromTag, "", true}, // the newest spot, and it covers the 17.5
		"vm-5": {capacity.ClassSpot, capacity.ClassFromOverride, "", false},
		"vm-6": {capacity.ClassGuaranteed, capacity.ClassFromDefault, capacity.ClassSpot, false},
		"vm-7": {capacity.ClassGuaranteed, capacity.ClassFromDefault, "", false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("running = %+v\nwant     %+v", got, want)
	}
	if len(run.Reclaim) != 1 {
		t.Fatalf("reclaim = %+v", run.Reclaim)
	}
	if rc := run.Reclaim[0]; rc.Resource != capacity.ResourceVCPU || string(rc.Needed) != "17.500000" || string(rc.Covered) != "25.000000" || rc.Resources != 1 || rc.Short {
		t.Fatalf("reclaim = %+v", rc)
	}
	for _, r := range run.Resources {
		if r.ResourceID == "vm-7" && (r.Via != "fam.*" || string(r.Consumes[capacity.ResourceVCPU]) != "6.000000") {
			t.Errorf("family member = %+v", r)
		}
		if r.ResourceID == "vm-1" && !reflect.DeepEqual(r.PlacedClasses, allClasses) {
			t.Errorf("vm-1 placed classes = %v", r.PlacedClasses)
		}
	}

	// Clearing the override sends vm-5 back to the default — and the overview
	// moves with it, because both read the same resolution.
	if err := st.ClearCapacityResourceClass(ctx, s.src.ID, "vm-5"); err != nil {
		t.Fatal(err)
	}
	if err := st.ClearCapacityResourceClass(ctx, s.src.ID, "vm-5"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("clearing twice = %v", err)
	}
	ov, err := st.CapacityOverview(ctx, s.now, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	cpu := resOf(t, poolOf(t, zoneOf(t, ov, "me-east-215", "me-east-215a"), "k8s-a"), capacity.ResourceVCPU)
	capDec(t, "guaranteed after clearing the override", cpu.Guaranteed, "71.000000")
	capDec(t, "spot after clearing the override", cpu.Spot, "55.000000")

	// An override on a resource nobody meters would match nothing.
	if _, err := st.SetCapacityResourceClass(ctx, s.src.ID, "vm-nope", capacity.ClassSpot, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("override on an unknown resource = %v", err)
	}
	if _, err := st.SetCapacityResourceClass(ctx, s.src.ID, "vm-1", "reserved", "x"); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("override with an unknown class = %v", err)
	}
}

// A pool that does not enforce burstable has no overcommit and no floor,
// whatever a row written before the rule holds.
func TestIntegrationCapacityPoolWithoutBurstableHasNoOvercommit(t *testing.T) {
	st := testdb.Open(t)
	s := seedClasses(t, st)
	ctx := context.Background()
	if _, err := st.DB().ExecContext(ctx, `DELETE FROM capacity_placements WHERE pool_id = $1 AND class = 'burstable'`, s.pool.ID); err != nil {
		t.Fatal(err)
	}
	// The row as an older release could have left it: ratio 2.5 and a floor on
	// a pool that enforces guaranteed and spot only.
	if _, err := st.DB().ExecContext(ctx, `UPDATE capacity_pools SET classes = ARRAY['guaranteed','spot'] WHERE id = $1`, s.pool.ID); err != nil {
		t.Fatal(err)
	}
	ov, err := st.CapacityOverview(ctx, s.now, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	cpu := resOf(t, poolOf(t, zoneOf(t, ov, "me-east-215", "me-east-215a"), "k8s-a"), capacity.ResourceVCPU)
	capDec(t, "effective ratio", cpu.OvercommitRatio, "1.000000")
	capDec(t, "effective floor", cpu.GuaranteedFloor, "0.000000")
	capDec(t, "no envelope without burstable", cpu.BurstableEnvelope, "0.000000")
	// vm-2 says burstable, which is no longer placed: it counts guaranteed,
	// and 111 guaranteed on 100 usable is oversold, not roomy.
	capDec(t, "guaranteed", cpu.Guaranteed, "111.000000")
	capDec(t, "burstable", cpu.Burstable, "0.000000")
	capDec(t, "guaranteed ceiling", cpu.GuaranteedCeiling, "0.000000")
	if cpu.Status != capacity.StatusCritical {
		t.Errorf("111 guaranteed on 100 usable must read critical, got %s", cpu.Status)
	}
}

func TestIntegrationCapacitySKUOptions(t *testing.T) {
	st := testdb.Open(t)
	s := seedClasses(t, st)
	ctx := context.Background()
	book, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "list", Currency: "OMR"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO price_items (price_book_id, sku, unit, unit_price, description) VALUES ($1, 'elb', 'hour', 0.01, 'Load balancer'), ($1, 'vm.one', 'instance-hour', 0.1, 'One')`, book.ID); err != nil {
		t.Fatal(err)
	}
	opts, err := st.ListCapacitySKUOptions(ctx, s.now)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]store.CapacitySKUOption{}
	for _, o := range opts.SKUs {
		by[o.SKU] = o
		if o.Shape == nil {
			t.Errorf("%s: shape must never be null", o.SKU)
		}
	}
	if o := by["vm.one"]; !o.InPriceBook || !o.Metered || !o.HasShape || o.Description != "One" {
		t.Errorf("vm.one = %+v", o)
	}
	if o := by["elb"]; !o.InPriceBook || o.Metered || o.HasShape {
		t.Errorf("elb = %+v: priced, not metered, and nothing says what it consumes", o)
	}
	if o := by["fam.a.small"]; o.InPriceBook || !o.Metered || !o.HasShape {
		t.Errorf("fam.a.small = %+v", o)
	}
	if o := by["evs.ssd.gb"]; !o.HasShape {
		t.Errorf("a seeded shape must be offered even when nothing meters it: %+v", o)
	}
	fams := map[string]int{}
	for _, f := range opts.Families {
		fams[f.Pattern] = f.SKUs
	}
	if fams["vm.*"] != 2 || fams["fam.*"] != 1 || fams["ecs.m7n.*"] != 2 {
		t.Errorf("families = %v", fams)
	}
}

// The class migration against the shape hw307 is in today: one pool with
// burstable placements already on it, and one that overcommits with none.
// Nothing an operator entered may be refused by the migration that introduced
// the rule.
func TestIntegrationCapacityClassesMigrationKeepsWhatIsInUse(t *testing.T) {
	dsn := os.Getenv(testdb.EnvVar)
	if dsn == "" {
		t.Skipf("%s not set; skipping integration test", testdb.EnvVar)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	for _, stmt := range []string{`DROP SCHEMA IF EXISTS capacity_classes_migration CASCADE`, `CREATE SCHEMA capacity_classes_migration`, `SET search_path = capacity_classes_migration, public`} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = db.ExecContext(c, `DROP SCHEMA IF EXISTS capacity_classes_migration CASCADE`)
	})
	st := store.New(db)
	if store.MigrationCapacityClasses <= store.MigrationCapacityPools {
		t.Fatalf("the class migration (%d) must come after the pool migration (%d)", store.MigrationCapacityClasses, store.MigrationCapacityPools)
	}
	if err := st.MigrateUpTo(ctx, store.MigrationCapacityClasses-1); err != nil {
		t.Fatalf("migrate to the version before classes: %v", err)
	}
	var regionID, zoneID string
	if err := db.QueryRowContext(ctx, `INSERT INTO capacity_regions (code, name) VALUES ('me-east-215', 'Muscat') RETURNING id`).Scan(&regionID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO capacity_zones (region_id, code, name, is_default) VALUES ($1, 'me-east-215-a', 'AZ 1', true) RETURNING id`, regionID).Scan(&zoneID); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for name, ratio := range map[string]string{"vcpu": "1", "overcommitted": "4", "plain": "1"} {
		var id string
		if err := db.QueryRowContext(ctx, `INSERT INTO capacity_pools (zone_id, name, machines) VALUES ($1, $2, 1) RETURNING id`, zoneID, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO capacity_pool_resources (pool_id, resource, per_machine, overcommit_ratio) VALUES ($1, 'vcpu', 256, $2)`, id, ratio); err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	// hw307, verbatim: three SKUs on the vcpu pool, two of them burstable.
	for sku, class := range map[string]string{"ecs.m7n.2xlarge.2": "burstable", "ecs.m7n.2xlarge.8": "guaranteed", "ecs.m7n.xlarge.8": "burstable"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO capacity_placements (pool_id, sku, class, updated_by) VALUES ($1, $2, $3, 'ops')`, ids["vcpu"], sku, class); err != nil {
			t.Fatal(err)
		}
	}

	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	pools, err := st.ListCapacityPools(ctx, zoneID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, p := range pools {
		got[p.Name] = p.Classes
		if string(p.Resources[0].GuaranteedFloor) != "0.000000" {
			t.Errorf("%s: the floor must start at 0, got %s", p.Name, p.Resources[0].GuaranteedFloor)
		}
	}
	want := map[string][]string{
		"vcpu":          allClasses, // it already sells burstable
		"overcommitted": allClasses, // overcommit IS burstable
		"plain":         {capacity.ClassGuaranteed, capacity.ClassSpot},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("classes after the migration = %v, want %v", got, want)
	}
	pls, err := st.ListCapacityPlacements(ctx)
	if err != nil || len(pls) != 3 {
		t.Fatalf("placements = %d, %v: every row must survive the key change", len(pls), err)
	}
	// And the key really did widen: the SKU that was guaranteed can now ALSO
	// be placed spot on the same pool.
	if _, err := st.PutCapacityPlacement(ctx, ids["vcpu"], "ecs.m7n.2xlarge.8", capacity.ClassSpot, "ops"); err != nil {
		t.Fatalf("the same SKU at a second class after the migration: %v", err)
	}
	// Every pool the migration touched can still be saved as it stands — the
	// editor round-trip an operator does the morning after.
	for _, p := range pools {
		in := store.CapacityPoolInput{Name: p.Name, Machines: p.Machines, Classes: p.Classes, Resources: p.Resources}
		if _, _, err := st.SetCapacityPool(ctx, p.ID, in, "ops"); err != nil {
			t.Errorf("re-saving migrated pool %s: %v", p.Name, err)
		}
	}
}
