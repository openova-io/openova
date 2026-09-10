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

// Collections, the customer account and account credit (DESIGN.md §9,
// founder direction 2026-09-10: "structured answer and solution based on all
// different types such as invoices, payments, collections etc").
//
// Lane 1 made a statement an invoice and gave it a lifecycle. What it left
// for this lane is everything that happens to MONEY around an invoice:
//
//   - the customer's ACCOUNT — an append-only ledger whose sum is the
//     balance, never a stored number;
//   - PAYMENTS as objects of their own, ALLOCATED to invoices, with the
//     unallocated remainder held as credit on the account for ANY customer
//     (a postpaid customer may top up too);
//   - CREDIT NOTES — the only way an issued invoice is ever reduced;
//   - the TAX PROFILE an Omani tax invoice must carry, snapshotted at issue;
//   - the COLLECTIONS schedule (reminders, escalation) and the aging report;
//   - the platform SUSPENSION hook and its audit trail.
//
// Every capability here sits behind the commercial-provider seam: with
// `commercial_provider = external` the operator's billing system owns the
// account, collections and enforcement, and this product only mirrors what
// it is told (DESIGN.md §9.1).

// ---------------------------------------------------------------------------
// migration
// ---------------------------------------------------------------------------

// collectionsMigrationSQL is one transaction, idempotent against a database
// that already carries the shape. Appended at the END of the migrations
// slice: migrations are positional.
const collectionsMigrationSQL = `
-- The tax profile (DESIGN.md §9.4). A customer's registration number, an
-- exemption with its reason, and an optional rate that overrides the
-- Sovereign default. NULL tax_rate = the Sovereign's rate.
ALTER TABLE customers ADD COLUMN IF NOT EXISTS tax_registration_number TEXT NOT NULL DEFAULT '';
ALTER TABLE customers ADD COLUMN IF NOT EXISTS tax_exempt BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE customers ADD COLUMN IF NOT EXISTS tax_exempt_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE customers ADD COLUMN IF NOT EXISTS tax_rate NUMERIC(6,4);
ALTER TABLE customers DROP CONSTRAINT IF EXISTS customers_tax_rate_check;
ALTER TABLE customers ADD CONSTRAINT customers_tax_rate_check CHECK (tax_rate IS NULL OR (tax_rate >= 0 AND tax_rate <= 1));

-- Account credit is universal (DESIGN.md §9.5). auto_apply_credit applies
-- available credit to every invoice at issue; the two wallet knobs are what
-- payment_model = prepaid adds: a low-balance alert and suspend-at-zero.
ALTER TABLE customers ADD COLUMN IF NOT EXISTS auto_apply_credit BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE customers ADD COLUMN IF NOT EXISTS low_balance_threshold NUMERIC(20,6);
ALTER TABLE customers ADD COLUMN IF NOT EXISTS suspend_at_zero BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE customers ADD COLUMN IF NOT EXISTS low_balance_alerted_at TIMESTAMPTZ;

-- The platform suspension this product asked for (DESIGN.md §9.7), and the
-- balance the external billing system last reported (external mode only).
ALTER TABLE customers ADD COLUMN IF NOT EXISTS platform_suspended_at TIMESTAMPTZ;
ALTER TABLE customers ADD COLUMN IF NOT EXISTS suspension_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE customers ADD COLUMN IF NOT EXISTS suspension_source TEXT NOT NULL DEFAULT '';
ALTER TABLE customers ADD COLUMN IF NOT EXISTS external_balance NUMERIC(20,6);
ALTER TABLE customers ADD COLUMN IF NOT EXISTS external_balance_at TIMESTAMPTZ;

-- The Sovereign's own tax identity (what the seller block of a tax invoice
-- carries), its default rate, the credit-note prefix, the collections
-- schedule and the external-ingest variant (DESIGN.md §9.1).
ALTER TABLE billing_settings ADD COLUMN IF NOT EXISTS tax_rate NUMERIC(6,4) NOT NULL DEFAULT 0.05;
ALTER TABLE billing_settings DROP CONSTRAINT IF EXISTS billing_settings_tax_rate_check;
ALTER TABLE billing_settings ADD CONSTRAINT billing_settings_tax_rate_check CHECK (tax_rate >= 0 AND tax_rate <= 1);
ALTER TABLE billing_settings ADD COLUMN IF NOT EXISTS tax_registration_number TEXT NOT NULL DEFAULT '';
ALTER TABLE billing_settings ADD COLUMN IF NOT EXISTS legal_name TEXT NOT NULL DEFAULT '';
ALTER TABLE billing_settings ADD COLUMN IF NOT EXISTS address TEXT NOT NULL DEFAULT '';
ALTER TABLE billing_settings ADD COLUMN IF NOT EXISTS credit_note_prefix TEXT NOT NULL DEFAULT 'CN';
ALTER TABLE billing_settings DROP CONSTRAINT IF EXISTS billing_settings_credit_note_prefix_check;
ALTER TABLE billing_settings ADD CONSTRAINT billing_settings_credit_note_prefix_check CHECK (credit_note_prefix ~ '^[A-Z0-9][A-Z0-9-]{0,11}$');
ALTER TABLE billing_settings ADD COLUMN IF NOT EXISTS reminder_days INT[] NOT NULL DEFAULT '{-3,0,7,14,30}';
ALTER TABLE billing_settings ADD COLUMN IF NOT EXISTS escalation_days INT NOT NULL DEFAULT 45;
ALTER TABLE billing_settings DROP CONSTRAINT IF EXISTS billing_settings_escalation_days_check;
ALTER TABLE billing_settings ADD CONSTRAINT billing_settings_escalation_days_check CHECK (escalation_days >= 0 AND escalation_days <= 3650);
ALTER TABLE billing_settings ADD COLUMN IF NOT EXISTS escalation_action TEXT NOT NULL DEFAULT 'notify';
ALTER TABLE billing_settings DROP CONSTRAINT IF EXISTS billing_settings_escalation_action_check;
ALTER TABLE billing_settings ADD CONSTRAINT billing_settings_escalation_action_check CHECK (escalation_action IN ('notify','suspend'));
ALTER TABLE billing_settings ADD COLUMN IF NOT EXISTS external_ingest TEXT NOT NULL DEFAULT 'rated_bill';
ALTER TABLE billing_settings DROP CONSTRAINT IF EXISTS billing_settings_external_ingest_check;
ALTER TABLE billing_settings ADD CONSTRAINT billing_settings_external_ingest_check CHECK (external_ingest IN ('rated_bill','summary_charge'));

-- What an issued invoice carries about tax, frozen at issue: the customer's
-- registration and exemption, the rate applied, and the seller's identity.
-- Never recomputed (DESIGN.md §9.4).
ALTER TABLE statements ADD COLUMN IF NOT EXISTS tax_snapshot JSONB;

-- A payment is its own object. Lane 1 gave it a status of received / pending
-- / failed; a REFUNDED payment settles nothing any more and posts a refund
-- debit on the ledger. purpose says whether the customer was present and
-- paying now (checkout) or an unpaid invoice was being pursued (collection)
-- — DESIGN.md §9.2, the founder's refinement (a).
ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_status_check;
ALTER TABLE payments ADD CONSTRAINT payments_status_check CHECK (status IN ('received','pending','failed','refunded'));
ALTER TABLE payments ADD COLUMN IF NOT EXISTS purpose TEXT NOT NULL DEFAULT 'collection';
ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_purpose_check;
ALTER TABLE payments ADD CONSTRAINT payments_purpose_check CHECK (purpose IN ('checkout','collection'));
ALTER TABLE payments ADD COLUMN IF NOT EXISTS intent_id UUID;
ALTER TABLE payments ADD COLUMN IF NOT EXISTS refunded_at TIMESTAMPTZ;
ALTER TABLE payments ADD COLUMN IF NOT EXISTS refund_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE payments ADD COLUMN IF NOT EXISTS note TEXT NOT NULL DEFAULT '';

-- Credit notes: numbered gaplessly per year with their own prefix, against
-- ONE invoice, with lines or a lump amount, and a reason (DESIGN.md §9.3).
CREATE TABLE IF NOT EXISTS credit_note_sequences (
	year INT PRIMARY KEY,
	last_value BIGINT NOT NULL DEFAULT 0 CHECK (last_value >= 0)
);
CREATE TABLE IF NOT EXISTS credit_notes (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
	statement_id UUID NOT NULL REFERENCES statements(id) ON DELETE CASCADE,
	number TEXT NOT NULL UNIQUE,
	kind TEXT NOT NULL DEFAULT 'partial' CHECK (kind IN ('partial','full','write_off')),
	reason TEXT NOT NULL DEFAULT '',
	currency TEXT NOT NULL,
	subtotal NUMERIC(20,6) NOT NULL CHECK (subtotal >= 0),
	tax_rate NUMERIC(6,4) NOT NULL DEFAULT 0,
	tax NUMERIC(20,6) NOT NULL CHECK (tax >= 0),
	total NUMERIC(20,6) NOT NULL CHECK (total > 0),
	lines JSONB,
	issued_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	issued_by TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS credit_notes_statement_idx ON credit_notes (statement_id);
CREATE INDEX IF NOT EXISTS credit_notes_customer_idx ON credit_notes (customer_id, issued_at);

-- ALLOCATION: what settles an invoice is an allocation row — from a payment
-- or from a credit note — never a column on the invoice. An invoice's paid
-- total is the sum of its payment allocations, its credited total the sum of
-- its credit-note allocations, and its balance the difference from the
-- total; a payment's or a credit note's unallocated remainder is credit on
-- the account (DESIGN.md §9.2, §9.5).
CREATE TABLE IF NOT EXISTS invoice_allocations (
	id BIGSERIAL PRIMARY KEY,
	statement_id UUID NOT NULL REFERENCES statements(id) ON DELETE CASCADE,
	payment_id BIGINT REFERENCES payments(id) ON DELETE CASCADE,
	credit_note_id UUID REFERENCES credit_notes(id) ON DELETE CASCADE,
	amount NUMERIC(20,6) NOT NULL CHECK (amount > 0),
	allocated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	allocated_by TEXT NOT NULL DEFAULT '',
	CHECK ((payment_id IS NOT NULL) <> (credit_note_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS invoice_allocations_statement_idx ON invoice_allocations (statement_id);
CREATE INDEX IF NOT EXISTS invoice_allocations_payment_idx ON invoice_allocations (payment_id);
CREATE INDEX IF NOT EXISTS invoice_allocations_credit_note_idx ON invoice_allocations (credit_note_id);

-- Backfill: every payment lane 1 recorded against an invoice was, by
-- construction, fully allocated to it (overpayment was refused), so the
-- allocation is the payment's whole amount and nothing changes on the wire.
INSERT INTO invoice_allocations (statement_id, payment_id, amount, allocated_at, allocated_by)
SELECT p.statement_id, p.id, p.amount, p.recorded_at, p.recorded_by
  FROM payments p
 WHERE p.statement_id IS NOT NULL AND p.status = 'received'
ON CONFLICT DO NOTHING;

-- The ACCOUNT LEDGER (DESIGN.md §9.1): append-only, one row per event that
-- moves the customer's balance. amount is signed — positive is a debit (the
-- customer owes more), negative a credit — and the CHECK ties the sign to
-- the kind so a row can never say one thing and mean another. The balance
-- is the sum, never a stored number.
CREATE TABLE IF NOT EXISTS account_entries (
	id BIGSERIAL PRIMARY KEY,
	customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
	kind TEXT NOT NULL CHECK (kind IN ('invoice','payment','credit_note','refund','write_off','top_up')),
	amount NUMERIC(20,6) NOT NULL,
	currency TEXT NOT NULL,
	statement_id UUID REFERENCES statements(id) ON DELETE CASCADE,
	payment_id BIGINT REFERENCES payments(id) ON DELETE CASCADE,
	credit_note_id UUID REFERENCES credit_notes(id) ON DELETE CASCADE,
	reference TEXT NOT NULL DEFAULT '',
	note TEXT NOT NULL DEFAULT '',
	entered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	entered_by TEXT NOT NULL DEFAULT '',
	CHECK ((kind IN ('invoice','refund') AND amount > 0) OR (kind IN ('payment','credit_note','write_off','top_up') AND amount < 0))
);
CREATE INDEX IF NOT EXISTS account_entries_customer_idx ON account_entries (customer_id, entered_at, id);
CREATE UNIQUE INDEX IF NOT EXISTS account_entries_invoice_idx ON account_entries (statement_id) WHERE kind = 'invoice';
CREATE UNIQUE INDEX IF NOT EXISTS account_entries_payment_idx ON account_entries (kind, payment_id) WHERE payment_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS account_entries_credit_note_idx ON account_entries (credit_note_id) WHERE credit_note_id IS NOT NULL;

-- Backfill the ledger from what already happened: a debit for every invoice
-- that was issued and not voided, a credit for every received payment.
INSERT INTO account_entries (customer_id, kind, amount, currency, statement_id, reference, entered_at, entered_by)
SELECT st.customer_id, 'invoice', st.total, st.currency, st.id, COALESCE(st.invoice_number, st.external_invoice_ref, ''), COALESCE(st.issued_at, st.created_at), 'migration'
  FROM statements st
 WHERE st.issued_at IS NOT NULL AND st.status <> 'cancelled' AND st.total > 0
ON CONFLICT DO NOTHING;
INSERT INTO account_entries (customer_id, kind, amount, currency, statement_id, payment_id, reference, entered_at, entered_by)
SELECT p.customer_id, 'payment', -p.amount, COALESCE(st.currency, 'OMR'), p.statement_id, p.id, p.reference, p.recorded_at, 'migration'
  FROM payments p LEFT JOIN statements st ON st.id = p.statement_id
 WHERE p.status = 'received'
ON CONFLICT DO NOTHING;

-- The balance, as a VIEW over the ledger and the allocations: what the
-- customer owes (the ledger sum), the credit it can still apply (settled
-- money and credit notes not yet allocated to an invoice), and what its
-- open invoices still carry. Each is a sum at read time.
CREATE OR REPLACE VIEW customer_balances AS
SELECT c.id AS customer_id,
       COALESCE((SELECT sum(e.amount) FROM account_entries e WHERE e.customer_id = c.id), 0)::numeric(20,6) AS balance,
       (COALESCE((SELECT sum(p.amount) FROM payments p WHERE p.customer_id = c.id AND p.status = 'received'), 0)
        - COALESCE((SELECT sum(a.amount) FROM invoice_allocations a JOIN payments p ON p.id = a.payment_id WHERE p.customer_id = c.id AND p.status = 'received'), 0)
        + COALESCE((SELECT sum(n.total) FROM credit_notes n WHERE n.customer_id = c.id), 0)
        - COALESCE((SELECT sum(a.amount) FROM invoice_allocations a JOIN credit_notes n ON n.id = a.credit_note_id WHERE n.customer_id = c.id), 0))::numeric(20,6) AS available_credit,
       COALESCE((SELECT sum(st.total
                    - COALESCE((SELECT sum(a.amount) FROM invoice_allocations a JOIN payments p ON p.id = a.payment_id WHERE a.statement_id = st.id AND p.status = 'received'), 0)
                    - COALESCE((SELECT sum(a.amount) FROM invoice_allocations a WHERE a.statement_id = st.id AND a.credit_note_id IS NOT NULL), 0))
                   FROM statements st WHERE st.customer_id = c.id AND st.status IN ('issued','sent')), 0)::numeric(20,6) AS outstanding
  FROM customers c;

-- A payment INTENT (DESIGN.md §9.2): the request made to the gateway seam,
-- with WHY it was made. checkout = the customer is present and pays now;
-- collection = an unpaid invoice is being pursued. The provider check refuses
-- a collection intent when the external billing system owns the receivable.
CREATE TABLE IF NOT EXISTS payment_intents (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
	purpose TEXT NOT NULL CHECK (purpose IN ('checkout','collection')),
	statement_id UUID REFERENCES statements(id) ON DELETE CASCADE,
	amount NUMERIC(20,6) NOT NULL CHECK (amount > 0),
	currency TEXT NOT NULL,
	gateway TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'requested' CHECK (status IN ('requested','pending','settled','failed','refused','awaiting-transfer')),
	reference TEXT NOT NULL DEFAULT '',
	pay_url TEXT NOT NULL DEFAULT '',
	detail TEXT NOT NULL DEFAULT '',
	payment_id BIGINT REFERENCES payments(id) ON DELETE SET NULL,
	requested_by TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS payment_intents_customer_idx ON payment_intents (customer_id, created_at DESC);

-- One row per reminder or escalation actually sent for an invoice, keyed
-- on (statement, kind, stage) so the daily evaluator can never send the
-- same stage twice — the budget_alerts pattern (DESIGN.md §9.6).
CREATE TABLE IF NOT EXISTS collection_reminders (
	id BIGSERIAL PRIMARY KEY,
	statement_id UUID NOT NULL REFERENCES statements(id) ON DELETE CASCADE,
	kind TEXT NOT NULL CHECK (kind IN ('reminder','escalation')),
	stage INT NOT NULL,
	sent_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	recipients TEXT[] NOT NULL DEFAULT '{}',
	UNIQUE (statement_id, kind, stage)
);

-- Every suspension and resumption this product executed, with its outcome
-- at the platform (DESIGN.md §9.7). Visible on the customer page.
CREATE TABLE IF NOT EXISTS customer_suspensions (
	id BIGSERIAL PRIMARY KEY,
	customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
	action TEXT NOT NULL CHECK (action IN ('suspend','resume')),
	source TEXT NOT NULL DEFAULT 'operator',
	reason TEXT NOT NULL DEFAULT '',
	ok BOOLEAN NOT NULL DEFAULT true,
	error TEXT NOT NULL DEFAULT '',
	actor TEXT NOT NULL DEFAULT '',
	at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS customer_suspensions_customer_idx ON customer_suspensions (customer_id, at DESC);
`

