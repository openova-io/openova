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

// contractsMigrationSQL is CONTRACTS AND COMMERCIAL TERMS — DESIGN.md §15,
// EPIC #6867.
//
// Two things the price books and discounts shipped so far cannot express, and
// which every cloud provider sells. The vocabulary is the industry's (AWS,
// Azure, Stripe, Zuora); nothing here is invented:
//
//   - RATING SHAPES on a price-book item, applied by the one rating engine:
//     an ALLOWANCE (N units of a SKU included per billing period; the excess
//     rates at the item price), VOLUME TIERS (bands instead of one unit
//     price, in the two standard modes — `graduated`, each band at its own
//     rate, and `all_units`, the whole volume at the band the total reaches)
//     and COMMITTED USE (a quantity of a SKU committed for a term at a
//     discounted rate; the committed quantity rates at the committed price,
//     the excess at list).
//   - CONTRACTS, the agreement those hang on: a term with an end date, an
//     auto-renewal and its notice period, a monthly MINIMUM COMMITMENT whose
//     shortfall is invoiced as a named `true-up` line, and SLA CREDITS issued
//     through the credit-note machinery that already exists.
//
// Shape of the schema:
//
//   - price_items gains tier_mode + tiers (JSONB bands) + allowance +
//     allowance_rollover. Every column is additive and defaults to the
//     behaviour of a book that has none of them, so an existing book rates
//     exactly as before.
//   - contracts is the agreement, contract_items its committed-use and
//     allowance lines. An allowance may belong to the PLAN (the price-book
//     item) or to the CONTRACT (a contract_items row of kind 'allowance');
//     they add up, because a negotiated allowance is on top of the plan's.
//   - statements gains contract_id — the agreement the period was rated
//     under, frozen with the bill.
//   - credit_notes gains contract_id, sla_pct and measured_availability, so
//     an SLA credit is a credit note that RECORDS the breach it answers.
//     There is no parallel table: one credit-note ledger, one numbering
//     sequence, one account entry.
//
// Appended at the END of migrations: they are positional — an entry inserted
// above a database's recorded version is silently skipped. MigrationContracts
// locates it by content so a migration appended after it cannot move it.
const contractsMigrationSQL = `
ALTER TABLE price_items ADD COLUMN IF NOT EXISTS tier_mode TEXT NOT NULL DEFAULT '';
ALTER TABLE price_items DROP CONSTRAINT IF EXISTS price_items_tier_mode_check;
ALTER TABLE price_items ADD CONSTRAINT price_items_tier_mode_check CHECK (tier_mode IN ('','graduated','all_units'));
ALTER TABLE price_items ADD COLUMN IF NOT EXISTS tiers JSONB;
ALTER TABLE price_items ADD COLUMN IF NOT EXISTS allowance NUMERIC(20,6);
ALTER TABLE price_items DROP CONSTRAINT IF EXISTS price_items_allowance_check;
ALTER TABLE price_items ADD CONSTRAINT price_items_allowance_check CHECK (allowance IS NULL OR allowance >= 0);
ALTER TABLE price_items ADD COLUMN IF NOT EXISTS allowance_rollover BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS contracts (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
	name TEXT NOT NULL CHECK (name <> ''),
	starts_on DATE NOT NULL,
	ends_on DATE NOT NULL,
	term_months INT NOT NULL DEFAULT 12 CHECK (term_months > 0),
	auto_renew BOOLEAN NOT NULL DEFAULT false,
	renewal_notice_days INT NOT NULL DEFAULT 30 CHECK (renewal_notice_days >= 0),
	minimum_commitment NUMERIC(20,6) CHECK (minimum_commitment IS NULL OR minimum_commitment >= 0),
	currency TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','active','expired','cancelled')),
	signed_at TIMESTAMPTZ,
	po_reference TEXT NOT NULL DEFAULT '',
	notes TEXT NOT NULL DEFAULT '',
	renewed_at TIMESTAMPTZ,
	renewal_count INT NOT NULL DEFAULT 0,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CHECK (ends_on >= starts_on)
);
CREATE INDEX IF NOT EXISTS contracts_customer_idx ON contracts (customer_id, starts_on DESC);
CREATE INDEX IF NOT EXISTS contracts_renewal_idx ON contracts (status, ends_on);

CREATE TABLE IF NOT EXISTS contract_items (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	contract_id UUID NOT NULL REFERENCES contracts(id) ON DELETE CASCADE,
	kind TEXT NOT NULL CHECK (kind IN ('commitment','allowance')),
	sku TEXT NOT NULL CHECK (sku <> ''),
	unit TEXT NOT NULL DEFAULT '',
	quantity NUMERIC(20,6) NOT NULL CHECK (quantity >= 0),
	committed_price NUMERIC(20,8) CHECK (committed_price IS NULL OR committed_price >= 0),
	discount_pct NUMERIC(7,4) CHECK (discount_pct IS NULL OR (discount_pct >= 0 AND discount_pct <= 100)),
	rollover BOOLEAN NOT NULL DEFAULT false,
	notes TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (contract_id, kind, sku)
);
CREATE INDEX IF NOT EXISTS contract_items_contract_idx ON contract_items (contract_id);

ALTER TABLE statements ADD COLUMN IF NOT EXISTS contract_id UUID REFERENCES contracts(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS statements_contract_idx ON statements (contract_id);
ALTER TABLE credit_notes ADD COLUMN IF NOT EXISTS contract_id UUID REFERENCES contracts(id) ON DELETE SET NULL;
ALTER TABLE credit_notes ADD COLUMN IF NOT EXISTS sla_pct NUMERIC(7,4);
ALTER TABLE credit_notes ADD COLUMN IF NOT EXISTS measured_availability NUMERIC(7,4);
`

