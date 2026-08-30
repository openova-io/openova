package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const statementColumns = `st.id, st.customer_id, to_char(st.period_start, 'YYYY-MM-DD'), to_char(st.period_end, 'YYYY-MM-DD'), st.currency,
	st.subtotal::text, st.tax_rate::text, st.tax::text, st.total::text, st.status, st.issued_at, st.created_at, c.name`

func scanStatement(row interface{ Scan(...any) error }) (Statement, error) {
	var st Statement
	var sub, rate, tax, total string
	var issued sql.NullTime
	if err := row.Scan(&st.ID, &st.CustomerID, &st.PeriodStart, &st.PeriodEnd, &st.Currency, &sub, &rate, &tax, &total, &st.Status, &issued, &st.CreatedAt, &st.CustomerName); err != nil {
		return st, mapErr(err)
	}
	st.Subtotal, st.TaxRate, st.Tax, st.Total = Decimal(sub), Decimal(rate), Decimal(tax), Decimal(total)
	st.IssuedAt = timePtr(issued)
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
		if err := tx.QueryRowContext(ctx, `INSERT INTO statements (customer_id, period_start, period_end, currency, subtotal, tax_rate, tax, total, status)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'draft') RETURNING id`,
			d.CustomerID, d.PeriodStart, d.PeriodEnd, d.Currency, string(d.Subtotal), string(d.TaxRate), string(d.Tax), string(d.Total)).Scan(&existingID); err != nil {
			return Statement{}, mapErr(err)
		}
	case err != nil:
		return Statement{}, mapErr(err)
	case status == "issued":
		return Statement{}, fmt.Errorf("%w: statement for this period is already issued", ErrConflict)
	default:
		if _, err := tx.ExecContext(ctx, `UPDATE statements SET period_end = $2, currency = $3, subtotal = $4, tax_rate = $5, tax = $6, total = $7, created_at = now() WHERE id = $1`,
			existingID, d.PeriodEnd, d.Currency, string(d.Subtotal), string(d.TaxRate), string(d.Tax), string(d.Total)); err != nil {
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
	return st, rows.Err()
}

// IssueStatement flips a draft to issued (idempotent on already-issued).
func (s *Store) IssueStatement(ctx context.Context, id string) (Statement, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE statements SET status = 'issued', issued_at = COALESCE(issued_at, now()) WHERE id = $1`, id)
	if err != nil {
		return Statement{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Statement{}, ErrNotFound
	}
	return s.GetStatement(ctx, OperatorScope, id)
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
