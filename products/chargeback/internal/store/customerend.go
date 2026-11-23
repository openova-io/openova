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

// CutUsageAt removes from a source's ledger everything metered after `at`,
// the instant its Organization ended. The customer is closed only once the
// Organization CR is marked for deletion, which comes after the provisioning
// service's teardown, its Git prune and its event, so a collection pass in
// that gap writes records past the end; without this they stay billed (the
// collector never revisits a window once its stamp has passed it). A record
// that starts at or after `at` is deleted. A record that spans it is cut to
// end there, its quantity scaled to the part before: a meter's quantity is
// hours times a fixed factor over its window (window.Quantity), so the scale
// is exact. A sampled metric (metric_skus.go) is never rated, so only its
// window is cut. One transaction; safe to run twice (the second finds
// nothing past `at`). Returns the number of records deleted or cut.
func (s *Store) CutUsageAt(ctx context.Context, sourceID string, at time.Time) (int64, error) {
	at = at.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, mapErr(err)
	}
	defer func() { _ = tx.Rollback() }() // a no-op after Commit
	res, err := tx.ExecContext(ctx, `DELETE FROM usage_records WHERE source_id = $1 AND window_start >= $2`, sourceID, at)
	if err != nil {
		return 0, mapErr(err)
	}
	deleted, _ := res.RowsAffected()
	res, err = tx.ExecContext(ctx, `UPDATE usage_records SET
	quantity = CASE WHEN sku IN `+metricSKUsSQL+` THEN quantity
		ELSE round(quantity * (EXTRACT(EPOCH FROM ($2::timestamptz - window_start))::numeric
			/ EXTRACT(EPOCH FROM (window_end - window_start))::numeric), 6) END,
	window_end = $2, collected_at = now()
 WHERE source_id = $1 AND window_start < $2 AND window_end > $2`, sourceID, at)
	if err != nil {
		return 0, mapErr(err)
	}
	cut, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, mapErr(err)
	}
	return deleted + cut, nil
}

// ReopenCustomer clears ended_at: an Organization of the same slug exists
// again. The caller restarts collection from now, so the ended gap is never
// metered.
func (s *Store) ReopenCustomer(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE customers SET ended_at = NULL, updated_at = now() WHERE id = $1 AND ended_at IS NOT NULL`, id)
	return mapErr(err)
}
