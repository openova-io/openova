package store_test

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// The attribution itself, against Postgres (DESIGN.md §19): the order a
// record is resolved in, and the bucket everything unnamed falls into.

type ccFixture struct {
	st   *store.Store
	cust store.Customer
	src  store.CostSource
	from time.Time
	to   time.Time
}

func ccSetup(t *testing.T) ccFixture {
	t.Helper()
	st := testdb.Open(t)
	ctx := context.Background()
	book, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "list", Currency: "OMR", AnnualDivisor: 8760, BillStopped: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, book.ID, []store.PriceItem{{SKU: "ecs.s6.large.2", Unit: "instance-hour", UnitPrice: "1"}}, true); err != nil {
		t.Fatal(err)
	}
	c, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "acme", Name: "Acme", AdminEmail: "owner@acme.example"})
	if err != nil {
		t.Fatal(err)
	}
	src, _, err := st.UpsertSource(ctx, c.ID, "huawei-project", "me-east-215", "ok-p1")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSourcePriceBook(ctx, src.ID, book.ID); err != nil {
		t.Fatal(err)
	}
	return ccFixture{
		st: st, cust: c, src: src,
		from: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		to:   time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
	}
}

// usage writes one hour of one resource carrying the given tags.
func (f ccFixture) usage(t *testing.T, resourceID string, hour int, tags map[string]string) {
	t.Helper()
	labels := map[string]any{"name": resourceID, "status": "ACTIVE"}
	if tags != nil {
		labels["tags"] = tags
	}
	lb, _ := json.Marshal(labels)
	at := f.from.Add(time.Duration(hour) * time.Hour)
	if _, err := f.st.UpsertUsage(context.Background(), []store.UsageRecord{{
		CustomerID: f.cust.ID, SourceID: f.src.ID, ResourceID: resourceID, ResourceKind: "ecs", SKU: "ecs.s6.large.2",
		Quantity: "1.000000", Unit: "instance-hour", WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-215", Labels: lb,
	}}); err != nil {
		t.Fatal(err)
	}
}

func (f ccFixture) centre(t *testing.T, code string) store.CostCentre {
	t.Helper()
	c, err := f.st.CreateCostCentre(context.Background(), f.cust.ID, store.CostCentreInput{Code: code, Name: code})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f ccFixture) weights(t *testing.T) map[string]store.Decimal {
	t.Helper()
	rows, err := f.st.CostCentreWeights(context.Background(), store.OperatorScope, f.cust.ID, f.from, f.to)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.Decimal{}
	for _, r := range rows {
		out[r.Code] = r.Amount
	}
	return out
}

