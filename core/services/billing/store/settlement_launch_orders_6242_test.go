// settlement_launch_orders_6242_test.go — #6242.
//
// ListOrdersAwaitingLaunch is the durable driver of the settlement-launch
// reconciler: it decides which paid Organizations get their lost launch call
// re-offered. Its whole value is in what it EXCLUDES, so it is tested against a
// real Postgres rather than sqlmock — a sqlmock expectation would assert the
// literal query string, which is the shape that cannot tell a working WHERE
// clause from a typo'd one. Same reasoning as voucher_integration_test.go, and
// the same BILLING_TEST_PG_URL gate (skipped, never mocked, when unset).
package store

import (
	"context"
	"testing"
	"time"
)

// mkOrderAt inserts an order and then back-dates its created_at, so a test can
// place a row on either side of the lookback cutoff. CreateOrder always stamps
// created_at server-side, which is correct for production and useless for
// exercising a time window, hence the explicit UPDATE.
func mkOrderAt(t *testing.T, s *Store, custID, tenantID, status string, age time.Duration) *Order {
	t.Helper()
	ctx := context.Background()
	o := &Order{
		CustomerID: custID,
		TenantID:   tenantID,
		PlanID:     "plan-m",
		AmountOMR:  10,
		Status:     status,
	}
	if err := s.CreateOrder(ctx, o); err != nil {
		t.Fatalf("create order (tenant=%q status=%q): %v", tenantID, status, err)
	}
	if age > 0 {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE orders SET created_at = $1 WHERE id = $2`, time.Now().Add(-age), o.ID); err != nil {
			t.Fatalf("backdate order %s: %v", o.ID, err)
		}
	}
	return o
}

func tenantIDsOf(orders []Order) map[string]int {
	out := map[string]int{}
	for _, o := range orders {
		out[o.TenantID]++
	}
	return out
}

// TestListOrdersAwaitingLaunch_ReturnsSettledAndExcludesTheRest is the store
// half of #6242.
//
// The CONTROLS all share the suspect property — every one of them is a real
// row in the same `orders` table, inserted by the same CreateOrder, differing
// from the subject in exactly ONE dimension. So a query that "returns orders"
// cannot pass: it has to return the settled, tenant-bearing, recent ones and
// no others.
//
//	subject   — completed, has a tenant, recent            => MUST appear
//	control A — completed, has a tenant, older than lookback => must NOT appear
//	control B — pending (customer never paid), recent        => must NOT appear
//	control C — completed but no tenant (no Org to launch)   => must NOT appear
//
// Control B is the one that matters most: returning it would make the
// reconciler launch Organizations for orders that were never settled, turning a
// recovery sweep into a way to get a free Organization by abandoning checkout.
func TestListOrdersAwaitingLaunch_ReturnsSettledAndExcludesTheRest(t *testing.T) {
	s, _, cleanup := freshSchema(t)
	defer cleanup()
	ctx := context.Background()

	cust := mkCustomer(t, s, "awaiting-launch")

	subject := mkOrderAt(t, s, cust.ID, "tenant-subject", "completed", 0)
	stale := mkOrderAt(t, s, cust.ID, "tenant-stale", "completed", 48*time.Hour)
	unpaid := mkOrderAt(t, s, cust.ID, "tenant-unpaid", "pending", 0)
	orphan := mkOrderAt(t, s, cust.ID, "", "completed", 0)

	got, err := s.ListOrdersAwaitingLaunch(ctx, 24*time.Hour, 100)
	if err != nil {
		t.Fatalf("ListOrdersAwaitingLaunch: %v", err)
	}
	byTenant := tenantIDsOf(got)

	if byTenant["tenant-subject"] != 1 {
		t.Errorf("settled recent order %s (tenant-subject) not returned: got %+v", subject.ID, byTenant)
	}
	if byTenant["tenant-stale"] != 0 {
		t.Errorf("order %s older than the lookback was returned — the window does not filter", stale.ID)
	}
	if byTenant["tenant-unpaid"] != 0 {
		t.Errorf("UNPAID order %s was returned — the sweep would launch an Organization the customer never paid for", unpaid.ID)
	}
	if byTenant[""] != 0 {
		t.Errorf("order %s with no tenant was returned — the sweep would POST a malformed launch URL", orphan.ID)
	}
}

// TestListOrdersAwaitingLaunch_LookbackIsHonoured pins that the window is a
// real parameter and not a constant baked into the SQL: the SAME stale row that
// is excluded at a 24h lookback must be INCLUDED at 72h. A query ignoring its
// argument passes the test above and fails this one.
func TestListOrdersAwaitingLaunch_LookbackIsHonoured(t *testing.T) {
	s, _, cleanup := freshSchema(t)
	defer cleanup()
	ctx := context.Background()

	cust := mkCustomer(t, s, "lookback-window")
	mkOrderAt(t, s, cust.ID, "tenant-48h", "completed", 48*time.Hour)

	narrow, err := s.ListOrdersAwaitingLaunch(ctx, 24*time.Hour, 100)
	if err != nil {
		t.Fatalf("narrow lookback: %v", err)
	}
	if n := tenantIDsOf(narrow)["tenant-48h"]; n != 0 {
		t.Fatalf("48h-old order returned at a 24h lookback (got %d)", n)
	}

	wide, err := s.ListOrdersAwaitingLaunch(ctx, 72*time.Hour, 100)
	if err != nil {
		t.Fatalf("wide lookback: %v", err)
	}
	if n := tenantIDsOf(wide)["tenant-48h"]; n != 1 {
		t.Fatalf("48h-old order NOT returned at a 72h lookback (got %d) — the lookback argument is ignored", n)
	}
}

// TestListOrdersAwaitingLaunch_LimitCapsTheBatch pins the batch cap, which is
// what keeps a backlog from becoming a thundering herd against the tenant
// service.
func TestListOrdersAwaitingLaunch_LimitCapsTheBatch(t *testing.T) {
	s, _, cleanup := freshSchema(t)
	defer cleanup()
	ctx := context.Background()

	cust := mkCustomer(t, s, "batch-cap")
	for i := 0; i < 5; i++ {
		mkOrderAt(t, s, cust.ID, "tenant-batch-"+string(rune('a'+i)), "completed", 0)
	}

	got, err := s.ListOrdersAwaitingLaunch(ctx, 24*time.Hour, 2)
	if err != nil {
		t.Fatalf("ListOrdersAwaitingLaunch: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("limit=2 returned %d rows — the batch cap is not applied", len(got))
	}
}
