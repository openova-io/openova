package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const priceBookColumns = `id, name, scope, currency, annual_divisor, bill_stopped, effective_from, description, created_at, public, updated_at, partner_id, derived_from_rule, derived_from_book_id`

func scanPriceBook(row interface{ Scan(...any) error }) (PriceBook, error) {
	var pb PriceBook
	var eff sql.NullTime
	var partner, derivedFrom sql.NullString
	if err := row.Scan(&pb.ID, &pb.Name, &pb.Scope, &pb.Currency, &pb.AnnualDivisor, &pb.BillStopped, &eff, &pb.Description, &pb.CreatedAt, &pb.Public, &pb.UpdatedAt, &partner, &pb.DerivedFromRule, &derivedFrom); err != nil {
		return pb, mapErr(err)
	}
	pb.EffectiveFrom = datePtr(eff)
	pb.UpdatedAt = pb.UpdatedAt.UTC()
	pb.PartnerID = strPtr(partner)
	pb.DerivedFromBookID = strPtr(derivedFrom)
	return pb, nil
}

// ListPriceBooks returns every rate card (no items).
func (s *Store) ListPriceBooks(ctx context.Context) ([]PriceBook, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+priceBookColumns+` FROM price_books ORDER BY name`)
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
	pb, err := scanPriceBook(s.db.QueryRowContext(ctx, `SELECT `+priceBookColumns+` FROM price_books WHERE id = $1`, id))
	if err != nil {
		return pb, err
	}
	pb.Items, err = s.ListPriceItems(ctx, id)
	return pb, err
}

// GetPriceBookByName resolves a rate card by name (imports).
func (s *Store) GetPriceBookByName(ctx context.Context, name string) (PriceBook, error) {
	return scanPriceBook(s.db.QueryRowContext(ctx, `SELECT `+priceBookColumns+` FROM price_books WHERE lower(name) = lower($1)`, strings.TrimSpace(name)))
}

