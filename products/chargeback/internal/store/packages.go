package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lib/pq"
)

// PACKAGES AND THE ENTITLEMENT MATRIX (DESIGN.md §22, founder direction
// 2026-10-10; the package LADDER approved 2026-10-10, 0.1.61).
//
// The SME marketplace sells the catalog plans as hosting-style PACKAGES — S,
// M, L, XL. A package is a POSITION on a fixed set of GROUPS, in this order:
// capacity, features (Blueprint chart parameters we provision), access
// (platform doors), ops (managed operations), scope, resilience, service.
// Platform-wide items are the FLOOR: listed once under every package, never
// priced, never a cell. A feature is one of four KINDS:
//
//   - BOOLEAN — included / optional (a priced add-on) / not offered;
//   - QUANTITY — an included quantity plus an OVERAGE POLICY per cell:
//     metered (the excess is billed at the feature's SKU), hard_cap (nothing
//     is billed above it — the platform refuses; the run reports the excess
//     and flags the SKU capped) or unlimited (no allowance, nothing billed);
//   - LEVEL — an ordered list of labels on the feature (backups: weekly ·
//     7 days → daily · 14 days → …); each cell carries a level index, and an
//     OPTIONAL cell on a level feature means "the next level is purchasable"
//     at the feature's add-on SKU;
//   - ACCESS — a platform door (console, Gitea + IaC, the Kubernetes UI, the
//     kube API): included or not offered, never an add-on, never billed.
//
// A TEASER feature publishes a not-offered cell as "available on <the first
// package that includes it>" instead of a dash. PACKAGE SETTINGS per (book,
// plan) carry the tagline, the recommended flag, the annual term rule and
// the shape (vCPU, memory, their guaranteed floors, disk); the catalog's
// constants are the fallback. The STEP-UP RULE is computed, never stored
// (api.packagesDocument).
//
// This file holds the matrix ONCE — the storefront comparison table, the
// public calculator, the statement run and the invoice all read the same
// rows — and the rules that keep it billable, enforced here and not by
// convention:
//
//   - a feature marked OPTIONAL on a package REQUIRES an add-on SKU priced in
//     that book, refused otherwise naming the SKU (an optional feature with no
//     price would be a promise the invoice cannot keep);
//   - a QUANTITY cell carries the quantity it includes and an overage policy;
//     a METERED overage needs the feature's SKU priced in the book, or the
//     excess would rate to nothing; a quantity feature is never optional —
//     more of it is bought through its overage;
//   - a LEVEL cell's level indexes into the feature's levels; a purchasable
//     next level needs a next level to exist and the add-on priced;
//   - an ACCESS feature refuses optional; a FLOOR item refuses every cell;
//   - a feature is deleted only while nothing depends on it: no cell of any
//     book's matrix, no Source that has taken it as an add-on.
//
// The matrix hangs off the PRICE BOOK, per plan item, so a negotiated clone
// of the plans book carries its own copy (ClonePriceBook copies the cells and
// the settings), and deleting a book removes them with it.

// packagesMigrationSQL is appended at the END of migrations (they are
// positional) and located by content as MigrationPackages. Every constraint
// is NAMED so dberr.go can give it a sentence and the live-constraint test can
// hold the name.
const packagesMigrationSQL = `
CREATE TABLE IF NOT EXISTS features (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	key TEXT NOT NULL UNIQUE,
	name TEXT NOT NULL,
	blurb TEXT NOT NULL DEFAULT '',
	kind TEXT NOT NULL,
	unit TEXT NOT NULL DEFAULT '',
	addon_sku TEXT,
	sort_order INT NOT NULL DEFAULT 0,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT features_key_check CHECK (key ~ '^[a-z0-9][a-z0-9_.-]*$'),
	CONSTRAINT features_name_check CHECK (name <> ''),
	CONSTRAINT features_kind_check CHECK (kind IN ('boolean','quantity')),
	CONSTRAINT features_unit_check CHECK (kind <> 'quantity' OR unit <> '')
);

CREATE TABLE IF NOT EXISTS package_entitlements (
	price_book_id UUID NOT NULL REFERENCES price_books(id) ON DELETE CASCADE,
	plan_sku TEXT NOT NULL,
	feature_id UUID NOT NULL REFERENCES features(id) ON DELETE RESTRICT,
	state TEXT NOT NULL,
	included_quantity NUMERIC(20,6),
	note TEXT NOT NULL DEFAULT '',
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (price_book_id, plan_sku, feature_id),
	CONSTRAINT package_entitlements_plan_sku_check CHECK (plan_sku LIKE 'plan.%'),
	CONSTRAINT package_entitlements_state_check CHECK (state IN ('included','optional','not_offered')),
	CONSTRAINT package_entitlements_included_quantity_check CHECK (included_quantity IS NULL OR included_quantity >= 0)
);
CREATE INDEX IF NOT EXISTS package_entitlements_feature_idx ON package_entitlements (feature_id);

CREATE TABLE IF NOT EXISTS source_addons (
	source_id UUID NOT NULL REFERENCES cost_sources(id) ON DELETE CASCADE,
	feature_id UUID NOT NULL REFERENCES features(id) ON DELETE RESTRICT,
	taken_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (source_id, feature_id)
);
CREATE INDEX IF NOT EXISTS source_addons_feature_idx ON source_addons (feature_id);

ALTER TABLE rated_lines ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '';
`

