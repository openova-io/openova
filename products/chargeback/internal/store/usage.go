package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/lib/pq"
)

func pqArray(v []string) any {
	if v == nil {
		v = []string{}
	}
	return pq.Array(v)
}

// UpsertUsage writes records idempotently on (source, resource, sku,
// window_start): a re-run over the same hour updates quantity and window_end.
func (s *Store) UpsertUsage(ctx context.Context, recs []UsageRecord) (int, error) {
	if len(recs) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO usage_records
		(customer_id, source_id, resource_id, resource_kind, sku, quantity, unit, window_start, window_end, region, labels, raw_ref, collected_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now())
		ON CONFLICT (source_id, resource_id, sku, window_start) DO UPDATE SET
			quantity = EXCLUDED.quantity, unit = EXCLUDED.unit, window_end = EXCLUDED.window_end,
			labels = EXCLUDED.labels, raw_ref = CASE WHEN EXCLUDED.raw_ref <> '' THEN EXCLUDED.raw_ref ELSE usage_records.raw_ref END,
			collected_at = now()`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	n := 0
	for _, r := range recs {
		labels := r.Labels
		if len(labels) == 0 {
			labels = []byte("{}")
		}
		if _, err := stmt.ExecContext(ctx, r.CustomerID, r.SourceID, r.ResourceID, r.ResourceKind, r.SKU, string(r.Quantity), r.Unit, r.WindowStart, r.WindowEnd, r.Region, []byte(labels), r.RawRef); err != nil {
			return n, mapErr(err)
		}
		n++
	}
	return n, tx.Commit()
}

// DeleteUsageInRange removes a resource's records whose window starts in
// [from, to) — used before recomputing hours whose boundaries changed.
func (s *Store) DeleteUsageInRange(ctx context.Context, sourceID, resourceID string, from, to time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM usage_records WHERE source_id = $1 AND resource_id = $2 AND window_start >= $3 AND window_start < $4 AND sku <> 'ecs.cpu_util'`,
		sourceID, resourceID, from, to)
	if err != nil {
		return 0, mapErr(err)
	}
	return res.RowsAffected()
}

// UsageQuery selects a period and grouping.
type UsageQuery struct {
	From, To time.Time
	GroupBy  string // sku | resource | day
}

// QueryUsage aggregates a customer's usage.
func (s *Store) QueryUsage(ctx context.Context, scope Scope, customerID string, q UsageQuery) ([]UsageRow, error) {
	if !scope.Allows(customerID) {
		return nil, ErrNotFound
	}
	var sqlText string
	switch q.GroupBy {
	case "resource":
		sqlText = `SELECT u.resource_id, u.sku, u.unit, sum(u.quantity)::text, 1, u.resource_kind,
			COALESCE((SELECT i.name FROM resource_inventory i WHERE i.source_id = u.source_id AND i.resource_id = u.resource_id), '')
			FROM usage_records u WHERE u.customer_id = $1 AND u.window_start >= $2 AND u.window_start < $3
			GROUP BY u.resource_id, u.sku, u.unit, u.resource_kind, u.source_id ORDER BY u.resource_kind, u.resource_id, u.sku`
	case "day":
		sqlText = `SELECT to_char(date_trunc('day', u.window_start AT TIME ZONE 'UTC'), 'YYYY-MM-DD'), u.sku, u.unit, sum(u.quantity)::text, count(DISTINCT u.resource_id), '', ''
			FROM usage_records u WHERE u.customer_id = $1 AND u.window_start >= $2 AND u.window_start < $3
			GROUP BY 1, u.sku, u.unit ORDER BY 1, u.sku`
	default:
		sqlText = `SELECT u.sku, u.sku, u.unit, sum(u.quantity)::text, count(DISTINCT u.resource_id), '', ''
			FROM usage_records u WHERE u.customer_id = $1 AND u.window_start >= $2 AND u.window_start < $3
			GROUP BY u.sku, u.unit ORDER BY u.sku`
	}
	rows, err := s.db.QueryContext(ctx, sqlText, customerID, q.From, q.To)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []UsageRow{}
	for rows.Next() {
		var r UsageRow
		var qty string
		if err := rows.Scan(&r.Key, &r.SKU, &r.Unit, &qty, &r.ResourceCount, &r.ResourceKind, &r.ResourceName); err != nil {
			return nil, err
		}
		r.Quantity = Decimal(qty)
		out = append(out, r)
	}
	return out, rows.Err()
}

