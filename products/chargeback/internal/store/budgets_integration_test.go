package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

func TestIntegrationBudgetsCRUDAndScope(t *testing.T) {
	st := testdb.Open(t)
	s := seedLedger(t, st)
	ctx := context.Background()

	global, err := st.CreateBudget(ctx, store.BudgetInput{Name: "Sovereign cap", Amount: "1000", Thresholds: []int{50, 80, 100}, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if global.CustomerID != nil || global.CustomerName != nil || global.Currency != "OMR" || global.Period != "monthly" || string(global.Amount) != "1000.000000" {
		t.Fatalf("global = %+v", global)
	}
	if len(global.Thresholds) != 3 || global.Thresholds[2] != 100 || len(global.NotifyEmails) != 0 || global.NotifyEmails == nil {
		t.Fatalf("global arrays = %v / %v", global.Thresholds, global.NotifyEmails)
	}
	acme, err := st.CreateBudget(ctx, store.BudgetInput{Name: "Acme cap", CustomerID: &s.a.ID, Amount: "200", Currency: "usd", Thresholds: []int{50, 80}, NotifyEmails: []string{"fin@acme.example"}, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if acme.CustomerID == nil || *acme.CustomerID != s.a.ID || acme.CustomerName == nil || *acme.CustomerName != "Acme" || acme.Currency != "USD" {
		t.Fatalf("acme = %+v", acme)
	}

	// Operator sees both; A sees only its own; B sees nothing; an empty
	// customer scope is a bug upstream, not a wildcard.
	all, err := st.ListBudgets(ctx, store.OperatorScope)
	if err != nil || len(all) != 2 {
		t.Fatalf("operator list = %d err=%v", len(all), err)
	}
	mine, err := st.ListBudgets(ctx, store.CustomerScope(s.a.ID))
	if err != nil || len(mine) != 1 || mine[0].ID != acme.ID {
		t.Fatalf("A list = %+v err=%v", mine, err)
	}
	if theirs, err := st.ListBudgets(ctx, store.CustomerScope(s.b.ID)); err != nil || len(theirs) != 0 {
		t.Fatalf("B list = %+v err=%v", theirs, err)
	}
	if _, err := st.ListBudgets(ctx, store.Scope{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("empty scope err = %v", err)
	}
	// Get: the global budget is outside every customer scope; A's is outside B's.
	if _, err := st.GetBudget(ctx, store.CustomerScope(s.a.ID), global.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("A reading the global budget: %v", err)
	}
	if _, err := st.GetBudget(ctx, store.CustomerScope(s.b.ID), acme.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("B reading A's budget: %v", err)
	}
	if got, err := st.GetBudget(ctx, store.CustomerScope(s.a.ID), acme.ID); err != nil || got.ID != acme.ID {
		t.Fatalf("A reading its own: %+v %v", got, err)
	}
	if _, err := st.GetBudget(ctx, store.OperatorScope, "not-a-uuid"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("bad id err = %v", err)
	}

	// Update replaces every field; re-pointing to another customer re-joins the name.
	upd, err := st.UpdateBudget(ctx, acme.ID, store.BudgetInput{Name: "Bravo cap", CustomerID: &s.b.ID, Amount: "10.5", Currency: "OMR", Thresholds: []int{90}, NotifyEmails: []string{"x@bravo.example", "y@bravo.example"}, Active: false})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Name != "Bravo cap" || *upd.CustomerID != s.b.ID || *upd.CustomerName != "Bravo" || string(upd.Amount) != "10.500000" || len(upd.Thresholds) != 1 || upd.Thresholds[0] != 90 || len(upd.NotifyEmails) != 2 || upd.Active {
		t.Fatalf("updated = %+v", upd)
	}
	if !upd.UpdatedAt.After(acme.UpdatedAt) && !upd.UpdatedAt.Equal(acme.UpdatedAt) {
		t.Fatalf("updated_at went backwards: %v → %v", acme.UpdatedAt, upd.UpdatedAt)
	}
	if _, err := st.UpdateBudget(ctx, "00000000-0000-0000-0000-000000000000", store.BudgetInput{Name: "x", Amount: "1"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update missing err = %v", err)
	}
	// An unknown customer is a FK violation → not found, never a 500.
	bogus := "00000000-0000-0000-0000-000000000000"
	if _, err := st.CreateBudget(ctx, store.BudgetInput{Name: "x", CustomerID: &bogus, Amount: "1", Active: true}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("create with unknown customer err = %v", err)
	}
	// A negative amount is refused by the CHECK constraint.
	if _, err := st.CreateBudget(ctx, store.BudgetInput{Name: "x", Amount: "-1", Active: true}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("negative amount err = %v", err)
	}

	// Only the global budget is still active.
	active, err := st.ListActiveBudgets(ctx)
	if err != nil || len(active) != 1 || active[0].ID != global.ID {
		t.Fatalf("active = %+v err=%v", active, err)
	}

	// Alerts: the first record inserts, the second is a no-op; the alert
	// disappears with its budget.
	ins, err := st.RecordBudgetAlert(ctx, global.ID, "2026-09", 50, "512.5")
	if err != nil || !ins {
		t.Fatalf("first record = %v err=%v", ins, err)
	}
	ins, err = st.RecordBudgetAlert(ctx, global.ID, "2026-09", 50, "600")
	if err != nil || ins {
		t.Fatalf("second record = %v err=%v (must be a no-op)", ins, err)
	}
	if ins, _ := st.RecordBudgetAlert(ctx, global.ID, "2026-10", 50, "1"); !ins {
		t.Fatal("a new period is a new crossing")
	}
	if ins, _ := st.RecordBudgetAlert(ctx, global.ID, "2026-09", 80, "800"); !ins {
		t.Fatal("a new threshold is a new crossing")
	}
	alerts, err := st.ListBudgetAlerts(ctx, global.ID)
	if err != nil || len(alerts) != 3 {
		t.Fatalf("alerts = %+v err=%v", alerts, err)
	}
	for _, a := range alerts {
		if a.Period == "2026-09" && a.Threshold == 50 && string(a.Actual) != "512.500000" {
			t.Fatalf("the first record's actual must be kept, got %s", a.Actual)
		}
	}
	if _, err := st.RecordBudgetAlert(ctx, bogus, "2026-09", 50, "1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("alert for a missing budget err = %v", err)
	}
	if err := st.DeleteBudget(ctx, global.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteBudget(ctx, global.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete err = %v", err)
	}
	if alerts, _ := st.ListBudgetAlerts(ctx, global.ID); len(alerts) != 0 {
		t.Fatalf("alerts survived the budget: %+v", alerts)
	}
	// Deleting the customer cascades to its budget (FK ON DELETE CASCADE).
	if _, err := st.DB().ExecContext(ctx, `DELETE FROM customers WHERE id = $1`, s.b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetBudget(ctx, store.OperatorScope, upd.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("budget survived its customer: %v", err)
	}
}
