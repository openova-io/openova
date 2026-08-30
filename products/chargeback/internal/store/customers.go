package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const customerColumns = `c.id, c.slug, c.name, c.admin_email, c.kind, c.org_slug, c.price_book_id, c.billing_mode, c.status, c.start_date, c.created_at, c.updated_at,
	(SELECT count(*) FROM cost_sources s WHERE s.customer_id = c.id),
	(SELECT count(*) FROM cost_sources s WHERE s.customer_id = c.id AND s.status = 'verified'),
	(SELECT max(s.last_collected_at) FROM cost_sources s WHERE s.customer_id = c.id),
	(SELECT to_char(max(st.period_start), 'YYYY-MM') FROM statements st WHERE st.customer_id = c.id)`

func scanCustomer(row interface{ Scan(...any) error }) (Customer, error) {
	var c Customer
	var orgSlug, pb sql.NullString
	var start, lastCollected sql.NullTime
	var lastPeriod sql.NullString
	err := row.Scan(&c.ID, &c.Slug, &c.Name, &c.AdminEmail, &c.Kind, &orgSlug, &pb, &c.BillingMode, &c.Status, &start, &c.CreatedAt, &c.UpdatedAt,
		&c.SourceCount, &c.VerifiedSourceCount, &lastCollected, &lastPeriod)
	if err != nil {
		return c, mapErr(err)
	}
	c.OrgSlug = strPtr(orgSlug)
	c.PriceBookID = strPtr(pb)
	c.StartDate = datePtr(start)
	c.LastCollectedAt = timePtr(lastCollected)
	c.LastStatementPeriod = strPtr(lastPeriod)
	c.Collecting = c.Status == "active" && c.VerifiedSourceCount > 0
	return c, nil
}

