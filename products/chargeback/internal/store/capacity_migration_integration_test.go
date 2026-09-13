package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/capacity"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// The capacity POOL migration (DESIGN.md §11, founder direction 2026-09-13).
// This module is live on hw307, so the test stands a database at the shape
// BEFORE the rewrite — per-(zone, family) pools with totals, a reserve, a
// note, history rows and a sku_cap — applies the migration, and asserts what
// an operator would check the morning after:
//
//   - the totals they entered are still there, to the last decimal, as a
//     single-resource pool named for its family;
//   - the pool IDS are unchanged, so capacity_pool_history still points at
//     them and the history is intact and now says which resource it was about;
//   - the pools NOBODY ever sized are gone rather than carried as noise;
//   - the SKUs that used to count against a family pool still count against
//     it, through a seeded placement;
//   - the sku_caps row is in the AUDIT TRAIL with the reason it was retired,
//     and the table is gone.
func TestIntegrationCapacityPoolsMigrationKeepsEnteredTotals(t *testing.T) {
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
	// A private schema: the pre-migration shape never touches the shared
	// public one, and one connection so the search_path holds.
	db.SetMaxOpenConns(1)
	for _, stmt := range []string{
		`DROP SCHEMA IF EXISTS capacity_migration CASCADE`,
		`CREATE SCHEMA capacity_migration`,
		`SET search_path = capacity_migration, public`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = db.ExecContext(c, `DROP SCHEMA IF EXISTS capacity_migration CASCADE`)
	})

	st := store.New(db)
	if err := st.MigrateUpTo(ctx, store.MigrationCapacityPools-1); err != nil {
		t.Fatalf("migrate to the version before the pool rewrite: %v", err)
	}

	// The operator's world, as it was.
	var regionID, zoneID string
	if err := db.QueryRowContext(ctx, `INSERT INTO capacity_regions (code, name) VALUES ('me-east-215', 'Muscat') RETURNING id`).Scan(&regionID); err != nil {
		t.Fatalf("seed region: %v", err)
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO capacity_zones (region_id, code, name, is_default) VALUES ($1, 'me-east-215a', 'AZ 1', true) RETURNING id`, regionID).Scan(&zoneID); err != nil {
		t.Fatalf("seed zone: %v", err)
	}
	poolID := map[string]string{}
	// All seven, exactly as CreateCapacityZone made them; only two were ever
	// given a number.
	for _, fam := range []string{"vcpu", "memory_gib", "block_ssd_gib", "block_hdd_gib", "object_gib", "eip_addresses", "bandwidth_mbps"} {
		var id string
		if err := db.QueryRowContext(ctx, `INSERT INTO capacity_pools (zone_id, family) VALUES ($1, $2) RETURNING id`, zoneID, fam).Scan(&id); err != nil {
			t.Fatalf("seed pool %s: %v", fam, err)
		}
		poolID[fam] = id
	}
	if _, err := db.ExecContext(ctx, `UPDATE capacity_pools SET total = 1536.500000, reserved = 128.000000, note = 'two racks', updated_by = 'ops@nc.example' WHERE id = $1`, poolID["vcpu"]); err != nil {
		t.Fatalf("seed vcpu total: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE capacity_pools SET total = 9216.000000, note = 'measured', updated_by = 'ops@nc.example' WHERE id = $1`, poolID["memory_gib"]); err != nil {
		t.Fatalf("seed memory total: %v", err)
	}
	for _, h := range []struct {
		fam   string
		total string
	}{{"vcpu", "1024.000000"}, {"vcpu", "1536.500000"}, {"memory_gib", "9216.000000"}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO capacity_pool_history (pool_id, total, source, note, changed_by) VALUES ($1, $2, 'manual', 'entered', 'ops@nc.example')`, poolID[h.fam], h.total); err != nil {
			t.Fatalf("seed history: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO sku_caps (zone_id, sku, total, updated_by) VALUES ($1, 'ecs.m7n.2xlarge.8', 60, 'ops@nc.example')`, zoneID); err != nil {
		t.Fatalf("seed cap: %v", err)
	}

	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// THE TOTALS SURVIVED, and they are now a pool of ONE machine holding
	// exactly what was entered: raw is unchanged to the last decimal.
	pools, err := st.ListCapacityPools(ctx, zoneID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pools) != 2 {
		t.Fatalf("pools after migration = %d, want 2 (the five nobody sized carried nothing): %+v", len(pools), pools)
	}
	byName := map[string]store.CapacityPool{}
	for _, p := range pools {
		byName[p.Name] = p
	}
	vcpu, ok := byName["vcpu"]
	if !ok {
		t.Fatalf("a per-family row becomes a pool named for its family: %+v", pools)
	}
	if vcpu.ID != poolID["vcpu"] {
		t.Fatalf("pool ids must survive (capacity_pool_history points at them): %s vs %s", vcpu.ID, poolID["vcpu"])
	}
	if string(vcpu.Machines) != "1.000000" || vcpu.Note != "two racks" || vcpu.UpdatedBy != "ops@nc.example" {
		t.Fatalf("migrated vcpu pool = %+v", vcpu)
	}
	if len(vcpu.Resources) != 1 {
		t.Fatalf("a per-family pool holds exactly one resource: %+v", vcpu.Resources)
	}
	r := vcpu.Resources[0]
	if r.Resource != capacity.ResourceVCPU || string(r.PerMachine) != "1536.500000" || string(r.Reserve) != "128.000000" || string(r.OvercommitRatio) != "1.000000" {
		t.Fatalf("the entered total must survive exactly: %+v", r)
	}
	if mem := byName["memory_gib"]; string(mem.Resources[0].PerMachine) != "9216.000000" {
		t.Fatalf("memory total = %+v", mem.Resources)
	}

	// THE HISTORY SURVIVED and now says which resource each entry was about.
	hist, err := st.ListCapacityPoolHistory(ctx, poolID["vcpu"], 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 {
		t.Fatalf("vcpu history = %d rows, want the 2 that were entered: %+v", len(hist), hist)
	}
	for _, h := range hist {
		if h.Resource != capacity.ResourceVCPU || string(h.Machines) != "1.000000" || h.ChangedBy != "ops@nc.example" || h.Note != "entered" {
			t.Fatalf("history row = %+v", h)
		}
		if string(h.PerMachine) != string(h.Total) {
			t.Fatalf("at one machine the per-machine amount IS the raw total: %+v", h)
		}
	}

	// THE ATTRIBUTION SURVIVED: every SKU whose shape names the pool's
	// resource is placed on it, as guaranteed — which is what the old model
	// counted, at 1:1, with no oversubscription anywhere.
	pls, err := st.ListCapacityPlacements(ctx)
	if err != nil {
		t.Fatal(err)
	}
	placed := map[string]string{}
	for _, pl := range pls {
		if pl.PoolID == poolID["vcpu"] {
			placed[pl.SKU] = pl.Class
		}
		if pl.UpdatedBy != "migration" {
			t.Fatalf("a seeded placement is recorded as the migration's: %+v", pl)
		}
	}
	for _, sku := range []string{"ecs.m7n.xlarge.8", "ecs.m7n.2xlarge.8", "ecs.s7n.2xlarge.2"} {
		if placed[sku] != capacity.ClassGuaranteed {
			t.Fatalf("%s must still count against the vcpu pool, as guaranteed: %+v", sku, placed)
		}
	}
	if _, wrong := placed["evs.ssd.gb"]; wrong {
		t.Fatalf("a block-storage SKU never counted against the vcpu pool: %+v", placed)
	}

	// THE SHAPES SURVIVED the rename, with their source.
	shapes, err := st.ListCapacityShapes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, sh := range shapes {
		if sh.SKU == "ecs.m7n.2xlarge.8" {
			found = true
			if sh.Source != capacity.SourceSeed || string(sh.Resources[capacity.ResourceVCPU]) != "8.000000" || string(sh.Resources[capacity.ResourceMemoryGiB]) != "64.000000" {
				t.Fatalf("renamed shape = %+v", sh)
			}
		}
	}
	if !found || len(shapes) != 6 {
		t.Fatalf("shapes after the rename = %d, want the 6 seeded footprints", len(shapes))
	}

	// sku_caps IS RETIRED — into the audit trail, with the reason, and the
	// table is gone. A per-SKU ceiling is capacity expressed a second time
	// and it does not compose.
	var actor, sku, total, why string
	if err := db.QueryRowContext(ctx, `SELECT actor, details->>'sku', details->>'total', details->>'why' FROM audit_log WHERE action = 'capacity.cap' AND details->>'op' = 'retired'`).
		Scan(&actor, &sku, &total, &why); err != nil {
		t.Fatalf("the retired cap must be readable in the audit trail: %v", err)
	}
	if actor != "ops@nc.example" || sku != "ecs.m7n.2xlarge.8" || total != "60.000000" || why == "" {
		t.Fatalf("retired cap = actor %q sku %q total %q why %q", actor, sku, total, why)
	}
	var stillThere bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('sku_caps') IS NOT NULL`).Scan(&stillThere); err != nil {
		t.Fatal(err)
	}
	if stillThere {
		t.Fatal("sku_caps must be gone")
	}

	// And the whole thing still reads: the migrated pools derive, with no
	// consumption and therefore nothing sold.
	ov, err := st.CapacityOverview(ctx, time.Date(2026, 9, 9, 10, 30, 0, 0, time.UTC), "", growthLikeAPI)
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.Regions) != 1 || len(ov.Regions[0].Zones) != 1 || len(ov.Regions[0].Zones[0].Pools) != 2 {
		t.Fatalf("overview after migration = %+v", ov.Regions)
	}
	p := ov.Regions[0].Zones[0].Pools[0]
	if p.Status != capacity.StatusOK || p.Binding == "" {
		t.Fatalf("a migrated pool with nothing sold is ok and has a binding resource: %+v", p)
	}
}
