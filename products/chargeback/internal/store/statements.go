package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const statementColumns = `st.id, st.customer_id, to_char(st.period_start, 'YYYY-MM-DD'), to_char(st.period_end, 'YYYY-MM-DD'), st.currency,
	st.subtotal::text, st.tax_rate::text, st.tax::text, st.total::text, st.status, st.issued_at, st.created_at, c.name,
	COALESCE(st.discount_total, 0)::text, st.discount_detail, st.discount_rule,
	st.invoice_number, st.external_invoice_ref, st.po_reference, st.payment_terms_days, st.due_at, st.sent_at, st.paid_at, st.cancelled_at, st.cancel_reason,
	COALESCE((SELECT sum(p.amount) FROM payments p WHERE p.statement_id = st.id AND p.status = 'received'), 0)::numeric(20,6)::text`

func scanStatement(row interface{ Scan(...any) error }) (Statement, error) {
	var st Statement
	var sub, rate, tax, total, disc string
	var issued sql.NullTime
	var detail []byte
	var rule, invoiceNo, externalRef sql.NullString
	var terms sql.NullInt64
	var due, sent, paidAt, cancelled sql.NullTime
	var paid string
	if err := row.Scan(&st.ID, &st.CustomerID, &st.PeriodStart, &st.PeriodEnd, &st.Currency, &sub, &rate, &tax, &total, &st.Status, &issued, &st.CreatedAt, &st.CustomerName, &disc, &detail, &rule,
		&invoiceNo, &externalRef, &st.PORef, &terms, &due, &sent, &paidAt, &cancelled, &st.CancelReason, &paid); err != nil {
		return st, mapErr(err)
	}
	st.Subtotal, st.TaxRate, st.Tax, st.Total = Decimal(sub), Decimal(rate), Decimal(tax), Decimal(total)
	st.DiscountTotal = Decimal(disc)
	if len(detail) > 0 && string(detail) != "null" {
		st.DiscountDetail = detail
	}
	st.DiscountRule = rule.String
	st.IssuedAt = timePtr(issued)
	// Post-paid invoicing (DESIGN.md §8): the invoice fields, the payments
	// total, and the two values derived from them — balance and the
	// overdue-aware status, both computed here so they can never drift from
	// the ledger they are read out of.
	st.InvoiceNumber = invoiceNo.String
	st.ExternalInvoiceRef = externalRef.String
	if terms.Valid {
		v := int(terms.Int64)
		st.PaymentTermsDays = &v
	}
	st.DueAt, st.SentAt, st.PaidAt, st.CancelledAt = timePtr(due), timePtr(sent), timePtr(paidAt), timePtr(cancelled)
	st.Paid = Decimal(paid)
	st.Balance = st.OutstandingAt()
	st.EffectiveStatus = st.EffectiveStatusAt(time.Now().UTC())
	return st, nil
}

// StatementDraft is the computed statement a rating run writes.
type StatementDraft struct {
	CustomerID  string
	PeriodStart time.Time
	PeriodEnd   time.Time
	Currency    string
	Subtotal    Decimal
	TaxRate     Decimal
	Tax         Decimal
	Total       Decimal
	Lines       []RatedLine
	// #6862 — what the discounts took off, and their breakdown. Frozen with
	// the statement so a campaign that later ends cannot change an issued bill.
	Discount         Decimal
	AppliedDiscounts any
	// DiscountRule names the combination rule the run applied (DESIGN.md
	// §2.11); recorded on the statement so an issued bill states it.
	DiscountRule string
}

