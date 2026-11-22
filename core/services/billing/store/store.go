package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	ID         string          `json:"id"`
	CustomerID string          `json:"customer_id"`
	TenantID   string          `json:"tenant_id"`
	PlanID     string          `json:"plan_id"`
	Apps       json.RawMessage `json:"apps"`
	Addons     json.RawMessage `json:"addons"`
	// Topology is the billed BCP topology ("single-region" |
	// "active-hot-standby") — a priced order dimension, persisted so
	// invoices/BSS views show what the surcharge bought (#5104 facet B).
	Topology        string    `json:"topology"`
	AmountOMR       int       `json:"amount_omr"`
	AmountBaisa     int64     `json:"amount_baisa"`
	Status          string    `json:"status"`
	StripeSessionID string    `json:"stripe_session_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`

	// PromoCode is the voucher applied at checkout, if any. Stored in the
	// orders row (not the promo_redemptions table) so the value survives
	// soft-deletion of the promo — admin views need to show which code was
	// used even after the admin retired the promo. See #91.
	PromoCode string `json:"promo_code,omitempty"`
	// PromoDeleted is populated on read by joining promo_codes.deleted_at;
	// the admin UI shows a "deleted" badge next to the code so historical
	// redemptions remain auditable without any stale reference appearing
	// "active". See #91.
	PromoDeleted bool `json:"promo_deleted,omitempty"`

	// #6971 — where the money on this order came from, so the receipt and
	// the later reconciliation with BSS match line by line.
	//
	// PriceSource is "bss:<price_book>@<prices_as_of>" when the order was
	// priced from the Catalyst BSS public packages document (the order
	// carried a package_sku), or "catalog" when it was priced from the
	// catalog service's plan/add-on prices (legacy deck, older clients).
	// PackageSKU is the BSS package the customer chose ("plan.m"); empty on
	// catalog-priced orders. AddonLines is the JSON array of OrderLine — every
	// priced add-on (BSS SKU or catalog id) with its name and amount.
	PriceSource string          `json:"price_source,omitempty"`
	PackageSKU  string          `json:"package_sku,omitempty"`
	AddonLines  json.RawMessage `json:"addon_lines,omitempty"`

	// Overage (founder model 2026-10-10). A package is a prepaid minimum
	// commitment; OverageMode is "capped" (nothing billed beyond the package —
	// the default, and every row that predates the column) or "grow" (the
	// Organization's quota is raised to GrowCeiling and usage above the
	// package allowance is billed in arrears). GrowCeiling is the RESOLVED
	// ceiling as a JSON object {vcpu, memory_gb, disk_gb, bandwidth_mbps},
	// NULL when capped. SpendLimitMonth is the optional monthly overage spend
	// limit as a money string ("25.000"), NULL when unset.
	OverageMode     string          `json:"overage_mode,omitempty"`
	GrowCeiling     json.RawMessage `json:"grow_ceiling,omitempty"`
	SpendLimitMonth string          `json:"spend_limit_month,omitempty"`

	// PrepaidBaisa is what the checkout collected for the PACKAGE and its
	// BSS add-on lines (#6971): the package's price plus the non-redundant
	// addon.* lines, never the topology surcharge or a catalog add-on, and
	// never more than AmountBaisa. 0 on a catalog-priced order and on every
	// row that predates the column. Handed to the Organization as
	// spec.commerce.prepaidAmount; BSS credits it on the first statement.
	PrepaidBaisa int64 `json:"prepaid_baisa,omitempty"`
}

// Overage modes an order carries.
const (
	OverageModeCapped = "capped"
	OverageModeGrow   = "grow"
)

// orderOverageMode defaults an empty OverageMode to capped.
func orderOverageMode(s string) string {
	if s == "" {
		return OverageModeCapped
	}
	return s
}

// orderGrowCeiling turns a scanned grow_ceiling column into the Order field:
// NULL (or JSON null) is no ceiling.
func orderGrowCeiling(b []byte) json.RawMessage {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	return json.RawMessage(append([]byte(nil), b...))
}

// nilIfEmptyJSON maps an empty JSON column value to SQL NULL.
func nilIfEmptyJSON(b json.RawMessage) any {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	return []byte(b)
}

// PriceSourceCatalog is the PriceSource of an order priced from the catalog
// service (no package_sku on the request) — and of every order that predates
// the column.
const PriceSourceCatalog = "catalog"

// OrderLine is one priced add-on on an order, persisted as JSONB in
// orders.addon_lines and returned on the checkout / quote responses.
type OrderLine struct {
	// SKU is the BSS add-on SKU ("addon.backup") or the catalog add-on id.
	SKU  string `json:"sku"`
	Name string `json:"name"`
	// AmountBaisa is the monthly price in baisa (1/1000 OMR).
	AmountBaisa int64 `json:"amount_baisa"`
	// Redundant marks an add-on the chosen package already includes: kept at
	// 0 so the receipt shows what was ticked; never an error.
	Redundant bool `json:"redundant,omitempty"`
}

// orderPriceSource defaults an empty PriceSource to the catalog.
func orderPriceSource(s string) string {
	if s == "" {
		return PriceSourceCatalog
	}
	return s
}

// orderAddonLines defaults nil AddonLines to an empty JSON array.
func orderAddonLines(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage(`[]`)
	}
	return b
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

