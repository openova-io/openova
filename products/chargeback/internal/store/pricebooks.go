package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func scanPriceBook(row interface{ Scan(...any) error }) (PriceBook, error) {
	var pb PriceBook
	var eff sql.NullTime
	if err := row.Scan(&pb.ID, &pb.Name, &pb.Currency, &pb.AnnualDivisor, &pb.BillStopped, &eff, &pb.CreatedAt); err != nil {
		return pb, mapErr(err)
	}
	pb.EffectiveFrom = datePtr(eff)
	return pb, nil
}

// ListPriceBooks returns every rate card (no items).
func (s *Store) ListPriceBooks(ctx context.Context) ([]PriceBook, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, currency, annual_divisor, bill_stopped, effective_from, created_at FROM price_books ORDER BY name`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []PriceBook{}
	for rows.Next() {
		pb, err := scanPriceBook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, pb)
	}
	return out, rows.Err()
}

// GetPriceBook returns a rate card with its items.
func (s *Store) GetPriceBook(ctx context.Context, id string) (PriceBook, error) {
	pb, err := scanPriceBook(s.db.QueryRowContext(ctx, `SELECT id, name, currency, annual_divisor, bill_stopped, effective_from, created_at FROM price_books WHERE id = $1`, id))
	if err != nil {
		return pb, err
	}
	pb.Items, err = s.ListPriceItems(ctx, id)
	return pb, err
}

// GetPriceBookByName resolves a rate card by name (imports).
func (s *Store) GetPriceBookByName(ctx context.Context, name string) (PriceBook, error) {
	return scanPriceBook(s.db.QueryRowContext(ctx, `SELECT id, name, currency, annual_divisor, bill_stopped, effective_from, created_at FROM price_books WHERE lower(name) = lower($1)`, strings.TrimSpace(name)))
}

// PriceBookInput is the creatable/updatable subset.
type PriceBookInput struct {
	Name          string
	Currency      string
	AnnualDivisor int
	BillStopped   string
	EffectiveFrom string
}

// CreatePriceBook inserts a rate card.
func (s *Store) CreatePriceBook(ctx context.Context, in PriceBookInput) (PriceBook, error) {
	if in.Currency == "" {
		in.Currency = "OMR"
	}
	if in.AnnualDivisor <= 0 {
		in.AnnualDivisor = 8760
	}
	if in.BillStopped == "" {
		in.BillStopped = "compute"
	}
	var id string
	err := s.db.QueryRowContext(ctx, `INSERT INTO price_books (name, currency, annual_divisor, bill_stopped, effective_from) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		strings.TrimSpace(in.Name), strings.ToUpper(in.Currency), in.AnnualDivisor, in.BillStopped, nullStr(&in.EffectiveFrom)).Scan(&id)
	if err != nil {
		return PriceBook{}, mapErr(err)
	}
	return s.GetPriceBook(ctx, id)
}

// UpdatePriceBook replaces the header fields; when the divisor changes, unit
// prices derived from an annual price are recomputed.
func (s *Store) UpdatePriceBook(ctx context.Context, id string, in PriceBookInput) (PriceBook, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PriceBook{}, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE price_books SET name = COALESCE(NULLIF($2, ''), name), currency = COALESCE(NULLIF($3, ''), currency),
		annual_divisor = CASE WHEN $4 > 0 THEN $4 ELSE annual_divisor END, bill_stopped = COALESCE(NULLIF($5, ''), bill_stopped),
		effective_from = COALESCE($6, effective_from) WHERE id = $1`,
		id, strings.TrimSpace(in.Name), strings.ToUpper(in.Currency), in.AnnualDivisor, in.BillStopped, nullStr(&in.EffectiveFrom))
	if err != nil {
		return PriceBook{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return PriceBook{}, ErrNotFound
	}
	if in.AnnualDivisor > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE price_items SET unit_price = round(annual_price / $2, 8) WHERE price_book_id = $1 AND annual_price IS NOT NULL`, id, in.AnnualDivisor); err != nil {
			return PriceBook{}, mapErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return PriceBook{}, err
	}
	return s.GetPriceBook(ctx, id)
}

