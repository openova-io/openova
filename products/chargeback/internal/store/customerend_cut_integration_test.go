package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// A collection pass that ran between an Organization's end and its customer's
// close metered past the end. CutUsageAt takes it back: the hour the end fell
// in is cut to the instant with its quantity scaled, every later record is
// deleted, a sampled metric keeps its value, another source is untouched, and
// a second run changes nothing.
func TestIntegrationCutUsageAtTakesBackWhatWasMeteredAfterTheEnd(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	cust, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "acme", Name: "Acme", AdminEmail: "a@acme.example", Kind: "organization", OrgSlug: "acme", PlanSlug: "s", StartDate: "2026-11-01"})
	if err != nil {
		t.Fatal(err)
	}
	src, _, err := st.UpsertSource(ctx, cust.ID, store.SourceKindOrg, "", "acme")
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := st.UpsertSource(ctx, cust.ID, store.SourceKindHuaweiProject, "me-east-215", "proj-acme")
	if err != nil {
		t.Fatal(err)
	}
	ended := time.Date(2026, 11, 22, 1, 15, 45, 0, time.UTC)
	h := func(n int) time.Time { return time.Date(2026, 11, 22, n, 0, 0, 0, time.UTC) }
	rec := func(source, sku, qty string, from, to time.Time) store.UsageRecord {
		return store.UsageRecord{CustomerID: cust.ID, SourceID: source, ResourceID: "acme/web-0", ResourceKind: "k8s-pod", SKU: sku, Quantity: store.Decimal(qty), Unit: "h", WindowStart: from, WindowEnd: to}
	}
	// The pass in the gap ran to 01:16:05: the hour before the end in full,
	// the hour it fell in up to 01:16:05 (965 s = 0.268056 h), and a stray
	// record after it.
	gapEnd := ended.Add(20 * time.Second)
	if _, err := st.UpsertUsage(ctx, []store.UsageRecord{
		rec(src.ID, "plan.s", "1.000000", h(0), h(1)),
		rec(src.ID, "plan.s", "0.268056", h(1), gapEnd),
		rec(src.ID, "k8s.cpu.core_hours", "0.134028", h(1), gapEnd),
		rec(src.ID, store.SKUCPUUtil, "42.000000", h(1), gapEnd),
		rec(src.ID, "plan.s", "1.000000", h(2), h(3)),
		rec(other.ID, "ecs.m7n.xlarge.8", "1.000000", h(1), h(2)),
	}); err != nil {
		t.Fatal(err)
	}

	n, err := st.CutUsageAt(ctx, src.ID, ended)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("CutUsageAt touched %d records, want 4 (three cut, one deleted)", n)
	}
	recs, err := st.ListUsageRecords(ctx, src.ID, "acme/web-0")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]store.UsageRecord{}
	for _, r := range recs {
		if r.WindowEnd.After(ended) {
			t.Errorf("%s %s..%s still reaches past the end %s", r.SKU, r.WindowStart, r.WindowEnd, ended)
		}
		got[r.SKU+"@"+r.WindowStart.UTC().Format("15")] = r
	}
	if len(recs) != 4 {
		t.Fatalf("%d records left, want 4: %+v", len(recs), recs)
	}
	want := map[string]string{
		"plan.s@00":              "1.000000",  // before the end: untouched
		"plan.s@01":              "0.262500",  // 0.268056 × 945/965
		"k8s.cpu.core_hours@01":  "0.131250",  // 0.134028 × 945/965
		store.SKUCPUUtil + "@01": "42.000000", // a metric is not scaled
	}
	for k, q := range want {
		r, ok := got[k]
		if !ok {
			t.Errorf("%s missing", k)
			continue
		}
		if string(r.Quantity) != q {
			t.Errorf("%s quantity = %s, want %s", k, r.Quantity, q)
		}
	}
	if r := got["plan.s@01"]; !r.WindowEnd.Equal(ended) {
		t.Errorf("the hour the end fell in ends %s, want %s", r.WindowEnd, ended)
	}
	if left, err := st.ListUsageRecords(ctx, other.ID, "acme/web-0"); err != nil || len(left) != 1 || string(left[0].Quantity) != "1.000000" {
		t.Errorf("another source was touched: %+v (err %v)", left, err)
	}
	if n, err := st.CutUsageAt(ctx, src.ID, ended); err != nil || n != 0 {
		t.Errorf("a second cut touched %d records (err %v), want 0", n, err)
	}
}
