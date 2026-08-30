package store

import (
	"context"
	"database/sql"
	"strings"
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
