package store

import (
	"context"
	"encoding/json"
	"strings"
)

// CompareDecimal compares two decimals exactly: -1, 0 or 1.
func CompareDecimal(a, b Decimal) int { return ratOf(a).Cmp(ratOf(b)) }

// CustomerCurrency is the currency a customer's account is kept in: the
// currency of its newest statement, else the reporting currency.
func (s *Store) CustomerCurrency(ctx context.Context, customerID string) (string, error) {
	var cur string
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT currency FROM statements WHERE customer_id = $1 ORDER BY period_start DESC LIMIT 1),
		(SELECT currency FROM allocation_settings WHERE id = 1), 'OMR')`, customerID).Scan(&cur)
	return cur, mapErr(err)
}

// FindPaymentByReference resolves a payment by the reference that proves it.
func (s *Store) FindPaymentByReference(ctx context.Context, customerID, reference string) (Payment, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return Payment{}, ErrNotFound
	}
	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM payments WHERE customer_id = $1 AND reference = $2`, customerID, reference).Scan(&id); err != nil {
		return Payment{}, mapErr(err)
	}
	return s.GetPayment(ctx, OperatorScope, id)
}

// GetStatementByInvoiceNumber finds the statement an import names by OUR
// number (summary-charge mode).
func (s *Store) GetStatementByInvoiceNumber(ctx context.Context, number string) (Statement, error) {
	var id string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM statements WHERE invoice_number = $1`, strings.TrimSpace(number)).Scan(&id); err != nil {
		return Statement{}, mapErr(err)
	}
	return s.GetStatement(ctx, OperatorScope, id)
}

// QueueOutbox queues documents for the operator's billing system outside an
// issuing transaction — a checkout payment and the account it landed on
// (DESIGN.md §9.2). Same outbox, same delivery loop, same idempotency rule:
// (doc_type, idempotency_key) is unique, so a repeat queues nothing.
func (s *Store) QueueOutbox(ctx context.Context, customerID, statementID string, docs ...OutboxDocument) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var stmt, customer any
	if statementID != "" {
		stmt = statementID
	}
	// A document about no single customer — the period journal of DESIGN.md
	// §18 — queues with a NULL customer rather than an empty uuid.
	if customerID != "" {
		customer = customerID
	}
	for _, e := range docs {
		if len(e.Document) == 0 || !json.Valid(e.Document) || e.IdempotencyKey == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO commercial_outbox (doc_type, idempotency_key, statement_id, customer_id, document)
			VALUES ($1, $2, $3, $4, $5::jsonb) ON CONFLICT (doc_type, idempotency_key) DO NOTHING`, e.DocType, e.IdempotencyKey, stmt, customer, string(e.Document)); err != nil {
			return mapErr(err)
		}
	}
	return tx.Commit()
}
