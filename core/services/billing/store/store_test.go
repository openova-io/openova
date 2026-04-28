package store

import (
	"context"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestCreateInvoice_BaisaAndOMRPersisted is the regression test for #78.
//
// Before the fix, the invoice row stored `inv.AmountPaid` (baisa) directly
// into `amount_omr` — so a 50 OMR Stripe invoice (AmountPaid = 50000 baisa)
// landed as amount_omr = 50000, reading back as "50000 OMR" (1000x overcharge)
// anywhere the integer OMR view was consumed directly.
//
// After the fix, AmountBaisa is the canonical value, AmountOMR is derived
// (floor(baisa/1000)), and both columns are written on INSERT. A 50 OMR
// invoice must land as amount_baisa=50000, amount_omr=50.
func TestCreateInvoice_BaisaAndOMRPersisted(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock new: %v", err)
	}
	defer db.Close()

	s := New(db)

	// 50 OMR invoice arriving from Stripe webhook = 50000 baisa.
	inv := &Invoice{
		CustomerID:      "cust-uuid",
		TenantID:        "tenant-42",
		StripeInvoiceID: "in_test_50omr",
		AmountBaisa:     50000,
		AmountOMR:       50, // derived by caller
		Currency:        "omr",
		Status:          "paid",
	}

	rows := sqlmock.NewRows([]string{"id", "created_at"}).AddRow("inv-uuid", time.Now())
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO invoices`)).
		WithArgs(
			"cust-uuid", "tenant-42",
			sqlmock.AnyArg(), // stripe_invoice_id (pointer via nilIfEmpty)
			50,               // amount_omr
			int64(50000),     // amount_baisa
			"omr",            // currency
			"paid",           // status
			sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(),
		).
		WillReturnRows(rows)

	if err := s.CreateInvoice(context.Background(), inv); err != nil {
		t.Fatalf("CreateInvoice: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}

	if inv.AmountBaisa != 50000 {
		t.Errorf("want AmountBaisa=50000, got %d", inv.AmountBaisa)
	}
	if inv.AmountOMR != 50 {
		t.Errorf("want AmountOMR=50, got %d", inv.AmountOMR)
	}
}

// TestCreateInvoice_DerivesMissingFields locks the default-derivation behavior
// that keeps legacy callers (who only set AmountOMR) working, and ensures
// Stripe webhook callers (who set AmountBaisa) get AmountOMR filled in.
func TestCreateInvoice_DerivesMissingFields(t *testing.T) {
	cases := []struct {
		name           string
		in             Invoice
		wantOMR        int
		wantBaisa      int64
		wantCurrency   string
	}{
		{
			name:         "baisa-only input (Stripe webhook path)",
			in:           Invoice{AmountBaisa: 5750}, // 5.750 OMR
			wantOMR:      5,
			wantBaisa:    5750,
			wantCurrency: "omr",
		},
		{
			name:         "omr-only input (legacy path)",
			in:           Invoice{AmountOMR: 12},
			wantOMR:      12,
			wantBaisa:    12000,
			wantCurrency: "omr",
		},
		{
			name:         "both set — no overwrite",
			in:           Invoice{AmountOMR: 50, AmountBaisa: 50000, Currency: "omr"},
			wantOMR:      50,
			wantBaisa:    50000,
			wantCurrency: "omr",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock new: %v", err)
			}
			defer db.Close()

			s := New(db)
			rows := sqlmock.NewRows([]string{"id", "created_at"}).AddRow("id", time.Now())
			mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO invoices`)).
				WillReturnRows(rows)

			inv := tc.in
			if err := s.CreateInvoice(context.Background(), &inv); err != nil {
				t.Fatalf("CreateInvoice: %v", err)
			}
			if inv.AmountOMR != tc.wantOMR {
				t.Errorf("AmountOMR: got %d, want %d", inv.AmountOMR, tc.wantOMR)
			}
			if inv.AmountBaisa != tc.wantBaisa {
				t.Errorf("AmountBaisa: got %d, want %d", inv.AmountBaisa, tc.wantBaisa)
			}
			if inv.Currency != tc.wantCurrency {
				t.Errorf("Currency: got %q, want %q", inv.Currency, tc.wantCurrency)
			}
		})
	}
}