// ListCustomers returns the customers visible to the scope.
func (s *Store) ListCustomers(ctx context.Context, scope Scope) ([]Customer, error) {
	q := `SELECT ` + customerColumns + ` FROM customers c`
	var args []any
	if !scope.Operator {
		q += ` WHERE c.id = $1`
		args = append(args, scope.CustomerID)
	}
	q += ` ORDER BY c.name`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []Customer{}
	for rows.Next() {
		c, err := scanCustomer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetCustomer returns one customer inside the scope (ErrNotFound otherwise).
func (s *Store) GetCustomer(ctx context.Context, scope Scope, id string) (Customer, error) {
	if !scope.Allows(id) {
		return Customer{}, ErrNotFound
	}
	return scanCustomer(s.db.QueryRowContext(ctx, `SELECT `+customerColumns+` FROM customers c WHERE c.id = $1`, id))
}

// GetCustomerBySlug is operator-only (used by import upserts).
func (s *Store) GetCustomerBySlug(ctx context.Context, slug string) (Customer, error) {
	return scanCustomer(s.db.QueryRowContext(ctx, `SELECT `+customerColumns+` FROM customers c WHERE c.slug = $1`, slug))
}

// CustomerInput is the creatable/updatable subset.
type CustomerInput struct {
	Slug        string
	Name        string
	AdminEmail  string
	Kind        string
	OrgSlug     string
	PriceBookID string
	BillingMode string
	StartDate   string
}

// CreateCustomer inserts a pending customer and grants admin_email the admin
// role on it.
func (s *Store) CreateCustomer(ctx context.Context, in CustomerInput) (Customer, error) {
	if in.Kind == "" {
		in.Kind = "external"
	}
	if in.BillingMode == "" {
		in.BillingMode = "showback"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Customer{}, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `INSERT INTO customers (slug, name, admin_email, kind, org_slug, price_book_id, billing_mode, start_date)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		strings.ToLower(strings.TrimSpace(in.Slug)), strings.TrimSpace(in.Name), strings.ToLower(strings.TrimSpace(in.AdminEmail)), in.Kind,
		nullStr(&in.OrgSlug), nullStr(&in.PriceBookID), in.BillingMode, nullStr(&in.StartDate)).Scan(&id)
	if err != nil {
		return Customer{}, mapErr(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO customer_users (customer_id, email, role) VALUES ($1, $2, 'admin') ON CONFLICT DO NOTHING`, id, strings.ToLower(strings.TrimSpace(in.AdminEmail))); err != nil {
		return Customer{}, mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return Customer{}, err
	}
	return s.GetCustomer(ctx, OperatorScope, id)
}

// CustomerPatch carries optional updates; nil means unchanged.
type CustomerPatch struct {
	Name        *string
	AdminEmail  *string
	PriceBookID *string
	BillingMode *string
	Status      *string
	StartDate   *string
	OrgSlug     *string
}

// UpdateCustomer applies a patch.
func (s *Store) UpdateCustomer(ctx context.Context, id string, p CustomerPatch) (Customer, error) {
	sets := []string{"updated_at = now()"}
	var args []any
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if p.Name != nil {
		add("name", strings.TrimSpace(*p.Name))
	}
	if p.AdminEmail != nil {
		add("admin_email", strings.ToLower(strings.TrimSpace(*p.AdminEmail)))
	}
	if p.PriceBookID != nil {
		add("price_book_id", nullStr(p.PriceBookID))
	}
	if p.BillingMode != nil {
		add("billing_mode", *p.BillingMode)
	}
	if p.Status != nil {
		add("status", *p.Status)
	}
	if p.StartDate != nil {
		add("start_date", nullStr(p.StartDate))
	}
	if p.OrgSlug != nil {
		add("org_slug", nullStr(p.OrgSlug))
	}
	args = append(args, id)
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`UPDATE customers SET %s WHERE id = $%d`, strings.Join(sets, ", "), len(args)), args...)
	if err != nil {
		return Customer{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Customer{}, ErrNotFound
	}
	if p.AdminEmail != nil {
		_, _ = s.db.ExecContext(ctx, `INSERT INTO customer_users (customer_id, email, role) VALUES ($1, $2, 'admin') ON CONFLICT DO NOTHING`, id, strings.ToLower(strings.TrimSpace(*p.AdminEmail)))
	}
	return s.GetCustomer(ctx, OperatorScope, id)
}

// SetCustomerStatus is a targeted status change (activation, suspension).
func (s *Store) SetCustomerStatus(ctx context.Context, id, status string) error {
	st := status
	_, err := s.UpdateCustomer(ctx, id, CustomerPatch{Status: &st})
	return err
}

// ListCustomerUsers returns the users of one customer.
func (s *Store) ListCustomerUsers(ctx context.Context, customerID string) ([]CustomerUser, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT customer_id, email, role FROM customer_users WHERE customer_id = $1 ORDER BY email`, customerID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []CustomerUser{}
	for rows.Next() {
		var u CustomerUser
		if err := rows.Scan(&u.CustomerID, &u.Email, &u.Role); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpsertCustomerUser adds or re-roles a user.
func (s *Store) UpsertCustomerUser(ctx context.Context, customerID, email, role string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO customer_users (customer_id, email, role) VALUES ($1, $2, $3)
		ON CONFLICT (customer_id, email) DO UPDATE SET role = EXCLUDED.role`, customerID, strings.ToLower(strings.TrimSpace(email)), role)
	return mapErr(err)
}

// DeleteCustomerUser removes a user's access.
func (s *Store) DeleteCustomerUser(ctx context.Context, customerID, email string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM customer_users WHERE customer_id = $1 AND email = $2`, customerID, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RoleForEmail resolves the customer role of an email: the first customer
// (by slug) granting it, admin winning over viewer. ok=false when none.
func (s *Store) RoleForEmail(ctx context.Context, email string) (customerID, role string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT cu.customer_id, cu.role FROM customer_users cu JOIN customers c ON c.id = cu.customer_id
		WHERE cu.email = $1 ORDER BY (cu.role = 'admin') DESC, c.slug LIMIT 1`, strings.ToLower(strings.TrimSpace(email))).Scan(&customerID, &role)
	if err == sql.ErrNoRows {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, mapErr(err)
	}
	return customerID, role, true, nil
}

// CustomerCountsByStatus feeds the operator overview.
func (s *Store) CustomerCountsByStatus(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, count(*) FROM customers GROUP BY status`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := map[string]int{"pending": 0, "active": 0, "suspended": 0}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// CustomerStartDate returns the billing start (zero when unset).
func (s *Store) CustomerStartDate(ctx context.Context, id string) (time.Time, error) {
	var d sql.NullTime
	if err := s.db.QueryRowContext(ctx, `SELECT start_date FROM customers WHERE id = $1`, id).Scan(&d); err != nil {
		return time.Time{}, mapErr(err)
	}
	if !d.Valid {
		return time.Time{}, nil
	}
	return d.Time.UTC(), nil
}