// packageLadderMigrationSQL is the package LADDER (DESIGN.md §22.1, 0.1.61):
// the two new kinds (level, access), the group a feature sits in, its level
// labels and teaser flag, the overage policy and level on a cell — every
// existing quantity cell reads METERED, which is what it was — and the
// package settings per (book, plan). Appended at the END of migrations and
// located by content as MigrationPackageLadder.
const packageLadderMigrationSQL = `
ALTER TABLE features DROP CONSTRAINT IF EXISTS features_kind_check;
ALTER TABLE features ADD CONSTRAINT features_kind_check CHECK (kind IN ('boolean','quantity','level','access'));
ALTER TABLE features ADD COLUMN IF NOT EXISTS feature_group TEXT NOT NULL DEFAULT 'features';
ALTER TABLE features ADD COLUMN IF NOT EXISTS levels JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE features ADD COLUMN IF NOT EXISTS teaser BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE features DROP CONSTRAINT IF EXISTS features_group_check;
ALTER TABLE features ADD CONSTRAINT features_group_check CHECK (feature_group IN ('floor','capacity','features','access','ops','scope','resilience','service'));
ALTER TABLE features DROP CONSTRAINT IF EXISTS features_levels_check;
ALTER TABLE features ADD CONSTRAINT features_levels_check CHECK (kind <> 'level' OR jsonb_array_length(levels) >= 2);

ALTER TABLE package_entitlements ADD COLUMN IF NOT EXISTS overage TEXT;
ALTER TABLE package_entitlements ADD COLUMN IF NOT EXISTS level INT;
UPDATE package_entitlements e SET overage = 'metered' FROM features f WHERE f.id = e.feature_id AND f.kind = 'quantity' AND e.overage IS NULL;
ALTER TABLE package_entitlements DROP CONSTRAINT IF EXISTS package_entitlements_overage_check;
ALTER TABLE package_entitlements ADD CONSTRAINT package_entitlements_overage_check CHECK (overage IS NULL OR overage IN ('metered','hard_cap','unlimited'));
ALTER TABLE package_entitlements DROP CONSTRAINT IF EXISTS package_entitlements_level_check;
ALTER TABLE package_entitlements ADD CONSTRAINT package_entitlements_level_check CHECK (level IS NULL OR level >= 0);

CREATE TABLE IF NOT EXISTS package_settings (
	price_book_id UUID NOT NULL REFERENCES price_books(id) ON DELETE CASCADE,
	plan_sku TEXT NOT NULL,
	tagline TEXT NOT NULL DEFAULT '',
	recommended BOOLEAN NOT NULL DEFAULT false,
	annual_months_free INT NOT NULL DEFAULT 0,
	vcpu NUMERIC(12,4),
	memory_gb NUMERIC(12,4),
	vcpu_guaranteed NUMERIC(12,4),
	memory_gb_guaranteed NUMERIC(12,4),
	disk_gb NUMERIC(12,4),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (price_book_id, plan_sku),
	CONSTRAINT package_settings_plan_sku_check CHECK (plan_sku LIKE 'plan.%'),
	CONSTRAINT package_settings_annual_months_free_check CHECK (annual_months_free >= 0 AND annual_months_free <= 12),
	CONSTRAINT package_settings_shape_check CHECK (
		COALESCE(vcpu, 0) >= 0 AND COALESCE(memory_gb, 0) >= 0 AND COALESCE(vcpu_guaranteed, 0) >= 0
		AND COALESCE(memory_gb_guaranteed, 0) >= 0 AND COALESCE(disk_gb, 0) >= 0)
);
`

// MigrationPackages is the schema_migrations version of the packages
// migration, located by content so a migration appended after it cannot
// move this version.
var MigrationPackages = func() int {
	for i, m := range migrations {
		if m == packagesMigrationSQL {
			return i + 1
		}
	}
	return len(migrations)
}()

// MigrationPackageLadder is the schema_migrations version of the package
// ladder migration, located by content like the others.
var MigrationPackageLadder = func() int {
	for i, m := range migrations {
		if m == packageLadderMigrationSQL {
			return i + 1
		}
	}
	return len(migrations)
}()

// Feature kinds (DESIGN.md §22.1).
const (
	FeatureKindBoolean  = "boolean"
	FeatureKindQuantity = "quantity"
	FeatureKindLevel    = "level"
	FeatureKindAccess   = "access"
)

// FeatureKinds is every kind, in display order.
var FeatureKinds = []string{FeatureKindBoolean, FeatureKindQuantity, FeatureKindLevel, FeatureKindAccess}

// ValidFeatureKind reports whether k is one of the four kinds.
func ValidFeatureKind(k string) bool {
	for _, x := range FeatureKinds {
		if x == k {
			return true
		}
	}
	return false
}

// Feature groups: the order a package is read in, and the floor.
const (
	FeatureGroupFloor      = "floor"
	FeatureGroupCapacity   = "capacity"
	FeatureGroupFeatures   = "features"
	FeatureGroupAccess     = "access"
	FeatureGroupOps        = "ops"
	FeatureGroupScope      = "scope"
	FeatureGroupResilience = "resilience"
	FeatureGroupService    = "service"
)

// FeatureGroup is one group of the matrix with the heading the pages show.
type FeatureGroup struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// FeatureGroups is every group a cell can sit in, in the order a package is
// read: capacity, features, access, managed operations, scope, resilience,
// service level. The floor is not a group of cells and is listed apart.
var FeatureGroups = []FeatureGroup{
	{FeatureGroupCapacity, "Capacity"},
	{FeatureGroupFeatures, "Features"},
	{FeatureGroupAccess, "Access"},
	{FeatureGroupOps, "Managed operations"},
	{FeatureGroupScope, "Scope"},
	{FeatureGroupResilience, "Resilience"},
	{FeatureGroupService, "Service level"},
}

// ValidFeatureGroup reports whether g is a group or the floor.
func ValidFeatureGroup(g string) bool {
	if g == FeatureGroupFloor {
		return true
	}
	for _, x := range FeatureGroups {
		if x.Key == g {
			return true
		}
	}
	return false
}

// The three states a feature has on a package.
const (
	EntitlementIncluded   = "included"
	EntitlementOptional   = "optional"
	EntitlementNotOffered = "not_offered"
)

// EntitlementStates is every state, in the order the console offers them.
var EntitlementStates = []string{EntitlementIncluded, EntitlementOptional, EntitlementNotOffered}

// ValidEntitlementState reports whether s is one of the three states.
func ValidEntitlementState(s string) bool {
	return s == EntitlementIncluded || s == EntitlementOptional || s == EntitlementNotOffered
}

// Overage policies of a quantity cell (DESIGN.md §22.2).
const (
	OverageMetered   = "metered"
	OverageHardCap   = "hard_cap"
	OverageUnlimited = "unlimited"
)

// OveragePolicies is every policy, in the order the console offers them.
var OveragePolicies = []string{OverageMetered, OverageHardCap, OverageUnlimited}

// ValidOverage reports whether o is a policy.
func ValidOverage(o string) bool {
	return o == OverageMetered || o == OverageHardCap || o == OverageUnlimited
}

