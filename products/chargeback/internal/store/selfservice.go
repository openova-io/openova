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

// Customer self-service (DESIGN.md §16): the two things a paying customer
// keeps of its own — a saved payment method and a dispute on an invoice.
// The third, downloading the invoice, needs no storage at all: it is the
// existing statement read rendered as a document.
//
// Nothing here is parallel to what already exists. A dispute's credit note
// is THE credit note (CreateCreditNote); a payment method is a display
// record beside the gateway that holds the instrument, never an instrument
// of our own.

// selfServiceMigrationSQL adds the two tables and the two columns §16 needs.
//
// The `last4` CHECK is load-bearing rather than decorative: it is the
// database refusing to hold anything longer than four digits in the field a
// card number could be mistakenly written to. A gateway implementation that
// returned a whole PAN there would fail its INSERT, which is the difference
// between a rule and a hope.
//
// Appended at the END of migrations: they are positional. MigrationSelfService
// locates it by content so a migration appended after it cannot move it.
const selfServiceMigrationSQL = `
CREATE TABLE IF NOT EXISTS customer_payment_methods (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
	gateway TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','active','removed')),
	token TEXT NOT NULL DEFAULT '',
	setup_id TEXT NOT NULL DEFAULT '',
	setup_url TEXT NOT NULL DEFAULT '',
	brand TEXT NOT NULL DEFAULT '' CHECK (length(brand) <= 40),
	last4 TEXT NOT NULL DEFAULT '' CHECK (last4 ~ '^[0-9]{0,4}$'),
	exp_month INT CHECK (exp_month IS NULL OR (exp_month BETWEEN 1 AND 12)),
	exp_year INT CHECK (exp_year IS NULL OR (exp_year BETWEEN 2000 AND 2200)),
	label TEXT NOT NULL DEFAULT '' CHECK (length(label) <= 120),
	created_by TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	confirmed_at TIMESTAMPTZ,
	removed_at TIMESTAMPTZ,
	removed_by TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS customer_payment_methods_customer_idx ON customer_payment_methods (customer_id) WHERE removed_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS customer_payment_methods_token_idx ON customer_payment_methods (customer_id, gateway, token) WHERE token <> '' AND removed_at IS NULL;

CREATE TABLE IF NOT EXISTS statement_disputes (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	statement_id UUID NOT NULL REFERENCES statements(id) ON DELETE CASCADE,
	customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
	reason TEXT NOT NULL CHECK (reason <> ''),
	lines JSONB NOT NULL DEFAULT '[]'::jsonb,
	amount NUMERIC(20,6) NOT NULL DEFAULT 0 CHECK (amount >= 0),
	currency TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','upheld','rejected')),
	opened_by TEXT NOT NULL DEFAULT '',
	opened_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	resolved_by TEXT NOT NULL DEFAULT '',
	resolved_at TIMESTAMPTZ,
	note TEXT NOT NULL DEFAULT '',
	credit_note_id UUID REFERENCES credit_notes(id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS statement_disputes_customer_idx ON statement_disputes (customer_id, opened_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS statement_disputes_open_idx ON statement_disputes (statement_id) WHERE status = 'open';

ALTER TABLE statements ADD COLUMN IF NOT EXISTS disputed_at TIMESTAMPTZ;
ALTER TABLE statements ADD COLUMN IF NOT EXISTS dispute_reason TEXT NOT NULL DEFAULT '';
`

// MigrationSelfService is the schema_migrations version of the §16
// migration, located by content like the others.
var MigrationSelfService = func() int {
	for i, m := range migrations {
		if m == selfServiceMigrationSQL {
			return i + 1
		}
	}
	return len(migrations)
}()

// ---------------------------------------------------------------------------
// saved payment methods
// ---------------------------------------------------------------------------

// Payment-method statuses.
const (
	// MethodPending — the gateway has been asked and the customer has not
	// finished entering the instrument yet.
	MethodPending = "pending"
	// MethodActive — the gateway confirmed it holds the instrument.
	MethodActive = "active"
	// MethodRemoved — the customer took it off file.
	MethodRemoved = "removed"
)

