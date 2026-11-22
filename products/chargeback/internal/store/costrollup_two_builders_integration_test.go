package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// TestIntegrationTwoBuildersOfOnePartitionLeaveOneCopy: every chargeback
// replica runs a rollup builder, and on a rollout both list the same stale
// partitions in the same order. Two builds of ONE partition must leave the
// aggregate once. On main both builds deleted what they could see (nothing
// the other had inserted), both inserted, both marked the day fresh: every
// row twice, and the explorer, budgets and reports served that day at double
// quantity from then on.
//
// The interleaving is forced, not hoped for: the test holds the partition's
// state row, so each build stops at its final mark and the second build has
// run as far as it can before the first commits.
func TestIntegrationTwoBuildersOfOnePartitionLeaveOneCopy(t *testing.T) {
	st := testdb.Open(t)
	fx := seedRollupLedger(t, st)
	ctx := context.Background()
	db := st.DB()

	p := store.CostRollupPartition{SourceID: fx.cloudA.ID, Day: day(2026, 9, 3)}
	if err := db.QueryRowContext(ctx, `SELECT version FROM cost_rollup_state WHERE source_id = $1 AND day = $2`, p.SourceID, p.Day).Scan(&p.Version); err != nil {
		t.Fatal(err)
	}

	hold, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.Rollback() }()
	if _, err := hold.ExecContext(ctx, `SELECT 1 FROM cost_rollup_state WHERE source_id = $1 AND day = $2 FOR UPDATE`, p.SourceID, p.Day); err != nil {
		t.Fatal(err)
	}

	type built struct {
		rows  int64
		fresh bool
		err   error
	}
	done := make(chan built, 2)
	for range 2 {
		go func() {
			rows, fresh, err := st.BuildCostRollupPartition(ctx, p)
			done <- built{rows, fresh, err}
		}()
	}
	// Both builds are parked on a lock: the first at its mark (the held state
	// row), the second at its mark too on main, at the partition lock here.
	deadline := time.Now().Add(15 * time.Second)
	for {
		var waiting int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the two builds never both waited (waiting = %d)", waiting)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := hold.Commit(); err != nil {
		t.Fatal(err)
	}
	var want int64
	for range 2 {
		b := <-done
		if b.err != nil {
			t.Fatal(b.err)
		}
		if b.rows == 0 {
			t.Fatal("a build aggregated no rows; the fixture has usage on 3 September")
		}
		want = b.rows
	}
	var have int64
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM cost_usage_daily WHERE source_id = $1 AND day = $2`, p.SourceID, p.Day).Scan(&have); err != nil {
		t.Fatal(err)
	}
	if have != want {
		t.Fatalf("the partition holds %d rollup rows after two concurrent builds, want %d (one copy)", have, want)
	}
	var fresh bool
	if err := db.QueryRowContext(ctx, `SELECT built_version = version FROM cost_rollup_state WHERE source_id = $1 AND day = $2`, p.SourceID, p.Day).Scan(&fresh); err != nil {
		t.Fatal(err)
	}
	if !fresh {
		t.Fatal("the partition is still stale after the builds")
	}
}
