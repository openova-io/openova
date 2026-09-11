package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// The daily cost rollup (#6926, DESIGN.md §20).
//
// Cost is still RATED at read time — the price book, the stopped-instance
// policy, the currency rate, the cost-centre rules and every label are read
// when the explorer runs, so a price change is visible immediately and the
// explorer can still never disagree with a statement for the same window.
// What is cached is the part that cannot change once a day is collected:
// the USAGE, aggregated to one row per (UTC day, source, resource, SKU,
// unit, region, labels).
//
// That is the whole trick. On hw307 usage_records held 742,461 hourly rows
// and the Sovereign-wide summary took ~50 s, because six explorer documents
// each rated all 742k rows, each through a price join and a cost-centre
// LATERAL. The same ledger is ~31k daily rows, and the rating arithmetic is
// unchanged.
//
// Two tables:
//
//   - cost_usage_daily   the aggregated ledger, one PARTITION per
//     (source_id, day). A partition is rebuilt whole — DELETE then INSERT
//     in one transaction — so it can never be half-updated.
//   - cost_rollup_state  one row per partition: `version`, bumped by a
//     trigger on EVERY usage write, and `built_version`, the version the
//     rows were built from. Fresh means built_version = version.
//
// Invalidation is STRUCTURAL, not a checklist. Four statement-level
// triggers on usage_records (insert, update-new, update-old, delete) bump
// the version of every partition the statement touched, using transition
// tables, so no writer — Go, psql, a cascade, a backfill — can put a row in
// the ledger without marking its partition. Deleting a source or a customer
// is one of those writers: it cascades into usage_records and the delete
// trigger fires with it.
//
// Everything else that changes a PRICE (a book edited, an item repriced, a
// book assigned to another source, a currency rate, the reporting currency,
// a discount, a contract term, a cost-centre rule, a customer renamed)
// needs no invalidation at all, because none of it is stored here: it is
// joined at read time exactly as before.
//
// Neither table carries a foreign key. Deleting a cost source cascades into
// usage_records, and the delete trigger then fires INSIDE that cascade — a
// foreign key on source_id would make that insert fail against the parent
// row the cascade has already removed. Orphans are swept by
// PurgeOrphanCostRollup instead, and the explorer's inner join to
// cost_sources hides them until it runs.
//
// The reader never touches a stale partition: costWindow asks which whole UTC
// days of the window the state table both KNOWS and calls fresh, serves those
// days from cost_usage_daily and the REST of the window — the part-day at
// each end, today, any day a late collection re-opened, and any day the state
// table says nothing about — from usage_records, aggregated by the SAME
// expression to the SAME grain. Both branches are unioned INSIDE the `u` CTE,
// so every aggregate downstream (including count(DISTINCT resource_id), which
// is not additive) sees one coherent relation and cannot double-count or drop
// a row at the seam.
//
// Absence is read as "serve it live", never as "there is nothing there". That
// is the one asymmetry worth stating twice: a stale partition costs a slow
// answer, an unknown one wrongly called empty would cost a silent zero.

