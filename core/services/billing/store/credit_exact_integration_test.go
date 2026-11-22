package store

// credit_exact_integration_test.go — the credit a checkout takes, against a
// real Postgres (gated on BILLING_TEST_PG_URL like voucher_integration_test.go;
// CI's "Test — Billing Integration (real Postgres)" job provides it).
//
// sqlmock proves which statements run; only a real database proves what they
// add up to, how two transactions interleave, and that the SQL parses.

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// mkCredit grants whole-OMR credit the way a redeemed voucher does.
func mkCredit(t *testing.T, s *Store, custID, code string, omr int) {
	t.Helper()
	mkPromo(t, s, code, omr, 0)
	if _, err := redeem(context.Background(), s, custID, code); err != nil {
		t.Fatalf("redeem %s: %v", code, err)
	}
}

func creditOnlyOrder(custID string, baisa int64) (*Order, *Subscription) {
	return &Order{
			CustomerID: custID, TenantID: "tenant-" + custID, PlanID: "s",
			AmountOMR: int((baisa + 999) / 1000), AmountBaisa: baisa, Status: "completed",
		}, &Subscription{
			CustomerID: custID, TenantID: "tenant-" + custID, PlanID: "s", Status: "active",
		}
}

func balanceMicro(t *testing.T, s *Store, custID string) int64 {
	t.Helper()
	b, err := s.GetCreditBalanceMicroOMR(context.Background(), custID)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	return b
}

// A 2.490 order paid from 5.000 of credit leaves exactly 2.510; the whole-OMR
// view floors to 2 (it used to read 2 because the debit took 3).
func TestIntegration_CreditPaysExactlyTheOrderTotal(t *testing.T) {
	s, _, cleanup := freshSchema(t)
	defer cleanup()
	ctx := context.Background()
	cust := mkCustomer(t, s, "exact")
	mkCredit(t, s, cust.ID, "VCH-EXACT-IT-0001", 5)

	o, sub := creditOnlyOrder(cust.ID, 2490)
	if err := checkoutFromCredit(ctx, s, o, sub); err != nil {
		t.Fatalf("credit-only checkout: %v", err)
	}
	if got := balanceMicro(t, s, cust.ID); got != 2_510_000 {
		t.Errorf("balance after a 2.490 order on 5.000: want 2,510,000 micro-OMR, got %d", got)
	}
	whole, err := s.GetCreditBalance(ctx, cust.ID)
	if err != nil {
		t.Fatalf("whole balance: %v", err)
	}
	if whole != 2 {
		t.Errorf("whole-OMR view: want 2 (floor of 2.510), got %d", whole)
	}

	// The 2.510 left covers a second 2.490 order exactly.
	o2, sub2 := creditOnlyOrder(cust.ID, 2490)
	if err := checkoutFromCredit(ctx, s, o2, sub2); err != nil {
		t.Fatalf("second credit-only checkout (2.510 covers 2.490): %v", err)
	}
	if got := balanceMicro(t, s, cust.ID); got != 20_000 {
		t.Errorf("balance after two 2.490 orders: want 20,000 micro-OMR, got %d", got)
	}
}

// Two checkouts of one customer at the same time, each covered by the balance
// alone but not together: exactly one order is placed, and the balance never
// goes below zero.
func TestIntegration_ConcurrentCheckoutsCannotSpendTheSameCredit(t *testing.T) {
	s, _, cleanup := freshSchema(t)
	defer cleanup()
	ctx := context.Background()
	cust := mkCustomer(t, s, "race")
	mkCredit(t, s, cust.ID, "VCH-RACE-IT-0001", 5)

	const n = 4
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			o, sub := creditOnlyOrder(cust.ID, 4490)
			<-start
			errs[i] = checkoutFromCredit(ctx, s, o, sub)
		}(i)
	}
	close(start)
	wg.Wait()

	placed := 0
	for _, err := range errs {
		switch {
		case err == nil:
			placed++
		case insufficient(err):
		default:
			t.Errorf("unexpected checkout error: %v", err)
		}
	}
	if placed != 1 {
		t.Errorf("orders placed against 5.000 of credit at 4.490 each: want 1, got %d", placed)
	}
	if got := balanceMicro(t, s, cust.ID); got != 510_000 {
		t.Errorf("balance: want 510,000 micro-OMR (5.000 - 4.490), got %d", got)
	}
}

// A code issued upper-case is redeemed when typed lower-case, the preview's
// lookup finds it by the same rule, and the redemption records the STORED
// code (so the order row and a rollback refer to the row that exists).
func TestIntegration_VoucherCodeMatchesTheSameWayInPreviewAndRedemption(t *testing.T) {
	s, _, cleanup := freshSchema(t)
	defer cleanup()
	ctx := context.Background()
	cust := mkCustomer(t, s, "case")
	mkPromo(t, s, "VCH-CASE-IT-0001", 5, 1)

	p, err := s.GetPromoCode(ctx, "vch-case-it-0001")
	if err != nil || p == nil || p.Code != "VCH-CASE-IT-0001" {
		t.Fatalf("preview lookup of the lower-case code: got %+v, %v", p, err)
	}
	stored, err := redeem(ctx, s, cust.ID, "vch-case-it-0001")
	if err != nil {
		t.Fatalf("redeem of the lower-case code the preview accepted: %v", err)
	}
	if stored != "VCH-CASE-IT-0001" {
		t.Errorf("redeem: want the stored code VCH-CASE-IT-0001, got %q", stored)
	}
	if got := balanceMicro(t, s, cust.ID); got != 5_000_000 {
		t.Errorf("balance after redeem: want 5,000,000, got %d", got)
	}
	if err := s.RollbackPromoCodeRedemption(ctx, cust.ID, stored); err != nil {
		t.Fatalf("rollback by the stored code: %v", err)
	}
	if got := balanceMicro(t, s, cust.ID); got != 0 {
		t.Errorf("balance after rollback: want 0, got %d", got)
	}
}

// A voucher whose credit another order already spent is not taken back: the
// rollback refuses and leaves the redemption and the grant in place, so the
// balance never goes negative.
func TestIntegration_VoucherRollbackKeepsCreditAnotherOrderSpent(t *testing.T) {
	s, _, cleanup := freshSchema(t)
	defer cleanup()
	ctx := context.Background()
	cust := mkCustomer(t, s, "spent")
	mkCredit(t, s, cust.ID, "VCH-SPENT-IT-001", 10)

	o, sub := creditOnlyOrder(cust.ID, 10_000)
	if err := checkoutFromCredit(ctx, s, o, sub); err != nil {
		t.Fatalf("the other order spends the 10: %v", err)
	}
	err := s.RollbackPromoCodeRedemption(ctx, cust.ID, "VCH-SPENT-IT-001")
	if !creditSpent(err) {
		t.Errorf("rollback of a spent grant: want the credit-spent refusal, got %v", err)
	}
	if got := balanceMicro(t, s, cust.ID); got != 0 {
		t.Errorf("balance: want 0 (the order paid with the grant), got %d", got)
	}
}

// The calls whose result shape this file depends on, in one place.

func checkoutFromCredit(ctx context.Context, s *Store, o *Order, sub *Subscription) error {
	_, err := s.CreditOnlyCheckout(ctx, o, sub)
	return err
}

func redeem(ctx context.Context, s *Store, custID, typed string) (string, error) {
	_, stored, err := s.RedeemPromoCode(ctx, custID, typed)
	return stored, err
}

func insufficient(err error) bool { return errors.Is(err, ErrCreditInsufficient) }

func creditSpent(err error) bool { return errors.Is(err, ErrPromoCreditSpent) }