// ---------------------------------------------------------------------------
// constants and types
// ---------------------------------------------------------------------------

// Ledger entry kinds. Debits raise what the customer owes; credits lower it.
const (
	EntryInvoice    = "invoice"     // debit: an invoice was issued
	EntryPayment    = "payment"     // credit: a payment settled
	EntryCreditNote = "credit_note" // credit: a credit note was applied
	EntryRefund     = "refund"      // debit: a settled payment was refunded
	EntryWriteOff   = "write_off"   // credit: a receivable was written off
	EntryTopUp      = "top_up"      // credit: money arrived with no invoice to settle
)

// EntryKinds lists the ledger kinds in display order.
var EntryKinds = []string{EntryInvoice, EntryPayment, EntryCreditNote, EntryRefund, EntryWriteOff, EntryTopUp}

// PaymentRefunded is the fourth payment status: the money went back, so the
// payment settles nothing and its allocations are released.
const PaymentRefunded = "refunded"

// PaymentStatuses lists every status a payment can be in.
var PaymentStatuses = []string{PaymentReceived, PaymentPending, PaymentFailed, PaymentRefunded}

// Payment purposes (DESIGN.md §9.2, founder refinement (a)).
const (
	// PurposeCheckout — the customer is present and pays now: a plan
	// purchase, a top-up. Our product calls the gateway in EVERY mode.
	PurposeCheckout = "checkout"
	// PurposeCollection — an unpaid invoice is pursued. Only the owner of
	// the receivable triggers payment, so in external mode this is refused.
	PurposeCollection = "collection"
)