// MigrationContracts is the schema_migrations version of the contracts
// migration, located by CONTENT like the others so a migration appended after
// it cannot move this version.
var MigrationContracts = func() int {
	for i, m := range migrations {
		if m == contractsMigrationSQL {
			return i + 1
		}
	}
	return len(migrations)
}()

// Tier modes on a price-book item (DESIGN.md §15.2). The names are the
// industry's: graduated is Stripe's "graduated" / AWS's tiered pricing, where
// each band rates at its own price; all_units is Stripe's "volume" /
// Zuora's "volume pricing", where the whole quantity rates at the price of
// the band the TOTAL reaches.
const (
	TierModeNone      = ""
	TierModeGraduated = "graduated"
	TierModeAllUnits  = "all_units"
)

// ValidTierMode reports whether a mode is one the engine knows.
func ValidTierMode(m string) bool {
	return m == TierModeNone || m == TierModeGraduated || m == TierModeAllUnits
}

// PriceTier is one band of a volume-tiered item: everything up to UpTo rates
// at Price. UpTo nil is the last, unbounded band.
type PriceTier struct {
	UpTo  *Decimal `json:"up_to"`
	Price Decimal  `json:"price"`
}

// Contract statuses.
const (
	ContractDraft     = "draft"
	ContractActive    = "active"
	ContractExpired   = "expired"
	ContractCancelled = "cancelled"
)

// ContractStatuses is every status, in lifecycle order.
var ContractStatuses = []string{ContractDraft, ContractActive, ContractExpired, ContractCancelled}

// Contract item kinds.
const (
	ContractItemCommitment = "commitment"
	ContractItemAllowance  = "allowance"
)

