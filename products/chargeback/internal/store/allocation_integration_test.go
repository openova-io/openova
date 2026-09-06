package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// Allocation against Postgres (#6867): two Organizations consuming the
// platform, the Sovereign's own platform-overhead footprint, and the
// Sovereign's priced cloud bill that is the pool.

type allocSeed struct {
	orgA, orgB, sov store.Customer
	sovSrc          store.CostSource
}

// seedAllocation writes, on 2026-09-01..03 (3 days × 24 h):
//   - orgA: 2 vCPU-h + 4 GiB-h per hour (72 h)          → 144 / 288 / 0
//   - orgB: 1 vCPU-h + 0 GiB + 10 PVC-GB-h per hour     → 72 / 0 / 720
//   - sov overhead: 1 vCPU-h + 1 GiB-h per hour         → 72 / 72 / 0
//   - sov cloud: one ECS at 0.5/h + 100 GB EVS at 0.001/h → 72×0.5 + 72×0.1 = 43.2
//   - orgA revenue: a priced plan meter 0.25/h          → 18; orgB: none
func seedAllocation(t *testing.T, st *store.Store) allocSeed {
	t.Helper()
	ctx := context.Background()
	book, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "list", Currency: "OMR", AnnualDivisor: 8760, BillStopped: "compute"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, book.ID, []store.PriceItem{
		{SKU: "ecs.m7n.xlarge.8", Unit: "instance-hour", UnitPrice: "0.5"},
		{SKU: "evs.ssd.gb", Unit: "gb-hour", UnitPrice: "0.001"},
		{SKU: "plan.hour", Unit: "hour", UnitPrice: "0.25"},
	}, true); err != nil {
		t.Fatal(err)
	}
	mk := func(slug string) store.Customer {
		c, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: slug, Name: slug, AdminEmail: slug + "@x.example", Kind: "organization", PriceBookID: book.ID})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	orgA, orgB, sov := mk("acme"), mk("bravo"), mk("sovereign")
	srcA, _, err := st.UpsertSource(ctx, orgA.ID, "openova-org", "", "acme")
	if err != nil {
		t.Fatal(err)
	}
	srcB, _, err := st.UpsertSource(ctx, orgB.ID, "openova-org", "", "bravo")
	if err != nil {
		t.Fatal(err)
	}
	srcSovK8s, _, err := st.UpsertSource(ctx, sov.ID, "openova-org", "", "platform")
	if err != nil {
		t.Fatal(err)
	}
	sovSrc, _, err := st.UpsertSource(ctx, sov.ID, "huawei-project", "me-east-1", "proj-sov")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSourceVerified(ctx, sovSrc.ID, ""); err != nil {
		t.Fatal(err)
	}
	var recs []store.UsageRecord
	rec := func(c store.Customer, src store.CostSource, res, kind, sku, unit string, qty float64, at time.Time, labels map[string]any) {
		lb, _ := json.Marshal(labels)
		recs = append(recs, store.UsageRecord{CustomerID: c.ID, SourceID: src.ID, ResourceID: res, ResourceKind: kind, SKU: sku,
			Quantity: store.Decimal(strconv.FormatFloat(qty, 'f', 6, 64)), Unit: unit, WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-1", Labels: lb})
	}
	org := map[string]any{"tier": "organization", "namespace": "ns"}
	overhead := map[string]any{"tier": "platform-overhead", "namespace": "gitea"}
	for d := 1; d <= 3; d++ {
		for h := 0; h < 24; h++ {
			at := day(2026, 9, d).Add(time.Duration(h) * time.Hour)
			rec(orgA, srcA, "acme/pod-1", "k8s-pod", "k8s.vcpu", "vcpu-hour", 2, at, org)
			rec(orgA, srcA, "acme/pod-1", "k8s-pod", "k8s.mem_gb", "gib-hour", 4, at, org)
			rec(orgA, srcA, "acme", "plan", "plan.hour", "hour", 1, at, org)
			rec(orgB, srcB, "bravo/pod-1", "k8s-pod", "k8s.vcpu", "vcpu-hour", 1, at, org)
			rec(orgB, srcB, "bravo/pvc-1", "k8s-pvc", "k8s.pvc_gb", "gb-hour", 10, at, org)
			rec(sov, srcSovK8s, "gitea/pod-1", "k8s-pod", "k8s.vcpu", "vcpu-hour", 1, at, overhead)
			rec(sov, srcSovK8s, "gitea/pod-1", "k8s-pod", "k8s.mem_gb", "gib-hour", 1, at, overhead)
			rec(sov, sovSrc, "vm-1", "ecs", "ecs.m7n.xlarge.8", "instance-hour", 1, at, map[string]any{"name": "node-1", "status": "ACTIVE"})
			rec(sov, sovSrc, "vol-1", "evs", "evs.ssd.gb", "gb-hour", 100, at, map[string]any{"name": "vol-1"})
		}
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	return allocSeed{orgA: orgA, orgB: orgB, sov: sov, sovSrc: sovSrc}
}

func allocRow(res store.AllocationResult, id, tier string) *store.AllocationRow {
	for i := range res.Rows {
		if res.Rows[i].CustomerID == id && res.Rows[i].Tier == tier {
			return &res.Rows[i]
		}
	}
	return nil
}

func TestIntegrationAllocationResolvesPoolAndReconciles(t *testing.T) {
	st := testdb.Open(t)
	s := seedAllocation(t, st)
	ctx := context.Background()

	res, err := st.Allocation(ctx, store.OperatorScope, day(2026, 9, 1), day(2026, 9, 4))
	if err != nil {
		t.Fatal(err)
	}
	// The pool is found without configuration: the one customer with a
	// verified huawei-project source, priced with platform SKUs excluded.
	if res.Pool.Source != "sovereign-cost" || res.Pool.CustomerID == nil || *res.Pool.CustomerID != s.sov.ID || res.Pool.Note != "" {
		t.Fatalf("pool = %+v", res.Pool)
	}
	if res.Pool.Amount != "43.200000" || res.Pool.Currency != "OMR" {
		t.Fatalf("pool amount = %s %s, want 43.200000 OMR", res.Pool.Amount, res.Pool.Currency)
	}
	if res.OrganizationRows != 2 || res.PlatformOverhead != 1 || len(res.Rows) != 3 {
		t.Fatalf("rows = %d org / %d overhead: %+v", res.OrganizationRows, res.PlatformOverhead, res.Rows)
	}
	if math.Abs(res.ShareTotal-1) > 1e-9 {
		t.Fatalf("share_total = %v", res.ShareTotal)
	}
	// Equal weights: A 432, B 792, overhead 144 of 1368.
	a, b, o := allocRow(res, s.orgA.ID, "organization"), allocRow(res, s.orgB.ID, "organization"), allocRow(res, s.sov.ID, "platform-overhead")
	if a == nil || b == nil || o == nil {
		t.Fatalf("missing rows: %+v", res.Rows)
	}
	if a.VCPUHours != "144.000000" || a.MemGiBHours != "288.000000" || a.Weight != "432.000000" {
		t.Fatalf("a basis = %+v", *a)
	}
	if b.PVCGBHours != "720.000000" || b.Weight != "792.000000" || o.Weight != "144.000000" {
		t.Fatalf("b/overhead basis = %+v / %+v", *b, *o)
	}
	// Money: allocated costs sum to the pool exactly, margins computed.
	if res.Totals.Allocated != res.Pool.Amount {
		t.Fatalf("allocated %s ≠ pool %s", res.Totals.Allocated, res.Pool.Amount)
	}
	if a.RatedRevenue != "18.000000" || b.RatedRevenue != "0.000000" || o.RatedRevenue != "0.000000" {
		t.Fatalf("revenue = %s / %s / %s", a.RatedRevenue, b.RatedRevenue, o.RatedRevenue)
	}
	// A: 43.2 × 432/1368 = 13.642105; margin 18 − 13.642105 = 4.357895 (24.2 %).
	if a.AllocatedCost != "13.642105" || a.Margin != "4.357895" || a.MarginPct == nil || math.Abs(*a.MarginPct-24.21) > 0.01 {
		t.Fatalf("a money = %+v", *a)
	}
	// B has no revenue: margin is −cost and the percentage is absent, not 0.
	if b.MarginPct != nil || b.Margin != store.Decimal("-"+string(b.AllocatedCost)) {
		t.Fatalf("b money = %+v", *b)
	}
	if res.Totals.Revenue != "18.000000" || res.Totals.Margin != "-25.200000" {
		t.Fatalf("totals = %+v", res.Totals)
	}
	if res.From != "2026-09-01" || res.To != "2026-09-04" || res.Settings.Pool != "sovereign-cost" {
		t.Fatalf("envelope = %s..%s %+v", res.From, res.To, res.Settings)
	}

	// Operator-only.
	if _, err := st.Allocation(ctx, store.CustomerScope(s.orgA.ID), day(2026, 9, 1), day(2026, 9, 4)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("customer scope read the allocation: %v", err)
	}
}

func TestIntegrationAllocationSettingsDriveTheSplit(t *testing.T) {
	st := testdb.Open(t)
	s := seedAllocation(t, st)
	ctx := context.Background()

	// Distribute: the overhead row is gone and the pool is entirely on the
	// Organizations; manual pool overrides the cloud bill.
	set, err := st.UpdateAllocationSettings(ctx, store.AllocationSettings{
		Weights: store.AllocationWeights{VCPU: 1, MemGiB: 0, PVCGB: 0}, OverheadPolicy: "distribute", Pool: "manual", ManualAmount: "1000", Currency: "usd",
	})
	if err != nil {
		t.Fatal(err)
	}
	if set.Currency != "USD" || set.Pool != "manual" || set.SovereignCustomerID != nil || set.UpdatedAt.IsZero() {
		t.Fatalf("settings after update = %+v", set)
	}
	res, err := st.Allocation(ctx, store.OperatorScope, day(2026, 9, 1), day(2026, 9, 4))
	if err != nil {
		t.Fatal(err)
	}
	if res.Pool.Source != "manual" || res.Pool.Amount != "1000.000000" || res.Pool.Currency != "USD" {
		t.Fatalf("manual pool = %+v", res.Pool)
	}
	if res.PlatformOverhead != 0 || res.OrganizationRows != 2 || math.Abs(res.ShareTotal-1) > 1e-9 {
		t.Fatalf("distribute → %d overhead, %d org, share_total %v", res.PlatformOverhead, res.OrganizationRows, res.ShareTotal)
	}
	// vCPU only: A 144, B 72 → 2/3 : 1/3 of 1000.
	a, b := allocRow(res, s.orgA.ID, "organization"), allocRow(res, s.orgB.ID, "organization")
	if a.AllocatedCost != "666.666667" || b.AllocatedCost != "333.333333" || res.Totals.Allocated != "1000.000000" {
		t.Fatalf("vcpu-only distribute = %s / %s (Σ %s)", a.AllocatedCost, b.AllocatedCost, res.Totals.Allocated)
	}

	// Naming the Sovereign customer explicitly is honoured, and a customer
	// that does not exist is refused.
	if _, err := st.UpdateAllocationSettings(ctx, store.AllocationSettings{
		Weights: store.AllocationWeights{VCPU: 1, MemGiB: 1, PVCGB: 1}, OverheadPolicy: "separate", Pool: "sovereign-cost", Currency: "OMR",
		SovereignCustomerID: strP("00000000-0000-0000-0000-000000000000"),
	}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown sovereign customer: %v", err)
	}
	if _, err := st.UpdateAllocationSettings(ctx, store.AllocationSettings{
		Weights: store.AllocationWeights{VCPU: 1, MemGiB: 1, PVCGB: 1}, OverheadPolicy: "separate", Pool: "sovereign-cost", Currency: "OMR",
		SovereignCustomerID: &s.orgA.ID,
	}); err != nil {
		t.Fatal(err)
	}
	res, err = st.Allocation(ctx, store.OperatorScope, day(2026, 9, 1), day(2026, 9, 4))
	if err != nil {
		t.Fatal(err)
	}
	// acme named as the Sovereign: its priced usage (the plan meter, 18) is
	// the pool and no longer its revenue.
	if res.Pool.CustomerID == nil || *res.Pool.CustomerID != s.orgA.ID || res.Pool.Amount != "18.000000" {
		t.Fatalf("named pool = %+v", res.Pool)
	}
	if a := allocRow(res, s.orgA.ID, "organization"); a.RatedRevenue != "0.000000" {
		t.Fatalf("pool customer's own usage counted as revenue: %+v", *a)
	}

	// Validation errors are ErrInvalid, and leave the row untouched.
	before, _ := st.GetAllocationSettings(ctx)
	for _, bad := range []store.AllocationSettings{
		{Weights: store.AllocationWeights{}, OverheadPolicy: "separate", Pool: "manual", Currency: "OMR"},
		{Weights: store.AllocationWeights{VCPU: 1}, OverheadPolicy: "halve", Pool: "manual", Currency: "OMR"},
		{Weights: store.AllocationWeights{VCPU: 1}, OverheadPolicy: "separate", Pool: "manual", ManualAmount: "-1", Currency: "OMR"},
		{Weights: store.AllocationWeights{VCPU: 1}, OverheadPolicy: "separate", Pool: "manual", Currency: "OMANI"},
	} {
		if _, err := st.UpdateAllocationSettings(ctx, bad); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("%+v accepted: %v", bad, err)
		}
	}
	after, _ := st.GetAllocationSettings(ctx)
	if after.UpdatedAt != before.UpdatedAt || after.Currency != before.Currency {
		t.Fatalf("rejected update changed the row: %+v → %+v", before, after)
	}
}

func TestIntegrationAllocationUnresolvedPoolSaysWhatToSet(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	// Two verified huawei-project customers and no explicit choice: guessing
	// would bill the wrong footprint, so the pool is unresolved with a note.
	for _, slug := range []string{"one", "two"} {
		c := mkCustomer(t, st, slug)
		src, _, err := st.UpsertSource(ctx, c.ID, "huawei-project", "me-east-1", "proj-"+slug)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetSourceVerified(ctx, src.ID, ""); err != nil {
			t.Fatal(err)
		}
	}
	res, err := st.Allocation(ctx, store.OperatorScope, day(2026, 9, 1), day(2026, 9, 4))
	if err != nil {
		t.Fatal(err)
	}
	if res.Pool.Source != "unresolved" || res.Pool.Amount != "0.000000" || res.Pool.Note == "" || res.Pool.CustomerID != nil {
		t.Fatalf("ambiguous pool = %+v", res.Pool)
	}
	if len(res.Rows) != 0 || res.ShareTotal != 0 {
		t.Fatalf("empty window produced rows: %+v", res.Rows)
	}
}

func strP(s string) *string { return &s }