// PaymentMethod is what this product keeps about an instrument the GATEWAY
// holds (DESIGN.md §16): the brand, the last four digits, the expiry and the
// gateway's own token id — enough for a payer to recognise which card they
// saved, and nothing that could charge it.
//
// Token is `json:"-"`: it is written to the database and read back by the
// gateway seam, and it leaves this process on no wire at all. That is how
// "an operator may see that a method exists but not its token" is enforced —
// by the type, not by remembering to redact in each of several handlers.
type PaymentMethod struct {
	ID         string `json:"id"`
	CustomerID string `json:"customer_id"`
	Gateway    string `json:"gateway,omitempty"`
	Status     string `json:"status"`
	Token      string `json:"-"`
	// SetupID is the gateway's id for the setup that created it, and
	// SetupURL the page the customer completes on while it is pending.
	SetupID  string `json:"-"`
	SetupURL string `json:"setup_url,omitempty"`
	Brand    string `json:"brand,omitempty"`
	Last4    string `json:"last4,omitempty"`
	ExpMonth *int   `json:"exp_month,omitempty"`
	ExpYear  *int   `json:"exp_year,omitempty"`
	Label    string `json:"label,omitempty"`
	// Saved reports whether the gateway holds a token for it; true without
	// exposing the token itself.
	Saved       bool       `json:"saved"`
	CreatedBy   string     `json:"created_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
	RemovedAt   *time.Time `json:"removed_at,omitempty"`
	RemovedBy   string     `json:"removed_by,omitempty"`
}

// PaymentMethodInput starts one. Only display fields and the gateway's own
// handles are accepted; there is no field for an instrument.
type PaymentMethodInput struct {
	CustomerID string
	Gateway    string
	SetupID    string
	SetupURL   string
	Token      string
	Brand      string
	Last4      string
	ExpMonth   int
	ExpYear    int
	Label      string
	Actor      string
}

// PaymentMethodConfirm is what a completed setup established.
type PaymentMethodConfirm struct {
	Token    string
	Brand    string
	Last4    string
	ExpMonth int
	ExpYear  int
	Label    string
	Actor    string
}

const paymentMethodColumns = `id, customer_id, gateway, status, token, setup_id, setup_url, brand, last4, exp_month, exp_year, label, created_by, created_at, confirmed_at, removed_at, removed_by`

func scanPaymentMethod(row interface{ Scan(...any) error }) (PaymentMethod, error) {
	var m PaymentMethod
	var month, year sql.NullInt64
	var confirmed, removed sql.NullTime
	if err := row.Scan(&m.ID, &m.CustomerID, &m.Gateway, &m.Status, &m.Token, &m.SetupID, &m.SetupURL, &m.Brand, &m.Last4, &month, &year, &m.Label,
		&m.CreatedBy, &m.CreatedAt, &confirmed, &removed, &m.RemovedBy); err != nil {
		return m, mapErr(err)
	}
	if month.Valid {
		v := int(month.Int64)
		m.ExpMonth = &v
	}
	if year.Valid {
		v := int(year.Int64)
		m.ExpYear = &v
	}
	m.ConfirmedAt, m.RemovedAt = timePtr(confirmed), timePtr(removed)
	m.CreatedAt = m.CreatedAt.UTC()
	m.Saved = strings.TrimSpace(m.Token) != ""
	return m, nil
}

// digitsAtMost keeps at most n leading digits of s and refuses anything else
// in it. A gateway that answered with a whole card number for `last4` is
// refused HERE as well as by the column's CHECK — two independent refusals,
// because this is the one field a mistake could put a card number in.
func digitsAtMost(s string, n int) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("%w: the last four digits of a payment method must be digits", ErrInvalid)
		}
	}
	if len(s) > n {
		return "", fmt.Errorf("%w: a payment method records at most %d digits, never the number itself", ErrInvalid, n)
	}
	return s, nil
}

func clipField(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

// StartPaymentMethod records a method the gateway is saving. It is `pending`
// until the gateway confirms, or `active` straight away when the gateway
// handed back a token during the call.
func (s *Store) StartPaymentMethod(ctx context.Context, in PaymentMethodInput) (PaymentMethod, error) {
	last4, err := digitsAtMost(in.Last4, 4)
	if err != nil {
		return PaymentMethod{}, err
	}
	status := MethodPending
	var confirmed any
	if strings.TrimSpace(in.Token) != "" {
		status = MethodActive
		confirmed = time.Now().UTC()
	}
	var id string
	if err := s.db.QueryRowContext(ctx, `INSERT INTO customer_payment_methods
		(customer_id, gateway, status, token, setup_id, setup_url, brand, last4, exp_month, exp_year, label, created_by, confirmed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) RETURNING id`,
		in.CustomerID, strings.ToLower(clipField(in.Gateway, 60)), status, strings.TrimSpace(in.Token), clipField(in.SetupID, 200), clipField(in.SetupURL, 2000),
		clipField(in.Brand, 40), last4, nullIntOrNil(in.ExpMonth), nullIntOrNil(in.ExpYear), clipField(in.Label, 120), in.Actor, confirmed).Scan(&id); err != nil {
		return PaymentMethod{}, mapErr(err)
	}
	return s.GetPaymentMethod(ctx, OperatorScope, id)
}

// ConfirmPaymentMethod records what a completed setup established. Only a
// pending method of that customer can be confirmed.
func (s *Store) ConfirmPaymentMethod(ctx context.Context, scope Scope, id string, in PaymentMethodConfirm) (PaymentMethod, error) {
	existing, err := s.GetPaymentMethod(ctx, scope, id)
	if err != nil {
		return PaymentMethod{}, err
	}
	if existing.Status == MethodRemoved {
		return PaymentMethod{}, fmt.Errorf("%w: this payment method was removed", ErrConflict)
	}
	last4, err := digitsAtMost(in.Last4, 4)
	if err != nil {
		return PaymentMethod{}, err
	}
	label := clipField(in.Label, 120)
	if label == "" {
		label = existing.Label
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE customer_payment_methods
		SET status = 'active', token = $2, brand = $3, last4 = $4, exp_month = $5, exp_year = $6, label = $7, confirmed_at = now()
		WHERE id = $1 AND removed_at IS NULL`,
		id, strings.TrimSpace(in.Token), clipField(in.Brand, 40), last4, nullIntOrNil(in.ExpMonth), nullIntOrNil(in.ExpYear), label); err != nil {
		return PaymentMethod{}, mapErr(err)
	}
	return s.GetPaymentMethod(ctx, scope, id)
}