// ContractItem is one committed-use line or one contract allowance.
//
//   - commitment: Quantity of SKU per billing period rates at CommittedPrice
//     (or at list less DiscountPct when no price is given); the excess rates
//     at list.
//   - allowance: Quantity units of SKU are included in every billing period.
//     Rollover carries what is unused into the next period; without it the
//     allowance lapses at the end of the period, which is the default.
type ContractItem struct {
	ID         string  `json:"id"`
	ContractID string  `json:"contract_id"`
	Kind       string  `json:"kind"`
	SKU        string  `json:"sku"`
	Unit       string  `json:"unit,omitempty"`
	Quantity   Decimal `json:"quantity"`
	// CommittedPrice is the negotiated unit price of a commitment. nil with
	// DiscountPct set means "that percent off the list price".
	CommittedPrice *Decimal  `json:"committed_price,omitempty"`
	DiscountPct    *Decimal  `json:"discount_pct,omitempty"`
	Rollover       bool      `json:"rollover"`
	Notes          string    `json:"notes,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// Contract is the agreement a customer's commercial terms hang on
// (DESIGN.md §15.3).
type Contract struct {
	ID           string `json:"id"`
	CustomerID   string `json:"customer_id"`
	CustomerName string `json:"customer_name,omitempty"`
	CustomerSlug string `json:"customer_slug,omitempty"`
	Name         string `json:"name"`
	StartsOn     string `json:"starts_on"`
	EndsOn       string `json:"ends_on"`
	TermMonths   int    `json:"term_months"`
	AutoRenew    bool   `json:"auto_renew"`
	// RenewalNoticeDays before EndsOn the contract appears in the
	// renewals-due list.
	RenewalNoticeDays int `json:"renewal_notice_days"`
	// MinimumCommitment is the MONTHLY floor: a period whose rated total
	// falls below it carries a true-up line for the shortfall. nil = none.
	MinimumCommitment *Decimal       `json:"minimum_commitment,omitempty"`
	Currency          string         `json:"currency"`
	Status            string         `json:"status"`
	SignedAt          *time.Time     `json:"signed_at,omitempty"`
	PORef             string         `json:"po_reference,omitempty"`
	Notes             string         `json:"notes,omitempty"`
	RenewedAt         *time.Time     `json:"renewed_at,omitempty"`
	RenewalCount      int            `json:"renewal_count"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	Items             []ContractItem `json:"items"`
	// RenewalDate is EndsOn + 1 day — when the next term starts if the
	// contract renews. NoticeFrom is EndsOn − RenewalNoticeDays, the day it
	// joins the renewals-due list. Both are derived, never stored.
	RenewalDate string `json:"renewal_date"`
	NoticeFrom  string `json:"notice_from"`
}

// Commitments returns the contract's committed-use lines.
func (c Contract) Commitments() []ContractItem { return c.itemsOfKind(ContractItemCommitment) }

// Allowances returns the contract's allowance lines.
func (c Contract) Allowances() []ContractItem { return c.itemsOfKind(ContractItemAllowance) }

func (c Contract) itemsOfKind(kind string) []ContractItem {
	out := []ContractItem{}
	for _, it := range c.Items {
		if it.Kind == kind {
			out = append(out, it)
		}
	}
	return out
}

// CoversDate reports whether the contract's term contains the given day.
func (c Contract) CoversDate(day string) bool {
	return c.StartsOn <= day && day <= c.EndsOn
}

const contractColumns = `ct.id, ct.customer_id, c.name, c.slug, ct.name, to_char(ct.starts_on, 'YYYY-MM-DD'), to_char(ct.ends_on, 'YYYY-MM-DD'),
	ct.term_months, ct.auto_renew, ct.renewal_notice_days, ct.minimum_commitment::text, ct.currency, ct.status, ct.signed_at,
	ct.po_reference, ct.notes, ct.renewed_at, ct.renewal_count, ct.created_at, ct.updated_at`

const contractFrom = ` FROM contracts ct JOIN customers c ON c.id = ct.customer_id`

func scanContract(row interface{ Scan(...any) error }) (Contract, error) {
	var ct Contract
	var minimum sql.NullString
	var signed, renewed sql.NullTime
	if err := row.Scan(&ct.ID, &ct.CustomerID, &ct.CustomerName, &ct.CustomerSlug, &ct.Name, &ct.StartsOn, &ct.EndsOn,
		&ct.TermMonths, &ct.AutoRenew, &ct.RenewalNoticeDays, &minimum, &ct.Currency, &ct.Status, &signed,
		&ct.PORef, &ct.Notes, &renewed, &ct.RenewalCount, &ct.CreatedAt, &ct.UpdatedAt); err != nil {
		return ct, mapErr(err)
	}
	ct.MinimumCommitment = decPtr(minimum)
	ct.SignedAt, ct.RenewedAt = timePtr(signed), timePtr(renewed)
	ct.CreatedAt, ct.UpdatedAt = ct.CreatedAt.UTC(), ct.UpdatedAt.UTC()
	ct.Items = []ContractItem{}
	ct.RenewalDate, ct.NoticeFrom = renewalDates(ct.EndsOn, ct.RenewalNoticeDays)
	return ct, nil
}

// renewalDates derives the day the next term would start and the day the
// contract joins the renewals-due list.
func renewalDates(endsOn string, noticeDays int) (renewal, notice string) {
	t, err := time.Parse("2006-01-02", endsOn)
	if err != nil {
		return "", ""
	}
	return t.AddDate(0, 0, 1).Format("2006-01-02"), t.AddDate(0, 0, -noticeDays).Format("2006-01-02")
}

