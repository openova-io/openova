package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/openova-io/openova/products/chargeback/internal/capacity"
)

// Capacity management (DESIGN.md §11, EPIC #6867).
//
// THE MODEL IS THREE STORED THINGS AND NOTHING ELSE.
//
//	POOL       a named set of identical machines in a zone: a machine count,
//	           a PER-MACHINE vector of resources, a reserve and an overcommit
//	           ratio per resource, and a procurement lead time.
//	SHAPE      a SKU's vector: how much of each resource ONE unit consumes.
//	PLACEMENT  (sku, pool, class): this SKU sells out of that pool, at that
//	           class. The CLASS lives here, not on the SKU's shape — the same
//	           shape sold guaranteed and sold spot is two SKUs at two prices,
//	           both placed on the same pool.
//
// Consumption is never entered. It is the usage ledger this product already
// keeps (§2), read one way: the latest metered hour, through the shapes, onto
// the pools the placements name.
//
// WHY THIS REPLACED THE FAMILY POOLS (founder direction 2026-09-13):
//
//  1. A pool's capacity is a VECTOR, not a number. vCPU and RAM in the same
//     server are not independently sellable; one pool per (zone, family) let
//     the product "sell" vCPU with no RAM behind it.
//  2. Several pools of the SAME resource kind must coexist in one zone (two
//     batches, two server types). UNIQUE (zone_id, family) forbade it, which
//     is why resource kinds are now DATA and never a CHECK constraint.
//  3. Per-SKU headroom was a wrong answer, not a missing feature: "50 large
//     fit" and "200 small fit" side by side are mutually exclusive, each
//     silently assuming the others sell zero. Free room is now ONE basket
//     headroom over a named mix, with the binding resource.
//
// Every change to a pool is audited (the API writes capacity.pool) and kept
// in capacity_pool_history, one row per resource, so a pool's size can be
// read back to the day it was entered.

// ---------------------------------------------------------------------------
// migration
// ---------------------------------------------------------------------------

// CapacityResourceKindSeedSQL inserts the resource kinds this product meters.
// It carries the LABEL and UNIT only: the row is a description, never a
// constraint, and a pool may declare a kind that is not in it (the store adds
// the row with the key as its label). The test database helper re-runs it
// after wiping the table.
func CapacityResourceKindSeedSQL() string {
	var b strings.Builder
	for _, k := range capacity.SeedResourceKinds {
		fmt.Fprintf(&b, "INSERT INTO capacity_resource_kinds (resource, label, unit, position) VALUES (%s, %s, %s, %d) ON CONFLICT (resource) DO NOTHING;\n",
			sqlQuote(k.Key), sqlQuote(k.Label), sqlQuote(k.Unit), k.Position)
	}
	return b.String()
}

// CapacityShapeSeedSQL inserts the seed shapes (capacity.Seed): the National
// Cloud list SKUs whose vector the name states. ON CONFLICT DO NOTHING, so a
// shape the operator has since edited is never overwritten. The test database
// helper re-runs it after wiping sku_shapes, so every test starts from the
// seeded rows.
func CapacityShapeSeedSQL() string {
	var b strings.Builder
	for _, r := range capacity.Seed() {
		fmt.Fprintf(&b, "INSERT INTO sku_shapes (sku, resource, amount_per_unit, source) VALUES (%s, %s, %s, %s) ON CONFLICT (sku, resource) DO NOTHING;\n",
			sqlQuote(r.SKU), sqlQuote(r.Resource), r.Amount, sqlQuote(capacity.SourceSeed))
	}
	return b.String()
}

