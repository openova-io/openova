package harness

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// WriterConfig controls the writer goroutine that drives load onto
// the primary during phase 1. Defaults match the canonical D31 plan:
// 1,000,000 rows, 8 parallel workers, 1KB payload each.
type WriterConfig struct {
	Table       string        // default: regression_d31_counter
	TargetRows  int64         // default: 1_000_000
	Workers     int           // default: 8
	PayloadSize int           // default: 1024 bytes; 0 disables payload column
	BatchSize   int // rows per INSERT statement; default 1000
}

// Defaults returns the canonical D31 WriterConfig per DESIGN.md
// §"Test fixture / Test phases / Write 1M rows".
func DefaultWriterConfig() WriterConfig {
	return WriterConfig{
		Table:       "regression_d31_counter",
		TargetRows:  1_000_000,
		Workers:     8,
		PayloadSize: 1024,
		BatchSize:   1000,
	}
}

// WriterResult is what the writer goroutine reports back when it's
// done OR when the harness signals region-kill. AckedRows is the
// authoritative floor for the post-failover gap check.
type WriterResult struct {
	AckedRows int64
	Errors    int64
}

// SchemaSQL is the DDL the harness applies before the writer phase.
// Schema rationale (per DESIGN.md): BIGSERIAL gives a dense monotonic
// id sequence that the gap detector can verify post-promotion. The
// payload column is bytea so each row is large enough that WAL
// pressure is meaningful (1KB × 1M = 1GB of WAL).
const SchemaSQL = `
CREATE TABLE IF NOT EXISTS regression_d31_counter (
    id      BIGSERIAL PRIMARY KEY,
    payload BYTEA,
    written_at TIMESTAMPTZ DEFAULT now()
);
TRUNCATE TABLE regression_d31_counter RESTART IDENTITY;
`

// RunWriter spawns `cfg.Workers` goroutines that INSERT batches into
// `cfg.Table` against `conn` until either `cfg.TargetRows` rows have
// been ACK'd OR `ctx` is cancelled (the harness cancels right before
// it triggers the region-kill). Returns the actual ACK count — that
// number is the floor every post-failover check must beat.
//
// Each worker constructs a single INSERT statement with BatchSize
// rows of `(DEFAULT, $1)` placeholders so the BIGSERIAL fills the
// id column monotonically and the workers don't fight over it. The
// per-batch payload is a fixed `cfg.PayloadSize`-byte blob — content
// doesn't matter, only WAL volume.
func RunWriter(ctx context.Context, d *Driver, conn ConnInfo, cfg WriterConfig) WriterResult {
	if cfg.Table == "" {
		cfg.Table = "regression_d31_counter"
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 8
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 1000
	}
	if cfg.TargetRows <= 0 {
		cfg.TargetRows = 1_000_000
	}

	// Shared ACK counter — every successful batch bumps it
	// atomically. The harness reads this AFTER cancelling the ctx
	// to learn the floor.
	var acked int64
	var errs int64

	// Payload blob — same for every row; we're not testing storage
	// content, just WAL volume + replication fidelity.
	payloadHex := ""
	if cfg.PayloadSize > 0 {
		// Construct a hex-escaped bytea literal of the requested size.
		// 2 hex chars per byte; rough but fine — close enough to the
		// PayloadSize target for the WAL-pressure intent.
		payloadHex = "\\x" + strings.Repeat("ab", cfg.PayloadSize)
	}

	// Build the batch INSERT statement once: one VALUES row per
	// batch slot. We do NOT use prepared statements — the harness
	// is a one-shot orchestrator, and shelling psql per batch is
	// the simplest hermetic shape.
	batchSQL := "BEGIN; "
	for i := 0; i < cfg.BatchSize; i++ {
		if payloadHex == "" {
			batchSQL += fmt.Sprintf("INSERT INTO %s (payload) VALUES (NULL); ", cfg.Table)
		} else {
			batchSQL += fmt.Sprintf("INSERT INTO %s (payload) VALUES ('%s'); ", cfg.Table, payloadHex)
		}
	}
	batchSQL += "COMMIT;"

	var wg sync.WaitGroup
	for w := 0; w < cfg.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if ctx.Err() != nil {
					return
				}
				// Atomically reserve a batch slot. If reserved beyond
				// TargetRows, exit cleanly.
				if atomic.LoadInt64(&acked) >= cfg.TargetRows {
					return
				}
				if err := d.PsqlExec(ctx, conn, batchSQL); err != nil {
					atomic.AddInt64(&errs, 1)
					// Don't tight-loop on persistent errors — sleep
					// a bit. Region-kill happens FAST so a few error
					// retries here are harmless.
					select {
					case <-time.After(50 * time.Millisecond):
					case <-ctx.Done():
						return
					}
					continue
				}
				atomic.AddInt64(&acked, int64(cfg.BatchSize))
			}
		}()
	}

	// Wait for every worker goroutine to finish — either because the
	// TargetRows ceiling was reached or because the harness cancelled
	// the ctx mid-write to trigger the kill. Either way we return the
	// latest ACK count and let the harness assert against it.
	wg.Wait()
	return WriterResult{AckedRows: atomic.LoadInt64(&acked), Errors: atomic.LoadInt64(&errs)}
}
