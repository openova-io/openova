package store_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// The batch (#6867, store/costbatch.go) against a database: several
// questions answered from one `f` built for the hull of their windows are
// the same documents, byte for byte, as each question answered from a `u`
// of its own — across the rollup/live seam, with the rollup off, with a
// filter that forces a second statement and an hourly chart that forces a
// live read. And the count of what it asked: one freshness read plus one
// statement per filter set.

func TestIntegrationCostBatchAnswersAsSeparateReads(t *testing.T) {
	st, log := testdb.OpenCounted(t)
	ctx := context.Background()
	fx := seedRollupLedger(t, st)
	// So every branch has rows: a SKU the cloud book does not price
	// (unpriced) and a source on a book whose currency has no rate
	// (unconverted).
	eur, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "eur", Currency: "EUR", AnnualDivisor: 8760, BillStopped: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, eur.ID, []store.PriceItem{{SKU: "eip", Unit: "hour", UnitPrice: "0.03"}}, true); err != nil {
		t.Fatal(err)
	}
	srcEUR, _, err := st.UpsertSource(ctx, fx.bravo.ID, "huawei-project", "eu-west-0", "proj-eur")
	if err != nil {
		t.Fatal(err)
	}
	assignBook(t, st, srcEUR.ID, eur.ID)
	lb, _ := json.Marshal(map[string]any{"name": "extra", "status": "ACTIVE"})
	at := fx.from.AddDate(0, 0, 1).Add(2 * time.Hour)
	if _, err := st.UpsertUsage(ctx, []store.UsageRecord{
		{CustomerID: fx.bravo.ID, SourceID: srcEUR.ID, ResourceID: "eip-eur", ResourceKind: "eip", SKU: "eip", Unit: "hour", Quantity: "1", WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "eu-west-0", Labels: lb},
		{CustomerID: fx.acme.ID, SourceID: fx.cloudA.ID, ResourceID: "nat-1", ResourceKind: "nat", SKU: "nat.small", Unit: "hour", Quantity: "1", WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-1", Labels: lb},
	}); err != nil {
		t.Fatal(err)
	}
	buildRollup(t, st)
	// Re-open one day of A's cloud source: served live, its neighbours from
	// the rollup, in every window that spans it.
	if _, err := st.UpsertUsage(ctx, []store.UsageRecord{{CustomerID: fx.acme.ID, SourceID: fx.cloudA.ID, ResourceID: "vm-late", ResourceKind: "ecs",
		SKU: "ecs.m7n.xlarge.8", Unit: "instance-hour", Quantity: "1", WindowStart: fx.from.AddDate(0, 0, 4).Add(3 * time.Hour),
		WindowEnd: fx.from.AddDate(0, 0, 4).Add(4 * time.Hour), Region: "me-east-1", Labels: lb}}); err != nil {
		t.Fatal(err)
	}
	if stale := staleDays(t, st); len(stale) != 1 {
		t.Fatalf("stale days after the late record = %v, want exactly the re-opened one", stale)
	}
	base := func(from, to time.Time, gran, groupBy string, limit int) store.CostQuery {
		return store.CostQuery{From: from, To: to, Granularity: gran, GroupBy: groupBy, Metric: "cost", Limit: limit}
	}
	// The overview's six shapes over the fixture's days, then a filtered
	// question (its own filter set) and an hourly one (its own grain).
	questions := []store.CostQuery{
		base(fx.from, fx.to, "day", "none", 0),
		base(fx.from.AddDate(0, 0, 2), fx.to.AddDate(0, 0, -1), "day", "none", 0),
		base(fx.from.AddDate(0, -1, 0), fx.from, "month", "none", 0),
		base(fx.from.AddDate(0, -1, 0), fx.from.AddDate(0, -1, 6), "month", "none", 0),
		base(fx.from, fx.to, "month", "customer", 1),
		base(fx.from, fx.to, "month", "kind", 10),
		{From: fx.from, To: fx.to, Granularity: "day", GroupBy: "sku", Metric: "cost", Include: map[string][]string{"kind": {"ecs"}}},
		base(fx.from.AddDate(0, 0, 1), fx.from.AddDate(0, 0, 3), "hour", "none", 0),
	}
	run := func(label string, wantLedger int) {
		t.Helper()
		want := make([]store.ExploreResult, len(questions))
		for i, q := range questions {
			res, err := st.Explore(ctx, store.OperatorScope, q)
			if err != nil {
				t.Fatalf("%s: question %d: %v", label, i, err)
			}
			want[i] = res
		}
		wantDaily, err := st.DailyCostByCustomerKind(ctx, store.OperatorScope, "", fx.from, fx.to)
		if err != nil {
			t.Fatal(err)
		}
		b := st.NewCostBatch()
		got := make([]*store.ExploreResult, len(questions))
		for i, q := range questions {
			if got[i], err = b.Explore(store.OperatorScope, q); err != nil {
				t.Fatalf("%s: queue %d: %v", label, i, err)
			}
		}
		gotDaily, err := b.DailyCostByCustomerKind(store.OperatorScope, "", fx.from, fx.to)
		if err != nil {
			t.Fatal(err)
		}
		log.Reset()
		if err := b.Run(ctx); err != nil {
			t.Fatalf("%s: run: %v", label, err)
		}
		if reads := log.Ledger(); len(reads) != wantLedger {
			var heads []string
			for _, q := range reads {
				heads = append(heads, strings.SplitN(strings.TrimSpace(q), "\n", 2)[0])
			}
			t.Fatalf("%s: batch issued %d ledger statements, want %d:\n  %s", label, len(reads), wantLedger, strings.Join(heads, "\n  "))
		}
		for i := range questions {
			if !reflect.DeepEqual(want[i], *got[i]) {
				w, _ := json.MarshalIndent(want[i], "", " ")
				g, _ := json.MarshalIndent(*got[i], "", " ")
				t.Fatalf("%s: question %d differs in the batch\nseparate: %s\nbatch:    %s", label, i, w, g)
			}
		}
		if !reflect.DeepEqual(wantDaily, *gotDaily) {
			t.Fatalf("%s: daily series differs in the batch:\n%v\n%v", label, wantDaily, *gotDaily)
		}
		if got[0].Total.Current == "0" || got[0].Total.Current == "" || len(got[0].Unpriced) == 0 || len(got[0].Unconverted) == 0 {
			t.Fatalf("%s: the fixture no longer exercises every branch: %+v", label, got[0].Total)
		}
	}
	// One freshness read; the operator's day-grain statement, the filtered
	// one and the hourly chart's.
	run("rollup on", 4)
	st.SetCostRollupEnabled(false)
	run("rollup off", 3)
}