// capacityPoolsMigrationSQL turns the per-(zone, family) pools into pools of
// machines, renames sku_footprints to sku_shapes, adds placements and retires
// sku_caps. ONE transaction, appended at the END of migrations: they are
// positional.
//
// WHAT HAPPENS TO WHAT IS ALREADY THERE — this module is live on hw307:
//
//   - Pool IDS SURVIVE. The table is ALTERed, never recreated, so every
//     capacity_pool_history row still points at its pool.
//   - A per-(zone, family) row BECOMES A SINGLE-RESOURCE POOL NAMED FOR ITS
//     FAMILY, which is exactly what it was: machines 1, per_machine = the
//     total the operator entered, reserve = what was reserved, ratio 1 (the
//     old model had no oversubscription, so everything it counted was
//     guaranteed at 1:1).
//   - The pools NOBODY EVER SIZED are deleted. CreateCapacityZone made seven
//     per zone whether or not an operator wanted them; a row at total 0 with
//     no reserve, no note and no history carries nothing to lose.
//   - HISTORY IS KEPT AND WIDENED: each row gains the resource it was about
//     and the machines / per_machine / reserve / ratio behind its total.
//   - PLACEMENTS ARE SEEDED from the stored shapes: every SKU whose shape
//     names a migrated pool's resource is placed on it as `guaranteed`. That
//     reproduces the old attribution exactly — the old model counted every
//     SKU with vcpu in its footprint against the zone's vcpu pool — so no
//     zone reads empty after the migration.
//   - sku_caps IS RETIRED, its rows copied into the audit trail first (see
//     the INSERT below for the reason it does not compose).
func capacityPoolsMigrationSQL() string {
	return `
-- Resource kinds are DATA. This table carries a label and a unit for display
-- and nothing else; there is no CHECK constraint anywhere on a resource key,
-- because a fixed list is exactly what stopped two vCPU pools coexisting.
CREATE TABLE IF NOT EXISTS capacity_resource_kinds (
	resource TEXT PRIMARY KEY CHECK (resource <> '' AND resource = lower(resource)),
	label TEXT NOT NULL DEFAULT '',
	unit TEXT NOT NULL DEFAULT '',
	position INT NOT NULL DEFAULT 1000
);
` + CapacityResourceKindSeedSQL() + `
-- Shapes: sku_footprints, renamed and freed of its family enum.
DO $cap$ BEGIN
	IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'sku_footprints') THEN
		ALTER TABLE sku_footprints RENAME TO sku_shapes;
	END IF;
	IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'sku_shapes' AND column_name = 'family') THEN
		ALTER TABLE sku_shapes RENAME COLUMN family TO resource;
	END IF;
	IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'sku_shapes' AND column_name = 'amount') THEN
		ALTER TABLE sku_shapes RENAME COLUMN amount TO amount_per_unit;
	END IF;
END $cap$;
CREATE TABLE IF NOT EXISTS sku_shapes (
	sku TEXT NOT NULL CHECK (sku <> ''),
	resource TEXT NOT NULL CHECK (resource <> ''),
	amount_per_unit NUMERIC(20,6) NOT NULL CHECK (amount_per_unit > 0),
	source TEXT NOT NULL DEFAULT 'manual',
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (sku, resource)
);
-- The seven-kind CHECK goes. Postgres rewrites "family IN (...)" as
-- "= ANY (ARRAY[...])", so the sweep matches BOTH spellings — matching only
-- the one that was written is how this survived its first rename.
DO $cap$ DECLARE c TEXT; BEGIN
	FOR c IN SELECT conname FROM pg_constraint
		WHERE conrelid = 'sku_shapes'::regclass AND contype = 'c'
		  AND (pg_get_constraintdef(oid) LIKE '%ANY (ARRAY[%' OR pg_get_constraintdef(oid) LIKE '%IN (%') LOOP
		EXECUTE format('ALTER TABLE sku_shapes DROP CONSTRAINT %I', c);
	END LOOP;
END $cap$;

-- Pools: ALTER, never recreate, so capacity_pool_history keeps its target.
ALTER TABLE capacity_pools ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT '';
ALTER TABLE capacity_pools ADD COLUMN IF NOT EXISTS machines NUMERIC(20,6) NOT NULL DEFAULT 1 CHECK (machines >= 0);
ALTER TABLE capacity_pools ADD COLUMN IF NOT EXISTS lead_time_days INT NOT NULL DEFAULT 0 CHECK (lead_time_days >= 0);
ALTER TABLE capacity_pools ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- A pool nobody ever sized carries nothing to lose: no total, no reserve, no
-- note, no history. CreateCapacityZone made seven of these per zone.
DELETE FROM capacity_pools p
 WHERE p.total = 0 AND p.reserved = 0 AND p.note = ''
   AND NOT EXISTS (SELECT 1 FROM capacity_pool_history h WHERE h.pool_id = p.id);

CREATE TABLE IF NOT EXISTS capacity_pool_resources (
	pool_id UUID NOT NULL REFERENCES capacity_pools(id) ON DELETE CASCADE,
	resource TEXT NOT NULL REFERENCES capacity_resource_kinds(resource) ON UPDATE CASCADE,
	per_machine NUMERIC(20,6) NOT NULL DEFAULT 0 CHECK (per_machine >= 0),
	reserve NUMERIC(20,6) NOT NULL DEFAULT 0 CHECK (reserve >= 0),
	overcommit_ratio NUMERIC(20,6) NOT NULL DEFAULT 1 CHECK (overcommit_ratio > 0),
	PRIMARY KEY (pool_id, resource)
);

-- A family the seed does not know (none today, but the column was free text
-- in practice) still needs a kind row before the foreign key will take it.
INSERT INTO capacity_resource_kinds (resource, label, unit, position)
	SELECT DISTINCT lower(family), lower(family), '', 1000 FROM capacity_pools
	ON CONFLICT (resource) DO NOTHING;

-- The row becomes a single-resource pool of ONE machine: per_machine is the
-- total that was entered, so raw is unchanged to the last decimal.
INSERT INTO capacity_pool_resources (pool_id, resource, per_machine, reserve, overcommit_ratio)
	SELECT id, lower(family), total, reserved, 1 FROM capacity_pools
	ON CONFLICT (pool_id, resource) DO NOTHING;
UPDATE capacity_pools SET name = lower(family) WHERE name = '';

-- History keeps every total ever entered and gains the resource it was about.
ALTER TABLE capacity_pool_history ADD COLUMN IF NOT EXISTS resource TEXT NOT NULL DEFAULT '';
ALTER TABLE capacity_pool_history ADD COLUMN IF NOT EXISTS machines NUMERIC(20,6) NOT NULL DEFAULT 1;
ALTER TABLE capacity_pool_history ADD COLUMN IF NOT EXISTS per_machine NUMERIC(20,6) NOT NULL DEFAULT 0;
ALTER TABLE capacity_pool_history ADD COLUMN IF NOT EXISTS reserve NUMERIC(20,6) NOT NULL DEFAULT 0;
ALTER TABLE capacity_pool_history ADD COLUMN IF NOT EXISTS overcommit_ratio NUMERIC(20,6) NOT NULL DEFAULT 1;
UPDATE capacity_pool_history h
   SET resource = p.name, per_machine = h.total, machines = 1
  FROM capacity_pools p
 WHERE p.id = h.pool_id AND h.resource = '';

ALTER TABLE capacity_pools DROP CONSTRAINT IF EXISTS capacity_pools_zone_id_family_key;
ALTER TABLE capacity_pools DROP COLUMN IF EXISTS family;
ALTER TABLE capacity_pools DROP COLUMN IF EXISTS total;
ALTER TABLE capacity_pools DROP COLUMN IF EXISTS reserved;
DO $cap$ BEGIN
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'capacity_pools'::regclass AND conname = 'capacity_pools_name_check') THEN
		ALTER TABLE capacity_pools ADD CONSTRAINT capacity_pools_name_check CHECK (name <> '');
	END IF;
END $cap$;
CREATE UNIQUE INDEX IF NOT EXISTS capacity_pools_zone_name_uniq ON capacity_pools (zone_id, lower(name));

-- Placements. The class is here and not on the shape: the same shape sold
-- guaranteed and sold spot is two SKUs at two prices on the same pool.
CREATE TABLE IF NOT EXISTS capacity_placements (
	pool_id UUID NOT NULL REFERENCES capacity_pools(id) ON DELETE CASCADE,
	sku TEXT NOT NULL CHECK (sku <> ''),
	class TEXT NOT NULL CHECK (class IN ('guaranteed','burstable','spot')),
	updated_by TEXT NOT NULL DEFAULT '',
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (pool_id, sku)
);
CREATE INDEX IF NOT EXISTS capacity_placements_sku_idx ON capacity_placements (sku);

-- Seeded from the stored shapes so the migrated zones read as they did: the
-- old model counted every SKU whose footprint named a family against that
-- family's pool, at 1:1 — which is guaranteed.
INSERT INTO capacity_placements (pool_id, sku, class, updated_by)
	SELECT DISTINCT pr.pool_id, s.sku, 'guaranteed', 'migration'
	  FROM capacity_pool_resources pr JOIN sku_shapes s ON s.resource = pr.resource
	ON CONFLICT (pool_id, sku) DO NOTHING;

-- sku_caps is RETIRED. A per-SKU ceiling is capacity expressed a second time
-- and it does not compose: two flavours sharing the same hardware carried
-- independent caps that never deducted from each other, so the sum of the
-- caps could exceed the machines twice over and nothing noticed. Every row
-- is copied into the audit trail, with the reason, before the table goes —
-- an operator can read back what they had entered and place the SKU instead.
INSERT INTO audit_log (customer_id, actor, action, details)
	SELECT NULL, COALESCE(NULLIF(c.updated_by, ''), 'migration'), 'capacity.cap',
		jsonb_build_object(
			'op', 'retired',
			'zone_id', c.zone_id::text,
			'zone', z.code,
			'region', r.code,
			'sku', c.sku,
			'total', c.total::text,
			'why', 'a per-SKU ceiling is capacity expressed a second time and does not compose: two SKUs sharing the same machines carried independent caps that never deducted from each other. Place the SKU on a pool and read its basket headroom instead.')
	  FROM sku_caps c
	  JOIN capacity_zones z ON z.id = c.zone_id
	  JOIN capacity_regions r ON r.id = z.region_id;
DROP TABLE IF EXISTS sku_caps;
`
}