// Feature is one row of the matrix: what the marketplace lists, the group it
// sits in, how it is counted, and the SKU that prices it when a package
// offers it (or its next level) as an add-on — or, for a quantity feature,
// the metered SKU its included quantity is an allowance on and its excess is
// billed at.
type Feature struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Name  string `json:"name"`
	Blurb string `json:"blurb"`
	Kind  string `json:"kind"`
	// Group is the group the row sits in, or "floor" for a platform-wide item.
	Group string `json:"group"`
	// Unit is the quantity's unit on a quantity feature ("Mbps"); empty on the
	// other kinds.
	Unit string `json:"unit,omitempty"`
	// AddonSKU is the SKU priced when the feature is OPTIONAL on a package
	// (the add-on line, or the next level of a level feature), and the SKU a
	// quantity feature's included quantity is an allowance on. Empty = the
	// feature can only be included or not offered.
	AddonSKU string `json:"addon_sku,omitempty"`
	// Levels are the ordered labels of a level feature; empty otherwise.
	Levels []string `json:"levels,omitempty"`
	// Teaser publishes a not-offered cell as "available on <first package
	// that includes it>" instead of a dash.
	Teaser    bool `json:"teaser"`
	SortOrder int  `json:"sort_order"`
	// IconID is the icon shown beside the feature (DESIGN.md §22.10), the
	// SHA-256 id of a stored icon; IconBG the "#RRGGBB" tile behind it.
	// Empty = none.
	IconID    string    `json:"icon_id,omitempty"`
	IconBG    string    `json:"icon_bg,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// FeatureInput is the creatable subset.
type FeatureInput struct {
	Key       string
	Name      string
	Blurb     string
	Kind      string
	Group     string
	Unit      string
	AddonSKU  string
	Levels    []string
	Teaser    bool
	SortOrder int
	IconID    string
	IconBG    string
}

// FeaturePatch carries optional edits; nil means unchanged. An AddonSKU of
// "" clears the add-on SKU, which is refused while any package offers the
// feature as optional (the add-on would have nothing to price).
type FeaturePatch struct {
	Name      *string
	Blurb     *string
	Kind      *string
	Group     *string
	Unit      *string
	AddonSKU  *string
	Levels    *[]string
	Teaser    *bool
	SortOrder *int
	// IconID and IconBG: "" clears.
	IconID *string
	IconBG *string
}

var featureKeyShape = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)

// ValidFeatureKey reports whether key is a lower-case identifier.
func ValidFeatureKey(key string) bool { return featureKeyShape.MatchString(key) }

const featureColumns = `f.id, f.key, f.name, f.blurb, f.kind, f.feature_group, f.unit, COALESCE(f.addon_sku, ''), f.levels::text, f.teaser, f.sort_order, f.created_at, f.updated_at, COALESCE(f.icon_id, ''), COALESCE(f.icon_bg, '')`

func scanFeatureInto(row interface{ Scan(...any) error }, f *Feature, rest ...any) error {
	var levels string
	dest := []any{&f.ID, &f.Key, &f.Name, &f.Blurb, &f.Kind, &f.Group, &f.Unit, &f.AddonSKU, &levels, &f.Teaser, &f.SortOrder, &f.CreatedAt, &f.UpdatedAt, &f.IconID, &f.IconBG}
	if err := row.Scan(append(rest, dest...)...); err != nil {
		return mapErr(err)
	}
	f.Levels = nil
	if levels != "" && levels != "[]" {
		if err := json.Unmarshal([]byte(levels), &f.Levels); err != nil {
			return fmt.Errorf("feature %s: levels: %w", f.Key, err)
		}
	}
	f.CreatedAt, f.UpdatedAt = f.CreatedAt.UTC(), f.UpdatedAt.UTC()
	return nil
}

func scanFeature(row interface{ Scan(...any) error }) (Feature, error) {
	var f Feature
	err := scanFeatureInto(row, &f)
	return f, err
}

// ListFeatures returns every feature in matrix order: sort order, then key.
func (s *Store) ListFeatures(ctx context.Context) ([]Feature, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+featureColumns+` FROM features f ORDER BY f.sort_order, f.key`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []Feature{}
	for rows.Next() {
		f, err := scanFeature(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetFeature resolves a feature by id OR by key — the console and the seeder
// address a feature by its key, the matrix cell by whichever it has.
func (s *Store) GetFeature(ctx context.Context, ref string) (Feature, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Feature{}, ErrNotFound
	}
	return scanFeature(s.db.QueryRowContext(ctx, `SELECT `+featureColumns+` FROM features f WHERE f.id::text = $1 OR f.key = $1`, ref))
}

// cleanLevels trims the labels and drops empty ones.
func cleanLevels(in []string) []string {
	var out []string
	for _, l := range in {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// validateFeatureShape holds the rules that tie kind, group, unit, levels and
// add-on SKU together (DESIGN.md §22.1).
func validateFeatureShape(f Feature) error {
	if !ValidFeatureKey(f.Key) {
		return fmt.Errorf("%w: a feature key is lower-case letters, digits, dot, dash or underscore", ErrInvalid)
	}
	if !ValidFeatureKind(f.Kind) {
		return fmt.Errorf("%w: kind must be boolean, quantity, level or access", ErrInvalid)
	}
	if !ValidFeatureGroup(f.Group) {
		return fmt.Errorf("%w: group must be capacity, features, access, ops, scope, resilience, service or floor", ErrInvalid)
	}
	if f.Kind == FeatureKindQuantity && strings.TrimSpace(f.Unit) == "" {
		return fmt.Errorf("%w: a quantity feature needs a unit (Mbps, GB)", ErrInvalid)
	}
	if f.Kind == FeatureKindLevel && len(f.Levels) < 2 {
		return fmt.Errorf("%w: a level feature needs at least two levels, in order", ErrInvalid)
	}
	if f.Kind != FeatureKindLevel && len(f.Levels) > 0 {
		return fmt.Errorf("%w: only a level feature carries levels", ErrInvalid)
	}
	if f.Kind == FeatureKindAccess && f.AddonSKU != "" {
		return fmt.Errorf("%w: an access feature is included or not offered; it has no add-on SKU", ErrInvalid)
	}
	if f.Group == FeatureGroupFloor {
		if f.Kind != FeatureKindBoolean {
			return fmt.Errorf("%w: a floor item is on every package; it is a boolean feature with no cell", ErrInvalid)
		}
		if f.AddonSKU != "" {
			return fmt.Errorf("%w: a floor item is never priced; it has no add-on SKU", ErrInvalid)
		}
	}
	return nil
}

func levelsJSON(levels []string) string {
	if len(levels) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(levels)
	return string(b)
}

// CreateFeature inserts a feature. A duplicate key is ErrConflict.
func (s *Store) CreateFeature(ctx context.Context, in FeatureInput) (Feature, error) {
	f := Feature{
		Key:       strings.ToLower(strings.TrimSpace(in.Key)),
		Name:      strings.TrimSpace(in.Name),
		Blurb:     strings.TrimSpace(in.Blurb),
		Kind:      strings.ToLower(strings.TrimSpace(in.Kind)),
		Group:     strings.ToLower(strings.TrimSpace(in.Group)),
		Unit:      strings.TrimSpace(in.Unit),
		AddonSKU:  strings.TrimSpace(in.AddonSKU),
		Levels:    cleanLevels(in.Levels),
		Teaser:    in.Teaser,
		SortOrder: in.SortOrder,
	}
	if f.Kind == "" {
		f.Kind = FeatureKindBoolean
	}
	if f.Group == "" {
		f.Group = FeatureGroupFeatures
	}
	if f.Name == "" {
		return Feature{}, fmt.Errorf("%w: a feature needs a name", ErrInvalid)
	}
	if err := validateFeatureShape(f); err != nil {
		return Feature{}, err
	}
	var addon any
	if f.AddonSKU != "" {
		addon = f.AddonSKU
	}
	icon, err := iconRef(ctx, s.db, in.IconID)
	if err != nil {
		return Feature{}, err
	}
	bg, err := colourArg("icon_bg", in.IconBG)
	if err != nil {
		return Feature{}, err
	}
	return scanFeature(s.db.QueryRowContext(ctx, `INSERT INTO features AS f (key, name, blurb, kind, feature_group, unit, addon_sku, levels, teaser, sort_order, icon_id, icon_bg)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10, $11, $12) RETURNING `+featureColumns,
		f.Key, f.Name, f.Blurb, f.Kind, f.Group, f.Unit, addon, levelsJSON(f.Levels), f.Teaser, f.SortOrder, icon, bg))
}

// UpdateFeature applies a patch. The kind may change only while no package
// cell names the feature (a quantity cell carries a quantity a boolean cannot
// read, a level cell a level); the levels of a level feature may shrink only
// while no cell sits above the new top; the add-on SKU may be cleared or
// changed only if every package that offers the feature as optional still has
// the new SKU priced.
func (s *Store) UpdateFeature(ctx context.Context, id string, p FeaturePatch) (Feature, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Feature{}, err
	}
	defer tx.Rollback()
	cur, err := scanFeature(tx.QueryRowContext(ctx, `SELECT `+featureColumns+` FROM features f WHERE f.id::text = $1 OR f.key = $1 FOR UPDATE`, strings.TrimSpace(id)))
	if err != nil {
		return Feature{}, err
	}
	next := cur
	if p.Name != nil {
		if strings.TrimSpace(*p.Name) == "" {
			return Feature{}, fmt.Errorf("%w: a feature needs a name", ErrInvalid)
		}
		next.Name = strings.TrimSpace(*p.Name)
	}
	if p.Blurb != nil {
		next.Blurb = strings.TrimSpace(*p.Blurb)
	}
	if p.Unit != nil {
		next.Unit = strings.TrimSpace(*p.Unit)
	}
	if p.SortOrder != nil {
		next.SortOrder = *p.SortOrder
	}
	if p.Teaser != nil {
		next.Teaser = *p.Teaser
	}
	if p.Group != nil {
		next.Group = strings.ToLower(strings.TrimSpace(*p.Group))
	}
	if p.Levels != nil {
		next.Levels = cleanLevels(*p.Levels)
	}
	var cells int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM package_entitlements WHERE feature_id = $1`, cur.ID).Scan(&cells); err != nil {
		return Feature{}, mapErr(err)
	}
	if p.Kind != nil {
		next.Kind = strings.ToLower(strings.TrimSpace(*p.Kind))
		if next.Kind != cur.Kind && cells > 0 {
			return Feature{}, fmt.Errorf("%w: %s is in %d package cell(s); its kind cannot change while a package carries it", ErrConflict, cur.Key, cells)
		}
		if next.Kind != FeatureKindLevel && p.Levels == nil {
			next.Levels = nil
		}
	}
	if next.Group == FeatureGroupFloor && cur.Group != FeatureGroupFloor && cells > 0 {
		return Feature{}, fmt.Errorf("%w: %s is in %d package cell(s); a floor item has no cell — remove them first", ErrConflict, cur.Key, cells)
	}
	if next.Kind == FeatureKindLevel && len(next.Levels) < len(cur.Levels) && cells > 0 {
		var above int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM package_entitlements WHERE feature_id = $1 AND level >= $2`, cur.ID, len(next.Levels)).Scan(&above); err != nil {
			return Feature{}, mapErr(err)
		}
		if above > 0 {
			return Feature{}, fmt.Errorf("%w: %d package cell(s) of %s sit at a level the shorter list no longer has; lower them first", ErrConflict, above, cur.Key)
		}
	}
	if p.AddonSKU != nil {
		next.AddonSKU = strings.TrimSpace(*p.AddonSKU)
		if next.AddonSKU != cur.AddonSKU {
			// Every package that offers the feature as an add-on must still
			// be able to price it.
			rows, err := tx.QueryContext(ctx, `SELECT b.name, e.plan_sku, EXISTS (SELECT 1 FROM price_items pi WHERE pi.price_book_id = b.id AND pi.sku = $2)
				FROM package_entitlements e JOIN price_books b ON b.id = e.price_book_id WHERE e.feature_id = $1 AND e.state = 'optional' ORDER BY b.name, e.plan_sku`, cur.ID, next.AddonSKU)
			if err != nil {
				return Feature{}, mapErr(err)
			}
			var unpriced []string
			for rows.Next() {
				var book, plan string
				var priced bool
				if err := rows.Scan(&book, &plan, &priced); err != nil {
					rows.Close()
					return Feature{}, err
				}
				if next.AddonSKU == "" || !priced {
					unpriced = append(unpriced, fmt.Sprintf("%s on %s", PlanName(strings.TrimPrefix(plan, PlanSKUPrefix)), book))
				}
			}
			rows.Close()
			if len(unpriced) > 0 {
				if next.AddonSKU == "" {
					return Feature{}, fmt.Errorf("%w: %s is optional on %s; an optional feature needs an add-on SKU to price", ErrConflict, cur.Key, strings.Join(unpriced, ", "))
				}
				return Feature{}, fmt.Errorf("%w: %s is optional on %s, where %s is not priced; price the add-on there first", ErrConflict, cur.Key, strings.Join(unpriced, ", "), next.AddonSKU)
			}
		}
	}
	if err := validateFeatureShape(next); err != nil {
		return Feature{}, err
	}
	var addon any
	if next.AddonSKU != "" {
		addon = next.AddonSKU
	}
	if p.IconID != nil {
		next.IconID = *p.IconID
	}
	if p.IconBG != nil {
		next.IconBG = *p.IconBG
	}
	icon, err := iconRef(ctx, tx, next.IconID)
	if err != nil {
		return Feature{}, err
	}
	bg, err := colourArg("icon_bg", next.IconBG)
	if err != nil {
		return Feature{}, err
	}
	out, err := scanFeature(tx.QueryRowContext(ctx, `UPDATE features AS f SET name = $2, blurb = $3, kind = $4, feature_group = $5, unit = $6, addon_sku = $7, levels = $8::jsonb, teaser = $9, sort_order = $10, icon_id = $11, icon_bg = $12, updated_at = now()
		WHERE f.id = $1 RETURNING `+featureColumns, cur.ID, next.Name, next.Blurb, next.Kind, next.Group, next.Unit, addon, levelsJSON(next.Levels), next.Teaser, next.SortOrder, icon, bg))
	if err != nil {
		return Feature{}, err
	}
	return out, tx.Commit()
}

// FeatureDependants is what still refers to a feature: the package cells of
// the books that carry it and the Sources that have taken it as an add-on.
type FeatureDependants struct {
	Cells   int      `json:"cells"`
	Books   []string `json:"books"`
	Sources int      `json:"sources"`
}

// DeleteFeature removes a feature. It is refused with ErrConflict — and the
// dependants, so the console can say what to clear first — while any package
// cell names it or any Source has taken it as an add-on; a feature on an
// invoice cannot simply vanish from the matrix that explains the invoice.
func (s *Store) DeleteFeature(ctx context.Context, id string) (FeatureDependants, error) {
	var dep FeatureDependants
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dep, err
	}
	defer tx.Rollback()
	f, err := scanFeature(tx.QueryRowContext(ctx, `SELECT `+featureColumns+` FROM features f WHERE f.id::text = $1 OR f.key = $1 FOR UPDATE`, strings.TrimSpace(id)))
	if err != nil {
		return dep, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*), COALESCE(array_agg(DISTINCT b.name ORDER BY b.name) FILTER (WHERE b.name IS NOT NULL), '{}')
		FROM package_entitlements e JOIN price_books b ON b.id = e.price_book_id WHERE e.feature_id = $1`, f.ID).Scan(&dep.Cells, pq.Array(&dep.Books)); err != nil {
		return dep, mapErr(err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM source_addons WHERE feature_id = $1`, f.ID).Scan(&dep.Sources); err != nil {
		return dep, mapErr(err)
	}
	if dep.Books == nil {
		dep.Books = []string{}
	}
	if dep.Cells > 0 || dep.Sources > 0 {
		var parts []string
		if dep.Cells > 0 {
			parts = append(parts, fmt.Sprintf("%d package cell(s) of %s", dep.Cells, strings.Join(dep.Books, ", ")))
		}
		if dep.Sources > 0 {
			parts = append(parts, fmt.Sprintf("%d source(s) that have taken it as an add-on", dep.Sources))
		}
		return dep, fmt.Errorf("%w: %s is still used by %s; clear those first", ErrConflict, f.Key, strings.Join(parts, " and "))
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM features WHERE id = $1`, f.ID); err != nil {
		return dep, mapDeleteErr(err)
	}
	return dep, tx.Commit()
}