// Two rules on DIFFERENT tag keys can both match one resource. PRIORITY
// decides, then the key, then the value — so the answer never depends on the
// order rows happen to come back in.
func TestIntegrationCostCentreRulePriorityDecidesBetweenTwoKeys(t *testing.T) {
	f := ccSetup(t)
	ctx := context.Background()
	eng, res := f.centre(t, "ENG"), f.centre(t, "RES")
	f.usage(t, "srv-1", 0, map[string]string{"team": "platform", "project": "atlas"})

	lower := 10
	higher := 20
	if _, err := f.st.PutCostCentreRule(ctx, f.cust.ID, store.CostCentreRuleInput{
		CostCentreRef: store.CostCentreRef{CostCentreID: res.ID}, TagKey: "project", TagValue: "atlas", Priority: &higher,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.PutCostCentreRule(ctx, f.cust.ID, store.CostCentreRuleInput{
		CostCentreRef: store.CostCentreRef{CostCentreID: eng.ID}, TagKey: "team", TagValue: "platform", Priority: &lower,
	}); err != nil {
		t.Fatal(err)
	}
	if w := f.weights(t); w["ENG"] != "1.000000" || w["RES"] != "" {
		t.Fatalf("the lower priority did not win: %v", w)
	}
	// Flip the ranks and the OTHER rule wins the same resource.
	if _, err := f.st.PutCostCentreRule(ctx, f.cust.ID, store.CostCentreRuleInput{
		CostCentreRef: store.CostCentreRef{CostCentreID: res.ID}, TagKey: "project", TagValue: "atlas", Priority: &lower,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.PutCostCentreRule(ctx, f.cust.ID, store.CostCentreRuleInput{
		CostCentreRef: store.CostCentreRef{CostCentreID: eng.ID}, TagKey: "team", TagValue: "platform", Priority: &higher,
	}); err != nil {
		t.Fatal(err)
	}
	if w := f.weights(t); w["RES"] != "1.000000" || w["ENG"] != "" {
		t.Fatalf("flipping the ranks did not flip the answer: %v", w)
	}
	// Equal ranks fall back to the KEY, so the answer is still one rule and
	// still the same one on every run: "project" sorts before "team".
	same := 50
	for _, r := range []struct {
		id, key, value string
	}{{res.ID, "project", "atlas"}, {eng.ID, "team", "platform"}} {
		if _, err := f.st.PutCostCentreRule(ctx, f.cust.ID, store.CostCentreRuleInput{
			CostCentreRef: store.CostCentreRef{CostCentreID: r.id}, TagKey: r.key, TagValue: r.value, Priority: &same,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if w := f.weights(t); w["RES"] != "1.000000" {
			t.Fatalf("run %d resolved an equal-rank tie differently: %v", i, w)
		}
	}
}

// The order of resolution, end to end: the per-resource OVERRIDE beats every
// rule, a rule beats nothing, and what nothing names is the unassigned
// bucket — never dropped.
func TestIntegrationCostCentreResolutionOrder(t *testing.T) {
	f := ccSetup(t)
	ctx := context.Background()
	f.centre(t, "ENG")
	res := f.centre(t, "RES")
	f.usage(t, "tagged", 0, map[string]string{"team": "platform"})
	f.usage(t, "overridden", 1, map[string]string{"team": "platform"})
	f.usage(t, "bare", 2, nil)
	f.usage(t, "other-value", 3, map[string]string{"team": "sales"})

	if w := f.weights(t); w[store.CostCentreUnassigned] != "4.000000" || len(w) != 1 {
		t.Fatalf("with no rules everything should be unassigned: %v", w)
	}
	if _, err := f.st.PutCostCentreRule(ctx, f.cust.ID, store.CostCentreRuleInput{
		CostCentreRef: store.CostCentreRef{Code: "ENG"}, TagKey: "team", TagValue: "platform",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.SetResourceCostCentre(ctx, f.cust.ID, "overridden", store.CostCentreRef{CostCentreID: res.ID}, "ops@nc.example"); err != nil {
		t.Fatal(err)
	}
	w := f.weights(t)
	if w["ENG"] != "1.000000" {
		t.Errorf("the rule attributed %s to ENG, want 1", w["ENG"])
	}
	if w["RES"] != "1.000000" {
		t.Errorf("the override attributed %s to RES, want 1 — the override must beat the rule", w["RES"])
	}
	if w[store.CostCentreUnassigned] != "2.000000" {
		t.Errorf("unassigned = %s, want the bare resource and the unmatched tag value", w[store.CostCentreUnassigned])
	}
	total := new(big.Rat)
	for _, v := range w {
		r, ok := new(big.Rat).SetString(string(v))
		if !ok {
			t.Fatalf("not a number: %q", v)
		}
		total.Add(total, r)
	}
	if total.Cmp(big.NewRat(4, 1)) != 0 {
		t.Fatalf("the weights sum to %s, and 4 hours were collected — usage went missing", total.FloatString(6))
	}

	// Clearing the override drops the resource back onto the rule.
	if err := f.st.ClearResourceCostCentre(ctx, f.cust.ID, "overridden"); err != nil {
		t.Fatal(err)
	}
	if w := f.weights(t); w["ENG"] != "2.000000" || w["RES"] != "" {
		t.Fatalf("clearing the override did not fall back to the rule: %v", w)
	}
	// Deleting the centre takes its rule and its usage with it — back to the
	// bucket, never nowhere.
	if err := f.st.DeleteCostCentre(ctx, f.centreID(t, "ENG")); err != nil {
		t.Fatal(err)
	}
	if w := f.weights(t); w[store.CostCentreUnassigned] != "4.000000" || len(w) != 1 {
		t.Fatalf("after deleting the centre: %v", w)
	}
}

func (f ccFixture) centreID(t *testing.T, code string) string {
	t.Helper()
	list, err := f.st.ListCostCentres(context.Background(), store.OperatorScope, f.cust.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if c.Code == code {
			return c.ID
		}
	}
	t.Fatalf("no cost centre %s", code)
	return ""
}