// MicroOMRPerBaisa is the credit ledger's exact unit (amount_micro_omr) per
// baisa: 1 OMR = 1,000 baisa = 1,000,000 micro-OMR.
const MicroOMRPerBaisa = 1000

// MicroOMRToBaisa converts a micro-OMR amount to whole baisa, rounding toward
// minus infinity, so a balance is never reported as covering more than it
// holds (a sub-baisa metering remainder is not spendable baisa).
func MicroOMRToBaisa(micro int64) int64 {
	return floorDiv(micro, MicroOMRPerBaisa)
}

// MicroOMRToWholeOMR is the whole-OMR display view of a micro-OMR amount,
// rounded toward minus infinity (never shows more credit than is held).
func MicroOMRToWholeOMR(micro int64) int {
	return int(floorDiv(micro, 1_000_000))
}

// floorDiv divides rounding toward minus infinity (Go's / truncates toward
// zero, which would round a negative balance UP).
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// ErrCreditInsufficient is CreditOnlyCheckout's answer when, under the
// customer's lock, the balance no longer covers the order: another checkout
// spent the same credit first. Nothing was written.
var ErrCreditInsufficient = errors.New("store: credit balance no longer covers the order")

// ErrPromoCreditSpent is RollbackPromoCodeRedemption's answer when taking the
// voucher's grant back would leave an order paid with credit that no longer
// exists: the orders paid since the grant took more than the credit there was
// before it (grantSpentByOrders), so the redemption stands. Debt from before
// the grant, or from metering, never triggers it. Nothing was changed.
var ErrPromoCreditSpent = errors.New("store: the voucher's credit has already been spent by an order")

// promoCodeMatch is the ONE rule a typed voucher code is matched by, at the
// public preview (GetPromoCode) and at redemption (RedeemPromoCode) alike:
// letter case is ignored, and when two stored codes differ only in case the
// live, exactly-typed one wins. Issue stores codes upper-cased; the legacy
// admin upsert stores them as typed — both are found the same way everywhere.
const promoCodeMatch = `upper(code) = upper($1)`

// promoCodePreference orders the rows promoCodeMatch selects (see there).
const promoCodePreference = `ORDER BY (deleted_at IS NULL) DESC, (code = $1) DESC LIMIT 1`

