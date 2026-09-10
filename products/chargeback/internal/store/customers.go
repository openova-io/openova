package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const customerColumns = `c.id, c.slug, c.name, c.admin_email, c.kind, c.org_slug, c.price_book_id, c.billing_mode, c.status, c.start_date, c.plan_slug,
	c.charging, COALESCE(c.payment_model, ''), COALESCE(c.payment_method, ''), c.gateway_name, c.po_reference, c.payment_terms_days, c.external_account_id,
	c.tax_registration_number, c.tax_exempt, c.tax_exempt_reason, c.tax_rate::text, c.auto_apply_credit, c.low_balance_threshold::text, c.suspend_at_zero,
	c.platform_suspended_at, c.suspension_reason, c.suspension_source, c.external_balance::text, c.external_balance_at, c.created_at, c.updated_at,
	(SELECT count(*) FROM cost_sources s WHERE s.customer_id = c.id),
	(SELECT count(*) FROM cost_sources s WHERE s.customer_id = c.id AND s.status = 'verified'),
	(SELECT count(*) FROM cost_sources s WHERE s.customer_id = c.id AND s.layer = 'cloud'),
	(SELECT count(*) FROM cost_sources s WHERE s.customer_id = c.id AND s.layer = 'platform'),
	(SELECT max(s.last_collected_at) FROM cost_sources s WHERE s.customer_id = c.id),
	(SELECT to_char(max(st.period_start), 'YYYY-MM') FROM statements st WHERE st.customer_id = c.id)`

