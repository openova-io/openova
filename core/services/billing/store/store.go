package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// Customer represents a billing customer linked to a Stripe account.
type Customer struct {
	ID               string    `json:"id"`
	UserID           string    `json:"user_id"`
	TenantID         string    `json:"tenant_id"`
	StripeCustomerID string    `json:"stripe_customer_id,omitempty"`
	Email            string    `json:"email"`
	CreatedAt        time.Time `json:"created_at"`
}

// Subscription represents an active or past subscription.
type Subscription struct {
	ID                   string    `json:"id"`
	CustomerID           string    `json:"customer_id"`
	TenantID             string    `json:"tenant_id"`
	StripeSubscriptionID string    `json:"stripe_subscription_id,omitempty"`
	PlanID               string    `json:"plan_id"`
	Status               string    `json:"status"`
	CurrentPeriodStart   time.Time `json:"current_period_start,omitempty"`
	CurrentPeriodEnd     time.Time `json:"current_period_end,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// Order represents a checkout order.
//
// Amounts: AmountBaisa is the canonical money value in the smallest currency
// unit (baisa, 1/1000 OMR). AmountOMR is the whole-OMR view kept for legacy
// API consumers (Console/Admin/Marketplace clients) that still expect OMR
// integers. Do NOT use AmountOMR for any math that originates from Stripe —
// Stripe emits smallest-unit amounts and a 1 OMR invoice arrives as 1000
// baisa. See store.BaisaToOMR / store.OMRToBaisa for safe conversion.
type Order struct {
	ID              string          `json:"id"`
	CustomerID      string          `json:"customer_id"`
	TenantID        string          `json:"tenant_id"`
	PlanID          string          `json:"plan_id"`
	Apps            json.RawMessage `json:"apps"`
	Addons          json.RawMessage `json:"addons"`
	AmountOMR       int             `json:"amount_omr"`
	AmountBaisa     int64           `json:"amount_baisa"`
	Status          string          `json:"status"`
	StripeSessionID string          `json:"stripe_session_id,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`

	// PromoCode is the voucher applied at checkout, if any. Stored in the
	// orders row (not the promo_redemptions table) so the value survives
	// soft-deletion of the promo — admin views need to show which code was
	// used even after the admin retired the promo. See #91.
	PromoCode    string `json:"promo_code,omitempty"`
	// PromoDeleted is populated on read by joining promo_codes.deleted_at;
	// the admin UI shows a "deleted" badge next to the code so historical
	// redemptions remain auditable without any stale reference appearing
	// "active". See #91.
	PromoDeleted bool `json:"promo_deleted,omitempty"`
}

