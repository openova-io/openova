package store

import (
	"context"
	"database/sql"
	"encoding/json"
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

// backfillIssuedInvoicesMigrationSQL numbers the invoices that were issued
// BEFORE the invoicing release (0.1.27) ran invoicingMigrationSQL.
//
// The gap: that migration mapped every customer onto the four commercial
// fields (billing_mode real / chargeback → charging = 'billed') and added the
// invoice columns to statements, but it never looked at the statements that
// were ALREADY issued when it ran. An issued statement of a billed customer
// IS an invoice — and on the live Sovereign hw307 six August statements of
// billed customers were left with invoice_number NULL, due_at NULL,
// payment_terms_days NULL and an empty po_reference: invoices with no
// number, invisible to the invoicing surface. Only IssueStatementOnce mints
// a number, and it runs on the draft → issued edge alone, so nothing would
// ever have numbered them.
//
// What it does, in one transaction, for every statement with invoice_number
// IS NULL and status issued / sent / paid whose customer is charging =
// 'billed':
//
//  1. Numbers it as <prefix>-<YYYY>-<00000>, the exact shape InvoiceNumberFor
//     renders: the prefix from the single billing_settings row ('INV' when
//     that row is absent), the year of issued_at in UTC — the way
//     nextInvoiceNumber takes it. Numbers are dealt per year in
//     (issued_at, id) order and CONTINUE from invoice_sequences.last_value:
//     the year row is advanced by the count first (inserted at that count
//     when absent, ON CONFLICT DO UPDATE otherwise, which is the same row
//     lock a concurrent live issue takes) and the numbers are dealt from the
//     value it held before. The counter stays gapless, and the next live
//     issue carries on after the backfilled ones.
//  2. Copies the customer's payment_terms_days where the statement's is
//     NULL, and the customer's po_reference where the statement's is empty,
//     exactly as issue would have.
//  3. Computes due_at = issued_at + terms where it is NULL.
//
// Idempotent: a second run matches no row and touches no sequence. The
// statements of informational customers are not invoices and are left
// alone. BackfillIssuedInvoices runs the same batch on demand.
const backfillIssuedInvoicesMigrationSQL = `
-- Issue has always stamped issued_at (COALESCE(issued_at, now())), so an
-- issued row without one is a repair, not a rule: it takes its creation
-- time so it can be placed in a year at all.
UPDATE statements SET issued_at = created_at
 WHERE issued_at IS NULL AND status IN ('issued','sent','paid');

WITH settings AS (
	SELECT COALESCE((SELECT invoice_prefix FROM billing_settings WHERE id = 1), 'INV') AS prefix
),
pending AS (
	SELECT st.id,
	       c.po_reference AS customer_po,
	       c.payment_terms_days AS customer_terms,
	       EXTRACT(YEAR FROM (st.issued_at AT TIME ZONE 'UTC'))::int AS yr,
	       row_number() OVER (
	           PARTITION BY EXTRACT(YEAR FROM (st.issued_at AT TIME ZONE 'UTC'))::int
	           ORDER BY st.issued_at, st.id) AS rn
	  FROM statements st
	  JOIN customers c ON c.id = st.customer_id
	 WHERE st.invoice_number IS NULL
	   AND st.status IN ('issued','sent','paid')
	   AND c.charging = 'billed'
),
per_year AS (
	SELECT yr, count(*)::bigint AS n FROM pending GROUP BY yr
),
advanced AS (
	INSERT INTO invoice_sequences (year, last_value)
	SELECT yr, n FROM per_year
	ON CONFLICT (year) DO UPDATE SET last_value = invoice_sequences.last_value + EXCLUDED.last_value
	RETURNING year, last_value
)
UPDATE statements st
   SET invoice_number = s.prefix
                        || '-' || lpad(p.yr::text, greatest(4, length(p.yr::text)), '0')
                        || '-' || lpad((a.last_value - y.n + p.rn)::text, greatest(5, length((a.last_value - y.n + p.rn)::text)), '0'),
       payment_terms_days = COALESCE(st.payment_terms_days, p.customer_terms),
       po_reference = CASE WHEN st.po_reference = '' THEN p.customer_po ELSE st.po_reference END,
       due_at = COALESCE(st.due_at, st.issued_at + make_interval(days => COALESCE(st.payment_terms_days, p.customer_terms)))
  FROM pending p
  JOIN per_year y ON y.yr = p.yr
  JOIN advanced a ON a.year = p.yr
 CROSS JOIN settings s
 WHERE st.id = p.id;
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
//
// A SENT (or overdue) invoice may become cancelled since DESIGN.md §9.3 —
// not by a status flip, which lane 1 rightly refused, but through the FULL
// CREDIT NOTE that CancelStatement now issues for anything past draft.
var statementTransitions = map[string][]string{
	StatusDraft:     {StatusIssued, StatusCancelled},
	StatusIssued:    {StatusSent, StatusPaid, StatusCancelled},
	StatusSent:      {StatusPaid, StatusOverdue, StatusCancelled},
	StatusOverdue:   {StatusPaid, StatusCancelled},
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

	// Allocation (DESIGN.md §9.2). Allocated is what this payment has been
	// applied to invoices; Unallocated is the remainder held as credit on
	// the account; Allocations lists where it went. On the single-statement
	// document Allocated is what reached THAT invoice.
	Allocated   Decimal      `json:"allocated,omitempty"`
	Unallocated Decimal      `json:"unallocated,omitempty"`
	Allocations []Allocation `json:"allocations,omitempty"`
	// Purpose is checkout (the customer was present and paid now) or
	// collection (an unpaid invoice was pursued) — founder refinement (a).
	Purpose  string `json:"purpose,omitempty"`
	IntentID string `json:"intent_id,omitempty"`
	// A refunded payment settles nothing; when and why.
	RefundedAt   *time.Time `json:"refunded_at,omitempty"`
	RefundReason string     `json:"refund_reason,omitempty"`
	Note         string     `json:"note,omitempty"`
}

// Payment is the customer-level name of the same object.
type Payment = StatementPayment

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
	if settlesAt(new(big.Rat).Sub(ratOf(s.Total), ratOf(s.Paid)), s.Currency) {
		return s.Status
	}
	return StatusOverdue
}

// OutstandingAt is total − payments − credit notes, exactly, floored at
// zero: a settlement that overshot by less than half a minor unit
// (settlesAt) leaves nothing owed, and a balance is never reported negative.
func (s Statement) OutstandingAt() Decimal {
	out := new(big.Rat).Sub(ratOf(s.Total), ratOf(s.Paid))
	out.Sub(out, ratOf(s.Credited))
	if out.Sign() < 0 {
		out.SetInt64(0)
	}
	return decOf(out)
}

// ---------------------------------------------------------------------------
// the minor unit
// ---------------------------------------------------------------------------

// Money is kept and added at six decimals, exactly; money MOVES at the
// currency's minor unit. An invoice of 14.856782 OMR part-paid by 10.000
// leaves 4.856782 owed, which no bank transfer can carry: the customer pays
// the 4.857 the dialog shows, and that must settle the invoice rather than
// be refused as an overpayment of 0.000218. So the two decisions a payment
// turns on — is it more than was owed, has it settled the invoice — are
// judged at the minor unit, while the arithmetic underneath stays exact.

// minorUnitDigits is how many decimals the currency's minor unit has — what
// a bank transfer, a gateway confirmation and the operator's dialog can
// actually carry. Three for the dinars and the rial (baisa, fils), two for
// everything else, which is where ISO 4217 puts the rest of the world. The
// console keeps the same table (ui/src/lib/money.ts minorUnitDigits).
func minorUnitDigits(currency string) int {
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	case "OMR", "BHD", "KWD", "JOD", "IQD", "LYD", "TND":
		return 3
	}
	return 2
}

// minorUnitTolerance is half of one minor unit of the currency: the widest
// difference from an amount that still rounds to it at the unit.
func minorUnitTolerance(currency string) *big.Rat {
	den := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(minorUnitDigits(currency))), nil)
	den.Mul(den, big.NewInt(2))
	return new(big.Rat).SetFrac(big.NewInt(1), den)
}

// settlesAt reports whether remaining — total − paid, exactly — is zero at
// the currency's minor unit: within half a unit of nothing owed, on either
// side. That is what "the payments reached the total" means to a bank.
func settlesAt(remaining *big.Rat, currency string) bool {
	return new(big.Rat).Abs(remaining).Cmp(minorUnitTolerance(currency)) < 0
}

// overpaysAt reports whether remaining is negative by at least half a minor
// unit: the customer visibly sent more than was owed, which is a credit
// note, not a bigger invoice.
func overpaysAt(remaining *big.Rat, currency string) bool {
	return remaining.Sign() < 0 && !settlesAt(remaining, currency)
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

// BackfillIssuedInvoices numbers the invoices that were issued before the
// invoicing release, on demand: the same batch the migration applies once
// at startup (backfillIssuedInvoicesMigrationSQL), for an operator running
// it by hand against a live database who wants to see how many it touched.
// It returns the number of statements it numbered; a second call returns 0.
func (s *Store) BackfillIssuedInvoices(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var pending int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM statements st JOIN customers c ON c.id = st.customer_id
		WHERE st.invoice_number IS NULL AND st.status IN ('issued','sent','paid') AND c.charging = 'billed'`).Scan(&pending); err != nil {
		return 0, mapErr(err)
	}
	if pending == 0 {
		return 0, nil
	}
	if _, err := tx.ExecContext(ctx, backfillIssuedInvoicesMigrationSQL); err != nil {
		return 0, mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return pending, nil
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

// statementPayments lists the payments that touch one invoice: every
// payment ALLOCATED to it (with what reached it), plus a pending or failed
// payment recorded against it that settles nothing yet.
func (s *Store) statementPayments(ctx context.Context, statementID string) ([]StatementPayment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+paymentColumns+`, COALESCE(a.amount, 0)::numeric(20,6)::text
		FROM payments p LEFT JOIN invoice_allocations a ON a.payment_id = p.id AND a.statement_id = $1
		WHERE a.id IS NOT NULL OR (p.statement_id = $1 AND p.status <> 'received')
		ORDER BY p.paid_at, p.id`, statementID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []StatementPayment{}
	for rows.Next() {
		var p StatementPayment
		var amt, alloc, here string
		var refunded sql.NullTime
		if err := rows.Scan(&p.ID, &p.CustomerID, &p.StatementID, &amt, &p.PaidAt, &p.Method, &p.Reference, &p.Status, &p.Gateway, &p.RecordedBy, &p.RecordedAt,
			&p.Purpose, &p.IntentID, &refunded, &p.RefundReason, &p.Note, &alloc, &here); err != nil {
			return nil, err
		}
		p.Amount = Decimal(amt)
		p.PaidAt, p.RecordedAt = p.PaidAt.UTC(), p.RecordedAt.UTC()
		p.RefundedAt = timePtr(refunded)
		// On the invoice document, Allocated is what reached THIS invoice.
		p.Allocated = Decimal(here)
		if p.Status == PaymentReceived {
			p.Unallocated = decOf(new(big.Rat).Sub(ratOf(p.Amount), ratOf(Decimal(alloc))))
		} else {
			p.Unallocated = "0"
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// transitions
// ---------------------------------------------------------------------------

// lockedStatement is what lockStatement reads: the fields every transition
// decides on, with the payments already summed.
type lockedStatement struct {
	status     string
	currency   string
	customerID string
	total      Decimal
	paid       Decimal
	due        *time.Time
}

// lockStatement reads a statement FOR UPDATE inside tx and returns the
// fields every transition needs, with the derived status already resolved.
//
// `paid` is what payment allocations and credit-note allocations together
// settled (DESIGN.md §9.2), so total − paid is the outstanding balance.
func lockStatement(ctx context.Context, tx *sql.Tx, id string) (lockedStatement, error) {
	var l lockedStatement
	var t, p, c string
	var d sql.NullTime
	err := tx.QueryRowContext(ctx, `SELECT st.status, st.currency, st.customer_id, st.total::text, st.due_at,
		COALESCE((SELECT sum(a.amount) FROM invoice_allocations a JOIN payments x ON x.id = a.payment_id WHERE a.statement_id = st.id AND x.status = 'received'), 0)::numeric(20,6)::text,
		COALESCE((SELECT sum(a.amount) FROM invoice_allocations a WHERE a.statement_id = st.id AND a.credit_note_id IS NOT NULL), 0)::numeric(20,6)::text
		FROM statements st WHERE st.id = $1 FOR UPDATE`, id).Scan(&l.status, &l.currency, &l.customerID, &t, &d, &p, &c)
	if err != nil {
		return lockedStatement{}, mapErr(err)
	}
	l.total, l.paid, l.due = Decimal(t), addDec(Decimal(p), Decimal(c)), timePtr(d)
	return l, nil
}

// effectiveStatus is EffectiveStatusAt over the locked columns.
func effectiveStatus(l lockedStatement, now time.Time) string {
	return Statement{Status: l.status, Currency: l.currency, Total: l.total, Paid: l.paid, DueAt: l.due}.EffectiveStatusAt(now)
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
	l, err := lockStatement(ctx, tx, id)
	if err != nil {
		return Statement{}, false, err
	}
	cur := effectiveStatus(l, time.Now().UTC())
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

// CancelStatement voids a statement. A DRAFT is a status flip: no invoice
// exists yet. Anything past draft — issued, sent, overdue — is an invoice
// the customer may hold, and the only way to take it back is a FULL CREDIT
// NOTE (DESIGN.md §9.3): one is issued for the whole total, applied to what
// the invoice still carries, with the rest — money already paid — becoming
// credit on the account; the invoice then reads cancelled. A paid invoice
// is final: credit it with a credit note instead. Cancelling is idempotent.
func (s *Store) CancelStatement(ctx context.Context, id, reason string) (st Statement, transitioned bool, err error) {
	return s.cancelStatement(ctx, id, reason, "")
}

// CancelStatementBy is CancelStatement with the actor the credit note names.
func (s *Store) CancelStatementBy(ctx context.Context, id, reason, actor string) (st Statement, transitioned bool, err error) {
	return s.cancelStatement(ctx, id, reason, actor)
}

func (s *Store) cancelStatement(ctx context.Context, id, reason, actor string) (st Statement, transitioned bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Statement{}, false, err
	}
	defer tx.Rollback()
	l, err := lockStatement(ctx, tx, id)
	if err != nil {
		return Statement{}, false, err
	}
	cur := effectiveStatus(l, time.Now().UTC())
	switch {
	case cur == StatusCancelled:
	case !LegalStatementTransition(cur, StatusCancelled):
		return Statement{}, false, refuseTransition(cur, StatusCancelled)
	case l.status == StatusDraft:
		if _, err := tx.ExecContext(ctx, `UPDATE statements SET status = 'cancelled', cancelled_at = now(), cancel_reason = $2 WHERE id = $1`, id, strings.TrimSpace(reason)); err != nil {
			return Statement{}, false, mapErr(err)
		}
		transitioned = true
	default:
		if _, err := createCreditNoteTx(ctx, tx, id, CreditNoteInput{Reason: reason, Actor: actor}, CreditNoteFull); err != nil {
			return Statement{}, false, err
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
//
// Since DESIGN.md §9.2 the payment is a payment plus ONE ALLOCATION of its
// whole amount to this invoice, and a settled one posts its ledger credit
// in the same transaction — so nothing lane 1 did changes shape on the
// wire, and the account is right by construction.
func (s *Store) RecordStatementPayment(ctx context.Context, id string, in PaymentInput) (st Statement, p StatementPayment, err error) {
	in, amt, err := normalisePayment(in)
	if err != nil {
		return Statement{}, p, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Statement{}, p, err
	}
	defer tx.Rollback()
	l, err := lockStatement(ctx, tx, id)
	if err != nil {
		return Statement{}, p, err
	}
	now := time.Now().UTC()
	cur := effectiveStatus(l, now)
	if !LegalStatementTransition(cur, StatusPaid) {
		return Statement{}, p, refuseTransition(cur, StatusPaid)
	}
	newPaid := new(big.Rat).Add(ratOf(l.paid), amt)
	if in.Status != PaymentReceived {
		// A pending or failed payment is recorded but settles nothing, so
		// it cannot exceed the balance and cannot flip the statement.
		newPaid = ratOf(l.paid)
	}
	// What the payment leaves owed, exactly — and judged at the minor unit:
	// the 4.857 a customer pays against 4.856782 is the settlement the
	// dialog offered, not an overpayment; half a unit or more over is.
	remaining := new(big.Rat).Sub(ratOf(l.total), newPaid)
	if overpaysAt(remaining, l.currency) {
		outstanding := Statement{Total: l.total, Paid: l.paid}.OutstandingAt()
		return Statement{}, p, fmt.Errorf("%w: payment of %s exceeds the outstanding balance of %s", ErrConflict, decOf(amt), outstanding)
	}
	customerID, currency := l.customerID, l.currency
	if customerID == "" {
		return Statement{}, p, fmt.Errorf("%w: statement %s has no customer", ErrInvalid, id)
	}
	var paymentID int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO payments (customer_id, statement_id, amount, paid_at, method, reference, status, gateway, recorded_by, purpose)
		VALUES ($1, $2, $3::numeric, $4, $5, $6, $7, $8, $9, 'collection') RETURNING id`,
		customerID, id, string(decOf(amt)), in.PaidAt.UTC(), in.Method, strings.TrimSpace(in.Reference), in.Status, in.Gateway, in.Actor).Scan(&paymentID); err != nil {
		return Statement{}, StatementPayment{}, mapErr(err)
	}
	if in.Status == PaymentReceived {
		// A pending or failed payment is recorded but settles nothing: no
		// allocation, no ledger credit, and the invoice cannot flip. A
		// received one is allocated whole to this invoice — clipped to what
		// is owed when it overshoots by less than a minor unit, which is
		// where allocateTx flips the invoice to paid.
		if err := allocateTx(ctx, tx, customerID, id, paymentID, "", amt, in.Actor, in.PaidAt); err != nil {
			return Statement{}, StatementPayment{}, err
		}
		if err := postEntry(ctx, tx, AccountEntry{CustomerID: customerID, Kind: EntryPayment, Amount: negate(decOf(amt)), Currency: currency,
			StatementID: id, PaymentID: paymentID, Reference: in.Reference, EnteredAt: in.PaidAt, EnteredBy: in.Actor}); err != nil {
			return Statement{}, StatementPayment{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Statement{}, StatementPayment{}, err
	}
	if p, err = s.GetPayment(ctx, OperatorScope, paymentID); err != nil {
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
	// Extra documents queued with the primary one — the TMF635 rated usage
	// beside the bill (DESIGN.md §9.1). Same transaction, same outbox.
	Extra []OutboxDocument
	// NumberInvoice is set only in summary-charge mode, where THIS product
	// numbers the invoice for the billing system to book as one line. The
	// number is taken inside the issuing transaction, exactly as an internal
	// issue does, and handed to BuildDocuments so the exported summary
	// charge quotes it.
	NumberInvoice bool
	// BuildDocuments, when set, renders the documents to queue once the
	// invoice number (empty unless NumberInvoice) is known; its output
	// replaces Document and Extra.
	BuildDocuments func(invoiceNumber string) (primary []byte, extra []OutboxDocument, err error)
	Actor          string
}

// OutboxDocument is one document to queue: its type, the key at-least-once
// delivery is idempotent on (the statement id when queued at issue), and
// the JSON body.
type OutboxDocument struct {
	DocType        string
	IdempotencyKey string
	Document       []byte
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
	if len(in.Document) == 0 && in.BuildDocuments == nil {
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
		var customerID, currency, rate, total string
		if err := tx.QueryRowContext(ctx, `SELECT customer_id, currency, tax_rate::text, total::text FROM statements WHERE id = $1`, id).Scan(&customerID, &currency, &rate, &total); err != nil {
			return Statement{}, false, mapErr(err)
		}
		settings, err := billingSettingsTx(ctx, tx)
		if err != nil {
			return Statement{}, false, err
		}
		// The tax snapshot is frozen here too: the exported bill states
		// the buyer's registration and exemption as they were at issue.
		snap, err := taxSnapshotTx(ctx, tx, customerID, Decimal(rate), settings)
		if err != nil {
			return Statement{}, false, err
		}
		snapJSON, _ := json.Marshal(snap)
		// Summary-charge mode numbers the invoice here (DESIGN.md §9.1): we
		// produce the invoice document, the billing system books one line.
		// Taken inside this transaction so the sequence stays gapless.
		var number any
		invoiceNumber := ""
		if in.NumberInvoice {
			if invoiceNumber, err = nextInvoiceNumber(ctx, tx, settings.InvoicePrefix); err != nil {
				return Statement{}, false, err
			}
			number = invoiceNumber
		}
		if in.BuildDocuments != nil {
			if in.Document, in.Extra, err = in.BuildDocuments(invoiceNumber); err != nil {
				return Statement{}, false, err
			}
			if len(in.Document) == 0 {
				return Statement{}, false, fmt.Errorf("%w: the export document is empty", ErrInvalid)
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE statements SET status = 'issued', issued_at = $4,
			po_reference = $2, payment_terms_days = $3,
			due_at = $4::timestamptz + make_interval(days => $3), tax_snapshot = $5, invoice_number = COALESCE($6, invoice_number)
			WHERE id = $1 AND status = 'draft'`, id, in.PORef, in.TermsDays, in.IssuedAt.UTC(), snapJSON, number); err != nil {
			return Statement{}, false, mapErr(err)
		}
		// The documents to export, queued in the same transaction. The
		// statement id is the idempotency key per document type, so
		// at-least-once delivery of a row is one document at the far end.
		if _, err := tx.ExecContext(ctx, `INSERT INTO commercial_outbox (doc_type, idempotency_key, statement_id, customer_id, document)
			SELECT $2, $1::text, st.id, st.customer_id, $3::jsonb FROM statements st WHERE st.id = $1::uuid
			ON CONFLICT (doc_type, idempotency_key) DO NOTHING`, id, docType, string(in.Document)); err != nil {
			return Statement{}, false, mapErr(err)
		}
		for _, extra := range in.Extra {
			if len(extra.Document) == 0 {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO commercial_outbox (doc_type, idempotency_key, statement_id, customer_id, document)
				SELECT $2, $1::text, st.id, st.customer_id, $3::jsonb FROM statements st WHERE st.id = $1::uuid
				ON CONFLICT (doc_type, idempotency_key) DO NOTHING`, id, extra.DocType, string(extra.Document)); err != nil {
				return Statement{}, false, mapErr(err)
			}
		}
		// The receivable exists whoever collects it: the ledger mirrors it
		// (DESIGN.md §9.1 — in external mode the account is theirs, ours is
		// a mirror of what we issued and what they reported).
		if ratOf(Decimal(total)).Sign() > 0 {
			if err := postEntry(ctx, tx, AccountEntry{CustomerID: customerID, Kind: EntryInvoice, Amount: Decimal(total), Currency: currency, StatementID: id, Reference: invoiceNumber, EnteredAt: in.IssuedAt, EnteredBy: in.Actor}); err != nil {
				return Statement{}, false, err
			}
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
		if overpaysAt(new(big.Rat).Sub(ratOf(st.Total), want), st.Currency) {
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
