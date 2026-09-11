// Package rollup keeps the daily cost rollup current (#6926, DESIGN.md §20).
//
// The reader never waits on it: a window whose partitions are stale is rated
// over usage_records instead, so the worst a stopped builder costs is a slow
// page and never a wrong figure. What the builder buys is that the Overview's
// month is served from ~30k daily rows instead of ~717k hourly ones — 1.55 s
// against 27.7 s, measured in DESIGN.md §20.8.
package rollup

import (
	"context"
	"log/slog"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// DefaultInterval is how often the builder looks for stale partitions. The
// collector writes every COLLECT_INTERVAL (15 minutes by default) and marks
// the day it wrote into; a minute keeps today's partition fresh between
// passes without the rebuild ever being more than one day of usage.
const DefaultInterval = time.Minute

// DefaultBatch caps one pass. A Sovereign with a year of history and a
// handful of sources has a few thousand partitions on the first pass after
// the migration; building them a batch at a time keeps any single pass short
// and leaves the connection pool to the API.
const DefaultBatch = 400

// Builder rebuilds stale rollup partitions on a ticker.
type Builder struct {
	Store    *store.Store
	Interval time.Duration
	Batch    int
	Metrics  *metrics.Registry
}

func (b *Builder) interval() time.Duration {
	if b.Interval > 0 {
		return b.Interval
	}
	return DefaultInterval
}

func (b *Builder) batch() int {
	if b.Batch > 0 {
		return b.Batch
	}
	return DefaultBatch
}

// Run builds continuously until ctx is done. The first pass is immediate:
// after a restart — and after the migration that seeds every existing
// (source, day) as stale — the Overview is slow until the rollup is built,
// so there is nothing to gain by waiting out a tick.
func (b *Builder) Run(ctx context.Context) {
	t := time.NewTicker(b.interval())
	defer t.Stop()
	for {
		if err := b.Drain(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("cost rollup", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// maxBatchesPerDrain bounds one drain. A partition a usage write re-marks
// mid-build comes back stale, so a ledger under continuous collection can
// hand the builder work forever; the bound hands the tick back instead of
// spinning, and the next tick picks up where this one stopped.
const maxBatchesPerDrain = 64

// Drain builds batches until nothing is stale, ctx is done, the bound above
// is reached, or a pass makes no progress. A pass that builds a full batch
// and still finds work is the normal shape of the first run after the
// migration.
func (b *Builder) Drain(ctx context.Context) error {
	if _, err := b.Store.PurgeOrphanCostRollup(ctx); err != nil {
		return err
	}
	for range maxBatchesPerDrain {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		built, rows, err := b.Pass(ctx)
		if err != nil {
			return err
		}
		if built > 0 {
			slog.Info("cost rollup built", "partitions", built, "rows", rows)
		}
		if built < b.batch() {
			return nil
		}
	}
	return nil
}

// Pass rebuilds at most one batch of stale partitions and returns how many
// partitions and rows it wrote.
func (b *Builder) Pass(ctx context.Context) (int, int64, error) {
	parts, err := b.Store.StaleCostRollupPartitions(ctx, b.batch())
	if err != nil {
		return 0, 0, err
	}
	var built int
	var rows int64
	for _, p := range parts {
		if ctx.Err() != nil {
			return built, rows, ctx.Err()
		}
		n, fresh, err := b.Store.BuildCostRollupPartition(ctx, p)
		if err != nil {
			return built, rows, err
		}
		rows += n
		built++
		b.inc("chargeback_cost_rollup_partitions_built_total", "Rollup partitions rebuilt", 1)
		b.inc("chargeback_cost_rollup_rows_written_total", "Rollup rows written", float64(n))
		if !fresh {
			// A usage write landed in the partition mid-build; it stays
			// stale and the next pass rebuilds it. Counted, because a
			// partition that races on every pass is a busy source, not a bug.
			b.inc("chargeback_cost_rollup_rebuild_races_total", "Partitions a concurrent usage write re-marked mid-build", 1)
		}
	}
	if b.Metrics != nil {
		if st, err := b.Store.CostRollupStatus(ctx); err == nil {
			b.Metrics.Set("chargeback_cost_rollup_partitions", "Rollup partitions", nil, float64(st.Partitions))
			b.Metrics.Set("chargeback_cost_rollup_partitions_stale", "Rollup partitions awaiting a rebuild", nil, float64(st.Stale))
			b.Metrics.Set("chargeback_cost_rollup_rows", "Rows in the rollup ledger", nil, float64(st.Rows))
		}
	}
	return built, rows, nil
}

func (b *Builder) inc(name, help string, v float64) {
	if b.Metrics != nil {
		b.Metrics.Inc(name, help, nil, v)
	}
}