// MigrationCapacityPools is the schema_migrations version of the capacity
// pool migration, located by content like the others so a migration appended
// after it cannot move this version.
var MigrationCapacityPools = func() int {
	want := capacityPoolsMigrationSQL()
	for i, m := range migrations {
		if m == want {
			return i + 1
		}
	}
	return len(migrations)
}()

// ---------------------------------------------------------------------------
// types
// ---------------------------------------------------------------------------

// CapacityRegion is a cloud region whose capacity is managed here. Code is
// the region as usage records carry it (cost_sources.region /
// usage_records.region, e.g. "me-east-215"), which is how consumption finds
// its region. CloudSourceKind says which collector will fill its pools.
type CapacityRegion struct {
	ID              string         `json:"id"`
	Code            string         `json:"code"`
	Name            string         `json:"name"`
	CloudSourceKind string         `json:"cloud_source_kind"`
	CreatedAt       time.Time      `json:"created_at"`
	Zones           []CapacityZone `json:"zones"`
}

// CapacityZone is one availability zone of a region. The DEFAULT zone of a
// region receives the consumption of records whose zone is not known — the
// inventory row carries no availability_zone — flagged as such.
type CapacityZone struct {
	ID         string    `json:"id"`
	RegionID   string    `json:"region_id"`
	RegionCode string    `json:"region_code,omitempty"`
	Code       string    `json:"code"`
	Name       string    `json:"name"`
	IsDefault  bool      `json:"is_default"`
	CreatedAt  time.Time `json:"created_at"`
	// Pools is ALWAYS on the wire, empty included: a zone with no pools is the
	// ordinary state now, and an absent key would read as "not loaded".
	Pools []CapacityPool `json:"pools"`
}

// CapacityPoolResource is one resource of a pool's per-machine vector, with
// the policy that applies to it. Ratios are per (pool, resource) and never
// global: vCPU may run 4:1 on the same machines whose RAM runs 1:1.
type CapacityPoolResource struct {
	Resource string `json:"resource"`
	Label    string `json:"label"`
	Unit     string `json:"unit"`
	// PerMachine is how much of this resource ONE machine holds; Raw is
	// PerMachine × the pool's machine count.
	PerMachine Decimal `json:"per_machine"`
	// Reserve is held back for redundancy and maintenance, in the resource's
	// own units — N+1 is one machine's worth.
	Reserve         Decimal `json:"reserve"`
	OvercommitRatio Decimal `json:"overcommit_ratio"`
}

// CapacityPool is a named set of identical machines in a zone.
//
// MACHINES × A PER-MACHINE VECTOR, not a raw total vector, and deliberately:
// it is what a pool IS. "Add two servers" is then a change to one field and
// every resource moves together, which is the honest behaviour — you cannot
// buy vCPU without the RAM in the same chassis. A raw total would let the two
// drift apart silently, which is the defect the family pools had.
type CapacityPool struct {
	ID         string  `json:"id"`
	ZoneID     string  `json:"zone_id"`
	ZoneCode   string  `json:"zone_code,omitempty"`
	RegionCode string  `json:"region_code,omitempty"`
	Name       string  `json:"name"`
	Machines   Decimal `json:"machines"`
	// LeadTimeDays is how long procurement takes. It is what turns a wall
	// into an ORDER-BY date, which is the date that matters.
	LeadTimeDays int                    `json:"lead_time_days"`
	Source       string                 `json:"source"`
	Note         string                 `json:"note"`
	UpdatedBy    string                 `json:"updated_by"`
	UpdatedAt    time.Time              `json:"updated_at"`
	CreatedAt    time.Time              `json:"created_at"`
	Resources    []CapacityPoolResource `json:"resources"`
}

// CapacityPoolChange is one entry of a pool's size history, one row per
// resource per change, so "vcpu went from 512 to 576" reads directly.
type CapacityPoolChange struct {
	ID              int64     `json:"id"`
	PoolID          string    `json:"pool_id"`
	Resource        string    `json:"resource"`
	Machines        Decimal   `json:"machines"`
	PerMachine      Decimal   `json:"per_machine"`
	Reserve         Decimal   `json:"reserve"`
	OvercommitRatio Decimal   `json:"overcommit_ratio"`
	Total           Decimal   `json:"total"` // machines × per_machine, the raw
	Source          string    `json:"source"`
	Note            string    `json:"note"`
	ChangedBy       string    `json:"changed_by"`
	ChangedAt       time.Time `json:"changed_at"`
}

// CapacityShape is how much of each resource ONE unit of a SKU consumes.
// Source is manual / seed for stored rows, derived for a shape the SKU name
// implies that has no row (capacity.Derive).
type CapacityShape struct {
	SKU       string             `json:"sku"`
	Resources map[string]Decimal `json:"resources"`
	Source    string             `json:"source"`
	UpdatedAt *time.Time         `json:"updated_at,omitempty"`
}