// ContractInput is the creatable / patchable document. On a PATCH a nil
// pointer means "unchanged"; on a create the zero values are defaulted.
type ContractInput struct {
	CustomerID        string
	Name              *string
	StartsOn          *string
	EndsOn            *string
	TermMonths        *int
	AutoRenew         *bool
	RenewalNoticeDays *int
	MinimumCommitment *Decimal
	ClearMinimum      bool
	Currency          *string
	Status            *string
	SignedAt          *time.Time
	ClearSignedAt     bool
	PORef             *string
	Notes             *string
}

func str(p *string, def string) string {
	if p == nil {
		return def
	}
	return strings.TrimSpace(*p)
}

// AddTerm is starts_on + months, minus one day: a 12-month term starting
// 2026-01-01 ends 2026-12-31, never 2027-01-01. Nobody signs a term that
// overlaps its own renewal by a day.
func AddTerm(startsOn string, months int) (string, error) {
	t, err := time.Parse("2006-01-02", startsOn)
	if err != nil {
		return "", fmt.Errorf("%w: starts_on must be YYYY-MM-DD", ErrInvalid)
	}
	if months <= 0 {
		months = 12
	}
	return t.AddDate(0, months, 0).AddDate(0, 0, -1).Format("2006-01-02"), nil
}