// TestBaisaToOMR_And_OMRToBaisa checks the conversion helpers produce the
// expected millibaisa-precision results used at the API boundary.
func TestBaisaToOMR_And_OMRToBaisa(t *testing.T) {
	cases := []struct {
		baisa int64
		omr   float64
	}{
		{0, 0.0},
		{1, 0.001},
		{500, 0.5},
		{1000, 1.0},
		{5750, 5.75},
		{50000, 50.0},
	}
	for _, c := range cases {
		if got := BaisaToOMR(c.baisa); got != c.omr {
			t.Errorf("BaisaToOMR(%d) = %v, want %v", c.baisa, got, c.omr)
		}
	}

	if got := OMRToBaisa(50); got != 50000 {
		t.Errorf("OMRToBaisa(50) = %d, want 50000", got)
	}
	if got := OMRToBaisa(0); got != 0 {
		t.Errorf("OMRToBaisa(0) = %d, want 0", got)
	}
}

// TestMarkWebhookEventProcessed_FirstDeliveryThenDuplicate is the regression
// test for #77. The first delivery of a Stripe event must report fresh=true;
// any subsequent delivery of the same event_id must report fresh=false so
// the handler short-circuits and no side effects run twice.
func TestMarkWebhookEventProcessed_FirstDeliveryThenDuplicate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock new: %v", err)
	}
	defer db.Close()

	s := New(db)

	// First delivery — insert succeeds, 1 row affected.
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO stripe_webhook_events`)).
		WithArgs("evt_test_abc", "invoice.paid").
		WillReturnResult(sqlmock.NewResult(0, 1))

	fresh, err := s.MarkWebhookEventProcessed(context.Background(), "evt_test_abc", "invoice.paid")
	if err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if !fresh {
		t.Fatal("first delivery: want fresh=true, got false")
	}

	// Duplicate delivery — ON CONFLICT DO NOTHING returns 0 rows affected.
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO stripe_webhook_events`)).
		WithArgs("evt_test_abc", "invoice.paid").
		WillReturnResult(sqlmock.NewResult(0, 0))

	fresh, err = s.MarkWebhookEventProcessed(context.Background(), "evt_test_abc", "invoice.paid")
	if err != nil {
		t.Fatalf("duplicate delivery: %v", err)
	}
	if fresh {
		t.Fatal("duplicate delivery: want fresh=false, got true")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}

// TestMarkWebhookEventProcessed_EmptyIDRejected fails closed: if Stripe
// somehow delivers an event with no ID, we refuse to process rather than
// risk silent double-processing.
func TestMarkWebhookEventProcessed_EmptyIDRejected(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock new: %v", err)
	}
	defer db.Close()

	s := New(db)
	fresh, err := s.MarkWebhookEventProcessed(context.Background(), "", "invoice.paid")
	if err == nil {
		t.Fatal("expected error for empty event_id, got nil")
	}
	if fresh {
		t.Fatal("expected fresh=false on error")
	}
}

