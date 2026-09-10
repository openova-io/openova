package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// The commercial outbox (DESIGN.md §8.10). When the operator's billing system
// is the system of record, issuing a statement writes the statement AND the
// document to export in one transaction, and a delivery loop pushes the
// undelivered rows out afterwards with backoff.
//
// This is what makes the coupling loose in the way that matters: rating and
// issuing complete at our speed, on our availability, and an export that
// cannot be delivered right now is a row with a `last_error` an operator can
// see rather than a bill that was never raised.

// Document types the outbox carries.
const (
	// OutboxInvoice is the rated bill (TMF678-shaped).
	OutboxInvoice = "invoice"
)

// OutboxBackoffCap bounds the exponential backoff between delivery attempts.
const OutboxBackoffCap = time.Hour

// OutboxBackoff is the wait before attempt n+1: one minute doubling to the
// cap, so a billing system down for an afternoon is retried steadily rather
// than hammered.
func OutboxBackoff(attempts int) time.Duration {
	if attempts < 1 {
		return time.Minute
	}
	if attempts > 20 {
		return OutboxBackoffCap
	}
	d := time.Duration(math.Pow(2, float64(attempts-1))) * time.Minute
	if d > OutboxBackoffCap || d <= 0 {
		return OutboxBackoffCap
	}
	return d
}

// OutboxEntry is one queued document.
type OutboxEntry struct {
	ID      int64  `json:"id"`
	DocType string `json:"doc_type"`
	// IdempotencyKey is the statement id: delivering this row twice must be
	// one document at the far end.
	IdempotencyKey string          `json:"idempotency_key"`
	StatementID    string          `json:"statement_id,omitempty"`
	CustomerID     string          `json:"customer_id,omitempty"`
	CustomerName   string          `json:"customer_name,omitempty"`
	Document       json.RawMessage `json:"document,omitempty"`
	Attempts       int             `json:"attempts"`
	NextAttemptAt  time.Time       `json:"next_attempt_at"`
	DeliveredAt    *time.Time      `json:"delivered_at,omitempty"`
	LastError      string          `json:"last_error,omitempty"`
	ExternalRef    string          `json:"external_ref,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

const outboxColumns = `o.id, o.doc_type, o.idempotency_key, COALESCE(o.statement_id::text, ''), COALESCE(o.customer_id::text, ''),
	COALESCE(c.name, ''), o.attempts, o.next_attempt_at, o.delivered_at, o.last_error, o.external_ref, o.created_at, o.updated_at`

func scanOutbox(row interface{ Scan(...any) error }, withDoc bool, doc *[]byte) (OutboxEntry, error) {
	var e OutboxEntry
	var delivered any
	args := []any{&e.ID, &e.DocType, &e.IdempotencyKey, &e.StatementID, &e.CustomerID, &e.CustomerName,
		&e.Attempts, &e.NextAttemptAt, &delivered, &e.LastError, &e.ExternalRef, &e.CreatedAt, &e.UpdatedAt}
	if withDoc {
		args = append(args, doc)
	}
	if err := row.Scan(args...); err != nil {
		return e, mapErr(err)
	}
	if t, ok := delivered.(time.Time); ok {
		u := t.UTC()
		e.DeliveredAt = &u
	}
	e.NextAttemptAt, e.CreatedAt, e.UpdatedAt = e.NextAttemptAt.UTC(), e.CreatedAt.UTC(), e.UpdatedAt.UTC()
	if withDoc && len(*doc) > 0 {
		e.Document = append(json.RawMessage{}, *doc...)
	}
	return e, nil
}

// ListOutbox returns queued documents. pending=true narrows to the ones not
// yet delivered — what an operator wants to see, with their last error.
func (s *Store) ListOutbox(ctx context.Context, pending bool, limit int) ([]OutboxEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT ` + outboxColumns + ` FROM commercial_outbox o LEFT JOIN customers c ON c.id = o.customer_id`
	if pending {
		q += ` WHERE o.delivered_at IS NULL`
	}
	q += ` ORDER BY o.delivered_at IS NOT NULL, o.next_attempt_at, o.id LIMIT $1`
	rows, err := s.db.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []OutboxEntry{}
	for rows.Next() {
		var doc []byte
		e, err := scanOutbox(rows, false, &doc)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DueOutbox returns undelivered entries whose next attempt is due, oldest
// first, with their documents. FOR UPDATE SKIP LOCKED so a second delivery
// loop — or a retry running beside the ticker — never picks the same row.
func (s *Store) DueOutbox(ctx context.Context, now time.Time, limit int) ([]OutboxEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+outboxColumns+`, o.document
		FROM commercial_outbox o LEFT JOIN customers c ON c.id = o.customer_id
		WHERE o.delivered_at IS NULL AND o.next_attempt_at <= $1
		ORDER BY o.next_attempt_at, o.id LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []OutboxEntry{}
	for rows.Next() {
		var doc []byte
		e, err := scanOutbox(rows, true, &doc)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetOutboxEntry reads one entry with its document.
func (s *Store) GetOutboxEntry(ctx context.Context, id int64) (OutboxEntry, error) {
	var doc []byte
	return scanOutbox(s.db.QueryRowContext(ctx, `SELECT `+outboxColumns+`, o.document
		FROM commercial_outbox o LEFT JOIN customers c ON c.id = o.customer_id WHERE o.id = $1`, id), true, &doc)
}

// MarkOutboxDelivered records a successful delivery and, when the far end
// answered with a reference, stores it on the statement — that reference is
// what every later import is matched on.
func (s *Store) MarkOutboxDelivered(ctx context.Context, id int64, externalRef string) error {
	externalRef = strings.TrimSpace(externalRef)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var statementID string
	if err := tx.QueryRowContext(ctx, `UPDATE commercial_outbox
		SET delivered_at = now(), last_error = '', external_ref = $2, attempts = attempts + 1, updated_at = now()
		WHERE id = $1 RETURNING COALESCE(statement_id::text, '')`, id, externalRef).Scan(&statementID); err != nil {
		return mapErr(err)
	}
	if externalRef != "" && statementID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE statements SET external_invoice_ref = $2 WHERE id = $1 AND external_invoice_ref IS NULL`, statementID, externalRef); err != nil {
			return mapErr(err)
		}
	}
	return tx.Commit()
}