// CapacityPlacement says a SKU sells out of a pool, at a class.
type CapacityPlacement struct {
	PoolID     string    `json:"pool_id"`
	PoolName   string    `json:"pool_name,omitempty"`
	ZoneID     string    `json:"zone_id,omitempty"`
	ZoneCode   string    `json:"zone_code,omitempty"`
	RegionCode string    `json:"region_code,omitempty"`
	SKU        string    `json:"sku"`
	Class      string    `json:"class"`
	UpdatedBy  string    `json:"updated_by"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// CapacityResourceKind is a resource key with its label and unit.
type CapacityResourceKind = capacity.ResourceKind

// ---------------------------------------------------------------------------
// regions and zones
// ---------------------------------------------------------------------------

const capacityRegionColumns = `r.id, r.code, r.name, r.cloud_source_kind, r.created_at`
const capacityZoneColumns = `z.id, z.region_id, r.code, z.code, z.name, z.is_default, z.created_at`

func scanCapacityRegion(row interface{ Scan(...any) error }) (CapacityRegion, error) {
	var out CapacityRegion
	if err := row.Scan(&out.ID, &out.Code, &out.Name, &out.CloudSourceKind, &out.CreatedAt); err != nil {
		return out, mapErr(err)
	}
	out.CreatedAt = out.CreatedAt.UTC()
	out.Zones = []CapacityZone{}
	return out, nil
}

func scanCapacityZone(row interface{ Scan(...any) error }) (CapacityZone, error) {
	var out CapacityZone
	if err := row.Scan(&out.ID, &out.RegionID, &out.RegionCode, &out.Code, &out.Name, &out.IsDefault, &out.CreatedAt); err != nil {
		return out, mapErr(err)
	}
	out.CreatedAt = out.CreatedAt.UTC()
	return out, nil
}

// ListCapacityRegions returns every region with its zones (no pools), by code.
func (s *Store) ListCapacityRegions(ctx context.Context) ([]CapacityRegion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+capacityRegionColumns+` FROM capacity_regions r ORDER BY r.code`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []CapacityRegion{}
	idx := map[string]int{}
	for rows.Next() {
		r, err := scanCapacityRegion(rows)
		if err != nil {
			return nil, err
		}
		idx[r.ID] = len(out)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	zrows, err := s.db.QueryContext(ctx, `SELECT `+capacityZoneColumns+` FROM capacity_zones z JOIN capacity_regions r ON r.id = z.region_id ORDER BY r.code, z.is_default DESC, z.code`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer zrows.Close()
	for zrows.Next() {
		z, err := scanCapacityZone(zrows)
		if err != nil {
			return nil, err
		}
		if i, ok := idx[z.RegionID]; ok {
			out[i].Zones = append(out[i].Zones, z)
		}
	}
	return out, zrows.Err()
}

// GetCapacityRegion returns one region with its zones.
func (s *Store) GetCapacityRegion(ctx context.Context, id string) (CapacityRegion, error) {
	r, err := scanCapacityRegion(s.db.QueryRowContext(ctx, `SELECT `+capacityRegionColumns+` FROM capacity_regions r WHERE r.id = $1`, id))
	if err != nil {
		return r, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+capacityZoneColumns+` FROM capacity_zones z JOIN capacity_regions r ON r.id = z.region_id WHERE z.region_id = $1 ORDER BY z.is_default DESC, z.code`, id)
	if err != nil {
		return r, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		z, err := scanCapacityZone(rows)
		if err != nil {
			return r, err
		}
		r.Zones = append(r.Zones, z)
	}
	return r, rows.Err()
}

// normCode lower-cases and trims a region or zone code.
func normCode(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// CreateCapacityRegion adds a region. Code is the region as the ledger names
// it; an existing code is ErrConflict.
func (s *Store) CreateCapacityRegion(ctx context.Context, code, name, cloudSourceKind string) (CapacityRegion, error) {
	code = normCode(code)
	if code == "" {
		return CapacityRegion{}, fmt.Errorf("%w: code is required", ErrInvalid)
	}
	kind := strings.TrimSpace(cloudSourceKind)
	if kind == "" {
		kind = SourceKindHuaweiProject
	}
	if LayerOfKind(kind) != LayerCloud {
		return CapacityRegion{}, fmt.Errorf("%w: cloud_source_kind must be one of %s", ErrInvalid, strings.Join(CloudSourceKinds, ", "))
	}
	var id string
	if err := s.db.QueryRowContext(ctx, `INSERT INTO capacity_regions (code, name, cloud_source_kind) VALUES ($1, $2, $3) RETURNING id`, code, strings.TrimSpace(name), kind).Scan(&id); err != nil {
		return CapacityRegion{}, mapErr(err)
	}
	return s.GetCapacityRegion(ctx, id)
}

// DeleteCapacityRegion removes a region, its zones, pools, resources,
// placements and history.
func (s *Store) DeleteCapacityRegion(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM capacity_regions WHERE id = $1`, id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetCapacityZone returns one zone with its pools.
func (s *Store) GetCapacityZone(ctx context.Context, id string) (CapacityZone, error) {
	z, err := scanCapacityZone(s.db.QueryRowContext(ctx, `SELECT `+capacityZoneColumns+` FROM capacity_zones z JOIN capacity_regions r ON r.id = z.region_id WHERE z.id = $1`, id))
	if err != nil {
		return z, err
	}
	z.Pools, err = s.ListCapacityPools(ctx, id)
	return z, err
}

// CreateCapacityZone adds a zone to a region. It creates NO pools: a pool is
// a set of machines somebody bought, with a name and a vector, and inventing
// seven empty ones is what made "unset" look like a defect instead of a fact.
// The first zone of a region is its default; makeDefault moves the default
// onto this zone.
func (s *Store) CreateCapacityZone(ctx context.Context, regionID, code, name string, makeDefault bool) (CapacityZone, error) {
	code = normCode(code)
	if code == "" {
		return CapacityZone{}, fmt.Errorf("%w: code is required", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CapacityZone{}, err
	}
	defer tx.Rollback()
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM capacity_zones WHERE region_id = $1`, regionID).Scan(&existing); err != nil {
		return CapacityZone{}, mapErr(err)
	}
	var regionExists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM capacity_regions WHERE id = $1 FOR UPDATE)`, regionID).Scan(&regionExists); err != nil {
		return CapacityZone{}, mapErr(err)
	}
	if !regionExists {
		return CapacityZone{}, ErrNotFound
	}
	isDefault := makeDefault || existing == 0
	if isDefault {
		if _, err := tx.ExecContext(ctx, `UPDATE capacity_zones SET is_default = false WHERE region_id = $1 AND is_default`, regionID); err != nil {
			return CapacityZone{}, mapErr(err)
		}
	}
	var id string
	if err := tx.QueryRowContext(ctx, `INSERT INTO capacity_zones (region_id, code, name, is_default) VALUES ($1, $2, $3, $4) RETURNING id`, regionID, code, strings.TrimSpace(name), isDefault).Scan(&id); err != nil {
		return CapacityZone{}, mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return CapacityZone{}, err
	}
	return s.GetCapacityZone(ctx, id)
}

// DeleteCapacityZone removes a zone with its pools, their resources,
// placements and history. When it was the region's default, the oldest
// remaining zone becomes default so unknown-zone consumption always has
// somewhere to land.
func (s *Store) DeleteCapacityZone(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var regionID string
	var wasDefault bool
	if err := tx.QueryRowContext(ctx, `DELETE FROM capacity_zones WHERE id = $1 RETURNING region_id, is_default`, id).Scan(&regionID, &wasDefault); err != nil {
		return mapErr(err)
	}
	if wasDefault {
		if _, err := tx.ExecContext(ctx, `UPDATE capacity_zones SET is_default = true WHERE id = (SELECT id FROM capacity_zones WHERE region_id = $1 ORDER BY created_at, code LIMIT 1)`, regionID); err != nil {
			return mapErr(err)
		}
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// resource kinds
// ---------------------------------------------------------------------------

// ListCapacityResourceKinds returns every resource kind in display order.
func (s *Store) ListCapacityResourceKinds(ctx context.Context) ([]CapacityResourceKind, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT resource, label, unit, position FROM capacity_resource_kinds ORDER BY position, resource`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []CapacityResourceKind{}
	for rows.Next() {
		var k CapacityResourceKind
		if err := rows.Scan(&k.Key, &k.Label, &k.Unit, &k.Position); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// PutCapacityResourceKind names a resource kind: its label and the unit its
// amounts count in. A kind a pool declared without one exists already with
// the key as its label; this is how an operator gives it words.
func (s *Store) PutCapacityResourceKind(ctx context.Context, resource, label, unit string) (CapacityResourceKind, error) {
	key := capacity.NormResource(resource)
	if key == "" {
		return CapacityResourceKind{}, fmt.Errorf("%w: resource is required", ErrInvalid)
	}
	label, unit = strings.TrimSpace(label), strings.TrimSpace(unit)
	if label == "" {
		label = key
	}
	var k CapacityResourceKind
	err := s.db.QueryRowContext(ctx, `INSERT INTO capacity_resource_kinds (resource, label, unit, position) VALUES ($1, $2, $3, 1000)
		ON CONFLICT (resource) DO UPDATE SET label = EXCLUDED.label, unit = EXCLUDED.unit
		RETURNING resource, label, unit, position`, key, label, unit).Scan(&k.Key, &k.Label, &k.Unit, &k.Position)
	if err != nil {
		return CapacityResourceKind{}, mapErr(err)
	}
	return k, nil
}

// ---------------------------------------------------------------------------
// pools
// ---------------------------------------------------------------------------

// CapacityPoolInput is a pool as the operator states it.
type CapacityPoolInput struct {
	Name         string                 `json:"name"`
	Machines     Decimal                `json:"machines"`
	LeadTimeDays int                    `json:"lead_time_days"`
	Note         string                 `json:"note"`
	Resources    []CapacityPoolResource `json:"resources"`
}

const capacityPoolColumns = `p.id, p.zone_id, z.code, r.code, p.name, p.machines::text, p.lead_time_days, p.source, p.note, p.updated_by, p.updated_at, p.created_at`

func scanCapacityPool(row interface{ Scan(...any) error }) (CapacityPool, error) {
	var p CapacityPool
	var machines string
	if err := row.Scan(&p.ID, &p.ZoneID, &p.ZoneCode, &p.RegionCode, &p.Name, &machines, &p.LeadTimeDays, &p.Source, &p.Note, &p.UpdatedBy, &p.UpdatedAt, &p.CreatedAt); err != nil {
		return p, mapErr(err)
	}
	p.Machines = Decimal(machines)
	p.UpdatedAt, p.CreatedAt = p.UpdatedAt.UTC(), p.CreatedAt.UTC()
	p.Resources = []CapacityPoolResource{}
	return p, nil
}

// loadPoolResources fills the Resources of the given pools in one query.
func (s *Store) loadPoolResources(ctx context.Context, pools []CapacityPool) error {
	if len(pools) == 0 {
		return nil
	}
	idx := map[string]int{}
	ids := make([]string, 0, len(pools))
	for i := range pools {
		idx[pools[i].ID] = i
		ids = append(ids, pools[i].ID)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT pr.pool_id, pr.resource, k.label, k.unit, pr.per_machine::text, pr.reserve::text, pr.overcommit_ratio::text
		FROM capacity_pool_resources pr JOIN capacity_resource_kinds k ON k.resource = pr.resource
		WHERE pr.pool_id = ANY($1) ORDER BY k.position, pr.resource`, pq.Array(ids))
	if err != nil {
		return mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var poolID string
		var r CapacityPoolResource
		var per, reserve, ratio string
		if err := rows.Scan(&poolID, &r.Resource, &r.Label, &r.Unit, &per, &reserve, &ratio); err != nil {
			return err
		}
		r.PerMachine, r.Reserve, r.OvercommitRatio = Decimal(per), Decimal(reserve), Decimal(ratio)
		if i, ok := idx[poolID]; ok {
			pools[i].Resources = append(pools[i].Resources, r)
		}
	}
	return rows.Err()
}

// ListCapacityPools returns a zone's pools with their resource vectors, by
// name; ErrNotFound for an unknown zone.
func (s *Store) ListCapacityPools(ctx context.Context, zoneID string) ([]CapacityPool, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM capacity_zones WHERE id = $1)`, zoneID).Scan(&exists); err != nil {
		return nil, mapErr(err)
	}
	if !exists {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+capacityPoolColumns+` FROM capacity_pools p
		JOIN capacity_zones z ON z.id = p.zone_id JOIN capacity_regions r ON r.id = z.region_id
		WHERE p.zone_id = $1 ORDER BY p.name`, zoneID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []CapacityPool{}
	for rows.Next() {
		p, err := scanCapacityPool(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.loadPoolResources(ctx, out)
}

// GetCapacityPool returns one pool with its resources.
func (s *Store) GetCapacityPool(ctx context.Context, id string) (CapacityPool, error) {
	p, err := scanCapacityPool(s.db.QueryRowContext(ctx, `SELECT `+capacityPoolColumns+` FROM capacity_pools p
		JOIN capacity_zones z ON z.id = p.zone_id JOIN capacity_regions r ON r.id = z.region_id WHERE p.id = $1`, id))
	if err != nil {
		return p, err
	}
	one := []CapacityPool{p}
	if err := s.loadPoolResources(ctx, one); err != nil {
		return p, err
	}
	return one[0], nil
}

// validNonNegativeDecimal reports whether s is a numeric literal >= 0.
func validNonNegativeDecimal(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && decimalShape.MatchString(s) && !strings.HasPrefix(s, "-")
}

// cleanPoolInput validates a pool as stated and returns it normalised.
func cleanPoolInput(in CapacityPoolInput) (CapacityPoolInput, error) {
	out := CapacityPoolInput{Name: strings.TrimSpace(in.Name), LeadTimeDays: in.LeadTimeDays, Note: strings.TrimSpace(in.Note)}
	if out.Name == "" {
		return out, fmt.Errorf("%w: name is required: what this set of machines is called, e.g. m7n-a", ErrInvalid)
	}
	machines := strings.TrimSpace(string(in.Machines))
	if machines == "" {
		machines = "0"
	}
	if !validNonNegativeDecimal(machines) {
		return out, fmt.Errorf("%w: machines must be a non-negative number", ErrInvalid)
	}
	out.Machines = Decimal(machines)
	if out.LeadTimeDays < 0 {
		return out, fmt.Errorf("%w: lead_time_days must be a non-negative number of days", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, r := range in.Resources {
		key := capacity.NormResource(r.Resource)
		if key == "" {
			return out, fmt.Errorf("%w: every resource needs a key, e.g. vcpu or memory_gib", ErrInvalid)
		}
		if seen[key] {
			return out, fmt.Errorf("%w: resource %s is listed twice", ErrInvalid, key)
		}
		seen[key] = true
		per := strings.TrimSpace(string(r.PerMachine))
		if per == "" {
			per = "0"
		}
		reserve := strings.TrimSpace(string(r.Reserve))
		if reserve == "" {
			reserve = "0"
		}
		ratio := strings.TrimSpace(string(r.OvercommitRatio))
		if ratio == "" {
			ratio = "1"
		}
		if !validNonNegativeDecimal(per) {
			return out, fmt.Errorf("%w: %s per_machine must be a non-negative number", ErrInvalid, key)
		}
		if !validNonNegativeDecimal(reserve) {
			return out, fmt.Errorf("%w: %s reserve must be a non-negative number", ErrInvalid, key)
		}
		if !validNonNegativeDecimal(ratio) || ratOf(Decimal(ratio)).Sign() == 0 {
			return out, fmt.Errorf("%w: %s overcommit_ratio must be a positive number (1 is no oversubscription)", ErrInvalid, key)
		}
		out.Resources = append(out.Resources, CapacityPoolResource{Resource: key, PerMachine: Decimal(per), Reserve: Decimal(reserve), OvercommitRatio: Decimal(ratio)})
	}
	if len(out.Resources) == 0 {
		return out, fmt.Errorf("%w: a pool holds at least one resource: a machine with nothing in it is not capacity", ErrInvalid)
	}
	return out, nil
}

// CreateCapacityPool adds a pool to a zone and records its first size.
func (s *Store) CreateCapacityPool(ctx context.Context, zoneID string, in CapacityPoolInput, actor string) (CapacityPool, error) {
	clean, err := cleanPoolInput(in)
	if err != nil {
		return CapacityPool{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CapacityPool{}, err
	}
	defer tx.Rollback()
	var zoneExists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM capacity_zones WHERE id = $1)`, zoneID).Scan(&zoneExists); err != nil {
		return CapacityPool{}, mapErr(err)
	}
	if !zoneExists {
		return CapacityPool{}, ErrNotFound
	}
	var id string
	if err := tx.QueryRowContext(ctx, `INSERT INTO capacity_pools (zone_id, name, machines, lead_time_days, source, note, updated_by) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		zoneID, clean.Name, string(clean.Machines), clean.LeadTimeDays, capacity.SourceManual, clean.Note, actor).Scan(&id); err != nil {
		return CapacityPool{}, mapErr(err)
	}
	if err := writePoolResources(ctx, tx, id, clean, actor); err != nil {
		return CapacityPool{}, err
	}
	if err := tx.Commit(); err != nil {
		return CapacityPool{}, err
	}
	return s.GetCapacityPool(ctx, id)
}

// SetCapacityPool replaces a pool's size and policy, records one history row
// per resource and returns the pool as it was before (for the audit entry).
func (s *Store) SetCapacityPool(ctx context.Context, id string, in CapacityPoolInput, actor string) (pool, previous CapacityPool, err error) {
	clean, err := cleanPoolInput(in)
	if err != nil {
		return CapacityPool{}, CapacityPool{}, err
	}
	previous, err = s.GetCapacityPool(ctx, id)
	if err != nil {
		return CapacityPool{}, CapacityPool{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CapacityPool{}, previous, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE capacity_pools SET name = $2, machines = $3, lead_time_days = $4, source = $5, note = $6, updated_by = $7, updated_at = now() WHERE id = $1`,
		id, clean.Name, string(clean.Machines), clean.LeadTimeDays, capacity.SourceManual, clean.Note, actor)
	if err != nil {
		return CapacityPool{}, previous, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return CapacityPool{}, previous, ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM capacity_pool_resources WHERE pool_id = $1`, id); err != nil {
		return CapacityPool{}, previous, mapErr(err)
	}
	if err := writePoolResources(ctx, tx, id, clean, actor); err != nil {
		return CapacityPool{}, previous, err
	}
	if err := tx.Commit(); err != nil {
		return CapacityPool{}, previous, err
	}
	pool, err = s.GetCapacityPool(ctx, id)
	return pool, previous, err
}

// writePoolResources inserts the resource vector and one history row per
// resource. It is the only place a pool's size is written.
func writePoolResources(ctx context.Context, tx txExec, poolID string, clean CapacityPoolInput, actor string) error {
	keys := make([]string, 0, len(clean.Resources))
	for _, r := range clean.Resources {
		keys = append(keys, r.Resource)
	}
	for _, k := range keys {
		kind := capacity.KindOf(k)
		if _, err := tx.ExecContext(ctx, `INSERT INTO capacity_resource_kinds (resource, label, unit, position) VALUES ($1, $2, $3, $4) ON CONFLICT (resource) DO NOTHING`,
			k, kind.Label, kind.Unit, kind.Position); err != nil {
			return mapErr(err)
		}
	}
	machines := ratOf(clean.Machines)
	for _, r := range clean.Resources {
		if _, err := tx.ExecContext(ctx, `INSERT INTO capacity_pool_resources (pool_id, resource, per_machine, reserve, overcommit_ratio) VALUES ($1, $2, $3, $4, $5)`,
			poolID, r.Resource, string(r.PerMachine), string(r.Reserve), string(r.OvercommitRatio)); err != nil {
			return mapErr(err)
		}
		raw := decOf(new(big.Rat).Mul(machines, ratOf(r.PerMachine)))
		if _, err := tx.ExecContext(ctx, `INSERT INTO capacity_pool_history (pool_id, resource, machines, per_machine, reserve, overcommit_ratio, total, source, note, changed_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			poolID, r.Resource, string(clean.Machines), string(r.PerMachine), string(r.Reserve), string(r.OvercommitRatio), string(raw), capacity.SourceManual, clean.Note, actor); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

// txExec is the part of *sql.Tx writePoolResources uses.
type txExec interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// DeleteCapacityPool removes a pool with its resources, placements and
// history.
func (s *Store) DeleteCapacityPool(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM capacity_pools WHERE id = $1`, id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListCapacityPoolHistory returns a pool's size changes, newest first.
func (s *Store) ListCapacityPoolHistory(ctx context.Context, poolID string, limit int) ([]CapacityPoolChange, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, pool_id, resource, machines::text, per_machine::text, reserve::text, overcommit_ratio::text, total::text, source, note, changed_by, changed_at
		FROM capacity_pool_history WHERE pool_id = $1 ORDER BY changed_at DESC, id DESC LIMIT $2`, poolID, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []CapacityPoolChange{}
	for rows.Next() {
		var c CapacityPoolChange
		var machines, per, reserve, ratio, total string
		if err := rows.Scan(&c.ID, &c.PoolID, &c.Resource, &machines, &per, &reserve, &ratio, &total, &c.Source, &c.Note, &c.ChangedBy, &c.ChangedAt); err != nil {
			return nil, err
		}
		c.Machines, c.PerMachine, c.Reserve, c.OvercommitRatio, c.Total = Decimal(machines), Decimal(per), Decimal(reserve), Decimal(ratio), Decimal(total)
		c.ChangedAt = c.ChangedAt.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// shapes
// ---------------------------------------------------------------------------

// ListCapacityShapes returns the stored shapes, one per SKU, by SKU.
func (s *Store) ListCapacityShapes(ctx context.Context) ([]CapacityShape, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sku, resource, amount_per_unit::text, source, updated_at FROM sku_shapes ORDER BY sku, resource`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []CapacityShape{}
	idx := map[string]int{}
	for rows.Next() {
		var sku, res, amount, source string
		var at time.Time
		if err := rows.Scan(&sku, &res, &amount, &source, &at); err != nil {
			return nil, err
		}
		i, ok := idx[sku]
		if !ok {
			i = len(out)
			idx[sku] = i
			out = append(out, CapacityShape{SKU: sku, Resources: map[string]Decimal{}, Source: source})
		}
		out[i].Resources[res] = Decimal(amount)
		at = at.UTC()
		if out[i].UpdatedAt == nil || at.After(*out[i].UpdatedAt) {
			out[i].UpdatedAt = &at
		}
		// A SKU with one edited resource reads as manual.
		if source == capacity.SourceManual {
			out[i].Source = source
		}
	}
	return out, rows.Err()
}

// PutCapacityShape makes the given resources THE shape of a SKU (PUT
// semantics): resources absent or 0 are removed, the rest written as manual.
// An empty map removes the shape. ErrInvalid names a bad amount.
func (s *Store) PutCapacityShape(ctx context.Context, sku string, resources map[string]Decimal) (CapacityShape, error) {
	sku = strings.TrimSpace(sku)
	if sku == "" {
		return CapacityShape{}, fmt.Errorf("%w: sku is required", ErrInvalid)
	}
	clean := map[string]string{}
	for res, amount := range resources {
		key := capacity.NormResource(res)
		if key == "" {
			return CapacityShape{}, fmt.Errorf("%w: every resource needs a key, e.g. vcpu or memory_gib", ErrInvalid)
		}
		a := strings.TrimSpace(string(amount))
		if a == "" || (decimalShape.MatchString(a) && ratOf(Decimal(a)).Sign() == 0) {
			continue
		}
		if !validNonNegativeDecimal(a) {
			return CapacityShape{}, fmt.Errorf("%w: %s amount must be a non-negative number", ErrInvalid, key)
		}
		clean[key] = a
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CapacityShape{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM sku_shapes WHERE sku = $1`, sku); err != nil {
		return CapacityShape{}, mapErr(err)
	}
	for res, a := range clean {
		kind := capacity.KindOf(res)
		if _, err := tx.ExecContext(ctx, `INSERT INTO capacity_resource_kinds (resource, label, unit, position) VALUES ($1, $2, $3, $4) ON CONFLICT (resource) DO NOTHING`,
			res, kind.Label, kind.Unit, kind.Position); err != nil {
			return CapacityShape{}, mapErr(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO sku_shapes (sku, resource, amount_per_unit, source) VALUES ($1, $2, $3, $4)`, sku, res, a, capacity.SourceManual); err != nil {
			return CapacityShape{}, mapErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return CapacityShape{}, err
	}
	out := CapacityShape{SKU: sku, Resources: map[string]Decimal{}, Source: capacity.SourceManual}
	for res, a := range clean {
		out.Resources[res] = Decimal(a)
	}
	now := time.Now().UTC()
	out.UpdatedAt = &now
	return out, nil
}

// ---------------------------------------------------------------------------
// placements
// ---------------------------------------------------------------------------

const capacityPlacementColumns = `pl.pool_id, p.name, p.zone_id, z.code, r.code, pl.sku, pl.class, pl.updated_by, pl.updated_at`

func scanCapacityPlacement(row interface{ Scan(...any) error }) (CapacityPlacement, error) {
	var pl CapacityPlacement
	if err := row.Scan(&pl.PoolID, &pl.PoolName, &pl.ZoneID, &pl.ZoneCode, &pl.RegionCode, &pl.SKU, &pl.Class, &pl.UpdatedBy, &pl.UpdatedAt); err != nil {
		return pl, mapErr(err)
	}
	pl.UpdatedAt = pl.UpdatedAt.UTC()
	return pl, nil
}

// ListCapacityPlacements returns every placement with its pool, zone and
// region, by region / zone / pool / SKU.
func (s *Store) ListCapacityPlacements(ctx context.Context) ([]CapacityPlacement, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+capacityPlacementColumns+` FROM capacity_placements pl
		JOIN capacity_pools p ON p.id = pl.pool_id
		JOIN capacity_zones z ON z.id = p.zone_id
		JOIN capacity_regions r ON r.id = z.region_id
		ORDER BY r.code, z.code, p.name, pl.sku`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []CapacityPlacement{}
	for rows.Next() {
		pl, err := scanCapacityPlacement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, pl)
	}
	return out, rows.Err()
}

// PutCapacityPlacement places a SKU on a pool at a class.
//
// Two things are validated, and both are the kind of mistake that otherwise
// reads as a silent zero:
//
//   - THE SHAPE MUST TOUCH THE POOL. A SKU whose vector names no resource the
//     pool holds would consume nothing there, which is never what the
//     operator meant.
//   - THE CLASS IS THE SKU'S, NOT THE PLACEMENT'S ALONE. A SKU is a product
//     at a price, and a price is quoted for one class; placing the same SKU
//     as guaranteed on one pool and spot on another would split its
//     consumption across two classes whose arithmetic means opposite things.
//     A second placement must carry the same class.
func (s *Store) PutCapacityPlacement(ctx context.Context, poolID, sku, class, actor string) (CapacityPlacement, error) {
	sku = strings.TrimSpace(sku)
	class = strings.ToLower(strings.TrimSpace(class))
	if sku == "" {
		return CapacityPlacement{}, fmt.Errorf("%w: sku is required", ErrInvalid)
	}
	if !capacity.ValidClass(class) {
		return CapacityPlacement{}, fmt.Errorf("%w: class must be one of %s", ErrInvalid, strings.Join(capacity.ClassKeys(), ", "))
	}
	pool, err := s.GetCapacityPool(ctx, poolID)
	if err != nil {
		return CapacityPlacement{}, err
	}
	shape, err := s.shapeOf(ctx, sku)
	if err != nil {
		return CapacityPlacement{}, err
	}
	if len(shape.Resources) == 0 {
		return CapacityPlacement{}, fmt.Errorf("%w: %s has no shape: say how much of each resource one unit consumes before placing it", ErrInvalid, sku)
	}
	held := map[string]bool{}
	for _, r := range pool.Resources {
		held[r.Resource] = true
	}
	touches := false
	for res := range shape.Resources {
		if held[res] {
			touches = true
			break
		}
	}
	if !touches {
		want := make([]string, 0, len(shape.Resources))
		for res := range shape.Resources {
			want = append(want, res)
		}
		sort.Strings(want)
		has := make([]string, 0, len(pool.Resources))
		for _, r := range pool.Resources {
			has = append(has, r.Resource)
		}
		return CapacityPlacement{}, fmt.Errorf("%w: %s consumes %s and pool %s holds %s: the shape and the pool share no resource", ErrInvalid, sku, strings.Join(want, ", "), pool.Name, strings.Join(has, ", "))
	}
	var otherClass, otherPool string
	err = s.db.QueryRowContext(ctx, `SELECT pl.class, p.name FROM capacity_placements pl JOIN capacity_pools p ON p.id = pl.pool_id
		WHERE pl.sku = $1 AND pl.pool_id <> $2 LIMIT 1`, sku, poolID).Scan(&otherClass, &otherPool)
	switch {
	case err == nil && otherClass != class:
		return CapacityPlacement{}, fmt.Errorf("%w: %s is already placed on %s as %s; a SKU is one product at one price and carries one class", ErrInvalid, sku, otherPool, otherClass)
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return CapacityPlacement{}, mapErr(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO capacity_placements (pool_id, sku, class, updated_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT (pool_id, sku) DO UPDATE SET class = EXCLUDED.class, updated_by = EXCLUDED.updated_by, updated_at = now()`, poolID, sku, class, actor); err != nil {
		return CapacityPlacement{}, mapErr(err)
	}
	return scanCapacityPlacement(s.db.QueryRowContext(ctx, `SELECT `+capacityPlacementColumns+` FROM capacity_placements pl
		JOIN capacity_pools p ON p.id = pl.pool_id JOIN capacity_zones z ON z.id = p.zone_id JOIN capacity_regions r ON r.id = z.region_id
		WHERE pl.pool_id = $1 AND pl.sku = $2`, poolID, sku))
}

// DeleteCapacityPlacement removes a placement; ErrNotFound when there was none.
func (s *Store) DeleteCapacityPlacement(ctx context.Context, poolID, sku string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM capacity_placements WHERE pool_id = $1 AND sku = $2`, poolID, strings.TrimSpace(sku))
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// shapeOf is the stored shape of a SKU, or the one its name implies.
func (s *Store) shapeOf(ctx context.Context, sku string) (CapacityShape, error) {
	out := CapacityShape{SKU: sku, Resources: map[string]Decimal{}}
	rows, err := s.db.QueryContext(ctx, `SELECT resource, amount_per_unit::text, source FROM sku_shapes WHERE sku = $1`, sku)
	if err != nil {
		return out, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var res, amount, source string
		if err := rows.Scan(&res, &amount, &source); err != nil {
			return out, err
		}
		out.Resources[res] = Decimal(amount)
		out.Source = source
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if len(out.Resources) > 0 {
		return out, nil
	}
	if d := capacity.Derive(sku); d != nil {
		out.Source = capacity.SourceDerived
		for res, a := range d {
			out.Resources[res] = Decimal(a)
		}
	}
	return out, nil
}
