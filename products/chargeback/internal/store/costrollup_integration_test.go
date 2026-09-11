package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// The daily cost rollup (#6926, DESIGN.md §20).
//
// The bar these tests hold is EXACTNESS before speed: a rollup that
// disagrees with the live rating by a micro-unit is worse than a slow page,
// because the number an operator reads would stop being the number an
// invoice charges. So the centrepiece here is not "the rollup is fast" — it
// is "every figure the explorer reports is byte-identical whether the window
// came from the rollup, from the live ledger, or half from each".

// ---------------------------------------------------------------------------
// the fixture
// ---------------------------------------------------------------------------

type rollupFixture struct {
	acme, bravo store.Customer
	cloudA      store.CostSource
	platformA   store.CostSource
	cloudB      store.CostSource
	internal    store.CostSource
	omrBook     string
	usdBook     string
	from, to    time.Time
}

// seedRollupLedger writes a deliberately awkward September: two customers on
// books in two currencies (so cost_base is a real conversion, not a copy),
// an unpriced platform meter, the Sovereign's own internal source, an
// instance that is SHUTOFF for part of a day (so the stopped-instance
// policy splits a day's records into two label groups), non-round
// quantities, resource tags and a cost centre attributed by a tag rule.
//
// Every one of those is a thing the rollup could get wrong by aggregating:
// a label it grouped away, a policy branch it flattened, a currency it
// converted once instead of per row.
func seedRollupLedger(t *testing.T, st *store.Store) rollupFixture {
	t.Helper()
	ctx := context.Background()
	omr, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "cloud OMR", Currency: "OMR", AnnualDivisor: 8760, BillStopped: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, omr.ID, []store.PriceItem{
		{SKU: "ecs.m7n.xlarge.8", Unit: "instance-hour", UnitPrice: "0.37"},
		{SKU: "evs.ssd.gb", Unit: "gb-hour", UnitPrice: "0.00137"},
	}, true); err != nil {
		t.Fatal(err)
	}
	usd, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "cloud USD", Currency: "USD", AnnualDivisor: 8760, BillStopped: "compute"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, usd.ID, []store.PriceItem{
		{SKU: "eip", Unit: "hour", UnitPrice: "0.0213"},
	}, true); err != nil {
		t.Fatal(err)
	}
	// 1 OMR buys 2.6 USD. A rate that is not a power of ten is the point:
	// the conversion is a division whose quotient does not terminate.
	if _, err := st.PutCurrencyRate(ctx, "USD", "2.6", "manual"); err != nil {
		t.Fatal(err)
	}
	acme, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "acme", Name: "Acme", AdminEmail: "a@acme.example", StartDate: "2026-08-01"})
	if err != nil {
		t.Fatal(err)
	}
	bravo, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "bravo", Name: "Bravo", AdminEmail: "b@bravo.example", StartDate: "2026-08-01"})
	if err != nil {
		t.Fatal(err)
	}
	cloudA, _, err := st.UpsertSource(ctx, acme.ID, "huawei-project", "me-east-1", "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	platformA, _, err := st.UpsertSource(ctx, acme.ID, "openova-org", "", "acme")
	if err != nil {
		t.Fatal(err)
	}
	cloudB, _, err := st.UpsertSource(ctx, bravo.ID, "huawei-project", "me-east-2", "proj-b")
	if err != nil {
		t.Fatal(err)
	}
	internal, _, err := st.EnsureInternalSource(ctx, "sovereign")
	if err != nil {
		t.Fatal(err)
	}
	assignBook(t, st, cloudA.ID, omr.ID)
	assignBook(t, st, cloudB.ID, usd.ID)

	// A cost centre attributed by a tag rule, so the cost_centre dimension is
	// resolved from labels the rollup carries rather than from a column it
	// stores.
	cc, err := st.CreateCostCentre(ctx, acme.ID, store.CostCentreInput{Code: "CC-WEB", Name: "Web"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCostCentreRule(ctx, acme.ID, store.CostCentreRuleInput{
		CostCentreRef: store.CostCentreRef{CostCentreID: cc.ID}, TagKey: "team", TagValue: "a",
	}); err != nil {
		t.Fatal(err)
	}

	var recs []store.UsageRecord
	rec := func(customerID, sourceID, res, kind, sku, unit string, qty string, at time.Time, region string, labels map[string]any) {
		lb, _ := json.Marshal(labels)
		recs = append(recs, store.UsageRecord{CustomerID: customerID, SourceID: sourceID, ResourceID: res, ResourceKind: kind,
			SKU: sku, Quantity: store.Decimal(qty), Unit: unit, WindowStart: at, WindowEnd: at.Add(time.Hour), Region: region, Labels: lb})
	}
	for d := 1; d <= 10; d++ {
		for h := 0; h < 24; h++ {
			at := day(2026, 9, d).Add(time.Duration(h) * time.Hour)
			// A quantity that is not 1: the sum over a day is not a round
			// number and the product with a 5-decimal price is not either.
			rec(acme.ID, cloudA.ID, "vm-1", "ecs", "ecs.m7n.xlarge.8", "instance-hour", "1.000000", at, "me-east-1",
				map[string]any{"name": "web-1", "status": "ACTIVE", "tier": "organization", "enterprise_project": "ep-1", "tags": map[string]string{"team": "a", "env": "prod"}})
			rec(acme.ID, cloudA.ID, "vol-1", "evs", "evs.ssd.gb", "gb-hour", "137.700000", at, "me-east-1",
				map[string]any{"name": "vol-1", "server_status": "ACTIVE", "tags": map[string]string{"team": "b"}})
			// vm-2 runs the first 9 hours of each day and is SHUTOFF for the
			// rest: two label groups inside ONE day, and the stopped hours
			// are waived by the OMR book's `none` policy.
			status := "ACTIVE"
			if h >= 9 {
				status = "SHUTOFF"
			}
			rec(acme.ID, cloudA.ID, "vm-2", "ecs", "ecs.m7n.xlarge.8", "instance-hour", "1.000000", at, "me-east-1",
				map[string]any{"name": "batch-2", "status": status, "tags": map[string]string{"team": "a"}})
			// Unpriced platform meter on a source with no book.
			rec(acme.ID, platformA.ID, "ns/pod-1", "k8s-pod", "k8s.vcpu", "vcpu-hour", "0.333333", at, "",
				map[string]any{"name": "pod-1", "namespace": "ns", "tier": "organization", "tags": map[string]string{"app": "wordpress"}})
			// A sampled measurement, which must never be rated and must never
			// reach the rollup.
			rec(acme.ID, cloudA.ID, "vm-1", "ecs", "ecs.cpu_util", "pct-hour-avg", "42.500000", at, "me-east-1",
				map[string]any{"name": "web-1"})
			// Customer B on the USD book: the conversion path.
			rec(bravo.ID, cloudB.ID, "eip-1", "eip", "eip", "hour", "1.000000", at, "me-east-2",
				map[string]any{"name": "1.2.3.4", "tags": map[string]string{"team": "zulu"}})
			// The Sovereign's own footprint: no customer, internal source.
			rec("", internal.ID, "sov/pod-9", "k8s-pod", "k8s.vcpu", "vcpu-hour", "0.750000", at, "",
				map[string]any{"name": "pod-9", "namespace": "openova-system"})
		}
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	return rollupFixture{acme: acme, bravo: bravo, cloudA: cloudA, platformA: platformA, cloudB: cloudB,
		internal: internal, omrBook: omr.ID, usdBook: usd.ID, from: day(2026, 9, 1), to: day(2026, 9, 11)}
}

// buildRollup builds every stale partition and returns how many it built.
func buildRollup(t *testing.T, st *store.Store) int {
	t.Helper()
	ctx := context.Background()
	built := 0
	for range 20 {
		parts, err := st.StaleCostRollupPartitions(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(parts) == 0 {
			return built
		}
		for _, p := range parts {
			if _, fresh, err := st.BuildCostRollupPartition(ctx, p); err != nil {
				t.Fatal(err)
			} else if !fresh {
				t.Fatalf("partition %s/%s did not take the build mark with no concurrent writer", p.SourceID, p.Day)
			}
			built++
		}
	}
	t.Fatal("rollup never became fresh")
	return built
}

// staleDays counts the partitions of one UTC day that are waiting to build.
func staleDays(t *testing.T, st *store.Store) map[string]int {
	t.Helper()
	parts, err := st.StaleCostRollupPartitions(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, p := range parts {
		out[p.Day.Format("2006-01-02")]++
	}
	return out
}

// ---------------------------------------------------------------------------
// the exactness bar
// ---------------------------------------------------------------------------

// costSurfaces is every read the rollup can serve, as (name, func) pairs.
// Each returns a value the test compares byte for byte: encoding to JSON is
// how the comparison reaches the exact Decimal STRING the API puts on the
// wire, so "1.000000" and "1.0" would not pass for each other.
func costSurfaces(st *store.Store, fx rollupFixture) []struct {
	name string
	read func(context.Context) (any, error)
} {
	win := func(gran, groupBy string) store.CostQuery {
		return store.CostQuery{From: fx.from, To: fx.to, Granularity: gran, GroupBy: groupBy, Metric: "cost"}
	}
	type surface = struct {
		name string
		read func(context.Context) (any, error)
	}
	explore := func(name string, scope store.Scope, q store.CostQuery) surface {
		return surface{name, func(ctx context.Context) (any, error) { return st.Explore(ctx, scope, q) }}
	}
	byCentre := win("month", store.CostCentreDimension)
	byTag := win("day", "tag:team")
	usage := win("day", "sku")
	usage.Metric = "usage"
	limited := win("month", "resource")
	limited.Limit = 2
	filtered := win("day", "kind")
	filtered.Include = map[string][]string{"region": {"me-east-1"}}
	filtered.Exclude = map[string][]string{"sku": {"evs.ssd.gb"}}
	partWindow := win("day", "customer")
	// A window that starts and ends mid-day: the rollup may serve only the
	// whole days between, and the part-days must come from the live ledger.
	partWindow.From = fx.from.Add(11 * time.Hour)
	partWindow.To = fx.to.Add(-7 * time.Hour)
	return []surface{
		explore("explore/day/none", store.OperatorScope, win("day", "none")),
		explore("explore/day/customer", store.OperatorScope, win("day", "customer")),
		explore("explore/day/kind", store.OperatorScope, win("day", "kind")),
		explore("explore/day/sku", store.OperatorScope, win("day", "sku")),
		explore("explore/day/source", store.OperatorScope, win("day", "source")),
		explore("explore/day/region", store.OperatorScope, win("day", "region")),
		explore("explore/day/tier", store.OperatorScope, win("day", "tier")),
		explore("explore/day/namespace", store.OperatorScope, win("day", "namespace")),
		explore("explore/day/enterprise_project", store.OperatorScope, win("day", "enterprise_project")),
		explore("explore/day/resource", store.OperatorScope, win("day", "resource")),
		explore("explore/month/cost_centre", store.OperatorScope, byCentre),
		explore("explore/day/tag:team", store.OperatorScope, byTag),
		explore("explore/usage/sku", store.OperatorScope, usage),
		explore("explore/limit/resource", store.OperatorScope, limited),
		explore("explore/filtered/kind", store.OperatorScope, filtered),
		explore("explore/part-window/customer", store.OperatorScope, partWindow),
		explore("explore/scoped/acme", store.CustomerScope(fx.acme.ID), win("day", "kind")),
		explore("explore/scoped/bravo", store.CustomerScope(fx.bravo.ID), win("day", "kind")),
		{"dimensions", func(ctx context.Context) (any, error) {
			return st.DimensionValues(ctx, store.OperatorScope, byTag)
		}},
		{"tag_keys", func(ctx context.Context) (any, error) {
			return st.TagKeys(ctx, store.OperatorScope, win("day", "none"))
		}},
		{"allocation", func(ctx context.Context) (any, error) {
			return st.Allocation(ctx, store.OperatorScope, fx.from, fx.to)
		}},
		{"daily_cost_by_customer_kind", func(ctx context.Context) (any, error) {
			return st.DailyCostByCustomerKind(ctx, store.OperatorScope, "", fx.from, fx.to)
		}},
		{"unpriced_by_customer", func(ctx context.Context) (any, error) {
			return st.UnpricedUsageByCustomer(ctx, store.OperatorScope, "", fx.from, fx.to)
		}},
		{"price_book_coverage", func(ctx context.Context) (any, error) {
			return st.PriceBookCoverage(ctx, fx.omrBook, fx.from, fx.to)
		}},
	}
}

func readSurfaces(t *testing.T, st *store.Store, fx rollupFixture) map[string]string {
	t.Helper()
	ctx := context.Background()
	out := map[string]string{}
	for _, s := range costSurfaces(st, fx) {
		v, err := s.read(ctx)
		if err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s: marshal: %v", s.name, err)
		}
		out[s.name] = string(b)
	}
	return out
}

func diffSurfaces(t *testing.T, what string, live, rolled map[string]string) {
	t.Helper()
	for name, want := range live {
		got, ok := rolled[name]
		if !ok {
			t.Fatalf("%s: %s missing", what, name)
		}
		if got != want {
			t.Errorf("%s: %s differs\n rollup: %s\n   live: %s", what, name, got, want)
		}
	}
}

// TestIntegrationRollupRatesExactlyAsTheLiveLedger is the centrepiece.
//
// Every cost surface is read twice over the SAME ledger — once with nothing
// built, so every window is rated over the hourly records, and once with the
// rollup built, so the whole days come from cost_usage_daily — and the two
// answers must be identical to the last digit, on a multi-currency ledger
// with a stopped-instance policy, an unpriced meter, tags and a cost centre.
func TestIntegrationRollupRatesExactlyAsTheLiveLedger(t *testing.T) {
	st := testdb.Open(t)
	fx := seedRollupLedger(t, st)
	ctx := context.Background()

	// Nothing is built yet: the triggers marked every partition when the
	// seed wrote it, so every window is served live. That is the control.
	if parts, err := st.StaleCostRollupPartitions(ctx, 0); err != nil {
		t.Fatal(err)
	} else if len(parts) == 0 {
		t.Fatal("the seed wrote usage and left no partition marked: the invalidation triggers are not firing")
	}
	live := readSurfaces(t, st, fx)

	built := buildRollup(t, st)
	// 4 sources × 10 days.
	if built != 40 {
		t.Fatalf("built %d partitions, want 40", built)
	}
	// The rollup must actually be the thing answering, or this test proves
	// nothing: a whole-day window over built partitions has no live range.
	assertRollupServes(t, st, fx.from, fx.to)

	rolled := readSurfaces(t, st, fx)
	diffSurfaces(t, "rollup vs live", live, rolled)

	// And the ledger it aggregated is the ledger it was built from: the
	// sampled measurement is a metric, not a meter, and never reaches it.
	var metricRows int
	if err := st.DB().QueryRowContext(ctx, `SELECT count(*) FROM cost_usage_daily WHERE sku = $1`, store.SKUCPUUtil).Scan(&metricRows); err != nil {
		t.Fatal(err)
	}
	if metricRows != 0 {
		t.Fatalf("the rollup stored %d rows of a sampled measurement", metricRows)
	}
}

// assertRollupServes fails unless the rollup holds every usage record of the
// window — half the vacuity guard for the comparison above, since a rollup
// that had dropped rows would otherwise be compared only with itself. The
// other half is TestIntegrationRollupPerturbationIsCaught, which proves the
// reader is genuinely reading these rows and not silently falling back.
func assertRollupServes(t *testing.T, st *store.Store, from, to time.Time) {
	t.Helper()
	ctx := context.Background()
	var live, rolled int
	if err := st.DB().QueryRowContext(ctx, `SELECT count(*) FROM usage_records
		WHERE window_start >= $1 AND window_start < $2 AND `+"sku NOT IN ('"+store.SKUCPUUtil+"', '"+store.SKUEIPTrafficObserved+"')",
		from, to).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT COALESCE(sum(records), 0) FROM cost_usage_daily WHERE day >= $1 AND day < $2`, from, to).Scan(&rolled); err != nil {
		t.Fatal(err)
	}
	if live == 0 || rolled != live {
		t.Fatalf("the rollup covers %d of the window's %d usage records: the comparison would be vacuous", rolled, live)
	}
}

// TestIntegrationRollupSeamNeitherDoublesNorDrops walks the boundary
// deliberately: a window whose middle day is stale, so it is served from the
// live ledger while the days either side come from the rollup, and a window
// that starts and ends mid-day. Every figure must still equal the all-live
// answer.
func TestIntegrationRollupSeamNeitherDoublesNorDrops(t *testing.T) {
	st := testdb.Open(t)
	fx := seedRollupLedger(t, st)
	ctx := context.Background()
	live := readSurfaces(t, st, fx)
	buildRollup(t, st)

	// Re-open one day in the middle by touching a record in it: the trigger
	// marks that (source, day) and the reader must fall back to the ledger
	// for the WHOLE day, on every source, without losing the days around it.
	hole := day(2026, 9, 5)
	if _, err := st.DB().ExecContext(ctx, `UPDATE usage_records SET collected_at = now()
		WHERE source_id = $1 AND window_start >= $2 AND window_start < $3`, fx.cloudA.ID, hole, hole.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	stale := staleDays(t, st)
	if stale["2026-09-05"] != 1 || len(stale) != 1 {
		t.Fatalf("touching one day marked %v", stale)
	}

	holed := readSurfaces(t, st, fx)
	diffSurfaces(t, "rollup with a hole vs live", live, holed)

	// The seam is real: a day is covered only when NO source of it is stale,
	// which is the reader's own rule, so one marked source takes the whole
	// day back to the live ledger.
	var covered int
	if err := st.DB().QueryRowContext(ctx, `SELECT count(DISTINCT r.day) FROM cost_rollup_state r
		WHERE r.day >= $1 AND r.day < $2
		  AND NOT EXISTS (SELECT 1 FROM cost_rollup_state x WHERE x.day = r.day AND x.built_version <> x.version)`,
		fx.from, fx.to).Scan(&covered); err != nil {
		t.Fatal(err)
	}
	if covered != 9 {
		t.Fatalf("%d days fresh, want 9 — the hole is not where the test thinks", covered)
	}
}

// TestIntegrationRollupPerturbationIsCaught is the vacuity check of the
// exactness test: move ONE rollup row by one minor unit of quantity and the
// comparison must fail. A test that cannot fail proves nothing.
func TestIntegrationRollupPerturbationIsCaught(t *testing.T) {
	st := testdb.Open(t)
	fx := seedRollupLedger(t, st)
	live := readSurfaces(t, st, fx)
	buildRollup(t, st)
	if same := readSurfaces(t, st, fx); !reflect.DeepEqual(same, live) {
		t.Fatal("the unperturbed rollup already disagrees with the live ledger")
	}
	// One micro-unit — the last digit usage_records carries — on one row.
	res, err := st.DB().ExecContext(context.Background(), `UPDATE cost_usage_daily
		SET quantity = quantity + 0.000001
		WHERE day = $1 AND source_id = $2 AND resource_id = 'vm-1' AND sku = 'ecs.m7n.xlarge.8'`,
		day(2026, 9, 4), fx.cloudA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("perturbed %d rows, want 1", n)
	}
	perturbed := readSurfaces(t, st, fx)
	if reflect.DeepEqual(perturbed, live) {
		t.Fatal("a one-micro-unit perturbation of the rollup changed nothing: the exactness comparison cannot fail")
	}
	// And it is the COST that moved, not merely some label: the window total
	// of the plain day/none document differs.
	if perturbed["explore/day/none"] == live["explore/day/none"] {
		t.Fatal("the perturbation did not reach the window total")
	}
}

// ---------------------------------------------------------------------------
// invalidation
// ---------------------------------------------------------------------------

// TestIntegrationRollupInvalidationPaths walks every way the answer can
// change and checks the rollup either re-marks itself (usage changed) or
// needs no marking at all because the input is read live (anything that
// changes a price).
func TestIntegrationRollupInvalidationPaths(t *testing.T) {
	st := testdb.Open(t)
	fx := seedRollupLedger(t, st)
	ctx := context.Background()
	buildRollup(t, st)
	total := func() string {
		r, err := st.Explore(ctx, store.OperatorScope, store.CostQuery{From: fx.from, To: fx.to, Granularity: "day", GroupBy: "none"})
		if err != nil {
			t.Fatal(err)
		}
		return string(r.Total.Current)
	}
	base := total()

	// 1. A new usage record marks its own day and nothing else.
	late := day(2026, 9, 2).Add(3 * time.Hour)
	lb, _ := json.Marshal(map[string]any{"name": "web-9", "status": "ACTIVE"})
	if _, err := st.UpsertUsage(ctx, []store.UsageRecord{{CustomerID: fx.acme.ID, SourceID: fx.cloudA.ID,
		ResourceID: "vm-9", ResourceKind: "ecs", SKU: "ecs.m7n.xlarge.8", Quantity: "2.000000", Unit: "instance-hour",
		WindowStart: late, WindowEnd: late.Add(time.Hour), Region: "me-east-1", Labels: lb}}); err != nil {
		t.Fatal(err)
	}
	if s := staleDays(t, st); s["2026-09-02"] != 1 || len(s) != 1 {
		t.Fatalf("a backfilled record marked %v", s)
	}
	// The figure is right BEFORE the rebuild — the day fell back to live —
	// and stays right after it.
	withLate := total()
	if withLate == base {
		t.Fatal("a backfilled record did not change the total")
	}
	buildRollup(t, st)
	if got := total(); got != withLate {
		t.Fatalf("the rebuild changed the total: %s then %s", withLate, got)
	}

	// 1b. The collector's normal write is an UPSERT of a record that already
	// exists: INSERT ... ON CONFLICT DO UPDATE, which fires the UPDATE
	// statement trigger rather than the INSERT one. It must mark the day just
	// the same — this is the path that runs every COLLECT_INTERVAL.
	if _, err := st.UpsertUsage(ctx, []store.UsageRecord{{CustomerID: fx.acme.ID, SourceID: fx.cloudA.ID,
		ResourceID: "vm-9", ResourceKind: "ecs", SKU: "ecs.m7n.xlarge.8", Quantity: "5.000000", Unit: "instance-hour",
		WindowStart: late, WindowEnd: late.Add(time.Hour), Region: "me-east-1", Labels: lb}}); err != nil {
		t.Fatal(err)
	}
	if s := staleDays(t, st); s["2026-09-02"] != 1 || len(s) != 1 {
		t.Fatalf("re-upserting an existing record marked %v", s)
	}
	reupserted := total()
	if reupserted == withLate {
		t.Fatal("re-upserting an existing record with a new quantity did not change the total")
	}
	buildRollup(t, st)
	if got := total(); got != reupserted {
		t.Fatalf("the rebuild changed the total: %s then %s", reupserted, got)
	}
	withLate = reupserted

	// 2. An UPDATE of an existing record marks its day.
	if _, err := st.DB().ExecContext(ctx, `UPDATE usage_records SET quantity = 3.000000
		WHERE source_id = $1 AND resource_id = 'vm-9'`, fx.cloudA.ID); err != nil {
		t.Fatal(err)
	}
	if s := staleDays(t, st); s["2026-09-02"] != 1 {
		t.Fatalf("an update marked %v", s)
	}
	updated := total()
	if updated == withLate {
		t.Fatal("an updated quantity did not change the total")
	}
	buildRollup(t, st)
	if got := total(); got != updated {
		t.Fatalf("the rebuild changed the total: %s then %s", updated, got)
	}

	// 3. A DELETE marks its day, through the store's own range delete.
	if _, err := st.DeleteUsageInRange(ctx, fx.cloudA.ID, "vm-9", day(2026, 9, 2), day(2026, 9, 3)); err != nil {
		t.Fatal(err)
	}
	if s := staleDays(t, st); s["2026-09-02"] != 1 {
		t.Fatalf("a delete marked %v", s)
	}
	buildRollup(t, st)
	if got := total(); got != base {
		t.Fatalf("deleting the backfilled record left %s, want the original %s", got, base)
	}

	// 4. A PRICE change needs no invalidation: no money is cached, so the
	// figure moves with the rollup fully fresh and nothing marked.
	if _, err := st.PutPriceItems(ctx, fx.omrBook, []store.PriceItem{
		{SKU: "ecs.m7n.xlarge.8", Unit: "instance-hour", UnitPrice: "0.74"},
		{SKU: "evs.ssd.gb", Unit: "gb-hour", UnitPrice: "0.00137"},
	}, true); err != nil {
		t.Fatal(err)
	}
	if s := staleDays(t, st); len(s) != 0 {
		t.Fatalf("a price change marked %v — the rollup is caching money it should not", s)
	}
	repriced := total()
	if repriced == base {
		t.Fatal("a price change did not move the total: the rollup is serving a cached cost")
	}

	// 5. A CURRENCY rate change, likewise.
	if _, err := st.PutCurrencyRate(ctx, "USD", "3.9", "manual"); err != nil {
		t.Fatal(err)
	}
	if got := total(); got == repriced {
		t.Fatal("a currency-rate change did not move the total")
	}
	converted := total()

	// 6. Assigning the source to a DIFFERENT book, likewise.
	assignBook(t, st, fx.cloudA.ID, fx.usdBook)
	if got := total(); got == converted {
		t.Fatal("reassigning a source's price book did not move the total")
	}
	assignBook(t, st, fx.cloudA.ID, fx.omrBook)

	// 7. A cost-centre rule change moves the cost_centre DIMENSION with the
	// rollup fresh, because the attribution is resolved from the labels the
	// rollup carries and never stored.
	centres := func() string {
		r, err := st.Explore(ctx, store.OperatorScope, store.CostQuery{From: fx.from, To: fx.to, Granularity: "month", GroupBy: store.CostCentreDimension})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(r.Groups)
		return string(b)
	}
	before := centres()
	cc2, err := st.CreateCostCentre(ctx, fx.acme.ID, store.CostCentreInput{Code: "CC-DATA", Name: "Data"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCostCentreRule(ctx, fx.acme.ID, store.CostCentreRuleInput{
		CostCentreRef: store.CostCentreRef{CostCentreID: cc2.ID}, TagKey: "team", TagValue: "b",
	}); err != nil {
		t.Fatal(err)
	}
	if s := staleDays(t, st); len(s) != 0 {
		t.Fatalf("a cost-centre rule marked %v", s)
	}
	if centres() == before {
		t.Fatal("a new cost-centre rule did not move the cost-centre breakdown")
	}

	// 8. A per-resource override, likewise.
	overridden := centres()
	if _, err := st.SetResourceCostCentre(ctx, fx.acme.ID, "vm-1", store.CostCentreRef{CostCentreID: cc2.ID}, "test"); err != nil {
		t.Fatal(err)
	}
	if centres() == overridden {
		t.Fatal("a per-resource cost-centre override did not move the breakdown")
	}

	// 9. Renaming the customer moves the LABEL with the rollup fresh: the
	// rollup keys on the id and joins the name at read time.
	newName := "Acme Renamed"
	if _, err := st.UpdateCustomer(ctx, fx.acme.ID, store.CustomerPatch{Name: &newName}); err != nil {
		t.Fatal(err)
	}
	byCustomer, err := st.Explore(ctx, store.OperatorScope, store.CostQuery{From: fx.from, To: fx.to, Granularity: "month", GroupBy: "customer"})
	if err != nil {
		t.Fatal(err)
	}
	var renamed bool
	for _, g := range byCustomer.Groups {
		if g.Label == "Acme Renamed" {
			renamed = true
		}
	}
	if !renamed {
		t.Fatalf("the customer rename did not reach the explorer: %+v", byCustomer.Groups)
	}
}

// TestIntegrationRollupBuildRaceLeavesThePartitionStale pins the one race
// the builder can lose: a usage write that lands while a partition is being
// built must leave the partition stale, so the reader keeps serving that day
// from the ledger and the next pass rebuilds it.
func TestIntegrationRollupBuildRaceLeavesThePartitionStale(t *testing.T) {
	st := testdb.Open(t)
	fx := seedRollupLedger(t, st)
	ctx := context.Background()
	buildRollup(t, st)

	// A builder that read the version, then a write, then the build: the
	// stale version it carries is what makes the mark refuse.
	parts := []store.CostRollupPartition{{SourceID: fx.cloudA.ID, Day: day(2026, 9, 3), Version: 1}}
	if _, err := st.DB().ExecContext(ctx, `UPDATE usage_records SET collected_at = now()
		WHERE source_id = $1 AND window_start >= $2 AND window_start < $3`, fx.cloudA.ID, day(2026, 9, 3), day(2026, 9, 4)); err != nil {
		t.Fatal(err)
	}
	_, fresh, err := st.BuildCostRollupPartition(ctx, parts[0])
	if err != nil {
		t.Fatal(err)
	}
	if fresh {
		t.Fatal("a build carrying a stale version marked the partition fresh")
	}
	if s := staleDays(t, st); s["2026-09-03"] != 1 {
		t.Fatalf("the raced partition is not stale: %v", s)
	}
}

// TestIntegrationRollupDroppedSourceIsSweptNotStranded pins the other half
// of having no foreign key: deleting a source must not leave its rows
// readable, and must not leave a day nothing can ever rebuild.
func TestIntegrationRollupDroppedSourceIsSweptNotStranded(t *testing.T) {
	st := testdb.Open(t)
	fx := seedRollupLedger(t, st)
	ctx := context.Background()
	buildRollup(t, st)
	before, err := st.Explore(ctx, store.OperatorScope, store.CostQuery{From: fx.from, To: fx.to, Granularity: "day", GroupBy: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSource(ctx, fx.cloudB.ID); err != nil {
		t.Fatal(err)
	}
	// The figure is already right — the explorer's inner join to cost_sources
	// drops the orphan rows — before the sweep runs.
	after, err := st.Explore(ctx, store.OperatorScope, store.CostQuery{From: fx.from, To: fx.to, Granularity: "day", GroupBy: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if after.Total.Current == before.Total.Current {
		t.Fatal("deleting a source did not change the total")
	}
	if _, err := st.PurgeOrphanCostRollup(ctx); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := st.DB().QueryRowContext(ctx, `SELECT count(*) FROM cost_usage_daily WHERE source_id = $1`, fx.cloudB.ID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d orphan rollup rows survived the sweep", left)
	}
	if s := staleDays(t, st); len(s) != 0 {
		t.Fatalf("the sweep left %v stranded", s)
	}
	swept, err := st.Explore(ctx, store.OperatorScope, store.CostQuery{From: fx.from, To: fx.to, Granularity: "day", GroupBy: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if swept.Total.Current != after.Total.Current {
		t.Fatalf("the sweep changed the total: %s then %s", after.Total.Current, swept.Total.Current)
	}
}

// TestIntegrationRollupKillSwitchServesLive pins COST_ROLLUP_ENABLED=false:
// with it off, the built rollup is ignored and the answer is unchanged.
func TestIntegrationRollupKillSwitchServesLive(t *testing.T) {
	st := testdb.Open(t)
	fx := seedRollupLedger(t, st)
	buildRollup(t, st)
	on := readSurfaces(t, st, fx)
	st.SetCostRollupEnabled(false)
	t.Cleanup(func() { st.SetCostRollupEnabled(true) })
	if st.CostRollupEnabled() {
		t.Fatal("the kill switch did not take")
	}
	off := readSurfaces(t, st, fx)
	diffSurfaces(t, "rollup off vs rollup on", on, off)
}

// TestIntegrationRollupHourGrainReadsTheLedger pins the one dimension of the
// request a daily rollup cannot serve: an hour-grain chart is always read
// from usage_records, and reads the same money as the day-grain document
// over the same window.
func TestIntegrationRollupHourGrainReadsTheLedger(t *testing.T) {
	st := testdb.Open(t)
	seedRollupLedger(t, st)
	ctx := context.Background()
	buildRollup(t, st)
	oneDay := store.CostQuery{From: day(2026, 9, 3), To: day(2026, 9, 4), Granularity: "hour", GroupBy: "kind"}
	hourly, err := st.Explore(ctx, store.OperatorScope, oneDay)
	if err != nil {
		t.Fatal(err)
	}
	if len(hourly.Buckets) != 24 {
		t.Fatalf("hour grain gave %d buckets", len(hourly.Buckets))
	}
	daily := oneDay
	daily.Granularity = "day"
	perDay, err := st.Explore(ctx, store.OperatorScope, daily)
	if err != nil {
		t.Fatal(err)
	}
	if hourly.Total.Current != perDay.Total.Current {
		t.Fatalf("hour grain totals %s, day grain %s", hourly.Total.Current, perDay.Total.Current)
	}
	// The stopped-instance policy splits vm-2's day; the hourly chart must
	// still show the 15 waived hours as zero-cost hours rather than gaps.
	if hourly.Total.Current == "0" {
		t.Fatal("the hour-grain window is empty")
	}
}

// ---------------------------------------------------------------------------
// scale
// ---------------------------------------------------------------------------

// TestIntegrationSummaryAtScale is the measurement the fix is reported with.
// It is skipped unless CHARGEBACK_SCALE_ROWS names a row count, because
// seeding the hw307 shape (~700k hourly records) takes about a minute; run
// it with
//
//	CHARGEBACK_SCALE_ROWS=700000 go test -run SummaryAtScale -timeout 30m ./internal/store
//
// and it prints the summary's six documents timed with the rollup off and
// with it on, over the same ledger.
func TestIntegrationSummaryAtScale(t *testing.T) {
	want, _ := strconv.Atoi(os.Getenv("CHARGEBACK_SCALE_ROWS"))
	if want <= 0 {
		t.Skip("CHARGEBACK_SCALE_ROWS not set; skipping the scale measurement")
	}
	st := testdb.Open(t)
	ctx := context.Background()
	rows := seedScaleLedger(t, st, want)
	t.Logf("seeded %d usage records", rows)

	from, to := day(2026, 9, 1), day(2026, 10, 1)
	// The six documents gatherSummary asks for, in one measurement.
	run := func() time.Duration {
		start := time.Now()
		for _, q := range []store.CostQuery{
			{From: from, To: to, Granularity: "day", GroupBy: "none"},
			{From: to.AddDate(0, 0, -30), To: to, Granularity: "day", GroupBy: "none"},
			{From: from.AddDate(0, -1, 0), To: from, Granularity: "month", GroupBy: "none"},
			{From: from.AddDate(0, -1, 0), To: from.AddDate(0, 0, -15), Granularity: "month", GroupBy: "none"},
			{From: from, To: to, Granularity: "month", GroupBy: "customer", Limit: 10},
			{From: from, To: to, Granularity: "month", GroupBy: "kind", Limit: 10},
		} {
			if _, err := st.Explore(ctx, store.OperatorScope, q); err != nil {
				t.Fatal(err)
			}
		}
		return time.Since(start)
	}
	st.SetCostRollupEnabled(false)
	before := run()
	st.SetCostRollupEnabled(true)
	t.Logf("summary over the hourly ledger: %s", before)

	buildStart := time.Now()
	built := buildRollup(t, st)
	t.Logf("built %d partitions in %s", built, time.Since(buildStart))

	after := run()
	t.Logf("summary over the daily rollup: %s (%.1fx)", after, float64(before)/float64(after))
	if after > before {
		t.Fatalf("the rollup made the summary slower: %s then %s", before, after)
	}
}

// seedScaleLedger writes roughly `want` hourly usage records across a
// handful of customers and a month of days — the hw307 shape.
func seedScaleLedger(t *testing.T, st *store.Store, want int) int {
	t.Helper()
	ctx := context.Background()
	book, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "scale", Currency: "OMR", AnnualDivisor: 8760, BillStopped: "none"})
	if err != nil {
		t.Fatal(err)
	}
	items := []store.PriceItem{
		{SKU: "ecs.m7n.xlarge.8", Unit: "instance-hour", UnitPrice: "0.37"},
		{SKU: "evs.ssd.gb", Unit: "gb-hour", UnitPrice: "0.00137"},
		{SKU: "eip", Unit: "hour", UnitPrice: "0.0213"},
	}
	if _, err := st.PutPriceItems(ctx, book.ID, items, true); err != nil {
		t.Fatal(err)
	}
	const customers, days, hours = 6, 30, 24
	// resources per customer so that customers × resources × days × hours ≈ want
	perCustomer := want / (customers * days * hours)
	if perCustomer < 1 {
		perCustomer = 1
	}
	written := 0
	for c := range customers {
		cust, err := st.CreateCustomer(ctx, store.CustomerInput{
			Slug: fmt.Sprintf("cust-%d", c), Name: fmt.Sprintf("Customer %d", c),
			AdminEmail: fmt.Sprintf("c%d@example.test", c), StartDate: "2026-08-01"})
		if err != nil {
			t.Fatal(err)
		}
		src, _, err := st.UpsertSource(ctx, cust.ID, "huawei-project", "me-east-1", fmt.Sprintf("proj-%d", c))
		if err != nil {
			t.Fatal(err)
		}
		assignBook(t, st, src.ID, book.ID)
		for d := 1; d <= days; d++ {
			var recs []store.UsageRecord
			for h := range hours {
				at := day(2026, 9, d).Add(time.Duration(h) * time.Hour)
				for r := range perCustomer {
					item := items[r%len(items)]
					lb, _ := json.Marshal(map[string]any{
						"name": fmt.Sprintf("res-%d", r), "status": "ACTIVE",
						"namespace": fmt.Sprintf("ns-%d", r%7), "tier": "organization",
						"tags": map[string]string{"team": fmt.Sprintf("t%d", r%5)},
					})
					recs = append(recs, store.UsageRecord{CustomerID: cust.ID, SourceID: src.ID,
						ResourceID: fmt.Sprintf("res-%d-%d", c, r), ResourceKind: []string{"ecs", "evs", "eip"}[r%3],
						SKU: item.SKU, Quantity: "1.250000", Unit: item.Unit,
						WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-1", Labels: lb})
				}
			}
			n, err := st.UpsertUsage(ctx, recs)
			if err != nil {
				t.Fatal(err)
			}
			written += n
		}
	}
	return written
}
