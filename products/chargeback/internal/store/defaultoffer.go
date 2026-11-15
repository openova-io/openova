package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/offer"
)

// THE DEFAULT OFFER (DESIGN.md §22.12). The engine ships the package ladder
// a fresh Sovereign publishes (internal/offer/default-offer.json) and writes
// it into the plans book ONCE, on the first start against a database whose
// plans book carries no offer. Before this the offer existed only as rows a
// script or a person typed into one installation's database, so a fresh
// database answered 404 on GET /api/v1/public/packages and the storefront had
// nothing to sell from.
//
// Exactly once, never over a sovereign-admin's entry:
//
//   - the decision is recorded in default_offer (one row): `applied` after
//     the offer was written, `skipped` when the database already carried an
//     offer at the first start. Either is final: no later start, restart or
//     upgrade writes the offer again, whatever the book holds by then and
//     whatever a newer release's file says;
//   - "carries an offer" is any feature at all, or any package cell or
//     package setting on the plans book. A plans book with only its plan
//     items (what the Organization sync creates) carries none, and is filled;
//   - nothing that exists is overwritten: a price item already in the book
//     keeps its price (a plan priced differently from the file is logged), a
//     feature already there keeps its fields;
//   - `started` is recorded before the first write. A start that dies
//     mid-way leaves it, and the next start finishes the same writes — every
//     write is an upsert of the engine's own rows, so finishing is safe;
//   - two replicas start together: a session advisory lock makes the second
//     wait and then read the first one's decision.
//
// Every write goes through the store's own rules (PutPriceItems,
// CreateFeature, PutEntitlement, PutPackageSettings, PutIcon,
// PutFeatureGroupIcon), so the file cannot publish a cell the console could
// not have entered.

const defaultOfferMigrationSQL = `
CREATE TABLE IF NOT EXISTS default_offer (
	id SMALLINT PRIMARY KEY DEFAULT 1,
	state TEXT NOT NULL,
	detail TEXT NOT NULL DEFAULT '',
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT default_offer_id_check CHECK (id = 1),
	CONSTRAINT default_offer_state_check CHECK (state IN ('started','applied','skipped'))
);
`

// MigrationDefaultOffer is the schema_migrations version of the default-offer
// migration, located by content like the others.
var MigrationDefaultOffer = func() int {
	for i, m := range migrations {
		if m == defaultOfferMigrationSQL {
			return i + 1
		}
	}
	return len(migrations)
}()

// The recorded states of the default offer.
const (
	DefaultOfferStarted = "started"
	DefaultOfferApplied = "applied"
	DefaultOfferSkipped = "skipped"
)

// defaultOfferLock is the advisory lock key the ensure step holds ("cbdo").
const defaultOfferLock int64 = 0x6362646f

// DefaultOfferResult is what EnsureDefaultOffer decided.
type DefaultOfferResult struct {
	// State is the recorded state after the call: applied or skipped.
	State string
	// Wrote is true when this call wrote the offer.
	Wrote bool
	// Detail says why it was skipped, or what was written.
	Detail string
}

// EnsureDefaultOffer writes o into the plans book once (see the comment at
// the top of this file). actor is the audit actor of the one audit entry the
// write records.
func (s *Store) EnsureDefaultOffer(ctx context.Context, o offer.Offer, actor string) (DefaultOfferResult, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return DefaultOfferResult{}, err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, defaultOfferLock); err != nil {
		return DefaultOfferResult{}, fmt.Errorf("default offer: take the lock: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, defaultOfferLock)
	}()

	var state, detail string
	err = conn.QueryRowContext(ctx, `SELECT state, detail FROM default_offer WHERE id = 1`).Scan(&state, &detail)
	switch {
	case err == nil && state != DefaultOfferStarted:
		return DefaultOfferResult{State: state, Detail: detail}, nil
	case err == nil:
		// A previous start died mid-way: finish its writes.
	case errors.Is(err, sql.ErrNoRows):
		present, why, err := s.offerPresent(ctx)
		if err != nil {
			return DefaultOfferResult{}, err
		}
		if present {
			if err := setDefaultOffer(ctx, conn, DefaultOfferSkipped, why); err != nil {
				return DefaultOfferResult{}, err
			}
			return DefaultOfferResult{State: DefaultOfferSkipped, Detail: why}, nil
		}
		if err := setDefaultOffer(ctx, conn, DefaultOfferStarted, ""); err != nil {
			return DefaultOfferResult{}, err
		}
	default:
		return DefaultOfferResult{}, mapErr(err)
	}
	written, err := s.writeDefaultOffer(ctx, o)
	if err != nil {
		return DefaultOfferResult{State: DefaultOfferStarted}, fmt.Errorf("default offer: %w", err)
	}
	if err := setDefaultOffer(ctx, conn, DefaultOfferApplied, written); err != nil {
		return DefaultOfferResult{}, err
	}
	if err := s.Audit(ctx, nil, actor, "default_offer.applied", map[string]any{"price_book": PlanBookName, "written": written}); err != nil {
		return DefaultOfferResult{}, err
	}
	return DefaultOfferResult{State: DefaultOfferApplied, Wrote: true, Detail: written}, nil
}