// CreateContract inserts a contract. The end date may be given or derived
// from the term; the currency defaults to the customer's billing currency
// when the caller leaves it empty.
func (s *Store) CreateContract(ctx context.Context, in ContractInput) (Contract, error) {
	name := str(in.Name, "")
	if name == "" {
		return Contract{}, fmt.Errorf("%w: a contract needs a name", ErrInvalid)
	}
	starts := str(in.StartsOn, "")
	if !ValidDate(starts) {
		return Contract{}, fmt.Errorf("%w: starts_on must be YYYY-MM-DD", ErrInvalid)
	}
	term := 12
	if in.TermMonths != nil && *in.TermMonths > 0 {
		term = *in.TermMonths
	}
	ends := str(in.EndsOn, "")
	if ends == "" {
		var err error
		if ends, err = AddTerm(starts, term); err != nil {
			return Contract{}, err
		}
	}
	if !ValidDate(ends) {
		return Contract{}, fmt.Errorf("%w: ends_on must be YYYY-MM-DD", ErrInvalid)
	}
	if ends < starts {
		return Contract{}, fmt.Errorf("%w: ends_on is before starts_on", ErrInvalid)
	}
	status := strings.ToLower(str(in.Status, ContractDraft))
	if !oneOf(status, ContractStatuses) {
		return Contract{}, fmt.Errorf("%w: status must be one of %s", ErrInvalid, strings.Join(ContractStatuses, ", "))
	}
	currency := strings.ToUpper(str(in.Currency, ""))
	if currency == "" {
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT b.currency FROM cost_sources cs JOIN price_books b ON b.id = cs.price_book_id WHERE cs.customer_id = $1 LIMIT 1), 'OMR')`, in.CustomerID).Scan(&currency); err != nil {
			return Contract{}, mapErr(err)
		}
	}
	notice := 30
	if in.RenewalNoticeDays != nil && *in.RenewalNoticeDays >= 0 {
		notice = *in.RenewalNoticeDays
	}
	var id string
	err := s.db.QueryRowContext(ctx, `INSERT INTO contracts (customer_id, name, starts_on, ends_on, term_months, auto_renew, renewal_notice_days, minimum_commitment, currency, status, signed_at, po_reference, notes)
		VALUES ($1, $2, $3::date, $4::date, $5, $6, $7, $8::numeric, $9, $10, $11, $12, $13) RETURNING id`,
		in.CustomerID, name, starts, ends, term, in.AutoRenew != nil && *in.AutoRenew, notice, nullDec(in.MinimumCommitment), currency, status,
		nullTime(in.SignedAt), str(in.PORef, ""), str(in.Notes, "")).Scan(&id)
	if err != nil {
		return Contract{}, mapErr(err)
	}
	return s.GetContract(ctx, OperatorScope, id)
}

// UpdateContract applies a patch. Absent keys stay unchanged.
func (s *Store) UpdateContract(ctx context.Context, id string, in ContractInput) (Contract, error) {
	current, err := s.GetContract(ctx, OperatorScope, id)
	if err != nil {
		return Contract{}, err
	}
	sets := []string{"updated_at = now()"}
	var args []any
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if in.Name != nil {
		if strings.TrimSpace(*in.Name) == "" {
			return Contract{}, fmt.Errorf("%w: a contract needs a name", ErrInvalid)
		}
		add("name", strings.TrimSpace(*in.Name))
	}
	starts, ends := current.StartsOn, current.EndsOn
	if in.StartsOn != nil {
		if starts = strings.TrimSpace(*in.StartsOn); !ValidDate(starts) {
			return Contract{}, fmt.Errorf("%w: starts_on must be YYYY-MM-DD", ErrInvalid)
		}
		args = append(args, starts)
		sets = append(sets, fmt.Sprintf("starts_on = $%d::date", len(args)))
	}
	if in.TermMonths != nil {
		if *in.TermMonths <= 0 {
			return Contract{}, fmt.Errorf("%w: term_months must be above zero", ErrInvalid)
		}
		add("term_months", *in.TermMonths)
	}
	if in.EndsOn != nil {
		if ends = strings.TrimSpace(*in.EndsOn); !ValidDate(ends) {
			return Contract{}, fmt.Errorf("%w: ends_on must be YYYY-MM-DD", ErrInvalid)
		}
		args = append(args, ends)
		sets = append(sets, fmt.Sprintf("ends_on = $%d::date", len(args)))
	} else if in.StartsOn != nil || in.TermMonths != nil {
		// The term moved and the caller named no end date: re-derive it, so
		// a contract's end never silently contradicts its own term.
		term := current.TermMonths
		if in.TermMonths != nil {
			term = *in.TermMonths
		}
		if ends, err = AddTerm(starts, term); err != nil {
			return Contract{}, err
		}
		args = append(args, ends)
		sets = append(sets, fmt.Sprintf("ends_on = $%d::date", len(args)))
	}
	if ends < starts {
		return Contract{}, fmt.Errorf("%w: ends_on is before starts_on", ErrInvalid)
	}
	if in.AutoRenew != nil {
		add("auto_renew", *in.AutoRenew)
	}
	if in.RenewalNoticeDays != nil {
		if *in.RenewalNoticeDays < 0 {
			return Contract{}, fmt.Errorf("%w: renewal_notice_days must not be negative", ErrInvalid)
		}
		add("renewal_notice_days", *in.RenewalNoticeDays)
	}
	switch {
	case in.ClearMinimum:
		add("minimum_commitment", nil)
	case in.MinimumCommitment != nil:
		args = append(args, string(*in.MinimumCommitment))
		sets = append(sets, fmt.Sprintf("minimum_commitment = $%d::numeric", len(args)))
	}
	if in.Currency != nil {
		c := strings.ToUpper(strings.TrimSpace(*in.Currency))
		if len(c) != 3 {
			return Contract{}, fmt.Errorf("%w: currency must be a three-letter code", ErrInvalid)
		}
		add("currency", c)
	}
	if in.Status != nil {
		st := strings.ToLower(strings.TrimSpace(*in.Status))
		if !oneOf(st, ContractStatuses) {
			return Contract{}, fmt.Errorf("%w: status must be one of %s", ErrInvalid, strings.Join(ContractStatuses, ", "))
		}
		add("status", st)
	}
	switch {
	case in.ClearSignedAt:
		add("signed_at", nil)
	case in.SignedAt != nil:
		add("signed_at", *in.SignedAt)
	}
	if in.PORef != nil {
		add("po_reference", strings.TrimSpace(*in.PORef))
	}
	if in.Notes != nil {
		add("notes", strings.TrimSpace(*in.Notes))
	}
	args = append(args, id)
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`UPDATE contracts SET %s WHERE id = $%d`, strings.Join(sets, ", "), len(args)), args...)
	if err != nil {
		return Contract{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Contract{}, ErrNotFound
	}
	return s.GetContract(ctx, OperatorScope, id)
}

// GetContract reads one contract with its items, inside the scope.
func (s *Store) GetContract(ctx context.Context, scope Scope, id string) (Contract, error) {
	ct, err := scanContract(s.db.QueryRowContext(ctx, `SELECT `+contractColumns+contractFrom+` WHERE ct.id = $1`, id))
	if err != nil {
		return Contract{}, err
	}
	if !scope.Allows(ct.CustomerID) {
		return Contract{}, ErrNotFound
	}
	if ct.Items, err = s.ListContractItems(ctx, ct.ID); err != nil {
		return Contract{}, err
	}
	return ct, nil
}

// ContractFilter narrows a listing.
type ContractFilter struct {
	CustomerID string
	Status     string
	// RenewalsDueOn lists only contracts inside their renewal notice window
	// on that day: active, not yet ended, and ends_on − notice ≤ day.
	RenewalsDueOn string
}

// ListContracts lists contracts inside the scope, soonest to renew first.
func (s *Store) ListContracts(ctx context.Context, scope Scope, f ContractFilter) ([]Contract, error) {
	where := []string{"TRUE"}
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if f.CustomerID != "" {
		if !scope.Allows(f.CustomerID) {
			return nil, ErrNotFound
		}
		where = append(where, "ct.customer_id = "+arg(f.CustomerID))
	} else if !scope.Operator {
		ids := scope.CustomerIDs
		if len(ids) == 0 {
			if scope.CustomerID == "" {
				return []Contract{}, nil
			}
			ids = []string{scope.CustomerID}
		}
		where = append(where, "ct.customer_id = ANY("+arg(pq.Array(ids))+")")
	}
	if f.Status != "" {
		where = append(where, "ct.status = "+arg(strings.ToLower(f.Status)))
	}
	if f.RenewalsDueOn != "" {
		d := arg(f.RenewalsDueOn)
		where = append(where, "ct.status = 'active'",
			"ct.ends_on >= "+d+"::date",
			"ct.ends_on - ct.renewal_notice_days <= "+d+"::date")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+contractColumns+contractFrom+` WHERE `+strings.Join(where, " AND ")+` ORDER BY ct.ends_on, c.name`, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []Contract{}
	for rows.Next() {
		ct, err := scanContract(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ct)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Items, err = s.ListContractItems(ctx, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ActiveContractAt returns the contract governing a customer on a given day:
// active and covering the day. When several do — a renegotiation signed
// mid-term — the one that STARTED LAST wins, because that is the agreement
// in force. found=false when the customer has none.
func (s *Store) ActiveContractAt(ctx context.Context, customerID, day string) (Contract, bool, error) {
	ct, err := scanContract(s.db.QueryRowContext(ctx, `SELECT `+contractColumns+contractFrom+`
		WHERE ct.customer_id = $1 AND ct.status = 'active' AND ct.starts_on <= $2::date AND ct.ends_on >= $2::date
		ORDER BY ct.starts_on DESC, ct.created_at DESC LIMIT 1`, customerID, day))
	if err != nil {
		if err == ErrNotFound {
			return Contract{}, false, nil
		}
		return Contract{}, false, err
	}
	if ct.Items, err = s.ListContractItems(ctx, ct.ID); err != nil {
		return Contract{}, false, err
	}
	return ct, true, nil
}

// DeleteContract removes a contract and its items. A contract a statement was
// rated under keeps that statement: the column is ON DELETE SET NULL.
func (s *Store) DeleteContract(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM contracts WHERE id = $1`, id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// items
// ---------------------------------------------------------------------------