// costRollupMigrationSQL is appended at the very END of migrations:
// migrations are positional, so an entry inserted above a database's
// recorded version is silently skipped. Located by content as
// MigrationCostRollup.
const costRollupMigrationSQL = `
-- The aggregated ledger. One row per (UTC day, source, resource, SKU, unit,
-- region, labels): labels are part of the key, so a record whose status,
-- namespace, tier, enterprise project or tags differ from its neighbour's
-- lands in its own row and every expression the explorer reads off labels
-- resolves exactly as it does per record. quantity is unconstrained NUMERIC
-- so the sum keeps the six decimals usage_records carries without ever
-- overflowing a precision.
CREATE TABLE IF NOT EXISTS cost_usage_daily (
	day TIMESTAMPTZ NOT NULL,
	source_id UUID NOT NULL,
	customer_id UUID,
	resource_id TEXT NOT NULL,
	resource_kind TEXT NOT NULL,
	sku TEXT NOT NULL,
	unit TEXT NOT NULL,
	region TEXT NOT NULL DEFAULT '',
	labels JSONB NOT NULL DEFAULT '{}'::jsonb,
	quantity NUMERIC NOT NULL,
	records BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS cost_usage_daily_partition_idx ON cost_usage_daily (source_id, day);
CREATE INDEX IF NOT EXISTS cost_usage_daily_day_idx ON cost_usage_daily (day);
CREATE INDEX IF NOT EXISTS cost_usage_daily_customer_day_idx ON cost_usage_daily (customer_id, day);

-- One row per partition. version is bumped by the triggers below on every
-- usage write; built_version records which version the rows were built
-- from. A partition is FRESH exactly when the two agree, and the reader
-- serves a day from the rollup only when no partition of that day is stale.
CREATE TABLE IF NOT EXISTS cost_rollup_state (
	source_id UUID NOT NULL,
	day TIMESTAMPTZ NOT NULL,
	version BIGINT NOT NULL DEFAULT 1,
	built_version BIGINT NOT NULL DEFAULT 0,
	built_at TIMESTAMPTZ,
	rows_built BIGINT NOT NULL DEFAULT 0,
	PRIMARY KEY (source_id, day)
);
CREATE INDEX IF NOT EXISTS cost_rollup_state_stale_idx ON cost_rollup_state (day) WHERE built_version <> version;

-- The invalidation. One function, four statement-level triggers, each
-- handing it its transition table under the same name: no usage write can
-- reach the ledger without bumping the version of the partition it lands
-- in. date_trunc is applied to the UTC clock (never the session's, which
-- would file a record under the wrong day in a non-UTC session), exactly
-- as the explorer's bucket expression does.
CREATE OR REPLACE FUNCTION cost_rollup_mark() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	INSERT INTO cost_rollup_state (source_id, day, version)
	SELECT DISTINCT source_id, (date_trunc('day', window_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'), 1
	  FROM chg
	ON CONFLICT (source_id, day) DO UPDATE SET version = cost_rollup_state.version + 1;
	RETURN NULL;
END $$;
DROP TRIGGER IF EXISTS usage_records_rollup_insert ON usage_records;
CREATE TRIGGER usage_records_rollup_insert AFTER INSERT ON usage_records
	REFERENCING NEW TABLE AS chg FOR EACH STATEMENT EXECUTE FUNCTION cost_rollup_mark();
DROP TRIGGER IF EXISTS usage_records_rollup_update_new ON usage_records;
CREATE TRIGGER usage_records_rollup_update_new AFTER UPDATE ON usage_records
	REFERENCING NEW TABLE AS chg FOR EACH STATEMENT EXECUTE FUNCTION cost_rollup_mark();
DROP TRIGGER IF EXISTS usage_records_rollup_update_old ON usage_records;
CREATE TRIGGER usage_records_rollup_update_old AFTER UPDATE ON usage_records
	REFERENCING OLD TABLE AS chg FOR EACH STATEMENT EXECUTE FUNCTION cost_rollup_mark();
DROP TRIGGER IF EXISTS usage_records_rollup_delete ON usage_records;
CREATE TRIGGER usage_records_rollup_delete AFTER DELETE ON usage_records
	REFERENCING OLD TABLE AS chg FOR EACH STATEMENT EXECUTE FUNCTION cost_rollup_mark();

-- The ledger this is deployed over already holds usage the triggers never
-- saw. Every (source, day) it carries gets a state row at version 1 with
-- built_version 0 — stale, so the reader keeps serving those days live
-- until the builder has them, and nothing is ever read from a partition
-- that was never built.
INSERT INTO cost_rollup_state (source_id, day, version, built_version)
SELECT DISTINCT source_id, (date_trunc('day', window_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'), 1, 0
  FROM usage_records
ON CONFLICT (source_id, day) DO NOTHING;
`

// MigrationCostRollup is the schema_migrations version of the rollup
// migration, located by CONTENT like every other one so a migration
// appended after it cannot move this version.
var MigrationCostRollup = func() int {
	for i, m := range migrations {
		if m == costRollupMigrationSQL {
			return i + 1
		}
	}
	return len(migrations)
}()

// ---------------------------------------------------------------------------
// the grain
// ---------------------------------------------------------------------------

// The grain the usage CTE aggregates to. Day is the rollup's own grain and
// what every day-, month- and window-total reader asks for; hour is asked
// for only by the explorer's hour-grain chart, which a daily rollup cannot
// serve and which therefore always reads the live ledger.
const (
	grainDay  = "day"
	grainHour = "hour"
)

