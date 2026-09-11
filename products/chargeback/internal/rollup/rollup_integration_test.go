package rollup_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/rollup"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// The builder (#6926, DESIGN.md §20.6). What it owes: drain everything the
// triggers marked, in batches, leaving nothing stale; sweep the rows of a
// source that is gone; and keep the counters an operator reads honest.

func day(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC) }

// seedDays writes one hourly record per hour for `days` days on one source.
func seedDays(t *testing.T, st *store.Store, days int) (store.Customer, store.CostSource) {
	t.Helper()
	ctx := context.Background()
	book, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "list", Currency: "OMR", AnnualDivisor: 8760, BillStopped: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, book.ID, []store.PriceItem{{SKU: "eip", Unit: "hour", UnitPrice: "0.02"}}, true); err != nil {
		t.Fatal(err)
	}
	cust, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "acme", Name: "Acme", AdminEmail: "a@acme.example", StartDate: "2026-08-01"})
	if err != nil {
		t.Fatal(err)
	}
	src, _, err := st.UpsertSource(ctx, cust.ID, "huawei-project", "me-east-1", "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSourcePriceBook(ctx, src.ID, book.ID); err != nil {
		t.Fatal(err)
	}
	lb, _ := json.Marshal(map[string]any{"name": "1.2.3.4"})
	var recs []store.UsageRecord
	for d := 1; d <= days; d++ {
		for h := range 24 {
			at := day(2026, 9, d).Add(time.Duration(h) * time.Hour)
			recs = append(recs, store.UsageRecord{CustomerID: cust.ID, SourceID: src.ID, ResourceID: "eip-1",
				ResourceKind: "eip", SKU: "eip", Quantity: "1.000000", Unit: "hour",
				WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-1", Labels: lb})
		}
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	return cust, src
}

func TestIntegrationBuilderDrainsEveryMarkedPartition(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	_, src := seedDays(t, st, 12)

	reg := metrics.New()
	// A batch smaller than the work, so Drain has to loop rather than
	// leaving the tail of the ledger stale after one pass.
	b := &rollup.Builder{Store: st, Batch: 5, Metrics: reg}
	if err := b.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	st0, err := st.CostRollupStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st0.Partitions != 12 || st0.Stale != 0 {
		t.Fatalf("status = %+v, want 12 partitions and none stale", st0)
	}
	if st0.Rows != 12 {
		t.Fatalf("12 days of one resource on one SKU should roll up to 12 rows, got %d", st0.Rows)
	}
	if st0.OldestDay == nil || *st0.OldestDay != "2026-09-01" || st0.NewestDay == nil || *st0.NewestDay != "2026-09-12" {
		t.Fatalf("day range = %v..%v", st0.OldestDay, st0.NewestDay)
	}
	if got := reg.Get("chargeback_cost_rollup_partitions_built_total", nil); got != 12 {
		t.Fatalf("built counter = %v", got)
	}
	if got := reg.Get("chargeback_cost_rollup_partitions_stale", nil); got != 0 {
		t.Fatalf("stale gauge = %v", got)
	}

	// A second drain with nothing marked is a no-op.
	if err := b.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if got := reg.Get("chargeback_cost_rollup_partitions_built_total", nil); got != 12 {
		t.Fatalf("an idle drain rebuilt %v partitions", got-12)
	}

	// Touching one day marks exactly that partition, and the next drain
	// rebuilds exactly that one.
	if _, err := st.DeleteUsageInRange(ctx, src.ID, "eip-1", day(2026, 9, 4), day(2026, 9, 5)); err != nil {
		t.Fatal(err)
	}
	if err := b.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if got := reg.Get("chargeback_cost_rollup_partitions_built_total", nil); got != 13 {
		t.Fatalf("built counter after one re-marked day = %v, want 13", got)
	}
	st1, err := st.CostRollupStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st1.Stale != 0 || st1.Rows != 11 {
		t.Fatalf("status after the delete = %+v, want no stale partitions and 11 rows", st1)
	}
}

func TestIntegrationBuilderSweepsADeletedSource(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	_, src := seedDays(t, st, 3)
	b := &rollup.Builder{Store: st}
	if err := b.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSource(ctx, src.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.CostRollupStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Partitions != 0 || got.Rows != 0 {
		t.Fatalf("the deleted source left %+v behind", got)
	}
}
