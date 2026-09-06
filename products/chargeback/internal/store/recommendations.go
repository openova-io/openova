package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/lib/pq"
)

// Recommendation inputs (#6867, DESIGN.md §3.7).
//
// The rules live in internal/recommend and are pure; this file only gathers
// what they read, every query scoped like every other read. The shapes are
// deliberately plain so the rules' unit tests can build them as fixtures.

// LiveResource is one inventory row not marked deleted, with its owner.
type LiveResource struct {
	CustomerID   string
	CustomerName string
	SourceID     string
	ResourceID   string
	Kind         string
	Name         string
	Attrs        map[string]any
}

// LiveResources lists the live inventory in scope (optionally one customer),
// ordered by customer, kind, name.
func (s *Store) LiveResources(ctx context.Context, scope Scope, customerID string) ([]LiveResource, error) {
	cid, err := scopedCustomer(scope, customerID)
	if err != nil {
		return nil, err
	}
	q := `SELECT c.id::text, c.name, i.source_id::text, i.resource_id, i.kind, i.name, i.attrs
	        FROM resource_inventory i
	        JOIN cost_sources s ON s.id = i.source_id
	        JOIN customers c ON c.id = s.customer_id
	       WHERE i.deleted_at IS NULL`
	var args []any
	if cid != "" {
		q += ` AND c.id::text = $1`
		args = append(args, cid)
	}
	q += ` ORDER BY c.name, i.kind, i.name, i.resource_id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []LiveResource{}
	for rows.Next() {
		var r LiveResource
		var attrs []byte
		if err := rows.Scan(&r.CustomerID, &r.CustomerName, &r.SourceID, &r.ResourceID, &r.Kind, &r.Name, &attrs); err != nil {
			return nil, err
		}
		r.Attrs = map[string]any{}
		if len(attrs) > 0 {
			_ = json.Unmarshal(attrs, &r.Attrs)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CustomerBook is a customer with the rate card it bills against. HasBook
// false means no price book is assigned (Rates is then empty).
type CustomerBook struct {
	CustomerID   string
	CustomerName string
	Status       string
	HasBook      bool
	BookName     string
	Currency     string
	BillStopped  string
	Rates        map[string]Decimal // sku → hourly unit price
}

// CustomerBooks lists every customer in scope with its book and rates.
func (s *Store) CustomerBooks(ctx context.Context, scope Scope, customerID string) ([]CustomerBook, error) {
	cid, err := scopedCustomer(scope, customerID)
	if err != nil {
		return nil, err
	}
	q := `SELECT c.id::text, c.name, c.status, b.id::text, b.name, b.currency, b.bill_stopped
	        FROM customers c LEFT JOIN price_books b ON b.id = c.price_book_id`
	var args []any
	if cid != "" {
		q += ` WHERE c.id::text = $1`
		args = append(args, cid)
	}
	q += ` ORDER BY c.name`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []CustomerBook{}
	bookIdx := map[string][]int{} // book id → indexes in out
	for rows.Next() {
		var cb CustomerBook
		var bookID, bookName, currency, billStopped sql.NullString
		if err := rows.Scan(&cb.CustomerID, &cb.CustomerName, &cb.Status, &bookID, &bookName, &currency, &billStopped); err != nil {
			return nil, err
		}
		cb.Rates = map[string]Decimal{}
		if bookID.Valid {
			cb.HasBook = true
			cb.BookName, cb.Currency, cb.BillStopped = bookName.String, currency.String, billStopped.String
			bookIdx[bookID.String] = append(bookIdx[bookID.String], len(out))
		}
		out = append(out, cb)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(bookIdx) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(bookIdx))
	for id := range bookIdx {
		ids = append(ids, id)
	}
	items, err := s.db.QueryContext(ctx, `SELECT price_book_id::text, sku, unit_price::text FROM price_items WHERE price_book_id::text = ANY($1)`, pq.Array(ids))
	if err != nil {
		return nil, mapErr(err)
	}
	defer items.Close()
	for items.Next() {
		var bookID, sku, price string
		if err := items.Scan(&bookID, &sku, &price); err != nil {
			return nil, err
		}
		for _, i := range bookIdx[bookID] {
			out[i].Rates[sku] = Decimal(price)
		}
	}
	return out, items.Err()
}

// SourceHealth is a cost source with the collection state the stale-source
// rule reads, plus its customer's status (a source of a customer that is
// not active is never collected, so it cannot be stale).
type SourceHealth struct {
	SourceID        string
	CustomerID      string
	CustomerName    string
	CustomerStatus  string
	Kind            string
	Region          string
	ProjectID       string
	Status          string
	LastCollectedAt *time.Time
	LastError       string
}

// SourceHealths lists every source in scope.
func (s *Store) SourceHealths(ctx context.Context, scope Scope, customerID string) ([]SourceHealth, error) {
	cid, err := scopedCustomer(scope, customerID)
	if err != nil {
		return nil, err
	}
	q := `SELECT s.id::text, c.id::text, c.name, c.status, s.kind, s.region, s.project_id, s.status, s.last_collected_at, s.last_error
	        FROM cost_sources s JOIN customers c ON c.id = s.customer_id`
	var args []any
	if cid != "" {
		q += ` WHERE c.id::text = $1`
		args = append(args, cid)
	}
	q += ` ORDER BY c.name, s.region, s.project_id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []SourceHealth{}
	for rows.Next() {
		var h SourceHealth
		var collected sql.NullTime
		var lastErr sql.NullString
		if err := rows.Scan(&h.SourceID, &h.CustomerID, &h.CustomerName, &h.CustomerStatus, &h.Kind, &h.Region, &h.ProjectID, &h.Status, &collected, &lastErr); err != nil {
			return nil, err
		}
		h.LastCollectedAt = timePtr(collected)
		h.LastError = lastErr.String
		out = append(out, h)
	}
	return out, rows.Err()
}

