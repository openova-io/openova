package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// resetBillingSettings puts the single settings row back to its defaults
// when the test ends. testdb wipes the per-customer tables between tests
// but the settings row is configuration and survives, so a test that flips
// the rule must not leak it into the next one.
func resetBillingSettings(t *testing.T, st *store.Store) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := st.UpdateBillingSettings(context.Background(), store.DefaultBillingSettings()); err != nil {
			t.Errorf("reset billing settings: %v", err)
		}
	})
}

// The setting round-trips, normalizes, refuses an unknown rule, and survives
// a wiped row; the stackable column round-trips through every discount path
// (DESIGN.md §2.11).
func TestIntegrationBillingSettingsRoundTripAndStackableColumn(t *testing.T) {
	st := testdb.Open(t)
	resetBillingSettings(t, st)
	ctx := context.Background()

	got, err := st.GetBillingSettings(ctx)
	if err != nil || got.DiscountRule != store.DiscountRuleMostSpecific {
		t.Fatalf("fresh settings = %+v err=%v, want the seeded default %q", got, err, store.DiscountRuleMostSpecific)
	}
	for _, rule := range store.DiscountRules {
		upd, err := st.UpdateBillingSettings(ctx, store.BillingSettings{DiscountRule: rule})
		if err != nil || upd.DiscountRule != rule || upd.UpdatedAt.IsZero() {
			t.Fatalf("update to %s = %+v err=%v", rule, upd, err)
		}
		if again, _ := st.GetBillingSettings(ctx); again.DiscountRule != rule {
			t.Fatalf("read back after %s = %+v", rule, again)
		}
	}
	// Case and whitespace are not a different rule.
	if upd, err := st.UpdateBillingSettings(ctx, store.BillingSettings{DiscountRule: "  Stack "}); err != nil || upd.DiscountRule != store.DiscountRuleStack {
		t.Fatalf("normalized update = %+v err=%v", upd, err)
	}
	// An unknown rule is ErrInvalid and changes nothing.
	if _, err := st.UpdateBillingSettings(ctx, store.BillingSettings{DiscountRule: "average"}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("unknown rule = %v, want ErrInvalid", err)
	}
	if _, err := st.UpdateBillingSettings(ctx, store.BillingSettings{}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("empty rule = %v, want ErrInvalid", err)
	}
	if still, _ := st.GetBillingSettings(ctx); still.DiscountRule != store.DiscountRuleStack {
		t.Fatalf("a refused update changed the rule: %+v", still)
	}
	// A wiped row reads as the defaults and is re-created by the next update.
	if _, err := st.DB().ExecContext(ctx, `DELETE FROM billing_settings`); err != nil {
		t.Fatal(err)
	}
	if def, err := st.GetBillingSettings(ctx); err != nil || def.DiscountRule != store.DefaultDiscountRule {
		t.Fatalf("settings without a row = %+v err=%v", def, err)
	}
	if upd, err := st.UpdateBillingSettings(ctx, store.BillingSettings{DiscountRule: store.DiscountRuleCompound}); err != nil || upd.DiscountRule != store.DiscountRuleCompound {
		t.Fatalf("update after wipe = %+v err=%v", upd, err)
	}
	var n int
	if err := st.DB().QueryRowContext(ctx, `SELECT count(*) FROM billing_settings`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("settings rows = %d err=%v, want exactly one", n, err)
	}

	// The stackable column: default false, set on create, replaced on
	// update (PUT semantics), flipped alone, and refused for a missing id.
	plain, err := st.CreateDiscount(ctx, store.DiscountInput{Name: "Launch 10%", Kind: "percent", Value: "10"})
	if err != nil || plain.Stackable {
		t.Fatalf("plain discount = %+v err=%v", plain, err)
	}
	camp, err := st.CreateDiscount(ctx, store.DiscountInput{Name: "Campaign", Kind: "percent", Value: "5", Stackable: true})
	if err != nil || !camp.Stackable {
		t.Fatalf("stackable discount = %+v err=%v", camp, err)
	}
	all, err := st.ListAllDiscounts(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("all = %+v err=%v", all, err)
	}
	for _, d := range all {
		if d.Stackable != (d.ID == camp.ID) {
			t.Fatalf("list lost the flag: %+v", d)
		}
	}
	if upd, err := st.UpdateDiscount(ctx, camp.ID, store.DiscountInput{Name: "Campaign", Kind: "percent", Value: "5"}); err != nil || upd.Stackable {
		t.Fatalf("update without the flag must clear it (PUT): %+v err=%v", upd, err)
	}
	if err := st.SetDiscountStackable(ctx, camp.ID, true); err != nil {
		t.Fatal(err)
	}
	if d, _ := st.GetDiscount(ctx, camp.ID); !d.Stackable || d.Name != "Campaign" || d.Value != "5.000000" {
		t.Fatalf("flip touched other fields or did not stick: %+v", d)
	}
	if err := st.SetDiscountStackable(ctx, "00000000-0000-0000-0000-000000000000", true); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("flip on a missing discount = %v", err)
	}
}