// ListPriceItems returns a rate card's SKUs.
func (s *Store) ListPriceItems(ctx context.Context, priceBookID string) ([]PriceItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT price_book_id, sku, unit, unit_price::text, annual_price::text, description FROM price_items WHERE price_book_id = $1 ORDER BY sku`, priceBookID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []PriceItem{}
	for rows.Next() {
		var it PriceItem
		var up string
		var ap sql.NullString
		if err := rows.Scan(&it.PriceBookID, &it.SKU, &it.Unit, &up, &ap, &it.Description); err != nil {
			return nil, err
		}
		it.UnitPrice = Decimal(up)
		if ap.Valid {
			d := Decimal(ap.String)
			it.AnnualPrice = &d
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// PutPriceItems upserts items (bulk). When replace is true, SKUs not in the
// list are removed.
func (s *Store) PutPriceItems(ctx context.Context, priceBookID string, items []PriceItem, replace bool) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM price_books WHERE id = $1)`, priceBookID).Scan(&exists); err != nil {
		return 0, mapErr(err)
	}
	if !exists {
		return 0, ErrNotFound
	}
	if replace {
		if _, err := tx.ExecContext(ctx, `DELETE FROM price_items WHERE price_book_id = $1`, priceBookID); err != nil {
			return 0, mapErr(err)
		}
	}
	n := 0
	for _, it := range items {
		var annual sql.NullString
		if it.AnnualPrice != nil && *it.AnnualPrice != "" {
			annual = sql.NullString{String: string(*it.AnnualPrice), Valid: true}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO price_items (price_book_id, sku, unit, unit_price, annual_price, description) VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (price_book_id, sku) DO UPDATE SET unit = EXCLUDED.unit, unit_price = EXCLUDED.unit_price, annual_price = EXCLUDED.annual_price, description = EXCLUDED.description`,
			priceBookID, strings.TrimSpace(it.SKU), strings.TrimSpace(it.Unit), string(it.UnitPrice), annual, it.Description); err != nil {
			return n, mapErr(err)
		}
		n++
	}
	return n, tx.Commit()
}

// DeletePriceBook removes a rate card and its items (cascade). It is refused
// while any customer is assigned to it — the names of those customers come
// back so the operator knows what to re-point first — because a customer
// without a book silently stops rating (rating.Run reports "no price book
// assigned" and writes nothing).
func (s *Store) DeletePriceBook(ctx context.Context, id string) (assigned []string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM price_books WHERE id = $1 FOR UPDATE)`, id).Scan(&exists); err != nil {
		return nil, mapErr(err)
	}
	if !exists {
		return nil, ErrNotFound
	}
	rows, err := tx.QueryContext(ctx, `SELECT name FROM customers WHERE price_book_id = $1 ORDER BY name`, id)
	if err != nil {
		return nil, mapErr(err)
	}
	assigned = []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		assigned = append(assigned, n)
	}
	rows.Close()
	if len(assigned) > 0 {
		return assigned, fmt.Errorf("%w: price book is assigned to %d customer(s); assign them another book first", ErrConflict, len(assigned))
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM price_books WHERE id = $1`, id); err != nil {
		return nil, mapErr(err)
	}
	return nil, tx.Commit()
}

// ClonePriceBook copies a rate card under a new name: the header and every
// item, annual_price preserved. This is how per-account pricing is made — the
// list book stays the list, the clone is negotiated.
func (s *Store) ClonePriceBook(ctx context.Context, id, name string) (PriceBook, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PriceBook{}, err
	}
	defer tx.Rollback()
	var newID string
	err = tx.QueryRowContext(ctx, `INSERT INTO price_books (name, currency, annual_divisor, bill_stopped, effective_from)
		SELECT $2, currency, annual_divisor, bill_stopped, effective_from FROM price_books WHERE id = $1 RETURNING id`,
		id, strings.TrimSpace(name)).Scan(&newID)
	if err != nil {
		return PriceBook{}, mapErr(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO price_items (price_book_id, sku, unit, unit_price, annual_price, description)
		SELECT $2, sku, unit, unit_price, annual_price, description FROM price_items WHERE price_book_id = $1`, id, newID); err != nil {
		return PriceBook{}, mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return PriceBook{}, err
	}
	return s.GetPriceBook(ctx, newID)
}

