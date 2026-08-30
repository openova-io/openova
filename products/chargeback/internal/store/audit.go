package store

import (
	"context"
)

// Audit appends one entry. Details must never contain secrets; callers pass
// identifiers and public fields only.
func (s *Store) Audit(ctx context.Context, customerID *string, actor, action string, details any) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_log (customer_id, actor, action, details) VALUES ($1, $2, $3, $4)`, nullStr(customerID), actor, action, jsonOrEmpty(details))
	return mapErr(err)
}

// ListAudit returns a customer's entries, newest first.
func (s *Store) ListAudit(ctx context.Context, scope Scope, customerID string, limit int) ([]AuditEntry, error) {
	if !scope.Allows(customerID) {
		return nil, ErrNotFound
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, customer_id, actor, action, details, at FROM audit_log WHERE customer_id = $1 ORDER BY at DESC, id DESC LIMIT $2`, customerID, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var cust *string
		var details []byte
		if err := rows.Scan(&e.ID, &cust, &e.Actor, &e.Action, &details, &e.At); err != nil {
			return nil, err
		}
		e.CustomerID = cust
		e.Details = details
		e.At = e.At.UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}