// CustomerUnpricedSKU is usage of a customer that HAS a price book but no
// rate for the SKU: revenue that rates to zero.
type CustomerUnpricedSKU struct {
	CustomerID   string
	CustomerName string
	SKU          string
	Unit         string
	Quantity     Decimal
	Resources    int
}

// UnpricedUsageByCustomer aggregates, per customer and SKU, the usage in
// [from, to) that the customer's book does not price. Customers without a
// book are left out (every SKU of theirs is unpriced; the no-price-book
// rule says so once), and so is the CPU-utilisation sample, which is a
// metric and not a meter — the base CTE already excludes it.
func (s *Store) UnpricedUsageByCustomer(ctx context.Context, scope Scope, customerID string, from, to time.Time) ([]CustomerUnpricedSKU, error) {
	cid, err := scopedCustomer(scope, customerID)
	if err != nil {
		return nil, err
	}
	cte, a, err := filteredCTE(CostQuery{CustomerID: cid}, from.UTC(), to.UTC())
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, cte+`
SELECT customer_id::text, min(customer_name), sku, unit, round(sum(quantity), 6)::text, count(DISTINCT resource_id)
  FROM f WHERE unit_price IS NULL
   AND customer_id IN (SELECT id FROM customers WHERE price_book_id IS NOT NULL)
 GROUP BY 1, 3, 4 ORDER BY 1, sum(quantity) DESC, 3`, a.args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []CustomerUnpricedSKU{}
	for rows.Next() {
		var u CustomerUnpricedSKU
		var qty string
		if err := rows.Scan(&u.CustomerID, &u.CustomerName, &u.SKU, &u.Unit, &qty, &u.Resources); err != nil {
			return nil, err
		}
		u.Quantity = Decimal(qty)
		out = append(out, u)
	}
	return out, rows.Err()
}

// CPUUtilMean is the mean hourly CPU utilisation (%) of one ECS instance
// over a window, with the number of hourly samples behind it.
type CPUUtilMean struct {
	CustomerID string
	SourceID   string
	ResourceID string
	Samples    int
	Mean       float64
}

// CPUUtilMeans averages the ecs.cpu_util samples per instance in [from, to).
// A mean of a metric is a float: nothing here is money.
func (s *Store) CPUUtilMeans(ctx context.Context, scope Scope, customerID string, from, to time.Time) ([]CPUUtilMean, error) {
	cid, err := scopedCustomer(scope, customerID)
	if err != nil {
		return nil, err
	}
	q := `SELECT customer_id::text, source_id::text, resource_id, count(*), avg(quantity)::float8
	        FROM usage_records WHERE sku = 'ecs.cpu_util' AND window_start >= $1 AND window_start < $2`
	args := []any{from.UTC(), to.UTC()}
	if cid != "" {
		q += ` AND customer_id::text = $3`
		args = append(args, cid)
	}
	q += ` GROUP BY 1, 2, 3 ORDER BY 1, 2, 3`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []CPUUtilMean{}
	for rows.Next() {
		var m CPUUtilMean
		if err := rows.Scan(&m.CustomerID, &m.SourceID, &m.ResourceID, &m.Samples, &m.Mean); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
