package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// A partner reads the cost explorer ACROSS its own customers (DESIGN.md
// §13.5) — the founder's reason for resellers: "so the resellers could see
// the cost analysis of their customers".
//
// The ledger below is deliberately uneven so no total can be reached by two
// different routes: partner One's two customers cost 10 and 20, partner
// Two's 40 and 80, and a direct customer 160. A partner's window total is
// therefore 30 or 120, the operator's 310, and any wrong predicate — a
// missing filter, a scope that widened, an intersection that became a union
// — lands on a number that is in none of those places.

type partnerCostSeed struct {
	one, two             store.Partner
	oneA, oneB           store.Customer
	twoA, twoB           store.Customer
	direct               store.Customer
	oneScope, twoScope   store.Scope
	fromDay, toDay       time.Time
	oneTotal, twoTotal   float64
	directTotal, allCost float64
}

// seedPartnerCost writes one priced ECS record per hour per customer from
// 2026-09-01 (a 7-day window, so 168 hours of room), with the hour count
// carrying the customer's share: One's customers 10 + 20, Two's 40 + 80, the
// direct one 160. Each customer owns exactly one inventory row.
func seedPartnerCost(t *testing.T, st *store.Store) partnerCostSeed {
	t.Helper()
	ctx := context.Background()
	s := partnerCostSeed{fromDay: day(2026, 9, 1), toDay: day(2026, 9, 8)}
	book, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "list", Currency: "OMR", AnnualDivisor: 8760})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, book.ID, []store.PriceItem{
		{SKU: "ecs.m7n.xlarge.8", Unit: "instance-hour", UnitPrice: "1"},
	}, true); err != nil {
		t.Fatal(err)
	}
	mkPartner := func(slug, name string) store.Partner {
		t.Helper()
		p, err := st.CreatePartner(ctx, store.PartnerInput{Slug: slug, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	// hours is both the record count and, at a unit price of 1, the cost.
	mkCustomer := func(slug, name string, partner *store.Partner, hours int) store.Customer {
		t.Helper()
		c, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: slug, Name: name, AdminEmail: slug + "@example.com", StartDate: "2026-09-01"})
		if err != nil {
			t.Fatal(err)
		}
		if partner != nil {
			if _, err := st.UpdateCustomer(ctx, c.ID, store.CustomerPatch{PartnerID: &partner.ID}); err != nil {
				t.Fatal(err)
			}
		}
		src, _, err := st.UpsertSource(ctx, c.ID, "huawei-project", "me-east-215", "proj-"+slug)
		if err != nil {
			t.Fatal(err)
		}
		assignBook(t, st, src.ID, book.ID)
		if _, err := st.UpsertInventory(ctx, src.ID, []store.InventoryUpsert{
			{ResourceID: "vm-" + slug, Kind: "ecs", Name: "vm " + name, Attrs: map[string]any{"status": "ACTIVE", "flavor": "m7n.xlarge.8"},
				Created: s.fromDay, SeenAt: s.fromDay.Add(time.Duration(hours) * time.Hour)},
		}); err != nil {
			t.Fatal(err)
		}
		var recs []store.UsageRecord
		for h := 0; h < hours; h++ {
			at := s.fromDay.Add(time.Duration(h) * time.Hour)
			recs = append(recs, store.UsageRecord{
				CustomerID: c.ID, SourceID: src.ID, ResourceID: "vm-" + slug, ResourceKind: "ecs",
				SKU: "ecs.m7n.xlarge.8", Quantity: "1.000000", Unit: "instance-hour",
				WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-215",
			})
		}
		if _, err := st.UpsertUsage(ctx, recs); err != nil {
			t.Fatal(err)
		}
		return c
	}
	s.one, s.two = mkPartner("one", "One"), mkPartner("two", "Two")
	s.oneA = mkCustomer("one-a", "One A", &s.one, 10)
	s.oneB = mkCustomer("one-b", "One B", &s.one, 20)
	s.twoA = mkCustomer("two-a", "Two A", &s.two, 40)
	s.twoB = mkCustomer("two-b", "Two B", &s.two, 80)
	s.direct = mkCustomer("direct", "Direct", nil, 160)
	s.oneTotal, s.twoTotal, s.directTotal = 30, 120, 160
	s.allCost = s.oneTotal + s.twoTotal + s.directTotal

	scopeOf := func(p store.Partner) store.Scope {
		t.Helper()
		ids, err := st.ExpandPartnerScope(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		// Its two customers plus its own party: three ids, not two.
		if len(ids) != 3 {
			t.Fatalf("%s scope = %v, want party + two customers", p.Slug, ids)
		}
		return store.CustomersScope(ids)
	}
	s.oneScope, s.twoScope = scopeOf(s.one), scopeOf(s.two)
	return s
}

func (s partnerCostSeed) window(groupBy string) store.CostQuery {
	return store.CostQuery{From: s.fromDay, To: s.toDay, Granularity: "day", GroupBy: groupBy}
}

// The explorer under a partner scope: its own two customers, their exact
// total, and nothing of the other partner's or of a direct customer's.
func TestIntegrationPartnerExploresAcrossItsOwnCustomers(t *testing.T) {
	st := testdb.Open(t)
	s := seedPartnerCost(t, st)
	ctx := context.Background()

	res, err := st.Explore(ctx, s.oneScope, s.window("customer"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Groups) != 2 {
		t.Fatalf("partner One groups = %+v, want its two customers", res.Groups)
	}
	keys := map[string]float64{}
	for _, g := range res.Groups {
		keys[g.Key] = f(g.Total)
	}
	if !near(keys[s.oneA.ID], 10) || !near(keys[s.oneB.ID], 20) {
		t.Fatalf("partner One by customer = %v, want %s=10 %s=20", keys, s.oneA.ID, s.oneB.ID)
	}
	if _, ok := keys[s.twoA.ID]; ok {
		t.Fatalf("partner One saw partner Two's customer: %v", keys)
	}
	if _, ok := keys[s.direct.ID]; ok {
		t.Fatalf("partner One saw a direct customer: %v", keys)
	}
	if !near(f(res.Total.Current), s.oneTotal) {
		t.Fatalf("partner One total = %s, want %v", res.Total.Current, s.oneTotal)
	}
	// The other partner's book is its own, and adds up to its own number.
	res, err = st.Explore(ctx, s.twoScope, s.window("customer"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Groups) != 2 || !near(f(res.Total.Current), s.twoTotal) {
		t.Fatalf("partner Two = %+v total %s, want two customers at %v", res.Groups, res.Total.Current, s.twoTotal)
	}
	// Grouped by something else, the confinement still holds: the SKU total
	// is the partner's total, not the Sovereign's.
	res, err = st.Explore(ctx, s.oneScope, s.window("sku"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Groups) != 1 || !near(f(res.Groups[0].Total), s.oneTotal) {
		t.Fatalf("partner One by sku = %+v, want one SKU at %v", res.Groups, s.oneTotal)
	}
	// The filter pickers are confined the same way.
	vals, err := st.DimensionValues(ctx, s.oneScope, s.window("customer"))
	if err != nil {
		t.Fatal(err)
	}
	if len(vals["customer"]) != 2 {
		t.Fatalf("partner One dimension values = %+v, want its two customers", vals["customer"])
	}
}

// Naming another partner's customer — by filter, or as the query's own
// customer — reaches nothing.
func TestIntegrationPartnerCannotNameAnotherPartnersCustomer(t *testing.T) {
	st := testdb.Open(t)
	s := seedPartnerCost(t, st)
	ctx := context.Background()

	// By filter: the predicate is an intersection, so the answer is empty —
	// never the partner's own rows relabelled, and never the other's.
	q := s.window("customer")
	q.Include = map[string][]string{"customer": {s.twoA.ID, s.direct.ID}}
	res, err := st.Explore(ctx, s.oneScope, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Groups) != 0 || !near(f(res.Total.Current), 0) {
		t.Fatalf("partner One filtering for Two's customer saw %+v total %s", res.Groups, res.Total.Current)
	}
	// As the query's own customer: refused, not quietly answered about its
	// own book (the scope narrows a question, it never rewrites one).
	q = s.window("none")
	q.CustomerID = s.twoA.ID
	if _, err := st.Explore(ctx, s.oneScope, q); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("partner One naming Two's customer: %v, want not found", err)
	}
	rq := store.ResourceQuery{From: s.fromDay, To: s.toDay, CustomerID: s.twoA.ID}
	if _, err := st.ListResources(ctx, s.oneScope, rq); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("partner One listing Two's resources: %v, want not found", err)
	}
	if _, err := st.DailyCostByCustomerKind(ctx, s.oneScope, s.twoA.ID, s.fromDay, s.toDay); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("partner One reading Two's daily cost: %v, want not found", err)
	}
	// One of its OWN, named, is that one alone — not its whole book.
	q = s.window("customer")
	q.CustomerID = s.oneB.ID
	res, err = st.Explore(ctx, s.oneScope, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Groups) != 1 || res.Groups[0].Key != s.oneB.ID || !near(f(res.Total.Current), 20) {
		t.Fatalf("partner One naming its own customer = %+v total %s, want One B at 20", res.Groups, res.Total.Current)
	}
}

// The operator still sees everything, and a customer principal still sees
// only itself: widening the scope to a set changed neither.
func TestIntegrationPartnerScopeLeavesOperatorAndCustomerUnchanged(t *testing.T) {
	st := testdb.Open(t)
	s := seedPartnerCost(t, st)
	ctx := context.Background()

	res, err := st.Explore(ctx, store.OperatorScope, s.window("customer"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Groups) != 5 || !near(f(res.Total.Current), s.allCost) {
		t.Fatalf("the operator sees %d groups totalling %s, want 5 at %v", len(res.Groups), res.Total.Current, s.allCost)
	}
	// One of the partner's customers, as itself: its own 20 and nothing else.
	res, err = st.Explore(ctx, store.CustomerScope(s.oneB.ID), s.window("customer"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Groups) != 1 || res.Groups[0].Key != s.oneB.ID || !near(f(res.Total.Current), 20) {
		t.Fatalf("One B as itself = %+v total %s, want itself at 20", res.Groups, res.Total.Current)
	}
	// Even naming its partner's other customer by filter.
	q := s.window("customer")
	q.Include = map[string][]string{"customer": {s.oneA.ID}}
	res, err = st.Explore(ctx, store.CustomerScope(s.oneB.ID), q)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Groups) != 0 {
		t.Fatalf("One B naming One A saw %+v", res.Groups)
	}
	// A direct customer is nobody's partner's: the operator alone sees it.
	res, err = st.Explore(ctx, store.CustomerScope(s.direct.ID), s.window("customer"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Groups) != 1 || !near(f(res.Total.Current), s.directTotal) {
		t.Fatalf("the direct customer = %+v total %s", res.Groups, res.Total.Current)
	}
	// An empty scope is still a bug upstream, never a wildcard.
	if _, err := st.Explore(ctx, store.Scope{}, s.window("customer")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("empty scope explored: %v", err)
	}
}

// The other cost surfaces the partner lens shows are confined by the same
// predicate: resources, the daily series anomalies are detected from, and
// the recommendation inputs.
func TestIntegrationPartnerResourcesAndAnomaliesAreConfined(t *testing.T) {
	st := testdb.Open(t)
	s := seedPartnerCost(t, st)
	ctx := context.Background()

	list, err := st.ListResources(ctx, s.oneScope, store.ResourceQuery{From: s.fromDay, To: s.toDay})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 2 {
		t.Fatalf("partner One resources = %d rows, want its two customers' one each", list.Total)
	}
	seen := map[string]bool{}
	for _, r := range list.Rows {
		seen[r.CustomerID] = true
	}
	if !seen[s.oneA.ID] || !seen[s.oneB.ID] || len(seen) != 2 {
		t.Fatalf("partner One resource customers = %v", seen)
	}
	if list.SumCost != "30.000000" {
		t.Fatalf("partner One resource cost = %s, want 30.000000", list.SumCost)
	}
	// The operator's list is every customer's.
	all, err := st.ListResources(ctx, store.OperatorScope, store.ResourceQuery{From: s.fromDay, To: s.toDay})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 5 {
		t.Fatalf("the operator's resources = %d rows, want 5", all.Total)
	}

	daily, err := st.DailyCostByCustomerKind(ctx, s.oneScope, "", s.fromDay, s.toDay)
	if err != nil {
		t.Fatal(err)
	}
	if len(daily) != 2 {
		t.Fatalf("partner One daily rows = %+v, want one per customer", daily)
	}
	sum := 0.0
	for _, d := range daily {
		if d.CustomerID != s.oneA.ID && d.CustomerID != s.oneB.ID {
			t.Fatalf("partner One daily carried %s", d.CustomerID)
		}
		sum += f(d.Cost)
	}
	if !near(sum, s.oneTotal) {
		t.Fatalf("partner One daily total = %v, want %v", sum, s.oneTotal)
	}

	// Recommendation inputs: the live estate and the source health a partner
	// may see are its customers', not the Sovereign's.
	live, err := st.LiveResources(ctx, s.oneScope, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 2 {
		t.Fatalf("partner One live resources = %+v, want two", live)
	}
	health, err := st.SourceHealths(ctx, s.oneScope, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(health) != 2 {
		t.Fatalf("partner One source healths = %+v, want two", health)
	}
	books, err := st.CustomerBooks(ctx, s.oneScope, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 3 {
		t.Fatalf("partner One customer books = %d, want its two customers and its party", len(books))
	}
}