// TestCreateOrder_DerivesBaisaFromOMR ensures the existing credit-settled
// checkout path (which uses whole-OMR ints) still writes a correct
// amount_baisa column value, so downstream baisa-aware consumers see the
// right number.
func TestCreateOrder_DerivesBaisaFromOMR(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock new: %v", err)
	}
	defer db.Close()

	s := New(db)
	rows := sqlmock.NewRows([]string{"id", "created_at"}).AddRow("order-uuid", time.Now())
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO orders`)).
		WithArgs(
			"cust", "tenant", "plan",
			sqlmock.AnyArg(), sqlmock.AnyArg(),
			42,           // amount_omr
			int64(42000), // amount_baisa
			"pending",
			sqlmock.AnyArg(), // stripe_session_id
			sqlmock.AnyArg(), // promo_code (#91)
		).
		WillReturnRows(rows)

	o := &Order{
		CustomerID: "cust", TenantID: "tenant", PlanID: "plan",
		AmountOMR: 42, Status: "pending",
	}
	if err := s.CreateOrder(context.Background(), o); err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if o.AmountBaisa != 42000 {
		t.Errorf("AmountBaisa: got %d, want 42000", o.AmountBaisa)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}

// TestDeletePromoCode_SoftDelete is the regression for #91.
//
// The previous implementation ran DELETE on promo_codes AND promo_redemptions
// in a single tx, which destroyed the audit trail of who had redeemed the
// code. After the fix, DeletePromoCode must:
//   - issue a single UPDATE that sets deleted_at = now() and active = false
//   - NOT touch promo_redemptions (so past redemptions stay visible)
//   - NOT issue DELETE against promo_codes
//   - return sql.ErrNoRows when the code is missing or already deleted
func TestDeletePromoCode_SoftDelete(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock new: %v", err)
	}
	defer db.Close()
	s := New(db)

	mock.ExpectExec(regexp.QuoteMeta(
		`UPDATE promo_codes SET deleted_at = now(), active = false
		 WHERE code = $1 AND deleted_at IS NULL`)).
		WithArgs("WELCOME-2026").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := s.DeletePromoCode(context.Background(), "WELCOME-2026"); err != nil {
		t.Fatalf("DeletePromoCode: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}

// TestDeletePromoCode_AlreadyGoneReturnsNoRows ensures deleting a non-existent
// or already-soft-deleted code returns sql.ErrNoRows so the HTTP handler can
// map it to 404, matching the legacy hard-delete contract.
func TestDeletePromoCode_AlreadyGoneReturnsNoRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock new: %v", err)
	}
	defer db.Close()
	s := New(db)

	mock.ExpectExec(regexp.QuoteMeta(`UPDATE promo_codes SET deleted_at`)).
		WithArgs("NEVER-EXISTED").
		WillReturnResult(sqlmock.NewResult(0, 0))

	err = s.DeletePromoCode(context.Background(), "NEVER-EXISTED")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// Caller checks errors.Is(err, sql.ErrNoRows) via the existing handler
	// code path. Compare directly since DeletePromoCode returns the sentinel.
	if err.Error() != "sql: no rows in result set" {
		t.Errorf("want sql.ErrNoRows, got %q", err.Error())
	}
}

// TestRedeemPromoCode_SoftDeletedRejected locks in the #91 contract on the
// redemption side: a code whose deleted_at is set must behave as if it does
// not exist, returning "promo code not found" and NOT incrementing
// times_redeemed.
func TestRedeemPromoCode_SoftDeletedRejected(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock new: %v", err)
	}
	defer db.Close()
	s := New(db)

	mock.ExpectBegin()
	deletedAt := time.Now().Add(-time.Hour)
	mock.ExpectQuery(regexp.QuoteMeta(
		`SELECT credit_omr, active, max_redemptions, times_redeemed, deleted_at
		 FROM promo_codes WHERE code = $1 FOR UPDATE`)).
		WithArgs("RETIRED").
		WillReturnRows(sqlmock.NewRows([]string{"credit_omr", "active", "max_redemptions", "times_redeemed", "deleted_at"}).
			AddRow(10, true, 100, 5, deletedAt))
	// The UPDATE incrementing times_redeemed must NOT happen.
	mock.ExpectRollback()

	credit, err := s.RedeemPromoCode(context.Background(), "cust-id", "RETIRED")
	if err == nil {
		t.Fatal("expected error for soft-deleted promo, got nil")
	}
	if credit != 0 {
		t.Errorf("want credit=0 on rejection, got %d", credit)
	}
	if err.Error() != "promo code not found" {
		t.Errorf("want 'promo code not found' (parity with non-existent codes), got %q", err.Error())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}

// TestCreditOnlyCheckout_CommitsAllThreeWrites is the happy-path regression
// for #92: the three DB writes (order, ledger, subscription) must run inside
// one transaction and all commit together.
func TestCreditOnlyCheckout_CommitsAllThreeWrites(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock new: %v", err)
	}
	defer db.Close()
	s := New(db)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO orders`)).
		WithArgs(
			"cust", "tenant", "plan",
			sqlmock.AnyArg(), sqlmock.AnyArg(), // apps, addons
			9, int64(9000), // amount_omr, amount_baisa
			"completed",
			sqlmock.AnyArg(), sqlmock.AnyArg(), // stripe_session_id (nil), promo_code (nil)
		).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow("order-id", time.Now()))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO credit_ledger`)).
		WithArgs("cust", -9, "order-payment", "order-id").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO subscriptions`)).
		WithArgs("cust", "tenant", sqlmock.AnyArg(), "plan", "active", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).
			AddRow("sub-id", time.Now(), time.Now()))
	mock.ExpectCommit()

	order := &Order{CustomerID: "cust", TenantID: "tenant", PlanID: "plan", AmountOMR: 9, Status: "completed"}
	sub := &Subscription{CustomerID: "cust", TenantID: "tenant", PlanID: "plan", Status: "active"}
	if err := s.CreditOnlyCheckout(context.Background(), order, sub); err != nil {
		t.Fatalf("CreditOnlyCheckout: %v", err)
	}
	if order.ID != "order-id" {
		t.Errorf("order.ID not populated: %q", order.ID)
	}
	if sub.ID != "sub-id" {
		t.Errorf("sub.ID not populated: %q", sub.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}

// TestCreditOnlyCheckout_RollsBackOnSubscriptionFailure is the core of #92 —
// if the subscription insert fails, the order insert AND the credit spend
// must be rolled back. Before the fix this was three separate DB calls and a
// failure on the last one left the customer debited for a subscription that
// never got created.
func TestCreditOnlyCheckout_RollsBackOnSubscriptionFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock new: %v", err)
	}
	defer db.Close()
	s := New(db)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO orders`)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow("order-id", time.Now()))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO credit_ledger`)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Third write fails — the transaction must roll back.
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO subscriptions`)).
		WillReturnError(fmt.Errorf("simulated constraint violation"))
	mock.ExpectRollback()

	order := &Order{CustomerID: "cust", TenantID: "tenant", PlanID: "plan", AmountOMR: 12, Status: "completed"}
	sub := &Subscription{CustomerID: "cust", TenantID: "tenant", PlanID: "plan", Status: "active"}
	err = s.CreditOnlyCheckout(context.Background(), order, sub)
	if err == nil {
		t.Fatal("expected error when subscription insert fails, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}

// TestCreditOnlyCheckout_ZeroTotalSkipsLedger ensures a fully-discounted
// order (e.g. promo covers a free-tier plan) does not insert a ledger row
// with amount 0, matching SpendCredit's own no-op behaviour.
func TestCreditOnlyCheckout_ZeroTotalSkipsLedger(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock new: %v", err)
	}
	defer db.Close()
	s := New(db)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO orders`)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow("order-id", time.Now()))
	// No ledger insert expected for AmountOMR = 0.
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO subscriptions`)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).
			AddRow("sub-id", time.Now(), time.Now()))
	mock.ExpectCommit()

	order := &Order{CustomerID: "cust", TenantID: "tenant", PlanID: "plan", AmountOMR: 0, Status: "completed"}
	sub := &Subscription{CustomerID: "cust", TenantID: "tenant", PlanID: "plan", Status: "active"}
	if err := s.CreditOnlyCheckout(context.Background(), order, sub); err != nil {
		t.Fatalf("CreditOnlyCheckout: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}

// TestListPromoCodes_ExcludesSoftDeleted checks the read side of #91: the
// admin listing hides tombstones so "delete" in the UI appears to work.
func TestListPromoCodes_ExcludesSoftDeleted(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock new: %v", err)
	}
	defer db.Close()
	s := New(db)

	// The query must filter WHERE deleted_at IS NULL.
	mock.ExpectQuery(regexp.QuoteMeta(
		`SELECT code, credit_omr, description, active, max_redemptions, times_redeemed, created_at, deleted_at
		 FROM promo_codes WHERE deleted_at IS NULL ORDER BY created_at DESC`)).
		WillReturnRows(sqlmock.NewRows([]string{
			"code", "credit_omr", "description", "active",
			"max_redemptions", "times_redeemed", "created_at", "deleted_at",
		}).AddRow("LIVE", 10, "", true, 100, 5, time.Now(), nil))

	out, err := s.ListPromoCodes(context.Background())
	if err != nil {
		t.Fatalf("ListPromoCodes: %v", err)
	}
	if len(out) != 1 || out[0].Code != "LIVE" {
		t.Fatalf("unexpected list: %+v", out)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}