// Entitlement is one cell of a book's matrix: the state of one feature on one
// plan, the quantity included and its overage policy when the feature is a
// quantity, the level when it is a level, and the operator's note. Feature is
// joined on every read.
type Entitlement struct {
	PriceBookID      string   `json:"price_book_id"`
	PlanSKU          string   `json:"plan_sku"`
	FeatureID        string   `json:"feature_id"`
	State            string   `json:"state"`
	IncludedQuantity *Decimal `json:"included_quantity,omitempty"`
	// Overage is the policy above the included quantity of a quantity cell:
	// metered, hard_cap or unlimited. Empty on the other kinds.
	Overage string `json:"overage,omitempty"`
	// Level is the index into the feature's levels on a level cell.
	Level     *int      `json:"level,omitempty"`
	Note      string    `json:"note,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	Feature   Feature   `json:"feature"`
}

// EntitlementInput is one cell as the console or the seeder writes it.
type EntitlementInput struct {
	State            string
	IncludedQuantity *Decimal
	// Overage is the quantity cell's policy; empty reads metered, which is
	// what every cell written before the ladder was.
	Overage string
	Level   *int
	Note    string
}

const entitlementColumns = `e.price_book_id, e.plan_sku, e.feature_id, e.state, e.included_quantity::text, COALESCE(e.overage, ''), e.level, e.note, e.updated_at, ` + featureColumns

func scanEntitlement(row interface{ Scan(...any) error }) (Entitlement, error) {
	var e Entitlement
	var qty sql.NullString
	var level sql.NullInt64
	if err := scanFeatureInto(row, &e.Feature, &e.PriceBookID, &e.PlanSKU, &e.FeatureID, &e.State, &qty, &e.Overage, &level, &e.Note, &e.UpdatedAt); err != nil {
		return e, err
	}
	e.IncludedQuantity = decPtr(qty)
	if level.Valid {
		l := int(level.Int64)
		e.Level = &l
	}
	e.UpdatedAt = e.UpdatedAt.UTC()
	return e, nil
}

func (s *Store) queryEntitlements(ctx context.Context, where string, args ...any) ([]Entitlement, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+entitlementColumns+` FROM package_entitlements e JOIN features f ON f.id = e.feature_id `+where+` ORDER BY f.sort_order, f.key, e.plan_sku`, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []Entitlement{}
	for rows.Next() {
		e, err := scanEntitlement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PackageCells returns every cell of a book's matrix, features joined.
func (s *Store) PackageCells(ctx context.Context, priceBookID string) ([]Entitlement, error) {
	return s.queryEntitlements(ctx, `WHERE e.price_book_id = $1`, priceBookID)
}

// PackageEntitlements returns the cells of ONE package — what the rating run
// applies to a Source on that plan.
func (s *Store) PackageEntitlements(ctx context.Context, priceBookID, planSKU string) ([]Entitlement, error) {
	return s.queryEntitlements(ctx, `WHERE e.price_book_id = $1 AND e.plan_sku = $2`, priceBookID, strings.TrimSpace(planSKU))
}

// GetEntitlement returns one cell; ErrNotFound when the package has no row
// for the feature, which reads as "not offered".
func (s *Store) GetEntitlement(ctx context.Context, priceBookID, planSKU, featureRef string) (Entitlement, error) {
	return scanEntitlement(s.db.QueryRowContext(ctx, `SELECT `+entitlementColumns+` FROM package_entitlements e JOIN features f ON f.id = e.feature_id
		WHERE e.price_book_id = $1 AND e.plan_sku = $2 AND (f.id::text = $3 OR f.key = $3)`, priceBookID, strings.TrimSpace(planSKU), strings.TrimSpace(featureRef)))
}

// packagePlan checks that planSKU is a plan.<slug> item the book prices and
// returns the slug and the book's name.
func packagePlan(ctx context.Context, tx *sql.Tx, priceBookID, planSKU string) (slug, bookName string, err error) {
	if err := tx.QueryRowContext(ctx, `SELECT name FROM price_books WHERE id = $1 FOR UPDATE`, priceBookID).Scan(&bookName); err != nil {
		return "", "", mapErr(err)
	}
	slug = strings.TrimPrefix(planSKU, PlanSKUPrefix)
	if !strings.HasPrefix(planSKU, PlanSKUPrefix) || !PlanBillable(slug) {
		return "", "", fmt.Errorf("%w: %q is not a package; a package is a priced plan.<slug> item of the book", ErrInvalid, planSKU)
	}
	var planPriced bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM price_items WHERE price_book_id = $1 AND sku = $2)`, priceBookID, planSKU).Scan(&planPriced); err != nil {
		return "", "", mapErr(err)
	}
	if !planPriced {
		return "", "", fmt.Errorf("%w: %s is not priced in %s; a package is a plan item of the book", ErrInvalid, planSKU, bookName)
	}
	return slug, bookName, nil
}

