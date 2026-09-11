package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The public calculator (DESIGN.md §11, EPIC #6867, founder requirement
// 2026-09-11: "we'll provide a cost calculator publicly").
//
// One cloud rate card on a Sovereign may be PUBLIC: its list prices are what
// the unauthenticated calculator shows and prices from. The flag lives on
// the book (price_books.public) and the designation on the single settings
// row (billing_settings.public_price_book_id); SetPriceBookPublic keeps the
// two in step, and a partial unique index refuses a second public book
// however it is attempted. A negotiated book, a discount, a partner tier or
// a customer's own rate never reaches the public surface — the calculator
// reads exactly one book, plus the two platform books the Organization sync
// owns (the plans and the pay-per-use card), and nothing else.
//
// An estimate is a saved, shareable pricing of a month of usage — the
// AWS / Azure calculator shape. It carries no customer and belongs to nobody;
// when the prospect leaves an address it is a LEAD the proposals module reads
// later. client_hash is a digest of the caller's address, never the address.

// estimatesMigrationSQL is one transaction, idempotent against a database
// that already carries the shape. Appended at the END of migrations, and
// located by content (MigrationEstimates): migrations are positional.
const estimatesMigrationSQL = `
ALTER TABLE price_books ADD COLUMN IF NOT EXISTS public BOOLEAN NOT NULL DEFAULT false;
CREATE UNIQUE INDEX IF NOT EXISTS price_books_one_public_idx ON price_books (public) WHERE public;
ALTER TABLE price_books ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
UPDATE price_books SET updated_at = created_at WHERE updated_at < created_at;

-- "Prices as of": the book's updated_at moves whenever the header or any of
-- its items changes, so the public catalog can date the list it shows.
CREATE OR REPLACE FUNCTION price_books_touch() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_TABLE_NAME = 'price_items' THEN
    UPDATE price_books SET updated_at = now() WHERE id = COALESCE(NEW.price_book_id, OLD.price_book_id);
    RETURN NULL;
  END IF;
  NEW.updated_at := now();
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS price_books_touch ON price_books;
CREATE TRIGGER price_books_touch BEFORE UPDATE ON price_books FOR EACH ROW EXECUTE FUNCTION price_books_touch();
DROP TRIGGER IF EXISTS price_items_touch ON price_items;
CREATE TRIGGER price_items_touch AFTER INSERT OR UPDATE OR DELETE ON price_items FOR EACH ROW EXECUTE FUNCTION price_books_touch();

ALTER TABLE billing_settings ADD COLUMN IF NOT EXISTS public_price_book_id UUID REFERENCES price_books(id) ON DELETE SET NULL;

CREATE TABLE IF NOT EXISTS estimates (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	price_book_id UUID REFERENCES price_books(id) ON DELETE SET NULL,
	price_book_name TEXT NOT NULL DEFAULT '',
	price_book_updated_at TIMESTAMPTZ,
	currency TEXT NOT NULL,
	region TEXT NOT NULL DEFAULT '',
	payload JSONB NOT NULL DEFAULT '{}'::jsonb,
	subtotal NUMERIC(20,6) NOT NULL,
	tax_rate NUMERIC(6,4) NOT NULL,
	tax NUMERIC(20,6) NOT NULL,
	total NUMERIC(20,6) NOT NULL,
	monthly NUMERIC(20,6) NOT NULL,
	yearly NUMERIC(20,6) NOT NULL,
	contact_email TEXT,
	lead BOOLEAN NOT NULL DEFAULT false,
	client_hash TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	valid_until TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS estimates_leads_idx ON estimates (created_at DESC) WHERE lead;
`

// MigrationEstimates is the schema_migrations version of the public
// calculator migration, located by content like the others so a migration
// appended after it cannot move this version.
var MigrationEstimates = func() int {
	for i, m := range migrations {
		if m == estimatesMigrationSQL {
			return i + 1
		}
	}
	return len(migrations)
}()

// EstimateValidity is how long a saved estimate quotes its prices for.
const EstimateValidity = 30 * 24 * time.Hour

// PlanShape is the vCPU and memory a sized catalog plan bundles (the
// org-controller's planQuotaTable: 2 GiB per vCPU). ok is false for flexi
// and unknown slugs, which have no fixed shape.
func PlanShape(slug string) (vcpu, memGiB int, ok bool) {
	v, found := paygShapeVCPU[NormalizePlanSlug(slug)]
	if !found {
		return 0, 0, false
	}
	return int(v), int(v) * paygUnitGiB, true
}

// SetPriceBookPublic flags a cloud book as the Sovereign's public list and
// designates it in billing_settings, or withdraws it. Only one book may be
// public: flagging a second is ErrConflict naming the current one. A
// platform book cannot be public — the plans and pay-per-use cards are
// published alongside the public cloud book automatically — that is
// ErrInvalid.
func (s *Store) SetPriceBookPublic(ctx context.Context, id string, public bool) (PriceBook, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PriceBook{}, err
	}
	defer tx.Rollback()
	var scope string
	if err := tx.QueryRowContext(ctx, `SELECT scope FROM price_books WHERE id = $1 FOR UPDATE`, id).Scan(&scope); err != nil {
		return PriceBook{}, mapErr(err)
	}
	if public {
		if scope != LayerCloud {
			return PriceBook{}, fmt.Errorf("%w: only a cloud price book can be public; the platform books are published with it", ErrInvalid)
		}
		var otherID, otherName string
		err := tx.QueryRowContext(ctx, `SELECT id, name FROM price_books WHERE public AND id <> $1`, id).Scan(&otherID, &otherName)
		if err == nil {
			return PriceBook{}, fmt.Errorf("%w: %q is already the public price book; withdraw it first", ErrConflict, otherName)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return PriceBook{}, mapErr(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE price_books SET public = true WHERE id = $1`, id); err != nil {
			return PriceBook{}, mapErr(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE billing_settings SET public_price_book_id = $1, updated_at = now() WHERE id = 1`, id); err != nil {
			return PriceBook{}, mapErr(err)
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE price_books SET public = false WHERE id = $1`, id); err != nil {
			return PriceBook{}, mapErr(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE billing_settings SET public_price_book_id = NULL, updated_at = now() WHERE id = 1 AND public_price_book_id = $1`, id); err != nil {
			return PriceBook{}, mapErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return PriceBook{}, err
	}
	return s.GetPriceBook(ctx, id)
}

// ErrNoPublicPriceBook is reported when no book is designated public: the
// calculator has nothing to show. The API answers 404 with the message.
var ErrNoPublicPriceBook = fmt.Errorf("%w: no public price book is designated", ErrNotFound)

// PublicPriceBook is the ONE book the public calculator prices from, with
// its items: billing_settings.public_price_book_id when set, else the single
// cloud book flagged public. Nothing else is ever returned here — a
// negotiated book has neither the flag nor the designation.
func (s *Store) PublicPriceBook(ctx context.Context) (PriceBook, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT b.id FROM price_books b
		WHERE b.id = (SELECT public_price_book_id FROM billing_settings WHERE id = 1)
		UNION ALL
		SELECT b.id FROM price_books b WHERE b.public AND b.scope = $1
		LIMIT 1`, LayerCloud).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return PriceBook{}, ErrNoPublicPriceBook
	}
	if err != nil {
		return PriceBook{}, mapErr(err)
	}
	return s.GetPriceBook(ctx, id)
}

// EstimateRegions lists the regions the calculator offers. The capacity
// module's `capacity_regions` table is read when it exists (checked at run
// time — this module does not depend on it); otherwise the regions the
// Sovereign's sources and usage carry. Sorted, without the empty region.
func (s *Store) EstimateRegions(ctx context.Context) ([]string, error) {
	var hasCapacity bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'capacity_regions' AND column_name = 'region')`).Scan(&hasCapacity); err != nil {
		return nil, mapErr(err)
	}
	q := `SELECT DISTINCT region FROM cost_sources WHERE region <> '' UNION SELECT DISTINCT region FROM usage_records WHERE region <> '' ORDER BY 1`
	if hasCapacity {
		q = `SELECT DISTINCT region FROM capacity_regions WHERE region <> '' ORDER BY 1`
	}
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EstimateLine is one priced line of a saved estimate, on the wire.
// Quantity × Hours × Months is RatedQuantity, the figure Rate priced; Plan
// is set for a plan line (its slug) and empty for an SKU line.
type EstimateLine struct {
	SKU           string  `json:"sku"`
	Plan          string  `json:"plan,omitempty"`
	Description   string  `json:"description,omitempty"`
	Unit          string  `json:"unit"`
	Quantity      Decimal `json:"quantity"`
	Hours         Decimal `json:"hours"`
	Months        int     `json:"months"`
	RatedQuantity Decimal `json:"rated_quantity"`
	UnitPrice     Decimal `json:"unit_price"`
	Amount        Decimal `json:"amount"`
}

// EstimateBook names the list an estimate was priced from and its date.
type EstimateBook struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Estimate is a saved estimate. ContactEmail is carried only on the
// operator's Leads list; the public document never includes it (the
// handler clears it), Lead alone says an address was left.
type Estimate struct {
	ID           string         `json:"id"`
	Lines        []EstimateLine `json:"lines"`
	Currency     string         `json:"currency"`
	Region       string         `json:"region,omitempty"`
	Subtotal     Decimal        `json:"subtotal"`
	TaxRate      Decimal        `json:"tax_rate"`
	Tax          Decimal        `json:"tax"`
	Total        Decimal        `json:"total"`
	Monthly      Decimal        `json:"monthly"`
	Yearly       Decimal        `json:"yearly"`
	PriceBook    EstimateBook   `json:"price_book"`
	ListPrices   bool           `json:"list_prices"`
	Lead         bool           `json:"lead"`
	ContactEmail string         `json:"contact_email,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	ValidUntil   time.Time      `json:"valid_until"`
}

// estimatePayload is the JSONB half of an estimate: what was asked and how
// each line priced. The totals are columns so the Leads list sorts on them.
type estimatePayload struct {
	Lines  []EstimateLine `json:"lines"`
	Region string         `json:"region,omitempty"`
}

// EstimateDraft is what the API hands the store after pricing.
type EstimateDraft struct {
	Lines        []EstimateLine
	Currency     string
	Region       string
	Subtotal     Decimal
	TaxRate      Decimal
	Tax          Decimal
	Total        Decimal
	Monthly      Decimal
	Yearly       Decimal
	PriceBook    EstimateBook
	ContactEmail string
	ClientHash   string
	Now          time.Time
}

const estimateColumns = `id, price_book_id, price_book_name, price_book_updated_at, currency, region, payload, subtotal::text, tax_rate::text, tax::text, total::text, monthly::text, yearly::text, contact_email, lead, created_at, valid_until`

// CreateEstimate saves a priced estimate. It is a lead when an address was
// left; valid for EstimateValidity from Now.
func (s *Store) CreateEstimate(ctx context.Context, d EstimateDraft) (Estimate, error) {
	if d.Now.IsZero() {
		d.Now = time.Now()
	}
	now := d.Now.UTC()
	payload, err := json.Marshal(estimatePayload{Lines: d.Lines, Region: strings.TrimSpace(d.Region)})
	if err != nil {
		return Estimate{}, err
	}
	email := strings.ToLower(strings.TrimSpace(d.ContactEmail))
	var bookID sql.NullString
	if d.PriceBook.ID != "" {
		bookID = sql.NullString{String: d.PriceBook.ID, Valid: true}
	}
	var updated sql.NullTime
	if !d.PriceBook.UpdatedAt.IsZero() {
		updated = sql.NullTime{Time: d.PriceBook.UpdatedAt.UTC(), Valid: true}
	}
	row := s.db.QueryRowContext(ctx, `INSERT INTO estimates (price_book_id, price_book_name, price_book_updated_at, currency, region, payload, subtotal, tax_rate, tax, total, monthly, yearly, contact_email, lead, client_hash, created_at, valid_until)
		VALUES ($1, $2, $3, $4, $5, $6, $7::numeric, $8::numeric, $9::numeric, $10::numeric, $11::numeric, $12::numeric, $13, $14, $15, $16, $17) RETURNING `+estimateColumns,
		bookID, d.PriceBook.Name, updated, strings.ToUpper(d.Currency), strings.TrimSpace(d.Region), payload,
		string(d.Subtotal), string(d.TaxRate), string(d.Tax), string(d.Total), string(d.Monthly), string(d.Yearly),
		nullStr(&email), email != "", d.ClientHash, now, now.Add(EstimateValidity))
	return scanEstimate(row)
}

// GetEstimate returns one saved estimate (the shareable link).
func (s *Store) GetEstimate(ctx context.Context, id string) (Estimate, error) {
	return scanEstimate(s.db.QueryRowContext(ctx, `SELECT `+estimateColumns+` FROM estimates WHERE id = $1`, id))
}

// ListLeads returns the estimates a prospect left an address on, newest
// first. Operator-only at the API: it carries the addresses.
func (s *Store) ListLeads(ctx context.Context, limit int) ([]Estimate, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+estimateColumns+` FROM estimates WHERE lead ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []Estimate{}
	for rows.Next() {
		e, err := scanEstimate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func scanEstimate(row interface{ Scan(...any) error }) (Estimate, error) {
	var e Estimate
	var bookID, email sql.NullString
	var bookUpdated sql.NullTime
	var payload []byte
	var subtotal, rate, tax, total, monthly, yearly string
	if err := row.Scan(&e.ID, &bookID, &e.PriceBook.Name, &bookUpdated, &e.Currency, &e.Region, &payload, &subtotal, &rate, &tax, &total, &monthly, &yearly, &email, &e.Lead, &e.CreatedAt, &e.ValidUntil); err != nil {
		return e, mapErr(err)
	}
	var p estimatePayload
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &p); err != nil {
			return e, fmt.Errorf("estimate %s payload: %w", e.ID, err)
		}
	}
	e.Lines = p.Lines
	if e.Lines == nil {
		e.Lines = []EstimateLine{}
	}
	if bookID.Valid {
		e.PriceBook.ID = bookID.String
	}
	if bookUpdated.Valid {
		e.PriceBook.UpdatedAt = bookUpdated.Time.UTC()
	}
	e.Subtotal, e.TaxRate, e.Tax, e.Total, e.Monthly, e.Yearly = Decimal(subtotal), Decimal(rate), Decimal(tax), Decimal(total), Decimal(monthly), Decimal(yearly)
	if email.Valid {
		e.ContactEmail = email.String
	}
	e.ListPrices = true
	e.CreatedAt, e.ValidUntil = e.CreatedAt.UTC(), e.ValidUntil.UTC()
	return e, nil
}
