package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/lib/pq"
)

// PACKAGES AND THE ENTITLEMENT MATRIX (DESIGN.md §22, founder direction
// 2026-10-10).
//
// The SME marketplace sells the catalog plans as hosting-style PACKAGES — S,
// M, L, XL. Technically every feature exists for every Organization (SSL,
// SSO, WAF, backups …); commercially each feature is, per package, either
// INCLUDED, OPTIONAL (a paid add-on) or NOT OFFERED, so the larger package is
// visibly the better deal. This file holds that matrix ONCE — the storefront
// comparison table, the public calculator, the statement run and the invoice
// all read the same rows — and the rules that keep it billable:
//
//   - a feature marked OPTIONAL on a package REQUIRES an add-on SKU priced in
//     that book, refused otherwise naming the SKU (an optional feature with no
//     price would be a promise the invoice cannot keep);
//   - a QUANTITY feature marked INCLUDED carries the quantity included (50
//     Mbps); the engine turns it into an ALLOWANCE on the feature's SKU — the
//     existing allowance path, not a second one;
//   - a feature is deleted only while nothing depends on it: no cell of any
//     book's matrix, no Source that has taken it as an add-on.
//
// The matrix hangs off the PRICE BOOK, per plan item, so a negotiated clone
// of the plans book carries its own copy (ClonePriceBook copies the cells),
// and deleting a book removes its cells with it.

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

// Feature kinds. A BOOLEAN feature is either carried by the package or not
// (SSL, backup, SSO); a QUANTITY feature comes with a quantity the package
// includes (50 Mbps of bandwidth), which the engine applies as an allowance.
const (
	FeatureKindBoolean  = "boolean"
	FeatureKindQuantity = "quantity"
)

// FeatureKinds is every kind, in display order.
var FeatureKinds = []string{FeatureKindBoolean, FeatureKindQuantity}

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

// Feature is one row of the matrix: what the marketplace lists, how it is
// counted, and the SKU that prices it when a package offers it as an
// add-on — or, for a quantity feature, the metered SKU its included quantity
// is an allowance on and its excess is billed at.
type Feature struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Name  string `json:"name"`
	Blurb string `json:"blurb"`
	Kind  string `json:"kind"`
	// Unit is the quantity's unit on a quantity feature ("Mbps"); empty on a
	// boolean one.
	Unit string `json:"unit,omitempty"`
	// AddonSKU is the SKU priced when the feature is OPTIONAL on a package
	// (the add-on line), and the SKU a quantity feature's included quantity is
	// an allowance on. Empty = the feature can only be included or not offered.
	AddonSKU  string    `json:"addon_sku,omitempty"`
	SortOrder int       `json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// FeatureInput is the creatable subset.
type FeatureInput struct {
	Key       string
	Name      string
	Blurb     string
	Kind      string
	Unit      string
	AddonSKU  string
	SortOrder int
}

// FeaturePatch carries optional edits; nil means unchanged. An AddonSKU of
// "" clears the add-on SKU, which is refused while any package offers the
// feature as optional (the add-on would have nothing to price).
type FeaturePatch struct {
	Name      *string
	Blurb     *string
	Kind      *string
	Unit      *string
	AddonSKU  *string
	SortOrder *int
}

var featureKeyShape = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)

// ValidFeatureKey reports whether key is a lower-case identifier.
func ValidFeatureKey(key string) bool { return featureKeyShape.MatchString(key) }

const featureColumns = `f.id, f.key, f.name, f.blurb, f.kind, f.unit, COALESCE(f.addon_sku, ''), f.sort_order, f.created_at, f.updated_at`

func scanFeature(row interface{ Scan(...any) error }) (Feature, error) {
	var f Feature
	if err := row.Scan(&f.ID, &f.Key, &f.Name, &f.Blurb, &f.Kind, &f.Unit, &f.AddonSKU, &f.SortOrder, &f.CreatedAt, &f.UpdatedAt); err != nil {
		return f, mapErr(err)
	}
	f.CreatedAt, f.UpdatedAt = f.CreatedAt.UTC(), f.UpdatedAt.UTC()
	return f, nil
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

func validateFeatureShape(kind, unit, key string) error {
	if !ValidFeatureKey(key) {
		return fmt.Errorf("%w: a feature key is lower-case letters, digits, dot, dash or underscore", ErrInvalid)
	}
	if kind != FeatureKindBoolean && kind != FeatureKindQuantity {
		return fmt.Errorf("%w: kind must be boolean or quantity", ErrInvalid)
	}
	if kind == FeatureKindQuantity && strings.TrimSpace(unit) == "" {
		return fmt.Errorf("%w: a quantity feature needs a unit (Mbps, GB)", ErrInvalid)
	}
	return nil
}

// CreateFeature inserts a feature. A duplicate key is ErrConflict.
func (s *Store) CreateFeature(ctx context.Context, in FeatureInput) (Feature, error) {
	in.Key = strings.ToLower(strings.TrimSpace(in.Key))
	in.Kind = strings.ToLower(strings.TrimSpace(in.Kind))
	if in.Kind == "" {
		in.Kind = FeatureKindBoolean
	}
	if strings.TrimSpace(in.Name) == "" {
		return Feature{}, fmt.Errorf("%w: a feature needs a name", ErrInvalid)
	}
	if err := validateFeatureShape(in.Kind, in.Unit, in.Key); err != nil {
		return Feature{}, err
	}
	var addon any
	if sku := strings.TrimSpace(in.AddonSKU); sku != "" {
		addon = sku
	}
	return scanFeature(s.db.QueryRowContext(ctx, `INSERT INTO features AS f (key, name, blurb, kind, unit, addon_sku, sort_order)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+featureColumns,
		in.Key, strings.TrimSpace(in.Name), strings.TrimSpace(in.Blurb), in.Kind, strings.TrimSpace(in.Unit), addon, in.SortOrder))
}