const contractItemColumns = `id, contract_id, kind, sku, unit, quantity::text, committed_price::text, discount_pct::text, rollover, notes, created_at`

func scanContractItem(row interface{ Scan(...any) error }) (ContractItem, error) {
	var it ContractItem
	var qty string
	var price, pct sql.NullString
	if err := row.Scan(&it.ID, &it.ContractID, &it.Kind, &it.SKU, &it.Unit, &qty, &price, &pct, &it.Rollover, &it.Notes, &it.CreatedAt); err != nil {
		return it, mapErr(err)
	}
	it.Quantity = Decimal(qty)
	it.CommittedPrice, it.DiscountPct = decPtr(price), decPtr(pct)
	it.CreatedAt = it.CreatedAt.UTC()
	return it, nil
}

// ListContractItems returns a contract's lines, commitments before
// allowances, then by SKU.
func (s *Store) ListContractItems(ctx context.Context, contractID string) ([]ContractItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+contractItemColumns+` FROM contract_items WHERE contract_id = $1 ORDER BY kind, sku`, contractID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []ContractItem{}
	for rows.Next() {
		it, err := scanContractItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// PutContractItems REPLACES a contract's lines in one transaction: the list
// sent is the whole list. A commitment needs either a committed price or a
// discount percentage — a commitment at list is not a commitment.
func (s *Store) PutContractItems(ctx context.Context, contractID string, items []ContractItem) ([]ContractItem, error) {
	for i, it := range items {
		if !oneOf(it.Kind, []string{ContractItemCommitment, ContractItemAllowance}) {
			return nil, fmt.Errorf("%w: line %d: kind must be commitment or allowance", ErrInvalid, i+1)
		}
		if strings.TrimSpace(it.SKU) == "" {
			return nil, fmt.Errorf("%w: line %d: sku is required", ErrInvalid, i+1)
		}
		if ratOf(it.Quantity).Sign() < 0 {
			return nil, fmt.Errorf("%w: line %d: quantity must not be negative", ErrInvalid, i+1)
		}
		if it.Kind == ContractItemCommitment && it.CommittedPrice == nil && it.DiscountPct == nil {
			return nil, fmt.Errorf("%w: line %d (%s): a committed-use line needs a committed price or a discount percentage — a commitment at list is not a commitment", ErrInvalid, i+1, it.SKU)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM contracts WHERE id = $1)`, contractID).Scan(&exists); err != nil {
		return nil, mapErr(err)
	}
	if !exists {
		return nil, ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM contract_items WHERE contract_id = $1`, contractID); err != nil {
		return nil, mapErr(err)
	}
	for _, it := range items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO contract_items (contract_id, kind, sku, unit, quantity, committed_price, discount_pct, rollover, notes)
			VALUES ($1, $2, $3, $4, $5::numeric, $6::numeric, $7::numeric, $8, $9)`,
			contractID, it.Kind, strings.TrimSpace(it.SKU), strings.TrimSpace(it.Unit), zeroDec(it.Quantity), nullDec(it.CommittedPrice), nullDec(it.DiscountPct), it.Rollover, strings.TrimSpace(it.Notes)); err != nil {
			return nil, mapErr(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE contracts SET updated_at = now() WHERE id = $1`, contractID); err != nil {
		return nil, mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.ListContractItems(ctx, contractID)
}

func zeroDec(d Decimal) string {
	if strings.TrimSpace(string(d)) == "" {
		return "0"
	}
	return string(d)
}

// ---------------------------------------------------------------------------
// renewals and expiry
// ---------------------------------------------------------------------------

// RenewalReport counts what one renewal pass did.
type RenewalReport struct {
	Renewed []string `json:"renewed,omitempty"`
	Expired []string `json:"expired,omitempty"`
}

// RunContractRenewals is the step the collections evaluator runs each day
// (DESIGN.md §15.6): every ACTIVE contract whose end date has passed either
// RENEWS for another term — the same agreement, its window moved forward and
// its renewal counted — or EXPIRES. There is no scheduler of its own and no
// mail: the renewals-due list is what the operator acts on, in the notice
// window, before this ever fires.
func (s *Store) RunContractRenewals(ctx context.Context, now time.Time) (RenewalReport, error) {
	var rep RenewalReport
	day := now.UTC().Format("2006-01-02")
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, to_char(ends_on, 'YYYY-MM-DD'), term_months, auto_renew FROM contracts WHERE status = 'active' AND ends_on < $1::date ORDER BY ends_on`, day)
	if err != nil {
		return rep, mapErr(err)
	}
	type due struct {
		id, name, endsOn string
		term             int
		auto             bool
	}
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.name, &d.endsOn, &d.term, &d.auto); err != nil {
			rows.Close()
			return rep, err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return rep, err
	}
	for _, d := range list {
		if !d.auto {
			if _, err := s.db.ExecContext(ctx, `UPDATE contracts SET status = 'expired', updated_at = now() WHERE id = $1 AND status = 'active'`, d.id); err != nil {
				return rep, mapErr(err)
			}
			rep.Expired = append(rep.Expired, d.id)
			continue
		}
		// Renew: the next term starts the day after the old one ended, so
		// there is no gap and no overlap. A contract whose end is several
		// terms in the past catches up in one pass.
		starts, ends := d.endsOn, d.endsOn
		for ends < day {
			t, err := time.Parse("2006-01-02", ends)
			if err != nil {
				return rep, fmt.Errorf("contract %s: %w", d.name, err)
			}
			starts = t.AddDate(0, 0, 1).Format("2006-01-02")
			if ends, err = AddTerm(starts, d.term); err != nil {
				return rep, err
			}
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE contracts SET starts_on = $2::date, ends_on = $3::date, renewed_at = $4, renewal_count = renewal_count + 1, updated_at = now() WHERE id = $1 AND status = 'active'`,
			d.id, starts, ends, now.UTC()); err != nil {
			return rep, mapErr(err)
		}
		rep.Renewed = append(rep.Renewed, d.id)
	}
	return rep, nil
}