// PutEntitlement writes one cell. The plan must be a plan.<slug> item the
// book prices. Per kind (DESIGN.md §22.1): a BOOLEAN cell marked optional
// needs the feature's add-on SKU priced in the book; a QUANTITY cell is
// included with its quantity and an overage policy (metered needs the SKU
// priced) or not offered, never optional; a LEVEL cell carries a level that
// indexes into the feature's levels, and optional means the NEXT level is
// purchasable (there must be one, and the add-on must be priced); an ACCESS
// cell is included or not offered; a FLOOR item has no cell at all.
func (s *Store) PutEntitlement(ctx context.Context, priceBookID, planSKU, featureRef string, in EntitlementInput) (Entitlement, error) {
	planSKU = strings.TrimSpace(planSKU)
	in.State = strings.ToLower(strings.TrimSpace(in.State))
	in.Overage = strings.ToLower(strings.TrimSpace(in.Overage))
	if !ValidEntitlementState(in.State) {
		return Entitlement{}, fmt.Errorf("%w: state must be included, optional or not_offered", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Entitlement{}, err
	}
	defer tx.Rollback()
	slug, bookName, err := packagePlan(ctx, tx, priceBookID, planSKU)
	if err != nil {
		return Entitlement{}, err
	}
	f, err := scanFeature(tx.QueryRowContext(ctx, `SELECT `+featureColumns+` FROM features f WHERE f.id::text = $1 OR f.key = $1`, strings.TrimSpace(featureRef)))
	if err != nil {
		return Entitlement{}, err
	}
	if f.Group == FeatureGroupFloor {
		return Entitlement{}, fmt.Errorf("%w: %s is a floor item — on every package, never priced; it has no cell", ErrInvalid, f.Key)
	}
	skuPriced := func(sku string) (bool, error) {
		var priced bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM price_items WHERE price_book_id = $1 AND sku = $2)`, priceBookID, sku).Scan(&priced)
		return priced, mapErr(err)
	}
	var qty, overage, level any
	switch f.Kind {
	case FeatureKindAccess:
		if in.State == EntitlementOptional {
			return Entitlement{}, fmt.Errorf("%w: %s is a platform door; it is included or not offered, never an add-on", ErrInvalid, f.Key)
		}
	case FeatureKindBoolean:
		if in.State == EntitlementOptional {
			if f.AddonSKU == "" {
				return Entitlement{}, fmt.Errorf("%w: %s has no add-on SKU; set one on the feature before offering it as an add-on", ErrInvalid, f.Key)
			}
			priced, err := skuPriced(f.AddonSKU)
			if err != nil {
				return Entitlement{}, err
			}
			if !priced {
				return Entitlement{}, fmt.Errorf("%w: %s is optional on %s but its add-on SKU %s is not priced in %s; price %s first", ErrInvalid, f.Key, PlanName(slug), f.AddonSKU, bookName, f.AddonSKU)
			}
		}
	case FeatureKindQuantity:
		if in.State == EntitlementOptional {
			return Entitlement{}, fmt.Errorf("%w: %s is a quantity feature; it is included with a quantity and an overage policy, or not offered — more of it is bought through its overage, not as an add-on", ErrInvalid, f.Key)
		}
		if in.IncludedQuantity != nil && strings.TrimSpace(string(*in.IncludedQuantity)) != "" {
			if ratOf(*in.IncludedQuantity).Sign() < 0 {
				return Entitlement{}, fmt.Errorf("%w: an included quantity cannot be negative", ErrInvalid)
			}
			qty = string(*in.IncludedQuantity)
		}
		if in.Overage == "" {
			in.Overage = OverageMetered
		}
		if !ValidOverage(in.Overage) {
			return Entitlement{}, fmt.Errorf("%w: overage must be metered, hard_cap or unlimited", ErrInvalid)
		}
		if in.State == EntitlementIncluded {
			if qty == nil && in.Overage != OverageUnlimited {
				return Entitlement{}, fmt.Errorf("%w: %s is a quantity feature; say how much the %s package includes (%s)", ErrInvalid, f.Key, PlanName(slug), f.Unit)
			}
			if in.Overage == OverageMetered {
				if f.AddonSKU == "" {
					return Entitlement{}, fmt.Errorf("%w: %s is metered above what %s includes but has no SKU to meter it on; set one on the feature, or cap it", ErrInvalid, f.Key, PlanName(slug))
				}
				priced, err := skuPriced(f.AddonSKU)
				if err != nil {
					return Entitlement{}, err
				}
				if !priced {
					return Entitlement{}, fmt.Errorf("%w: %s is metered above what %s includes but %s is not priced in %s; price it first, or cap it", ErrInvalid, f.Key, PlanName(slug), f.AddonSKU, bookName)
				}
			}
		}
		overage = in.Overage
	case FeatureKindLevel:
		if in.State != EntitlementNotOffered || in.Level != nil {
			if in.Level == nil {
				return Entitlement{}, fmt.Errorf("%w: %s is a level feature; say which level the %s package is at (0 = %s)", ErrInvalid, f.Key, PlanName(slug), f.Levels[0])
			}
			if *in.Level < 0 || *in.Level >= len(f.Levels) {
				return Entitlement{}, fmt.Errorf("%w: %s has %d levels (0 to %d); %d is not one of them", ErrInvalid, f.Key, len(f.Levels), len(f.Levels)-1, *in.Level)
			}
			level = *in.Level
		}
		if in.State == EntitlementOptional {
			if *in.Level+1 >= len(f.Levels) {
				return Entitlement{}, fmt.Errorf("%w: %s on %s is already at the top level (%s); there is no next level to offer", ErrInvalid, f.Key, PlanName(slug), f.Levels[*in.Level])
			}
			if f.AddonSKU == "" {
				return Entitlement{}, fmt.Errorf("%w: %s has no add-on SKU; set one on the feature before offering its next level as an add-on", ErrInvalid, f.Key)
			}
			priced, err := skuPriced(f.AddonSKU)
			if err != nil {
				return Entitlement{}, err
			}
			if !priced {
				return Entitlement{}, fmt.Errorf("%w: the next level of %s is purchasable on %s but its add-on SKU %s is not priced in %s; price %s first", ErrInvalid, f.Key, PlanName(slug), f.AddonSKU, bookName, f.AddonSKU)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO package_entitlements (price_book_id, plan_sku, feature_id, state, included_quantity, overage, level, note)
		VALUES ($1, $2, $3, $4, $5::numeric, $6, $7, $8)
		ON CONFLICT (price_book_id, plan_sku, feature_id) DO UPDATE SET state = EXCLUDED.state, included_quantity = EXCLUDED.included_quantity, overage = EXCLUDED.overage, level = EXCLUDED.level, note = EXCLUDED.note, updated_at = now()`,
		priceBookID, planSKU, f.ID, in.State, qty, overage, level, strings.TrimSpace(in.Note)); err != nil {
		return Entitlement{}, mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return Entitlement{}, err
	}
	return s.GetEntitlement(ctx, priceBookID, planSKU, f.ID)
}

