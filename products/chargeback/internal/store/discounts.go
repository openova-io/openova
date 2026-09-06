package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// discountColumns is the shared projection so a new field cannot be added to
// one query and forgotten in another.
const discountColumns = `d.id, d.customer_id, d.name, d.kind, d.value::text, d.sku, d.starts_at, d.ends_at, d.active, d.created_at`

func scanDiscount(row interface{ Scan(...any) error }) (Discount, error) {
	var d Discount
	var val string
	var starts, ends sql.NullTime
	if err := row.Scan(&d.ID, &d.CustomerID, &d.Name, &d.Kind, &val, &d.SKU, &starts, &ends, &d.Active, &d.CreatedAt); err != nil {
		return d, mapErr(err)
	}
	d.Value = Decimal(val)
	d.StartsAt = timePtr(starts)
	d.EndsAt = timePtr(ends)
	return d, nil
}

// ListDiscounts returns a customer's discounts, newest first.
func (s *Store) ListDiscounts(ctx context.Context, scope Scope, customerID string) ([]Discount, error) {
	if !scope.Allows(customerID) {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+discountColumns+` FROM discounts d WHERE d.customer_id = $1 ORDER BY d.created_at DESC`, customerID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []Discount{}
	for rows.Next() {
		d, err := scanDiscount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DiscountInput creates one discount.
type DiscountInput struct {
	CustomerID string
	Name       string
	Kind       string
	Value      Decimal
	SKU        string
	StartsAt   *time.Time
	EndsAt     *time.Time
}

// CreateDiscount stores a discount. The CHECK constraints reject a negative
// value and an inverted window at the database, so a bad campaign cannot be
// persisted even if a caller skips validation.
func (s *Store) CreateDiscount(ctx context.Context, in DiscountInput) (Discount, error) {
	row := s.db.QueryRowContext(ctx,
		`INSERT INTO discounts (customer_id, name, kind, value, sku, starts_at, ends_at)
		 VALUES ($1,$2,$3,$4::numeric,$5,$6,$7) RETURNING `+strings.ReplaceAll(discountColumns, "d.", ""),
		in.CustomerID, strings.TrimSpace(in.Name), in.Kind, string(in.Value),
		strings.TrimSpace(in.SKU), in.StartsAt, in.EndsAt)
	return scanDiscount(row)
}

// SetDiscountActive enables or disables a discount without deleting it, so a
// campaign that ran stays visible on the statements it affected.
func (s *Store) SetDiscountActive(ctx context.Context, id string, active bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE discounts SET active = $2 WHERE id = $1`, id, active)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ActiveDiscountsAt returns the discounts live for a customer at t.
func (s *Store) ActiveDiscountsAt(ctx context.Context, customerID string, t time.Time) ([]Discount, error) {
	all, err := s.ListDiscounts(ctx, OperatorScope, customerID)
	if err != nil {
		return nil, err
	}
	out := []Discount{}
	for _, d := range all {
		if d.AppliesAt(t) {
			out = append(out, d)
		}
	}
	return out, nil
}
