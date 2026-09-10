package store

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/capacity"
)

// Capacity management (DESIGN.md §11, EPIC #6867, founder requirement
// 2026-09-11: "capacity management for the underlying regions — overall
// capacity information of underlying AZs and regions as well for each SKU;
// initially static, the admin defines the capacity; later from integrations").
//
// The model is static-first: a REGION holds ZONES, a zone holds one POOL per
// resource family (capacity.Families) whose TOTAL the operator types until a
// capacity collector fills it. Everything else is DERIVED from what this
// product already meters: a SKU FOOTPRINT says how much of each family one
// unit of the SKU consumes, so the latest complete hour's usage records,
// multiplied through the footprints, are the zone's CONSUMED capacity —
// there is no second meter and no capacity ledger to keep in step with the
// usage ledger. RESERVED is carried at 0 with its column and wire key in
// place for proposals and plans to fill later.
//
// Every change of a pool total is audited (the API writes capacity.pool)
// and kept in capacity_pool_history, so a total can be read back to the day
// it was entered.

// capacityFamilyCheckSQL is the family list as a SQL CHECK, built from
// capacity.Families so the constraint and the Go list cannot disagree.
func capacityFamilyCheckSQL() string {
	keys := capacity.FamilyKeys()
	for i, k := range keys {
		keys[i] = sqlQuote(k)
	}
	return "CHECK (family IN (" + strings.Join(keys, ",") + "))"
}

// CapacityFootprintSeedSQL inserts the seed footprints (capacity.Seed): the
// National Cloud list SKUs whose footprint the name states. ON CONFLICT DO
// NOTHING, so a footprint the operator has since edited is never overwritten.
// The test database helper re-runs it after wiping sku_footprints, so every
// test starts from the seeded rows.
func CapacityFootprintSeedSQL() string {
	var b strings.Builder
	for _, r := range capacity.Seed() {
		fmt.Fprintf(&b, "INSERT INTO sku_footprints (sku, family, amount, source) VALUES (%s, %s, %s, %s) ON CONFLICT (sku, family) DO NOTHING;\n",
			sqlQuote(r.SKU), sqlQuote(r.Family), r.Amount, sqlQuote(capacity.SourceSeed))
	}
	return b.String()
}