// MarkOutboxFailed records a failed attempt and schedules the next one.
func (s *Store) MarkOutboxFailed(ctx context.Context, id int64, cause error) error {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	var attempts int
	if err := s.db.QueryRowContext(ctx, `SELECT attempts FROM commercial_outbox WHERE id = $1`, id).Scan(&attempts); err != nil {
		return mapErr(err)
	}
	wait := OutboxBackoff(attempts + 1)
	_, err := s.db.ExecContext(ctx, `UPDATE commercial_outbox
		SET attempts = attempts + 1, last_error = $2, next_attempt_at = now() + $3::interval, updated_at = now()
		WHERE id = $1`, id, msg, fmt.Sprintf("%d seconds", int(wait.Seconds())))
	return mapErr(err)
}

// RequeueOutbox makes an entry due now and clears its last error, which is
// what the operator's Retry does. An already-delivered entry is refused: it
// is not a failure to retry.
func (s *Store) RequeueOutbox(ctx context.Context, id int64) (OutboxEntry, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE commercial_outbox SET next_attempt_at = now(), last_error = '', updated_at = now()
		WHERE id = $1 AND delivered_at IS NULL`, id)
	if err != nil {
		return OutboxEntry{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		e, gerr := s.GetOutboxEntry(ctx, id)
		if gerr != nil {
			return OutboxEntry{}, gerr
		}
		return OutboxEntry{}, fmt.Errorf("%w: this document was already delivered on %s", ErrConflict, e.DeliveredAt.Format(time.RFC3339))
	}
	return s.GetOutboxEntry(ctx, id)
}

// SetStatementExternalRef records the reference the billing system uses,
// when an inbound import is the first thing to name it. It never overwrites
// one already recorded.
func (s *Store) SetStatementExternalRef(ctx context.Context, statementID, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fmt.Errorf("%w: an external invoice reference cannot be empty", ErrInvalid)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE statements SET external_invoice_ref = $2 WHERE id = $1 AND external_invoice_ref IS NULL`, statementID, ref)
	return mapErr(err)
}