// utcDayExpr and utcHourExpr truncate a usage record's window_start on the
// UTC clock, never the session's. The explorer's bucket labels are UTC
// (bucketExpr), so an aggregate filed by any other clock would put a record
// in a bucket the chart does not draw.
const (
	utcDayExpr  = `(date_trunc('day', window_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')`
	utcHourExpr = `(date_trunc('hour', window_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')`
)

func grainExpr(grain string) string {
	if grain == grainHour {
		return utcHourExpr
	}
	return utcDayExpr
}

// costRange is a half-open [from, to) slice of the requested window.
type costRange struct{ from, to time.Time }

// usageWindow is how a window is split: the whole UTC days the rollup
// serves, and the ranges the live ledger serves. The two are disjoint and
// together cover the window exactly.
type usageWindow struct {
	rollup []costRange
	live   []costRange
}

// utcDay is the UTC midnight at or before t.
func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// liveWindow serves the whole window from usage_records: what a window falls
// back to when the rollup is off, when the grain is hourly, or when no day of
// it is fresh. The rows are still aggregated to the reader's grain — the
// branches are one shape — so this is the same rating over the same records,
// not a second arithmetic.
func liveWindow(from, to time.Time) usageWindow {
	return usageWindow{live: []costRange{{from.UTC(), to.UTC()}}}
}

// SetCostRollupEnabled turns the rollup off for READS as well as builds.
// COST_ROLLUP_ENABLED=false is the operator's kill switch: with it off every
// window is rated over usage_records, and the answers are the same ones —
// what it costs is measured in DESIGN.md §20.8.
func (s *Store) SetCostRollupEnabled(on bool) {
	var v int32
	if !on {
		v = 1
	}
	s.rollupOff.Store(v)
}

// CostRollupEnabled reports whether the rollup is read and built.
func (s *Store) CostRollupEnabled() bool { return s.rollupOff.Load() == 0 }

// costWindow splits [from, to) into the whole UTC days the rollup covers and
// the ranges the live ledger must serve.
//
// A day is covered when three things hold: it lies WHOLLY inside the window,
// the state table KNOWS it — it carries at least one partition for that day —
// and none of those partitions is stale.
//
// The middle condition is what keeps a missing state row from reading as an
// empty day. Absence has to mean "serve it live", never "there is nothing
// there": a day whose partitions were never recorded holds usage the rollup
// does not have, and calling it covered would report zero for it. A day that
// genuinely has no usage reads the same either way, so the rule costs only an
// index probe over an empty range.
//
// A part-day at either end is never covered: the rollup row carries the whole
// day, so serving it for a window that starts at noon would count the
// morning. That, and the fact that both branches are unioned before anything
// is aggregated, is what makes the seam safe.
func (s *Store) costWindow(ctx context.Context, from, to time.Time, grain string) (usageWindow, error) {
	from, to = from.UTC(), to.UTC()
	if !s.CostRollupEnabled() || grain == grainHour || !to.After(from) {
		return liveWindow(from, to), nil
	}
	covered, err := s.coveredRollupDays(ctx, from, to)
	if err != nil {
		return usageWindow{}, err
	}
	return splitWindow(from, to, covered), nil
}

// splitWindow is the pure half of costWindow: given the days the rollup may
// serve, which whole days it takes and what is left for the live ledger.
func splitWindow(from, to time.Time, coveredDays map[int64]bool) usageWindow {
	from, to = from.UTC(), to.UTC()
	var covered []costRange
	d := utcDay(from)
	if d.Before(from) {
		d = d.AddDate(0, 0, 1)
	}
	for ; !d.AddDate(0, 0, 1).After(to); d = d.AddDate(0, 0, 1) {
		if !coveredDays[d.Unix()] {
			continue
		}
		if n := len(covered); n > 0 && covered[n-1].to.Equal(d) {
			covered[n-1].to = d.AddDate(0, 0, 1)
			continue
		}
		covered = append(covered, costRange{d, d.AddDate(0, 0, 1)})
	}
	if len(covered) == 0 {
		return liveWindow(from, to)
	}
	var live []costRange
	cur := from
	for _, c := range covered {
		if c.from.After(cur) {
			live = append(live, costRange{cur, c.from})
		}
		cur = c.to
	}
	if cur.Before(to) {
		live = append(live, costRange{cur, to})
	}
	return usageWindow{rollup: covered, live: live}
}

