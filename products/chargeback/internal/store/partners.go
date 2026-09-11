package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/lib/pq"
)

// partnersMigrationSQL is the PARTNERS (resellers) model — DESIGN.md
// §13, EPIC #6867.
//
// The founder's model in one sentence: ONE list price per SKU, two
// independent discount steps off it. Customer discounts (the existing engine)
// give the CUSTOMER NET price — what the end customer pays; a partner TIER
// discount gives the PARTNER BUY price — what the partner pays us. Margin is
// customer net minus partner buy, derived per line and never typed anywhere.
//
//   - partner_tiers + tier discounts: a tier's discounts are rows of the ONE
//     discounts table (tier_id set, customer_id NULL, percent only), so the
//     buy price is decided by the same combination engine as every other
//     discount — there is no second pricing path.
//   - partners: a PARTY with its own account. Each partner owns one
//     customers row flagged party_kind = 'partner' (party_customer_id), so it
//     has a balance, payments, invoices and collections through the ledger
//     that already exists, with zero new ledger code. customers.partner_id
//     assigns an end customer to a partner.
//   - partner_retail_rules + derived books: under bill_to = 'partner'
//     (resell) the partner's retail book is MATERIALISED as a real
//     price_books row per list book (derived_from_rule, partner_id,
//     derived_from_book_id) and re-derived on every list, tier or rule
//     change. It is read-only in the editor.
//   - statements gain the partner keys (partner_id, statement_kind,
//     buy_total, margin_total) and rated_lines the per-line waterfall (list,
//     buy, net, end customer), all additive.
//   - role_bindings / group_role_mappings gain the partner scope kind and the
//     two partner roles; account_entries the 'commission' credit kind; the
//     sessions role CHECK the two roles. The unnamed table CHECKs of those
//     tables are dropped by catalogue lookup and re-added under explicit
//     names, because their auto-generated names are not something a
//     migration should guess.
//   - The customers_owner_binding trigger skips party rows: a partner's
//     contact is granted partner-owner at the partner scope by CreatePartner,
//     not customer-owner on its own account row.
//
// Appended at the END of migrations: they are positional. MigrationPartners
// locates it by content so a migration appended after it cannot move it.
const partnersMigrationSQL = `
CREATE TABLE IF NOT EXISTS partner_tiers (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	name TEXT NOT NULL UNIQUE CHECK (name <> ''),
	description TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS partners (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	slug TEXT NOT NULL UNIQUE,
	name TEXT NOT NULL,
	tier_id UUID REFERENCES partner_tiers(id) ON DELETE SET NULL,
	bill_to TEXT NOT NULL DEFAULT 'partner' CHECK (bill_to IN ('partner','customer')),
	commission_pct NUMERIC(7,4) CHECK (commission_pct IS NULL OR (commission_pct >= 0 AND commission_pct <= 100)),
	status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','suspended')),
	contact_email TEXT NOT NULL DEFAULT '',
	party_customer_id UUID REFERENCES customers(id) ON DELETE RESTRICT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE customers ADD COLUMN IF NOT EXISTS party_kind TEXT NOT NULL DEFAULT 'customer';
ALTER TABLE customers DROP CONSTRAINT IF EXISTS customers_party_kind_check;
ALTER TABLE customers ADD CONSTRAINT customers_party_kind_check CHECK (party_kind IN ('customer','partner'));
ALTER TABLE customers ADD COLUMN IF NOT EXISTS partner_id UUID REFERENCES partners(id) ON DELETE SET NULL;
ALTER TABLE customers DROP CONSTRAINT IF EXISTS customers_party_no_partner_check;
ALTER TABLE customers ADD CONSTRAINT customers_party_no_partner_check CHECK (party_kind = 'customer' OR partner_id IS NULL);
CREATE INDEX IF NOT EXISTS customers_partner_idx ON customers (partner_id);
CREATE OR REPLACE FUNCTION customers_owner_binding() RETURNS trigger AS $fn$
BEGIN
	IF NEW.party_kind = 'customer' AND NEW.admin_email IS NOT NULL AND NEW.admin_email <> '' THEN
		INSERT INTO role_bindings (subject_email, role, scope_kind, customer_id, granted_by)
		VALUES (lower(NEW.admin_email), 'customer-owner', 'customer', NEW.id, 'admin_email')
		ON CONFLICT DO NOTHING;
	END IF;
	RETURN NEW;
END
$fn$ LANGUAGE plpgsql;
ALTER TABLE discounts ADD COLUMN IF NOT EXISTS tier_id UUID REFERENCES partner_tiers(id) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS discounts_tier_idx ON discounts (tier_id);
ALTER TABLE discounts DROP CONSTRAINT IF EXISTS discounts_tier_scope_check;
ALTER TABLE discounts ADD CONSTRAINT discounts_tier_scope_check CHECK (tier_id IS NULL OR (customer_id IS NULL AND kind = 'percent'));
CREATE TABLE IF NOT EXISTS partner_retail_rules (
	partner_id UUID PRIMARY KEY REFERENCES partners(id) ON DELETE CASCADE,
	base TEXT NOT NULL CHECK (base IN ('list','buy')),
	markup_pct NUMERIC(7,4) NOT NULL DEFAULT 0,
	overrides JSONB NOT NULL DEFAULT '[]'::jsonb,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE price_books ADD COLUMN IF NOT EXISTS partner_id UUID REFERENCES partners(id) ON DELETE CASCADE;
ALTER TABLE price_books ADD COLUMN IF NOT EXISTS derived_from_rule BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE price_books ADD COLUMN IF NOT EXISTS derived_from_book_id UUID REFERENCES price_books(id) ON DELETE CASCADE;
CREATE UNIQUE INDEX IF NOT EXISTS price_books_derived_uniq ON price_books (partner_id, derived_from_book_id) WHERE derived_from_rule;
ALTER TABLE statements ADD COLUMN IF NOT EXISTS partner_id UUID REFERENCES partners(id) ON DELETE SET NULL;
ALTER TABLE statements ADD COLUMN IF NOT EXISTS statement_kind TEXT NOT NULL DEFAULT 'customer';
ALTER TABLE statements DROP CONSTRAINT IF EXISTS statements_statement_kind_check;
ALTER TABLE statements ADD CONSTRAINT statements_statement_kind_check CHECK (statement_kind IN ('customer','wholesale','commission'));
ALTER TABLE statements ADD COLUMN IF NOT EXISTS buy_total NUMERIC(20,6);
ALTER TABLE statements ADD COLUMN IF NOT EXISTS margin_total NUMERIC(20,6);
CREATE INDEX IF NOT EXISTS statements_partner_idx ON statements (partner_id, period_start);
ALTER TABLE rated_lines ADD COLUMN IF NOT EXISTS end_customer_id UUID REFERENCES customers(id) ON DELETE SET NULL;
ALTER TABLE rated_lines ADD COLUMN IF NOT EXISTS list_unit_price NUMERIC(20,8);
ALTER TABLE rated_lines ADD COLUMN IF NOT EXISTS list_amount NUMERIC(20,6);
ALTER TABLE rated_lines ADD COLUMN IF NOT EXISTS buy_amount NUMERIC(20,6);
ALTER TABLE rated_lines ADD COLUMN IF NOT EXISTS net_amount NUMERIC(20,6);
ALTER TABLE role_bindings ADD COLUMN IF NOT EXISTS partner_id UUID REFERENCES partners(id) ON DELETE CASCADE;
ALTER TABLE group_role_mappings ADD COLUMN IF NOT EXISTS partner_id UUID REFERENCES partners(id) ON DELETE CASCADE;
DO $do$
DECLARE r record;
BEGIN
	FOR r IN SELECT conname, conrelid::regclass AS rel FROM pg_constraint
		WHERE contype = 'c' AND conrelid IN ('role_bindings'::regclass, 'group_role_mappings'::regclass, 'account_entries'::regclass, 'sessions'::regclass)
	LOOP
		EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', r.rel, r.conname);
	END LOOP;
END
$do$;
ALTER TABLE role_bindings ADD CONSTRAINT role_bindings_subject_email_check CHECK (subject_email = lower(subject_email) AND subject_email <> '');
ALTER TABLE role_bindings ADD CONSTRAINT role_bindings_role_check CHECK (role IN ('sovereign-admin','billing-operator','finance-viewer','partner-owner','partner-viewer','customer-owner','customer-billing','customer-viewer'));
ALTER TABLE role_bindings ADD CONSTRAINT role_bindings_scope_kind_check CHECK (scope_kind IN ('sovereign','customer','partner'));
ALTER TABLE role_bindings ADD CONSTRAINT role_bindings_scope_check CHECK (
	(scope_kind = 'sovereign' AND customer_id IS NULL AND partner_id IS NULL)
	OR (scope_kind = 'customer' AND customer_id IS NOT NULL AND partner_id IS NULL)
	OR (scope_kind = 'partner' AND partner_id IS NOT NULL AND customer_id IS NULL));
CREATE UNIQUE INDEX IF NOT EXISTS role_bindings_partner_uniq ON role_bindings (subject_email, role, scope_kind, partner_id) WHERE partner_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS role_bindings_partner_idx ON role_bindings (partner_id);
ALTER TABLE group_role_mappings ADD CONSTRAINT group_role_mappings_group_name_check CHECK (group_name <> '');
ALTER TABLE group_role_mappings ADD CONSTRAINT group_role_mappings_role_check CHECK (role IN ('sovereign-admin','billing-operator','finance-viewer','partner-owner','partner-viewer','customer-owner','customer-billing','customer-viewer'));
ALTER TABLE group_role_mappings ADD CONSTRAINT group_role_mappings_scope_kind_check CHECK (scope_kind IN ('sovereign','customer','partner'));
ALTER TABLE group_role_mappings ADD CONSTRAINT group_role_mappings_scope_check CHECK (
	(scope_kind = 'sovereign' AND customer_id IS NULL AND partner_id IS NULL)
	OR (scope_kind = 'customer' AND customer_id IS NOT NULL AND partner_id IS NULL)
	OR (scope_kind = 'partner' AND partner_id IS NOT NULL AND customer_id IS NULL));
CREATE UNIQUE INDEX IF NOT EXISTS group_role_mappings_partner_uniq ON group_role_mappings (group_name, role, scope_kind, partner_id) WHERE partner_id IS NOT NULL;
ALTER TABLE sessions ADD CONSTRAINT sessions_role_check CHECK (role IN ('operator','customer-admin','customer-viewer','sovereign-admin','billing-operator','finance-viewer','customer-owner','customer-billing','partner-owner','partner-viewer'));
ALTER TABLE account_entries ADD CONSTRAINT account_entries_kind_check CHECK (kind IN ('invoice','payment','credit_note','refund','write_off','top_up','commission'));
ALTER TABLE account_entries ADD CONSTRAINT account_entries_sign_check CHECK ((kind IN ('invoice','refund') AND amount > 0) OR (kind IN ('payment','credit_note','write_off','top_up','commission') AND amount < 0));
`

