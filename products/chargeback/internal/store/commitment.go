package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// THE PACKAGE IS PREPAID (DESIGN.md §22.11, founder direction 2026-10-10).
//
// A package is a prepaid monthly commitment. The statement issued for period
// N carries the package for period N+1 IN ADVANCE — one line at the flat
// monthly price, its service period stamped on the line — next to the usage
// above the package for period N IN ARREARS. This file is what the rating
// run needs from the database for that:
//
//   - rated_lines.service_period_start / service_period_end — the period a
//     line pays for when it is not the statement's own period (the advance
//     line), so the next statement can find what was already paid;
//   - the TIMELINE of a Source's plan-hours inside a period, in time order —
//     the plan it was on when, which is what an upgrade's prorated charge and
//     the plan billed in advance are read off;
//   - cost_sources.prepaid_amount — what the checkout collected for the
//     package and its add-ons (spec.commerce.prepaidAmount), credited on the
//     Source's first statement so it is never charged twice;
//   - spend_stops — one row per (Source, period) in which the grow spend
//     limit was reached: when, at what accrued overage, whether the platform
//     was told, whether the stop was lifted.

// commitmentMigrationSQL is appended at the END of migrations and located by
// content as MigrationCommitment.
const commitmentMigrationSQL = `
ALTER TABLE rated_lines ADD COLUMN IF NOT EXISTS service_period_start DATE;
ALTER TABLE rated_lines ADD COLUMN IF NOT EXISTS service_period_end DATE;
CREATE INDEX IF NOT EXISTS rated_lines_service_period_idx ON rated_lines (source_id, service_period_start) WHERE service_period_start IS NOT NULL;
ALTER TABLE cost_sources ADD COLUMN IF NOT EXISTS prepaid_amount NUMERIC(20,3);
ALTER TABLE cost_sources DROP CONSTRAINT IF EXISTS cost_sources_prepaid_amount_check;
ALTER TABLE cost_sources ADD CONSTRAINT cost_sources_prepaid_amount_check CHECK (prepaid_amount IS NULL OR prepaid_amount > 0);
CREATE TABLE IF NOT EXISTS spend_stops (
	source_id UUID NOT NULL REFERENCES cost_sources(id) ON DELETE CASCADE,
	period_start DATE NOT NULL,
	stopped_at TIMESTAMPTZ NOT NULL,
	until TIMESTAMPTZ NOT NULL,
	accrued NUMERIC(20,6) NOT NULL,
	spend_limit NUMERIC(20,3) NOT NULL,
	applied_at TIMESTAMPTZ,
	notified_at TIMESTAMPTZ,
	cleared_at TIMESTAMPTZ,
	last_error TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (source_id, period_start)
);
`

// MigrationCommitment is the schema_migrations version of the commitment
// migration, located by content.
var MigrationCommitment = func() int {
	for i, m := range migrations {
		if m == commitmentMigrationSQL {
			return i + 1
		}
	}
	return len(migrations)
}()

// PlanSpan is a stretch of consecutive plan-hours of ONE plan inside a
// period: [Start, End), Hours the plan-hours the collector wrote for it.
type PlanSpan struct {
	Slug  string
	Start time.Time
	End   time.Time
	Hours Decimal
}