func setDefaultOffer(ctx context.Context, conn *sql.Conn, state, detail string) error {
	_, err := conn.ExecContext(ctx, `INSERT INTO default_offer (id, state, detail) VALUES (1, $1, $2)
		ON CONFLICT (id) DO UPDATE SET state = EXCLUDED.state, detail = EXCLUDED.detail, updated_at = now()`, state, detail)
	return mapErr(err)
}

// offerPresent reports whether the database already carries an offer: any
// feature, or any package cell or package setting on the plans book.
func (s *Store) offerPresent(ctx context.Context) (bool, string, error) {
	var features int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM features`).Scan(&features); err != nil {
		return false, "", mapErr(err)
	}
	if features > 0 {
		return true, fmt.Sprintf("the database already holds %d feature(s)", features), nil
	}
	pb, err := s.GetPriceBookByName(ctx, PlanBookName)
	if errors.Is(err, ErrNotFound) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	var cells, settings int
	if err := s.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM package_entitlements WHERE price_book_id = $1), (SELECT count(*) FROM package_settings WHERE price_book_id = $1)`, pb.ID).Scan(&cells, &settings); err != nil {
		return false, "", mapErr(err)
	}
	if cells+settings > 0 {
		return true, fmt.Sprintf("%s already carries %d package cell(s) and %d package setting(s)", PlanBookName, cells, settings), nil
	}
	return false, "", nil
}