// MigrationPartners is the schema_migrations version of the partners
// migration, located by content like the others.
var MigrationPartners = func() int {
	for i, m := range migrations {
		if m == partnersMigrationSQL {
			return i + 1
		}
	}
	return len(migrations)
}()

// EntryCommission is the ledger CREDIT a commission statement posts on the
// partner's party at issue (DESIGN.md §13, agent model): money we owe
// the partner, so its balance reads in credit.
const EntryCommission = "commission"

// Partner billing models.
const (
	// BillToPartner — RESELL: the partner is invoiced the wholesale
	// statement (its customers' usage at the partner buy price) and bills
	// its customers itself, at the derived retail book.
	BillToPartner = "partner"
	// BillToCustomer — AGENT: the end customer is invoiced by us at our
	// books; the partner is credited a commission per end-customer invoice.
	BillToCustomer = "customer"
)

// Partner is a reseller or agent: a party with a tier, a billing model and
// its own account (the party customers row).
type Partner struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
	// TierID names the partner tier whose discounts set the buy price; nil
	// = no tier (buy = list under resell; commission_pct under agent).
	TierID   *string `json:"tier_id"`
	TierName string  `json:"tier_name,omitempty"`
	// BillTo is the billing model: partner (resell) or customer (agent).
	BillTo string `json:"bill_to"`
	// CommissionPct is the agent commission, a percent of the customer net,
	// used when the partner has no tier. nil = none.
	CommissionPct *Decimal `json:"commission_pct,omitempty"`
	Status        string   `json:"status"`
	ContactEmail  string   `json:"contact_email"`
	// PartyCustomerID is the partner's own account row in customers
	// (party_kind = partner): its balance, invoices and collections.
	PartyCustomerID string `json:"party_customer_id"`
	CustomerCount   int    `json:"customer_count"`
	// Balance and AvailableCredit are the party's, from the ledger view.
	Balance         Decimal `json:"balance"`
	AvailableCredit Decimal `json:"available_credit"`
	// HasRetailRule says whether a retail rule (and so derived books) exists.
	HasRetailRule bool      `json:"has_retail_rule"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// PartnerTier is a named bundle of tier discounts.
type PartnerTier struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Partners    int        `json:"partners"`
	Discounts   []Discount `json:"discounts"`
	CreatedAt   time.Time  `json:"created_at"`
}

// RetailOverride narrows the markup to one service (SKU prefix) or one SKU.
type RetailOverride struct {
	Scope     string  `json:"scope"` // service | sku
	Key       string  `json:"key"`
	MarkupPct Decimal `json:"markup_pct"`
}

// RetailRule derives a resell partner's retail book: base (list or buy) plus
// a markup, with per-service / per-SKU overrides (most specific wins).
type RetailRule struct {
	PartnerID string           `json:"partner_id"`
	Base      string           `json:"base"`
	MarkupPct Decimal          `json:"markup_pct"`
	Overrides []RetailOverride `json:"overrides"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// Retail rule bases.
