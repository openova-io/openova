package store

import (
	"fmt"
	"strings"
)

// The FIRST capacity migration, frozen (DESIGN.md §11, EPIC #6867).
//
// It created the per-(zone, family) pools, the seven-family CHECK constraint,
// sku_footprints and sku_caps. capacityPoolsMigrationSQL (capacity.go) is
// what turns all of that into pools of machines, shapes and placements — and
// it only works if a FRESH database passes through the same shape a live one
// already has.
//
// SO THIS TEXT IS FROZEN AND MUST NOT CHANGE. It used to be generated from
// internal/capacity's family list and seed, which meant editing that list
// silently rewrote a migration other databases had already applied: a fresh
// database and hw307 would have diverged, and the ALTERs that follow would be
// running against two different schemas. The constants it needs are copied
// here, next to the SQL, exactly as they were the day it shipped.
//
// Nothing reads these copies except this file. capacity.Derive and
// capacity.SeedResourceKinds are the LIVE lists; these are history.

// legacyCapacityFamilies is the seven-family list as it was — the CHECK
// constraint capacityPoolsMigrationSQL later drops, because a fixed list is
// exactly what stopped two vCPU pools coexisting in one zone.
var legacyCapacityFamilies = []string{
	"vcpu", "memory_gib", "block_ssd_gib", "block_hdd_gib", "object_gib", "eip_addresses", "bandwidth_mbps",
}

// legacyCapacityFootprintSeed is capacity.Seed() as it was: the National
// Cloud list SKUs whose footprint their name states, in family order.
var legacyCapacityFootprintSeed = [][3]string{
	{"ecs.m7n.xlarge.8", "vcpu", "4"},
	{"ecs.m7n.xlarge.8", "memory_gib", "32"},
	{"ecs.m7n.2xlarge.8", "vcpu", "8"},
	{"ecs.m7n.2xlarge.8", "memory_gib", "64"},
	{"ecs.s7n.2xlarge.2", "vcpu", "8"},
	{"ecs.s7n.2xlarge.2", "memory_gib", "16"},
	{"evs.ssd.gb", "block_ssd_gib", "1"},
	{"eip", "eip_addresses", "1"},
	{"eip.bandwidth_mbps", "bandwidth_mbps", "1"},
}

// legacyCapacityFamilyCheckSQL is the family list as a SQL CHECK.
func legacyCapacityFamilyCheckSQL() string {
	keys := make([]string, len(legacyCapacityFamilies))
	for i, k := range legacyCapacityFamilies {
		keys[i] = sqlQuote(k)
	}
	return "CHECK (family IN (" + strings.Join(keys, ",") + "))"
}

// legacyCapacityFootprintSeedSQL inserts the seed footprints. The rows are
// carried into sku_shapes by the rename in capacityPoolsMigrationSQL; the
// live re-seed for a wiped test database is CapacityShapeSeedSQL.
func legacyCapacityFootprintSeedSQL() string {
	var b strings.Builder
	for _, r := range legacyCapacityFootprintSeed {
		fmt.Fprintf(&b, "INSERT INTO sku_footprints (sku, family, amount, source) VALUES (%s, %s, %s, %s) ON CONFLICT (sku, family) DO NOTHING;\n",
			sqlQuote(r[0]), sqlQuote(r[1]), r[2], sqlQuote("seed"))
	}
	return b.String()
}

// capacityMigrationSQL is the capacity schema as it first shipped, one
// transaction. Positional, and frozen: see the file comment.
func capacityMigrationSQL() string {
	check := legacyCapacityFamilyCheckSQL()
	return `
CREATE TABLE IF NOT EXISTS capacity_regions (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	code TEXT NOT NULL UNIQUE CHECK (code <> '' AND code = lower(code)),
	name TEXT NOT NULL DEFAULT '',
	cloud_source_kind TEXT NOT NULL DEFAULT 'huawei-project' CHECK (cloud_source_kind IN ('huawei-project','file')),
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS capacity_zones (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	region_id UUID NOT NULL REFERENCES capacity_regions(id) ON DELETE CASCADE,
	code TEXT NOT NULL CHECK (code <> '' AND code = lower(code)),
	name TEXT NOT NULL DEFAULT '',
	is_default BOOLEAN NOT NULL DEFAULT false,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (region_id, code)
);
CREATE UNIQUE INDEX IF NOT EXISTS capacity_zones_default_uniq ON capacity_zones (region_id) WHERE is_default;
CREATE TABLE IF NOT EXISTS capacity_pools (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	zone_id UUID NOT NULL REFERENCES capacity_zones(id) ON DELETE CASCADE,
	family TEXT NOT NULL ` + check + `,
	total NUMERIC(20,6) NOT NULL DEFAULT 0 CHECK (total >= 0),
	reserved NUMERIC(20,6) NOT NULL DEFAULT 0 CHECK (reserved >= 0),
	source TEXT NOT NULL DEFAULT 'manual',
	note TEXT NOT NULL DEFAULT '',
	updated_by TEXT NOT NULL DEFAULT '',
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (zone_id, family)
);
CREATE TABLE IF NOT EXISTS capacity_pool_history (
	id BIGSERIAL PRIMARY KEY,
	pool_id UUID NOT NULL REFERENCES capacity_pools(id) ON DELETE CASCADE,
	total NUMERIC(20,6) NOT NULL,
	source TEXT NOT NULL,
	note TEXT NOT NULL DEFAULT '',
	changed_by TEXT NOT NULL DEFAULT '',
	changed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS capacity_pool_history_pool_idx ON capacity_pool_history (pool_id, changed_at DESC);
CREATE TABLE IF NOT EXISTS sku_footprints (
	sku TEXT NOT NULL CHECK (sku <> ''),
	family TEXT NOT NULL ` + check + `,
	amount NUMERIC(20,6) NOT NULL CHECK (amount > 0),
	source TEXT NOT NULL DEFAULT 'manual',
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (sku, family)
);
CREATE TABLE IF NOT EXISTS sku_caps (
	zone_id UUID NOT NULL REFERENCES capacity_zones(id) ON DELETE CASCADE,
	sku TEXT NOT NULL CHECK (sku <> ''),
	total NUMERIC(20,6) NOT NULL CHECK (total >= 0),
	updated_by TEXT NOT NULL DEFAULT '',
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (zone_id, sku)
);
` + legacyCapacityFootprintSeedSQL()
}

// MigrationCapacity is the schema_migrations version of the first capacity
// migration, located by content like the others so a migration appended after
// it cannot move this version.
var MigrationCapacity = func() int {
	want := capacityMigrationSQL()
	for i, m := range migrations {
		if m == want {
			return i + 1
		}
	}
	return len(migrations)
}()