// ListPaymentMethods returns a customer's methods, the removed ones last.
func (s *Store) ListPaymentMethods(ctx context.Context, scope Scope, customerID string) ([]PaymentMethod, error) {
	if !scope.Allows(customerID) {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+paymentMethodColumns+` FROM customer_payment_methods
		WHERE customer_id = $1 AND removed_at IS NULL ORDER BY created_at DESC`, customerID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []PaymentMethod{}
	for rows.Next() {
		m, err := scanPaymentMethod(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetPaymentMethod reads one inside the scope. A method of another customer
// is ErrNotFound, never a filtered answer.
func (s *Store) GetPaymentMethod(ctx context.Context, scope Scope, id string) (PaymentMethod, error) {
	m, err := scanPaymentMethod(s.db.QueryRowContext(ctx, `SELECT `+paymentMethodColumns+` FROM customer_payment_methods WHERE id = $1`, id))
	if err != nil {
		return PaymentMethod{}, err
	}
	if !scope.Allows(m.CustomerID) {
		return PaymentMethod{}, ErrNotFound
	}
	return m, nil
}

// RemovePaymentMethod takes a method off file. The row stays — who removed
// what and when is part of the account's history — but the token is ERASED
// in the same statement, so a removed method cannot be charged even by a bug.
func (s *Store) RemovePaymentMethod(ctx context.Context, scope Scope, id, actor string) (PaymentMethod, error) {
	m, err := s.GetPaymentMethod(ctx, scope, id)
	if err != nil {
		return PaymentMethod{}, err
	}
	if m.Status == MethodRemoved {
		return m, nil
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE customer_payment_methods
		SET status = 'removed', removed_at = now(), removed_by = $2, token = '' WHERE id = $1`, id, actor); err != nil {
		return PaymentMethod{}, mapErr(err)
	}
	return s.GetPaymentMethod(ctx, OperatorScope, id)
}

func nullIntOrNil(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

// ---------------------------------------------------------------------------
// invoice disputes
// ---------------------------------------------------------------------------

// Dispute statuses.
const (
	DisputeOpen     = "open"
	DisputeUpheld   = "upheld"
	DisputeRejected = "rejected"
)

// Dispute is one customer's objection to one invoice (DESIGN.md §16).
type Dispute struct {
	ID            string          `json:"id"`
	StatementID   string          `json:"statement_id"`
	CustomerID    string          `json:"customer_id"`
	InvoiceNumber string          `json:"invoice_number,omitempty"`
	Reason        string          `json:"reason"`
	Lines         json.RawMessage `json:"lines,omitempty"`
	Amount        Decimal         `json:"amount"`
	Currency      string          `json:"currency,omitempty"`
	Status        string          `json:"status"`
	OpenedBy      string          `json:"opened_by,omitempty"`
	OpenedAt      time.Time       `json:"opened_at"`
	ResolvedBy    string          `json:"resolved_by,omitempty"`
	ResolvedAt    *time.Time      `json:"resolved_at,omitempty"`
	Note          string          `json:"note,omitempty"`
	CreditNoteID  string          `json:"credit_note_id,omitempty"`
}

// DisputeInput opens one: why, and optionally which rated lines it is about.
type DisputeInput struct {
	Reason string
	Lines  []string
	Actor  string
}

const disputeColumns = `d.id, d.statement_id, d.customer_id, COALESCE(st.invoice_number, st.external_invoice_ref, ''), d.reason, d.lines, d.amount::text, d.currency, d.status,
	d.opened_by, d.opened_at, d.resolved_by, d.resolved_at, d.note, d.credit_note_id`

const disputeFrom = ` FROM statement_disputes d JOIN statements st ON st.id = d.statement_id`

func scanDispute(row interface{ Scan(...any) error }) (Dispute, error) {
	var d Dispute
	var lines []byte
	var amount string
	var resolvedAt sql.NullTime
	var creditNote sql.NullString
	if err := row.Scan(&d.ID, &d.StatementID, &d.CustomerID, &d.InvoiceNumber, &d.Reason, &lines, &amount, &d.Currency, &d.Status,
		&d.OpenedBy, &d.OpenedAt, &d.ResolvedBy, &resolvedAt, &d.Note, &creditNote); err != nil {
		return d, mapErr(err)
	}
	if len(lines) > 0 && string(lines) != "null" && string(lines) != "[]" {
		d.Lines = lines
	}
	d.Amount = Decimal(amount)
	d.OpenedAt = d.OpenedAt.UTC()
	d.ResolvedAt = timePtr(resolvedAt)
	d.CreditNoteID = creditNote.String
	return d, nil
}

// OpenDispute records a customer's objection to an invoice and flags the
// statement (DESIGN.md §16).
//
// WHAT IS DISPUTED. With no lines named, it is the invoice's OUTSTANDING
// balance — what is actually being chased. With lines named, it is the share
// of the invoice TOTAL those lines represent, computed as an exact rational
// over the rated amounts and rendered at the schema's six decimals, so the
// figure a credit note later carries is the figure agreed here. Either way it
// is capped at what the invoice can still be credited (total less credit
// notes already issued), because an upheld dispute must always be creditable.
//
// A draft has no invoice to dispute and a cancelled one collects nothing, so
// both are refused: the flag exists to stop collections, and neither is ever
// chased.
func (s *Store) OpenDispute(ctx context.Context, statementID string, in DisputeInput) (Dispute, error) {
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return Dispute{}, fmt.Errorf("%w: a dispute carries the reason it was raised", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Dispute{}, err
	}
	defer tx.Rollback()

	customerID, status, total, outstanding, err := invoiceOutstandingTx(ctx, tx, statementID)
	if err != nil {
		return Dispute{}, err
	}
	switch status {
	case StatusDraft:
		return Dispute{}, fmt.Errorf("%w: a draft is not an invoice; there is nothing to dispute until it is issued", ErrConflict)
	case StatusCancelled:
		return Dispute{}, fmt.Errorf("%w: a cancelled invoice is collected on by nobody", ErrConflict)
	}
	var currency string
	var disputedAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT currency, disputed_at FROM statements WHERE id = $1`, statementID).Scan(&currency, &disputedAt); err != nil {
		return Dispute{}, mapErr(err)
	}
	if disputedAt.Valid {
		return Dispute{}, fmt.Errorf("%w: this invoice is already disputed; the open dispute must be resolved first", ErrConflict)
	}

	amount, lines, err := disputedAmountTx(ctx, tx, statementID, in.Lines, total, outstanding)
	if err != nil {
		return Dispute{}, err
	}
	// Never more than the invoice can still be credited.
	var creditedS string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(sum(total), 0)::numeric(20,6)::text FROM credit_notes WHERE statement_id = $1`, statementID).Scan(&creditedS); err != nil {
		return Dispute{}, mapErr(err)
	}
	room := new(big.Rat).Sub(total, ratOf(Decimal(creditedS)))
	if room.Sign() < 0 {
		room = new(big.Rat)
	}
	if amount.Cmp(room) > 0 {
		amount = room
	}

	linesJSON, _ := json.Marshal(lines)
	var id string
	if err := tx.QueryRowContext(ctx, `INSERT INTO statement_disputes (statement_id, customer_id, reason, lines, amount, currency, opened_by)
		VALUES ($1, $2, $3, $4, $5::numeric, $6, $7) RETURNING id`,
		statementID, customerID, reason, linesJSON, string(decOf(amount)), currency, in.Actor).Scan(&id); err != nil {
		return Dispute{}, mapErr(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE statements SET disputed_at = now(), dispute_reason = $2 WHERE id = $1`, statementID, reason); err != nil {
		return Dispute{}, mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return Dispute{}, err
	}
	return s.GetDispute(ctx, OperatorScope, id)
}

// disputedAmountTx computes what is disputed, and returns the line ids the
// dispute names (empty when it is the whole invoice).
func disputedAmountTx(ctx context.Context, tx *sql.Tx, statementID string, want []string, total, outstanding *big.Rat) (*big.Rat, []string, error) {
	named := map[string]bool{}
	for _, id := range want {
		if id = strings.TrimSpace(id); id != "" {
			named[id] = true
		}
	}
	if len(named) == 0 {
		amount := new(big.Rat).Set(outstanding)
		if amount.Sign() < 0 {
			amount = new(big.Rat)
		}
		return amount, []string{}, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id::text, amount::text FROM rated_lines WHERE statement_id = $1`, statementID)
	if err != nil {
		return nil, nil, mapErr(err)
	}
	defer rows.Close()
	all, part := new(big.Rat), new(big.Rat)
	got := []string{}
	for rows.Next() {
		var id, amount string
		if err := rows.Scan(&id, &amount); err != nil {
			return nil, nil, err
		}
		v := ratOf(Decimal(amount))
		all.Add(all, v)
		if named[id] {
			part.Add(part, v)
			got = append(got, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(got) != len(named) {
		return nil, nil, fmt.Errorf("%w: a disputed line must be a line of this invoice", ErrInvalid)
	}
	if all.Sign() == 0 {
		return new(big.Rat), got, nil
	}
	// The lines' share of the invoice total: their rated amounts are before
	// the statement's discounts and tax, so scaling by total/list is what
	// makes the disputed figure the one the invoice actually carries.
	amount := new(big.Rat).Quo(part, all)
	amount.Mul(amount, total)
	return amount, got, nil
}

// ResolveDispute closes an open dispute (DESIGN.md §16).
//
// UPHELD issues a credit note for the disputed amount through
// createCreditNoteTx — the SAME function POST /statements/{id}/credit-notes
// calls, with the same numbering, the same allocation against the invoice
// and the same ledger entry. It runs inside THIS transaction, so a dispute
// can never end up credited but still open, nor resolved without the credit
// the customer was promised; a retry after a failure finds the dispute still
// open and no note issued.
//
// REJECTED credits nothing. Either way the flag the aging report and the
// collections evaluator read is cleared in the same transaction, so
// collections resume the moment the outcome is recorded.
func (s *Store) ResolveDispute(ctx context.Context, id, outcome, note, actor string) (Dispute, error) {
	if !oneOf(outcome, []string{DisputeUpheld, DisputeRejected}) {
		return Dispute{}, fmt.Errorf("%w: outcome must be upheld or rejected", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Dispute{}, err
	}
	defer tx.Rollback()
	var statementID, status, reason, amount string
	if err := tx.QueryRowContext(ctx, `SELECT statement_id, status, reason, amount::text FROM statement_disputes WHERE id = $1 FOR UPDATE`, id).
		Scan(&statementID, &status, &reason, &amount); err != nil {
		return Dispute{}, mapErr(err)
	}
	if status != DisputeOpen {
		return Dispute{}, fmt.Errorf("%w: this dispute was already %s", ErrConflict, status)
	}
	creditNoteID := ""
	if outcome == DisputeUpheld {
		creditReason := "dispute upheld: " + reason
		if n := strings.TrimSpace(note); n != "" {
			creditReason += " — " + n
		}
		creditNoteID, err = createCreditNoteTx(ctx, tx, statementID, CreditNoteInput{
			Reason: creditReason, Amount: Decimal(amount), Actor: actor,
		}, CreditNotePartial)
		if err != nil {
			return Dispute{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE statement_disputes SET status = $2, note = $3, credit_note_id = $4, resolved_by = $5, resolved_at = now() WHERE id = $1`,
		id, outcome, strings.TrimSpace(note), nullStr(&creditNoteID), actor); err != nil {
		return Dispute{}, mapErr(err)
	}
	// Collections resume: the flag the aging report and the evaluator read
	// is cleared whichever way it went. Upheld, the credit note above has
	// reduced what is owed; rejected, the invoice is chased again from here.
	if _, err := tx.ExecContext(ctx, `UPDATE statements SET disputed_at = NULL, dispute_reason = '' WHERE id = $1`, statementID); err != nil {
		return Dispute{}, mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return Dispute{}, err
	}
	return s.GetDispute(ctx, OperatorScope, id)
}

// GetDispute reads one inside the scope.
func (s *Store) GetDispute(ctx context.Context, scope Scope, id string) (Dispute, error) {
	d, err := scanDispute(s.db.QueryRowContext(ctx, `SELECT `+disputeColumns+disputeFrom+` WHERE d.id = $1`, id))
	if err != nil {
		return Dispute{}, err
	}
	if !scope.Allows(d.CustomerID) {
		return Dispute{}, ErrNotFound
	}
	return d, nil
}

// ListStatementDisputes returns an invoice's disputes, newest first.
func (s *Store) ListStatementDisputes(ctx context.Context, scope Scope, statementID string) ([]Dispute, error) {
	return s.disputes(ctx, scope, `d.statement_id = $1`, statementID)
}

// ListCustomerDisputes returns a customer's disputes, newest first.
func (s *Store) ListCustomerDisputes(ctx context.Context, scope Scope, customerID string) ([]Dispute, error) {
	if !scope.Allows(customerID) {
		return nil, ErrNotFound
	}
	return s.disputes(ctx, scope, `d.customer_id = $1`, customerID)
}

func (s *Store) disputes(ctx context.Context, scope Scope, where string, arg any) ([]Dispute, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+disputeColumns+disputeFrom+` WHERE `+where+` ORDER BY d.opened_at DESC`, arg)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []Dispute{}
	for rows.Next() {
		d, err := scanDispute(rows)
		if err != nil {
			return nil, err
		}
		if !scope.Allows(d.CustomerID) {
			continue
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