const (
	RetailBaseList = "list"
	RetailBaseBuy  = "buy"
)

// MarginRow is one (end customer, service) line of the margin report.
type MarginRow struct {
	CustomerID   string   `json:"customer_id"`
	CustomerName string   `json:"customer_name"`
	Service      string   `json:"service"`
	Net          Decimal  `json:"customer_net"`
	Buy          Decimal  `json:"partner_buy"`
	Margin       Decimal  `json:"margin"`
	MarginPct    *float64 `json:"margin_pct"`
}

// MarginReport is GET /partners/{id}/margin: per customer per service, the
// customer net, the partner buy, the margin and its percentage — read from
// the per-line figures frozen on the customer statements of the period.
type MarginReport struct {
	PartnerID string      `json:"partner_id"`
	Period    string      `json:"period"`
	Currency  string      `json:"currency"`
	Rows      []MarginRow `json:"rows"`
	Totals    MarginRow   `json:"totals"`
}

// ---------------------------------------------------------------------------
// tiers
// ---------------------------------------------------------------------------

const tierColumns = `t.id, t.name, t.description, t.created_at, (SELECT count(*) FROM partners p WHERE p.tier_id = t.id)`

func scanTier(row interface{ Scan(...any) error }) (PartnerTier, error) {
	var t PartnerTier
	if err := row.Scan(&t.ID, &t.Name, &t.Description, &t.CreatedAt, &t.Partners); err != nil {
		return t, mapErr(err)
	}
	t.CreatedAt = t.CreatedAt.UTC()
	t.Discounts = []Discount{}
	return t, nil
}