// Every statement a run writes records the rule in force, and the frozen
// breakdown carries the superseded entries, so an issued bill can say which
// rule produced its numbers and why a discount did not add up.
func TestIntegrationStatementRecordsTheDiscountRule(t *testing.T) {
	st := testdb.Open(t)
	resetBillingSettings(t, st)
	s := seedLedger(t, st)
	ctx := context.Background()
	// Customer A in 2026-09: ECS 168 h × 0.5 = 84, EVS 16.8 → gross 100.8.
	global, err := st.CreateDiscount(ctx, store.DiscountInput{Name: "Everyone 30%", Kind: "percent", Value: "30"})
	if err != nil {
		t.Fatal(err)
	}
	ecs, err := st.CreateDiscount(ctx, store.DiscountInput{CustomerID: &s.a.ID, Name: "ECS 20%", Kind: "percent", Value: "20", SKU: "ecs.m7n.xlarge.8"})
	if err != nil {
		t.Fatal(err)
	}
	run := func(rule string) store.Statement {
		t.Helper()
		if _, err := st.UpdateBillingSettings(ctx, store.BillingSettings{DiscountRule: rule}); err != nil {
			t.Fatal(err)
		}
		results, err := rating.Run(ctx, st, "2026-09", s.a.ID)
		if err != nil || len(results) != 1 || results[0].Error != "" {
			t.Fatalf("run under %s = %+v err=%v", rule, results, err)
		}
		stmt, err := st.GetStatement(ctx, store.OperatorScope, results[0].StatementID)
		if err != nil {
			t.Fatal(err)
		}
		if stmt.DiscountRule != rule {
			t.Fatalf("statement under %s records rule %q", rule, stmt.DiscountRule)
		}
		list, err := st.ListStatements(ctx, store.OperatorScope, s.a.ID)
		if err != nil || len(list) != 1 || list[0].DiscountRule != rule {
			t.Fatalf("list under %s = %+v err=%v", rule, list, err)
		}
		return stmt
	}
	type detail struct {
		DiscountID   string  `json:"discount_id"`
		Amount       float64 `json:"amount"`
		SupersededBy string  `json:"superseded_by"`
	}
	details := func(stmt store.Statement) map[string]detail {
		t.Helper()
		var rows []detail
		if err := json.Unmarshal(stmt.DiscountDetail, &rows); err != nil {
			t.Fatalf("discount_detail %s: %v", stmt.DiscountDetail, err)
		}
		out := map[string]detail{}
		for _, r := range rows {
			out[r.DiscountID] = r
		}
		return out
	}

	// highest: 30 % on both meters (30.24); the ECS discount is on the bill
	// as superseded by the global one.
	stmt := run(store.DiscountRuleHighest)
	if stmt.DiscountTotal != "30.240000" || stmt.Subtotal != "70.560000" {
		t.Fatalf("highest: discount %s subtotal %s", stmt.DiscountTotal, stmt.Subtotal)
	}
	if d := details(stmt); d[global.ID].Amount != 30.24 || d[ecs.ID].Amount != 0 || d[ecs.ID].SupersededBy != global.ID {
		t.Fatalf("highest detail = %+v", d)
	}
	// most-specific: ECS 20 % (16.8) + EVS 30 % (5.04) = 21.84; nothing superseded.
	stmt = run(store.DiscountRuleMostSpecific)
	if stmt.DiscountTotal != "21.840000" || stmt.Subtotal != "78.960000" {
		t.Fatalf("most-specific: discount %s subtotal %s", stmt.DiscountTotal, stmt.Subtotal)
	}
	if d := details(stmt); d[global.ID].Amount != 5.04 || d[ecs.ID].Amount != 16.8 || d[global.ID].SupersededBy != "" || d[ecs.ID].SupersededBy != "" {
		t.Fatalf("most-specific detail = %+v", d)
	}
	// stack: ECS 50 % (42) + EVS 30 % (5.04) = 47.04.
	if stmt = run(store.DiscountRuleStack); stmt.DiscountTotal != "47.040000" {
		t.Fatalf("stack: discount %s", stmt.DiscountTotal)
	}
	// compound: ECS 1 − 0.8 × 0.7 = 44 % (36.96) + EVS 30 % (5.04) = 42.
	if stmt = run(store.DiscountRuleCompound); stmt.DiscountTotal != "42.000000" {
		t.Fatalf("compound: discount %s", stmt.DiscountTotal)
	}

	// A statement with no discount at all still states the rule in force.
	resB, err := rating.Run(ctx, st, "2026-09", s.b.ID)
	if err != nil || len(resB) != 1 || resB[0].Error != "" {
		t.Fatalf("run B = %+v err=%v", resB, err)
	}
	// B has only the global 30 % — one discount, every rule agrees — but the
	// bill still says which rule it was rated under.
	if stB, err := st.GetStatement(ctx, store.OperatorScope, resB[0].StatementID); err != nil || stB.DiscountRule != store.DiscountRuleCompound {
		t.Fatalf("B = %+v err=%v", stB, err)
	}
}