// PaymentPurposes lists the accepted values.
var PaymentPurposes = []string{PurposeCheckout, PurposeCollection}

// Payment intent statuses.
const (
	IntentRequested        = "requested"
	IntentPending          = "pending"
	IntentSettled          = "settled"
	IntentFailed           = "failed"
	IntentRefused          = "refused"
	IntentAwaitingTransfer = "awaiting-transfer"
)

// Credit note kinds.
const (
	CreditNotePartial  = "partial"
	CreditNoteFull     = "full"
	CreditNoteWriteOff = "write_off"
)

// Escalation actions of the collections schedule.
const (
	EscalationNotify  = "notify"
	EscalationSuspend = "suspend"
)

// External-ingest variants (DESIGN.md §9.1, founder refinement (b)).
const (
	// IngestRatedBill — the billing system takes the full TMF678 bill; we
	// never number an invoice.
	IngestRatedBill = "rated_bill"
	// IngestSummaryCharge — the billing system cannot take a rated bill:
	// WE number and produce the detailed invoice document, and export ONE
	// summary charge line per statement for it to book as a receivable.
	IngestSummaryCharge = "summary_charge"
)

// Suspension sources — who asked for the platform suspension.
const (
	SuspendSourceOperator    = "operator"
	SuspendSourceCollections = "collections"
	SuspendSourceWallet      = "wallet"
	SuspendSourceImport      = "import"
)

// DefaultReminderDays is the founder's schedule: 3 days before the due
// date, on it, and 7 / 14 / 30 days after. Negative is before.
var DefaultReminderDays = []int{-3, 0, 7, 14, 30}

// DefaultEscalationDays is the overdue age at which the escalation action
// fires when the operator has not set one.
const DefaultEscalationDays = 45

// DefaultCreditNotePrefix numbers credit notes CN-<year>-<seq>.
const DefaultCreditNotePrefix = "CN"

// DefaultTaxRate is Oman VAT, the Sovereign default a customer's own rate
// overrides.
const DefaultTaxRate Decimal = "0.0500"

// TaxSnapshot is what an issued invoice carries about tax, frozen at issue
// (DESIGN.md §9.4): the rate it was computed at, the customer's registration
// and exemption, and the seller's identity as the operator configured it.
type TaxSnapshot struct {
	Rate         Decimal `json:"rate"`
	Exempt       bool    `json:"exempt"`
	ExemptReason string  `json:"exempt_reason,omitempty"`
	// CustomerName and CustomerTaxNumber identify the buyer.
	CustomerName      string `json:"customer_name"`
	CustomerTaxNumber string `json:"customer_tax_registration_number,omitempty"`
	// The seller block.
	SellerLegalName string `json:"seller_legal_name,omitempty"`
	SellerTaxNumber string `json:"seller_tax_registration_number,omitempty"`
	SellerAddress   string `json:"seller_address,omitempty"`
}

// TaxProfile is the customer side of the tax profile.
type TaxProfile struct {
	TaxRegistrationNumber string
	TaxExempt             bool
	TaxExemptReason       string
	// TaxRate nil = the Sovereign default.
	TaxRate *Decimal
}

// EffectiveTaxRate is the rate a customer's statements are rated at: zero
// when exempt, the customer's own rate when it has one, else the Sovereign
// default (DESIGN.md §9.4).
func EffectiveTaxRate(c Customer, b BillingSettings) Decimal {
	if c.TaxExempt {
		return "0"
	}
	if c.TaxRate != nil && strings.TrimSpace(string(*c.TaxRate)) != "" {
		return *c.TaxRate
	}
	if strings.TrimSpace(string(b.TaxRate)) == "" {
		return DefaultTaxRate
	}
	return b.TaxRate
}

// AccountEntry is one row of the ledger. Amount is signed: positive is a
// debit (the customer owes more), negative a credit.
type AccountEntry struct {
	ID         int64   `json:"id"`
	CustomerID string  `json:"customer_id"`
	Kind       string  `json:"kind"`
	Amount     Decimal `json:"amount"`
	Currency   string  `json:"currency"`
	// The object the entry is about, when there is one.
	StatementID   string `json:"statement_id,omitempty"`
	InvoiceNumber string `json:"invoice_number,omitempty"`
	PaymentID     int64  `json:"payment_id,omitempty"`
	CreditNoteID  string `json:"credit_note_id,omitempty"`
	CreditNoteNo  string `json:"credit_note_number,omitempty"`
	Reference     string `json:"reference,omitempty"`
	Note          string `json:"note,omitempty"`
	// Balance is the running balance AFTER this entry, computed on read.
	Balance   Decimal   `json:"balance"`
	EnteredAt time.Time `json:"entered_at"`
	EnteredBy string    `json:"entered_by,omitempty"`
}

// AccountBalance is the customer_balances view row: the balance (owed,
// positive; in credit, negative), the credit still applicable, and what the
// open invoices carry. Every figure is a sum at read time.
type AccountBalance struct {
	CustomerID      string  `json:"customer_id"`
	Balance         Decimal `json:"balance"`
	AvailableCredit Decimal `json:"available_credit"`
	Outstanding     Decimal `json:"outstanding"`
}

// Allocation is one application of a payment or a credit note to an invoice.
type Allocation struct {
	ID            int64     `json:"id"`
	StatementID   string    `json:"statement_id"`
	InvoiceNumber string    `json:"invoice_number,omitempty"`
	PaymentID     int64     `json:"payment_id,omitempty"`
	CreditNoteID  string    `json:"credit_note_id,omitempty"`
	Amount        Decimal   `json:"amount"`
	AllocatedAt   time.Time `json:"allocated_at"`
	AllocatedBy   string    `json:"allocated_by,omitempty"`
}

// AllocationInput names an invoice and how much of a payment to apply.
type AllocationInput struct {
	StatementID string
	Amount      Decimal
}

// CreditNoteLine is one line of a credit note.
type CreditNoteLine struct {
	SKU         string  `json:"sku,omitempty"`
	Description string  `json:"description,omitempty"`
	Quantity    Decimal `json:"quantity,omitempty"`
	Unit        string  `json:"unit,omitempty"`
	UnitPrice   Decimal `json:"unit_price,omitempty"`
	Amount      Decimal `json:"amount"`
}

// CreditNote reduces an issued invoice (DESIGN.md §9.3).
type CreditNote struct {
	ID            string           `json:"id"`
	CustomerID    string           `json:"customer_id"`
	StatementID   string           `json:"statement_id"`
	InvoiceNumber string           `json:"invoice_number,omitempty"`
	Number        string           `json:"number"`
	Kind          string           `json:"kind"`
	Reason        string           `json:"reason,omitempty"`
	Currency      string           `json:"currency"`
	Subtotal      Decimal          `json:"subtotal"`
	TaxRate       Decimal          `json:"tax_rate"`
	Tax           Decimal          `json:"tax"`
	Total         Decimal          `json:"total"`
	Lines         []CreditNoteLine `json:"lines,omitempty"`
	// Applied is what reduced the invoice; Unapplied is what became credit
	// on the account because the invoice was already settled that far.
	Applied   Decimal   `json:"applied"`
	Unapplied Decimal   `json:"unapplied"`
	IssuedAt  time.Time `json:"issued_at"`
	IssuedBy  string    `json:"issued_by,omitempty"`
}

// CreditNoteInput is what POST /statements/{id}/credit-notes carries: a
// reason, and EITHER a lump amount (tax-inclusive) OR lines (tax added at
// the invoice's frozen rate).
type CreditNoteInput struct {
	Reason string
	Amount Decimal
	Lines  []CreditNoteLine
	Kind   string
	Actor  string
}

// PaymentIntent is one request to the gateway seam and its outcome.
type PaymentIntent struct {
	ID          string    `json:"id"`
	CustomerID  string    `json:"customer_id"`
	Purpose     string    `json:"purpose"`
	StatementID string    `json:"statement_id,omitempty"`
	Amount      Decimal   `json:"amount"`
	Currency    string    `json:"currency"`
	Gateway     string    `json:"gateway,omitempty"`
	Status      string    `json:"status"`
	Reference   string    `json:"reference,omitempty"`
	PayURL      string    `json:"pay_url,omitempty"`
	Detail      string    `json:"detail,omitempty"`
	PaymentID   int64     `json:"payment_id,omitempty"`
	RequestedBy string    `json:"requested_by,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Suspension is one executed suspend or resume.
type Suspension struct {
	ID         int64     `json:"id"`
	CustomerID string    `json:"customer_id"`
	Action     string    `json:"action"`
	Source     string    `json:"source"`
	Reason     string    `json:"reason,omitempty"`
	OK         bool      `json:"ok"`
	Error      string    `json:"error,omitempty"`
	Actor      string    `json:"actor,omitempty"`
	At         time.Time `json:"at"`
}

// OpenInvoice is one unsettled invoice as the collections evaluator and the
// aging report see it.
type OpenInvoice struct {
	StatementID   string
	InvoiceNumber string
	CustomerID    string
	CustomerName  string
	CustomerSlug  string
	CustomerKind  string
	AdminEmail    string
	Currency      string
	Total         Decimal
	Outstanding   Decimal
	Status        string
	IssuedAt      time.Time
	DueAt         time.Time
	PeriodStart   string
	PeriodEnd     string
}

// ---------------------------------------------------------------------------
// the ledger
// ---------------------------------------------------------------------------

// postEntry appends one ledger row inside tx. It is the ONLY writer of
// account_entries, so every balance-moving path goes through one place.
func postEntry(ctx context.Context, tx *sql.Tx, e AccountEntry) error {
	var stmt, note any
	if e.StatementID != "" {
		stmt = e.StatementID
	}
	if e.CreditNoteID != "" {
		note = e.CreditNoteID
	}
	var pay any
	if e.PaymentID != 0 {
		pay = e.PaymentID
	}
	if e.EnteredAt.IsZero() {
		e.EnteredAt = time.Now().UTC()
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO account_entries (customer_id, kind, amount, currency, statement_id, payment_id, credit_note_id, reference, note, entered_at, entered_by)
		VALUES ($1, $2, $3::numeric, $4, $5, $6, $7, $8, $9, $10, $11) ON CONFLICT DO NOTHING`,
		e.CustomerID, e.Kind, string(e.Amount), e.Currency, stmt, pay, note, strings.TrimSpace(e.Reference), strings.TrimSpace(e.Note), e.EnteredAt.UTC(), e.EnteredBy)
	return mapErr(err)
}