// ListPartnerTiers returns every tier with its discounts.
func (s *Store) ListPartnerTiers(ctx context.Context) ([]PartnerTier, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tierColumns+` FROM partner_tiers t ORDER BY t.name`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []PartnerTier{}
	for rows.Next() {
		t, err := scanTier(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		ds, err := s.TierDiscounts(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Discounts = ds
	}
	return out, nil
}

// GetPartnerTier returns one tier with its discounts.
func (s *Store) GetPartnerTier(ctx context.Context, id string) (PartnerTier, error) {
	t, err := scanTier(s.db.QueryRowContext(ctx, `SELECT `+tierColumns+` FROM partner_tiers t WHERE t.id = $1`, id))
	if err != nil {
		return t, err
	}
	t.Discounts, err = s.TierDiscounts(ctx, id)
	return t, err
}

// CreatePartnerTier adds a tier (no discounts yet).
func (s *Store) CreatePartnerTier(ctx context.Context, name, description string) (PartnerTier, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return PartnerTier{}, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	var id string
	if err := s.db.QueryRowContext(ctx, `INSERT INTO partner_tiers (name, description) VALUES ($1, $2) RETURNING id`, name, strings.TrimSpace(description)).Scan(&id); err != nil {
		return PartnerTier{}, mapErr(err)
	}
	return s.GetPartnerTier(ctx, id)
}

// UpdatePartnerTier renames or re-describes a tier ("" leaves a field).
func (s *Store) UpdatePartnerTier(ctx context.Context, id, name, description string) (PartnerTier, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE partner_tiers SET name = COALESCE(NULLIF($2, ''), name), description = CASE WHEN $3 = '' THEN description ELSE $3 END WHERE id = $1`,
		id, strings.TrimSpace(name), strings.TrimSpace(description))
	if err != nil {
		return PartnerTier{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return PartnerTier{}, ErrNotFound
	}
	return s.GetPartnerTier(ctx, id)
}

// TierDiscounts lists a tier's discounts (the rows of the ONE discounts
// table that carry this tier_id), newest first.
func (s *Store) TierDiscounts(ctx context.Context, tierID string) ([]Discount, error) {
	return s.queryDiscounts(ctx, ` WHERE d.tier_id = $1`, tierID)
}

// ReplaceTierDiscounts makes the given percent discounts THE discounts of a
// tier (PUT semantics), in one transaction. A tier discount is percent-only:
// it sets a buy PRICE, and a fixed amount off a unit price has no meaning.
func (s *Store) ReplaceTierDiscounts(ctx context.Context, tierID string, in []DiscountInput) ([]Discount, error) {
	for _, d := range in {
		if d.Kind != "percent" {
			return nil, fmt.Errorf("%w: a tier discount must be a percent off list (%q is %s)", ErrInvalid, d.Name, d.Kind)
		}
		if strings.TrimSpace(d.Name) == "" {
			return nil, fmt.Errorf("%w: every tier discount needs a name", ErrInvalid)
		}
		if r := ratOf(d.Value); r.Sign() < 0 || r.Cmp(big.NewRat(100, 1)) > 0 {
			return nil, fmt.Errorf("%w: a tier discount must be between 0 and 100 percent", ErrInvalid)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM partner_tiers WHERE id = $1 FOR UPDATE)`, tierID).Scan(&exists); err != nil {
		return nil, mapErr(err)
	}
	if !exists {
		return nil, ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM discounts WHERE tier_id = $1`, tierID); err != nil {
		return nil, mapErr(err)
	}
	for _, d := range in {
		active := true
		if d.Active != nil {
			active = *d.Active
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO discounts (customer_id, tier_id, name, kind, value, sku, starts_at, ends_at, active, stackable)
			VALUES (NULL, $1, $2, 'percent', $3::numeric, $4, $5, $6, $7, $8)`,
			tierID, strings.TrimSpace(d.Name), string(d.Value), strings.TrimSpace(d.SKU), nullTime(d.StartsAt), nullTime(d.EndsAt), active, d.Stackable); err != nil {
			return nil, mapErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.TierDiscounts(ctx, tierID)
}

// ActiveTierDiscountsAt is what a tier takes off list at t — the input the
// combination engine turns into the partner buy price.
func (s *Store) ActiveTierDiscountsAt(ctx context.Context, tierID string, t time.Time) ([]Discount, error) {
	all, err := s.TierDiscounts(ctx, tierID)
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

// PartnersOnTier lists the ids of the partners on a tier.
func (s *Store) PartnersOnTier(ctx context.Context, tierID string) ([]string, error) {
	return s.ids(ctx, `SELECT id FROM partners WHERE tier_id = $1 ORDER BY slug`, tierID)
}

func (s *Store) ids(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// partners
// ---------------------------------------------------------------------------

const partnerColumns = `p.id, p.slug, p.name, p.tier_id, COALESCE(t.name, ''), p.bill_to, p.commission_pct::text, p.status, p.contact_email, COALESCE(p.party_customer_id::text, ''),
	(SELECT count(*) FROM customers c WHERE c.partner_id = p.id),
	COALESCE((SELECT b.balance FROM customer_balances b WHERE b.customer_id = p.party_customer_id), 0)::numeric(20,6)::text,
	COALESCE((SELECT b.available_credit FROM customer_balances b WHERE b.customer_id = p.party_customer_id), 0)::numeric(20,6)::text,
	EXISTS (SELECT 1 FROM partner_retail_rules r WHERE r.partner_id = p.id),
	p.created_at, p.updated_at`

const partnerFrom = ` FROM partners p LEFT JOIN partner_tiers t ON t.id = p.tier_id`

func scanPartner(row interface{ Scan(...any) error }) (Partner, error) {
	var p Partner
	var tier, pct sql.NullString
	var bal, credit string
	if err := row.Scan(&p.ID, &p.Slug, &p.Name, &tier, &p.TierName, &p.BillTo, &pct, &p.Status, &p.ContactEmail, &p.PartyCustomerID,
		&p.CustomerCount, &bal, &credit, &p.HasRetailRule, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return p, mapErr(err)
	}
	p.TierID = strPtr(tier)
	p.CommissionPct = decPtr(pct)
	p.Balance, p.AvailableCredit = Decimal(bal), Decimal(credit)
	p.CreatedAt, p.UpdatedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC()
	return p, nil
}

// ListPartners returns every partner (ids nil) or the given ones, by name.
func (s *Store) ListPartners(ctx context.Context, ids []string) ([]Partner, error) {
	q := `SELECT ` + partnerColumns + partnerFrom
	var args []any
	if ids != nil {
		q += ` WHERE p.id = ANY($1)`
		args = append(args, pq.Array(ids))
	}
	q += ` ORDER BY p.name`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []Partner{}
	for rows.Next() {
		p, err := scanPartner(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPartner returns one partner.
func (s *Store) GetPartner(ctx context.Context, id string) (Partner, error) {
	return scanPartner(s.db.QueryRowContext(ctx, `SELECT `+partnerColumns+partnerFrom+` WHERE p.id = $1`, id))
}

// GetPartnerBySlug resolves a partner by slug.
func (s *Store) GetPartnerBySlug(ctx context.Context, slug string) (Partner, error) {
	return scanPartner(s.db.QueryRowContext(ctx, `SELECT `+partnerColumns+partnerFrom+` WHERE p.slug = $1`, strings.ToLower(strings.TrimSpace(slug))))
}

// PartnerInput creates a partner.
type PartnerInput struct {
	Slug          string
	Name          string
	TierID        *string
	BillTo        string
	CommissionPct *Decimal
	ContactEmail  string
	// GrantedBy names who granted the contact its partner-owner binding.
	GrantedBy string
}

func validCommission(p *Decimal) error {
	if p == nil || strings.TrimSpace(string(*p)) == "" {
		return nil
	}
	r := ratOf(*p)
	if r.Sign() < 0 || r.Cmp(big.NewRat(100, 1)) > 0 {
		return fmt.Errorf("%w: commission_pct must be between 0 and 100", ErrInvalid)
	}
	return nil
}

// CreatePartner inserts the partner AND its party — the customers row that
// is its account — in one transaction, and grants the contact email
// partner-owner at the partner scope. The party is billed, postpaid, by
// transfer: a partner pays its wholesale invoice on terms.
func (s *Store) CreatePartner(ctx context.Context, in PartnerInput) (Partner, error) {
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	in.Name = strings.TrimSpace(in.Name)
	in.ContactEmail = strings.ToLower(strings.TrimSpace(in.ContactEmail))
	if in.Slug == "" || in.Name == "" {
		return Partner{}, fmt.Errorf("%w: slug and name are required", ErrInvalid)
	}
	if in.BillTo == "" {
		in.BillTo = BillToPartner
	}
	if in.BillTo != BillToPartner && in.BillTo != BillToCustomer {
		return Partner{}, fmt.Errorf("%w: bill_to must be partner (resell) or customer (agent)", ErrInvalid)
	}
	if err := validCommission(in.CommissionPct); err != nil {
		return Partner{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Partner{}, err
	}
	defer tx.Rollback()
	var partyID string
	if err := tx.QueryRowContext(ctx, `INSERT INTO customers (slug, name, admin_email, kind, billing_mode, status, party_kind, charging, payment_model, payment_method, gateway_name)
		VALUES ($1, $2, $3, 'external', 'real', 'active', 'partner', 'billed', 'postpaid', 'transfer', '') RETURNING id`,
		"partner-"+in.Slug, in.Name, in.ContactEmail).Scan(&partyID); err != nil {
		return Partner{}, mapErr(err)
	}
	var id string
	if err := tx.QueryRowContext(ctx, `INSERT INTO partners (slug, name, tier_id, bill_to, commission_pct, contact_email, party_customer_id)
		VALUES ($1, $2, $3, $4, $5::numeric, $6, $7) RETURNING id`,
		in.Slug, in.Name, nullStr(in.TierID), in.BillTo, nullDec(in.CommissionPct), in.ContactEmail, partyID).Scan(&id); err != nil {
		return Partner{}, mapErr(err)
	}
	if in.ContactEmail != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO role_bindings (subject_email, role, scope_kind, partner_id, granted_by) VALUES ($1, 'partner-owner', 'partner', $2, $3) ON CONFLICT DO NOTHING`,
			in.ContactEmail, id, strings.TrimSpace(in.GrantedBy)); err != nil {
			return Partner{}, mapErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Partner{}, err
	}
	return s.GetPartner(ctx, id)
}

// PartnerPatch carries optional updates; nil means unchanged. A TierID or
// CommissionPct pointing at "" clears the field.
type PartnerPatch struct {
	Name          *string
	TierID        *string
	BillTo        *string
	CommissionPct *Decimal
	Status        *string
	ContactEmail  *string
	GrantedBy     string
}

// UpdatePartner applies a patch and keeps the party row's name and contact
// in step. A new contact email is granted partner-owner; the previous one
// keeps its binding until revoked, like a customer's admin_email.
func (s *Store) UpdatePartner(ctx context.Context, id string, p PartnerPatch) (Partner, error) {
	if p.BillTo != nil && *p.BillTo != BillToPartner && *p.BillTo != BillToCustomer {
		return Partner{}, fmt.Errorf("%w: bill_to must be partner (resell) or customer (agent)", ErrInvalid)
	}
	if p.Status != nil && *p.Status != "active" && *p.Status != "suspended" {
		return Partner{}, fmt.Errorf("%w: status must be active or suspended", ErrInvalid)
	}
	if err := validCommission(p.CommissionPct); err != nil {
		return Partner{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Partner{}, err
	}
	defer tx.Rollback()
	var partyID string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(party_customer_id::text, '') FROM partners WHERE id = $1 FOR UPDATE`, id).Scan(&partyID); err != nil {
		return Partner{}, mapErr(err)
	}
	sets := []string{"updated_at = now()"}
	var args []any
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if p.Name != nil && strings.TrimSpace(*p.Name) != "" {
		add("name", strings.TrimSpace(*p.Name))
	}
	if p.TierID != nil {
		add("tier_id", nullStr(p.TierID))
	}
	if p.BillTo != nil {
		add("bill_to", *p.BillTo)
	}
	if p.CommissionPct != nil {
		add("commission_pct", nullDec(p.CommissionPct))
	}
	if p.Status != nil {
		add("status", *p.Status)
	}
	email := ""
	if p.ContactEmail != nil {
		email = strings.ToLower(strings.TrimSpace(*p.ContactEmail))
		add("contact_email", email)
	}
	args = append(args, id)
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE partners SET %s WHERE id = $%d`, strings.Join(sets, ", "), len(args)), args...); err != nil {
		return Partner{}, mapErr(err)
	}
	if partyID != "" {
		if p.Name != nil && strings.TrimSpace(*p.Name) != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE customers SET name = $2, updated_at = now() WHERE id = $1`, partyID, strings.TrimSpace(*p.Name)); err != nil {
				return Partner{}, mapErr(err)
			}
		}
		if p.ContactEmail != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE customers SET admin_email = $2, updated_at = now() WHERE id = $1`, partyID, email); err != nil {
				return Partner{}, mapErr(err)
			}
		}
		if p.Status != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE customers SET status = $2, updated_at = now() WHERE id = $1`, partyID, *p.Status); err != nil {
				return Partner{}, mapErr(err)
			}
		}
	}
	if email != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO role_bindings (subject_email, role, scope_kind, partner_id, granted_by) VALUES ($1, 'partner-owner', 'partner', $2, $3) ON CONFLICT DO NOTHING`,
			email, id, strings.TrimSpace(p.GrantedBy)); err != nil {
			return Partner{}, mapErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Partner{}, err
	}
	return s.GetPartner(ctx, id)
}

// PartnerCustomers lists the end customers assigned to a partner.
func (s *Store) PartnerCustomers(ctx context.Context, partnerID string) ([]Customer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+customerColumns+customerFrom+` WHERE c.partner_id = $1 AND c.party_kind = 'customer' ORDER BY c.name`, partnerID)
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

// ExpandPartnerScope is what a binding at partner:<id> reads: the customers
// assigned to the partner plus the partner's own party. The party comes
// first so a reader that takes one customer (the legacy customer_id) lands
// on the partner's own account.
func (s *Store) ExpandPartnerScope(ctx context.Context, partnerID string) ([]string, error) {
	return s.ids(ctx, `SELECT id FROM (
		SELECT party_customer_id::text AS id, 0 AS rank, '' AS name FROM partners WHERE id = $1 AND party_customer_id IS NOT NULL
		UNION ALL
		SELECT id::text, 1, name FROM customers WHERE partner_id = $1 AND party_kind = 'customer'
	) x ORDER BY rank, name`, partnerID)
}

// PartnerOfCustomer resolves the partner of a customer (nil = direct).
func (s *Store) PartnerOfCustomer(ctx context.Context, customerID string) (*Partner, error) {
	var pid sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT partner_id FROM customers WHERE id = $1`, customerID).Scan(&pid); err != nil {
		return nil, mapErr(err)
	}
	if !pid.Valid {
		return nil, nil
	}
	p, err := s.GetPartner(ctx, pid.String)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// PartnerUsers lists the partner-scoped bindings of a partner.
func (s *Store) PartnerUsers(ctx context.Context, partnerID string) ([]RoleBinding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+roleBindingColumns+roleBindingFrom+` WHERE b.partner_id = $1 ORDER BY (b.role = 'partner-owner') DESC, b.subject_email`, partnerID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []RoleBinding{}
	for rows.Next() {
		b, err := scanRoleBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// UpsertPartnerUser gives an email exactly one partner role on a partner.
func (s *Store) UpsertPartnerUser(ctx context.Context, partnerID, email, role, grantedBy string) error {
	if role != RolePartnerOwner && role != RolePartnerViewer {
		return fmt.Errorf("%w: role must be partner-owner or partner-viewer", ErrInvalid)
	}
	email = strings.ToLower(strings.TrimSpace(email))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM role_bindings WHERE partner_id = $1 AND subject_email = $2 AND scope_kind = 'partner' AND role <> $3`, partnerID, email, role); err != nil {
		return mapErr(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO role_bindings (subject_email, role, scope_kind, partner_id, granted_by) VALUES ($1, $2, 'partner', $3, $4) ON CONFLICT DO NOTHING`, email, role, partnerID, strings.TrimSpace(grantedBy)); err != nil {
		return mapErr(err)
	}
	return tx.Commit()
}

// DeletePartnerUser removes every partner-scoped binding of an email on a partner.
func (s *Store) DeletePartnerUser(ctx context.Context, partnerID, email string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM role_bindings WHERE partner_id = $1 AND subject_email = $2 AND scope_kind = 'partner'`, partnerID, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// the retail rule and the derived books
// ---------------------------------------------------------------------------

// GetRetailRule returns a partner's retail rule; ok=false when none.
func (s *Store) GetRetailRule(ctx context.Context, partnerID string) (RetailRule, bool, error) {
	var r RetailRule
	var markup string
	var overrides []byte
	err := s.db.QueryRowContext(ctx, `SELECT partner_id, base, markup_pct::text, overrides, updated_at FROM partner_retail_rules WHERE partner_id = $1`, partnerID).
		Scan(&r.PartnerID, &r.Base, &markup, &overrides, &r.UpdatedAt)
	if err == sql.ErrNoRows {
		return RetailRule{}, false, nil
	}
	if err != nil {
		return RetailRule{}, false, mapErr(err)
	}
	r.MarkupPct = Decimal(markup)
	r.Overrides = []RetailOverride{}
	if len(overrides) > 0 {
		if err := json.Unmarshal(overrides, &r.Overrides); err != nil {
			return RetailRule{}, false, err
		}
	}
	r.UpdatedAt = r.UpdatedAt.UTC()
	return r, true, nil
}

// ValidateRetailRule normalises a rule or names the first problem.
func ValidateRetailRule(r RetailRule) (RetailRule, error) {
	r.Base = strings.ToLower(strings.TrimSpace(r.Base))
	if r.Base != RetailBaseList && r.Base != RetailBaseBuy {
		return r, fmt.Errorf("%w: base must be list or buy", ErrInvalid)
	}
	if strings.TrimSpace(string(r.MarkupPct)) == "" {
		r.MarkupPct = "0"
	}
	if !decimalShape.MatchString(strings.TrimSpace(string(r.MarkupPct))) {
		return r, fmt.Errorf("%w: markup_pct must be a number", ErrInvalid)
	}
	if ratOf(r.MarkupPct).Cmp(big.NewRat(-100, 1)) < 0 {
		return r, fmt.Errorf("%w: markup_pct cannot be below -100", ErrInvalid)
	}
	clean := make([]RetailOverride, 0, len(r.Overrides))
	seen := map[string]bool{}
	for _, o := range r.Overrides {
		o.Scope = strings.ToLower(strings.TrimSpace(o.Scope))
		o.Key = strings.TrimSpace(o.Key)
		if o.Scope != "service" && o.Scope != "sku" {
			return r, fmt.Errorf("%w: override scope must be service or sku", ErrInvalid)
		}
		if o.Key == "" {
			return r, fmt.Errorf("%w: every override needs a key", ErrInvalid)
		}
		if strings.TrimSpace(string(o.MarkupPct)) == "" || !decimalShape.MatchString(strings.TrimSpace(string(o.MarkupPct))) {
			return r, fmt.Errorf("%w: override %s %s needs a numeric markup_pct", ErrInvalid, o.Scope, o.Key)
		}
		k := o.Scope + ":" + o.Key
		if seen[k] {
			return r, fmt.Errorf("%w: override %s %s given twice", ErrInvalid, o.Scope, o.Key)
		}
		seen[k] = true
		clean = append(clean, o)
	}
	r.Overrides = clean
	return r, nil
}

// PutRetailRule stores (replaces) a partner's retail rule. Deriving the
// books from it is the rating package's job (rating.DeriveRetailBooks).
func (s *Store) PutRetailRule(ctx context.Context, partnerID string, r RetailRule) (RetailRule, error) {
	r, err := ValidateRetailRule(r)
	if err != nil {
		return RetailRule{}, err
	}
	ov, _ := json.Marshal(r.Overrides)
	res, err := s.db.ExecContext(ctx, `INSERT INTO partner_retail_rules (partner_id, base, markup_pct, overrides, updated_at)
		SELECT $1, $2, $3::numeric, $4, now() WHERE EXISTS (SELECT 1 FROM partners WHERE id = $1)
		ON CONFLICT (partner_id) DO UPDATE SET base = EXCLUDED.base, markup_pct = EXCLUDED.markup_pct, overrides = EXCLUDED.overrides, updated_at = now()`,
		partnerID, r.Base, string(r.MarkupPct), ov)
	if err != nil {
		return RetailRule{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return RetailRule{}, ErrNotFound
	}
	out, _, err := s.GetRetailRule(ctx, partnerID)
	return out, err
}

// ListBooksForPartner returns the LIST books the partner's customers'
// sources are assigned to (never a derived book), by name. These are the
// books a retail book is derived from, one derived book each.
func (s *Store) ListBooksForPartner(ctx context.Context, partnerID string) ([]PriceBook, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+priceBookColumns+` FROM price_books WHERE NOT derived_from_rule AND id IN (
		SELECT DISTINCT s.price_book_id FROM cost_sources s JOIN customers c ON c.id = s.customer_id
		WHERE c.partner_id = $1 AND c.party_kind = 'customer' AND s.price_book_id IS NOT NULL) ORDER BY name`, partnerID)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Items, err = s.ListPriceItems(ctx, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// DerivedBooks returns a partner's derived retail books with their items.
func (s *Store) DerivedBooks(ctx context.Context, partnerID string) ([]PriceBook, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+priceBookColumns+` FROM price_books WHERE partner_id = $1 AND derived_from_rule ORDER BY name`, partnerID)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Items, err = s.ListPriceItems(ctx, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// DerivedBookFor returns the partner's retail book derived from one list
// book; ok=false when none.
func (s *Store) DerivedBookFor(ctx context.Context, partnerID, listBookID string) (PriceBook, bool, error) {
	pb, err := scanPriceBook(s.db.QueryRowContext(ctx, `SELECT `+priceBookColumns+` FROM price_books WHERE partner_id = $1 AND derived_from_book_id = $2 AND derived_from_rule`, partnerID, listBookID))
	if err != nil {
		if err == ErrNotFound {
			return PriceBook{}, false, nil
		}
		return PriceBook{}, false, err
	}
	pb.Items, err = s.ListPriceItems(ctx, pb.ID)
	return pb, err == nil, err
}

// UpsertDerivedBook materialises (or refreshes) the partner's retail book
// derived from a list book: the header follows the list book (scope,
// currency, divisor, stopped policy), the items are replaced. annual_price
// is left NULL on purpose so a divisor change never recomputes a derived
// unit price behind the rule's back.
func (s *Store) UpsertDerivedBook(ctx context.Context, partnerID string, list PriceBook, name, description string, items []PriceItem) (PriceBook, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PriceBook{}, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM price_books WHERE partner_id = $1 AND derived_from_book_id = $2 AND derived_from_rule FOR UPDATE`, partnerID, list.ID).Scan(&id)
	switch {
	case err == sql.ErrNoRows:
		if err := tx.QueryRowContext(ctx, `INSERT INTO price_books (name, scope, currency, annual_divisor, bill_stopped, effective_from, description, partner_id, derived_from_rule, derived_from_book_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, true, $9) RETURNING id`,
			name, list.Scope, list.Currency, list.AnnualDivisor, list.BillStopped, nullStr(list.EffectiveFrom), description, partnerID, list.ID).Scan(&id); err != nil {
			return PriceBook{}, mapErr(err)
		}
	case err != nil:
		return PriceBook{}, mapErr(err)
	default:
		if _, err := tx.ExecContext(ctx, `UPDATE price_books SET name = $2, scope = $3, currency = $4, annual_divisor = $5, bill_stopped = $6, effective_from = $7, description = $8 WHERE id = $1`,
			id, name, list.Scope, list.Currency, list.AnnualDivisor, list.BillStopped, nullStr(list.EffectiveFrom), description); err != nil {
			return PriceBook{}, mapErr(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM price_items WHERE price_book_id = $1`, id); err != nil {
		return PriceBook{}, mapErr(err)
	}
	for _, it := range items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO price_items (price_book_id, sku, unit, unit_price, annual_price, description) VALUES ($1, $2, $3, $4, NULL, $5)`,
			id, strings.TrimSpace(it.SKU), strings.TrimSpace(it.Unit), string(it.UnitPrice), it.Description); err != nil {
			return PriceBook{}, mapErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return PriceBook{}, err
	}
	return s.GetPriceBook(ctx, id)
}

// DeleteDerivedBooks removes the partner's derived books except those
// derived from the list books in keep (nil keep = remove them all).
func (s *Store) DeleteDerivedBooks(ctx context.Context, partnerID string, keep []string) error {
	if keep == nil {
		keep = []string{}
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM price_books WHERE partner_id = $1 AND derived_from_rule AND NOT (derived_from_book_id::text = ANY($2))`, partnerID, pq.Array(keep))
	return mapErr(err)
}

// PartnersDerivingFrom lists the resell partners whose retail books derive
// (or would derive) from a list book: every partner with a retail rule whose
// customers' sources use it, plus every partner that already holds a book
// derived from it.
func (s *Store) PartnersDerivingFrom(ctx context.Context, listBookID string) ([]string, error) {
	return s.ids(ctx, `SELECT DISTINCT p.id FROM partners p
		WHERE EXISTS (SELECT 1 FROM partner_retail_rules r WHERE r.partner_id = p.id)
		  AND (EXISTS (SELECT 1 FROM cost_sources s JOIN customers c ON c.id = s.customer_id WHERE c.partner_id = p.id AND s.price_book_id = $1)
		    OR EXISTS (SELECT 1 FROM price_books b WHERE b.partner_id = p.id AND b.derived_from_book_id = $1))`, listBookID)
}

// IsDerivedBook reports whether a price book is a partner's derived retail
// book (read-only in the editor).
func (s *Store) IsDerivedBook(ctx context.Context, bookID string) (bool, error) {
	var derived bool
	if err := s.db.QueryRowContext(ctx, `SELECT derived_from_rule FROM price_books WHERE id = $1`, bookID).Scan(&derived); err != nil {
		return false, mapErr(err)
	}
	return derived, nil
}

// ---------------------------------------------------------------------------
// partner statements: the lines of a period, and the margin report
// ---------------------------------------------------------------------------

// PartnerPeriodLines returns the rated lines of the CUSTOMER statements of a
// partner's customers for one period, each carrying the per-line waterfall
// (list, buy, net) and the end customer — the input of the wholesale and
// commission statements. Statements are frozen at rating time: a customer
// that later moves to another partner keeps its old periods here.
func (s *Store) PartnerPeriodLines(ctx context.Context, partnerID string, periodStart time.Time) ([]RatedLine, []string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT l.id, l.statement_id, l.customer_id, l.source_id, l.sku, l.quantity::text, l.unit, l.unit_price::text, l.amount::text, l.resource_count,
			l.list_unit_price::text, l.list_amount::text, l.buy_amount::text, l.net_amount::text, c.name, st.currency
		FROM rated_lines l JOIN statements st ON st.id = l.statement_id JOIN customers c ON c.id = l.customer_id
		WHERE st.statement_kind = 'customer' AND st.partner_id = $1 AND st.period_start = $2 AND st.status <> 'cancelled'
		ORDER BY c.name, l.sku, l.source_id`, partnerID, periodStart)
	if err != nil {
		return nil, nil, mapErr(err)
	}
	defer rows.Close()
	out := []RatedLine{}
	currencies := []string{}
	seenCur := map[string]bool{}
	for rows.Next() {
		var l RatedLine
		var src, lup, lam, buy, net sql.NullString
		var q, up, amt, cur string
		if err := rows.Scan(&l.ID, &l.StatementID, &l.CustomerID, &src, &l.SKU, &q, &l.Unit, &up, &amt, &l.ResourceCount, &lup, &lam, &buy, &net, &l.EndCustomerName, &cur); err != nil {
			return nil, nil, err
		}
		l.SourceID = strPtr(src)
		l.Quantity, l.UnitPrice, l.Amount = Decimal(q), Decimal(up), Decimal(amt)
		l.ListUnitPrice, l.ListAmount, l.BuyAmount, l.NetAmount = decPtr(lup), decPtr(lam), decPtr(buy), decPtr(net)
		cid := l.CustomerID
		l.EndCustomerID = &cid
		if !seenCur[cur] {
			seenCur[cur] = true
			currencies = append(currencies, cur)
		}
		out = append(out, l)
	}
	return out, currencies, rows.Err()
}

// ListPartnerStatements returns the partner's OWN statements — the wholesale
// or commission statements billed to its party — newest period first.
func (s *Store) ListPartnerStatements(ctx context.Context, partnerID string) ([]Statement, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+statementColumns+statementFrom+` WHERE st.partner_id = $1 AND st.statement_kind <> 'customer' ORDER BY st.period_start DESC`, partnerID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []Statement{}
	for rows.Next() {
		st, err := scanStatement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// MarginReport aggregates the per-line figures frozen on a partner's
// customers' statements for one period: per customer per service (the SKU's
// first segment — ecs, evs, eip, plan, k8s …), the customer net, the partner
// buy, the margin and its percentage of the net. Money is summed by
// Postgres numeric and never touches a float; only the percentage is one.
func (s *Store) MarginReport(ctx context.Context, partnerID, period string) (MarginReport, error) {
	from, _, err := PeriodBounds(period)
	if err != nil {
		return MarginReport{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	rep := MarginReport{PartnerID: partnerID, Period: period, Rows: []MarginRow{}}
	rows, err := s.db.QueryContext(ctx, `SELECT l.customer_id, c.name, split_part(l.sku, '.', 1),
			COALESCE(sum(l.net_amount), 0)::numeric(20,6)::text, COALESCE(sum(l.buy_amount), 0)::numeric(20,6)::text,
			(COALESCE(sum(l.net_amount), 0) - COALESCE(sum(l.buy_amount), 0))::numeric(20,6)::text, min(st.currency)
		FROM rated_lines l JOIN statements st ON st.id = l.statement_id JOIN customers c ON c.id = l.customer_id
		WHERE st.statement_kind = 'customer' AND st.partner_id = $1 AND st.period_start = $2 AND st.status <> 'cancelled'
		GROUP BY l.customer_id, c.name, split_part(l.sku, '.', 1) ORDER BY c.name, split_part(l.sku, '.', 1)`, partnerID, from)
	if err != nil {
		return rep, mapErr(err)
	}
	defer rows.Close()
	net, buy := new(big.Rat), new(big.Rat)
	for rows.Next() {
		var r MarginRow
		var n, b, m, cur string
		if err := rows.Scan(&r.CustomerID, &r.CustomerName, &r.Service, &n, &b, &m, &cur); err != nil {
			return rep, err
		}
		r.Net, r.Buy, r.Margin = Decimal(n), Decimal(b), Decimal(m)
		r.MarginPct = marginPct(ratOf(r.Margin), ratOf(r.Net))
		if rep.Currency == "" {
			rep.Currency = cur
		}
		net.Add(net, ratOf(r.Net))
		buy.Add(buy, ratOf(r.Buy))
		rep.Rows = append(rep.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return rep, err
	}
	margin := new(big.Rat).Sub(net, buy)
	rep.Totals = MarginRow{Net: decOf(net), Buy: decOf(buy), Margin: decOf(margin), MarginPct: marginPct(margin, net)}
	return rep, nil
}

// marginPct is margin / net × 100, nil when the net is zero.
func marginPct(margin, net *big.Rat) *float64 {
	if net.Sign() == 0 {
		return nil
	}
	f, _ := new(big.Rat).Quo(new(big.Rat).Mul(margin, big.NewRat(100, 1)), net).Float64()
	return &f
}
