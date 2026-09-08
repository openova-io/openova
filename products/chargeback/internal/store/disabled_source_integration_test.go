package store_test

import (
	"context"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// A DISABLED source is a decommission, not a deletion (coordinator direction
// 2026-09-08): the collector skips it and it counts as neither verified nor
// live, but its collected history is billing data — it still rates, still
// appears in the explorer, and still stands on the statement already issued
// from it.
//
// The test disables the cloud source of the two-layer seed AFTER issuing a
// statement, so both halves are checked on the same rows: what was already
// billed does not move, and nothing new is collected.
func TestIntegrationDisabledSourceKeepsItsHistoryAndCollectsNothing(t *testing.T) {
	st := testdb.Open(t)
	s := seedTwoLayer(t, st)
	ctx := context.Background()
	from, to := day(2026, 8, 1), day(2026, 8, 8)

	// The cloud source is verified and carries a credential, so it is one of
	// the collector's sources before anything is disabled.
	if err := st.SetSourceVerified(ctx, s.cloudSrc.ID, "dom-1"); err != nil {
		t.Fatal(err)
	}
	cred, err := st.CreateCredential(ctx, s.cust.ID, "AKLIVE", []byte("sealed"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSourceCredential(ctx, s.cloudSrc.ID, cred.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSourceVerified(ctx, s.cloudSrc.ID, "dom-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetCustomerStatus(ctx, s.cust.ID, "active"); err != nil {
		t.Fatal(err)
	}
	// The platform source is verified the way OrgSync verifies it (nothing
	// external to check), so "verified" has two members before the disable.
	if err := st.SetSourceVerified(ctx, s.platSrc.ID, ""); err != nil {
		t.Fatal(err)
	}
	// One live inventory row, so the live-resource count has something to lose.
	if _, err := st.UpsertInventory(ctx, s.cloudSrc.ID, []store.InventoryUpsert{
		{ResourceID: "vm-1", Kind: "ecs", Name: "web-1", Attrs: map[string]any{"status": "ACTIVE"}, Created: day(2026, 7, 1), SeenAt: day(2026, 8, 7)},
	}); err != nil {
		t.Fatal(err)
	}
	collectable := func() bool {
		t.Helper()
		live, err := st.ListVerifiedSources(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range live {
			if x.ID == s.cloudSrc.ID {
				return true
			}
		}
		return false
	}
	if !collectable() {
		t.Fatal("the seeded cloud source must be collectable before it is disabled")
	}

	// The statement issued while it was live: 84 cloud + 42 plan = 126.
	results, err := rating.Run(ctx, st, "2026-08", s.cust.ID)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := st.IssueStatement(ctx, results[0].StatementID)
	if err != nil {
		t.Fatal(err)
	}
	if string(issued.Subtotal) != "126.000000" || issued.Status != "issued" {
		t.Fatalf("issued statement = %s %s", issued.Subtotal, issued.Status)
	}

	before, err := st.LiveResourceCount(ctx, store.OperatorScope, s.cust.ID)
	if err != nil || before != 1 {
		t.Fatalf("live resources before = %d err=%v, want 1", before, err)
	}
	// ── Disable it ──
	disabled, err := st.SetSourceDisabled(ctx, s.cloudSrc.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Status != store.StatusDisabled {
		t.Fatalf("status after disable = %q", disabled.Status)
	}

	// 1. It collects nothing more: the collector's source list drops it.
	if collectable() {
		t.Fatal("a disabled source is still in ListVerifiedSources — the collector would keep collecting")
	}

	// 2. It counts as neither verified nor live.
	c, err := st.GetCustomer(ctx, store.OperatorScope, s.cust.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.VerifiedSourceCount != 1 {
		t.Fatalf("verified sources = %d, want only the platform source", c.VerifiedSourceCount)
	}
	if c.SourceCount != 2 || c.CloudSourceCount != 1 {
		t.Fatalf("a disabled source must still be listed: %+v", c)
	}
	if live, err := st.LiveResourceCount(ctx, store.OperatorScope, s.cust.ID); err != nil || live != 0 {
		t.Fatalf("live resources after disable = %d err=%v, want 0", live, err)
	}
	if byStatus, err := st.SourceStatusCounts(ctx); err != nil || byStatus[store.StatusDisabled] != 1 || byStatus["verified"] != 1 {
		t.Fatalf("source status counts = %v err=%v", byStatus, err)
	}

	// 3. Its history still rates: the explorer total is unchanged and the
	//    source still carries its 84.
	ex, err := st.Explore(ctx, store.OperatorScope, store.CostQuery{From: from, To: to, Granularity: "day", GroupBy: "source", Metric: "cost"})
	if err != nil {
		t.Fatal(err)
	}
	if string(ex.Total.Current) != "126.000000" {
		t.Fatalf("explorer total after disable = %s, want the unchanged 126.000000", ex.Total.Current)
	}
	got := map[string]string{}
	for _, g := range ex.Groups {
		got[g.Key] = string(g.Total)
	}
	if got[s.cloudSrc.ID] != "84.000000" {
		t.Fatalf("the disabled source lost its history in the explorer: %v", got)
	}

	// 4. The statement already issued from it is untouched, lines included.
	still, err := st.GetStatement(ctx, store.OperatorScope, issued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(still.Subtotal) != "126.000000" || still.Status != "issued" {
		t.Fatalf("issued statement changed after disabling its source: %s %s", still.Subtotal, still.Status)
	}
	var cloudLine bool
	for _, l := range still.Lines {
		if l.SourceID != nil && *l.SourceID == s.cloudSrc.ID && string(l.Amount) == "84.000000" {
			cloudLine = true
		}
	}
	if !cloudLine {
		t.Fatalf("the disabled source's line vanished from the issued statement: %+v", still.Lines)
	}
	// A fresh run of a LATER period rates only what is there; the disabled
	// source's own history still rates in its own period.
	rerun, err := rating.Run(ctx, st, "2026-08", s.cust.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rerun[0].Error == "" {
		t.Fatalf("re-running an issued period must be refused: %+v", rerun[0])
	}

	// ── Enable it again: it was verified before, so it returns verified ──
	back, err := st.SetSourceDisabled(ctx, s.cloudSrc.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if back.Status != "verified" {
		t.Fatalf("status after enable = %q, want verified (it had been verified)", back.Status)
	}
	if !collectable() {
		t.Fatal("an enabled source must be collectable again")
	}
	// A source that never verified comes back pending, not verified.
	fresh, _, err := st.UpsertSource(ctx, s.cust.ID, store.SourceKindHuaweiProject, "me-east-216", "proj-new")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSourceDisabled(ctx, fresh.ID, true); err != nil {
		t.Fatal(err)
	}
	again, err := st.SetSourceDisabled(ctx, fresh.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if again.Status != "pending" {
		t.Fatalf("a never-verified source came back as %q, want pending", again.Status)
	}
	// The internal platform source is maintained by the collector.
	if _, err := st.SetSourceDisabled(ctx, s.internal.ID, true); err == nil {
		t.Fatal("the internal platform source was disabled through the store")
	}
}