// WriteDraftStatement upserts a draft for (customer, period) and replaces its
// lines. An issued statement for the period is left untouched (ErrConflict).
func (s *Store) WriteDraftStatement(ctx context.Context, d StatementDraft) (Statement, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Statement{}, err
	}
	defer tx.Rollback()
	var existingID, status string
	err = tx.QueryRowContext(ctx, `SELECT id, status FROM statements WHERE customer_id = $1 AND period_start = $2 FOR UPDATE`, d.CustomerID, d.PeriodStart).Scan(&existingID, &status)
	switch {
	case err == sql.ErrNoRows:
		if err := tx.QueryRowContext(ctx, `INSERT INTO statements (customer_id, period_start, period_end, currency, subtotal, tax_rate, tax, total, status, discount_total, discount_detail, discount_rule)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'draft', $9::numeric, $10, $11) RETURNING id`,
			d.CustomerID, d.PeriodStart, d.PeriodEnd, d.Currency, string(d.Subtotal), string(d.TaxRate), string(d.Tax), string(d.Total), discountOrZero(d.Discount), discountDetailJSON(d.AppliedDiscounts), nullStr(&d.DiscountRule)).Scan(&existingID); err != nil {
			return Statement{}, mapErr(err)
		}
	case err != nil:
		return Statement{}, mapErr(err)
	case status != StatusDraft:
		// Anything past draft is a document the customer has: an issued
		// invoice, one that was sent, paid or voided. Re-rating the period
		// must never rewrite it.
		return Statement{}, fmt.Errorf("%w: statement for this period is already %s", ErrConflict, status)
	default:
		if _, err := tx.ExecContext(ctx, `UPDATE statements SET period_end = $2, currency = $3, subtotal = $4, tax_rate = $5, tax = $6, total = $7, discount_total = $8::numeric, discount_detail = $9, discount_rule = $10, created_at = now() WHERE id = $1`,
			existingID, d.PeriodEnd, d.Currency, string(d.Subtotal), string(d.TaxRate), string(d.Tax), string(d.Total), discountOrZero(d.Discount), discountDetailJSON(d.AppliedDiscounts), nullStr(&d.DiscountRule)); err != nil {
			return Statement{}, mapErr(err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM rated_lines WHERE statement_id = $1`, existingID); err != nil {
			return Statement{}, mapErr(err)
		}
	}
	for _, l := range d.Lines {
		if _, err := tx.ExecContext(ctx, `INSERT INTO rated_lines (statement_id, customer_id, source_id, sku, quantity, unit, unit_price, amount, resource_count)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			existingID, d.CustomerID, nullStr(l.SourceID), l.SKU, string(l.Quantity), l.Unit, string(l.UnitPrice), string(l.Amount), l.ResourceCount); err != nil {
			return Statement{}, mapErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Statement{}, err
	}
	return s.GetStatement(ctx, OperatorScope, existingID)
}

// ListStatements returns a customer's statements, newest period first.
func (s *Store) ListStatements(ctx context.Context, scope Scope, customerID string) ([]Statement, error) {
	if !scope.Allows(customerID) {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+statementColumns+` FROM statements st JOIN customers c ON c.id = st.customer_id WHERE st.customer_id = $1 ORDER BY st.period_start DESC`, customerID)
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

// ListAllStatements returns statements across customers (operator), newest first.
func (s *Store) ListAllStatements(ctx context.Context, period string) ([]Statement, error) {
	q := `SELECT ` + statementColumns + ` FROM statements st JOIN customers c ON c.id = st.customer_id`
	var args []any
	if period != "" {
		q += ` WHERE to_char(st.period_start, 'YYYY-MM') = $1`
		args = append(args, period)
	}
	q += ` ORDER BY st.period_start DESC, c.name`
	rows, err := s.db.QueryContext(ctx, q, args...)
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

// GetStatement returns a statement with lines, inside the scope.
func (s *Store) GetStatement(ctx context.Context, scope Scope, id string) (Statement, error) {
	st, err := scanStatement(s.db.QueryRowContext(ctx, `SELECT `+statementColumns+` FROM statements st JOIN customers c ON c.id = st.customer_id WHERE st.id = $1`, id))
	if err != nil {
		return st, err
	}
	if !scope.Allows(st.CustomerID) {
		return Statement{}, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, statement_id, customer_id, source_id, sku, quantity::text, unit, unit_price::text, amount::text, resource_count FROM rated_lines WHERE statement_id = $1 ORDER BY sku, source_id`, id)
	if err != nil {
		return st, mapErr(err)
	}
	defer rows.Close()
	st.Lines = []RatedLine{}
	for rows.Next() {
		var l RatedLine
		var src sql.NullString
		var q, up, amt string
		if err := rows.Scan(&l.ID, &l.StatementID, &l.CustomerID, &src, &l.SKU, &q, &l.Unit, &up, &amt, &l.ResourceCount); err != nil {
			return st, err
		}
		l.SourceID = strPtr(src)
		l.Quantity, l.UnitPrice, l.Amount = Decimal(q), Decimal(up), Decimal(amt)
		st.Lines = append(st.Lines, l)
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	// The payment history behind Paid/Balance (DESIGN.md §8). Only the
	// single-statement document carries it; the list documents carry the
	// totals alone.
	pays, err := s.statementPayments(ctx, id)
	if err != nil {
		return st, err
	}
	st.Payments = pays
	return st, nil
}

// IssueStatement flips a draft to issued (idempotent on already-issued).
func (s *Store) IssueStatement(ctx context.Context, id string) (Statement, error) {
	st, _, err := s.IssueStatementOnce(ctx, id)
	return st, err
}

// IssueStatementOnce flips a draft to issued and reports whether THIS call
// made the transition. A second call on an issued statement returns it
// unchanged with transitioned = false, which is what lets the API mail the
// customer exactly once: the notification rides on the draft → issued edge,
// not on the request.
//
// Issuing is where a statement becomes an INVOICE (DESIGN.md §8). In the
// SAME transaction that flips the status it takes the next invoice number of
// the calendar year, copies the purchase-order reference and payment terms
// the invoice must quote, and computes the due date from them. Number and
// status therefore commit together: a concurrent issue cannot duplicate a
// number, and a transaction that rolls back leaves no hole in the sequence.
func (s *Store) IssueStatementOnce(ctx context.Context, id string) (st Statement, transitioned bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Statement{}, false, err
	}
	defer tx.Rollback()
	var status, poRef string
	var terms sql.NullInt64
	var custPO string
	var custTerms int
	err = tx.QueryRowContext(ctx, `SELECT st.status, st.po_reference, st.payment_terms_days, c.po_reference, c.payment_terms_days
		FROM statements st JOIN customers c ON c.id = st.customer_id WHERE st.id = $1 FOR UPDATE OF st`, id).
		Scan(&status, &poRef, &terms, &custPO, &custTerms)
	if err != nil {
		return Statement{}, false, mapErr(err)
	}
	switch {
	case status == StatusIssued:
		// Already issued: idempotent, and the number it was given stands.
	case status != StatusDraft:
		return Statement{}, false, refuseTransition(status, StatusIssued)
	default:
		// The statement's own override wins; otherwise the customer's
		// standing purchase order and terms are copied onto the invoice, so
		// what the customer receives is fixed at issue and cannot change
		// later when the customer record does.
		if poRef == "" {
			poRef = custPO
		}
		// The customer's column is NOT NULL and defaults to net-30, so it is
		// authoritative as it stands: a stored 0 means due on receipt, not
		// "unset". A per-statement override, when the operator set one,
		// wins over it.
		days := custTerms
		if days < 0 {
			days = DefaultPaymentTermsDays
		}
		if terms.Valid {
			days = int(terms.Int64)
		}
		settings, err := billingSettingsTx(ctx, tx)
		if err != nil {
			return Statement{}, false, err
		}
		number, err := nextInvoiceNumber(ctx, tx, settings.InvoicePrefix)
		if err != nil {
			return Statement{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE statements SET status = 'issued', issued_at = COALESCE(issued_at, now()),
			invoice_number = $2, po_reference = $3, payment_terms_days = $4,
			due_at = COALESCE(issued_at, now()) + make_interval(days => $4)
			WHERE id = $1 AND status = 'draft'`, id, number, poRef, days); err != nil {
			return Statement{}, false, mapErr(err)
		}
		transitioned = true
	}
	if err := tx.Commit(); err != nil {
		return Statement{}, false, err
	}
	st, err = s.GetStatement(ctx, OperatorScope, id)
	if err != nil {
		return Statement{}, false, err
	}
	return st, transitioned, nil
}

// LastPeriodTotal sums the totals of the most recent statement period.
func (s *Store) LastPeriodTotal(ctx context.Context) (period string, total Decimal, count int, err error) {
	var p sql.NullString
	var t sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT to_char(period_start, 'YYYY-MM'), sum(total)::text, count(*) FROM statements
		WHERE period_start = (SELECT max(period_start) FROM statements) GROUP BY period_start`).Scan(&p, &t, &count)
	if err == sql.ErrNoRows {
		return "", "0", 0, nil
	}
	if err != nil {
		return "", "", 0, mapErr(err)
	}
	return p.String, Decimal(t.String), count, nil
}

// discountOrZero renders a discount for SQL; an empty Decimal is 0, not NULL,
// so `subtotal + tax = total` arithmetic downstream never meets a NULL.
func discountOrZero(d Decimal) string {
	if strings.TrimSpace(string(d)) == "" {
		return "0"
	}
	return string(d)
}

// discountDetailJSON stores the per-discount breakdown, or SQL NULL when none
// applied — an empty array and "no discounts" should not read the same.
func discountDetailJSON(v any) any {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	if string(b) == "null" || string(b) == "[]" {
		return nil
	}
	return b
}

// DeleteDraftStatement removes a draft and its rated lines (cascade). An
// issued statement is refused with ErrConflict: it is the bill the customer
// received, and the next run for the period must still see it as issued.
func (s *Store) DeleteDraftStatement(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM statements WHERE id = $1 FOR UPDATE`, id).Scan(&status); err != nil {
		return mapErr(err)
	}
	if status != StatusDraft {
		return fmt.Errorf("%w: statement is %s; only drafts can be deleted", ErrConflict, status)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM statements WHERE id = $1`, id); err != nil {
		return mapErr(err)
	}
	return tx.Commit()
}