// ---------------------------------------------------------------------------
// SLA credits
// ---------------------------------------------------------------------------

// SLACreditInput is what POST /contracts/{id}/sla-credit carries: the
// statement to credit, the percentage the SLA owes for the breach and the
// availability actually measured.
type SLACreditInput struct {
	StatementID  string
	Pct          Decimal
	Availability Decimal
	Reason       string
	Actor        string
}

// IssueSLACredit credits an availability breach against a named statement
// (DESIGN.md §15.5). It is a CREDIT NOTE — the same numbering, the same
// allocation onto the invoice, the same ledger entry — that additionally
// records the contract, the percentage and the measured availability, so the
// document says what it answers. Nothing parallel is created.
//
// The amount is Pct percent of the statement's TOTAL, which is what an
// availability SLA credits: a percentage of the charges for the period.
func (s *Store) IssueSLACredit(ctx context.Context, contractID string, in SLACreditInput) (CreditNote, error) {
	ct, err := s.GetContract(ctx, OperatorScope, contractID)
	if err != nil {
		return CreditNote{}, err
	}
	pct := ratOf(in.Pct)
	if pct.Sign() <= 0 || pct.Cmp(ratOf("100")) > 0 {
		return CreditNote{}, fmt.Errorf("%w: the SLA percentage must be above 0 and at most 100", ErrInvalid)
	}
	var customerID, currency, total string
	if err := s.db.QueryRowContext(ctx, `SELECT customer_id, currency, total::text FROM statements WHERE id = $1`, in.StatementID).Scan(&customerID, &currency, &total); err != nil {
		return CreditNote{}, mapErr(err)
	}
	if customerID != ct.CustomerID {
		return CreditNote{}, fmt.Errorf("%w: that statement belongs to another customer than contract %s", ErrConflict, ct.Name)
	}
	credit := roundRat(new(big.Rat).Quo(new(big.Rat).Mul(ratOf(Decimal(total)), pct), big.NewRat(100, 1)), 6)
	if credit.Sign() <= 0 {
		return CreditNote{}, fmt.Errorf("%w: %s%% of %s %s is nothing to credit", ErrInvalid, in.Pct, total, currency)
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		reason = fmt.Sprintf("SLA credit under %s: %s%% of the period's charges for measured availability of %s%%", ct.Name, in.Pct, in.Availability)
	}
	note, err := s.CreateCreditNote(ctx, in.StatementID, CreditNoteInput{Reason: reason, Amount: decOf(credit), Kind: CreditNotePartial, Actor: in.Actor})
	if err != nil {
		return CreditNote{}, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE credit_notes SET contract_id = $2, sla_pct = $3::numeric, measured_availability = $4::numeric WHERE id = $1`,
		note.ID, contractID, string(in.Pct), nullDecValue(in.Availability)); err != nil {
		return CreditNote{}, mapErr(err)
	}
	return s.GetCreditNote(ctx, OperatorScope, note.ID)
}

func nullDecValue(d Decimal) any {
	if strings.TrimSpace(string(d)) == "" {
		return nil
	}
	return string(d)
}

// ContractItemsJSON is used by the audit trail: the lines as the operator
// sent them, without the generated ids.
func ContractItemsJSON(items []ContractItem) string {
	type line struct {
		Kind, SKU, Unit string
		Quantity        Decimal
		CommittedPrice  *Decimal
		DiscountPct     *Decimal
		Rollover        bool
	}
	out := make([]line, 0, len(items))
	for _, it := range items {
		out = append(out, line{it.Kind, it.SKU, it.Unit, it.Quantity, it.CommittedPrice, it.DiscountPct, it.Rollover})
	}
	b, _ := json.Marshal(out)
	return string(b)
}
