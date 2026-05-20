package harness

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDefaultWriterConfig_CanonicalD31Plan(t *testing.T) {
	// Lock in the canonical D31 spec from
	// platform/cnpg-pair/DESIGN.md:234-237 ("write 1M rows, 8 workers,
	// 1KB payload"). Any change requires updating DESIGN.md too.
	cfg := DefaultWriterConfig()
	if cfg.TargetRows != 1_000_000 {
		t.Fatalf("expected 1M rows, got %d", cfg.TargetRows)
	}
	if cfg.Workers != 8 {
		t.Fatalf("expected 8 workers, got %d", cfg.Workers)
	}
	if cfg.PayloadSize != 1024 {
		t.Fatalf("expected 1KB payload, got %d", cfg.PayloadSize)
	}
	if cfg.Table != "regression_d31_counter" {
		t.Fatalf("expected canonical table name, got %s", cfg.Table)
	}
}

func TestSchemaSQL_CreatesCanonicalTable(t *testing.T) {
	// DESIGN.md:235 fixes the schema as
	// "(id BIGSERIAL PRIMARY KEY, payload BYTEA)". The gap detector
	// relies on BIGSERIAL allocating IDs densely from 1 — lock it in.
	if !strings.Contains(SchemaSQL, "BIGSERIAL PRIMARY KEY") {
		t.Fatalf("schema missing BIGSERIAL PRIMARY KEY: %s", SchemaSQL)
	}
	if !strings.Contains(SchemaSQL, "BYTEA") {
		t.Fatalf("schema missing BYTEA payload: %s", SchemaSQL)
	}
	if !strings.Contains(SchemaSQL, "TRUNCATE") {
		t.Fatalf("schema must TRUNCATE on every run to reset BIGSERIAL: %s", SchemaSQL)
	}
}

func TestRunWriter_StopsOnContextCancel(t *testing.T) {
	// The harness cancels the writer's ctx right before issuing the
	// region-kill — verify the workers respect cancellation and that
	// the returned ACK count is meaningful (not zero, not negative).
	var batches int64
	d := &Driver{
		R: runnerFunc(func(ctx context.Context, _ string, _ []string, _ map[string]string, _ string) (string, string, error) {
			atomic.AddInt64(&batches, 1)
			return "", "", nil
		}),
		Psql: "psql",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	cfg := WriterConfig{
		Table: "t", TargetRows: 1_000_000_000, Workers: 4, BatchSize: 100, PayloadSize: 0,
	}
	res := RunWriter(ctx, d, ConnInfo{Host: "h", Port: 5432, Database: "d", User: "u"}, cfg)
	if res.AckedRows < int64(cfg.BatchSize) {
		t.Fatalf("expected at least one batch worth of ACKs, got %d (batches=%d)", res.AckedRows, batches)
	}
	if res.AckedRows%int64(cfg.BatchSize) != 0 {
		t.Fatalf("AckedRows must be a multiple of BatchSize, got %d", res.AckedRows)
	}
}

func TestRunWriter_TargetRowsCeiling(t *testing.T) {
	// With a tiny TargetRows the workers should exit cleanly once
	// the ceiling is hit, well before any context cancel.
	d := &Driver{
		R: runnerFunc(func(_ context.Context, _ string, _ []string, _ map[string]string, _ string) (string, string, error) {
			return "", "", nil
		}),
		Psql: "psql",
	}
	cfg := WriterConfig{Table: "t", TargetRows: 500, Workers: 2, BatchSize: 100, PayloadSize: 0}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res := RunWriter(ctx, d, ConnInfo{Host: "h", Port: 5432, Database: "d", User: "u"}, cfg)
	if res.AckedRows < 500 {
		t.Fatalf("expected ≥500 ACKs (ceiling), got %d", res.AckedRows)
	}
}
