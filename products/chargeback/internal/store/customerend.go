package store

import (
	"context"
	"time"
)

// An Organization's customer ENDS when the Organization ends — its
// subscription is cancelled and the Organization is deleted. ended_at is that
// instant: the customer is metered and billed up to it and never after
// (the platform collector stops its windows there, the commitment never bills
// the next period in advance). The customer row, its statements and its usage
// ledger stay — they are billing records. Before this column a cancelled
// Organization's customer stayed active, metered and billed (measured on the
// two-region environment, 22 November 2026: usage records 17 hours after the
// cancellation).
const customerEndedMigrationSQL = `
ALTER TABLE customers ADD COLUMN IF NOT EXISTS ended_at TIMESTAMPTZ;
`

// MigrationCustomerEnded is the schema_migrations version of the ended_at
// column, located by content so a migration appended after it cannot move it.
var MigrationCustomerEnded = func() int {
	for i, m := range migrations {
		if m == customerEndedMigrationSQL {
			return i + 1
		}
	}
	return len(migrations)
}()

// EndCustomer closes a customer at the instant its Organization ended. The
// FIRST instant wins (COALESCE), so a repeated end — the informer redelivers,
// the Organization is first marked for deletion and then removed — never
// moves it. The status becomes suspended: nothing new is collected for it.
func (s *Store) EndCustomer(ctx context.Context, id string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE customers SET ended_at = COALESCE(ended_at, $2), status = 'suspended', updated_at = now() WHERE id = $1`, id, at.UTC())
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ReopenCustomer clears ended_at: an Organization of the same slug exists
// again. The caller restarts collection from now, so the ended gap is never
// metered.
func (s *Store) ReopenCustomer(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE customers SET ended_at = NULL, updated_at = now() WHERE id = $1 AND ended_at IS NOT NULL`, id)
	return mapErr(err)
}
