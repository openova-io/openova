package store_test

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// TestIntegrationEnsurePlanBookIdempotent: the "OpenOva plans" book is
// created once with the four catalog plans priced at monthly/730 per
// plan-hour, and never re-created or re-priced — an operator's edit stands.
func TestIntegrationEnsurePlanBookIdempotent(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	pb, created, err := st.EnsurePlanBook(ctx)
	if err != nil || !created {
		t.Fatalf("first ensure: created=%v err=%v", created, err)
	}
	if pb.Name != store.PlanBookName || pb.Currency != "OMR" || pb.AnnualDivisor != 8760 {
		t.Fatalf("book = %+v", pb)
	}
	// annual = monthly × 12; unit = annual / 8760 = monthly / 730, 8 decimals.
	want := map[string][2]string{
		"plan.s":  {"60.00000000", "0.00684932"},
		"plan.m":  {"108.00000000", "0.01232877"},
		"plan.l":  {"192.00000000", "0.02191781"},
		"plan.xl": {"360.00000000", "0.04109589"},
	}
	if len(pb.Items) != len(want) {
		t.Fatalf("items = %+v", pb.Items)
	}
	for _, it := range pb.Items {
		w, ok := want[it.SKU]
		if !ok || it.Unit != store.PlanUnit || it.AnnualPrice == nil || string(*it.AnnualPrice) != w[0] || string(it.UnitPrice) != w[1] {
			t.Fatalf("item %s = unit %s annual %v price %s, want %v", it.SKU, it.Unit, it.AnnualPrice, it.UnitPrice, w)
		}
		if it.Description == "" {
			t.Fatalf("item %s has no description stating the arithmetic", it.SKU)
		}
	}
	// Flexi is pay per use and k8s.* meters are the allocation basis: none priced.
	for _, sku := range []string{"plan.flexi", "k8s.vcpu", "k8s.mem_gb", "k8s.pvc_gb"} {
		if _, err := st.GetPriceItem(ctx, pb.ID, sku); err == nil {
			t.Fatalf("%s must not be priced in the plan book", sku)
		}
	}

	// The operator negotiates plan.m down; a second ensure keeps it.
	newPrice := store.Decimal("0.01")
	if _, err := st.UpdatePriceItem(ctx, pb.ID, "plan.m", store.PriceItemPatch{UnitPrice: &newPrice}); err != nil {
		t.Fatal(err)
	}
	again, created, err := st.EnsurePlanBook(ctx)
	if err != nil || created || again.ID != pb.ID {
		t.Fatalf("second ensure: id %s→%s created=%v err=%v", pb.ID, again.ID, created, err)
	}
	it, err := st.GetPriceItem(ctx, pb.ID, "plan.m")
	if err != nil || string(it.UnitPrice) != "0.01000000" {
		t.Fatalf("operator edit lost: %+v err=%v", it, err)
	}
	books, err := st.ListPriceBooks(ctx)
	if err != nil || len(books) != 1 {
		t.Fatalf("books = %d err=%v, want exactly one", len(books), err)
	}
}