// coveredRollupDays is the set of UTC days in the window the rollup may
// serve — those the state table carries partitions for, none of them stale —
// keyed by the day's Unix second. A day it says nothing about is not in the
// set, which is what makes absence mean "read it live".
func (s *Store) coveredRollupDays(ctx context.Context, from, to time.Time) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT day, count(*) FILTER (WHERE built_version <> version)
		FROM cost_rollup_state WHERE day >= $1 AND day < $2 GROUP BY day`, utcDay(from), to)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var d time.Time
		var stale int
		if err := rows.Scan(&d, &stale); err != nil {
			return nil, err
		}
		if stale == 0 {
			out[d.UTC().Unix()] = true
		}
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// the usage CTE
// ---------------------------------------------------------------------------

// costUsageProjection is the column list BOTH branches of the usage CTE
// project, in this order. It is written once so the two branches cannot
// drift into projecting different things in the same position.
const costUsageProjection = `customer_id, source_id, resource_id, resource_kind, sku, unit, region, labels`

// rangeClause renders `(col >= $a AND col < $b) OR ...` for the ranges.
func rangeClause(a *costArgs, col string, rs []costRange) string {
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		parts = append(parts, "("+col+" >= "+a.add(r.from)+" AND "+col+" < "+a.add(r.to)+")")
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// usageBranches renders the body of the `u` CTE: the rollup rows for the
// days it covers, UNION ALL the live ledger aggregated to the SAME grain by
// the SAME expression for the rest of the window.
//
// The two branches are the same aggregation of the same facts — the rollup
// is a cache of exactly what the live branch computes — so a figure is the
// same number whichever branch produced it, to the last digit, and a window
// served partly by each is not a different arithmetic from a window served
// wholly by one.
func usageBranches(a *costArgs, w usageWindow, grain string) string {
	var parts []string
	if len(w.rollup) > 0 {
		parts = append(parts, `
SELECT `+costUsageProjection+`, day AS window_start, quantity, records
  FROM cost_usage_daily
 WHERE `+rangeClause(a, "day", w.rollup))
	}
	if len(w.live) > 0 {
		parts = append(parts, `