const priceItemColumns = `price_book_id, sku, unit, unit_price::text, annual_price::text, description`

func scanPriceItem(row interface{ Scan(...any) error }) (PriceItem, error) {
	var it PriceItem
	var up string
	var ap sql.NullString
	if err := row.Scan(&it.PriceBookID, &it.SKU, &it.Unit, &up, &ap, &it.Description); err != nil {
		return it, mapErr(err)
	}
	it.UnitPrice = Decimal(up)
	if ap.Valid {
		d := Decimal(ap.String)
		it.AnnualPrice = &d
	}
	return it, nil
}

// GetPriceItem returns one SKU of a rate card.
func (s *Store) GetPriceItem(ctx context.Context, priceBookID, sku string) (PriceItem, error) {
	return scanPriceItem(s.db.QueryRowContext(ctx, `SELECT `+priceItemColumns+` FROM price_items WHERE price_book_id = $1 AND sku = $2`, priceBookID, strings.TrimSpace(sku)))
}

// AddPriceItem inserts one SKU. An existing SKU is ErrConflict (the caller
// meant PATCH); a missing book is ErrNotFound.
func (s *Store) AddPriceItem(ctx context.Context, priceBookID string, it PriceItem) (PriceItem, error) {
	var annual sql.NullString
	if it.AnnualPrice != nil && *it.AnnualPrice != "" {
		annual = sql.NullString{String: string(*it.AnnualPrice), Valid: true}
	}
	return scanPriceItem(s.db.QueryRowContext(ctx, `INSERT INTO price_items (price_book_id, sku, unit, unit_price, annual_price, description)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+priceItemColumns,
		priceBookID, strings.TrimSpace(it.SKU), strings.TrimSpace(it.Unit), string(it.UnitPrice), annual, it.Description))
}

// PriceItemPatch carries optional item updates; nil means unchanged. When
// UnitPrice is set, AnnualPrice is written alongside it (nil clears it): a
// unit price typed directly must not keep a stale annual figure that a later
// divisor change would silently recompute over it.
type PriceItemPatch struct {
	Unit        *string
	Description *string
	UnitPrice   *Decimal
	AnnualPrice *Decimal
}

// UpdatePriceItem applies a patch to one SKU; ErrNotFound when the SKU is not
// in the book.
func (s *Store) UpdatePriceItem(ctx context.Context, priceBookID, sku string, p PriceItemPatch) (PriceItem, error) {
	sets := []string{}
	var args []any
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if p.Unit != nil {
		add("unit", strings.TrimSpace(*p.Unit))
	}
	if p.Description != nil {
		add("description", *p.Description)
	}
	if p.UnitPrice != nil {
		add("unit_price", string(*p.UnitPrice))
		var annual sql.NullString
		if p.AnnualPrice != nil && *p.AnnualPrice != "" {
			annual = sql.NullString{String: string(*p.AnnualPrice), Valid: true}
		}
		add("annual_price", annual)
	}
	if len(sets) == 0 {
		return s.GetPriceItem(ctx, priceBookID, sku)
	}
	args = append(args, priceBookID, strings.TrimSpace(sku))
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`UPDATE price_items SET %s WHERE price_book_id = $%d AND sku = $%d`, strings.Join(sets, ", "), len(args)-1, len(args)), args...)
	if err != nil {
		return PriceItem{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return PriceItem{}, ErrNotFound
	}
	return s.GetPriceItem(ctx, priceBookID, sku)
}

// DeletePriceItem removes one SKU from a rate card.
func (s *Store) DeletePriceItem(ctx context.Context, priceBookID, sku string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM price_items WHERE price_book_id = $1 AND sku = $2`, priceBookID, strings.TrimSpace(sku))
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CoverageCustomer is one customer assigned to a rate card.
type CoverageCustomer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// CoverageSKU is one SKU the assigned customers actually consumed, with
// whether the book prices it.
type CoverageSKU struct {
	SKU         string   `json:"sku"`
	Unit        string   `json:"unit"`
	Quantity30d Decimal  `json:"quantity_30d"`
	Resources   int      `json:"resources"`
	Priced      bool     `json:"priced"`
	UnitPrice   *Decimal `json:"unit_price"`
}

// PriceBookCoverage answers "does this book price what its customers use?"
// (DESIGN.md §2.5). CoveragePct is priced SKUs over SKUs in use; 100 when
// nothing is in use, because an unused book is not an incomplete one.
type PriceBookCoverage struct {
	Customers     []CoverageCustomer `json:"customers"`
	SKUsInUse     []CoverageSKU      `json:"skus_in_use"`
	CoveragePct   float64            `json:"coverage_pct"`
	UnpricedCount int                `json:"unpriced_count"`
}

// PriceBookCoverage computes the coverage of a rate card over the usage its
// assigned customers recorded in [from, to). It runs over costBaseSQL, the
// same priced ledger the explorer and rating use, so "unpriced here" and
// "unpriced on the statement" can never disagree (ecs.cpu_util excluded
// alike).
func (s *Store) PriceBookCoverage(ctx context.Context, priceBookID string, from, to time.Time) (PriceBookCoverage, error) {
	out := PriceBookCoverage{Customers: []CoverageCustomer{}, SKUsInUse: []CoverageSKU{}}
	if _, err := s.GetPriceBook(ctx, priceBookID); err != nil {
		return out, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, slug FROM customers WHERE price_book_id = $1 ORDER BY name`, priceBookID)
	if err != nil {
		return out, mapErr(err)
	}
	for rows.Next() {
		var c CoverageCustomer
		if err := rows.Scan(&c.ID, &c.Name, &c.Slug); err != nil {
			rows.Close()
			return out, err
		}
		out.Customers = append(out.Customers, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	rows, err = s.db.QueryContext(ctx, `WITH f AS (`+costBaseSQL+` AND c.price_book_id = $3)
		SELECT sku, COALESCE((SELECT p.unit FROM price_items p WHERE p.price_book_id = $3 AND p.sku = f.sku), min(unit)),
		       sum(quantity)::text, count(DISTINCT resource_id), unit_price::text
		  FROM f GROUP BY sku, unit_price ORDER BY sku`, from, to, priceBookID)
	if err != nil {
		return out, mapErr(err)
	}
	defer rows.Close()
	priced := 0
	for rows.Next() {
		var k CoverageSKU
		var qty string
		var up sql.NullString
		if err := rows.Scan(&k.SKU, &k.Unit, &qty, &k.Resources, &up); err != nil {
			return out, err
		}
		k.Quantity30d = Decimal(qty)
		if up.Valid {
			d := Decimal(up.String)
			k.UnitPrice = &d
			k.Priced = true
			priced++
		}
		out.SKUsInUse = append(out.SKUsInUse, k)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.UnpricedCount = len(out.SKUsInUse) - priced
	if len(out.SKUsInUse) == 0 {
		out.CoveragePct = 100
	} else {
		out.CoveragePct = float64(priced) * 100 / float64(len(out.SKUsInUse))
	}
	return out, nil
}