// capacityMigrationSQL is the capacity schema, one transaction. Appended at
// the END of migrations: they are positional.
func capacityMigrationSQL() string {
	check := capacityFamilyCheckSQL()
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
` + CapacityFootprintSeedSQL()
}

// MigrationCapacity is the schema_migrations version of the capacity
// migration, located by content like the others so a migration appended
// after it cannot move this version.
var MigrationCapacity = func() int {
	want := capacityMigrationSQL()
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
// its region. CloudSourceKind says which collector will fill its totals.
type CapacityRegion struct {
	ID              string         `json:"id"`
	Code            string         `json:"code"`
	Name            string         `json:"name"`
	CloudSourceKind string         `json:"cloud_source_kind"`
	CreatedAt       time.Time      `json:"created_at"`
	Zones           []CapacityZone `json:"zones"`
}

// CapacityZone is one availability zone of a region. The DEFAULT zone of a
// region receives the consumption of records whose zone is not known —
// the inventory row carries no availability_zone — flagged as such.
type CapacityZone struct {
	ID         string         `json:"id"`
	RegionID   string         `json:"region_id"`
	RegionCode string         `json:"region_code,omitempty"`
	Code       string         `json:"code"`
	Name       string         `json:"name"`
	IsDefault  bool           `json:"is_default"`
	CreatedAt  time.Time      `json:"created_at"`
	Pools      []CapacityPool `json:"pools,omitempty"`
}

// CapacityPool is a zone's total of one family. Total is what the operator
// entered (source manual) or a collector reported; Reserved is 0 until
// proposals and plans fill it.
type CapacityPool struct {
	ID        string    `json:"id"`
	ZoneID    string    `json:"zone_id"`
	Family    string    `json:"family"`
	Total     Decimal   `json:"total"`
	Reserved  Decimal   `json:"reserved"`
	Source    string    `json:"source"`
	Note      string    `json:"note"`
	UpdatedBy string    `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CapacityPoolChange is one entry of a pool's total history.
type CapacityPoolChange struct {
	ID        int64     `json:"id"`
	PoolID    string    `json:"pool_id"`
	Total     Decimal   `json:"total"`
	Source    string    `json:"source"`
	Note      string    `json:"note"`
	ChangedBy string    `json:"changed_by"`
	ChangedAt time.Time `json:"changed_at"`
}

// SKUFootprint is how much of each family ONE unit of a SKU consumes.
// Source is manual / seed for stored rows, derived for a footprint the SKU
// name implies that has no row (capacity.Derive).
type SKUFootprint struct {
	SKU       string             `json:"sku"`
	Families  map[string]Decimal `json:"families"`
	Source    string             `json:"source"`
	UpdatedAt *time.Time         `json:"updated_at,omitempty"`
}

// SKUCap is an optional direct ceiling on one SKU in one zone, in units of
// the SKU, on top of what the family pools allow.
type SKUCap struct {
	ZoneID     string    `json:"zone_id"`
	ZoneCode   string    `json:"zone_code,omitempty"`
	RegionCode string    `json:"region_code,omitempty"`
	SKU        string    `json:"sku"`
	Total      Decimal   `json:"total"`
	UpdatedBy  string    `json:"updated_by"`
	UpdatedAt  time.Time `json:"updated_at"`
}

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

// DeleteCapacityRegion removes a region, its zones, pools, history and caps.
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

// CreateCapacityZone adds a zone to a region and creates its seven pools at
// total 0. The first zone of a region is its default; makeDefault moves the
// default onto this zone.
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
	for _, fam := range capacity.FamilyKeys() {
		if _, err := tx.ExecContext(ctx, `INSERT INTO capacity_pools (zone_id, family) VALUES ($1, $2)`, id, fam); err != nil {
			return CapacityZone{}, mapErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return CapacityZone{}, err
	}
	return s.GetCapacityZone(ctx, id)
}

// DeleteCapacityZone removes a zone with its pools, history and caps. When
// it was the region's default, the oldest remaining zone becomes default so
// unknown-zone consumption always has somewhere to land.
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
// pools
// ---------------------------------------------------------------------------

const capacityPoolColumns = `p.id, p.zone_id, p.family, p.total::text, p.reserved::text, p.source, p.note, p.updated_by, p.updated_at`

func scanCapacityPool(row interface{ Scan(...any) error }) (CapacityPool, error) {
	var p CapacityPool
	var total, reserved string
	if err := row.Scan(&p.ID, &p.ZoneID, &p.Family, &total, &reserved, &p.Source, &p.Note, &p.UpdatedBy, &p.UpdatedAt); err != nil {
		return p, mapErr(err)
	}
	p.Total, p.Reserved = Decimal(total), Decimal(reserved)
	p.UpdatedAt = p.UpdatedAt.UTC()
	return p, nil
}

// familyOrder sorts pools in capacity.Families order.
func familyOrder(fam string) int {
	for i, f := range capacity.Families {
		if f.Key == fam {
			return i
		}
	}
	return len(capacity.Families)
}

// ListCapacityPools returns a zone's pools in family order; ErrNotFound for
// an unknown zone.
func (s *Store) ListCapacityPools(ctx context.Context, zoneID string) ([]CapacityPool, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM capacity_zones WHERE id = $1)`, zoneID).Scan(&exists); err != nil {
		return nil, mapErr(err)
	}
	if !exists {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+capacityPoolColumns+` FROM capacity_pools p WHERE p.zone_id = $1`, zoneID)
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
	sort.SliceStable(out, func(i, j int) bool { return familyOrder(out[i].Family) < familyOrder(out[j].Family) })
	return out, nil
}

// GetCapacityPool returns one pool.
func (s *Store) GetCapacityPool(ctx context.Context, id string) (CapacityPool, error) {
	return scanCapacityPool(s.db.QueryRowContext(ctx, `SELECT `+capacityPoolColumns+` FROM capacity_pools p WHERE p.id = $1`, id))
}

// validNonNegativeDecimal reports whether s is a numeric literal ≥ 0.
func validNonNegativeDecimal(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && decimalShape.MatchString(s) && !strings.HasPrefix(s, "-")
}

// SetCapacityPoolTotal writes a pool's total with the operator's note,
// records the change in capacity_pool_history and returns the pool with the
// total it had before (for the audit entry). source names who set it:
// "manual" for the console, a collector's name later.
func (s *Store) SetCapacityPoolTotal(ctx context.Context, id string, total Decimal, note, source, actor string) (pool CapacityPool, previous Decimal, err error) {
	if !validNonNegativeDecimal(string(total)) {
		return CapacityPool{}, "", fmt.Errorf("%w: total must be a non-negative number", ErrInvalid)
	}
	if source = strings.TrimSpace(source); source == "" {
		source = capacity.SourceManual
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CapacityPool{}, "", err
	}
	defer tx.Rollback()
	var prev string
	if err := tx.QueryRowContext(ctx, `SELECT total::text FROM capacity_pools WHERE id = $1 FOR UPDATE`, id).Scan(&prev); err != nil {
		return CapacityPool{}, "", mapErr(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE capacity_pools SET total = $2, source = $3, note = $4, updated_by = $5, updated_at = now() WHERE id = $1`,
		id, strings.TrimSpace(string(total)), source, strings.TrimSpace(note), actor); err != nil {
		return CapacityPool{}, "", mapErr(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO capacity_pool_history (pool_id, total, source, note, changed_by) VALUES ($1, $2, $3, $4, $5)`,
		id, strings.TrimSpace(string(total)), source, strings.TrimSpace(note), actor); err != nil {
		return CapacityPool{}, "", mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return CapacityPool{}, "", err
	}
	pool, err = s.GetCapacityPool(ctx, id)
	return pool, Decimal(prev), err
}

// ListCapacityPoolHistory returns a pool's total changes, newest first.
func (s *Store) ListCapacityPoolHistory(ctx context.Context, poolID string, limit int) ([]CapacityPoolChange, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, pool_id, total::text, source, note, changed_by, changed_at FROM capacity_pool_history WHERE pool_id = $1 ORDER BY changed_at DESC, id DESC LIMIT $2`, poolID, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []CapacityPoolChange{}
	for rows.Next() {
		var c CapacityPoolChange
		var total string
		if err := rows.Scan(&c.ID, &c.PoolID, &total, &c.Source, &c.Note, &c.ChangedBy, &c.ChangedAt); err != nil {
			return nil, err
		}
		c.Total = Decimal(total)
		c.ChangedAt = c.ChangedAt.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// footprints and caps
// ---------------------------------------------------------------------------

// ListSKUFootprints returns the stored footprints, one per SKU, by SKU.
func (s *Store) ListSKUFootprints(ctx context.Context) ([]SKUFootprint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sku, family, amount::text, source, updated_at FROM sku_footprints ORDER BY sku, family`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []SKUFootprint{}
	idx := map[string]int{}
	for rows.Next() {
		var sku, fam, amount, source string
		var at time.Time
		if err := rows.Scan(&sku, &fam, &amount, &source, &at); err != nil {
			return nil, err
		}
		i, ok := idx[sku]
		if !ok {
			i = len(out)
			idx[sku] = i
			out = append(out, SKUFootprint{SKU: sku, Families: map[string]Decimal{}, Source: source})
		}
		out[i].Families[fam] = Decimal(amount)
		at = at.UTC()
		if out[i].UpdatedAt == nil || at.After(*out[i].UpdatedAt) {
			out[i].UpdatedAt = &at
		}
		// A SKU with one edited family reads as manual.
		if source == capacity.SourceManual {
			out[i].Source = source
		}
	}
	return out, rows.Err()
}

// PutSKUFootprint makes the given families THE footprint of a SKU (PUT
// semantics): families absent or 0 are removed, the rest written as manual.
// An empty map removes the footprint. ErrInvalid names a bad family or amount.
func (s *Store) PutSKUFootprint(ctx context.Context, sku string, families map[string]Decimal) (SKUFootprint, error) {
	sku = strings.TrimSpace(sku)
	if sku == "" {
		return SKUFootprint{}, fmt.Errorf("%w: sku is required", ErrInvalid)
	}
	clean := map[string]string{}
	for fam, amount := range families {
		fam = strings.TrimSpace(fam)
		if !capacity.ValidFamily(fam) {
			return SKUFootprint{}, fmt.Errorf("%w: unknown family %q; families are %s", ErrInvalid, fam, strings.Join(capacity.FamilyKeys(), ", "))
		}
		a := strings.TrimSpace(string(amount))
		if a == "" || ratOf(Decimal(a)).Sign() == 0 {
			continue
		}
		if !validNonNegativeDecimal(a) {
			return SKUFootprint{}, fmt.Errorf("%w: %s amount must be a non-negative number", ErrInvalid, fam)
		}
		clean[fam] = a
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SKUFootprint{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM sku_footprints WHERE sku = $1`, sku); err != nil {
		return SKUFootprint{}, mapErr(err)
	}
	for fam, a := range clean {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sku_footprints (sku, family, amount, source) VALUES ($1, $2, $3, $4)`, sku, fam, a, capacity.SourceManual); err != nil {
			return SKUFootprint{}, mapErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return SKUFootprint{}, err
	}
	out := SKUFootprint{SKU: sku, Families: map[string]Decimal{}, Source: capacity.SourceManual}
	for fam, a := range clean {
		out.Families[fam] = Decimal(a)
	}
	now := time.Now().UTC()
	out.UpdatedAt = &now
	return out, nil
}

// ListSKUCaps returns every direct per-SKU cap with its zone and region.
func (s *Store) ListSKUCaps(ctx context.Context) ([]SKUCap, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.zone_id, z.code, r.code, c.sku, c.total::text, c.updated_by, c.updated_at
		FROM sku_caps c JOIN capacity_zones z ON z.id = c.zone_id JOIN capacity_regions r ON r.id = z.region_id ORDER BY r.code, z.code, c.sku`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []SKUCap{}
	for rows.Next() {
		var c SKUCap
		var total string
		if err := rows.Scan(&c.ZoneID, &c.ZoneCode, &c.RegionCode, &c.SKU, &total, &c.UpdatedBy, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.Total = Decimal(total)
		c.UpdatedAt = c.UpdatedAt.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// PutSKUCap upserts a direct cap on a SKU in a zone; ErrNotFound for an
// unknown zone.
func (s *Store) PutSKUCap(ctx context.Context, zoneID, sku string, total Decimal, actor string) (SKUCap, error) {
	sku = strings.TrimSpace(sku)
	if sku == "" {
		return SKUCap{}, fmt.Errorf("%w: sku is required", ErrInvalid)
	}
	if !validNonNegativeDecimal(string(total)) {
		return SKUCap{}, fmt.Errorf("%w: total must be a non-negative number", ErrInvalid)
	}
	var c SKUCap
	var t string
	err := s.db.QueryRowContext(ctx, `INSERT INTO sku_caps (zone_id, sku, total, updated_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT (zone_id, sku) DO UPDATE SET total = EXCLUDED.total, updated_by = EXCLUDED.updated_by, updated_at = now()
		RETURNING zone_id, sku, total::text, updated_by, updated_at`, zoneID, sku, strings.TrimSpace(string(total)), actor).Scan(&c.ZoneID, &c.SKU, &t, &c.UpdatedBy, &c.UpdatedAt)
	if err != nil {
		return SKUCap{}, mapErr(err)
	}
	c.Total = Decimal(t)
	c.UpdatedAt = c.UpdatedAt.UTC()
	if err := s.db.QueryRowContext(ctx, `SELECT z.code, r.code FROM capacity_zones z JOIN capacity_regions r ON r.id = z.region_id WHERE z.id = $1`, zoneID).Scan(&c.ZoneCode, &c.RegionCode); err != nil {
		return SKUCap{}, mapErr(err)
	}
	return c, nil
}

// DeleteSKUCap removes a cap; ErrNotFound when there was none.
func (s *Store) DeleteSKUCap(ctx context.Context, zoneID, sku string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sku_caps WHERE zone_id = $1 AND sku = $2`, zoneID, strings.TrimSpace(sku))
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// the overview: consumption derived from metering
// ---------------------------------------------------------------------------

// CapacityOverview is GET /capacity/overview: every region → zone → pool
// with total / reserved / consumed / available, the SKU headroom per zone,
// and what could not be attributed.
type CapacityOverview struct {
	// AsOf is the latest complete hour consumption was measured in; nil when
	// the ledger holds no cloud usage at all.
	AsOf *time.Time `json:"as_of"`
	// Sources is how many cloud sources contributed to Consumed;
	// LaggingSources how many of them last metered more than six hours
	// before AsOf (their last hour is still counted, as the best fact held).
	Sources        int                      `json:"sources"`
	LaggingSources int                      `json:"lagging_sources"`
	Thresholds     CapacityThresholds       `json:"thresholds"`
	Families       []capacity.Family        `json:"families"`
	Regions        []CapacityRegionView     `json:"regions"`
	UnmappedSKUs   []CapacityUnmappedSKU    `json:"unmapped_skus"`
	UnmappedRegion []CapacityUnmappedRegion `json:"unmapped_regions"`
	Summary        CapacitySummary          `json:"summary"`
}

// CapacityThresholds are the utilisation percentages pools are coloured at.
type CapacityThresholds struct {
	WarnPct     int `json:"warn_pct"`
	CriticalPct int `json:"critical_pct"`
}

// CapacitySummary is the KPI strip.
type CapacitySummary struct {
	Regions        int `json:"regions"`
	Zones          int `json:"zones"`
	Pools          int `json:"pools"`
	PoolsWithTotal int `json:"pools_with_total"`
	PoolsWarn      int `json:"pools_warn"`
	PoolsCritical  int `json:"pools_critical"`
	// PoolsBelowThreshold is warn + critical: pools past the 70 % line.
	PoolsBelowThreshold int `json:"pools_below_threshold"`
	SKUs                int `json:"skus"`
	UnmappedSKUs        int `json:"unmapped_skus"`
}

// CapacityRegionView is one region of the overview.
type CapacityRegionView struct {
	ID              string             `json:"id"`
	Code            string             `json:"code"`
	Name            string             `json:"name"`
	CloudSourceKind string             `json:"cloud_source_kind"`
	Zones           []CapacityZoneView `json:"zones"`
}

// CapacityZoneView is one zone with its pools and SKU headroom.
type CapacityZoneView struct {
	ID        string             `json:"id"`
	Code      string             `json:"code"`
	Name      string             `json:"name"`
	IsDefault bool               `json:"is_default"`
	Pools     []CapacityPoolView `json:"pools"`
	SKUs      []CapacitySKUView  `json:"skus"`
}

// CapacityPoolView is a pool with its derived figures. Consumed is the
// latest complete hour's metered usage through the footprints; Available is
// total − reserved − consumed, never below 0 (Clamped is true and Overcommit
// is the shortfall when the arithmetic went negative). ExhaustionDays is
// available ÷ the 7-day growth of consumed per day (rating.RunRate); nil
// when consumption is not growing or the history is shorter than three days.
type CapacityPoolView struct {
	CapacityPool
	Label          string   `json:"label"`
	Unit           string   `json:"unit"`
	Consumed       Decimal  `json:"consumed"`
	Available      Decimal  `json:"available"`
	UtilisationPct *float64 `json:"utilisation_pct"`
	Status         string   `json:"status"`
	Clamped        bool     `json:"clamped"`
	Overcommit     Decimal  `json:"overcommit"`
	// ZoneUnknown is the part of Consumed attributed to this (default) zone
	// because the inventory carried no availability zone for the resource.
	ZoneUnknown    Decimal  `json:"zone_unknown"`
	GrowthPerDay   *float64 `json:"growth_per_day"`
	ExhaustionDays *float64 `json:"exhaustion_days"`
	HistoryDays    int      `json:"history_days"`
	// Series is consumed at the end of each complete day before the current
	// one, oldest first — what GrowthPerDay was fitted on.
	Series []CapacityDayPoint `json:"series"`
}

// CapacityDayPoint is one complete day's consumption of a pool.
type CapacityDayPoint struct {
	Day      string  `json:"day"`
	Consumed Decimal `json:"consumed"`
}

// CapacityGrowth fits a daily series and returns its growth in units per
// day; ok is false when the series is too short to fit. The API passes the
// explorer's run-rate arithmetic (rating.RunRate), which this package cannot
// import — rating imports store — so the store never invents a second one.
type CapacityGrowth func(days []CapacityDayPoint) (perDay float64, ok bool)

// CapacitySKUView is one SKU's headroom in one zone: how many more units
// the pools (and a cap, when set) allow, and which family binds first.
type CapacitySKUView struct {
	SKU             string             `json:"sku"`
	Footprint       map[string]Decimal `json:"footprint"`
	FootprintSource string             `json:"footprint_source"`
	ConsumedUnits   Decimal            `json:"consumed_units"`
	Resources       int                `json:"resources"`
	// HeadroomUnits is nil when no family in the footprint has a total yet.
	HeadroomUnits *Decimal `json:"headroom_units"`
	// BindingFamily is the family that limits HeadroomUnits, or "cap" when
	// the direct cap does.
	BindingFamily string   `json:"binding_family"`
	Cap           *Decimal `json:"cap"`
}

// CapacityUnmappedSKU is a metered SKU with no footprint, stored or derived,
// so its consumption counts against no pool.
type CapacityUnmappedSKU struct {
	SKU       string   `json:"sku"`
	Unit      string   `json:"unit"`
	Quantity  Decimal  `json:"quantity"`
	Resources int      `json:"resources"`
	Regions   []string `json:"regions"`
	// Suggested is the footprint the name implies when it does — always nil
	// here by construction, kept on the wire for a reader that expects it.
	Suggested map[string]Decimal `json:"suggested,omitempty"`
}

// CapacityUnmappedRegion is metered usage in a region the operator has not
// added (or has added without a zone), so nothing could receive it.
type CapacityUnmappedRegion struct {
	Region    string  `json:"region"`
	Reason    string  `json:"reason"` // no-region | no-zones
	SKUs      int     `json:"skus"`
	Quantity  Decimal `json:"quantity"`
	Resources int     `json:"resources"`
}

// capacityUsageRow is one (source, day, region, az, sku) of the last metered
// hour of that source on that day.
type capacityUsageRow struct {
	sourceID  string
	day       string
	hour      time.Time
	region    string
	az        string
	sku       string
	unit      string
	quantity  *big.Rat
	resources int
}

// capacityHistoryDays is how many complete days of consumption feed the
// growth trend: the run rate's window (7) plus one so seven complete days
// precede the current one.
const capacityHistoryDays = 8

// capacityLagging is how far behind AsOf a source's last hour may be before
// it is reported as lagging.
const capacityLagging = 6 * time.Hour

// queryCapacityUsage reads, per cloud source and per UTC day inside the
// history window, the source's LAST metered hour of that day, grouped by
// region, availability zone (from the inventory row) and SKU. The last hour
// of the latest day is the current consumption; the last hour of each
// earlier day is that day's point in the growth series.
func (s *Store) queryCapacityUsage(ctx context.Context, now time.Time) ([]capacityUsageRow, error) {
	cutoff := now.UTC().Truncate(time.Hour)
	from := time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -capacityHistoryDays)
	rows, err := s.db.QueryContext(ctx, `
WITH recs AS (
  SELECT u.source_id, u.resource_id, u.sku, u.unit, u.quantity, lower(u.region) AS region, u.window_start,
         (u.window_start AT TIME ZONE 'UTC')::date AS day,
         lower(COALESCE(NULLIF(i.attrs->>'availability_zone', ''), NULLIF(i.attrs->>'az', ''), '')) AS az
    FROM usage_records u
    JOIN cost_sources s ON s.id = u.source_id AND s.layer = '`+LayerCloud+`' AND s.status <> '`+StatusDisabled+`'
    LEFT JOIN resource_inventory i ON i.source_id = u.source_id AND i.resource_id = u.resource_id
   WHERE u.window_start >= $1 AND u.window_start < $2 AND u.`+metricSKUFilter+`
),
last_hours AS (SELECT source_id, day, max(window_start) AS ws FROM recs GROUP BY source_id, day)
SELECT r.source_id, to_char(r.day, 'YYYY-MM-DD'), r.window_start, r.region, r.az, r.sku, min(r.unit), sum(r.quantity)::text, count(DISTINCT r.resource_id)
  FROM recs r JOIN last_hours l ON l.source_id = r.source_id AND l.day = r.day AND l.ws = r.window_start
 GROUP BY r.source_id, r.day, r.window_start, r.region, r.az, r.sku
 ORDER BY 1, 2, 4, 5, 6`, from, cutoff)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []capacityUsageRow
	for rows.Next() {
		var r capacityUsageRow
		var qty string
		if err := rows.Scan(&r.sourceID, &r.day, &r.hour, &r.region, &r.az, &r.sku, &r.unit, &qty, &r.resources); err != nil {
			return nil, err
		}
		r.hour = r.hour.UTC()
		r.quantity = ratOf(Decimal(qty))
		out = append(out, r)
	}
	return out, rows.Err()
}

// zoneFamilyKey keys per-zone, per-family accumulators.
type zoneFamilyKey struct{ zone, family string }

// CapacityOverview derives the capacity picture at now (DESIGN.md §11).
// regionFilter narrows the regions listed (a code; "" = all); consumption is
// always attributed over every region so the unmapped lists are complete.
// growth fits each pool's daily series for time-to-exhaustion; nil reports
// no growth and no exhaustion.
func (s *Store) CapacityOverview(ctx context.Context, now time.Time, regionFilter string, growth CapacityGrowth) (CapacityOverview, error) {
	out := CapacityOverview{
		Thresholds:     CapacityThresholds{WarnPct: capacity.ThresholdWarnPct, CriticalPct: capacity.ThresholdCriticalPct},
		Families:       capacity.Families,
		Regions:        []CapacityRegionView{},
		UnmappedSKUs:   []CapacityUnmappedSKU{},
		UnmappedRegion: []CapacityUnmappedRegion{},
	}
	regions, err := s.ListCapacityRegions(ctx)
	if err != nil {
		return out, err
	}
	// Pools of every zone, keyed by zone id.
	poolsByZone := map[string][]CapacityPool{}
	prow, err := s.db.QueryContext(ctx, `SELECT `+capacityPoolColumns+` FROM capacity_pools p`)
	if err != nil {
		return out, mapErr(err)
	}
	for prow.Next() {
		p, err := scanCapacityPool(prow)
		if err != nil {
			prow.Close()
			return out, err
		}
		poolsByZone[p.ZoneID] = append(poolsByZone[p.ZoneID], p)
	}
	prow.Close()
	if err := prow.Err(); err != nil {
		return out, err
	}
	stored, err := s.ListSKUFootprints(ctx)
	if err != nil {
		return out, err
	}
	caps, err := s.ListSKUCaps(ctx)
	if err != nil {
		return out, err
	}
	usage, err := s.queryCapacityUsage(ctx, now)
	if err != nil {
		return out, err
	}

	// Region and zone lookups by code (lower-cased, as the query returns).
	regionByCode := map[string]int{}
	zoneByCode := map[string]map[string]string{} // region code → zone code → zone id
	defaultZone := map[string]string{}           // region code → default zone id
	for i, r := range regions {
		regionByCode[r.Code] = i
		zoneByCode[r.Code] = map[string]string{}
		for _, z := range r.Zones {
			zoneByCode[r.Code][z.Code] = z.ID
			if z.IsDefault {
				defaultZone[r.Code] = z.ID
			}
		}
		if _, ok := defaultZone[r.Code]; !ok && len(r.Zones) > 0 {
			defaultZone[r.Code] = r.Zones[0].ID
		}
	}
	// The footprint universe: stored rows win; a metered SKU without a row
	// takes what its name implies.
	footprints := map[string]SKUFootprint{}
	for _, fp := range stored {
		footprints[fp.SKU] = fp
	}
	footprintOf := func(sku string) (SKUFootprint, bool) {
		if fp, ok := footprints[sku]; ok {
			return fp, len(fp.Families) > 0
		}
		d := capacity.Derive(sku)
		if d == nil {
			return SKUFootprint{}, false
		}
		fp := SKUFootprint{SKU: sku, Families: map[string]Decimal{}, Source: capacity.SourceDerived}
		for fam, a := range d {
			fp.Families[fam] = Decimal(a)
		}
		footprints[sku] = fp
		return fp, true
	}
	capOf := map[zoneFamilyKey]Decimal{} // (zone, sku) → cap total
	for _, c := range caps {
		capOf[zoneFamilyKey{c.ZoneID, c.SKU}] = c.Total
	}

	// Current = each source's latest hour; the series = each (source, day)
	// last hour, summed over sources per day.
	latestBySource := map[string]time.Time{}
	for _, r := range usage {
		if r.hour.After(latestBySource[r.sourceID]) {
			latestBySource[r.sourceID] = r.hour
		}
	}
	var asOf time.Time
	for _, h := range latestBySource {
		if h.After(asOf) {
			asOf = h
		}
	}
	if !asOf.IsZero() {
		t := asOf
		out.AsOf = &t
		out.Sources = len(latestBySource)
		for _, h := range latestBySource {
			if asOf.Sub(h) > capacityLagging {
				out.LaggingSources++
			}
		}
	}
	today := ""
	if out.AsOf != nil {
		today = asOf.Format("2006-01-02")
	}

	consumed := map[zoneFamilyKey]*big.Rat{}    // current consumption per zone × family
	zoneUnknown := map[zoneFamilyKey]*big.Rat{} // the part attributed by default
	series := map[zoneFamilyKey]map[string]*big.Rat{}
	type skuAcc struct {
		units     *big.Rat
		resources int
	}
	skuUnits := map[zoneFamilyKey]*skuAcc{} // (zone, sku) → current units
	unmappedSKU := map[string]*CapacityUnmappedSKU{}
	unmappedSKURegions := map[string]map[string]bool{}
	unmappedRegion := map[string]*CapacityUnmappedRegion{}
	unmappedRegionSKUs := map[string]map[string]bool{}
	add := func(m map[zoneFamilyKey]*big.Rat, k zoneFamilyKey, v *big.Rat) {
		if m[k] == nil {
			m[k] = new(big.Rat)
		}
		m[k].Add(m[k], v)
	}
	for _, r := range usage {
		current := r.hour.Equal(latestBySource[r.sourceID])
		ri, regionKnown := regionByCode[r.region]
		var zoneID string
		unknownZone := false
		if regionKnown {
			code := regions[ri].Code
			if id, ok := zoneByCode[code][r.az]; ok && r.az != "" {
				zoneID = id
			} else {
				zoneID = defaultZone[code]
				unknownZone = true
			}
		}
		if zoneID == "" {
			if current {
				reason := "no-region"
				if regionKnown {
					reason = "no-zones"
				}
				u := unmappedRegion[r.region]
				if u == nil {
					u = &CapacityUnmappedRegion{Region: r.region, Reason: reason, Quantity: "0"}
					unmappedRegion[r.region] = u
					unmappedRegionSKUs[r.region] = map[string]bool{}
				}
				unmappedRegionSKUs[r.region][r.sku] = true
				u.Quantity = addDec(u.Quantity, decOf(r.quantity))
				u.Resources += r.resources
			}
			continue
		}
		fp, ok := footprintOf(r.sku)
		if !ok {
			if current {
				u := unmappedSKU[r.sku]
				if u == nil {
					u = &CapacityUnmappedSKU{SKU: r.sku, Unit: r.unit, Quantity: "0"}
					unmappedSKU[r.sku] = u
					unmappedSKURegions[r.sku] = map[string]bool{}
				}
				unmappedSKURegions[r.sku][r.region] = true
				u.Quantity = addDec(u.Quantity, decOf(r.quantity))
				u.Resources += r.resources
			}
			continue
		}
		for fam, amount := range fp.Families {
			v := new(big.Rat).Mul(r.quantity, ratOf(amount))
			k := zoneFamilyKey{zoneID, fam}
			if current {
				add(consumed, k, v)
				if unknownZone {
					add(zoneUnknown, k, v)
				}
			}
			if r.day != today {
				if series[k] == nil {
					series[k] = map[string]*big.Rat{}
				}
				if series[k][r.day] == nil {
					series[k][r.day] = new(big.Rat)
				}
				series[k][r.day].Add(series[k][r.day], v)
			}
		}
		if current {
			sk := zoneFamilyKey{zoneID, r.sku}
			acc := skuUnits[sk]
			if acc == nil {
				acc = &skuAcc{units: new(big.Rat)}
				skuUnits[sk] = acc
			}
			acc.units.Add(acc.units, r.quantity)
			acc.resources += r.resources
		}
	}

	// The SKU universe every zone lists: stored footprints plus derived ones
	// for metered SKUs (footprintOf added those), sorted.
	skuList := make([]string, 0, len(footprints))
	for sku, fp := range footprints {
		if len(fp.Families) > 0 {
			skuList = append(skuList, sku)
		}
	}
	sort.Strings(skuList)
	out.Summary.SKUs = len(skuList)

	filter := normCode(regionFilter)
	for _, r := range regions {
		if filter != "" && r.Code != filter {
			continue
		}
		rv := CapacityRegionView{ID: r.ID, Code: r.Code, Name: r.Name, CloudSourceKind: r.CloudSourceKind, Zones: []CapacityZoneView{}}
		for _, z := range r.Zones {
			zv := CapacityZoneView{ID: z.ID, Code: z.Code, Name: z.Name, IsDefault: z.IsDefault, Pools: []CapacityPoolView{}, SKUs: []CapacitySKUView{}}
			pools := poolsByZone[z.ID]
			sort.SliceStable(pools, func(i, j int) bool { return familyOrder(pools[i].Family) < familyOrder(pools[j].Family) })
			available := map[string]*big.Rat{} // family → available (clamped)
			hasTotal := map[string]bool{}
			for _, p := range pools {
				pv := poolView(p, consumed[zoneFamilyKey{z.ID, p.Family}], zoneUnknown[zoneFamilyKey{z.ID, p.Family}], series[zoneFamilyKey{z.ID, p.Family}], growth)
				available[p.Family] = ratOf(pv.Available)
				hasTotal[p.Family] = ratOf(p.Total).Sign() > 0
				out.Summary.Pools++
				switch pv.Status {
				case capacity.StatusWarn:
					out.Summary.PoolsWarn++
				case capacity.StatusCritical:
					out.Summary.PoolsCritical++
				}
				if hasTotal[p.Family] {
					out.Summary.PoolsWithTotal++
				}
				zv.Pools = append(zv.Pools, pv)
			}
			for _, sku := range skuList {
				fp := footprints[sku]
				sv := CapacitySKUView{SKU: sku, Footprint: fp.Families, FootprintSource: fp.Source, ConsumedUnits: "0"}
				if acc := skuUnits[zoneFamilyKey{z.ID, sku}]; acc != nil {
					sv.ConsumedUnits = decOf(acc.units)
					sv.Resources = acc.resources
				}
				sv.HeadroomUnits, sv.BindingFamily = headroom(fp.Families, available, hasTotal)
				if c, ok := capOf[zoneFamilyKey{z.ID, sku}]; ok {
					cc := c
					sv.Cap = &cc
					left := new(big.Rat).Sub(ratOf(c), ratOf(sv.ConsumedUnits))
					if left.Sign() < 0 {
						left = new(big.Rat)
					}
					capUnits := floorRat(left)
					if sv.HeadroomUnits == nil || capUnits.Cmp(floorRat(ratOf(*sv.HeadroomUnits))) < 0 {
						d := decOfInt(capUnits)
						sv.HeadroomUnits, sv.BindingFamily = &d, "cap"
					}
				}
				zv.SKUs = append(zv.SKUs, sv)
			}
			out.Summary.Zones++
			rv.Zones = append(rv.Zones, zv)
		}
		out.Summary.Regions++
		out.Regions = append(out.Regions, rv)
	}
	out.Summary.PoolsBelowThreshold = out.Summary.PoolsWarn + out.Summary.PoolsCritical

	for sku, u := range unmappedSKU {
		for region := range unmappedSKURegions[sku] {
			u.Regions = append(u.Regions, region)
		}
		sort.Strings(u.Regions)
		out.UnmappedSKUs = append(out.UnmappedSKUs, *u)
	}
	sort.Slice(out.UnmappedSKUs, func(i, j int) bool { return out.UnmappedSKUs[i].SKU < out.UnmappedSKUs[j].SKU })
	out.Summary.UnmappedSKUs = len(out.UnmappedSKUs)
	for region, u := range unmappedRegion {
		u.SKUs = len(unmappedRegionSKUs[region])
		out.UnmappedRegion = append(out.UnmappedRegion, *u)
	}
	sort.Slice(out.UnmappedRegion, func(i, j int) bool { return out.UnmappedRegion[i].Region < out.UnmappedRegion[j].Region })
	return out, nil
}

// poolView derives one pool's figures from its total, its current
// consumption and its daily series.
func poolView(p CapacityPool, cons, unknown *big.Rat, days map[string]*big.Rat, growth CapacityGrowth) CapacityPoolView {
	if cons == nil {
		cons = new(big.Rat)
	}
	if unknown == nil {
		unknown = new(big.Rat)
	}
	fam := capacity.Family{Key: p.Family, Label: p.Family, Unit: ""}
	for _, f := range capacity.Families {
		if f.Key == p.Family {
			fam = f
		}
	}
	total, reserved := ratOf(p.Total), ratOf(p.Reserved)
	pv := CapacityPoolView{CapacityPool: p, Label: fam.Label, Unit: fam.Unit, Consumed: decOf(cons), ZoneUnknown: decOf(unknown), Overcommit: "0.000000", Series: []CapacityDayPoint{}}
	avail := new(big.Rat).Sub(total, reserved)
	avail.Sub(avail, cons)
	if avail.Sign() < 0 {
		pv.Clamped = true
		pv.Overcommit = decOf(new(big.Rat).Neg(avail))
		avail = new(big.Rat)
	}
	pv.Available = decOf(avail)
	hasTotal := total.Sign() > 0
	if hasTotal {
		used := new(big.Rat).Add(cons, reserved)
		pct, _ := new(big.Rat).Quo(used, total).Float64()
		pct *= 100
		pv.UtilisationPct = &pct
	}
	pv.Status = capacity.Status(derefFloat(pv.UtilisationPct), hasTotal)

	// Growth: the fitted trend over the complete days before the current
	// one, in family units per day; exhaustion is available ÷ growth.
	keys := make([]string, 0, len(days))
	for d := range days {
		keys = append(keys, d)
	}
	sort.Strings(keys)
	for _, d := range keys {
		pv.Series = append(pv.Series, CapacityDayPoint{Day: d, Consumed: decOf(days[d])})
	}
	pv.HistoryDays = len(pv.Series)
	if growth != nil && len(pv.Series) > 0 {
		if trend, ok := growth(pv.Series); ok {
			g := trend
			pv.GrowthPerDay = &g
			if hasTotal && trend > 0 {
				a, _ := avail.Float64()
				d := math.Round(a/trend*10) / 10
				pv.ExhaustionDays = &d
			}
		}
	}
	return pv
}

// headroom is min over the footprint's families of floor(available ÷
// amount), over the families that have a total; nil when none has.
func headroom(fp map[string]Decimal, available map[string]*big.Rat, hasTotal map[string]bool) (*Decimal, string) {
	var best *big.Int
	binding := ""
	fams := make([]string, 0, len(fp))
	for fam := range fp {
		fams = append(fams, fam)
	}
	sort.SliceStable(fams, func(i, j int) bool { return familyOrder(fams[i]) < familyOrder(fams[j]) })
	for _, fam := range fams {
		if !hasTotal[fam] {
			continue
		}
		amount := ratOf(fp[fam])
		if amount.Sign() <= 0 {
			continue
		}
		avail := available[fam]
		if avail == nil {
			avail = new(big.Rat)
		}
		units := floorRat(new(big.Rat).Quo(avail, amount))
		if best == nil || units.Cmp(best) < 0 {
			best, binding = units, fam
		}
	}
	if best == nil {
		return nil, ""
	}
	d := decOfInt(best)
	return &d, binding
}

// floorRat is the integer floor of a non-negative rational.
func floorRat(r *big.Rat) *big.Int {
	if r.Sign() <= 0 {
		return new(big.Int)
	}
	return new(big.Int).Quo(r.Num(), r.Denom())
}

// decOfInt renders an integer as a Decimal.
func decOfInt(i *big.Int) Decimal { return Decimal(i.String()) }

func derefFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