// lockCustomer takes the row lock that serialises every spend and every
// voucher take-back of one customer's credit (one ledger per customer, shared
// by all of the customer's Organizations). Held until tx ends. NO KEY UPDATE,
// not UPDATE: it still excludes every other spend, but not the KEY SHARE lock
// a foreign-key insert against the customer takes (a redeem's ledger grant),
// so it can neither block nor deadlock with one.
func lockCustomer(ctx context.Context, tx *sql.Tx, customerID string) error {
	var id string
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM customers WHERE id = $1 FOR NO KEY UPDATE`, customerID,
	).Scan(&id); err != nil {
		return fmt.Errorf("store: lock customer %s: %w", customerID, err)
	}
	return nil
}

// creditBalanceMicroTx is GetCreditBalanceMicroOMR read inside tx (after
// lockCustomer, so no other spend of this customer can interleave).
func creditBalanceMicroTx(ctx context.Context, tx *sql.Tx, customerID string) (int64, error) {
	var balance sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(CAST(SUM(amount_omr) AS BIGINT) * 1000000
		                + CAST(SUM(amount_micro_omr) AS BIGINT), 0)
		 FROM credit_ledger WHERE customer_id = $1`,
		customerID,
	).Scan(&balance); err != nil {
		return 0, fmt.Errorf("store: credit balance micro (locked): %w", err)
	}
	return balance.Int64, nil
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
	Code        string `json:"code"`
	CreditOMR   int    `json:"credit_omr"`
	Description string `json:"description"`
	// PlanTier is the catalog plan slug (e.g. "s"/"m"/"l"/"xl" — see
	// core/services/catalog store.Plan.Slug) this voucher targets.
	// #5223 (UAT row 72): surfaced on the BSS Vouchers issuance form +
	// list. Empty string = credit-only voucher, usable with any plan.
	// Informational for the operator today — checkout does NOT enforce
	// plan matching (the granted credit is fungible at checkout).
	PlanTier       string     `json:"plan_tier,omitempty"`
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
		`ALTER TABLE orders ADD COLUMN IF NOT EXISTS topology TEXT NOT NULL DEFAULT 'single-region'`,
		// #6971 — price provenance. Every row that predates these columns was
		// priced from the catalog, so the default is the truthful backfill.
		`ALTER TABLE orders ADD COLUMN IF NOT EXISTS price_source TEXT NOT NULL DEFAULT 'catalog'`,
		`ALTER TABLE orders ADD COLUMN IF NOT EXISTS package_sku TEXT`,
		`ALTER TABLE orders ADD COLUMN IF NOT EXISTS addon_lines JSONB NOT NULL DEFAULT '[]'`,
		// Overage mode (founder model 2026-10-10). Every row that predates the
		// column was sold capped, so the default is the truthful backfill; the
		// grow ceiling and the spend limit are NULL unless the order set them.
		`ALTER TABLE orders ADD COLUMN IF NOT EXISTS overage_mode TEXT NOT NULL DEFAULT 'capped'`,
		`ALTER TABLE orders ADD COLUMN IF NOT EXISTS grow_ceiling JSONB`,
		`ALTER TABLE orders ADD COLUMN IF NOT EXISTS spend_limit_month TEXT`,
		// #6971 — what the checkout collected for the package and its BSS
		// add-ons, handed over so BSS credits it on the first statement.
		// Every older row collected nothing BSS can credit: 0 is truthful.
		`ALTER TABLE orders ADD COLUMN IF NOT EXISTS prepaid_baisa BIGINT NOT NULL DEFAULT 0`,
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
		// #5223 (UAT row 72) — plan-tier targeting. The BSS Vouchers
		// issuance form carries a plan tier (catalog plan slug); persisted
		// here so the voucher list can render it. Empty = credit-only.
		`ALTER TABLE promo_codes ADD COLUMN IF NOT EXISTS plan_tier TEXT NOT NULL DEFAULT ''`,
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
		// #798 — NewAPI metering integration. Per-LLM-request charges land
		// here at sub-baisa precision. amount_micro_omr is the canonical
		// signed amount in micro-OMR (1 OMR = 1,000,000 micro-OMR; 1 baisa
		// = 1,000 micro-OMR). Existing whole-OMR rows continue to carry 0
		// in this column and are summed via amount_omr * 1,000,000 in
		// GetCreditBalanceMicroOMR. external_ref is the deduplication key
		// (NewAPI request_id) — UNIQUE so at-least-once redelivery from
		// the NATS metering subscriber AND a retry of the synchronous
		// HTTP handler both converge to a single ledger row. metadata
		// JSONB carries the full envelope (model, tokens_used, latency_ms,
		// tenant_id, completed_at) for downstream analytics without
		// requiring a separate audit table. Per ADR-0001 §6 + #795
		// [Q-mine-4], this ledger is authoritative; NewAPI's own ledger is
		// an in-flight cache only.
		`ALTER TABLE credit_ledger ADD COLUMN IF NOT EXISTS amount_micro_omr BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE credit_ledger ADD COLUMN IF NOT EXISTS external_ref TEXT`,
		`ALTER TABLE credit_ledger ADD COLUMN IF NOT EXISTS metadata JSONB`,
		`CREATE UNIQUE INDEX IF NOT EXISTS credit_ledger_external_ref_uniq
		   ON credit_ledger (external_ref)
		   WHERE external_ref IS NOT NULL`,
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
	o.PriceSource = orderPriceSource(o.PriceSource)
	o.AddonLines = orderAddonLines(o.AddonLines)
	o.OverageMode = orderOverageMode(o.OverageMode)
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO orders (customer_id, tenant_id, plan_id, apps, addons, topology, amount_omr, amount_baisa, status, stripe_session_id, promo_code, package_sku, price_source, addon_lines, overage_mode, grow_ceiling, spend_limit_month, prepaid_baisa)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		 RETURNING id, created_at`,
		o.CustomerID, o.TenantID, o.PlanID, o.Apps, o.Addons, orderTopology(o.Topology), o.AmountOMR, o.AmountBaisa, o.Status, nilIfEmpty(o.StripeSessionID), nilIfEmpty(o.PromoCode),
		nilIfEmpty(o.PackageSKU), o.PriceSource, o.AddonLines,
		o.OverageMode, nilIfEmptyJSON(o.GrowCeiling), nilIfEmpty(o.SpendLimitMonth), o.PrepaidBaisa,
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
	var packageSKU, spendLimit sql.NullString
	var growCeiling []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT o.id, o.customer_id, o.tenant_id, o.plan_id, o.apps, o.addons, o.topology,
		        o.amount_omr, o.amount_baisa, o.status, o.stripe_session_id, o.created_at,
		        o.promo_code, pc.deleted_at, o.package_sku, o.price_source, o.addon_lines,
		        o.overage_mode, o.grow_ceiling, o.spend_limit_month, o.prepaid_baisa
		   FROM orders o
		   LEFT JOIN promo_codes pc ON pc.code = o.promo_code
		  WHERE o.id = $1`, id,
	).Scan(&o.ID, &o.CustomerID, &o.TenantID, &o.PlanID, &o.Apps, &o.Addons, &o.Topology,
		&o.AmountOMR, &o.AmountBaisa, &o.Status, &sessionID, &o.CreatedAt,
		&promoCode, &promoDeletedAt, &packageSKU, &o.PriceSource, &o.AddonLines,
		&o.OverageMode, &growCeiling, &spendLimit, &o.PrepaidBaisa)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get order %s: %w", id, err)
	}
	o.StripeSessionID = sessionID.String
	o.PromoCode = promoCode.String
	o.PromoDeleted = promoDeletedAt.Valid
	o.PackageSKU = packageSKU.String
	o.SpendLimitMonth = spendLimit.String
	o.GrowCeiling = orderGrowCeiling(growCeiling)
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
		`SELECT o.id, o.customer_id, o.tenant_id, o.plan_id, o.apps, o.addons, o.topology,
		        o.amount_omr, o.amount_baisa, o.status, o.stripe_session_id, o.created_at,
		        o.promo_code, pc.deleted_at, o.package_sku, o.price_source, o.addon_lines,
		        o.overage_mode, o.grow_ceiling, o.spend_limit_month, o.prepaid_baisa
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
		var packageSKU, spendLimit sql.NullString
		var growCeiling []byte
		if err := rows.Scan(&o.ID, &o.CustomerID, &o.TenantID, &o.PlanID, &o.Apps, &o.Addons, &o.Topology,
			&o.AmountOMR, &o.AmountBaisa, &o.Status, &sessionID, &o.CreatedAt,
			&promoCode, &promoDeletedAt, &packageSKU, &o.PriceSource, &o.AddonLines,
			&o.OverageMode, &growCeiling, &spendLimit, &o.PrepaidBaisa); err != nil {
			return nil, fmt.Errorf("store: scan order: %w", err)
		}
		o.StripeSessionID = sessionID.String
		o.PromoCode = promoCode.String
		o.PromoDeleted = promoDeletedAt.Valid
		o.PackageSKU = packageSKU.String
		o.SpendLimitMonth = spendLimit.String
		o.GrowCeiling = orderGrowCeiling(growCeiling)
		orders = append(orders, o)
	}
	if orders == nil {
		orders = []Order{}
	}
	return orders, rows.Err()
}

// ListOrdersAwaitingLaunch returns SETTLED orders that carry a tenant, newest
// first, created within `lookback` of now.
//
// #6242. This is the durable record the settlement-launch reconciler sweeps.
// The `orders` row is written inside the same transaction that spends the
// customer's credit (CreditOnlyCheckout) or is flipped by the Stripe webhook,
// so a row in `completed` is proof the customer PAID. The launch that turns
// that payment into a running Organization is a single fire-and-forget HTTP
// call (handlers.go launchTenant), and losing it stranded the paid Org at
// `pending_payment` with nothing to retry it.
//
// Deliberately NOT joined against tenant status: billing owns no tenant table,
// and asking the tenant service per row would trade one lost call for N. The
// launch endpoint is itself the authority — it CAS-transitions only from
// `pending_payment` and answers `launched:false` for everything else — so
// re-offering an already-launched order is a cheap no-op, and a row that
// SHOULD launch can never be filtered out here by a stale second opinion.
//
// The lookback bounds the sweep: an order old enough that the customer has
// given up is a support case, not something to silently provision weeks later.
func (s *Store) ListOrdersAwaitingLaunch(ctx context.Context, lookback time.Duration, limit int) ([]Order, error) {
	if limit <= 0 {
		limit = 100
	}
	cutoff := time.Now().Add(-lookback)
	rows, err := s.db.QueryContext(ctx,
		`SELECT o.id, o.customer_id, o.tenant_id, o.plan_id, o.apps, o.addons, o.topology,
		        o.amount_omr, o.amount_baisa, o.status, o.stripe_session_id, o.created_at,
		        o.promo_code, pc.deleted_at, o.package_sku, o.price_source, o.addon_lines,
		        o.overage_mode, o.grow_ceiling, o.spend_limit_month, o.prepaid_baisa
		   FROM orders o
		   LEFT JOIN promo_codes pc ON pc.code = o.promo_code
		  WHERE o.status = 'completed'
		    AND o.tenant_id IS NOT NULL
		    AND o.tenant_id <> ''
		    AND o.created_at >= $1
		  ORDER BY o.created_at DESC LIMIT $2`, cutoff, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list orders awaiting launch: %w", err)
	}
	defer rows.Close()

	var orders []Order
	for rows.Next() {
		var o Order
		var sessionID sql.NullString
		var promoCode sql.NullString
		var promoDeletedAt sql.NullTime
		var packageSKU, spendLimit sql.NullString
		var growCeiling []byte
		if err := rows.Scan(&o.ID, &o.CustomerID, &o.TenantID, &o.PlanID, &o.Apps, &o.Addons, &o.Topology,
			&o.AmountOMR, &o.AmountBaisa, &o.Status, &sessionID, &o.CreatedAt,
			&promoCode, &promoDeletedAt, &packageSKU, &o.PriceSource, &o.AddonLines,
			&o.OverageMode, &growCeiling, &spendLimit, &o.PrepaidBaisa); err != nil {
			return nil, fmt.Errorf("store: scan order awaiting launch: %w", err)
		}
		o.StripeSessionID = sessionID.String
		o.PromoCode = promoCode.String
		o.PromoDeleted = promoDeletedAt.Valid
		o.PackageSKU = packageSKU.String
		o.SpendLimitMonth = spendLimit.String
		o.GrowCeiling = orderGrowCeiling(growCeiling)
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
		"status":                 true,
		"stripe_subscription_id": true,
		"plan_id":                true,
		"current_period_start":   true,
		"current_period_end":     true,
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
// The code is matched by promoCodeMatch — the same rule RedeemPromoCode
// applies — so the preview never promises a voucher the checkout refuses.
func (s *Store) GetPromoCode(ctx context.Context, code string) (*PromoCode, error) {
	var p PromoCode
	var deletedAt sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT code, credit_omr, description, plan_tier, active, max_redemptions, times_redeemed, created_at, deleted_at
		 FROM promo_codes WHERE `+promoCodeMatch+` AND deleted_at IS NULL `+promoCodePreference, code,
	).Scan(&p.Code, &p.CreditOMR, &p.Description, &p.PlanTier, &p.Active, &p.MaxRedemptions, &p.TimesRedeemed, &p.CreatedAt, &deletedAt)
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
		`INSERT INTO promo_codes (code, credit_omr, description, plan_tier, active, max_redemptions)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (code) DO UPDATE
		 SET credit_omr = EXCLUDED.credit_omr,
		     description = EXCLUDED.description,
		     plan_tier = EXCLUDED.plan_tier,
		     active = EXCLUDED.active,
		     max_redemptions = EXCLUDED.max_redemptions,
		     deleted_at = NULL`,
		p.Code, p.CreditOMR, p.Description, p.PlanTier, p.Active, p.MaxRedemptions,
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
		`SELECT code, credit_omr, description, plan_tier, active, max_redemptions, times_redeemed, created_at, deleted_at
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
		if err := rows.Scan(&p.Code, &p.CreditOMR, &p.Description, &p.PlanTier, &p.Active, &p.MaxRedemptions, &p.TimesRedeemed, &p.CreatedAt, &deletedAt); err != nil {
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
// granted and the code as STORED (the typed code is matched by
// promoCodeMatch, so 'vch-abc' redeems 'VCH-ABC'; every later reference — the
// rollback, the order row — must use the stored form). Returns 0 with an
// error if the code is invalid, inactive, already redeemed by this customer,
// or over its redemption cap.
func (s *Store) RedeemPromoCode(ctx context.Context, customerID, typed string) (int, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, "", fmt.Errorf("store: begin redeem tx: %w", err)
	}
	defer tx.Rollback()

	var code string
	var credit, maxRedemptions, timesRedeemed int
	var active bool
	var deletedAt sql.NullTime
	err = tx.QueryRowContext(ctx,
		`SELECT code, credit_omr, active, max_redemptions, times_redeemed, deleted_at
		 FROM promo_codes WHERE `+promoCodeMatch+` `+promoCodePreference+` FOR UPDATE`, typed,
	).Scan(&code, &credit, &active, &maxRedemptions, &timesRedeemed, &deletedAt)
	if err == sql.ErrNoRows {
		return 0, "", fmt.Errorf("promo code not found")
	}
	if err != nil {
		return 0, "", fmt.Errorf("store: lock promo: %w", err)
	}
	// #91 — soft-deleted codes are indistinguishable from non-existent ones at
	// the API level. Do not leak the tombstone via a more specific error.
	if deletedAt.Valid {
		return 0, "", fmt.Errorf("promo code not found")
	}
	if !active {
		return 0, "", fmt.Errorf("promo code is not active")
	}
	if maxRedemptions > 0 && timesRedeemed >= maxRedemptions {
		return 0, "", fmt.Errorf("promo code has reached its redemption limit")
	}

	// Check customer has not already redeemed this code.
	var already int
	err = tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM promo_redemptions WHERE customer_id = $1 AND code = $2`,
		customerID, code,
	).Scan(&already)
	if err != nil {
		return 0, "", fmt.Errorf("store: check redemption: %w", err)
	}
	if already > 0 {
		return 0, "", fmt.Errorf("promo code already redeemed by this customer")
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO promo_redemptions (customer_id, code) VALUES ($1, $2)`,
		customerID, code,
	); err != nil {
		return 0, "", fmt.Errorf("store: record redemption: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE promo_codes SET times_redeemed = times_redeemed + 1 WHERE code = $1`,
		code,
	); err != nil {
		return 0, "", fmt.Errorf("store: increment redemptions: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO credit_ledger (customer_id, amount_omr, reason) VALUES ($1, $2, $3)`,
		customerID, credit, "promo:"+code,
	); err != nil {
		return 0, "", fmt.Errorf("store: add credit: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, "", fmt.Errorf("store: commit redeem: %w", err)
	}
	return credit, code, nil
}

// RollbackPromoCodeRedemption is the compensating action for RedeemPromoCode.
// TBD-V9 (#2000): Checkout commits the voucher redemption in its own
// transaction up front (needed for the FOR UPDATE concurrency cap), then runs
// downstream steps (Stripe-settings probe, order INSERT, Stripe session
// creation). If any downstream step fails — most importantly the
// "payment processor not configured" 503 — the voucher counter would
// previously stay advanced even though no order was placed. The customer's
// "one-per-customer" promo would be silently burned with nothing to show for
// it; the admin's redemption cap accounting would diverge from reality.
//
// This rollback undoes all three side-effects of RedeemPromoCode in a single
// transaction:
//   - decrement promo_codes.times_redeemed (guarded against underflow)
//   - delete the promo_redemptions row for this (customer, code) pair
//   - delete the credit_ledger row written by the redeem (reason='promo:<code>',
//     order_id IS NULL — the redeem ledger row never references an order)
//
// Idempotency (TBD-V9): callers safely invoke this on every Checkout failure
// branch without first checking whether RedeemPromoCode actually ran. If
// nothing was redeemed (or the rollback already executed), the SQL DELETE +
// conditional UPDATE no-op and the function returns nil. Re-running a failed
// order can never double-decrement.
//
// The compensating tx is best-effort from the caller's perspective: if it
// fails the original Checkout error (503 etc.) is the one returned to the
// client. We log the rollback failure with WARN-level — it's the only signal
// an operator has that manual reconciliation may be needed for that voucher.
func (s *Store) RollbackPromoCodeRedemption(ctx context.Context, customerID, code string) error {
	if customerID == "" || code == "" {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin rollback tx: %w", err)
	}
	defer tx.Rollback()

	// 1. Delete the redemption row first. If it doesn't exist (idempotent
	//    re-entry), we have nothing to undo — short-circuit so a stray
	//    rollback never decrements times_redeemed without a matching
	//    redemption.
	res, err := tx.ExecContext(ctx,
		`DELETE FROM promo_redemptions WHERE customer_id = $1 AND code = $2`,
		customerID, code,
	)
	if err != nil {
		return fmt.Errorf("store: rollback delete redemption: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: rollback redemption rows-affected: %w", err)
	}
	if n == 0 {
		// No redemption row → nothing to roll back. Idempotent no-op.
		return tx.Commit()
	}

	// 1b. Take the grant back only if no order spent it. The redeem commits
	//     before the checkout settles, so a concurrent checkout of the same
	//     customer can already have spent this credit; deleting the grant then
	//     would leave that order paid with credit that no longer exists. Under
	//     the customer lock — the one CreditOnlyCheckout takes — no order can
	//     be paid while we decide. If an order spent it the redemption stands,
	//     untouched.
	//
	//     "Spent" is decided on the ledger around the grant, NOT on the
	//     balance: a balance below the grant is also what a customer in
	//     post-paid metering debt has (RecordUsage permits a negative
	//     balance), and refusing then would burn the voucher with no order
	//     placed (TBD-V9 / #2000).
	if err := lockCustomer(ctx, tx, customerID); err != nil {
		return err
	}
	var before, after int64
	if err := tx.QueryRowContext(ctx, grantSurroundingsQuery,
		customerID, "promo:"+code,
	).Scan(&before, &after); err != nil {
		return fmt.Errorf("store: rollback read grant: %w", err)
	}
	if grantSpentByOrders(before, after) {
		return ErrPromoCreditSpent
	}

	// 2. Decrement times_redeemed. The GREATEST(...,0) guard ensures we never
	//    drive the counter negative in the event of a race where a parallel
	//    rollback already ran.
	if _, err := tx.ExecContext(ctx,
		`UPDATE promo_codes
		   SET times_redeemed = GREATEST(times_redeemed - 1, 0)
		 WHERE code = $1`,
		code,
	); err != nil {
		return fmt.Errorf("store: rollback decrement times_redeemed: %w", err)
	}

	// 3. Remove the credit ledger entry written by the redeem. The redeem
	//    ledger row is the one with reason='promo:<code>' and a NULL
	//    order_id; any spend rows tied to actual orders have order_id set
	//    and are NOT touched by this DELETE.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM credit_ledger
		  WHERE customer_id = $1
		    AND reason = $2
		    AND order_id IS NULL`,
		customerID, "promo:"+code,
	); err != nil {
		return fmt.Errorf("store: rollback delete credit: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit rollback: %w", err)
	}
	return nil
}

// grantSurroundingsQuery reads, in micro-OMR, the two sums that decide whether
// a voucher grant ($2 = 'promo:<code>', order_id IS NULL) was spent by an
// order:
//
//   - before: every ledger row of the customer created before the grant —
//     the balance the customer stood at when the voucher was redeemed (debt
//     included);
//   - after: every row created at or after the grant, except the grant itself
//     and metering rows (RecordUsage is the only writer of external_ref):
//     the orders paid since, and any credit granted since.
//
// Metering is left out of "after" because it is post-paid: usage after the
// grant may take the balance below zero with or without the voucher, so it
// is not an order spending the grant. Order-payment rows are stamped with
// clock_timestamp() (CreditOnlyCheckout), which is after the balance read
// that saw the grant, so an order that spent the grant is always "after" it.
const grantSurroundingsQuery = `WITH g AS (
	SELECT MIN(created_at) AS at FROM credit_ledger
	 WHERE customer_id = $1 AND reason = $2 AND order_id IS NULL
)
SELECT
	COALESCE(CAST(SUM(CASE WHEN l.created_at < g.at
		THEN CAST(l.amount_omr AS BIGINT) * 1000000 + l.amount_micro_omr END) AS BIGINT), 0),
	COALESCE(CAST(SUM(CASE WHEN l.created_at >= g.at AND l.external_ref IS NULL
		AND NOT (l.reason = $2 AND l.order_id IS NULL)
		THEN CAST(l.amount_omr AS BIGINT) * 1000000 + l.amount_micro_omr END) AS BIGINT), 0)
  FROM credit_ledger l CROSS JOIN g
 WHERE l.customer_id = $1`

// grantSpentByOrders reports whether the orders paid since a grant took more
// than the credit the customer had before it — i.e. some of that spending
// could only have come from the grant. before is the balance before the grant
// (a debt counts as no credit, not as spending); after is the non-metering
// movement since, without the grant (see grantSurroundingsQuery).
func grantSpentByOrders(before, after int64) bool {
	available := before
	if available < 0 {
		available = 0
	}
	return available+after < 0
}

// ---------------------------------------------------------------------------
// Credit Ledger
// ---------------------------------------------------------------------------

// GetCreditBalance returns a customer's current credit balance in WHOLE OMR,
// rounded toward minus infinity — a display view only. No money decision is
// made on it: checkout settles against GetCreditBalanceMicroOMR, because a
// credit-paid order is debited exactly (amount_micro_omr) and 2.510 OMR of
// credit must cover a 2.490 order.
//
// #798 — sub-baisa metering rows store their value in amount_micro_omr
// (1 OMR = 1,000,000 micro-OMR); this view is the floor of the exact sum of
// both columns. (It used to add CAST(SUM(micro)/1e6 AS BIGINT), which
// Postgres ROUNDS, so a 2.490 debit read as 2 and the view overstated the
// balance.)
func (s *Store) GetCreditBalance(ctx context.Context, customerID string) (int, error) {
	micro, err := s.GetCreditBalanceMicroOMR(ctx, customerID)
	if err != nil {
		return 0, fmt.Errorf("store: credit balance: %w", err)
	}
	return MicroOMRToWholeOMR(micro), nil
}

// GetCreditBalanceMicroOMR returns the canonical balance in micro-OMR
// (1 OMR = 1,000,000 micro-OMR). Per #798, NewAPI metering writes
// fractional-baisa amounts into amount_micro_omr; the legacy whole-OMR
// rows are projected upward via amount_omr * 1,000,000. The sum is what
// the synchronous HTTP handler returns as `balance_after_micro_omr` so
// Organization-admin pre-flight checks have full precision. Negative balances are
// permitted (post-paid Organization tier may be charged before top-up).
//
// CAST(... AS BIGINT) — see GetCreditBalance comment.
func (s *Store) GetCreditBalanceMicroOMR(ctx context.Context, customerID string) (int64, error) {
	var balance sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(CAST(SUM(amount_omr) AS BIGINT) * 1000000
		                + CAST(SUM(amount_micro_omr) AS BIGINT), 0)
		 FROM credit_ledger WHERE customer_id = $1`,
		customerID,
	).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("store: credit balance micro: %w", err)
	}
	return balance.Int64, nil
}