SELECT `+costUsageProjection+`, `+grainExpr(grain)+` AS window_start,
       sum(quantity) AS quantity, count(*)::bigint AS records
  FROM usage_records
 WHERE `+rangeClause(a, "window_start", w.live)+` AND `+metricSKUFilter+`
 GROUP BY 1, 2, 3, 4, 5, 6, 7, 8, 9`)
	}
	if len(parts) == 0 {
		return `SELECT NULL::uuid AS customer_id, NULL::uuid AS source_id, ''::text AS resource_id,
		       ''::text AS resource_kind, ''::text AS sku, ''::text AS unit, ''::text AS region,
		       '{}'::jsonb AS labels, now() AS window_start, 0::numeric AS quantity, 0::bigint AS records
		 WHERE false`
	}
	return strings.Join(parts, "\nUNION ALL")
}

// ---------------------------------------------------------------------------
// the builder
// ---------------------------------------------------------------------------

// CostRollupPartition is one (source, UTC day) unit of rebuild, with the
// version the builder read before it started.
type CostRollupPartition struct {
	SourceID string
	Day      time.Time
	Version  int64
}

// StaleCostRollupPartitions lists the partitions whose rows are missing or
// behind their version, newest day first — the recent days are the ones the
// Overview reads. limit 0 means every one of them.
func (s *Store) StaleCostRollupPartitions(ctx context.Context, limit int) ([]CostRollupPartition, error) {
	q := `SELECT source_id::text, day, version FROM cost_rollup_state
		WHERE built_version <> version ORDER BY day DESC, source_id`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []CostRollupPartition{}
	for rows.Next() {
		var p CostRollupPartition
		if err := rows.Scan(&p.SourceID, &p.Day, &p.Version); err != nil {
			return nil, err
		}
		p.Day = p.Day.UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

// costRollupBuildSQL rebuilds one partition from the live ledger. It is the
// SAME aggregation usageBranches' live branch performs, written once here so
// a cached day and a live day can never be aggregated differently.
const costRollupBuildSQL = `
INSERT INTO cost_usage_daily (` + costUsageProjection + `, day, quantity, records)
SELECT ` + costUsageProjection + `, ` + utcDayExpr + `, sum(quantity), count(*)::bigint
  FROM usage_records
 WHERE source_id = $1 AND window_start >= $2 AND window_start < $3 AND ` + metricSKUFilter + `
 GROUP BY 1, 2, 3, 4, 5, 6, 7, 8, 9`

// BuildCostRollupPartition rebuilds one partition whole, in one transaction:
// the old rows go, the aggregate goes in, and the partition is marked built
// at the version it started from.
//
// fresh is false when a usage write landed in the partition while it was
// being built — the version moved, the mark does not take, and the partition
// is rebuilt on the next pass. The rows written in that case stay marked
// stale, so the reader keeps serving that day live: the race can cost a
// rebuild, never a wrong figure.
func (s *Store) BuildCostRollupPartition(ctx context.Context, p CostRollupPartition) (rows int64, fresh bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM cost_usage_daily WHERE source_id = $1 AND day = $2`, p.SourceID, p.Day); err != nil {
		return 0, false, mapErr(err)
	}
	res, err := tx.ExecContext(ctx, costRollupBuildSQL, p.SourceID, p.Day, p.Day.AddDate(0, 0, 1))
	if err != nil {
		return 0, false, mapErr(err)
	}
	rows, _ = res.RowsAffected()
	mark, err := tx.ExecContext(ctx, `UPDATE cost_rollup_state
		SET built_version = version, built_at = now(), rows_built = $3
		WHERE source_id = $1 AND day = $2 AND version = $4`, p.SourceID, p.Day, rows, p.Version)
	if err != nil {
		return 0, false, mapErr(err)
	}
	n, _ := mark.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return rows, n == 1, nil
}

// PurgeOrphanCostRollup removes rollup rows and state whose cost source is
// gone. Neither table carries a foreign key (see the file comment), so the
// sweep is what keeps a deleted source from leaving behind a stale day that
// nothing would ever rebuild.
func (s *Store) PurgeOrphanCostRollup(ctx context.Context) (int64, error) {
	var total int64
	for _, q := range []string{
		`DELETE FROM cost_usage_daily d WHERE NOT EXISTS (SELECT 1 FROM cost_sources s WHERE s.id = d.source_id)`,
		`DELETE FROM cost_rollup_state r WHERE NOT EXISTS (SELECT 1 FROM cost_sources s WHERE s.id = r.source_id)`,
	} {
		res, err := s.db.ExecContext(ctx, q)
		if err != nil {
			return total, mapErr(err)
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

// CostRollupStatus is what the rollup holds, for the metrics the service
// exposes and for an operator asking whether the Overview is being served
// from the cache or from the ledger.
type CostRollupStatus struct {
	Partitions int        `json:"partitions"`
	Stale      int        `json:"stale"`
	Rows       int64      `json:"rows"`
	OldestDay  *string    `json:"oldest_day"`
	NewestDay  *string    `json:"newest_day"`
	BuiltAt    *time.Time `json:"built_at"`
}

// CostRollupStatus reads the counters above in one query.
func (s *Store) CostRollupStatus(ctx context.Context) (CostRollupStatus, error) {
	var out CostRollupStatus
	var oldest, newest *time.Time
	err := s.db.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE built_version <> version),
		COALESCE(sum(rows_built), 0), min(day), max(day), max(built_at) FROM cost_rollup_state`).
		Scan(&out.Partitions, &out.Stale, &out.Rows, &oldest, &newest, &out.BuiltAt)
	if err != nil {
		return out, mapErr(err)
	}
	fmtDay := func(t *time.Time) *string {
		if t == nil {
			return nil
		}
		v := t.UTC().Format(bucketFormatDay)
		return &v
	}
	out.OldestDay, out.NewestDay = fmtDay(oldest), fmtDay(newest)
	if out.BuiltAt != nil {
		v := out.BuiltAt.UTC()
		out.BuiltAt = &v
	}
	return out, nil
}