// PriceBookInput is the creatable/updatable subset. Scope is cloud or
// platform ("" = cloud on create, unchanged on update); Description is the
// operator-editable note on the book ("" = unchanged on update).
type PriceBookInput struct {
	Name          string
	Scope         string
	Currency      string
	AnnualDivisor int
	BillStopped   string
	EffectiveFrom string
	Description   string
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
	in.Scope = strings.ToLower(strings.TrimSpace(in.Scope))
	if in.Scope == "" {
		in.Scope = LayerCloud
	}
	if !ValidLayer(in.Scope) {
		return PriceBook{}, fmt.Errorf("%w: scope must be cloud or platform", ErrInvalid)
	}
	var id string
	err := s.db.QueryRowContext(ctx, `INSERT INTO price_books (name, scope, currency, annual_divisor, bill_stopped, effective_from, description) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		strings.TrimSpace(in.Name), in.Scope, strings.ToUpper(in.Currency), in.AnnualDivisor, in.BillStopped, nullStr(&in.EffectiveFrom), in.Description).Scan(&id)
	if err != nil {
		return PriceBook{}, mapErr(err)
	}
	return s.GetPriceBook(ctx, id)
}

// UpdatePriceBook replaces the header fields; when the divisor changes, unit
// prices derived from an annual price are recomputed. The scope may change
// only while no source is assigned: a source's book must always match its
// layer, and flipping the book under assigned sources would break that.
func (s *Store) UpdatePriceBook(ctx context.Context, id string, in PriceBookInput) (PriceBook, error) {
	in.Scope = strings.ToLower(strings.TrimSpace(in.Scope))
	if in.Scope != "" && !ValidLayer(in.Scope) {
		return PriceBook{}, fmt.Errorf("%w: scope must be cloud or platform", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PriceBook{}, err
	}
	defer tx.Rollback()
	if in.Scope != "" {
		var current string
		var assigned int
		if err := tx.QueryRowContext(ctx, `SELECT scope, (SELECT count(*) FROM cost_sources s WHERE s.price_book_id = b.id) FROM price_books b WHERE id = $1 FOR UPDATE`, id).Scan(&current, &assigned); err != nil {
			return PriceBook{}, mapErr(err)
		}
		if current != in.Scope && assigned > 0 {
			return PriceBook{}, fmt.Errorf("%w: scope cannot change while %d source(s) are assigned to this book; assign them another book first", ErrConflict, assigned)
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE price_books SET name = COALESCE(NULLIF($2, ''), name), currency = COALESCE(NULLIF($3, ''), currency),
		annual_divisor = CASE WHEN $4 > 0 THEN $4 ELSE annual_divisor END, bill_stopped = COALESCE(NULLIF($5, ''), bill_stopped),
		effective_from = COALESCE($6, effective_from), scope = COALESCE(NULLIF($7, ''), scope),
		description = COALESCE(NULLIF($8, ''), description) WHERE id = $1`,
		id, strings.TrimSpace(in.Name), strings.ToUpper(in.Currency), in.AnnualDivisor, in.BillStopped, nullStr(&in.EffectiveFrom), in.Scope, in.Description)
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

// CoverageSource is one source assigned to a rate card: whose it is, what it
// meters and which layer it belongs to.
type CoverageSource struct {
	SourceID     string `json:"source_id"`
	CustomerID   string `json:"customer_id"`
	CustomerName string `json:"customer_name"`
	CustomerSlug string `json:"customer_slug"`
	Label        string `json:"label"`
	Kind         string `json:"kind"`
	Layer        string `json:"layer"`
}

// AssignedSources lists the sources assigned to a rate card, by customer
// name then label. The internal source is never assigned a book.
func (s *Store) AssignedSources(ctx context.Context, priceBookID string) ([]CoverageSource, error) {
	return s.assignedSources(ctx, s.db, priceBookID)
}

func (s *Store) assignedSources(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, priceBookID string) ([]CoverageSource, error) {
	rows, err := q.QueryContext(ctx, `SELECT s.id, c.id, c.name, c.slug, COALESCE(NULLIF(s.project_id, ''), s.kind), s.kind, s.layer
		FROM cost_sources s JOIN customers c ON c.id = s.customer_id
		WHERE s.price_book_id = $1 ORDER BY c.name, s.layer, s.project_id`, priceBookID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []CoverageSource{}
	for rows.Next() {
		var cs CoverageSource
		if err := rows.Scan(&cs.SourceID, &cs.CustomerID, &cs.CustomerName, &cs.CustomerSlug, &cs.Label, &cs.Kind, &cs.Layer); err != nil {
			return nil, err
		}
		out = append(out, cs)
	}
	return out, rows.Err()
}

// coverageCustomers reduces the assigned sources to their distinct customers.
func coverageCustomers(srcs []CoverageSource) []CoverageCustomer {
	out := []CoverageCustomer{}
	seen := map[string]bool{}
	for _, cs := range srcs {
		if seen[cs.CustomerID] {
			continue
		}
		seen[cs.CustomerID] = true
		out = append(out, CoverageCustomer{ID: cs.CustomerID, Name: cs.CustomerName, Slug: cs.CustomerSlug})
	}
	return out
}

// DeletePriceBook removes a rate card and its items (cascade). It is refused
// while any source is assigned to it — those sources come back so the
// operator knows what to re-point first — because a source without a book
// silently stops rating (its SKUs are unpriced on every statement).
func (s *Store) DeletePriceBook(ctx context.Context, id string) (assigned []CoverageSource, err error) {
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
	assigned, err = s.assignedSources(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if len(assigned) > 0 {
		return assigned, fmt.Errorf("%w: price book is assigned to %d source(s) of %d customer(s); assign them another book first", ErrConflict, len(assigned), len(coverageCustomers(assigned)))
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM price_books WHERE id = $1`, id); err != nil {
		return nil, mapErr(err)
	}
	return nil, tx.Commit()
}

// ClonePriceBook copies a rate card under a new name: the header (scope and
// description included) and every item, annual_price preserved. This is how per-account
// pricing is made — the list book stays the list, the clone is negotiated.
func (s *Store) ClonePriceBook(ctx context.Context, id, name string) (PriceBook, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PriceBook{}, err
	}
	defer tx.Rollback()
	var newID string
	err = tx.QueryRowContext(ctx, `INSERT INTO price_books (name, scope, currency, annual_divisor, bill_stopped, effective_from, description)
		SELECT $2, scope, currency, annual_divisor, bill_stopped, effective_from, description FROM price_books WHERE id = $1 RETURNING id`,
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

// CoverageCustomer is one customer that owns a source assigned to a rate
// card (kept on the wire for the pages that list customers by book).
type CoverageCustomer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// CoverageSKU is one SKU the assigned sources actually consumed, with
// whether the book prices it. NotSoldPerUse marks a k8s.* meter under a
// platform book that prices none of the platform meters: the allocation
// basis, deliberately unpriced — not a gap in the book.
type CoverageSKU struct {
	SKU           string   `json:"sku"`
	Unit          string   `json:"unit"`
	Quantity30d   Decimal  `json:"quantity_30d"`
	Resources     int      `json:"resources"`
	Priced        bool     `json:"priced"`
	NotSoldPerUse bool     `json:"not_sold_per_use"`
	UnitPrice     *Decimal `json:"unit_price"`
}

// PriceBookCoverage answers "does this book price what the sources assigned
// to it use?" (DESIGN.md §2.5). CoveragePct is priced SKUs over the SKUs in
// use that the book is expected to price (not-sold-per-use meters are left
// out of both sides); 100 when nothing is in use, because an unused book is
// not an incomplete one.
type PriceBookCoverage struct {
	Scope         string             `json:"scope"`
	Sources       []CoverageSource   `json:"sources"`
	Customers     []CoverageCustomer `json:"customers"`
	SKUsInUse     []CoverageSKU      `json:"skus_in_use"`
	CoveragePct   float64            `json:"coverage_pct"`
	UnpricedCount int                `json:"unpriced_count"`
	NotSoldCount  int                `json:"not_sold_count"`
}

// PriceBookCoverage computes the coverage of a rate card over the usage the
// sources ASSIGNED TO IT recorded in [from, to). It runs over costBaseSQL,
// the same priced ledger the explorer and rating use, so "unpriced here" and
// "unpriced on the statement" can never disagree (ecs.cpu_util excluded
// alike), and a platform meter can never appear under a cloud book.
func (s *Store) PriceBookCoverage(ctx context.Context, priceBookID string, from, to time.Time) (PriceBookCoverage, error) {
	out := PriceBookCoverage{Sources: []CoverageSource{}, Customers: []CoverageCustomer{}, SKUsInUse: []CoverageSKU{}}
	pb, err := s.GetPriceBook(ctx, priceBookID)
	if err != nil {
		return out, err
	}
	out.Scope = pb.Scope
	if out.Sources, err = s.AssignedSources(ctx, priceBookID); err != nil {
		return out, err
	}
	out.Customers = coverageCustomers(out.Sources)
	rows, err := s.db.QueryContext(ctx, `WITH f AS (`+costBaseSQL+` AND NOT s.internal AND s.price_book_id = $3)
		SELECT sku, COALESCE((SELECT p.unit FROM price_items p WHERE p.price_book_id = $3 AND p.sku = f.sku), min(unit)),
		       sum(quantity)::text, count(DISTINCT resource_id), unit_price::text, bool_or(`+costNotSoldPerUseExpr+`)
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
		if err := rows.Scan(&k.SKU, &k.Unit, &qty, &k.Resources, &up, &k.NotSoldPerUse); err != nil {
			return out, err
		}
		k.Quantity30d = Decimal(qty)
		if up.Valid {
			d := Decimal(up.String)
			k.UnitPrice = &d
			k.Priced = true
			k.NotSoldPerUse = false
			priced++
		} else if k.NotSoldPerUse {
			out.NotSoldCount++
		}
		out.SKUsInUse = append(out.SKUsInUse, k)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.UnpricedCount = len(out.SKUsInUse) - priced - out.NotSoldCount
	priceable := len(out.SKUsInUse) - out.NotSoldCount
	if priceable == 0 {
		out.CoveragePct = 100
	} else {
		out.CoveragePct = float64(priced) * 100 / float64(priceable)
	}
	return out, nil
}