func scanCustomer(row interface{ Scan(...any) error }) (Customer, error) {
	var c Customer
	var orgSlug, pb sql.NullString
	var start, lastCollected sql.NullTime
	var lastPeriod sql.NullString
	var taxRate, lowBalance, extBalance sql.NullString
	var platformSuspended, extBalanceAt sql.NullTime
	err := row.Scan(&c.ID, &c.Slug, &c.Name, &c.AdminEmail, &c.Kind, &orgSlug, &pb, &c.BillingMode, &c.Status, &start, &c.PlanSlug,
		&c.Charging, &c.PaymentModel, &c.PaymentMethod, &c.GatewayName, &c.PORef, &c.PaymentTermsDays, &c.ExternalAccountID,
		&c.TaxRegistrationNumber, &c.TaxExempt, &c.TaxExemptReason, &taxRate, &c.AutoApplyCredit, &lowBalance, &c.SuspendAtZero,
		&platformSuspended, &c.SuspensionReason, &c.SuspensionSource, &extBalance, &extBalanceAt, &c.CreatedAt, &c.UpdatedAt,
		&c.SourceCount, &c.VerifiedSourceCount, &c.CloudSourceCount, &c.PlatformSourceCount, &lastCollected, &lastPeriod)
	if err != nil {
		return c, mapErr(err)
	}
	c.OrgSlug = strPtr(orgSlug)
	c.PriceBookID = strPtr(pb)
	c.TaxRate = decPtr(taxRate)
	c.LowBalanceThreshold = decPtr(lowBalance)
	c.ExternalBalance = decPtr(extBalance)
	c.PlatformSuspendedAt = timePtr(platformSuspended)
	c.ExternalBalanceAt = timePtr(extBalanceAt)
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

// CustomerInput is the creatable/updatable subset. There is no price book
// here: the book is assigned per source (SetSourcePriceBook), never per
// customer (DESIGN.md §2).
type CustomerInput struct {
	Slug       string
	Name       string
	AdminEmail string
	Kind       string
	OrgSlug    string
	// BillingMode is the LEGACY input (DESIGN.md §8): it is translated
	// through CommercialFromBillingMode when Commercial is not given, which
	// is how the CSV importer and the Organization sync keep working. The
	// customer API no longer sends it.
	BillingMode string
	StartDate   string
	PlanSlug    string
	// Commercial is the four-field commercial position. Its zero value
	// falls back to BillingMode, and failing that to informational.
	Commercial Commercial
	PORef      string
	// ExternalAccountID is the customer's account in the operator's billing
	// system, used when the Sovereign's commercial provider is external.
	ExternalAccountID string
	// PaymentTermsDays is the net terms in days; nil takes the net-30
	// default. A pointer rather than an int because 0 is a real value —
	// due on receipt — and must not read as "not given".
	PaymentTermsDays *int
	// Tax is the customer's tax profile (DESIGN.md §9.4); the zero value
	// is "not registered, not exempt, the Sovereign's rate".
	Tax TaxProfile
	// The account-credit knobs (DESIGN.md §9.5).
	AutoApplyCredit     bool
	LowBalanceThreshold *Decimal
	SuspendAtZero       bool
}

// decPtr reads a nullable numeric column.
func decPtr(ns sql.NullString) *Decimal {
	if !ns.Valid || strings.TrimSpace(ns.String) == "" {
		return nil
	}
	d := Decimal(ns.String)
	return &d
}

// nullDec renders an optional Decimal for SQL: nil or empty is NULL.
func nullDec(p *Decimal) any {
	if p == nil || strings.TrimSpace(string(*p)) == "" {
		return nil
	}
	return string(*p)
}

// validTaxRate checks a rate is a fraction in [0, 1].
func validTaxRate(d Decimal) bool {
	r := ratOf(d)
	return r.Sign() >= 0 && r.Cmp(ratOf("1")) <= 0
}

// CreateCustomer inserts a pending customer and grants admin_email the admin
// role on it.
func (s *Store) CreateCustomer(ctx context.Context, in CustomerInput) (Customer, error) {
	if in.Kind == "" {
		in.Kind = "external"
	}
	// The four-field position wins; a legacy billing_mode is translated
	// through the same mapping the migration used; neither given is
	// informational, which is what the old 'showback' default meant.
	com := in.Commercial
	if com.IsZero() {
		com = CommercialFromBillingMode(in.BillingMode)
	}
	com = com.Normalized()
	if err := com.Validate(); err != nil {
		return Customer{}, err
	}
	terms := DefaultPaymentTermsDays
	if in.PaymentTermsDays != nil {
		terms = *in.PaymentTermsDays
	}
	if terms < 0 || terms > MaxPaymentTermsDays {
		return Customer{}, fmt.Errorf("%w: payment_terms_days must be between 0 and %d", ErrInvalid, MaxPaymentTermsDays)
	}
	if in.Tax.TaxRate != nil && !validTaxRate(*in.Tax.TaxRate) {
		return Customer{}, fmt.Errorf("%w: tax_rate must be a fraction between 0 and 1 (0.05 is 5%%)", ErrInvalid)
	}
	if in.LowBalanceThreshold != nil && ratOf(*in.LowBalanceThreshold).Sign() < 0 {
		return Customer{}, fmt.Errorf("%w: low_balance_threshold cannot be negative", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Customer{}, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `INSERT INTO customers (slug, name, admin_email, kind, org_slug, billing_mode, start_date, plan_slug,
		charging, payment_model, payment_method, gateway_name, po_reference, payment_terms_days, external_account_id,
		tax_registration_number, tax_exempt, tax_exempt_reason, tax_rate, auto_apply_credit, low_balance_threshold, suspend_at_zero)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19::numeric, $20, $21::numeric, $22) RETURNING id`,
		strings.ToLower(strings.TrimSpace(in.Slug)), strings.TrimSpace(in.Name), strings.ToLower(strings.TrimSpace(in.AdminEmail)), in.Kind,
		nullStr(&in.OrgSlug), com.BillingMode(), nullStr(&in.StartDate), NormalizePlanSlug(in.PlanSlug),
		com.Charging, nullStr(&com.PaymentModel), nullStr(&com.PaymentMethod), com.GatewayName,
		strings.TrimSpace(in.PORef), terms, strings.TrimSpace(in.ExternalAccountID),
		strings.TrimSpace(in.Tax.TaxRegistrationNumber), in.Tax.TaxExempt, strings.TrimSpace(in.Tax.TaxExemptReason), nullDec(in.Tax.TaxRate),
		in.AutoApplyCredit, nullDec(in.LowBalanceThreshold), in.SuspendAtZero).Scan(&id)
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

// CustomerPatch carries optional updates; nil means unchanged. The price
// book is not here: it is a property of each source (SourcePatch).
type CustomerPatch struct {
	Name *string
	// BillingMode is the LEGACY patch field: it is translated through
	// CommercialFromBillingMode, so the CSV importer and the Organization
	// sync keep working. The customer API decodes and ignores it.
	BillingMode *string
	AdminEmail  *string
	Status      *string
	StartDate   *string
	OrgSlug     *string
	PlanSlug    *string
	// The commercial model (DESIGN.md §8); nil leaves a field unchanged.
	// Switching Charging to informational clears the three that are then
	// meaningless, so a partial patch can never leave a refused combination.
	Charging      *string
	PaymentModel  *string
	PaymentMethod *string
	GatewayName   *string
	// PORef and PaymentTermsDays are the invoicing terms.
	PORef            *string
	PaymentTermsDays *int
	// ExternalAccountID is the customer's account in the operator's billing
	// system (external commercial provider only).
	ExternalAccountID *string
	// The tax profile (DESIGN.md §9.4); nil leaves a field unchanged. A
	// TaxRate pointing at an empty Decimal clears the override back to the
	// Sovereign default.
	TaxRegistrationNumber *string
	TaxExempt             *bool
	TaxExemptReason       *string
	TaxRate               *Decimal
	// The account-credit knobs (DESIGN.md §9.5); a LowBalanceThreshold
	// pointing at an empty Decimal turns the alert off.
	AutoApplyCredit     *bool
	LowBalanceThreshold *Decimal
	SuspendAtZero       *bool
}

// UpdateCustomer applies a patch. It runs in a transaction because the
// commercial fields are patched partially and validated as a WHOLE: the row
// is read FOR UPDATE, the patch merged onto it, and the derived billing_mode
// written from the result — so two concurrent patches cannot interleave into
// a combination neither of them asked for.
func (s *Store) UpdateCustomer(ctx context.Context, id string, p CustomerPatch) (Customer, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Customer{}, err
	}
	defer tx.Rollback()
	var cur Commercial
	if err := tx.QueryRowContext(ctx, `SELECT charging, COALESCE(payment_model, ''), COALESCE(payment_method, ''), gateway_name
		FROM customers WHERE id = $1 FOR UPDATE`, id).Scan(&cur.Charging, &cur.PaymentModel, &cur.PaymentMethod, &cur.GatewayName); err != nil {
		return Customer{}, mapErr(err)
	}
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
	// The four commercial fields, plus the legacy billing_mode translated
	// through the same mapping the migration used. billing_mode is never
	// written on its own: it is derived from the result.
	charging, model, method, gateway := p.Charging, p.PaymentModel, p.PaymentMethod, p.GatewayName
	if p.BillingMode != nil && charging == nil && model == nil && method == nil && gateway == nil {
		legacy := CommercialFromBillingMode(*p.BillingMode)
		charging, model, method, gateway = &legacy.Charging, &legacy.PaymentModel, &legacy.PaymentMethod, &legacy.GatewayName
	}
	if charging != nil || model != nil || method != nil || gateway != nil {
		next := cur.Merge(charging, model, method, gateway)
		if err := next.Validate(); err != nil {
			return Customer{}, err
		}
		add("charging", next.Charging)
		add("payment_model", nullStr(&next.PaymentModel))
		add("payment_method", nullStr(&next.PaymentMethod))
		add("gateway_name", next.GatewayName)
		add("billing_mode", next.BillingMode())
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
	if p.PlanSlug != nil {
		add("plan_slug", NormalizePlanSlug(*p.PlanSlug))
	}
	if p.PORef != nil {
		add("po_reference", strings.TrimSpace(*p.PORef))
	}
	if p.ExternalAccountID != nil {
		add("external_account_id", strings.TrimSpace(*p.ExternalAccountID))
	}
	if p.PaymentTermsDays != nil {
		if *p.PaymentTermsDays < 0 || *p.PaymentTermsDays > MaxPaymentTermsDays {
			return Customer{}, fmt.Errorf("%w: payment_terms_days must be between 0 and %d", ErrInvalid, MaxPaymentTermsDays)
		}
		add("payment_terms_days", *p.PaymentTermsDays)
	}
	if p.TaxRegistrationNumber != nil {
		add("tax_registration_number", strings.TrimSpace(*p.TaxRegistrationNumber))
	}
	if p.TaxExempt != nil {
		add("tax_exempt", *p.TaxExempt)
	}
	if p.TaxExemptReason != nil {
		add("tax_exempt_reason", strings.TrimSpace(*p.TaxExemptReason))
	}
	if p.TaxRate != nil {
		if strings.TrimSpace(string(*p.TaxRate)) != "" && !validTaxRate(*p.TaxRate) {
			return Customer{}, fmt.Errorf("%w: tax_rate must be a fraction between 0 and 1 (0.05 is 5%%)", ErrInvalid)
		}
		add("tax_rate", nullDec(p.TaxRate))
	}
	if p.AutoApplyCredit != nil {
		add("auto_apply_credit", *p.AutoApplyCredit)
	}
	if p.LowBalanceThreshold != nil {
		if strings.TrimSpace(string(*p.LowBalanceThreshold)) != "" && ratOf(*p.LowBalanceThreshold).Sign() < 0 {
			return Customer{}, fmt.Errorf("%w: low_balance_threshold cannot be negative", ErrInvalid)
		}
		add("low_balance_threshold", nullDec(p.LowBalanceThreshold))
	}
	if p.SuspendAtZero != nil {
		add("suspend_at_zero", *p.SuspendAtZero)
	}
	args = append(args, id)
	res, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE customers SET %s WHERE id = $%d`, strings.Join(sets, ", "), len(args)), args...)
	if err != nil {
		return Customer{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Customer{}, ErrNotFound
	}
	if p.AdminEmail != nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO customer_users (customer_id, email, role) VALUES ($1, $2, 'admin') ON CONFLICT DO NOTHING`, id, strings.ToLower(strings.TrimSpace(*p.AdminEmail))); err != nil {
			return Customer{}, mapErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Customer{}, err
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

// DeleteCustomer removes a customer and, through the FK cascades, its
// sources, credentials, usage, inventory, users, invites, discounts, budgets
// and draft statements. It is refused (ErrConflict) while any ISSUED
// statement exists: an issued bill is a financial record and must survive
// the customer that received it — suspend the customer instead.
func (s *Store) DeleteCustomer(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM customers WHERE id = $1 FOR UPDATE)`, id).Scan(&exists); err != nil {
		return mapErr(err)
	}
	if !exists {
		return ErrNotFound
	}
	// Anything past draft — issued, sent, paid or cancelled — is a document
	// the customer received and a financial record; only drafts cascade.
	var issued int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM statements WHERE customer_id = $1 AND status <> 'draft'`, id).Scan(&issued); err != nil {
		return mapErr(err)
	}
	if issued > 0 {
		return fmt.Errorf("%w: customer has %d issued statement(s); issued statements are permanent records, suspend the customer instead of deleting it", ErrConflict, issued)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM customers WHERE id = $1`, id); err != nil {
		return mapErr(err)
	}
	return tx.Commit()
}