// writeDefaultOffer writes the offer into the plans book, creating the book
// when absent, and says what it wrote.
func (s *Store) writeDefaultOffer(ctx context.Context, o offer.Offer) (string, error) {
	pb, _, err := s.EnsurePlanBook(ctx)
	if err != nil {
		return "", fmt.Errorf("the plans book: %w", err)
	}
	have, err := s.ListPriceItems(ctx, pb.ID)
	if err != nil {
		return "", err
	}
	priced := map[string]PriceItem{}
	for _, it := range have {
		priced[it.SKU] = it
	}
	// 1. The price items the book does not price yet: the add-ons and the
	//    meters, and a plan the book lacks. A price already there stays.
	var missing []PriceItem
	for _, it := range o.PriceItems {
		want, err := offerPriceItem(it, pb.AnnualDivisor)
		if err != nil {
			return "", fmt.Errorf("price item %s: %w", it.SKU, err)
		}
		cur, ok := priced[it.SKU]
		if !ok {
			missing = append(missing, want)
			continue
		}
		if cur.AnnualPrice != nil && !sameDecimal(*cur.AnnualPrice, *want.AnnualPrice) {
			slog.Warn("default offer: the plans book prices an item differently from the offer; its price is left as it is", "sku", it.SKU, "book_annual", string(*cur.AnnualPrice), "offer_annual", string(*want.AnnualPrice))
		}
	}
	if len(missing) > 0 {
		if _, err := s.PutPriceItems(ctx, pb.ID, missing, false); err != nil {
			return "", fmt.Errorf("price the add-ons and meters: %w", err)
		}
	}
	// 2. The icons, then each group's.
	ids := map[string]string{}
	for _, file := range o.IconFiles() {
		b, err := offer.IconBytes(file)
		if err != nil {
			return "", err
		}
		ic, _, err := s.PutIcon(ctx, "image/svg+xml", b)
		if err != nil {
			return "", fmt.Errorf("icon %s: %w", file, err)
		}
		ids[file] = ic.ID
	}
	iconOf := func(ic *offer.Icon) (string, string) {
		if ic == nil {
			return "", ""
		}
		return ids[ic.File], ic.BG
	}
	for _, g := range o.Document.Groups {
		if g.Icon == nil {
			continue
		}
		if _, err := s.PutFeatureGroupIcon(ctx, g.Key, ids[g.Icon.File]); err != nil {
			return "", fmt.Errorf("icon of group %s: %w", g.Key, err)
		}
	}
	// 3. The features in document order: the floor, then the matrix rows.
	//    The sort order is the place, so the document reads back in order.
	order, created := 0, 0
	ensureFeature := func(in FeatureInput) error {
		order++
		in.SortOrder = order
		if _, err := s.GetFeature(ctx, in.Key); err == nil {
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if _, err := s.CreateFeature(ctx, in); err != nil {
			return fmt.Errorf("feature %s: %w", in.Key, err)
		}
		created++
		return nil
	}
	for _, f := range o.Document.Floor {
		id, bg := iconOf(f.Icon)
		if err := ensureFeature(FeatureInput{Key: f.Key, Name: f.Name, Blurb: f.Blurb, Kind: FeatureKindBoolean, Group: FeatureGroupFloor, IconID: id, IconBG: bg}); err != nil {
			return "", err
		}
	}
	for _, f := range o.Document.Features {
		id, bg := iconOf(f.Icon)
		if err := ensureFeature(FeatureInput{Key: f.Key, Name: f.Name, Blurb: f.Blurb, Kind: f.Kind, Group: f.Group, Unit: f.Unit, AddonSKU: f.AddonSKU, Levels: f.Levels, Teaser: f.Teaser, IconID: id, IconBG: bg}); err != nil {
			return "", err
		}
	}
	// 4. Every cell, in package order: the cell as the console would enter
	//    it for what the document publishes.
	cells := 0
	for _, f := range o.Document.Features {
		for _, p := range o.Document.Packages {
			c, ok := f.Cells[p.SKU]
			if !ok {
				continue
			}
			in, err := entitlementOf(f, c)
			if err != nil {
				return "", fmt.Errorf("cell %s × %s: %w", p.SKU, f.Key, err)
			}
			if _, err := s.PutEntitlement(ctx, pb.ID, p.SKU, f.Key, in); err != nil {
				return "", fmt.Errorf("cell %s × %s: %w", p.SKU, f.Key, err)
			}
			cells++
		}
	}
	// 5. Each package's settings, after the cells: grow checks its bandwidth
	//    ceiling against the bandwidth cell.
	for _, p := range o.Document.Packages {
		id, _ := iconOf(p.Icon)
		in := PackageSettingsInput{
			Tagline: p.Tagline, Recommended: p.Recommended, AnnualMonthsFree: p.AnnualMonthsFree,
			VCPU: offerDec(p.Shape.VCPU), MemoryGB: offerDec(p.Shape.MemoryGB), VCPUGuaranteed: offerDec(p.Shape.VCPUGuaranteed),
			MemoryGBGuaranteed: offerDec(p.Shape.MemoryGBGuaranteed), DiskGB: offerDec(p.Shape.DiskGB),
			IconID: id, Accent: p.Accent, Badge: p.Badge,
		}
		if p.Grow != nil && p.Grow.Allowed {
			in.GrowAllowed = true
			in.GrowCeilingVCPU, in.GrowCeilingMemoryGB = offerDec(p.Grow.Ceiling.VCPU), offerDec(p.Grow.Ceiling.MemoryGB)
			in.GrowCeilingDiskGB, in.GrowCeilingBandwidthMbps = offerDec(p.Grow.Ceiling.DiskGB), offerDec(p.Grow.Ceiling.BandwidthMbps)
			for _, r := range p.Grow.OverageRates {
				switch r.Key {
				case "vcpu":
					in.OverageVCPUMonth = offerDec(r.PriceMonth)
				case "memory":
					in.OverageMemGBMonth = offerDec(r.PriceMonth)
				}
			}
		}
		if _, err := s.PutPackageSettings(ctx, pb.ID, p.SKU, in); err != nil {
			return "", fmt.Errorf("settings of %s: %w", p.SKU, err)
		}
	}
	return fmt.Sprintf("%d price item(s) added, %d feature(s) created, %d cell(s), %d package setting(s), %d icon(s) into %s",
		len(missing), created, cells, len(o.Document.Packages), len(ids), PlanBookName), nil
}

// entitlementOf is the cell a sovereign-admin would enter for what the
// document publishes: a teaser is a not-offered cell of a teaser feature, a
// level cell with a purchasable next level is optional, one whose next level
// comes with grow mode is optional and grow-only, any other level cell is
// included at its level.
func entitlementOf(f offer.Feature, c offer.Cell) (EntitlementInput, error) {
	in := EntitlementInput{State: c.State, Note: c.Note, GrowOnly: c.GrowOnly}
	if c.State == "teaser" {
		in.State = EntitlementNotOffered
	}
	switch f.Kind {
	case FeatureKindQuantity:
		if in.State == EntitlementIncluded {
			in.IncludedQuantity = offerDec(c.Quantity)
			in.Overage = c.Overage
		}
	case FeatureKindLevel:
		if c.Level != nil {
			l := *c.Level
			in.Level = &l
			switch {
			case c.GrowOnly:
				in.State = EntitlementOptional
			case c.NextLevelAddon != nil:
				in.State = EntitlementOptional
			default:
				in.State = EntitlementIncluded
			}
		}
	}
	if !ValidEntitlementState(in.State) {
		return in, fmt.Errorf("%w: state %q", ErrInvalid, c.State)
	}
	return in, nil
}

// offerDec is a number of the offer as a Decimal, nil when absent.
func offerDec[T ~string](n T) *Decimal {
	v := strings.TrimSpace(string(n))
	if v == "" {
		return nil
	}
	d := Decimal(v)
	return &d
}

// sameDecimal compares two decimals as numbers.
func sameDecimal(a, b Decimal) bool {
	return ratOf(a).Cmp(ratOf(b)) == 0
}