// DeleteEntitlement removes one cell; the package then reads "not offered"
// for the feature.
func (s *Store) DeleteEntitlement(ctx context.Context, priceBookID, planSKU, featureRef string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM package_entitlements e USING features f WHERE f.id = e.feature_id
		AND e.price_book_id = $1 AND e.plan_sku = $2 AND (f.id::text = $3 OR f.key = $3)`, priceBookID, strings.TrimSpace(planSKU), strings.TrimSpace(featureRef))
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PackageSettings is what a package carries besides its cells (DESIGN.md
// §22.1): the tagline under its name, whether the storefront marks it
// recommended, the term rule (months free on an annual term) and its SHAPE —
// the vCPU and memory headline with the guaranteed floors under them, and
// the disk. A shape value left nil falls back to the catalog's constants
// (PlanShape) in the published document.
type PackageSettings struct {
	PriceBookID        string   `json:"price_book_id"`
	PlanSKU            string   `json:"plan_sku"`
	Tagline            string   `json:"tagline"`
	Recommended        bool     `json:"recommended"`
	AnnualMonthsFree   int      `json:"annual_months_free"`
	VCPU               *Decimal `json:"vcpu,omitempty"`
	MemoryGB           *Decimal `json:"memory_gb,omitempty"`
	VCPUGuaranteed     *Decimal `json:"vcpu_guaranteed,omitempty"`
	MemoryGBGuaranteed *Decimal `json:"memory_gb_guaranteed,omitempty"`
	DiskGB             *Decimal `json:"disk_gb,omitempty"`
	// IconID, Accent and Badge brand the package's column (DESIGN.md
	// §22.10): an icon, a "#RRGGBB" accent colour, a short badge ("Most
	// popular"). Empty = none.
	IconID    string    `json:"icon_id,omitempty"`
	Accent    string    `json:"accent,omitempty"`
	Badge     string    `json:"badge,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PackageSettingsInput is the settings as the console writes them, whole.
type PackageSettingsInput struct {
	Tagline            string
	Recommended        bool
	AnnualMonthsFree   int
	VCPU               *Decimal
	MemoryGB           *Decimal
	VCPUGuaranteed     *Decimal
	MemoryGBGuaranteed *Decimal
	DiskGB             *Decimal
	IconID             string
	Accent             string
	Badge              string
}

const packageSettingsColumns = `price_book_id, plan_sku, tagline, recommended, annual_months_free, vcpu::text, memory_gb::text, vcpu_guaranteed::text, memory_gb_guaranteed::text, disk_gb::text, COALESCE(icon_id, ''), COALESCE(accent, ''), badge, updated_at`

func scanPackageSettings(row interface{ Scan(...any) error }) (PackageSettings, error) {
	var ps PackageSettings
	var vcpu, mem, vcpuG, memG, disk sql.NullString
	if err := row.Scan(&ps.PriceBookID, &ps.PlanSKU, &ps.Tagline, &ps.Recommended, &ps.AnnualMonthsFree, &vcpu, &mem, &vcpuG, &memG, &disk, &ps.IconID, &ps.Accent, &ps.Badge, &ps.UpdatedAt); err != nil {
		return ps, mapErr(err)
	}
	ps.VCPU, ps.MemoryGB, ps.VCPUGuaranteed, ps.MemoryGBGuaranteed, ps.DiskGB = decPtr(vcpu), decPtr(mem), decPtr(vcpuG), decPtr(memG), decPtr(disk)
	ps.UpdatedAt = ps.UpdatedAt.UTC()
	return ps, nil
}

// PackageSettingsOf returns a book's package settings keyed by plan SKU.
func (s *Store) PackageSettingsOf(ctx context.Context, priceBookID string) (map[string]PackageSettings, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+packageSettingsColumns+` FROM package_settings WHERE price_book_id = $1 ORDER BY plan_sku`, priceBookID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := map[string]PackageSettings{}
	for rows.Next() {
		ps, err := scanPackageSettings(rows)
		if err != nil {
			return nil, err
		}
		out[ps.PlanSKU] = ps
	}
	return out, rows.Err()
}

func shapeValue(d *Decimal, what string) (any, error) {
	if d == nil || strings.TrimSpace(string(*d)) == "" {
		return nil, nil
	}
	r, ok := new(big.Rat).SetString(strings.TrimSpace(string(*d)))
	if !ok {
		return nil, fmt.Errorf("%w: %s is not a number", ErrInvalid, what)
	}
	if r.Sign() < 0 {
		return nil, fmt.Errorf("%w: %s cannot be negative", ErrInvalid, what)
	}
	return strings.TrimSpace(string(*d)), nil
}

// PutPackageSettings writes the settings of one package, whole. The plan must
// be a plan.<slug> item the book prices; the months free are 0 to 12; every
// shape value is a non-negative number or absent.
func (s *Store) PutPackageSettings(ctx context.Context, priceBookID, planSKU string, in PackageSettingsInput) (PackageSettings, error) {
	planSKU = strings.TrimSpace(planSKU)
	if in.AnnualMonthsFree < 0 || in.AnnualMonthsFree > 12 {
		return PackageSettings{}, fmt.Errorf("%w: annual months free is between 0 and 12", ErrInvalid)
	}
	vals := make([]any, 5)
	for i, v := range []struct {
		d    *Decimal
		what string
	}{{in.VCPU, "vcpu"}, {in.MemoryGB, "memory_gb"}, {in.VCPUGuaranteed, "vcpu_guaranteed"}, {in.MemoryGBGuaranteed, "memory_gb_guaranteed"}, {in.DiskGB, "disk_gb"}} {
		x, err := shapeValue(v.d, v.what)
		if err != nil {
			return PackageSettings{}, err
		}
		vals[i] = x
	}
	accent, err := colourArg("accent", in.Accent)
	if err != nil {
		return PackageSettings{}, err
	}
	badge := strings.TrimSpace(in.Badge)
	if utf8.RuneCountInString(badge) > BadgeMaxRunes {
		return PackageSettings{}, fmt.Errorf("%w: a badge is at most %d characters (\"Most popular\"); %q is %d", ErrInvalid, BadgeMaxRunes, badge, utf8.RuneCountInString(badge))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PackageSettings{}, err
	}
	defer tx.Rollback()
	if _, _, err := packagePlan(ctx, tx, priceBookID, planSKU); err != nil {
		return PackageSettings{}, err
	}
	icon, err := iconRef(ctx, tx, in.IconID)
	if err != nil {
		return PackageSettings{}, err
	}
	ps, err := scanPackageSettings(tx.QueryRowContext(ctx, `INSERT INTO package_settings (price_book_id, plan_sku, tagline, recommended, annual_months_free, vcpu, memory_gb, vcpu_guaranteed, memory_gb_guaranteed, disk_gb, icon_id, accent, badge)
		VALUES ($1, $2, $3, $4, $5, $6::numeric, $7::numeric, $8::numeric, $9::numeric, $10::numeric, $11, $12, $13)
		ON CONFLICT (price_book_id, plan_sku) DO UPDATE SET tagline = EXCLUDED.tagline, recommended = EXCLUDED.recommended, annual_months_free = EXCLUDED.annual_months_free,
			vcpu = EXCLUDED.vcpu, memory_gb = EXCLUDED.memory_gb, vcpu_guaranteed = EXCLUDED.vcpu_guaranteed, memory_gb_guaranteed = EXCLUDED.memory_gb_guaranteed, disk_gb = EXCLUDED.disk_gb,
			icon_id = EXCLUDED.icon_id, accent = EXCLUDED.accent, badge = EXCLUDED.badge, updated_at = now()
		RETURNING `+packageSettingsColumns,
		priceBookID, planSKU, strings.TrimSpace(in.Tagline), in.Recommended, in.AnnualMonthsFree, vals[0], vals[1], vals[2], vals[3], vals[4], icon, accent, badge))
	if err != nil {
		return PackageSettings{}, err
	}
	return ps, tx.Commit()
}

// SetSourceAddons replaces the add-ons a platform Source has taken, named by
// feature key. Every key is checked against the Source's book and its
// customer's plan: an OPTIONAL feature is taken (a boolean add-on, or the next
// level of a level feature); an INCLUDED one is refused as redundant (the
// package already carries it); a feature the package does not offer — no
// cell, or a not_offered cell — is refused as not offered.
func (s *Store) SetSourceAddons(ctx context.Context, sourceID string, keys []string) (CostSource, error) {
	src, err := s.GetSource(ctx, OperatorScope, sourceID)
	if err != nil {
		return CostSource{}, err
	}
	if src.Internal {
		return CostSource{}, fmt.Errorf("%w: the internal platform source is never billed and takes no add-ons", ErrInvalid)
	}
	if src.Layer != LayerPlatform {
		return CostSource{}, fmt.Errorf("%w: add-ons belong to a platform source on a package; %s is a %s source", ErrInvalid, src.Label(), src.Layer)
	}
	clean := []string{}
	seen := map[string]bool{}
	for _, k := range keys {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		clean = append(clean, k)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CostSource{}, err
	}
	defer tx.Rollback()
	ids := make([]string, 0, len(clean))
	if len(clean) > 0 {
		if src.PriceBookID == nil {
			return CostSource{}, fmt.Errorf("%w: %s has no price book, so no package offers it an add-on; assign the plans book first", ErrInvalid, src.Label())
		}
		var planSlug string
		if err := tx.QueryRowContext(ctx, `SELECT plan_slug FROM customers WHERE id = $1`, src.CustomerID).Scan(&planSlug); err != nil {
			return CostSource{}, mapErr(err)
		}
		planSlug = NormalizePlanSlug(planSlug)
		if !PlanBillable(planSlug) {
			return CostSource{}, fmt.Errorf("%w: %s is not on a sized package (plan %q); add-ons are offered per package", ErrInvalid, src.Label(), planSlug)
		}
		planSKU := PlanSKU(planSlug)
		for _, key := range clean {
			f, err := scanFeature(tx.QueryRowContext(ctx, `SELECT `+featureColumns+` FROM features f WHERE f.key = $1 OR f.id::text = $1`, key))
			if errors.Is(err, ErrNotFound) {
				return CostSource{}, fmt.Errorf("%w: there is no feature %q", ErrInvalid, key)
			}
			if err != nil {
				return CostSource{}, err
			}
			var state string
			var level sql.NullInt64
			err = tx.QueryRowContext(ctx, `SELECT state, level FROM package_entitlements WHERE price_book_id = $1 AND plan_sku = $2 AND feature_id = $3`, *src.PriceBookID, planSKU, f.ID).Scan(&state, &level)
			if errors.Is(err, sql.ErrNoRows) {
				state = EntitlementNotOffered
			} else if err != nil {
				return CostSource{}, mapErr(err)
			}
			switch state {
			case EntitlementOptional:
				ids = append(ids, f.ID)
			case EntitlementIncluded:
				if f.Kind == FeatureKindLevel && level.Valid && int(level.Int64) < len(f.Levels) {
					return CostSource{}, fmt.Errorf("%w: the %s package has %s at %s and offers no level above it", ErrInvalid, PlanName(planSlug), f.Name, f.Levels[level.Int64])
				}
				return CostSource{}, fmt.Errorf("%w: %s is included in the %s package; there is nothing to add", ErrInvalid, f.Name, PlanName(planSlug))
			default:
				return CostSource{}, fmt.Errorf("%w: %s is not offered on the %s package", ErrInvalid, f.Name, PlanName(planSlug))
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM source_addons WHERE source_id = $1 AND NOT (feature_id = ANY($2::uuid[]))`, src.ID, pq.Array(ids)); err != nil {
		return CostSource{}, mapErr(err)
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `INSERT INTO source_addons (source_id, feature_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, src.ID, id); err != nil {
			return CostSource{}, mapErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return CostSource{}, err
	}
	return s.GetSource(ctx, OperatorScope, src.ID)
}

// MinorUnitDigits is how many decimals the currency's minor unit has (three
// for the rial and the dinars, two elsewhere) — what a published price per
// month is rounded to.
func MinorUnitDigits(currency string) int { return minorUnitDigits(currency) }

// colourArg is a "#RRGGBB" colour as a column value: nil when empty, the
// upper-case colour otherwise, ErrInvalid naming the field when malformed.
func colourArg(field, c string) (any, error) {
	n, err := NormalizeColour(field, c)
	if err != nil || n == "" {
		return nil, err
	}
	return n, nil
}