// negate renders −d.
func negate(d Decimal) Decimal { return decOf(new(big.Rat).Neg(ratOf(d))) }

// GetAccountBalance reads the balance view for one customer.
func (s *Store) GetAccountBalance(ctx context.Context, scope Scope, customerID string) (AccountBalance, error) {
	if !scope.Allows(customerID) {
		return AccountBalance{}, ErrNotFound
	}
	return accountBalance(ctx, s.db, customerID)
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func accountBalance(ctx context.Context, q querier, customerID string) (AccountBalance, error) {
	var b AccountBalance
	var bal, avail, out string
	if err := q.QueryRowContext(ctx, `SELECT customer_id, balance::text, available_credit::text, outstanding::text FROM customer_balances WHERE customer_id = $1`, customerID).
		Scan(&b.CustomerID, &bal, &avail, &out); err != nil {
		return b, mapErr(err)
	}
	b.Balance, b.AvailableCredit, b.Outstanding = Decimal(bal), Decimal(avail), Decimal(out)
	return b, nil
}

// ListAccountEntries returns the ledger oldest first with a running balance.
func (s *Store) ListAccountEntries(ctx context.Context, scope Scope, customerID string, limit int) ([]AccountEntry, error) {
	if !scope.Allows(customerID) {
		return nil, ErrNotFound
	}
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.id, e.customer_id, e.kind, e.amount::text, e.currency, COALESCE(e.statement_id::text, ''), COALESCE(st.invoice_number, ''),
			COALESCE(e.payment_id, 0), COALESCE(e.credit_note_id::text, ''), COALESCE(n.number, ''), e.reference, e.note, e.entered_at, e.entered_by
		FROM account_entries e LEFT JOIN statements st ON st.id = e.statement_id LEFT JOIN credit_notes n ON n.id = e.credit_note_id
		WHERE e.customer_id = $1 ORDER BY e.entered_at, e.id LIMIT $2`, customerID, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []AccountEntry{}
	running := new(big.Rat)
	for rows.Next() {
		var e AccountEntry
		var amt string
		if err := rows.Scan(&e.ID, &e.CustomerID, &e.Kind, &amt, &e.Currency, &e.StatementID, &e.InvoiceNumber, &e.PaymentID, &e.CreditNoteID, &e.CreditNoteNo, &e.Reference, &e.Note, &e.EnteredAt, &e.EnteredBy); err != nil {
			return nil, err
		}
		e.Amount = Decimal(amt)
		running.Add(running, ratOf(e.Amount))
		e.Balance = decOf(running)
		e.EnteredAt = e.EnteredAt.UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// payments
// ---------------------------------------------------------------------------

const paymentColumns = `p.id, p.customer_id, COALESCE(p.statement_id::text, ''), p.amount::text, p.paid_at, p.method, p.reference, p.status, p.gateway, p.recorded_by, p.recorded_at,
	p.purpose, COALESCE(p.intent_id::text, ''), p.refunded_at, p.refund_reason, p.note,
	COALESCE((SELECT sum(a.amount) FROM invoice_allocations a WHERE a.payment_id = p.id), 0)::numeric(20,6)::text`

func scanPayment(row interface{ Scan(...any) error }) (StatementPayment, error) {
	var p StatementPayment
	var amt, alloc string
	var refunded sql.NullTime
	if err := row.Scan(&p.ID, &p.CustomerID, &p.StatementID, &amt, &p.PaidAt, &p.Method, &p.Reference, &p.Status, &p.Gateway, &p.RecordedBy, &p.RecordedAt,
		&p.Purpose, &p.IntentID, &refunded, &p.RefundReason, &p.Note, &alloc); err != nil {
		return p, mapErr(err)
	}
	p.Amount = Decimal(amt)
	p.PaidAt, p.RecordedAt = p.PaidAt.UTC(), p.RecordedAt.UTC()
	p.RefundedAt = timePtr(refunded)
	p.Allocated = Decimal(alloc)
	if p.Status == PaymentReceived {
		p.Unallocated = decOf(new(big.Rat).Sub(ratOf(p.Amount), ratOf(p.Allocated)))
	} else {
		p.Unallocated = "0"
	}
	return p, nil
}

// GetPayment reads one payment with its allocations, inside the scope.
func (s *Store) GetPayment(ctx context.Context, scope Scope, id int64) (StatementPayment, error) {
	p, err := scanPayment(s.db.QueryRowContext(ctx, `SELECT `+paymentColumns+` FROM payments p WHERE p.id = $1`, id))
	if err != nil {
		return p, err
	}
	if !scope.Allows(p.CustomerID) {
		return StatementPayment{}, ErrNotFound
	}
	p.Allocations, err = s.paymentAllocations(ctx, id)
	return p, err
}

func (s *Store) paymentAllocations(ctx context.Context, paymentID int64) ([]Allocation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, a.statement_id, COALESCE(st.invoice_number, ''), a.amount::text, a.allocated_at, a.allocated_by
		FROM invoice_allocations a JOIN statements st ON st.id = a.statement_id WHERE a.payment_id = $1 ORDER BY a.allocated_at, a.id`, paymentID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []Allocation{}
	for rows.Next() {
		var a Allocation
		var amt string
		if err := rows.Scan(&a.ID, &a.StatementID, &a.InvoiceNumber, &amt, &a.AllocatedAt, &a.AllocatedBy); err != nil {
			return nil, err
		}
		a.PaymentID = paymentID
		a.Amount = Decimal(amt)
		a.AllocatedAt = a.AllocatedAt.UTC()
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListCustomerPayments returns every payment of a customer, newest first,
// each with its allocations.
func (s *Store) ListCustomerPayments(ctx context.Context, scope Scope, customerID string) ([]StatementPayment, error) {
	if !scope.Allows(customerID) {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+paymentColumns+` FROM payments p WHERE p.customer_id = $1 ORDER BY p.paid_at DESC, p.id DESC`, customerID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []StatementPayment{}
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Allocations, err = s.paymentAllocations(ctx, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// CustomerPaymentInput records a payment for a CUSTOMER — with allocations,
// or with none, in which case the whole amount is credit on the account
// (a top-up). DESIGN.md §9.2 / §9.5.
type CustomerPaymentInput struct {
	CustomerID  string
	Payment     PaymentInput
	Purpose     string
	IntentID    string
	Note        string
	Allocations []AllocationInput
}

// normalisePayment applies the defaults and validates the facts a payment
// must carry; the lifecycle checks are the caller's.
func normalisePayment(in PaymentInput) (PaymentInput, *big.Rat, error) {
	amt := ratOf(in.Amount)
	if amt.Sign() <= 0 {
		return in, nil, fmt.Errorf("%w: a payment amount must be above zero", ErrInvalid)
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
		return in, nil, fmt.Errorf("%w: a payment method must be %s", ErrInvalid, strings.Join(PaymentMethods, ", "))
	}
	// "settled" is the wire word DESIGN.md §9 uses for the status lane 1
	// stored as received; both name the same fact.
	if strings.EqualFold(strings.TrimSpace(in.Status), "settled") {
		in.Status = PaymentReceived
	}
	if in.Status == "" {
		in.Status = PaymentReceived
	}
	if !oneOf(in.Status, []string{PaymentReceived, PaymentPending, PaymentFailed}) {
		return in, nil, fmt.Errorf("%w: a payment status must be settled, pending or failed", ErrInvalid)
	}
	return in, amt, nil
}

// RecordCustomerPayment books a payment against the customer's account and
// allocates it to the named invoices, if any. Every allocation must fit the
// invoice's outstanding balance and their sum must fit the payment; whatever
// is left is available credit. A settled payment posts its ledger credit in
// the same transaction — as a top-up when nothing was allocated.
func (s *Store) RecordCustomerPayment(ctx context.Context, in CustomerPaymentInput) (StatementPayment, error) {
	pay, amt, err := normalisePayment(in.Payment)
	if err != nil {
		return StatementPayment{}, err
	}
	purpose := in.Purpose
	if purpose == "" {
		purpose = PurposeCollection
		if len(in.Allocations) == 0 {
			purpose = PurposeCheckout
		}
	}
	if !oneOf(purpose, PaymentPurposes) {
		return StatementPayment{}, fmt.Errorf("%w: purpose must be checkout or collection", ErrInvalid)
	}
	sum := new(big.Rat)
	for _, a := range in.Allocations {
		if ratOf(a.Amount).Sign() <= 0 {
			return StatementPayment{}, fmt.Errorf("%w: an allocation amount must be above zero", ErrInvalid)
		}
		sum.Add(sum, ratOf(a.Amount))
	}
	if sum.Cmp(amt) > 0 {
		return StatementPayment{}, fmt.Errorf("%w: the allocations (%s) exceed the payment (%s)", ErrConflict, decOf(sum), decOf(amt))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return StatementPayment{}, err
	}
	defer tx.Rollback()
	var currency string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT currency FROM statements WHERE customer_id = $1 ORDER BY period_start DESC LIMIT 1),
		(SELECT currency FROM allocation_settings WHERE id = 1), 'OMR') FROM customers WHERE id = $1`, in.CustomerID).Scan(&currency); err != nil {
		return StatementPayment{}, mapErr(err)
	}
	var intent any
	if in.IntentID != "" {
		intent = in.IntentID
	}
	var stmtID any
	if len(in.Allocations) == 1 {
		stmtID = in.Allocations[0].StatementID
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO payments (customer_id, statement_id, amount, paid_at, method, reference, status, gateway, recorded_by, purpose, intent_id, note)
		VALUES ($1, $2, $3::numeric, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id`,
		in.CustomerID, stmtID, string(decOf(amt)), pay.PaidAt.UTC(), pay.Method, strings.TrimSpace(pay.Reference), pay.Status, pay.Gateway, pay.Actor, purpose, intent, strings.TrimSpace(in.Note)).Scan(&id); err != nil {
		return StatementPayment{}, mapErr(err)
	}
	if pay.Status == PaymentReceived {
		for _, a := range in.Allocations {
			if err := allocateTx(ctx, tx, in.CustomerID, a.StatementID, id, "", ratOf(a.Amount), pay.Actor, pay.PaidAt); err != nil {
				return StatementPayment{}, err
			}
		}
		kind := EntryPayment
		if len(in.Allocations) == 0 {
			kind = EntryTopUp
		}
		if err := postEntry(ctx, tx, AccountEntry{CustomerID: in.CustomerID, Kind: kind, Amount: negate(decOf(amt)), Currency: currency,
			PaymentID: id, Reference: pay.Reference, Note: in.Note, EnteredAt: pay.PaidAt, EnteredBy: pay.Actor}); err != nil {
			return StatementPayment{}, err
		}
	}
	if in.IntentID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE payment_intents SET status = CASE WHEN $2 = 'received' THEN 'settled' ELSE $2 END, payment_id = $3, updated_at = now() WHERE id = $1`, in.IntentID, pay.Status, id); err != nil {
			return StatementPayment{}, mapErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return StatementPayment{}, err
	}
	return s.GetPayment(ctx, OperatorScope, id)
}

// invoiceOutstandingTx locks an invoice and returns its status and what it
// still carries: total − payment allocations − credit-note allocations.
func invoiceOutstandingTx(ctx context.Context, tx *sql.Tx, statementID string) (customerID, status string, total, outstanding *big.Rat, err error) {
	var t, paid, credited string
	if err := tx.QueryRowContext(ctx, `SELECT st.customer_id, st.status, st.total::text,
		COALESCE((SELECT sum(a.amount) FROM invoice_allocations a JOIN payments p ON p.id = a.payment_id WHERE a.statement_id = st.id AND p.status = 'received'), 0)::numeric(20,6)::text,
		COALESCE((SELECT sum(a.amount) FROM invoice_allocations a WHERE a.statement_id = st.id AND a.credit_note_id IS NOT NULL), 0)::numeric(20,6)::text
		FROM statements st WHERE st.id = $1 FOR UPDATE`, statementID).Scan(&customerID, &status, &t, &paid, &credited); err != nil {
		return "", "", nil, nil, mapErr(err)
	}
	total = ratOf(Decimal(t))
	outstanding = new(big.Rat).Sub(total, ratOf(Decimal(paid)))
	outstanding.Sub(outstanding, ratOf(Decimal(credited)))
	return customerID, status, total, outstanding, nil
}

// allocateTx applies `amount` of a payment (paymentID) or a credit note
// (creditNoteID) to an invoice inside tx: the invoice must be open, belong to
// the customer, and carry at least that much; reaching zero settles it.
func allocateTx(ctx context.Context, tx *sql.Tx, customerID, statementID string, paymentID int64, creditNoteID string, amount *big.Rat, actor string, at time.Time) error {
	owner, status, _, outstanding, err := invoiceOutstandingTx(ctx, tx, statementID)
	if err != nil {
		return err
	}
	if owner != customerID {
		return fmt.Errorf("%w: invoice %s belongs to another customer", ErrConflict, statementID)
	}
	if status != StatusIssued && status != StatusSent {
		return fmt.Errorf("%w: a %s invoice cannot be settled", ErrConflict, status)
	}
	if amount.Cmp(outstanding) > 0 {
		return fmt.Errorf("%w: allocation of %s exceeds the outstanding balance of %s", ErrConflict, decOf(amount), decOf(outstanding))
	}
	var pay, note any
	if paymentID != 0 {
		pay = paymentID
	}
	if creditNoteID != "" {
		note = creditNoteID
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO invoice_allocations (statement_id, payment_id, credit_note_id, amount, allocated_at, allocated_by) VALUES ($1, $2, $3, $4::numeric, $5, $6)`,
		statementID, pay, note, string(decOf(amount)), at.UTC(), actor); err != nil {
		return mapErr(err)
	}
	if new(big.Rat).Sub(outstanding, amount).Sign() == 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE statements SET status = 'paid', paid_at = COALESCE(paid_at, $2) WHERE id = $1 AND status IN ('issued','sent')`, statementID, at.UTC()); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

// AllocatePayment applies more of a settled payment to invoices. With
// explicit allocations each is checked against the invoice and the
// payment's unallocated remainder; with auto = true the remainder is applied
// oldest due date first across the customer's open invoices. Either way it
// is an explicit operator action, never implicit (DESIGN.md §9.2).
func (s *Store) AllocatePayment(ctx context.Context, paymentID int64, allocations []AllocationInput, auto bool, actor string) (StatementPayment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return StatementPayment{}, err
	}
	defer tx.Rollback()
	var customerID, status, amtS, allocS string
	var paidAt time.Time
	if err := tx.QueryRowContext(ctx, `SELECT p.customer_id, p.status, p.amount::text, p.paid_at,
		COALESCE((SELECT sum(a.amount) FROM invoice_allocations a WHERE a.payment_id = p.id), 0)::numeric(20,6)::text
		FROM payments p WHERE p.id = $1 FOR UPDATE`, paymentID).Scan(&customerID, &status, &amtS, &paidAt, &allocS); err != nil {
		return StatementPayment{}, mapErr(err)
	}
	if status != PaymentReceived {
		return StatementPayment{}, fmt.Errorf("%w: a %s payment settles nothing and cannot be allocated", ErrConflict, status)
	}
	remaining := new(big.Rat).Sub(ratOf(Decimal(amtS)), ratOf(Decimal(allocS)))
	if remaining.Sign() <= 0 {
		return StatementPayment{}, fmt.Errorf("%w: the payment is fully allocated", ErrConflict)
	}
	now := time.Now().UTC()
	if auto {
		open, err := openInvoicesTx(ctx, tx, customerID)
		if err != nil {
			return StatementPayment{}, err
		}
		for _, id := range open {
			if remaining.Sign() <= 0 {
				break
			}
			_, _, _, outstanding, err := invoiceOutstandingTx(ctx, tx, id)
			if err != nil {
				return StatementPayment{}, err
			}
			take := new(big.Rat).Set(outstanding)
			if take.Cmp(remaining) > 0 {
				take.Set(remaining)
			}
			if take.Sign() <= 0 {
				continue
			}
			if err := allocateTx(ctx, tx, customerID, id, paymentID, "", take, actor, now); err != nil {
				return StatementPayment{}, err
			}
			remaining.Sub(remaining, take)
		}
	} else {
		if len(allocations) == 0 {
			return StatementPayment{}, fmt.Errorf("%w: give allocations, or auto = true", ErrInvalid)
		}
		sum := new(big.Rat)
		for _, a := range allocations {
			if ratOf(a.Amount).Sign() <= 0 {
				return StatementPayment{}, fmt.Errorf("%w: an allocation amount must be above zero", ErrInvalid)
			}
			sum.Add(sum, ratOf(a.Amount))
		}
		if sum.Cmp(remaining) > 0 {
			return StatementPayment{}, fmt.Errorf("%w: the allocations (%s) exceed the unallocated %s of this payment", ErrConflict, decOf(sum), decOf(remaining))
		}
		for _, a := range allocations {
			if err := allocateTx(ctx, tx, customerID, a.StatementID, paymentID, "", ratOf(a.Amount), actor, now); err != nil {
				return StatementPayment{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return StatementPayment{}, err
	}
	return s.GetPayment(ctx, OperatorScope, paymentID)
}

// openInvoicesTx lists a customer's open invoices oldest due date first —
// the order credit is applied in when the operator does not choose.
func openInvoicesTx(ctx context.Context, tx *sql.Tx, customerID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM statements WHERE customer_id = $1 AND status IN ('issued','sent') ORDER BY due_at NULLS LAST, issued_at, id`, customerID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// RefundPayment reverses a settled payment: its allocations are released
// (the invoices they settled are open again), the payment reads refunded,
// and the ledger gets a refund debit. Append-only: the original credit
// stays, the refund offsets it.
func (s *Store) RefundPayment(ctx context.Context, paymentID int64, reason, actor string) (StatementPayment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return StatementPayment{}, err
	}
	defer tx.Rollback()
	var customerID, status, amtS, reference string
	if err := tx.QueryRowContext(ctx, `SELECT customer_id, status, amount::text, reference FROM payments WHERE id = $1 FOR UPDATE`, paymentID).Scan(&customerID, &status, &amtS, &reference); err != nil {
		return StatementPayment{}, mapErr(err)
	}
	if status != PaymentReceived {
		return StatementPayment{}, fmt.Errorf("%w: only a settled payment can be refunded; this one is %s", ErrConflict, status)
	}
	rows, err := tx.QueryContext(ctx, `SELECT statement_id FROM invoice_allocations WHERE payment_id = $1`, paymentID)
	if err != nil {
		return StatementPayment{}, mapErr(err)
	}
	var reopened []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return StatementPayment{}, err
		}
		reopened = append(reopened, id)
	}
	rows.Close()
	if _, err := tx.ExecContext(ctx, `DELETE FROM invoice_allocations WHERE payment_id = $1`, paymentID); err != nil {
		return StatementPayment{}, mapErr(err)
	}
	for _, id := range reopened {
		// A settled invoice whose money went back is due again: sent if it
		// was sent, issued otherwise.
		if _, err := tx.ExecContext(ctx, `UPDATE statements SET status = CASE WHEN sent_at IS NOT NULL THEN 'sent' ELSE 'issued' END, paid_at = NULL WHERE id = $1 AND status = 'paid'`, id); err != nil {
			return StatementPayment{}, mapErr(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE payments SET status = 'refunded', refunded_at = now(), refund_reason = $2 WHERE id = $1`, paymentID, strings.TrimSpace(reason)); err != nil {
		return StatementPayment{}, mapErr(err)
	}
	var currency string
	if err := tx.QueryRowContext(ctx, `SELECT currency FROM account_entries WHERE payment_id = $1 ORDER BY id LIMIT 1`, paymentID).Scan(&currency); err != nil {
		if err != sql.ErrNoRows {
			return StatementPayment{}, mapErr(err)
		}
		currency = "OMR"
	}
	if err := postEntry(ctx, tx, AccountEntry{CustomerID: customerID, Kind: EntryRefund, Amount: Decimal(amtS), Currency: currency, PaymentID: paymentID, Reference: reference, Note: reason, EnteredBy: actor}); err != nil {
		return StatementPayment{}, err
	}
	if err := tx.Commit(); err != nil {
		return StatementPayment{}, err
	}
	return s.GetPayment(ctx, OperatorScope, paymentID)
}

// ---------------------------------------------------------------------------
// applying account credit
// ---------------------------------------------------------------------------

// creditSource is one thing available credit comes from: a settled payment
// with an unallocated remainder, or a credit note not fully applied.
type creditSource struct {
	paymentID    int64
	creditNoteID string
	remaining    *big.Rat
}

// creditSourcesTx lists what the customer's available credit consists of,
// oldest first, so credit is consumed in the order it arrived.
func creditSourcesTx(ctx context.Context, tx *sql.Tx, customerID string) ([]creditSource, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT p.id, NULL::uuid, (p.amount - COALESCE((SELECT sum(a.amount) FROM invoice_allocations a WHERE a.payment_id = p.id), 0))::numeric(20,6)::text, p.paid_at
		  FROM payments p WHERE p.customer_id = $1 AND p.status = 'received'
		UNION ALL
		SELECT NULL::bigint, n.id, (n.total - COALESCE((SELECT sum(a.amount) FROM invoice_allocations a WHERE a.credit_note_id = n.id), 0))::numeric(20,6)::text, n.issued_at
		  FROM credit_notes n WHERE n.customer_id = $1
		ORDER BY 4, 1, 2`, customerID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []creditSource
	for rows.Next() {
		var pid sql.NullInt64
		var nid sql.NullString
		var rem string
		var at time.Time
		if err := rows.Scan(&pid, &nid, &rem, &at); err != nil {
			return nil, err
		}
		r := ratOf(Decimal(rem))
		if r.Sign() <= 0 {
			continue
		}
		out = append(out, creditSource{paymentID: pid.Int64, creditNoteID: nid.String, remaining: r})
	}
	return out, rows.Err()
}

// applyCreditTx applies the customer's available credit to ONE invoice,
// oldest credit first, and reports how much was applied.
func applyCreditTx(ctx context.Context, tx *sql.Tx, customerID, statementID, actor string, now time.Time) (Decimal, error) {
	_, status, _, outstanding, err := invoiceOutstandingTx(ctx, tx, statementID)
	if err != nil {
		return "0", err
	}
	if status != StatusIssued && status != StatusSent {
		return "0", fmt.Errorf("%w: a %s invoice cannot be settled from credit", ErrConflict, status)
	}
	sources, err := creditSourcesTx(ctx, tx, customerID)
	if err != nil {
		return "0", err
	}
	applied := new(big.Rat)
	for _, src := range sources {
		if outstanding.Sign() <= 0 {
			break
		}
		take := new(big.Rat).Set(src.remaining)
		if take.Cmp(outstanding) > 0 {
			take.Set(outstanding)
		}
		if err := allocateTx(ctx, tx, customerID, statementID, src.paymentID, src.creditNoteID, take, actor, now); err != nil {
			return "0", err
		}
		outstanding.Sub(outstanding, take)
		applied.Add(applied, take)
	}
	return decOf(applied), nil
}

// ApplyCredit settles invoices from the customer's available credit: the
// invoices named, in the order given, or — with none named — every open
// invoice oldest due date first. Explicit, never implicit (DESIGN.md §9.5).
// It reports what was applied per invoice.
func (s *Store) ApplyCredit(ctx context.Context, customerID string, statementIDs []string, actor string) (map[string]Decimal, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ids := statementIDs
	if len(ids) == 0 {
		if ids, err = openInvoicesTx(ctx, tx, customerID); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	applied := map[string]Decimal{}
	for _, id := range ids {
		owner, _, _, _, err := invoiceOutstandingTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if owner != customerID {
			return nil, fmt.Errorf("%w: invoice %s belongs to another customer", ErrConflict, id)
		}
		v, err := applyCreditTx(ctx, tx, customerID, id, actor, now)
		if err != nil {
			return nil, err
		}
		applied[id] = v
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return applied, nil
}

// ---------------------------------------------------------------------------
// credit notes
// ---------------------------------------------------------------------------

// CreditNoteNumberFor formats one credit-note number.
func CreditNoteNumberFor(prefix string, year int, seq int64) string {
	return fmt.Sprintf("%s-%04d-%05d", prefix, year, seq)
}

// nextCreditNoteNumber takes the next number of the year INSIDE tx, exactly
// as nextInvoiceNumber does: the year row's lock serialises concurrent
// issues and a rollback leaves no hole.
func nextCreditNoteNumber(ctx context.Context, tx *sql.Tx, prefix string) (string, error) {
	var year int
	var seq int64
	err := tx.QueryRowContext(ctx, `
		WITH y AS (SELECT EXTRACT(YEAR FROM (now() AT TIME ZONE 'UTC'))::int AS yr)
		INSERT INTO credit_note_sequences (year, last_value) SELECT yr, 1 FROM y
		ON CONFLICT (year) DO UPDATE SET last_value = credit_note_sequences.last_value + 1
		RETURNING year, last_value`).Scan(&year, &seq)
	if err != nil {
		return "", mapErr(err)
	}
	return CreditNoteNumberFor(prefix, year, seq), nil
}

const creditNoteColumns = `n.id, n.customer_id, n.statement_id, COALESCE(st.invoice_number, ''), n.number, n.kind, n.reason, n.currency, n.subtotal::text, n.tax_rate::text, n.tax::text, n.total::text, n.lines, n.issued_at, n.issued_by,
	COALESCE((SELECT sum(a.amount) FROM invoice_allocations a WHERE a.credit_note_id = n.id), 0)::numeric(20,6)::text`

func scanCreditNote(row interface{ Scan(...any) error }) (CreditNote, error) {
	var n CreditNote
	var sub, rate, tax, total, applied string
	var lines []byte
	if err := row.Scan(&n.ID, &n.CustomerID, &n.StatementID, &n.InvoiceNumber, &n.Number, &n.Kind, &n.Reason, &n.Currency, &sub, &rate, &tax, &total, &lines, &n.IssuedAt, &n.IssuedBy, &applied); err != nil {
		return n, mapErr(err)
	}
	n.Subtotal, n.TaxRate, n.Tax, n.Total, n.Applied = Decimal(sub), Decimal(rate), Decimal(tax), Decimal(total), Decimal(applied)
	n.Unapplied = decOf(new(big.Rat).Sub(ratOf(n.Total), ratOf(n.Applied)))
	n.IssuedAt = n.IssuedAt.UTC()
	if len(lines) > 0 && string(lines) != "null" {
		_ = json.Unmarshal(lines, &n.Lines)
	}
	return n, nil
}

// GetCreditNote reads one credit note inside the scope.
func (s *Store) GetCreditNote(ctx context.Context, scope Scope, id string) (CreditNote, error) {
	n, err := scanCreditNote(s.db.QueryRowContext(ctx, `SELECT `+creditNoteColumns+` FROM credit_notes n JOIN statements st ON st.id = n.statement_id WHERE n.id = $1`, id))
	if err != nil {
		return n, err
	}
	if !scope.Allows(n.CustomerID) {
		return CreditNote{}, ErrNotFound
	}
	return n, nil
}

func (s *Store) listCreditNotes(ctx context.Context, where string, arg any) ([]CreditNote, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+creditNoteColumns+` FROM credit_notes n JOIN statements st ON st.id = n.statement_id WHERE `+where+` ORDER BY n.issued_at DESC, n.number DESC`, arg)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []CreditNote{}
	for rows.Next() {
		n, err := scanCreditNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ListStatementCreditNotes returns an invoice's credit notes, inside the scope.
func (s *Store) ListStatementCreditNotes(ctx context.Context, scope Scope, statementID string) ([]CreditNote, error) {
	var customerID string
	if err := s.db.QueryRowContext(ctx, `SELECT customer_id FROM statements WHERE id = $1`, statementID).Scan(&customerID); err != nil {
		return nil, mapErr(err)
	}
	if !scope.Allows(customerID) {
		return nil, ErrNotFound
	}
	return s.listCreditNotes(ctx, `n.statement_id = $1`, statementID)
}

// ListCustomerCreditNotes returns a customer's credit notes, newest first.
func (s *Store) ListCustomerCreditNotes(ctx context.Context, scope Scope, customerID string) ([]CreditNote, error) {
	if !scope.Allows(customerID) {
		return nil, ErrNotFound
	}
	return s.listCreditNotes(ctx, `n.customer_id = $1`, customerID)
}

// roundRat renders a rational at `scale` decimals, half away from zero.
func roundRat(r *big.Rat, scale int) *big.Rat {
	s := r.FloatString(scale)
	out, _ := new(big.Rat).SetString(s)
	return out
}

// creditNoteFigures derives subtotal / tax / total for a credit note. A lump
// amount is TAX-INCLUSIVE — the operator credits what the customer is owed —
// and the net is backed out at the invoice's frozen rate; lines are net and
// carry tax at that rate.
func creditNoteFigures(in CreditNoteInput, rate Decimal) (subtotal, tax, total *big.Rat, err error) {
	r := ratOf(rate)
	onePlus := new(big.Rat).Add(big.NewRat(1, 1), r)
	if len(in.Lines) > 0 {
		subtotal = new(big.Rat)
		for i, l := range in.Lines {
			a := ratOf(l.Amount)
			if a.Sign() <= 0 {
				return nil, nil, nil, fmt.Errorf("%w: line %d of the credit note has no amount above zero", ErrInvalid, i+1)
			}
			subtotal.Add(subtotal, a)
		}
		subtotal = roundRat(subtotal, 6)
		tax = roundRat(new(big.Rat).Mul(subtotal, r), 6)
		total = new(big.Rat).Add(subtotal, tax)
		return subtotal, tax, total, nil
	}
	total = ratOf(in.Amount)
	if total.Sign() <= 0 {
		return nil, nil, nil, fmt.Errorf("%w: a credit note needs an amount above zero, or lines", ErrInvalid)
	}
	total = roundRat(total, 6)
	subtotal = roundRat(new(big.Rat).Quo(total, onePlus), 6)
	tax = new(big.Rat).Sub(total, subtotal)
	return subtotal, tax, total, nil
}

// CreateCreditNote issues a credit note against an invoice (DESIGN.md §9.3):
// numbered gaplessly, tax at the invoice's frozen rate, refused when the
// invoice is a draft, voided, or would be credited beyond its total. The
// note is applied to the invoice up to what it still carries — settling it
// when that reaches zero — and the rest is credit on the account, which is
// how an invoice already paid in full can still be credited. Everything,
// including the ledger credit, commits in one transaction.
func (s *Store) CreateCreditNote(ctx context.Context, statementID string, in CreditNoteInput) (CreditNote, error) {
	kind := in.Kind
	if kind == "" {
		kind = CreditNotePartial
	}
	if !oneOf(kind, []string{CreditNotePartial, CreditNoteFull, CreditNoteWriteOff}) {
		return CreditNote{}, fmt.Errorf("%w: kind must be partial, full or write_off", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CreditNote{}, err
	}
	defer tx.Rollback()
	note, err := createCreditNoteTx(ctx, tx, statementID, in, kind)
	if err != nil {
		return CreditNote{}, err
	}
	if err := tx.Commit(); err != nil {
		return CreditNote{}, err
	}
	return s.GetCreditNote(ctx, OperatorScope, note)
}

func createCreditNoteTx(ctx context.Context, tx *sql.Tx, statementID string, in CreditNoteInput, kind string) (string, error) {
	customerID, status, total, outstanding, err := invoiceOutstandingTx(ctx, tx, statementID)
	if err != nil {
		return "", err
	}
	switch status {
	case StatusDraft:
		return "", fmt.Errorf("%w: a draft has no invoice to credit; issue it first, or delete the draft", ErrConflict)
	case StatusCancelled:
		return "", fmt.Errorf("%w: a cancelled invoice cannot be credited", ErrConflict)
	}
	var currency, rateS string
	var snap []byte
	if err := tx.QueryRowContext(ctx, `SELECT currency, tax_rate::text, tax_snapshot FROM statements WHERE id = $1`, statementID).Scan(&currency, &rateS, &snap); err != nil {
		return "", mapErr(err)
	}
	rate := Decimal(rateS)
	if len(snap) > 0 {
		var ts TaxSnapshot
		if json.Unmarshal(snap, &ts) == nil && strings.TrimSpace(string(ts.Rate)) != "" {
			rate = ts.Rate
		}
	}
	if kind == CreditNoteFull {
		// A full credit note credits the whole invoice, whatever was
		// already paid: the paid part becomes credit on the account.
		in.Amount = decOf(total)
		in.Lines = nil
	}
	subtotal, tax, noteTotal, err := creditNoteFigures(in, rate)
	if err != nil {
		return "", err
	}
	var creditedS string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(sum(total), 0)::numeric(20,6)::text FROM credit_notes WHERE statement_id = $1`, statementID).Scan(&creditedS); err != nil {
		return "", mapErr(err)
	}
	if new(big.Rat).Add(ratOf(Decimal(creditedS)), noteTotal).Cmp(total) > 0 {
		room := new(big.Rat).Sub(total, ratOf(Decimal(creditedS)))
		return "", fmt.Errorf("%w: a credit note of %s would exceed the invoice; %s of its %s total can still be credited", ErrConflict, decOf(noteTotal), decOf(room), decOf(total))
	}
	settings, err := billingSettingsTx(ctx, tx)
	if err != nil {
		return "", err
	}
	number, err := nextCreditNoteNumber(ctx, tx, settings.CreditNotePrefix)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	var linesJSON any
	if len(in.Lines) > 0 {
		b, _ := json.Marshal(in.Lines)
		linesJSON = b
	}
	var id string
	if err := tx.QueryRowContext(ctx, `INSERT INTO credit_notes (customer_id, statement_id, number, kind, reason, currency, subtotal, tax_rate, tax, total, lines, issued_at, issued_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7::numeric, $8::numeric, $9::numeric, $10::numeric, $11, $12, $13) RETURNING id`,
		customerID, statementID, number, kind, strings.TrimSpace(in.Reason), currency, decOf(subtotal), string(rate), decOf(tax), decOf(noteTotal), linesJSON, now, in.Actor).Scan(&id); err != nil {
		return "", mapErr(err)
	}
	// Apply it to the invoice as far as the invoice still carries; the rest
	// is credit on the account.
	if outstanding.Sign() > 0 && status != StatusPaid {
		apply := new(big.Rat).Set(noteTotal)
		if apply.Cmp(outstanding) > 0 {
			apply.Set(outstanding)
		}
		if err := allocateTx(ctx, tx, customerID, statementID, 0, id, apply, in.Actor, now); err != nil {
			return "", err
		}
	}
	entryKind := EntryCreditNote
	if kind == CreditNoteWriteOff {
		entryKind = EntryWriteOff
	}
	if err := postEntry(ctx, tx, AccountEntry{CustomerID: customerID, Kind: entryKind, Amount: negate(decOf(noteTotal)), Currency: currency, StatementID: statementID, CreditNoteID: id,
		Reference: number, Note: in.Reason, EnteredAt: now, EnteredBy: in.Actor}); err != nil {
		return "", err
	}
	if kind == CreditNoteFull {
		if _, err := tx.ExecContext(ctx, `UPDATE statements SET status = 'cancelled', cancelled_at = $2, cancel_reason = $3 WHERE id = $1`, statementID, now, strings.TrimSpace(in.Reason)); err != nil {
			return "", mapErr(err)
		}
	}
	return id, nil
}

// ---------------------------------------------------------------------------
// tax snapshot
// ---------------------------------------------------------------------------

// taxSnapshotTx builds the snapshot an invoice freezes at issue from the
// customer's profile and the Sovereign's identity as they stand right now.
func taxSnapshotTx(ctx context.Context, tx *sql.Tx, customerID string, rate Decimal, settings BillingSettings) (TaxSnapshot, error) {
	var ts TaxSnapshot
	if err := tx.QueryRowContext(ctx, `SELECT name, tax_registration_number, tax_exempt, tax_exempt_reason FROM customers WHERE id = $1`, customerID).
		Scan(&ts.CustomerName, &ts.CustomerTaxNumber, &ts.Exempt, &ts.ExemptReason); err != nil {
		return ts, mapErr(err)
	}
	ts.Rate = rate
	ts.SellerLegalName, ts.SellerTaxNumber, ts.SellerAddress = settings.LegalName, settings.TaxRegistrationNumber, settings.Address
	return ts, nil
}

// ---------------------------------------------------------------------------
// collections: open invoices, reminders, suspension
// ---------------------------------------------------------------------------

// ListOpenInvoices returns every invoice that is issued or sent with money
// outstanding and a due date, oldest due first — the collections evaluator's
// and the aging report's input, in every mode.
func (s *Store) ListOpenInvoices(ctx context.Context, scope Scope) ([]OpenInvoice, error) {
	q := `SELECT st.id, COALESCE(st.invoice_number, st.external_invoice_ref, ''), st.customer_id, c.name, c.slug, c.kind, c.admin_email, st.currency, st.total::text,
		(st.total
		  - COALESCE((SELECT sum(a.amount) FROM invoice_allocations a JOIN payments p ON p.id = a.payment_id WHERE a.statement_id = st.id AND p.status = 'received'), 0)
		  - COALESCE((SELECT sum(a.amount) FROM invoice_allocations a WHERE a.statement_id = st.id AND a.credit_note_id IS NOT NULL), 0))::numeric(20,6)::text,
		st.status, st.issued_at, st.due_at, to_char(st.period_start, 'YYYY-MM-DD'), to_char(st.period_end, 'YYYY-MM-DD')
		FROM statements st JOIN customers c ON c.id = st.customer_id
		WHERE st.status IN ('issued','sent') AND st.due_at IS NOT NULL AND st.issued_at IS NOT NULL`
	var args []any
	if !scope.Operator {
		q += ` AND st.customer_id = $1`
		args = append(args, scope.CustomerID)
	}
	q += ` ORDER BY st.due_at, st.issued_at, st.id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []OpenInvoice{}
	for rows.Next() {
		var o OpenInvoice
		var total, outstanding string
		if err := rows.Scan(&o.StatementID, &o.InvoiceNumber, &o.CustomerID, &o.CustomerName, &o.CustomerSlug, &o.CustomerKind, &o.AdminEmail, &o.Currency, &total, &outstanding, &o.Status, &o.IssuedAt, &o.DueAt, &o.PeriodStart, &o.PeriodEnd); err != nil {
			return nil, err
		}
		o.Total, o.Outstanding = Decimal(total), Decimal(outstanding)
		if ratOf(o.Outstanding).Sign() <= 0 {
			continue
		}
		o.IssuedAt, o.DueAt = o.IssuedAt.UTC(), o.DueAt.UTC()
		out = append(out, o)
	}
	return out, rows.Err()
}

// RecordReminder marks a reminder or escalation stage sent for an invoice.
// It reports whether THIS call inserted the row: the second call for the
// same (statement, kind, stage) does nothing and returns false, which is
// what makes the daily evaluator unable to send a stage twice.
func (s *Store) RecordReminder(ctx context.Context, statementID, kind string, stage int, recipients []string) (bool, error) {
	if recipients == nil {
		recipients = []string{}
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO collection_reminders (statement_id, kind, stage, recipients) VALUES ($1, $2, $3, $4) ON CONFLICT (statement_id, kind, stage) DO NOTHING`,
		statementID, kind, stage, pq.Array(recipients))
	if err != nil {
		return false, mapErr(err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ReminderRecord is one sent reminder or escalation.
type ReminderRecord struct {
	StatementID string    `json:"statement_id"`
	Kind        string    `json:"kind"`
	Stage       int       `json:"stage"`
	SentAt      time.Time `json:"sent_at"`
	Recipients  []string  `json:"recipients"`
}

// ListReminders returns what was sent for one invoice, oldest first.
func (s *Store) ListReminders(ctx context.Context, statementID string) ([]ReminderRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT statement_id, kind, stage, sent_at, recipients FROM collection_reminders WHERE statement_id = $1 ORDER BY sent_at, id`, statementID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []ReminderRecord{}
	for rows.Next() {
		var r ReminderRecord
		if err := rows.Scan(&r.StatementID, &r.Kind, &r.Stage, &r.SentAt, pq.Array(&r.Recipients)); err != nil {
			return nil, err
		}
		r.SentAt = r.SentAt.UTC()
		if r.Recipients == nil {
			r.Recipients = []string{}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecordSuspension appends one executed suspend / resume with its outcome and,
// when it succeeded, stamps the customer's platform suspension state.
func (s *Store) RecordSuspension(ctx context.Context, in Suspension) (Suspension, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Suspension{}, err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `INSERT INTO customer_suspensions (customer_id, action, source, reason, ok, error, actor) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id, at`,
		in.CustomerID, in.Action, in.Source, strings.TrimSpace(in.Reason), in.OK, in.Error, in.Actor).Scan(&in.ID, &in.At); err != nil {
		return Suspension{}, mapErr(err)
	}
	if in.OK {
		switch in.Action {
		case "suspend":
			if _, err := tx.ExecContext(ctx, `UPDATE customers SET platform_suspended_at = COALESCE(platform_suspended_at, now()), suspension_reason = $2, suspension_source = $3, status = 'suspended', updated_at = now() WHERE id = $1`, in.CustomerID, strings.TrimSpace(in.Reason), in.Source); err != nil {
				return Suspension{}, mapErr(err)
			}
		case "resume":
			if _, err := tx.ExecContext(ctx, `UPDATE customers SET platform_suspended_at = NULL, suspension_reason = '', suspension_source = '', status = CASE WHEN status = 'suspended' THEN 'active' ELSE status END, updated_at = now() WHERE id = $1`, in.CustomerID); err != nil {
				return Suspension{}, mapErr(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return Suspension{}, err
	}
	in.At = in.At.UTC()
	return in, nil
}

// ListSuspensions returns a customer's suspension history, newest first.
func (s *Store) ListSuspensions(ctx context.Context, scope Scope, customerID string) ([]Suspension, error) {
	if !scope.Allows(customerID) {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, customer_id, action, source, reason, ok, error, actor, at FROM customer_suspensions WHERE customer_id = $1 ORDER BY at DESC, id DESC LIMIT 200`, customerID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []Suspension{}
	for rows.Next() {
		var x Suspension
		if err := rows.Scan(&x.ID, &x.CustomerID, &x.Action, &x.Source, &x.Reason, &x.OK, &x.Error, &x.Actor, &x.At); err != nil {
			return nil, err
		}
		x.At = x.At.UTC()
		out = append(out, x)
	}
	return out, rows.Err()
}

// SetLowBalanceAlerted stamps (or clears, with nil) the low-balance alert so
// a wallet crossing is mailed once and again only after it recovered.
func (s *Store) SetLowBalanceAlerted(ctx context.Context, customerID string, at *time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE customers SET low_balance_alerted_at = $2 WHERE id = $1`, customerID, nullTime(at))
	return mapErr(err)
}

// SetExternalBalance records the balance the operator's billing system
// reported for a customer (external mode).
func (s *Store) SetExternalBalance(ctx context.Context, customerID string, balance Decimal, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE customers SET external_balance = $2::numeric, external_balance_at = $3, updated_at = now() WHERE id = $1`, customerID, string(balance), at.UTC())
	return mapErr(err)
}

// GetCustomerByExternalAccount resolves the customer an external document
// names by its billing-account id.
func (s *Store) GetCustomerByExternalAccount(ctx context.Context, externalAccountID string) (Customer, error) {
	id := strings.TrimSpace(externalAccountID)
	if id == "" {
		return Customer{}, ErrNotFound
	}
	return scanCustomer(s.db.QueryRowContext(ctx, `SELECT `+customerColumns+` FROM customers c WHERE c.external_account_id = $1 ORDER BY c.created_at LIMIT 1`, id))
}

// ---------------------------------------------------------------------------
// payment intents
// ---------------------------------------------------------------------------

const intentColumns = `i.id, i.customer_id, i.purpose, COALESCE(i.statement_id::text, ''), i.amount::text, i.currency, i.gateway, i.status, i.reference, i.pay_url, i.detail, COALESCE(i.payment_id, 0), i.requested_by, i.created_at, i.updated_at`

func scanIntent(row interface{ Scan(...any) error }) (PaymentIntent, error) {
	var in PaymentIntent
	var amt string
	if err := row.Scan(&in.ID, &in.CustomerID, &in.Purpose, &in.StatementID, &amt, &in.Currency, &in.Gateway, &in.Status, &in.Reference, &in.PayURL, &in.Detail, &in.PaymentID, &in.RequestedBy, &in.CreatedAt, &in.UpdatedAt); err != nil {
		return in, mapErr(err)
	}
	in.Amount = Decimal(amt)
	in.CreatedAt, in.UpdatedAt = in.CreatedAt.UTC(), in.UpdatedAt.UTC()
	return in, nil
}

// CreatePaymentIntent records a request to the gateway seam before it is made.
func (s *Store) CreatePaymentIntent(ctx context.Context, in PaymentIntent) (PaymentIntent, error) {
	if !oneOf(in.Purpose, PaymentPurposes) {
		return PaymentIntent{}, fmt.Errorf("%w: purpose must be checkout or collection", ErrInvalid)
	}
	if ratOf(in.Amount).Sign() <= 0 {
		return PaymentIntent{}, fmt.Errorf("%w: an amount above zero is required", ErrInvalid)
	}
	if in.Status == "" {
		in.Status = IntentRequested
	}
	var stmt any
	if in.StatementID != "" {
		stmt = in.StatementID
	}
	var id string
	if err := s.db.QueryRowContext(ctx, `INSERT INTO payment_intents (customer_id, purpose, statement_id, amount, currency, gateway, status, reference, pay_url, detail, requested_by)
		VALUES ($1, $2, $3, $4::numeric, $5, $6, $7, $8, $9, $10, $11) RETURNING id`,
		in.CustomerID, in.Purpose, stmt, string(decOf(ratOf(in.Amount))), in.Currency, in.Gateway, in.Status, in.Reference, in.PayURL, in.Detail, in.RequestedBy).Scan(&id); err != nil {
		return PaymentIntent{}, mapErr(err)
	}
	return s.GetPaymentIntent(ctx, OperatorScope, id)
}

// UpdatePaymentIntent records the gateway's answer.
func (s *Store) UpdatePaymentIntent(ctx context.Context, id, status, gateway, reference, payURL, detail string) (PaymentIntent, error) {
	if _, err := s.db.ExecContext(ctx, `UPDATE payment_intents SET status = $2, gateway = COALESCE(NULLIF($3, ''), gateway), reference = COALESCE(NULLIF($4, ''), reference), pay_url = $5, detail = $6, updated_at = now() WHERE id = $1`,
		id, status, gateway, reference, payURL, detail); err != nil {
		return PaymentIntent{}, mapErr(err)
	}
	return s.GetPaymentIntent(ctx, OperatorScope, id)
}

// GetPaymentIntent reads one intent inside the scope.
func (s *Store) GetPaymentIntent(ctx context.Context, scope Scope, id string) (PaymentIntent, error) {
	in, err := scanIntent(s.db.QueryRowContext(ctx, `SELECT `+intentColumns+` FROM payment_intents i WHERE i.id = $1`, id))
	if err != nil {
		return in, err
	}
	if !scope.Allows(in.CustomerID) {
		return PaymentIntent{}, ErrNotFound
	}
	return in, nil
}

// ListPaymentIntents returns a customer's intents, newest first.
func (s *Store) ListPaymentIntents(ctx context.Context, scope Scope, customerID string) ([]PaymentIntent, error) {
	if !scope.Allows(customerID) {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+intentColumns+` FROM payment_intents i WHERE i.customer_id = $1 ORDER BY i.created_at DESC LIMIT 200`, customerID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []PaymentIntent{}
	for rows.Next() {
		in, err := scanIntent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}
