package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// discountColumns is the shared projection so a new field cannot be added to
// one query and forgotten in another. Every query joins customers with a LEFT
// JOIN because a global campaign (customer_id NULL, #6867) has no customer.
const discountColumns = `d.id, d.customer_id, c.name, d.name, d.kind, d.value::text, d.sku, d.starts_at, d.ends_at, d.active, d.created_at`

const discountFrom = ` FROM discounts d LEFT JOIN customers c ON c.id = d.customer_id`

func scanDiscount(row interface{ Scan(...any) error }) (Discount, error) {
	var d Discount
	var val string
	var customerID, customerName sql.NullString
	var starts, ends sql.NullTime
	if err := row.Scan(&d.ID, &customerID, &customerName, &d.Name, &d.Kind, &val, &d.SKU, &starts, &ends, &d.Active, &d.CreatedAt); err != nil {
		return d, mapErr(err)
	}
	d.CustomerID = strPtr(customerID)
	d.CustomerName = strPtr(customerName)
	d.Value = Decimal(val)
	d.StartsAt = timePtr(starts)
	d.EndsAt = timePtr(ends)
	return d, nil
}

func (s *Store) queryDiscounts(ctx context.Context, where string, args ...any) ([]Discount, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+discountColumns+discountFrom+where+` ORDER BY d.created_at DESC, d.id`, args...)
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

// ListDiscounts returns what applies to a customer, newest first: its own
// discounts AND the global campaigns (customer_id NULL). A customer reading
// its list sees everything that will actually reduce its bill; the global
// rows are distinguishable by their null customer_id.
func (s *Store) ListDiscounts(ctx context.Context, scope Scope, customerID string) ([]Discount, error) {
	if !scope.Allows(customerID) {
		return nil, ErrNotFound
	}
	return s.queryDiscounts(ctx, ` WHERE d.customer_id = $1 OR d.customer_id IS NULL`, customerID)
}

// ListAllDiscounts returns every discount and campaign (operator view).
func (s *Store) ListAllDiscounts(ctx context.Context) ([]Discount, error) {
	return s.queryDiscounts(ctx, ``)
}

// GetDiscount returns one discount by id (operator; no scope — a discount
// is granted by the operator and only the operator edits it).
func (s *Store) GetDiscount(ctx context.Context, id string) (Discount, error) {
	return scanDiscount(s.db.QueryRowContext(ctx, `SELECT `+discountColumns+discountFrom+` WHERE d.id = $1`, id))
}

// DiscountInput creates or fully replaces one discount.
type DiscountInput struct {
	// CustomerID nil = a global campaign for every customer.
	CustomerID *string
	Name       string
	Kind       string
	Value      Decimal
	SKU        string
	StartsAt   *time.Time
	EndsAt     *time.Time
	// Active nil keeps the default (true on create, unchanged on update).
	Active *bool
}

// CreateDiscount stores a discount. The CHECK constraints reject a negative
// value and an inverted window at the database, so a bad campaign cannot be
// persisted even if a caller skips validation.
func (s *Store) CreateDiscount(ctx context.Context, in DiscountInput) (Discount, error) {
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	var id string
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO discounts (customer_id, name, kind, value, sku, starts_at, ends_at, active)
		 VALUES ($1,$2,$3,$4::numeric,$5,$6,$7,$8) RETURNING id`,
		nullStr(in.CustomerID), strings.TrimSpace(in.Name), in.Kind, string(in.Value),
		strings.TrimSpace(in.SKU), nullTime(in.StartsAt), nullTime(in.EndsAt), active).Scan(&id)
	if err != nil {
		return Discount{}, mapErr(err)
	}
	return s.GetDiscount(ctx, id)
}

// UpdateDiscount replaces every field of a discount (PUT semantics). A nil
// CustomerID turns it into a global campaign; nil StartsAt/EndsAt clear the
// window; Active nil leaves the flag as it is.
func (s *Store) UpdateDiscount(ctx context.Context, id string, in DiscountInput) (Discount, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE discounts SET customer_id = $2, name = $3, kind = $4, value = $5::numeric, sku = $6, starts_at = $7, ends_at = $8,
		 active = COALESCE($9, active) WHERE id = $1`,
		id, nullStr(in.CustomerID), strings.TrimSpace(in.Name), in.Kind, string(in.Value),
		strings.TrimSpace(in.SKU), nullTime(in.StartsAt), nullTime(in.EndsAt), nullBool(in.Active))
	if err != nil {
		return Discount{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Discount{}, ErrNotFound
	}
	return s.GetDiscount(ctx, id)
}

// DeleteDiscount removes a discount. Statements already rated keep their
// frozen discount_detail, so deleting never rewrites an issued bill.
func (s *Store) DeleteDiscount(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM discounts WHERE id = $1`, id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
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

// ActiveDiscountsAt returns the discounts live for a customer at t: the
// customer's own plus every global campaign (customer_id NULL), which is
// what makes a global 10 % actually reach the statement.
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

func nullBool(p *bool) sql.NullBool {
	if p == nil {
		return sql.NullBool{}
	}
	return sql.NullBool{Bool: *p, Valid: true}
}

// ScopeLabel names the discount's scope for audit and display.
func (d Discount) ScopeLabel() string {
	if d.CustomerID == nil {
		return "all customers"
	}
	if d.CustomerName != nil {
		return *d.CustomerName
	}
	return fmt.Sprintf("customer %s", *d.CustomerID)
}