// CreditEntry is a single row of the credit ledger.
type CreditEntry struct {
	ID        string `json:"id"`
	AmountOMR int    `json:"amount_omr"`
	// AmountMicroOMR is the row's exact part (amount_micro_omr): a
	// credit-paid order's debit and metering usage live here, not in
	// AmountOMR. The row's value is AmountOMR×1,000,000 + AmountMicroOMR.
	AmountMicroOMR int64     `json:"amount_micro_omr"`
	Reason         string    `json:"reason"`
	OrderID        string    `json:"order_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// AmountBaisa is the row's exact value in baisa (both columns).
func (e CreditEntry) AmountBaisa() int64 {
	return MicroOMRToBaisa(int64(e.AmountOMR)*1_000_000 + e.AmountMicroOMR)
}

// ListCreditEntries returns the most recent credit ledger entries for a customer.
func (s *Store) ListCreditEntries(ctx context.Context, customerID string, limit int) ([]CreditEntry, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, amount_omr, amount_micro_omr, reason, COALESCE(order_id, ''), created_at
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
		if err := rows.Scan(&e.ID, &e.AmountOMR, &e.AmountMicroOMR, &e.Reason, &e.OrderID, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scan credit entry: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// UsageEntry captures one ledger insert produced by NewAPI metering. Per
// #798 + ADR-0001 §6, the sub-baisa amount is stored in amount_micro_omr
// (1 OMR = 1,000,000 micro-OMR; -0.000234 OMR = -234 micro-OMR — exact),
// the reason follows the convention `usage:newapi:<model>`, the
// external_ref is the NewAPI request_id (idempotency key), and metadata
// is the raw envelope (model, tokens_used, tenant_id, latency_ms,
// completed_at) preserved for downstream analytics.
type UsageEntry struct {
	CustomerID     string
	AmountMicroOMR int64
	Reason         string
	ExternalRef    string
	Metadata       json.RawMessage
}

// RecordUsage inserts a metering ledger row and returns the new balance
// in micro-OMR. Idempotent on ExternalRef — a duplicate key returns the
// existing row's id and the unchanged balance, NOT an error. This is the
// at-least-once contract for both the NATS subscriber and the synchronous
// HTTP handler: a redelivered envelope with the same request_id collapses
// into a single ledger row.
//
// AmountMicroOMR MUST be negative for usage spend (the convention matches
// SpendCredit's negative-on-spend posture). A zero amount short-circuits
// to a no-op for free-tier callouts (some upstreams emit usage events
// with zero tokens on health-checks).
//
// Returns:
//   - ledgerID: id of the new (or pre-existing-on-duplicate) row
//   - balanceAfterMicroOMR: post-insert balance for the caller to surface
//   - duplicate: true when the row already existed (ExternalRef collision)
func (s *Store) RecordUsage(ctx context.Context, entry UsageEntry) (ledgerID string, balanceAfterMicroOMR int64, duplicate bool, err error) {
	if entry.CustomerID == "" {
		return "", 0, false, fmt.Errorf("store: record usage: customer_id required")
	}
	if entry.ExternalRef == "" {
		return "", 0, false, fmt.Errorf("store: record usage: external_ref required for idempotency")
	}
	if entry.Reason == "" {
		return "", 0, false, fmt.Errorf("store: record usage: reason required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, false, fmt.Errorf("store: record usage: begin tx: %w", err)
	}
	defer tx.Rollback()

	// Insert with ON CONFLICT on the partial unique index over external_ref.
	// The DO UPDATE … SET external_ref = EXCLUDED.external_ref is a no-op
	// that lets us use RETURNING uniformly: a fresh INSERT and a duplicate
	// hit both produce the row's id, and the (xmax = 0) trick distinguishes
	// the two paths so callers can surface "duplicate" without a follow-up
	// SELECT.
	var metadata any = entry.Metadata
	if len(entry.Metadata) == 0 {
		metadata = nil
	}
	var existed bool
	err = tx.QueryRowContext(ctx,
		`INSERT INTO credit_ledger (customer_id, amount_omr, amount_micro_omr, reason, external_ref, metadata)
		 VALUES ($1, 0, $2, $3, $4, $5)
		 ON CONFLICT (external_ref)
		   WHERE external_ref IS NOT NULL
		 DO UPDATE SET external_ref = EXCLUDED.external_ref
		 RETURNING id, (xmax <> 0) AS existed`,
		entry.CustomerID, entry.AmountMicroOMR, entry.Reason, entry.ExternalRef, metadata,
	).Scan(&ledgerID, &existed)
	if err != nil {
		return "", 0, false, fmt.Errorf("store: record usage: insert: %w", err)
	}

	// Sum the running balance INSIDE the same tx so concurrent writers see
	// a consistent snapshot. Negative balances are permitted (post-paid Organization
	// tier; balance enforcement is a policy decision at a higher layer).
	// CAST(... AS BIGINT) so lib/pq scans a Go int64 instead of a numeric
	// string — see GetCreditBalanceMicroOMR.
	var bal sql.NullInt64
	err = tx.QueryRowContext(ctx,
		`SELECT COALESCE(CAST(SUM(amount_omr) AS BIGINT) * 1000000
		                + CAST(SUM(amount_micro_omr) AS BIGINT), 0)
		 FROM credit_ledger WHERE customer_id = $1`,
		entry.CustomerID,
	).Scan(&bal)
	if err != nil {
		return "", 0, false, fmt.Errorf("store: record usage: balance: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", 0, false, fmt.Errorf("store: record usage: commit: %w", err)
	}
	return ledgerID, bal.Int64, existed, nil
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

// CreditOnlyCheckout atomically performs the writes that a credit-settled
// checkout requires: INSERT orders, INSERT credit_ledger (when the order had a
// non-zero total), INSERT subscriptions — all inside a single transaction.
// Either all land or none do (#92).
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
// The debit is EXACT: -AmountBaisa, written to amount_micro_omr. It used to
// be -AmountOMR, the total rounded UP to whole OMR, so a 2.490 order took
// 3.000 of credit while the order, the receipt and the BSS prepaid amount
// said 2.490.
//
// The spend is made under the customer's row lock, and the balance is read
// again under it: a checkout that read the balance before another checkout
// spent the same credit gets ErrCreditInsufficient and writes nothing, so one
// credit can never pay two orders. Returns the balance after the debit, in
// micro-OMR.
func (s *Store) CreditOnlyCheckout(ctx context.Context, order *Order, sub *Subscription) (int64, error) {
	if order == nil || sub == nil {
		return 0, fmt.Errorf("store: credit-only checkout: order and subscription are required")
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
		return 0, fmt.Errorf("store: begin credit-only tx: %w", err)
	}
	defer tx.Rollback()

	// 0. Serialise against every other spend of this customer's credit, then
	//    decide on the balance as it is NOW, not as the caller read it.
	if err := lockCustomer(ctx, tx, order.CustomerID); err != nil {
		return 0, err
	}
	balance, err := creditBalanceMicroTx(ctx, tx, order.CustomerID)
	if err != nil {
		return 0, err
	}
	debit := order.AmountBaisa * MicroOMRPerBaisa
	if debit > 0 && balance < debit {
		return 0, ErrCreditInsufficient
	}

	order.PriceSource = orderPriceSource(order.PriceSource)
	order.AddonLines = orderAddonLines(order.AddonLines)

	// 1. Persist the order.
	order.PriceSource = orderPriceSource(order.PriceSource)
	order.OverageMode = orderOverageMode(order.OverageMode)
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO orders (customer_id, tenant_id, plan_id, apps, addons, topology, amount_omr, amount_baisa, status, stripe_session_id, promo_code, package_sku, price_source, addon_lines, overage_mode, grow_ceiling, spend_limit_month, prepaid_baisa)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		 RETURNING id, created_at`,
		order.CustomerID, order.TenantID, order.PlanID, order.Apps, order.Addons,
		orderTopology(order.Topology), order.AmountOMR, order.AmountBaisa, order.Status,
		nilIfEmpty(order.StripeSessionID), nilIfEmpty(order.PromoCode),
		nilIfEmpty(order.PackageSKU), order.PriceSource, order.AddonLines,
		order.OverageMode, nilIfEmptyJSON(order.GrowCeiling), nilIfEmpty(order.SpendLimitMonth), order.PrepaidBaisa,
	).Scan(&order.ID, &order.CreatedAt); err != nil {
		return 0, fmt.Errorf("store: credit-only create order: %w", err)
	}

	// 2. Record the exact credit spend against the fresh order. Zero-total
	//    orders (promo fully covers free plan + free addons) skip this step,
	//    same as SpendCredit's own no-op path. created_at is clock_timestamp(),
	//    not the default now() (the tx start): this tx may have begun before a
	//    voucher grant it then spent was committed, and the voucher rollback
	//    must see this debit as AFTER that grant (grantSurroundingsQuery).
	if debit > 0 {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO credit_ledger (customer_id, amount_omr, amount_micro_omr, reason, order_id, created_at)
			 VALUES ($1, 0, $2, $3, $4, clock_timestamp())`,
			order.CustomerID, -debit, "order-payment", order.ID,
		); err != nil {
			return 0, fmt.Errorf("store: credit-only spend credit: %w", err)
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
		return 0, fmt.Errorf("store: credit-only create subscription: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: commit credit-only checkout: %w", err)
	}
	return balance - debit, nil
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

// orderTopology defends the orders.topology NOT NULL invariant: an empty
// struct field persists as the canonical default, never as ”.
func orderTopology(t string) string {
	if t == "" {
		return "single-region"
	}
	return t
}

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
