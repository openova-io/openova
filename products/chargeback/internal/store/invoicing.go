package store

import (
	"context"
	"database/sql"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Post-paid invoicing and the commercial model (DESIGN.md §8, founder
// direction 2026-09-10).
//
// A statement was a rated period with two states, draft and issued, and the
// only way money ever moved was the Stripe-backed billing hook. An Omantel
// corporate customer who raises a purchase order and pays by transfer on
// thirty-day terms fitted nowhere, and an SME who must pay through Omantel's
// gateway could not be served at all.
//
// The old showback / chargeback / real trio was three labels standing in for
// three DIFFERENT questions, which is why nothing fitted between them. They
// are replaced by four orthogonal fields:
//
//	charging       — is anything collected?   billed | informational
//	payment_model  — when is it paid?         prepaid | postpaid
//	payment_method — how does the money move?  gateway | transfer | internal
//	gateway_name   — which gateway collects?   stripe today, omantel later
//
// The last three are meaningful only when charging is billed. billing_mode
// stays as a DEPRECATED derived column so old readers keep working; it is
// never written directly.
//
// The statement grows the fields an invoice needs (number, purchase-order
// reference, terms, due date) plus the lifecycle a receivable has.

// ---------------------------------------------------------------------------
// migration
// ---------------------------------------------------------------------------

// invoicingMigrationSQL is one transaction: the settlement column, the
// invoice fields on a statement, the payments ledger, the gapless per-year
// invoice sequence and the configurable prefix. Every statement is
// idempotent against a database that already carries the shape.
const invoicingMigrationSQL = `
-- The commercial position of a customer, as four orthogonal fields instead
-- of the three labels billing_mode compressed them into. The last three are
-- NULL / empty unless charging is 'billed', and the CHECK below is what
-- makes that structural rather than a convention.
ALTER TABLE customers ADD COLUMN IF NOT EXISTS charging TEXT NOT NULL DEFAULT 'informational';
ALTER TABLE customers DROP CONSTRAINT IF EXISTS customers_charging_check;
ALTER TABLE customers ADD CONSTRAINT customers_charging_check CHECK (charging IN ('billed','informational'));
ALTER TABLE customers ADD COLUMN IF NOT EXISTS payment_model TEXT;
ALTER TABLE customers DROP CONSTRAINT IF EXISTS customers_payment_model_check;
ALTER TABLE customers ADD CONSTRAINT customers_payment_model_check CHECK (payment_model IS NULL OR payment_model IN ('prepaid','postpaid'));
ALTER TABLE customers ADD COLUMN IF NOT EXISTS payment_method TEXT;
ALTER TABLE customers DROP CONSTRAINT IF EXISTS customers_payment_method_check;
ALTER TABLE customers ADD CONSTRAINT customers_payment_method_check CHECK (payment_method IS NULL OR payment_method IN ('gateway','transfer','internal'));
ALTER TABLE customers ADD COLUMN IF NOT EXISTS gateway_name TEXT NOT NULL DEFAULT '';
ALTER TABLE customers DROP CONSTRAINT IF EXISTS customers_commercial_check;
ALTER TABLE customers ADD CONSTRAINT customers_commercial_check CHECK (
	(charging = 'informational' AND payment_model IS NULL AND payment_method IS NULL AND gateway_name = '')
	OR (charging = 'billed' AND payment_model IS NOT NULL AND payment_method IS NOT NULL
	    AND ((payment_method = 'gateway' AND gateway_name <> '') OR (payment_method <> 'gateway' AND gateway_name = '')))
);

-- The mapping out of the retired modes, exactly. billing_mode itself stays
-- as a DEPRECATED column, derived from these four on every write, so an
-- older reader (and the wire key) keeps seeing what it always saw.
UPDATE customers SET charging = 'informational', payment_model = NULL, payment_method = NULL, gateway_name = '' WHERE billing_mode = 'showback';
UPDATE customers SET charging = 'billed', payment_model = 'postpaid', payment_method = 'internal', gateway_name = '' WHERE billing_mode = 'chargeback';
UPDATE customers SET charging = 'billed', payment_model = 'prepaid', payment_method = 'gateway', gateway_name = 'stripe' WHERE billing_mode = 'real';

ALTER TABLE customers ADD COLUMN IF NOT EXISTS po_reference TEXT NOT NULL DEFAULT '';
ALTER TABLE customers ADD COLUMN IF NOT EXISTS payment_terms_days INT NOT NULL DEFAULT 30;
ALTER TABLE customers DROP CONSTRAINT IF EXISTS customers_payment_terms_days_check;
ALTER TABLE customers ADD CONSTRAINT customers_payment_terms_days_check CHECK (payment_terms_days >= 0 AND payment_terms_days <= 365);

-- The invoice a statement becomes at issue. invoice_number is assigned
-- inside the transaction that flips the status, and is unique across the
-- whole table: a partial unique index, because every draft carries NULL.
ALTER TABLE statements ADD COLUMN IF NOT EXISTS invoice_number TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS statements_invoice_number_idx ON statements (invoice_number) WHERE invoice_number IS NOT NULL;
ALTER TABLE statements ADD COLUMN IF NOT EXISTS po_reference TEXT NOT NULL DEFAULT '';
ALTER TABLE statements ADD COLUMN IF NOT EXISTS payment_terms_days INT;
ALTER TABLE statements ADD COLUMN IF NOT EXISTS due_at TIMESTAMPTZ;
ALTER TABLE statements ADD COLUMN IF NOT EXISTS sent_at TIMESTAMPTZ;
ALTER TABLE statements ADD COLUMN IF NOT EXISTS paid_at TIMESTAMPTZ;
ALTER TABLE statements ADD COLUMN IF NOT EXISTS cancelled_at TIMESTAMPTZ;
ALTER TABLE statements ADD COLUMN IF NOT EXISTS cancel_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE statements DROP CONSTRAINT IF EXISTS statements_status_check;
ALTER TABLE statements ADD CONSTRAINT statements_status_check CHECK (status IN ('draft','issued','sent','paid','cancelled'));
CREATE INDEX IF NOT EXISTS statements_due_idx ON statements (due_at) WHERE status = 'sent';

-- Money the customer paid. A payment is a row of its OWN — it belongs to the
-- customer, carries its own amount, date, method, reference and status, and
-- is LINKED to the invoice it was recorded against. It is not a column on a
-- statement, and statement_id is nullable, because account credit is
-- universal: a customer of any payment model may hold credit that is not yet
-- against any invoice. This lane always sets the link (every payment is
-- recorded through POST /statements/{id}/payments); allocating one payment
-- across several invoices, and holding unallocated credit, is a later lane
-- that adds rows and an allocation table without changing this shape.
--
-- Part payment is normal, so the balance is total − sum of the RECEIVED
-- payments linked to the statement. A reference is the bank or gateway
-- transaction id: unique per customer, so a gateway that delivers the same
-- confirmation twice books one payment.
CREATE TABLE IF NOT EXISTS payments (
	id BIGSERIAL PRIMARY KEY,
	customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
	statement_id UUID REFERENCES statements(id) ON DELETE CASCADE,
	amount NUMERIC(20,6) NOT NULL CHECK (amount > 0),
	paid_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	method TEXT NOT NULL DEFAULT 'transfer' CHECK (method IN ('gateway','transfer','internal')),
	reference TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'received' CHECK (status IN ('received','pending','failed')),
	gateway TEXT NOT NULL DEFAULT 'manual',
	recorded_by TEXT NOT NULL DEFAULT '',
	recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS payments_statement_idx ON payments (statement_id, paid_at);
CREATE INDEX IF NOT EXISTS payments_customer_idx ON payments (customer_id, paid_at);
CREATE UNIQUE INDEX IF NOT EXISTS payments_reference_idx ON payments (customer_id, reference) WHERE reference <> '';

-- The gapless per-calendar-year invoice counter. One row per year, taken
-- with ON CONFLICT DO UPDATE inside the issuing transaction: concurrent
-- issues serialise on the row lock, and a transaction that rolls back gives
-- its number back instead of leaving a hole in the sequence.
CREATE TABLE IF NOT EXISTS invoice_sequences (
	year INT PRIMARY KEY,
	last_value BIGINT NOT NULL DEFAULT 0 CHECK (last_value >= 0)
);

ALTER TABLE billing_settings ADD COLUMN IF NOT EXISTS invoice_prefix TEXT NOT NULL DEFAULT 'INV';
ALTER TABLE billing_settings DROP CONSTRAINT IF EXISTS billing_settings_invoice_prefix_check;
ALTER TABLE billing_settings ADD CONSTRAINT billing_settings_invoice_prefix_check CHECK (invoice_prefix ~ '^[A-Z0-9][A-Z0-9-]{0,11}$');

-- WHICH system of record owns invoicing on this Sovereign (DESIGN.md §8.10).
-- 'internal' is this product; 'external' is the operator's own billing system,
-- which already runs invoicing, payment and collections. Default 'internal',
-- so an upgraded Sovereign behaves exactly as it did.
ALTER TABLE billing_settings ADD COLUMN IF NOT EXISTS commercial_provider TEXT NOT NULL DEFAULT 'internal';
ALTER TABLE billing_settings DROP CONSTRAINT IF EXISTS billing_settings_commercial_provider_check;
ALTER TABLE billing_settings ADD CONSTRAINT billing_settings_commercial_provider_check CHECK (commercial_provider IN ('internal','external'));

-- The customer's identifier in the operator's billing system (TMF666 billing
-- account id). Empty when this Sovereign invoices internally.
ALTER TABLE customers ADD COLUMN IF NOT EXISTS external_account_id TEXT NOT NULL DEFAULT '';

-- The reference the external billing system knows this invoice by. Recorded
-- when the export is DELIVERED — or when an inbound import first names it —
-- never an invoice number: numbering belongs to whoever is the system of
-- record, and we never assign one for them.
ALTER TABLE statements ADD COLUMN IF NOT EXISTS external_invoice_ref TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS statements_external_ref_idx ON statements (external_invoice_ref) WHERE external_invoice_ref IS NOT NULL;

-- The OUTBOX. Issuing writes the statement and the document to export in ONE
-- transaction and returns; a delivery loop pushes undelivered rows out with
-- backoff. Rating and issuing therefore never wait on, and never fail
-- because of, the operator's billing system.
--
-- idempotency_key is the statement id, and it is unique per document type,
-- so at-least-once delivery of the same row is one bill at the far end.
CREATE TABLE IF NOT EXISTS commercial_outbox (
	id BIGSERIAL PRIMARY KEY,
	doc_type TEXT NOT NULL DEFAULT 'invoice',
	idempotency_key TEXT NOT NULL,
	statement_id UUID REFERENCES statements(id) ON DELETE CASCADE,
	customer_id UUID REFERENCES customers(id) ON DELETE CASCADE,
	document JSONB NOT NULL,
	attempts INT NOT NULL DEFAULT 0,
	next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	delivered_at TIMESTAMPTZ,
	last_error TEXT NOT NULL DEFAULT '',
	external_ref TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (doc_type, idempotency_key)
);
CREATE INDEX IF NOT EXISTS commercial_outbox_due_idx ON commercial_outbox (next_attempt_at) WHERE delivered_at IS NULL;
`

// ---------------------------------------------------------------------------
// the commercial model
// ---------------------------------------------------------------------------

// Charging — is anything collected at all?
const (
	// ChargingBilled — the statement is an invoice and is collected.
	ChargingBilled = "billed"
	// ChargingInformational — the statement exists for visibility only and
	// nothing is ever collected. This is what showback meant.
	ChargingInformational = "informational"
)

// Payment model — when the money is paid, relative to the usage.
const (
	// PaymentModelPrepaid — the customer pays ahead, or holds a balance
	// that is debited when the statement is issued.
	PaymentModelPrepaid = "prepaid"
	// PaymentModelPostpaid — the customer is invoiced after the period and
	// pays on terms. The Omantel corporate case.
	PaymentModelPostpaid = "postpaid"
)

// Payment method — how the money actually moves.
const (
	// PaymentMethodGateway — a pluggable payment gateway collects; which
	// one is GatewayName.
	PaymentMethodGateway = "gateway"
	// PaymentMethodTransfer — bank transfer against the invoice and its
	// purchase order, recorded by the operator when the bank shows it.
	PaymentMethodTransfer = "transfer"
	// PaymentMethodInternal — a cost-centre recharge. No external money
	// moves and no gateway is called.
	PaymentMethodInternal = "internal"
)

// GatewayStripe is the one gateway implementation that exists today, behind
// the seam. Omantel's is another name registered against the same seam.
const GatewayStripe = "stripe"

// Billing modes — the RETIRED trio. The column is kept, derived from the
// four fields on every write, so readers written against it keep working.
const (
	BillingModeShowback   = "showback"
	BillingModeChargeback = "chargeback"
	BillingModeReal       = "real"
)

// Chargings, PaymentModels and PaymentMethods list the accepted values in
// display order.
var (
	Chargings      = []string{ChargingBilled, ChargingInformational}
	PaymentModels  = []string{PaymentModelPrepaid, PaymentModelPostpaid}
	PaymentMethods = []string{PaymentMethodGateway, PaymentMethodTransfer, PaymentMethodInternal}
)

func oneOf(v string, set []string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// Commercial is a customer's commercial position: the four orthogonal fields
// that replaced billing_mode. The zero value is "nothing chosen", which
// defaults to informational.
type Commercial struct {
	Charging      string
	PaymentModel  string
	PaymentMethod string
	GatewayName   string
}

// IsZero reports whether nothing at all was given.
func (c Commercial) IsZero() bool {
	return c.Charging == "" && c.PaymentModel == "" && c.PaymentMethod == "" && c.GatewayName == ""
}

// Normalized trims and case-folds every field and applies the informational
// default; it does not validate.
func (c Commercial) Normalized() Commercial {
	l := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	out := Commercial{Charging: l(c.Charging), PaymentModel: l(c.PaymentModel), PaymentMethod: l(c.PaymentMethod), GatewayName: l(c.GatewayName)}
	if out.Charging == "" {
		out.Charging = ChargingInformational
	}
	return out
}

// Validate enforces the "only meaningful when billed" rules. Every message
// names the field and what would make it consistent.
func (c Commercial) Validate() error {
	if !oneOf(c.Charging, Chargings) {
		return fmt.Errorf("%w: charging must be %s", ErrInvalid, strings.Join(Chargings, " or "))
	}
	if c.Charging == ChargingInformational {
		switch {
		case c.PaymentModel != "":
			return fmt.Errorf("%w: payment_model is only meaningful when charging is billed; an informational customer is never collected from", ErrInvalid)
		case c.PaymentMethod != "":
			return fmt.Errorf("%w: payment_method is only meaningful when charging is billed; an informational customer is never collected from", ErrInvalid)
		case c.GatewayName != "":
			return fmt.Errorf("%w: gateway_name is only meaningful when payment_method is gateway", ErrInvalid)
		}
		return nil
	}
	if !oneOf(c.PaymentModel, PaymentModels) {
		return fmt.Errorf("%w: payment_model must be %s when charging is billed", ErrInvalid, strings.Join(PaymentModels, " or "))
	}
	if !oneOf(c.PaymentMethod, PaymentMethods) {
		return fmt.Errorf("%w: payment_method must be %s when charging is billed", ErrInvalid, strings.Join(PaymentMethods, ", "))
	}
	if c.PaymentMethod == PaymentMethodGateway && c.GatewayName == "" {
		return fmt.Errorf("%w: gateway_name is required when payment_method is gateway (e.g. %s)", ErrInvalid, GatewayStripe)
	}
	if c.PaymentMethod != PaymentMethodGateway && c.GatewayName != "" {
		return fmt.Errorf("%w: gateway_name is only meaningful when payment_method is gateway", ErrInvalid)
	}
	return nil
}

// BillingMode derives the deprecated column: informational reads as
// showback, an internal recharge as chargeback, and anything else as real.
func (c Commercial) BillingMode() string {
	if c.Charging != ChargingBilled {
		return BillingModeShowback
	}
	if c.PaymentMethod == PaymentMethodInternal {
		return BillingModeChargeback
	}
	return BillingModeReal
}

// CommercialFromBillingMode is the mapping OUT of the retired trio — the one
// the migration applies, in Go, so the CSV importer and the Organization
// sync translate a legacy mode exactly the same way the database did.
func CommercialFromBillingMode(mode string) Commercial {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case BillingModeChargeback:
		return Commercial{Charging: ChargingBilled, PaymentModel: PaymentModelPostpaid, PaymentMethod: PaymentMethodInternal}
	case BillingModeReal:
		return Commercial{Charging: ChargingBilled, PaymentModel: PaymentModelPrepaid, PaymentMethod: PaymentMethodGateway, GatewayName: GatewayStripe}
	default:
		return Commercial{Charging: ChargingInformational}
	}
}

// Merge applies a partial patch: a nil field is unchanged.
//
// Turning charging off, or moving off the gateway method, CLEARS the fields
// that then mean nothing — so an operator flipping one control never has to
// clear three others to get past the CHECK. What it does not do is clear a
// field the patch itself named: asking for a payment model on an
// informational customer is a contradiction, and is refused by Validate
// rather than silently dropped.
func (c Commercial) Merge(charging, model, method, gateway *string) Commercial {
	out := c
	if charging != nil {
		out.Charging = *charging
	}
	if model != nil {
		out.PaymentModel = *model
	}
	if method != nil {
		out.PaymentMethod = *method
	}
	if gateway != nil {
		out.GatewayName = *gateway
	}
	out = out.Normalized()
	if out.Charging == ChargingInformational {
		if model == nil {
			out.PaymentModel = ""
		}
		if method == nil {
			out.PaymentMethod = ""
		}
		if gateway == nil {
			out.GatewayName = ""
		}
	} else if gateway == nil && out.PaymentMethod != "" && out.PaymentMethod != PaymentMethodGateway {
		out.GatewayName = ""
	}
	return out
}

// Commercial reads the customer's four fields.
func (c Customer) Commercial() Commercial {
	return Commercial{Charging: c.Charging, PaymentModel: c.PaymentModel, PaymentMethod: c.PaymentMethod, GatewayName: c.GatewayName}
}

// IsBilled reports whether anything is collected for this customer at all.
func (c Customer) IsBilled() bool { return c.Charging == ChargingBilled }

// DefaultPaymentTermsDays is the net-30 the founder named; overridable per
// customer and again per statement before it is issued.
const DefaultPaymentTermsDays = 30

// MaxPaymentTermsDays bounds the column so a typo cannot invent a due date
// years out. Zero is allowed and means due on receipt.
const MaxPaymentTermsDays = 365

// ---------------------------------------------------------------------------
// statement lifecycle
// ---------------------------------------------------------------------------

// Statement statuses. Five are stored; StatusOverdue is DERIVED from due_at
// and the outstanding balance, so no sweeper has to walk the table at
// midnight to keep the ledger honest.
const (
	StatusDraft     = "draft"
	StatusIssued    = "issued"
	StatusSent      = "sent"
	StatusPaid      = "paid"
	StatusOverdue   = "overdue"
	StatusCancelled = "cancelled"
)

// StatementStatuses lists every status a reader can meet, stored or derived.
var StatementStatuses = []string{StatusDraft, StatusIssued, StatusSent, StatusPaid, StatusOverdue, StatusCancelled}

// statementTransitions is the whole legal lifecycle. Anything absent here is
// refused by the store, not merely discouraged in the UI.
var statementTransitions = map[string][]string{
	StatusDraft:     {StatusIssued, StatusCancelled},
	StatusIssued:    {StatusSent, StatusPaid, StatusCancelled},
	StatusSent:      {StatusPaid, StatusOverdue},
	StatusOverdue:   {StatusPaid},
	StatusPaid:      {},
	StatusCancelled: {},
}

// LegalStatementTransition reports whether from → to is in the lifecycle.
func LegalStatementTransition(from, to string) bool {
	for _, s := range statementTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// NextStatementStatuses returns the statuses reachable from one, for the UI.
func NextStatementStatuses(from string) []string {
	out := append([]string{}, statementTransitions[from]...)
	return out
}

// Payment statuses. Only a RECEIVED payment counts towards a statement's
// balance; pending is announced but not cleared, failed is a reversal.
const (
	PaymentReceived = "received"
	PaymentPending  = "pending"
	PaymentFailed   = "failed"
)

// StatementPayment is one payment the customer made. It belongs to the
// CUSTOMER and is linked to the statement it was recorded against —
// StatementID is empty for credit that is not against any invoice, which a
// later lane adds. Part payment is ordinary, so these accumulate and a
// statement's balance is derived from the received ones linked to it.
type StatementPayment struct {
	ID         int64  `json:"id"`
	CustomerID string `json:"customer_id,omitempty"`
	// StatementID is the invoice this payment was recorded against; empty
	// means unallocated credit.
	StatementID string    `json:"statement_id,omitempty"`
	Amount      Decimal   `json:"amount"`
	PaidAt      time.Time `json:"paid_at"`
	// Method is how the money arrived — gateway, transfer or internal.
	Method    string `json:"method,omitempty"`
	Reference string `json:"reference,omitempty"`
	// Status is received, pending or failed; only received counts.
	Status string `json:"status,omitempty"`
	// Gateway names the settlement implementation that produced the
	// payment: "manual" when the operator recorded a transfer, otherwise
	// the gateway that confirmed it.
	Gateway    string    `json:"gateway,omitempty"`
	RecordedBy string    `json:"recorded_by,omitempty"`
	RecordedAt time.Time `json:"recorded_at"`
}

// PaymentInput is one recorded payment: an amount, the day the money
// arrived, how it arrived, and the bank or gateway reference that proves it.
type PaymentInput struct {
	Amount    Decimal
	PaidAt    time.Time
	Method    string
	Reference string
	Status    string
	Gateway   string
	Actor     string
}

// StatementInvoicePatch edits the invoice fields of a DRAFT: the
// purchase-order reference to quote and the terms to compute the due date
// from. Nil means unchanged. Both are frozen once the statement is issued.
type StatementInvoicePatch struct {
	PORef            *string
	PaymentTermsDays *int
}

// EffectiveStatusAt returns the status a reader should see at t: the stored
// one, except that a SENT statement whose due date has passed with money
// still outstanding reads as overdue. Derived rather than stored, so
// "overdue" is always true of the clock rather than of the last sweep.
func (s Statement) EffectiveStatusAt(t time.Time) string {
	if s.Status != StatusSent || s.DueAt == nil {
		return s.Status
	}
	if !t.After(*s.DueAt) {
		return s.Status
	}
	if ratOf(s.OutstandingAt()).Sign() <= 0 {
		return s.Status
	}
	return StatusOverdue
}

// OutstandingAt is total − payments, exactly.
func (s Statement) OutstandingAt() Decimal {
	return decOf(new(big.Rat).Sub(ratOf(s.Total), ratOf(s.Paid)))
}

// ---------------------------------------------------------------------------
// invoice numbering
// ---------------------------------------------------------------------------

// InvoiceNumberFor formats one invoice number: <prefix>-<year>-<seq>, the
// sequence zero-padded to five digits so a year's invoices sort as text.
func InvoiceNumberFor(prefix string, year int, seq int64) string {
	return fmt.Sprintf("%s-%04d-%05d", prefix, year, seq)
}

// DefaultInvoicePrefix is what a fresh install numbers invoices with.
const DefaultInvoicePrefix = "INV"

// NormalizeInvoicePrefix upper-cases and trims a prefix.
func NormalizeInvoicePrefix(p string) string {
	return strings.ToUpper(strings.TrimSpace(p))
}

// ValidInvoicePrefix mirrors the column CHECK: 1–12 upper-case letters,
// digits or dashes, starting with a letter or digit.
func ValidInvoicePrefix(p string) bool {
	if len(p) == 0 || len(p) > 12 {
		return false
	}
	for i, r := range p {
		alnum := (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if alnum {
			continue
		}
		if r == '-' && i > 0 {
			continue
		}
		return false
	}
	return true
}

// nextInvoiceNumber allocates the next number of the year INSIDE tx. The
// ON CONFLICT DO UPDATE takes the year row's lock, so a concurrent issue
// blocks here until this transaction commits or rolls back — which is what
// makes the sequence gapless as well as unique.
func nextInvoiceNumber(ctx context.Context, tx *sql.Tx, prefix string) (string, error) {
	var year int
	var seq int64
	err := tx.QueryRowContext(ctx, `
		WITH y AS (SELECT EXTRACT(YEAR FROM (now() AT TIME ZONE 'UTC'))::int AS yr)
		INSERT INTO invoice_sequences (year, last_value) SELECT yr, 1 FROM y
		ON CONFLICT (year) DO UPDATE SET last_value = invoice_sequences.last_value + 1
		RETURNING year, last_value`).Scan(&year, &seq)
	if err != nil {
		return "", mapErr(err)
	}
	return InvoiceNumberFor(prefix, year, seq), nil
}

// ---------------------------------------------------------------------------
// reads
// ---------------------------------------------------------------------------

// ListStatementPayments returns a statement's payments oldest first, inside
// the caller's scope.
func (s *Store) ListStatementPayments(ctx context.Context, scope Scope, statementID string) ([]StatementPayment, error) {
	var customerID string
	if err := s.db.QueryRowContext(ctx, `SELECT customer_id FROM statements WHERE id = $1`, statementID).Scan(&customerID); err != nil {
		return nil, mapErr(err)
	}
	if !scope.Allows(customerID) {
		return nil, ErrNotFound
	}
	return s.statementPayments(ctx, statementID)
}

func (s *Store) statementPayments(ctx context.Context, statementID string) ([]StatementPayment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, customer_id, COALESCE(statement_id::text, ''), amount::text, paid_at, method, reference, status, gateway, recorded_by, recorded_at
		FROM payments WHERE statement_id = $1 ORDER BY paid_at, id`, statementID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []StatementPayment{}
	for rows.Next() {
		var p StatementPayment
		var amt string
		if err := rows.Scan(&p.ID, &p.CustomerID, &p.StatementID, &amt, &p.PaidAt, &p.Method, &p.Reference, &p.Status, &p.Gateway, &p.RecordedBy, &p.RecordedAt); err != nil {
			return nil, err
		}
		p.Amount = Decimal(amt)
		p.PaidAt, p.RecordedAt = p.PaidAt.UTC(), p.RecordedAt.UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// transitions
// ---------------------------------------------------------------------------

// lockStatement reads a statement FOR UPDATE inside tx and returns the
// fields every transition needs, with the derived status already resolved.
func lockStatement(ctx context.Context, tx *sql.Tx, id string) (status string, total, paid Decimal, due *time.Time, err error) {
	var t, p string
	var d sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT st.status, st.total::text, st.due_at,
		COALESCE((SELECT sum(x.amount) FROM payments x WHERE x.statement_id = st.id AND x.status = 'received'), 0)::numeric(20,6)::text
		FROM statements st WHERE st.id = $1 FOR UPDATE`, id).Scan(&status, &t, &d, &p)
	if err != nil {
		return "", "", "", nil, mapErr(err)
	}
	return status, Decimal(t), Decimal(p), timePtr(d), nil
}

// effectiveStatus is EffectiveStatusAt over the locked columns.
func effectiveStatus(status string, total, paid Decimal, due *time.Time, now time.Time) string {
	return Statement{Status: status, Total: total, Paid: paid, DueAt: due}.EffectiveStatusAt(now)
}

// refuseTransition renders the one message every illegal transition answers
// with: what it is now, what was asked, and what it could become instead.
func refuseTransition(from, to string) error {
	next := NextStatementStatuses(from)
	if len(next) == 0 {
		return fmt.Errorf("%w: a %s statement is final; it cannot become %s", ErrConflict, from, to)
	}
	return fmt.Errorf("%w: a %s statement cannot become %s; it can only become %s", ErrConflict, from, to, strings.Join(next, " or "))
}

// SendStatement records that the invoice was sent to the customer
// (issued → sent). Idempotent: a second call on a sent statement returns it
// unchanged with transitioned = false, so a repeated click never re-mails.
func (s *Store) SendStatement(ctx context.Context, id string) (st Statement, transitioned bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Statement{}, false, err
	}
	defer tx.Rollback()
	status, total, paid, due, err := lockStatement(ctx, tx, id)
	if err != nil {
		return Statement{}, false, err
	}
	cur := effectiveStatus(status, total, paid, due, time.Now().UTC())
	switch {
	case cur == StatusSent || cur == StatusOverdue:
		// Already sent; the send is the edge, not the request.
	case !LegalStatementTransition(cur, StatusSent):
		return Statement{}, false, refuseTransition(cur, StatusSent)
	default:
		if _, err := tx.ExecContext(ctx, `UPDATE statements SET status = 'sent', sent_at = COALESCE(sent_at, now()) WHERE id = $1 AND status = 'issued'`, id); err != nil {
			return Statement{}, false, mapErr(err)
		}
		transitioned = true
	}
	if err := tx.Commit(); err != nil {
		return Statement{}, false, err
	}
	st, err = s.GetStatement(ctx, OperatorScope, id)
	return st, transitioned, err
}

// CancelStatement voids a statement (draft → cancelled, issued → cancelled).
// A SENT invoice is deliberately not cancellable: the customer holds it, and
// the correction for that is a credit note, not a status flip. Cancelling is
// idempotent.
func (s *Store) CancelStatement(ctx context.Context, id, reason string) (st Statement, transitioned bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Statement{}, false, err
	}
	defer tx.Rollback()
	status, total, paid, due, err := lockStatement(ctx, tx, id)
	if err != nil {
		return Statement{}, false, err
	}
	cur := effectiveStatus(status, total, paid, due, time.Now().UTC())
	switch {
	case cur == StatusCancelled:
	case !LegalStatementTransition(cur, StatusCancelled):
		return Statement{}, false, refuseTransition(cur, StatusCancelled)
	default:
		if _, err := tx.ExecContext(ctx, `UPDATE statements SET status = 'cancelled', cancelled_at = now(), cancel_reason = $2 WHERE id = $1`, id, strings.TrimSpace(reason)); err != nil {
			return Statement{}, false, mapErr(err)
		}
		transitioned = true
	}
	if err := tx.Commit(); err != nil {
		return Statement{}, false, err
	}
	st, err = s.GetStatement(ctx, OperatorScope, id)
	return st, transitioned, err
}

// RecordStatementPayment books one payment and links it to a statement.
//
// The payment is a row of its own against the CUSTOMER; linking it to this
// invoice is what makes it settle this invoice. Part payment is normal: the
// balance carries and the status is unchanged until the payments reach the
// total, at which point the statement becomes paid. Overpayment is refused —
// a customer who sent too much is a credit note, not a bigger invoice.
func (s *Store) RecordStatementPayment(ctx context.Context, id string, in PaymentInput) (st Statement, p StatementPayment, err error) {
	amt := ratOf(in.Amount)
	if amt.Sign() <= 0 {
		return Statement{}, p, fmt.Errorf("%w: a payment amount must be above zero", ErrInvalid)
	}
	if in.PaidAt.IsZero() {
		in.PaidAt = time.Now().UTC()
	}
	if in.Gateway == "" {
		in.Gateway = "manual"
	}
	if in.Method == "" {
		in.Method = PaymentMethodTransfer
	}
	if !oneOf(in.Method, PaymentMethods) {
		return Statement{}, p, fmt.Errorf("%w: a payment method must be %s", ErrInvalid, strings.Join(PaymentMethods, ", "))
	}
	if in.Status == "" {
		in.Status = PaymentReceived
	}
	if !oneOf(in.Status, []string{PaymentReceived, PaymentPending, PaymentFailed}) {
		return Statement{}, p, fmt.Errorf("%w: a payment status must be received, pending or failed", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Statement{}, p, err
	}
	defer tx.Rollback()
	status, total, paid, due, err := lockStatement(ctx, tx, id)
	if err != nil {
		return Statement{}, p, err
	}
	now := time.Now().UTC()
	cur := effectiveStatus(status, total, paid, due, now)
	if !LegalStatementTransition(cur, StatusPaid) {
		return Statement{}, p, refuseTransition(cur, StatusPaid)
	}
	newPaid := new(big.Rat).Add(ratOf(paid), amt)
	if in.Status != PaymentReceived {
		// A pending or failed payment is recorded but settles nothing, so
		// it cannot exceed the balance and cannot flip the statement.
		newPaid = ratOf(paid)
	}
	if newPaid.Cmp(ratOf(total)) > 0 {
		outstanding := decOf(new(big.Rat).Sub(ratOf(total), ratOf(paid)))
		return Statement{}, p, fmt.Errorf("%w: payment of %s exceeds the outstanding balance of %s", ErrConflict, decOf(amt), outstanding)
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO payments (customer_id, statement_id, amount, paid_at, method, reference, status, gateway, recorded_by)
		SELECT st.customer_id, st.id, $2::numeric, $3, $4, $5, $6, $7, $8 FROM statements st WHERE st.id = $1
		RETURNING id, customer_id, COALESCE(statement_id::text, ''), amount::text, paid_at, method, reference, status, gateway, recorded_by, recorded_at`,
		id, string(decOf(amt)), in.PaidAt.UTC(), in.Method, strings.TrimSpace(in.Reference), in.Status, in.Gateway, in.Actor).
		Scan(&p.ID, &p.CustomerID, &p.StatementID, (*string)(&p.Amount), &p.PaidAt, &p.Method, &p.Reference, &p.Status, &p.Gateway, &p.RecordedBy, &p.RecordedAt)
	if err != nil {
		return Statement{}, StatementPayment{}, mapErr(err)
	}
	p.PaidAt, p.RecordedAt = p.PaidAt.UTC(), p.RecordedAt.UTC()
	if newPaid.Cmp(ratOf(total)) == 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE statements SET status = 'paid', paid_at = $2 WHERE id = $1`, id, in.PaidAt.UTC()); err != nil {
			return Statement{}, StatementPayment{}, mapErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Statement{}, StatementPayment{}, err
	}
	st, err = s.GetStatement(ctx, OperatorScope, id)
	return st, p, err
}

// ---------------------------------------------------------------------------
// external system of record (DESIGN.md §8.10)
// ---------------------------------------------------------------------------

// ExternalIssue is one external issue: the statement, the document to queue,
// and the invoice terms the caller resolved — which are already inside that
// document, so the row and the export can never disagree.
type ExternalIssue struct {
	StatementID string
	DocType     string
	Document    []byte
	PORef       string
	TermsDays   int
	IssuedAt    time.Time
}

// IssueStatementExternally flips a draft to issued WITHOUT taking an invoice
// number — the operator's billing system numbers its own invoices — and, in
// the SAME transaction, queues the document to export in the outbox. Nothing
// here talks to the billing system: issuing a bill must not wait on, or fail
// because of, a system in someone else's estate.
//
// Idempotent on an already-issued statement, which returns it unchanged with
// transitioned = false and queues nothing further.
func (s *Store) IssueStatementExternally(ctx context.Context, in ExternalIssue) (st Statement, transitioned bool, err error) {
	id := in.StatementID
	if len(in.Document) == 0 {
		return Statement{}, false, fmt.Errorf("%w: the export document is empty", ErrInvalid)
	}
	docType := in.DocType
	if docType == "" {
		docType = OutboxInvoice
	}
	if in.IssuedAt.IsZero() {
		in.IssuedAt = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Statement{}, false, err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM statements WHERE id = $1 FOR UPDATE`, id).Scan(&status); err != nil {
		return Statement{}, false, mapErr(err)
	}
	switch {
	case status == StatusIssued:
	case status != StatusDraft:
		return Statement{}, false, refuseTransition(status, StatusIssued)
	default:
		// The terms were resolved by the caller and are already IN the
		// document being queued, so the statement and the export agree by
		// construction rather than by two separate computations.
		if _, err := tx.ExecContext(ctx, `UPDATE statements SET status = 'issued', issued_at = $4,
			po_reference = $2, payment_terms_days = $3,
			due_at = $4::timestamptz + make_interval(days => $3)
			WHERE id = $1 AND status = 'draft'`, id, in.PORef, in.TermsDays, in.IssuedAt.UTC()); err != nil {
			return Statement{}, false, mapErr(err)
		}
		// The document to export, queued in the same transaction. The
		// statement id is the idempotency key, so at-least-once delivery of
		// this row is one bill at the far end.
		if _, err := tx.ExecContext(ctx, `INSERT INTO commercial_outbox (doc_type, idempotency_key, statement_id, customer_id, document)
			SELECT $2, $1::text, st.id, st.customer_id, $3::jsonb FROM statements st WHERE st.id = $1::uuid
			ON CONFLICT (doc_type, idempotency_key) DO NOTHING`, id, docType, string(in.Document)); err != nil {
			return Statement{}, false, mapErr(err)
		}
		transitioned = true
	}
	if err := tx.Commit(); err != nil {
		return Statement{}, false, err
	}
	st, err = s.GetStatement(ctx, OperatorScope, id)
	return st, transitioned, err
}

// GetStatementByExternalRef finds the statement an import is about.
func (s *Store) GetStatementByExternalRef(ctx context.Context, ref string) (Statement, error) {
	var id string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM statements WHERE external_invoice_ref = $1`, strings.TrimSpace(ref)).Scan(&id); err != nil {
		return Statement{}, mapErr(err)
	}
	return s.GetStatement(ctx, OperatorScope, id)
}

// ExternalInvoiceStatus is one imported update from the operator's billing
// system: what it now says the invoice is, and how much of it has been paid.
type ExternalInvoiceStatus struct {
	// Ref is the external invoice reference (the one issue recorded).
	Ref string
	// Status is our vocabulary after mapping: sent, paid or cancelled.
	Status string
	// PaidAmount is the CUMULATIVE amount paid per the external system;
	// empty means "unchanged".
	PaidAmount Decimal
	PaidAt     time.Time
	// Reference is the external payment reference, when the import carries
	// one; it keeps a repeated import from booking a second payment.
	Reference string
	Actor     string
}

// ApplyExternalInvoiceStatus records what the operator's billing system says
// about one invoice. In external mode this is the ONLY thing that moves a
// statement after issue: the lifecycle is theirs, and this writes what they
// report rather than deciding anything.
//
// The cumulative paid amount is reconciled by booking the difference as a
// payment, so the payment ledger stays the single place a balance comes from.
func (s *Store) ApplyExternalInvoiceStatus(ctx context.Context, in ExternalInvoiceStatus) (Statement, error) {
	switch in.Status {
	case StatusSent, StatusPaid, StatusCancelled:
	case "":
	default:
		return Statement{}, fmt.Errorf("%w: an imported status must be sent, paid or cancelled", ErrInvalid)
	}
	st, err := s.GetStatementByExternalRef(ctx, in.Ref)
	if err != nil {
		return Statement{}, err
	}
	if st.Status == StatusDraft {
		return Statement{}, fmt.Errorf("%w: %s has not been issued yet", ErrConflict, in.Ref)
	}
	if in.PaidAt.IsZero() {
		in.PaidAt = time.Now().UTC()
	}
	// Reconcile the cumulative paid amount by booking the difference.
	if strings.TrimSpace(string(in.PaidAmount)) != "" {
		want := ratOf(in.PaidAmount)
		if want.Cmp(ratOf(st.Total)) > 0 {
			return Statement{}, fmt.Errorf("%w: the billing system reports %s paid against a total of %s", ErrConflict, in.PaidAmount, st.Total)
		}
		delta := new(big.Rat).Sub(want, ratOf(st.Paid))
		if delta.Sign() > 0 {
			ref := strings.TrimSpace(in.Reference)
			if ref == "" {
				ref = in.Ref + "-" + string(decOf(want))
			}
			method := PaymentMethodGateway
			if c, err := s.GetCustomer(ctx, OperatorScope, st.CustomerID); err == nil && c.PaymentMethod != "" {
				method = c.PaymentMethod
			}
			if _, _, err := s.RecordStatementPayment(ctx, st.ID, PaymentInput{
				Amount: decOf(delta), PaidAt: in.PaidAt, Method: method, Reference: ref, Status: PaymentReceived,
				Gateway: "external", Actor: in.Actor,
			}); err != nil && !IsConflict(err) {
				// A conflict here is a repeated import of the same payment
				// reference, which is exactly what the unique index is for.
				return Statement{}, err
			}
			st, err = s.GetStatement(ctx, OperatorScope, st.ID)
			if err != nil {
				return Statement{}, err
			}
		}
	}
	if in.Status == "" || in.Status == st.Status {
		return st, nil
	}
	// Their vocabulary is authoritative, within reason: a terminal statement
	// stays terminal, and nothing returns to draft.
	if st.Status == StatusPaid || st.Status == StatusCancelled {
		return Statement{}, fmt.Errorf("%w: %s is already %s", ErrConflict, in.Ref, st.Status)
	}
	set := `status = $2`
	switch in.Status {
	case StatusSent:
		set += `, sent_at = COALESCE(sent_at, now())`
	case StatusPaid:
		set += `, paid_at = COALESCE(paid_at, $3)`
	case StatusCancelled:
		set += `, cancelled_at = COALESCE(cancelled_at, now()), cancel_reason = 'cancelled in the billing system'`
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE statements SET `+set+` WHERE id = $1`, st.ID, in.Status, in.PaidAt.UTC()); err != nil {
		return Statement{}, mapErr(err)
	}
	return s.GetStatement(ctx, OperatorScope, st.ID)
}

// UpdateStatementInvoice edits a DRAFT's purchase-order reference and payment
// terms. Once issued the invoice is a document the customer holds, so the
// fields are frozen and the call is refused.
func (s *Store) UpdateStatementInvoice(ctx context.Context, id string, patch StatementInvoicePatch) (Statement, error) {
	if patch.PaymentTermsDays != nil && (*patch.PaymentTermsDays < 0 || *patch.PaymentTermsDays > MaxPaymentTermsDays) {
		return Statement{}, fmt.Errorf("%w: payment_terms_days must be between 0 and %d", ErrInvalid, MaxPaymentTermsDays)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Statement{}, err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM statements WHERE id = $1 FOR UPDATE`, id).Scan(&status); err != nil {
		return Statement{}, mapErr(err)
	}
	if status != StatusDraft {
		return Statement{}, fmt.Errorf("%w: the purchase-order reference and payment terms are frozen once a statement is issued; this one is %s", ErrConflict, status)
	}
	sets := []string{}
	var args []any
	if patch.PORef != nil {
		args = append(args, strings.TrimSpace(*patch.PORef))
		sets = append(sets, fmt.Sprintf("po_reference = $%d", len(args)))
	}
	if patch.PaymentTermsDays != nil {
		args = append(args, *patch.PaymentTermsDays)
		sets = append(sets, fmt.Sprintf("payment_terms_days = $%d", len(args)))
	}
	if len(sets) > 0 {
		args = append(args, id)
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE statements SET %s WHERE id = $%d`, strings.Join(sets, ", "), len(args)), args...); err != nil {
			return Statement{}, mapErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Statement{}, err
	}
	return s.GetStatement(ctx, OperatorScope, id)
}