// UpdateFeature applies a patch. The kind may change only while no package
// cell names the feature (a quantity cell carries a quantity a boolean cannot
// read); the add-on SKU may be cleared or changed only if every package that
// offers the feature as optional still has the new SKU priced.
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
	if p.Kind != nil {
		next.Kind = strings.ToLower(strings.TrimSpace(*p.Kind))
		if next.Kind != cur.Kind {
			var cells int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM package_entitlements WHERE feature_id = $1`, cur.ID).Scan(&cells); err != nil {
				return Feature{}, mapErr(err)
			}
			if cells > 0 {
				return Feature{}, fmt.Errorf("%w: %s is in %d package cell(s); its kind cannot change while a package carries it", ErrConflict, cur.Key, cells)
			}
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
	if err := validateFeatureShape(next.Kind, next.Unit, next.Key); err != nil {
		return Feature{}, err
	}
	var addon any
	if next.AddonSKU != "" {
		addon = next.AddonSKU
	}
	out, err := scanFeature(tx.QueryRowContext(ctx, `UPDATE features AS f SET name = $2, blurb = $3, kind = $4, unit = $5, addon_sku = $6, sort_order = $7, updated_at = now()
		WHERE f.id = $1 RETURNING `+featureColumns, cur.ID, next.Name, next.Blurb, next.Kind, next.Unit, addon, next.SortOrder))
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
// plan, the quantity included when the feature is a quantity, and the
// operator's note. Feature is joined on every read.
type Entitlement struct {
	PriceBookID      string    `json:"price_book_id"`
	PlanSKU          string    `json:"plan_sku"`
	FeatureID        string    `json:"feature_id"`
	State            string    `json:"state"`
	IncludedQuantity *Decimal  `json:"included_quantity,omitempty"`
	Note             string    `json:"note,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
	Feature          Feature   `json:"feature"`
}

// EntitlementInput is one cell as the console or the seeder writes it.
type EntitlementInput struct {
	State            string
	IncludedQuantity *Decimal
	Note             string
}

const entitlementColumns = `e.price_book_id, e.plan_sku, e.feature_id, e.state, e.included_quantity::text, e.note, e.updated_at, ` + featureColumns

func scanEntitlement(row interface{ Scan(...any) error }) (Entitlement, error) {
	var e Entitlement
	var qty sql.NullString
	if err := row.Scan(&e.PriceBookID, &e.PlanSKU, &e.FeatureID, &e.State, &qty, &e.Note, &e.UpdatedAt,
		&e.Feature.ID, &e.Feature.Key, &e.Feature.Name, &e.Feature.Blurb, &e.Feature.Kind, &e.Feature.Unit, &e.Feature.AddonSKU, &e.Feature.SortOrder, &e.Feature.CreatedAt, &e.Feature.UpdatedAt); err != nil {
		return e, mapErr(err)
	}
	e.IncludedQuantity = decPtr(qty)
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

// PutEntitlement writes one cell. The plan must be a plan.<slug> item the
// book prices; OPTIONAL requires the feature's add-on SKU to be priced in the
// book (refused naming the SKU); a quantity feature marked INCLUDED needs the
// quantity it includes; a boolean cell carries none.
func (s *Store) PutEntitlement(ctx context.Context, priceBookID, planSKU, featureRef string, in EntitlementInput) (Entitlement, error) {
	planSKU = strings.TrimSpace(planSKU)
	in.State = strings.ToLower(strings.TrimSpace(in.State))
	if !ValidEntitlementState(in.State) {
		return Entitlement{}, fmt.Errorf("%w: state must be included, optional or not_offered", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Entitlement{}, err
	}
	defer tx.Rollback()
	var bookName string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM price_books WHERE id = $1 FOR UPDATE`, priceBookID).Scan(&bookName); err != nil {
		return Entitlement{}, mapErr(err)
	}
	slug := strings.TrimPrefix(planSKU, PlanSKUPrefix)
	if !strings.HasPrefix(planSKU, PlanSKUPrefix) || !PlanBillable(slug) {
		return Entitlement{}, fmt.Errorf("%w: %q is not a package; a package is a priced plan.<slug> item of the book", ErrInvalid, planSKU)
	}
	var planPriced bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM price_items WHERE price_book_id = $1 AND sku = $2)`, priceBookID, planSKU).Scan(&planPriced); err != nil {
		return Entitlement{}, mapErr(err)
	}
	if !planPriced {
		return Entitlement{}, fmt.Errorf("%w: %s is not priced in %s; a package is a plan item of the book", ErrInvalid, planSKU, bookName)
	}
	f, err := scanFeature(tx.QueryRowContext(ctx, `SELECT `+featureColumns+` FROM features f WHERE f.id::text = $1 OR f.key = $1`, strings.TrimSpace(featureRef)))
	if err != nil {
		return Entitlement{}, err
	}
	if in.State == EntitlementOptional {
		if f.AddonSKU == "" {
			return Entitlement{}, fmt.Errorf("%w: %s has no add-on SKU; set one on the feature before offering it as an add-on", ErrInvalid, f.Key)
		}
		var priced bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM price_items WHERE price_book_id = $1 AND sku = $2)`, priceBookID, f.AddonSKU).Scan(&priced); err != nil {
			return Entitlement{}, mapErr(err)
		}
		if !priced {
			return Entitlement{}, fmt.Errorf("%w: %s is optional on %s but its add-on SKU %s is not priced in %s; price %s first", ErrInvalid, f.Key, PlanName(slug), f.AddonSKU, bookName, f.AddonSKU)
		}
	}
	var qty any
	switch {
	case f.Kind == FeatureKindQuantity && in.State == EntitlementIncluded:
		if in.IncludedQuantity == nil || strings.TrimSpace(string(*in.IncludedQuantity)) == "" {
			return Entitlement{}, fmt.Errorf("%w: %s is a quantity feature; say how much the %s package includes (%s)", ErrInvalid, f.Key, PlanName(slug), f.Unit)
		}
		if ratOf(*in.IncludedQuantity).Sign() < 0 {
			return Entitlement{}, fmt.Errorf("%w: an included quantity cannot be negative", ErrInvalid)
		}
		qty = string(*in.IncludedQuantity)
	case f.Kind == FeatureKindQuantity && in.IncludedQuantity != nil && strings.TrimSpace(string(*in.IncludedQuantity)) != "":
		// Kept on an optional or not-offered quantity cell as information
		// ("up to 1000 Mbps"); the engine reads it on an included cell only.
		if ratOf(*in.IncludedQuantity).Sign() < 0 {
			return Entitlement{}, fmt.Errorf("%w: an included quantity cannot be negative", ErrInvalid)
		}
		qty = string(*in.IncludedQuantity)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO package_entitlements (price_book_id, plan_sku, feature_id, state, included_quantity, note)
		VALUES ($1, $2, $3, $4, $5::numeric, $6)
		ON CONFLICT (price_book_id, plan_sku, feature_id) DO UPDATE SET state = EXCLUDED.state, included_quantity = EXCLUDED.included_quantity, note = EXCLUDED.note, updated_at = now()`,
		priceBookID, planSKU, f.ID, in.State, qty, strings.TrimSpace(in.Note)); err != nil {
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

// SetSourceAddons replaces the add-ons a platform Source has taken, named by
// feature key. Every key is checked against the Source's book and its
// customer's plan: an OPTIONAL feature is taken; an INCLUDED one is refused
// as redundant (the package already carries it); a feature the package does
// not offer — no cell, or a not_offered cell — is refused as not offered.
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
			err = tx.QueryRowContext(ctx, `SELECT state FROM package_entitlements WHERE price_book_id = $1 AND plan_sku = $2 AND feature_id = $3`, *src.PriceBookID, planSKU, f.ID).Scan(&state)
			if errors.Is(err, sql.ErrNoRows) {
				state = EntitlementNotOffered
			} else if err != nil {
				return CostSource{}, mapErr(err)
			}
			switch state {
			case EntitlementOptional:
				ids = append(ids, f.ID)
			case EntitlementIncluded:
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