// Invoice represents a billing invoice.
//
// Amounts follow the same convention as Order: AmountBaisa is authoritative,
// AmountOMR is a derived whole-OMR view for legacy clients.
type Invoice struct {
	ID              string    `json:"id"`
	CustomerID      string    `json:"customer_id"`
	TenantID        string    `json:"tenant_id"`
	StripeInvoiceID string    `json:"stripe_invoice_id,omitempty"`
	AmountOMR       int       `json:"amount_omr"`
	AmountBaisa     int64     `json:"amount_baisa"`
	Currency        string    `json:"currency,omitempty"`
	Status          string    `json:"status"`
	PeriodStart     time.Time `json:"period_start,omitempty"`
	PeriodEnd       time.Time `json:"period_end,omitempty"`
	PDFURL          string    `json:"pdf_url,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// BaisaToOMR converts an integer baisa amount (1/1000 OMR) into a float OMR
// value suitable for UI rendering at the API boundary. The returned value has
// millibaisa precision (3 decimals) which matches the smallest unit of OMR.
func BaisaToOMR(baisa int64) float64 {
	return float64(baisa) / 1000.0
}

// OMRToBaisa converts a whole-OMR integer into baisa. Intended for migrating
// legacy integer-OMR order totals into the baisa column.
func OMRToBaisa(omr int) int64 {
	return int64(omr) * 1000
}

// RevenueSummary holds aggregate billing metrics.
type RevenueSummary struct {
	TotalMRR            int `json:"total_mrr"`
	TotalCustomers      int `json:"total_customers"`
	NewThisMonth        int `json:"new_this_month"`
	ActiveSubscriptions int `json:"active_subscriptions"`
}

// Settings stores admin-configured billing configuration (Stripe keys, etc).
// There is a single row with id = 1.
type Settings struct {
	StripeSecretKey     string    `json:"stripe_secret_key,omitempty"`
	StripeWebhookSecret string    `json:"stripe_webhook_secret,omitempty"`
	StripePublicKey     string    `json:"stripe_public_key,omitempty"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// PromoCode grants a credit amount to the customer when redeemed.
//
// Soft-delete semantics (#91): DeletedAt is non-null when an admin retired the
// code. Retired codes:
//   - Are excluded from ListPromoCodes output
//   - Cannot be redeemed (RedeemPromoCode returns "promo code not found")
//   - Still resolve by GetPromoCode only if the caller explicitly asks — the
//     public surface treats them as absent
//   - Remain the foreign-key target for past promo_redemptions + orders so
//     historical analytics and admin order views stay intact
type PromoCode struct {
	Code           string     `json:"code"`
	CreditOMR      int        `json:"credit_omr"`
	Description    string     `json:"description"`
	Active         bool       `json:"active"`
	MaxRedemptions int        `json:"max_redemptions"`
	TimesRedeemed  int        `json:"times_redeemed"`
	CreatedAt      time.Time  `json:"created_at"`
	DeletedAt      *time.Time `json:"deleted_at,omitempty"`
}

// CreditLedger tracks credit transactions per customer.
// Positive amount = grant, negative = spend.
type CreditLedger struct {
	ID         string    `json:"id"`
	CustomerID string    `json:"customer_id"`
	AmountOMR  int       `json:"amount_omr"`
	Reason     string    `json:"reason"`
	OrderID    string    `json:"order_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

// Store provides CRUD operations against a PostgreSQL database.
type Store struct {
	db *sql.DB
}

// New creates a Store backed by the given database connection.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// Migrate creates tables if they do not exist.
func (s *Store) Migrate(ctx context.Context) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS customers (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id TEXT UNIQUE NOT NULL,
			tenant_id TEXT NOT NULL,
			stripe_customer_id TEXT UNIQUE,
			email TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS subscriptions (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			customer_id UUID NOT NULL REFERENCES customers(id),
			tenant_id TEXT NOT NULL,
			stripe_subscription_id TEXT UNIQUE,
			plan_id TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			current_period_start TIMESTAMPTZ,
			current_period_end TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS orders (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			customer_id UUID NOT NULL REFERENCES customers(id),
			tenant_id TEXT NOT NULL,
			plan_id TEXT NOT NULL,
			apps JSONB NOT NULL DEFAULT '[]',
			addons JSONB NOT NULL DEFAULT '[]',
			amount_omr INT NOT NULL,
			amount_baisa BIGINT NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'pending',
			stripe_session_id TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		// Backfill for databases that predate the amount_baisa column.
		`ALTER TABLE orders ADD COLUMN IF NOT EXISTS amount_baisa BIGINT NOT NULL DEFAULT 0`,
		`UPDATE orders SET amount_baisa = amount_omr * 1000 WHERE amount_baisa = 0 AND amount_omr > 0`,
		// #91 — record which promo code (if any) was applied to this order so
		// the admin order list can show it even after the promo is retired.
		// The column is nullable because most orders have no promo.
		`ALTER TABLE orders ADD COLUMN IF NOT EXISTS promo_code TEXT`,
		`CREATE TABLE IF NOT EXISTS invoices (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			customer_id UUID NOT NULL REFERENCES customers(id),
			tenant_id TEXT NOT NULL,
			stripe_invoice_id TEXT UNIQUE,
			amount_omr INT NOT NULL,
			amount_baisa BIGINT NOT NULL DEFAULT 0,
			currency TEXT NOT NULL DEFAULT 'omr',
			status TEXT NOT NULL DEFAULT 'draft',
			period_start TIMESTAMPTZ,
			period_end TIMESTAMPTZ,
			pdf_url TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`ALTER TABLE invoices ADD COLUMN IF NOT EXISTS amount_baisa BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE invoices ADD COLUMN IF NOT EXISTS currency TEXT NOT NULL DEFAULT 'omr'`,
		// Legacy rows were written with amount_omr set to the raw Stripe baisa
		// value (the #78 bug). We can't distinguish a legitimate 5 OMR invoice
		// from a mis-stored 5-baisa one after the fact, so we conservatively
		// mirror amount_omr into amount_baisa for any row that still has
		// amount_baisa = 0. New writes use both columns correctly.
		`UPDATE invoices SET amount_baisa = amount_omr WHERE amount_baisa = 0 AND amount_omr > 0`,
		// Stripe webhook idempotency (#77). We INSERT-on-conflict-ignore and
		// short-circuit the handler when the event has already been seen.
		`CREATE TABLE IF NOT EXISTS stripe_webhook_events (
			event_id TEXT PRIMARY KEY,
			event_type TEXT NOT NULL DEFAULT '',
			processed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			status TEXT NOT NULL DEFAULT 'processed'
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			id INT PRIMARY KEY DEFAULT 1,
			stripe_secret_key TEXT NOT NULL DEFAULT '',
			stripe_webhook_secret TEXT NOT NULL DEFAULT '',
			stripe_public_key TEXT NOT NULL DEFAULT '',
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			CONSTRAINT settings_single_row CHECK (id = 1)
		)`,
		`INSERT INTO settings (id) VALUES (1) ON CONFLICT (id) DO NOTHING`,
		`CREATE TABLE IF NOT EXISTS promo_codes (
			code TEXT PRIMARY KEY,
			credit_omr INT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			active BOOLEAN NOT NULL DEFAULT true,
			max_redemptions INT NOT NULL DEFAULT 0,
			times_redeemed INT NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		// #91 — soft-delete column. A non-null deleted_at means the admin
		// retired the code; the row stays for FK integrity (promo_redemptions
		// + orders.promo_code both reference it) but the code is hidden from
		// listings and rejected by RedeemPromoCode.
		`ALTER TABLE promo_codes ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ`,
		`CREATE TABLE IF NOT EXISTS credit_ledger (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			customer_id UUID NOT NULL REFERENCES customers(id),
			amount_omr INT NOT NULL,
			reason TEXT NOT NULL,
			order_id TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		// Status column for the credit ledger — when a tenant is deleted we
		// flip its entries to 'tenant_deleted' rather than purging them,
		// preserving the audit trail for financial reporting. New entries
		// default to 'active'. See issue #94.
		`ALTER TABLE credit_ledger ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active'`,
		`CREATE TABLE IF NOT EXISTS promo_redemptions (
			customer_id UUID NOT NULL REFERENCES customers(id),
			code TEXT NOT NULL REFERENCES promo_codes(code),
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (customer_id, code)
		)`,
	}

	for _, q := range queries {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("store: migrate: %w", err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Customers
// ---------------------------------------------------------------------------

// CreateCustomer inserts a new customer record.
func (s *Store) CreateCustomer(ctx context.Context, c *Customer) error {
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO customers (user_id, tenant_id, stripe_customer_id, email)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, created_at`,
		c.UserID, c.TenantID, nilIfEmpty(c.StripeCustomerID), c.Email,
	).Scan(&c.ID, &c.CreatedAt)
	if err != nil {
		return fmt.Errorf("store: create customer: %w", err)
	}
	return nil
}

// GetCustomerByUserID returns a customer by their user ID.
func (s *Store) GetCustomerByUserID(ctx context.Context, userID string) (*Customer, error) {
	var c Customer
	var stripeID sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, tenant_id, stripe_customer_id, email, created_at
		 FROM customers WHERE user_id = $1`, userID,
	).Scan(&c.ID, &c.UserID, &c.TenantID, &stripeID, &c.Email, &c.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get customer by user_id %s: %w", userID, err)
	}
	c.StripeCustomerID = stripeID.String
	return &c, nil
}

// GetCustomerByStripeID returns a customer by their Stripe customer ID.
func (s *Store) GetCustomerByStripeID(ctx context.Context, stripeID string) (*Customer, error) {
	var c Customer
	var sid sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, tenant_id, stripe_customer_id, email, created_at
		 FROM customers WHERE stripe_customer_id = $1`, stripeID,
	).Scan(&c.ID, &c.UserID, &c.TenantID, &sid, &c.Email, &c.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get customer by stripe_id %s: %w", stripeID, err)
	}
	c.StripeCustomerID = sid.String
	return &c, nil
}

// UpdateStripeCustomerID sets the Stripe customer ID for a customer.
func (s *Store) UpdateStripeCustomerID(ctx context.Context, customerID, stripeCustomerID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE customers SET stripe_customer_id = $1 WHERE id = $2`,
		stripeCustomerID, customerID,
	)
	if err != nil {
		return fmt.Errorf("store: update stripe customer id: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("store: customer %s not found", customerID)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Orders
// ---------------------------------------------------------------------------

// CreateOrder inserts a new order record.
//
// Callers should set both AmountOMR (legacy int OMR) and AmountBaisa (the
// canonical value). If only AmountOMR is set, AmountBaisa is auto-derived as
// AmountOMR * 1000 so existing in-app flows (credit-settled orders that come
// from catalog prices in whole OMR) remain correct without code changes.
func (s *Store) CreateOrder(ctx context.Context, o *Order) error {
	if o.Apps == nil {
		o.Apps = json.RawMessage(`[]`)
	}
	if o.Addons == nil {
		o.Addons = json.RawMessage(`[]`)
	}
	if o.AmountBaisa == 0 && o.AmountOMR > 0 {
		o.AmountBaisa = OMRToBaisa(o.AmountOMR)
	}
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO orders (customer_id, tenant_id, plan_id, apps, addons, amount_omr, amount_baisa, status, stripe_session_id, promo_code)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING id, created_at`,
		o.CustomerID, o.TenantID, o.PlanID, o.Apps, o.Addons, o.AmountOMR, o.AmountBaisa, o.Status, nilIfEmpty(o.StripeSessionID), nilIfEmpty(o.PromoCode),
	).Scan(&o.ID, &o.CreatedAt)
	if err != nil {
		return fmt.Errorf("store: create order: %w", err)
	}
	return nil
}

// GetOrder returns an order by ID.
//
// #91 — joins promo_codes to surface the code used plus whether it has since
// been soft-deleted, so the admin order detail view can render a "deleted"
// badge without needing a second round-trip.
func (s *Store) GetOrder(ctx context.Context, id string) (*Order, error) {
	var o Order
	var sessionID sql.NullString
	var promoCode sql.NullString
	var promoDeletedAt sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT o.id, o.customer_id, o.tenant_id, o.plan_id, o.apps, o.addons,
		        o.amount_omr, o.amount_baisa, o.status, o.stripe_session_id, o.created_at,
		        o.promo_code, pc.deleted_at
		   FROM orders o
		   LEFT JOIN promo_codes pc ON pc.code = o.promo_code
		  WHERE o.id = $1`, id,
	).Scan(&o.ID, &o.CustomerID, &o.TenantID, &o.PlanID, &o.Apps, &o.Addons,
		&o.AmountOMR, &o.AmountBaisa, &o.Status, &sessionID, &o.CreatedAt,
		&promoCode, &promoDeletedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get order %s: %w", id, err)
	}
	o.StripeSessionID = sessionID.String
	o.PromoCode = promoCode.String
	o.PromoDeleted = promoDeletedAt.Valid
	return &o, nil
}

// UpdateOrderStatus updates the status and optional Stripe session ID of an order.
func (s *Store) UpdateOrderStatus(ctx context.Context, id, status, stripeSessionID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE orders SET status = $1, stripe_session_id = $2 WHERE id = $3`,
		status, nilIfEmpty(stripeSessionID), id,
	)
	if err != nil {
		return fmt.Errorf("store: update order status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("store: order %s not found", id)
	}
	return nil
}

// ListRecentOrders returns the most recent orders (limit 50).
//
// #91 — LEFT JOINs promo_codes so callers see both the promo code used and
// whether it has been soft-deleted, without a per-row extra query.
func (s *Store) ListRecentOrders(ctx context.Context) ([]Order, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT o.id, o.customer_id, o.tenant_id, o.plan_id, o.apps, o.addons,
		        o.amount_omr, o.amount_baisa, o.status, o.stripe_session_id, o.created_at,
		        o.promo_code, pc.deleted_at
		   FROM orders o
		   LEFT JOIN promo_codes pc ON pc.code = o.promo_code
		  ORDER BY o.created_at DESC LIMIT 50`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list recent orders: %w", err)
	}
	defer rows.Close()

	var orders []Order
	for rows.Next() {
		var o Order
		var sessionID sql.NullString
		var promoCode sql.NullString
		var promoDeletedAt sql.NullTime
		if err := rows.Scan(&o.ID, &o.CustomerID, &o.TenantID, &o.PlanID, &o.Apps, &o.Addons,
			&o.AmountOMR, &o.AmountBaisa, &o.Status, &sessionID, &o.CreatedAt,
			&promoCode, &promoDeletedAt); err != nil {
			return nil, fmt.Errorf("store: scan order: %w", err)
		}
		o.StripeSessionID = sessionID.String
		o.PromoCode = promoCode.String
		o.PromoDeleted = promoDeletedAt.Valid
		orders = append(orders, o)
	}
	if orders == nil {
		orders = []Order{}
	}
	return orders, rows.Err()
}

// ---------------------------------------------------------------------------
// Subscriptions
// ---------------------------------------------------------------------------

// CreateSubscription inserts a new subscription record.
func (s *Store) CreateSubscription(ctx context.Context, sub *Subscription) error {
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO subscriptions (customer_id, tenant_id, stripe_subscription_id, plan_id, status, current_period_start, current_period_end)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, created_at, updated_at`,
		sub.CustomerID, sub.TenantID, nilIfEmpty(sub.StripeSubscriptionID), sub.PlanID, sub.Status,
		nilTimeIfZero(sub.CurrentPeriodStart), nilTimeIfZero(sub.CurrentPeriodEnd),
	).Scan(&sub.ID, &sub.CreatedAt, &sub.UpdatedAt)
	if err != nil {
		return fmt.Errorf("store: create subscription: %w", err)
	}
	return nil
}

// ListActiveSubscriptionsByTenant returns all subscriptions for a tenant that
// are not already in a terminal state (canceled/tenant_deleted). Used by the
// tenant.deleted cascade (issue #94) to find subs that still need Stripe-side
// cancellation.
func (s *Store) ListActiveSubscriptionsByTenant(ctx context.Context, tenantID string) ([]Subscription, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, customer_id, tenant_id, stripe_subscription_id, plan_id, status,
		        current_period_start, current_period_end, created_at, updated_at
		 FROM subscriptions
		 WHERE tenant_id = $1
		   AND status NOT IN ('canceled', 'tenant_deleted')
		 ORDER BY created_at DESC`, tenantID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list subs by tenant %s: %w", tenantID, err)
	}
	defer rows.Close()

	var subs []Subscription
	for rows.Next() {
		var sub Subscription
		var stripeSubID sql.NullString
		var periodStart, periodEnd sql.NullTime
		if err := rows.Scan(&sub.ID, &sub.CustomerID, &sub.TenantID, &stripeSubID, &sub.PlanID, &sub.Status,
			&periodStart, &periodEnd, &sub.CreatedAt, &sub.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: scan subscription: %w", err)
		}
		sub.StripeSubscriptionID = stripeSubID.String
		if periodStart.Valid {
			sub.CurrentPeriodStart = periodStart.Time
		}
		if periodEnd.Valid {
			sub.CurrentPeriodEnd = periodEnd.Time
		}
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}

// VoidOpenInvoicesByTenant marks all draft/open invoices for the tenant as
// 'voided'. Paid/uncollectible invoices are preserved as-is — voiding those
// would misrepresent the audit history. Returns the number of rows updated.
//
// Note the explicit NOT IN filter on terminal statuses: if an invoice is
// already 'voided' we don't bump its updated_at either. See issue #94.
func (s *Store) VoidOpenInvoicesByTenant(ctx context.Context, tenantID string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE invoices SET status = 'voided'
		 WHERE tenant_id = $1
		   AND status IN ('draft', 'open')`, tenantID,
	)
	if err != nil {
		return 0, fmt.Errorf("store: void invoices for tenant %s: %w", tenantID, err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// MarkCreditLedgerTenantDeleted flips every credit-ledger entry belonging to
// the tenant's customers to status='tenant_deleted'. Entries stay in the
// table so financial reporting can still trace historical grants/spends; the
// balance query (GetCreditBalance) continues to sum amount_omr irrespective
// of status, matching the audit-preserve intent of issue #94.
//
// Returns the number of rows updated.
func (s *Store) MarkCreditLedgerTenantDeleted(ctx context.Context, tenantID string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE credit_ledger
		 SET status = 'tenant_deleted'
		 WHERE customer_id IN (SELECT id FROM customers WHERE tenant_id = $1)
		   AND status <> 'tenant_deleted'`, tenantID,
	)
	if err != nil {
		return 0, fmt.Errorf("store: mark credit ledger tenant_deleted for %s: %w", tenantID, err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// GetSubscriptionByTenant returns the most recent subscription for a tenant.
func (s *Store) GetSubscriptionByTenant(ctx context.Context, tenantID string) (*Subscription, error) {
	var sub Subscription
	var stripeSubID sql.NullString
	var periodStart, periodEnd sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT id, customer_id, tenant_id, stripe_subscription_id, plan_id, status,
		        current_period_start, current_period_end, created_at, updated_at
		 FROM subscriptions WHERE tenant_id = $1
		 ORDER BY created_at DESC LIMIT 1`, tenantID,
	).Scan(&sub.ID, &sub.CustomerID, &sub.TenantID, &stripeSubID, &sub.PlanID, &sub.Status,
		&periodStart, &periodEnd, &sub.CreatedAt, &sub.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get subscription by tenant %s: %w", tenantID, err)
	}
	sub.StripeSubscriptionID = stripeSubID.String
	if periodStart.Valid {
		sub.CurrentPeriodStart = periodStart.Time
	}
	if periodEnd.Valid {
		sub.CurrentPeriodEnd = periodEnd.Time
	}
	return &sub, nil
}

// UpdateSubscription updates a subscription by ID with the given fields.
// Supported keys: status, stripe_subscription_id, plan_id, current_period_start, current_period_end.
func (s *Store) UpdateSubscription(ctx context.Context, id string, fields map[string]any) error {
	// Build SET clause dynamically from allowed fields.
	allowed := map[string]bool{
		"status":                  true,
		"stripe_subscription_id":  true,
		"plan_id":                 true,
		"current_period_start":    true,
		"current_period_end":      true,
	}

	setClauses := "updated_at = now()"
	args := []any{}
	argIdx := 1

	for k, v := range fields {
		if !allowed[k] {
			continue
		}
		setClauses += fmt.Sprintf(", %s = $%d", k, argIdx)
		args = append(args, v)
		argIdx++
	}

	args = append(args, id)
	query := fmt.Sprintf("UPDATE subscriptions SET %s WHERE id = $%d", setClauses, argIdx)

	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("store: update subscription %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("store: subscription %s not found", id)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Invoices
// ---------------------------------------------------------------------------

// CreateInvoice inserts a new invoice record.
//
// AmountBaisa is authoritative. AmountOMR is derived as baisa/1000 if not
// explicitly provided, so legacy readers see whole-OMR integers that match
// the previous schema.
func (s *Store) CreateInvoice(ctx context.Context, inv *Invoice) error {
	if inv.Currency == "" {
		inv.Currency = "omr"
	}
	if inv.AmountBaisa == 0 && inv.AmountOMR > 0 {
		inv.AmountBaisa = OMRToBaisa(inv.AmountOMR)
	}
	if inv.AmountOMR == 0 && inv.AmountBaisa > 0 {
		inv.AmountOMR = int(inv.AmountBaisa / 1000)
	}
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO invoices (customer_id, tenant_id, stripe_invoice_id, amount_omr, amount_baisa, currency, status, period_start, period_end, pdf_url)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING id, created_at`,
		inv.CustomerID, inv.TenantID, nilIfEmpty(inv.StripeInvoiceID), inv.AmountOMR, inv.AmountBaisa, inv.Currency, inv.Status,
		nilTimeIfZero(inv.PeriodStart), nilTimeIfZero(inv.PeriodEnd), nilIfEmpty(inv.PDFURL),
	).Scan(&inv.ID, &inv.CreatedAt)
	if err != nil {
		return fmt.Errorf("store: create invoice: %w", err)
	}
	return nil
}

// ListInvoicesByTenant returns all invoices for a tenant, newest first.
func (s *Store) ListInvoicesByTenant(ctx context.Context, tenantID string) ([]Invoice, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, customer_id, tenant_id, stripe_invoice_id, amount_omr, amount_baisa, currency, status,
		        period_start, period_end, pdf_url, created_at
		 FROM invoices WHERE tenant_id = $1
		 ORDER BY created_at DESC`, tenantID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list invoices for tenant %s: %w", tenantID, err)
	}
	defer rows.Close()

	var invoices []Invoice
	for rows.Next() {
		var inv Invoice
		var stripeInvID, pdfURL sql.NullString
		var periodStart, periodEnd sql.NullTime
		if err := rows.Scan(&inv.ID, &inv.CustomerID, &inv.TenantID, &stripeInvID, &inv.AmountOMR, &inv.AmountBaisa, &inv.Currency, &inv.Status,
			&periodStart, &periodEnd, &pdfURL, &inv.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scan invoice: %w", err)
		}
		inv.StripeInvoiceID = stripeInvID.String
		inv.PDFURL = pdfURL.String
		if periodStart.Valid {
			inv.PeriodStart = periodStart.Time
		}
		if periodEnd.Valid {
			inv.PeriodEnd = periodEnd.Time
		}
		invoices = append(invoices, inv)
	}
	if invoices == nil {
		invoices = []Invoice{}
	}
	return invoices, rows.Err()
}

// ---------------------------------------------------------------------------
// Revenue Summary
// ---------------------------------------------------------------------------

// GetRevenueSummary returns aggregate billing metrics.
func (s *Store) GetRevenueSummary(ctx context.Context) (*RevenueSummary, error) {
	var rs RevenueSummary

	// Total MRR: sum of amount_omr from active subscriptions' latest orders.
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(o.amount_omr), 0)
		 FROM subscriptions s
		 JOIN orders o ON o.customer_id = s.customer_id AND o.tenant_id = s.tenant_id
		 WHERE s.status = 'active'`,
	).Scan(&rs.TotalMRR)
	if err != nil {
		return nil, fmt.Errorf("store: revenue summary mrr: %w", err)
	}

	// Total customers.
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM customers`,
	).Scan(&rs.TotalCustomers)
	if err != nil {
		return nil, fmt.Errorf("store: revenue summary customers: %w", err)
	}

	// New customers this month.
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM customers
		 WHERE created_at >= date_trunc('month', now())`,
	).Scan(&rs.NewThisMonth)
	if err != nil {
		return nil, fmt.Errorf("store: revenue summary new this month: %w", err)
	}

	// Active subscriptions.
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM subscriptions WHERE status = 'active'`,
	).Scan(&rs.ActiveSubscriptions)
	if err != nil {
		return nil, fmt.Errorf("store: revenue summary active subs: %w", err)
	}

	return &rs, nil
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

// GetSettings returns the singleton billing settings row.
func (s *Store) GetSettings(ctx context.Context) (*Settings, error) {
	var st Settings
	err := s.db.QueryRowContext(ctx,
		`SELECT stripe_secret_key, stripe_webhook_secret, stripe_public_key, updated_at
		 FROM settings WHERE id = 1`,
	).Scan(&st.StripeSecretKey, &st.StripeWebhookSecret, &st.StripePublicKey, &st.UpdatedAt)
	if err == sql.ErrNoRows {
		return &Settings{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get settings: %w", err)
	}
	return &st, nil
}

// UpdateSettings overwrites the settings row with the given values.
// Empty strings clear the corresponding field.
func (s *Store) UpdateSettings(ctx context.Context, st *Settings) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE settings
		 SET stripe_secret_key = $1,
		     stripe_webhook_secret = $2,
		     stripe_public_key = $3,
		     updated_at = now()
		 WHERE id = 1`,
		st.StripeSecretKey, st.StripeWebhookSecret, st.StripePublicKey,
	)
	if err != nil {
		return fmt.Errorf("store: update settings: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Promo Codes
// ---------------------------------------------------------------------------

// GetPromoCode returns a live promo code by code string, or nil if not found
// OR soft-deleted. Callers that need to audit retired codes must use a
// dedicated admin-scope method (none exists yet — add when the need appears).
// #91: filtering `deleted_at IS NULL` here means every non-admin lookup
// (redemption, checkout UI, public Stripe coupons) ignores tombstones.
func (s *Store) GetPromoCode(ctx context.Context, code string) (*PromoCode, error) {
	var p PromoCode
	var deletedAt sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT code, credit_omr, description, active, max_redemptions, times_redeemed, created_at, deleted_at
		 FROM promo_codes WHERE code = $1 AND deleted_at IS NULL`, code,
	).Scan(&p.Code, &p.CreditOMR, &p.Description, &p.Active, &p.MaxRedemptions, &p.TimesRedeemed, &p.CreatedAt, &deletedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get promo %s: %w", code, err)
	}
	if deletedAt.Valid {
		t := deletedAt.Time
		p.DeletedAt = &t
	}
	return &p, nil
}

// UpsertPromoCode creates or updates a promo code.
//
// #91 — an upsert of an existing (code, deleted_at IS NOT NULL) row
// resurrects the promo by clearing deleted_at. This matches the admin mental
// model: "creating" a code that already exists should be idempotent and
// un-delete the tombstone rather than silently failing because of the PK.
func (s *Store) UpsertPromoCode(ctx context.Context, p *PromoCode) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO promo_codes (code, credit_omr, description, active, max_redemptions)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (code) DO UPDATE
		 SET credit_omr = EXCLUDED.credit_omr,
		     description = EXCLUDED.description,
		     active = EXCLUDED.active,
		     max_redemptions = EXCLUDED.max_redemptions,
		     deleted_at = NULL`,
		p.Code, p.CreditOMR, p.Description, p.Active, p.MaxRedemptions,
	)
	if err != nil {
		return fmt.Errorf("store: upsert promo %s: %w", p.Code, err)
	}
	return nil
}

// ListPromoCodes returns all live promo codes (soft-deleted rows excluded).
// #91 — the admin promo list must mirror what a customer can see during
// checkout; including tombstones here would produce a UI where "delete"
// appears to do nothing.
func (s *Store) ListPromoCodes(ctx context.Context) ([]PromoCode, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT code, credit_omr, description, active, max_redemptions, times_redeemed, created_at, deleted_at
		 FROM promo_codes WHERE deleted_at IS NULL ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list promos: %w", err)
	}
	defer rows.Close()
	var out []PromoCode
	for rows.Next() {
		var p PromoCode
		var deletedAt sql.NullTime
		if err := rows.Scan(&p.Code, &p.CreditOMR, &p.Description, &p.Active, &p.MaxRedemptions, &p.TimesRedeemed, &p.CreatedAt, &deletedAt); err != nil {
			return nil, err
		}
		if deletedAt.Valid {
			t := deletedAt.Time
			p.DeletedAt = &t
		}
		out = append(out, p)
	}
	if out == nil {
		out = []PromoCode{}
	}
	return out, rows.Err()
}

// DeletePromoCode soft-deletes a promo code (#91).
//
// Historically this was a hard DELETE that also purged promo_redemptions
// rows — which destroyed the audit trail of which customers had already
// used the code. Retiring a promo should not retroactively erase customer
// history or break financial reporting.
//
// After this change:
//   - The promo_codes row stays. deleted_at is set to now() and active is
//     flipped to false (defence-in-depth: any code path that checks
//     `active` without checking `deleted_at` still treats it as dead).
//   - promo_redemptions rows are untouched — historical usage remains
//     visible and the FK stays intact.
//   - orders.promo_code values still resolve against this row, which is how
//     the admin order list renders a "deleted" badge on old orders.
//   - A future UpsertPromoCode with the same code will resurrect the row.
//
// Returns sql.ErrNoRows when no matching live code exists — matches the
// previous caller contract so handlers keep returning 404 correctly.
func (s *Store) DeletePromoCode(ctx context.Context, code string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE promo_codes SET deleted_at = now(), active = false
		 WHERE code = $1 AND deleted_at IS NULL`, code,
	)
	if err != nil {
		return fmt.Errorf("store: soft-delete promo %s: %w", code, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: soft-delete promo %s rows affected: %w", code, err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// RedeemPromoCode atomically applies a promo code to a customer, recording
// the redemption and adding credit to the ledger. Returns the credit amount
// granted, or 0 with an error if the code is invalid, inactive, already
// redeemed by this customer, or over its redemption cap.
func (s *Store) RedeemPromoCode(ctx context.Context, customerID, code string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: begin redeem tx: %w", err)
	}
	defer tx.Rollback()

	var credit, maxRedemptions, timesRedeemed int
	var active bool
	var deletedAt sql.NullTime
	err = tx.QueryRowContext(ctx,
		`SELECT credit_omr, active, max_redemptions, times_redeemed, deleted_at
		 FROM promo_codes WHERE code = $1 FOR UPDATE`, code,
	).Scan(&credit, &active, &maxRedemptions, &timesRedeemed, &deletedAt)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("promo code not found")
	}
	if err != nil {
		return 0, fmt.Errorf("store: lock promo: %w", err)
	}
	// #91 — soft-deleted codes are indistinguishable from non-existent ones at
	// the API level. Do not leak the tombstone via a more specific error.
	if deletedAt.Valid {
		return 0, fmt.Errorf("promo code not found")
	}
	if !active {
		return 0, fmt.Errorf("promo code is not active")
	}
	if maxRedemptions > 0 && timesRedeemed >= maxRedemptions {
		return 0, fmt.Errorf("promo code has reached its redemption limit")
	}

	// Check customer has not already redeemed this code.
	var already int
	err = tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM promo_redemptions WHERE customer_id = $1 AND code = $2`,
		customerID, code,
	).Scan(&already)
	if err != nil {
		return 0, fmt.Errorf("store: check redemption: %w", err)
	}
	if already > 0 {
		return 0, fmt.Errorf("promo code already redeemed by this customer")
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO promo_redemptions (customer_id, code) VALUES ($1, $2)`,
		customerID, code,
	); err != nil {
		return 0, fmt.Errorf("store: record redemption: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE promo_codes SET times_redeemed = times_redeemed + 1 WHERE code = $1`,
		code,
	); err != nil {
		return 0, fmt.Errorf("store: increment redemptions: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO credit_ledger (customer_id, amount_omr, reason) VALUES ($1, $2, $3)`,
		customerID, credit, "promo:"+code,
	); err != nil {
		return 0, fmt.Errorf("store: add credit: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: commit redeem: %w", err)
	}
	return credit, nil
}

// ---------------------------------------------------------------------------
// Credit Ledger
// ---------------------------------------------------------------------------

// GetCreditBalance returns a customer's current credit balance in OMR.
func (s *Store) GetCreditBalance(ctx context.Context, customerID string) (int, error) {
	var balance sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount_omr), 0) FROM credit_ledger WHERE customer_id = $1`,
		customerID,
	).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("store: credit balance: %w", err)
	}
	return int(balance.Int64), nil
}

// CreditEntry is a single row of the credit ledger.
type CreditEntry struct {
	ID        string    `json:"id"`
	AmountOMR int       `json:"amount_omr"`
	Reason    string    `json:"reason"`
	OrderID   string    `json:"order_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ListCreditEntries returns the most recent credit ledger entries for a customer.
func (s *Store) ListCreditEntries(ctx context.Context, customerID string, limit int) ([]CreditEntry, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, amount_omr, reason, COALESCE(order_id, ''), created_at
		 FROM credit_ledger WHERE customer_id = $1
		 ORDER BY created_at DESC LIMIT $2`,
		customerID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list credit entries: %w", err)
	}
	defer rows.Close()

	entries := []CreditEntry{}
	for rows.Next() {
		var e CreditEntry
		if err := rows.Scan(&e.ID, &e.AmountOMR, &e.Reason, &e.OrderID, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scan credit entry: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// SpendCredit records a negative credit entry against a customer, tied to an order.
func (s *Store) SpendCredit(ctx context.Context, customerID, orderID string, amountOMR int) error {
	if amountOMR <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO credit_ledger (customer_id, amount_omr, reason, order_id)
		 VALUES ($1, $2, $3, $4)`,
		customerID, -amountOMR, "order-payment", orderID,
	)
	if err != nil {
		return fmt.Errorf("store: spend credit: %w", err)
	}
	return nil
}

// CreditOnlyCheckout atomically performs the three DB writes that a
// credit-settled checkout requires: INSERT orders, INSERT credit_ledger (when
// the order had a non-zero total), INSERT subscriptions — all inside a single
// transaction. Either all three land or none do (#92).
//
// Before this existed, handlers.Checkout called CreateOrder, SpendCredit, and
// CreateSubscription sequentially with no shared tx. If the third write
// failed, the first two had already committed — the customer was charged
// credit for a subscription that never got provisioned. That kind of silent
// drift is unacceptable for billing.
//
// Both `order` and `sub` are populated with the returned IDs + timestamps, so
// the caller can emit the standard "order.completed" + "subscription.created"
// events with real IDs after Commit.
//
// Amounts follow the same convention as CreateOrder: AmountBaisa is
// authoritative, AmountOMR derived. The ledger write uses whole OMR (matching
// SpendCredit) to keep the existing credit_ledger schema stable.
func (s *Store) CreditOnlyCheckout(ctx context.Context, order *Order, sub *Subscription) error {
	if order == nil || sub == nil {
		return fmt.Errorf("store: credit-only checkout: order and subscription are required")
	}
	if order.Apps == nil {
		order.Apps = json.RawMessage(`[]`)
	}
	if order.Addons == nil {
		order.Addons = json.RawMessage(`[]`)
	}
	if order.AmountBaisa == 0 && order.AmountOMR > 0 {
		order.AmountBaisa = OMRToBaisa(order.AmountOMR)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin credit-only tx: %w", err)
	}
	defer tx.Rollback()

	// 1. Persist the order.
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO orders (customer_id, tenant_id, plan_id, apps, addons, amount_omr, amount_baisa, status, stripe_session_id, promo_code)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING id, created_at`,
		order.CustomerID, order.TenantID, order.PlanID, order.Apps, order.Addons,
		order.AmountOMR, order.AmountBaisa, order.Status,
		nilIfEmpty(order.StripeSessionID), nilIfEmpty(order.PromoCode),
	).Scan(&order.ID, &order.CreatedAt); err != nil {
		return fmt.Errorf("store: credit-only create order: %w", err)
	}

	// 2. Record the credit spend against the fresh order. Zero-total orders
	//    (promo fully covers free plan + free addons) skip this step, same as
	//    SpendCredit's own no-op path.
	if order.AmountOMR > 0 {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO credit_ledger (customer_id, amount_omr, reason, order_id)
			 VALUES ($1, $2, $3, $4)`,
			order.CustomerID, -order.AmountOMR, "order-payment", order.ID,
		); err != nil {
			return fmt.Errorf("store: credit-only spend credit: %w", err)
		}
	}

	// 3. Create the subscription. If this fails, the entire tx rolls back —
	//    the customer keeps their credit AND has no phantom order.
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO subscriptions (customer_id, tenant_id, stripe_subscription_id, plan_id, status, current_period_start, current_period_end)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, created_at, updated_at`,
		sub.CustomerID, sub.TenantID, nilIfEmpty(sub.StripeSubscriptionID), sub.PlanID, sub.Status,
		nilTimeIfZero(sub.CurrentPeriodStart), nilTimeIfZero(sub.CurrentPeriodEnd),
	).Scan(&sub.ID, &sub.CreatedAt, &sub.UpdatedAt); err != nil {
		return fmt.Errorf("store: credit-only create subscription: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit credit-only checkout: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Stripe Webhook Idempotency
// ---------------------------------------------------------------------------

// MarkWebhookEventProcessed records that a Stripe webhook event has been
// processed. It returns (true, nil) when the event is new (insert succeeded)
// and (false, nil) when the same event_id was already recorded (conflict).
// Any other error is returned as-is.
//
// Callers MUST call this before performing any side-effecting work for the
// event, and skip the work when the return value is false. This is the #77
// idempotency guard — Stripe retries on non-2xx responses and also on
// transient network failures; a duplicate delivery must NOT re-credit, nor
// create duplicate subscriptions/invoices.
func (s *Store) MarkWebhookEventProcessed(ctx context.Context, eventID, eventType string) (bool, error) {
	if eventID == "" {
		// An event with no ID cannot be deduplicated. Fail closed: the caller
		// should reject rather than silently process, because repeat delivery
		// would double-credit.
		return false, fmt.Errorf("store: webhook event id required for idempotency")
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO stripe_webhook_events (event_id, event_type)
		 VALUES ($1, $2)
		 ON CONFLICT (event_id) DO NOTHING`,
		eventID, eventType,
	)
	if err != nil {
		return false, fmt.Errorf("store: mark webhook event: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: mark webhook event rows: %w", err)
	}
	return n > 0, nil
}

// DeleteWebhookEvent removes the idempotency record for the given event ID.
// It is used when a handler errors AFTER the event was recorded, so that
// Stripe's automatic retry is processed afresh (not short-circuited as a
// duplicate).
func (s *Store) DeleteWebhookEvent(ctx context.Context, eventID string) error {
	if eventID == "" {
		return nil
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM stripe_webhook_events WHERE event_id = $1`, eventID,
	); err != nil {
		return fmt.Errorf("store: delete webhook event %s: %w", eventID, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nilTimeIfZero(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