// PlanTimeline reads a Source's plan.<slug> records whose window starts in
// [from, to), in time order, merged into spans: consecutive records of the
// same plan whose windows touch are one span. A plan change mid-hour leaves
// the old plan's slice ending at the change and the new plan's starting
// there, so the order by window_start is the order the plans ran in. A gap
// (a suspension) starts a new span even on the same plan.
func (s *Store) PlanTimeline(ctx context.Context, sourceID string, from, to time.Time) ([]PlanSpan, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sku, window_start, window_end, quantity::text FROM usage_records
		WHERE source_id = $1 AND resource_kind = $2 AND sku LIKE 'plan.%' AND window_start >= $3 AND window_start < $4 AND quantity > 0
		ORDER BY window_start, window_end`, sourceID, PlanKind, from, to)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []PlanSpan
	for rows.Next() {
		var sku, q string
		var ws, we time.Time
		if err := rows.Scan(&sku, &ws, &we, &q); err != nil {
			return nil, err
		}
		slug := NormalizePlanSlug(strings.TrimPrefix(sku, PlanSKUPrefix))
		if !ValidPlanSlug(slug) || !PlanBillable(slug) {
			continue
		}
		ws, we = ws.UTC(), we.UTC()
		if n := len(out); n > 0 && out[n-1].Slug == slug && !ws.After(out[n-1].End) {
			out[n-1].Hours = addDec(out[n-1].Hours, Decimal(q))
			if we.After(out[n-1].End) {
				out[n-1].End = we
			}
			continue
		}
		out = append(out, PlanSpan{Slug: slug, Start: ws, End: we, Hours: Decimal(q)})
	}
	return out, rows.Err()
}

// HasPlanBefore reports whether the Source has any plan-hour before t — false
// means the period starting at t is the Source's FIRST on a package, the one
// the checkout paid for.
func (s *Store) HasPlanBefore(ctx context.Context, sourceID string, t time.Time) (bool, error) {
	var ok bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM usage_records WHERE source_id = $1 AND resource_kind = $2 AND sku LIKE 'plan.%' AND window_start < $3 AND quantity > 0)`,
		sourceID, PlanKind, t).Scan(&ok)
	return ok, mapErr(err)
}

// PrepaidPeriod is the advance line that paid for a period: the plan and the
// amount, read off a statement of an EARLIER period.
type PrepaidPeriod struct {
	PlanSlug    string
	Amount      Decimal
	StatementID string
}

// PrepaidFor returns the advance line an earlier statement wrote for the
// period starting at periodStart on this Source — the plan.<slug> line whose
// service period starts there — or nil when the period was not paid in
// advance. A cancelled statement paid nothing.
func (s *Store) PrepaidFor(ctx context.Context, sourceID string, periodStart time.Time) (*PrepaidPeriod, error) {
	var p PrepaidPeriod
	err := s.db.QueryRowContext(ctx, `SELECT l.sku, l.amount::text, st.id FROM rated_lines l JOIN statements st ON st.id = l.statement_id
		WHERE l.source_id = $1 AND l.sku LIKE 'plan.%' AND l.service_period_start = $2::date AND st.period_start < $2::date AND st.status <> $3
		ORDER BY l.amount DESC LIMIT 1`, sourceID, periodStart.UTC().Format("2006-01-02"), StatusCancelled).Scan(&p.PlanSlug, &p.Amount, &p.StatementID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, mapErr(err)
	}
	p.PlanSlug = NormalizePlanSlug(strings.TrimPrefix(p.PlanSlug, PlanSKUPrefix))
	return &p, nil
}