// RatableUsage is one (source, sku, unit) aggregate with the stopped-hours
// split the rating step needs for the bill_stopped policy.
type RatableUsage struct {
	SourceID        string
	SKU             string
	Unit            string
	ResourceKind    string
	Quantity        Decimal // total
	StoppedQuantity Decimal // portion labelled as a stopped instance (or a volume attached to one)
	ResourceCount   int
}

// UsageForRating aggregates a customer's records in [from, to) per source and
// SKU, splitting out the stopped-instance share.
func (s *Store) UsageForRating(ctx context.Context, customerID string, from, to time.Time) ([]RatableUsage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT u.source_id, u.sku, u.unit, u.resource_kind, sum(u.quantity)::text,
		COALESCE(sum(u.quantity) FILTER (WHERE upper(COALESCE(u.labels->>'status','')) IN ('SHUTOFF','STOPPED','SHUTDOWN')
			OR upper(COALESCE(u.labels->>'server_status','')) IN ('SHUTOFF','STOPPED','SHUTDOWN')), 0)::text,
		count(DISTINCT u.resource_id)
		FROM usage_records u WHERE u.customer_id = $1 AND u.window_start >= $2 AND u.window_start < $3
		GROUP BY u.source_id, u.sku, u.unit, u.resource_kind ORDER BY u.source_id, u.sku`, customerID, from, to)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []RatableUsage{}
	for rows.Next() {
		var r RatableUsage
		var q, sq string
		if err := rows.Scan(&r.SourceID, &r.SKU, &r.Unit, &r.ResourceKind, &q, &sq, &r.ResourceCount); err != nil {
			return nil, err
		}
		r.Quantity, r.StoppedQuantity = Decimal(q), Decimal(sq)
		out = append(out, r)
	}
	return out, rows.Err()
}

// UsageSummary is a top-level total for the operator overview.
type UsageSummary struct {
	SKU           string  `json:"sku"`
	Unit          string  `json:"unit"`
	Quantity      Decimal `json:"quantity"`
	CustomerCount int     `json:"customer_count"`
}

// UsageSince aggregates all customers' usage since a time (overview).
func (s *Store) UsageSince(ctx context.Context, since time.Time, limit int) ([]UsageSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sku, unit, sum(quantity)::text, count(DISTINCT customer_id) FROM usage_records
		WHERE window_start >= $1 AND sku <> 'ecs.cpu_util' GROUP BY sku, unit ORDER BY sum(quantity) DESC LIMIT $2`, since, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []UsageSummary{}
	for rows.Next() {
		var u UsageSummary
		var q string
		if err := rows.Scan(&u.SKU, &u.Unit, &q, &u.CustomerCount); err != nil {
			return nil, err
		}
		u.Quantity = Decimal(q)
		out = append(out, u)
	}
	return out, rows.Err()
}

// UsageCount returns the number of records for a source (tests/metrics).
func (s *Store) UsageCount(ctx context.Context, sourceID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM usage_records WHERE source_id = $1`, sourceID).Scan(&n)
	return n, mapErr(err)
}

// ListUsageRecords returns raw records for a resource (tests/debugging).
func (s *Store) ListUsageRecords(ctx context.Context, sourceID, resourceID string) ([]UsageRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, customer_id, source_id, resource_id, resource_kind, sku, quantity::text, unit, window_start, window_end, region, labels, raw_ref, collected_at
		FROM usage_records WHERE source_id = $1 AND resource_id = $2 ORDER BY sku, window_start`, sourceID, resourceID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []UsageRecord{}
	for rows.Next() {
		var r UsageRecord
		var q string
		var labels []byte
		var rawRef sql.NullString
		if err := rows.Scan(&r.ID, &r.CustomerID, &r.SourceID, &r.ResourceID, &r.ResourceKind, &r.SKU, &q, &r.Unit, &r.WindowStart, &r.WindowEnd, &r.Region, &labels, &rawRef, &r.CollectedAt); err != nil {
			return nil, err
		}
		r.Quantity = Decimal(q)
		r.Labels = labels
		r.RawRef = rawRef.String
		r.WindowStart, r.WindowEnd = r.WindowStart.UTC(), r.WindowEnd.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// PeriodBounds turns "YYYY-MM" into [first day, first day of next month).
func PeriodBounds(period string) (time.Time, time.Time, error) {
	t, err := time.Parse("2006-01", period)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("period must be YYYY-MM: %w", err)
	}
	start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 1, 0), nil
}