// TestIntegrationAllocationPlanRevenue (DESIGN.md §2.8): an Organization on
// plan m for a full 7-day window earns 7 × 24 × (9/730) as rated revenue
// on the allocation view — the Explore total of its plan line — and the
// margin is that revenue minus its allocated share of the pool.
func TestIntegrationAllocationPlanRevenue(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	book, _, err := st.EnsurePlanBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	orgA, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "acme", Name: "acme", AdminEmail: "acme@x.example", Kind: "organization", OrgSlug: "acme", PlanSlug: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if orgA.PlanSlug != "m" {
		t.Fatalf("plan_slug not stored: %+v", orgA)
	}
	// The pool: the Sovereign's cloud bill, one ECS at 0.5/h for the week.
	cloud, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "cloud", Currency: "OMR", AnnualDivisor: 8760, BillStopped: "compute"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, cloud.ID, []store.PriceItem{{SKU: "ecs.m7n.xlarge.8", Unit: "instance-hour", UnitPrice: "0.5"}}, true); err != nil {
		t.Fatal(err)
	}
	// The landlord: a plain external customer whose cloud source carries the
	// cloud bill (the Sovereign itself is not a customer, DESIGN.md §2).
	sov, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "landlord", Name: "landlord", AdminEmail: "sov@x.example"})
	if err != nil {
		t.Fatal(err)
	}
	srcA, _, err := st.UpsertSource(ctx, orgA.ID, "openova-org", "", "acme")
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
	// Per-source books (DESIGN.md §2): the Organization's platform source on
	// the plans book, the landlord's cloud source on the cloud book.
	assignBook(t, st, srcA.ID, book.ID)
	assignBook(t, st, sovSrc.ID, cloud.ID)
	planLabels, _ := json.Marshal(map[string]any{"name": "M plan", "plan": "m"})
	orgLabels, _ := json.Marshal(map[string]any{"tier": "organization", "namespace": "acme"})
	var recs []store.UsageRecord
	from := day(2026, 9, 1)
	for h := 0; h < 7*24; h++ {
		at := from.Add(time.Duration(h) * time.Hour)
		recs = append(recs,
			store.UsageRecord{CustomerID: orgA.ID, SourceID: srcA.ID, ResourceID: "plan/m", ResourceKind: store.PlanKind, SKU: "plan.m", Quantity: "1.000000", Unit: store.PlanUnit, WindowStart: at, WindowEnd: at.Add(time.Hour), Labels: planLabels},
			store.UsageRecord{CustomerID: orgA.ID, SourceID: srcA.ID, ResourceID: "acme/pod-1", ResourceKind: "k8s-pod", SKU: "k8s.vcpu", Quantity: "2.000000", Unit: "vcpu-hour", WindowStart: at, WindowEnd: at.Add(time.Hour), Labels: orgLabels},
			store.UsageRecord{CustomerID: sov.ID, SourceID: sovSrc.ID, ResourceID: "vm-1", ResourceKind: "ecs", SKU: "ecs.m7n.xlarge.8", Quantity: "1.000000", Unit: "instance-hour", WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-1"},
		)
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}

	res, err := st.Allocation(ctx, store.OperatorScope, from, day(2026, 9, 8))
	if err != nil {
		t.Fatal(err)
	}
	// 168 plan-hours × round(9/730, 8) = 168 × 0.01232877 = 2.07123336 →
	// 2.071233 at the ledger's 6 decimals; the exact 168 × 9/730 =
	// 2.0712329 rounds to the same figure.
	a := allocRow(res, orgA.ID, "organization")
	if a == nil {
		t.Fatalf("no row for acme: %+v", res.Rows)
	}
	if a.RatedRevenue != "2.071233" {
		t.Fatalf("rated_revenue = %s, want 2.071233 (7 × 24 × 9/730)", a.RatedRevenue)
	}
	// The pool is the Sovereign's cloud bill (7 × 24 × 0.5 = 84.0; the
	// Sovereign's own Organization carries no plan line, so nothing else
	// feeds it); acme is the sole basis row, so it is allocated all of it,
	// and the margin is revenue − allocation.
	if res.Pool.Amount != "84.000000" || a.AllocatedCost != "84.000000" {
		t.Fatalf("pool %s allocated %s, want 84.000000", res.Pool.Amount, a.AllocatedCost)
	}
	wantPct := -81.928767 / 2.071233 * 100
	if a.Margin != "-81.928767" || a.MarginPct == nil || math.Abs(*a.MarginPct-wantPct) > 0.01 {
		t.Fatalf("margin = %s (%v), want 2.071233 − 84 = −81.928767 (%.2f %%)", a.Margin, a.MarginPct, wantPct)
	}
	if res.Totals.Revenue != "2.071233" || res.Totals.Margin != "-81.928767" {
		t.Fatalf("totals = %+v", res.Totals)
	}
	// The statement side files the same line under the plan kind.
	if store.KindLabel(store.PlanKind) != "Subscription plan" {
		t.Fatalf("KindLabel(plan) = %q", store.KindLabel(store.PlanKind))
	}
}