// SetSourcePrepaid records what the checkout collected for the Source's
// package (spec.commerce.prepaidAmount). Only a platform Source carries it.
func (s *Store) SetSourcePrepaid(ctx context.Context, sourceID string, amount Decimal) error {
	if r, ok := new(big.Rat).SetString(strings.TrimSpace(string(amount))); !ok || r.Sign() <= 0 {
		return fmt.Errorf("%w: a prepaid amount is a positive money string, not %q", ErrInvalid, amount)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE cost_sources SET prepaid_amount = $2::numeric WHERE id = $1 AND layer = $3 AND NOT internal`, sourceID, string(amount), LayerPlatform)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SpendStop is one period in which a grow Source's spend limit was reached.
type SpendStop struct {
	SourceID    string     `json:"source_id"`
	PeriodStart time.Time  `json:"period_start"`
	StoppedAt   time.Time  `json:"stopped_at"`
	Until       time.Time  `json:"until"`
	Accrued     Decimal    `json:"accrued"`
	SpendLimit  Decimal    `json:"spend_limit"`
	AppliedAt   *time.Time `json:"applied_at,omitempty"`
	NotifiedAt  *time.Time `json:"notified_at,omitempty"`
	ClearedAt   *time.Time `json:"cleared_at,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
}

const spendStopColumns = `source_id::text, period_start, stopped_at, until, accrued::text, spend_limit::text, applied_at, notified_at, cleared_at, last_error`

func scanSpendStop(row interface{ Scan(...any) error }) (SpendStop, error) {
	var ss SpendStop
	var applied, notified, cleared sql.NullTime
	if err := row.Scan(&ss.SourceID, &ss.PeriodStart, &ss.StoppedAt, &ss.Until, &ss.Accrued, &ss.SpendLimit, &applied, &notified, &cleared, &ss.LastError); err != nil {
		return ss, mapErr(err)
	}
	ss.PeriodStart, ss.StoppedAt, ss.Until = ss.PeriodStart.UTC(), ss.StoppedAt.UTC(), ss.Until.UTC()
	ss.AppliedAt, ss.NotifiedAt, ss.ClearedAt = timePtr(applied), timePtr(notified), timePtr(cleared)
	return ss, nil
}

// RecordSpendStop inserts the stop of one (Source, period) and reports
// whether THIS call inserted it: a second replica or an overlapping tick
// finds the row and leaves the notice to the one that wrote it.
func (s *Store) RecordSpendStop(ctx context.Context, ss SpendStop) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO spend_stops (source_id, period_start, stopped_at, until, accrued, spend_limit)
		VALUES ($1, $2::date, $3, $4, $5::numeric, $6::numeric) ON CONFLICT (source_id, period_start) DO NOTHING`,
		ss.SourceID, ss.PeriodStart.UTC().Format("2006-01-02"), ss.StoppedAt, ss.Until, string(ss.Accrued), string(ss.SpendLimit))
	if err != nil {
		return false, mapErr(err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// GetSpendStop reads the stop of one (Source, period), ErrNotFound when the
// limit was not reached in it.
func (s *Store) GetSpendStop(ctx context.Context, sourceID string, periodStart time.Time) (SpendStop, error) {
	return scanSpendStop(s.db.QueryRowContext(ctx, `SELECT `+spendStopColumns+` FROM spend_stops WHERE source_id = $1 AND period_start = $2::date`,
		sourceID, periodStart.UTC().Format("2006-01-02")))
}

// OpenSpendStops lists the stops not lifted yet — the ones the platform still
// has to be told about, and the past ones to lift.
func (s *Store) OpenSpendStops(ctx context.Context) ([]SpendStop, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+spendStopColumns+` FROM spend_stops WHERE cleared_at IS NULL ORDER BY period_start, source_id`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []SpendStop
	for rows.Next() {
		ss, err := scanSpendStop(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ss)
	}
	return out, rows.Err()
}

// MarkSpendStop stamps what happened to a stop: field is applied_at,
// notified_at or cleared_at, at the instant given. errText is recorded as
// last_error ("" clears it).
func (s *Store) MarkSpendStop(ctx context.Context, sourceID string, periodStart time.Time, field string, at time.Time, errText string) error {
	switch field {
	case "applied_at", "notified_at", "cleared_at":
	default:
		return fmt.Errorf("%w: unknown spend stop field %q", ErrInvalid, field)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE spend_stops SET `+field+` = $3, last_error = $4 WHERE source_id = $1 AND period_start = $2::date`,
		sourceID, periodStart.UTC().Format("2006-01-02"), at, errText)
	return mapErr(err)
}

// DeleteSpendStop removes the stop of one (Source, period) — lifted early
// because the limit was raised above what had accrued, so the period may
// stop again at the new limit.
func (s *Store) DeleteSpendStop(ctx context.Context, sourceID string, periodStart time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM spend_stops WHERE source_id = $1 AND period_start = $2::date`, sourceID, periodStart.UTC().Format("2006-01-02"))
	return mapErr(err)
}

// SpendStopError records why the platform could not be told (retried on the
// next tick).
func (s *Store) SpendStopError(ctx context.Context, sourceID string, periodStart time.Time, errText string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE spend_stops SET last_error = $3 WHERE source_id = $1 AND period_start = $2::date`,
		sourceID, periodStart.UTC().Format("2006-01-02"), errText)
	return mapErr(err)
}

// SpendLimitedSources lists every grow platform Source with a spend limit,
// on a customer — the Sources the spend-limit evaluator watches.
func (s *Store) SpendLimitedSources(ctx context.Context) ([]CostSource, error) {
	return s.querySources(ctx, `WHERE s.overage_mode = 'grow' AND s.spend_limit_month IS NOT NULL AND s.customer_id IS NOT NULL AND NOT s.internal AND s.status <> $1 ORDER BY s.customer_id, s.id`, StatusDisabled)
}
