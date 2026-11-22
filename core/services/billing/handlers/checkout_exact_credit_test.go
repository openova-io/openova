package handlers

// checkout_exact_credit_test.go — a checkout takes exactly the quoted amount.
//
// Three defects, each pinned here by a test that fails on the code before
// this file (the file uses no symbol that code lacks, so it runs there too):
//
//  1. Paying from credit debited the total ROUNDED UP to whole OMR
//     (pricedOrder.WholeOMR → order.AmountOMR → `-order.AmountOMR` on the
//     ledger) while the order row, the receipt and the BSS prepaid amount
//     held the exact baisa total: a 2.490 order took 3.000 of credit.
//  2. The voucher code was matched case-insensitively by the preview (it
//     upper-cased) and exactly by the redemption, so the page said
//     "covered" and the checkout answered "promo code not found".
//  3. Two checkouts of one customer could spend the same credit: the
//     balance was read outside any lock and CreditOnlyCheckout re-checked
//     nothing; and a voucher rollback deleted a grant another order had
//     already spent.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/openova-io/openova/core/services/billing/store"
)

// microPerBaisa is the ledger's exact unit per baisa (1 baisa = 1,000
// micro-OMR; 1 OMR = 1,000,000 micro-OMR).
const microPerBaisa = 1000

func micro(baisa int64) int64 { return baisa * microPerBaisa }

// fakeCatalogBaisa serves one plan priced in baisa (the catalog's money
// field since #6971), with the decimal OMR mirror beside it.
func fakeCatalogBaisa(t *testing.T, planID string, baisa int64) *httptest.Server {
	t.Helper()
	return fakeCatalogRows(t, []map[string]any{{
		"id": planID, "stripe_price_id": "", "price_omr": float64(baisa) / 1000, "price_baisa": baisa,
	}})
}

// expectRedeem programs RedeemPromoCode's transaction. typed is what the
// customer typed; stored is the code as promo_codes holds it — every
// statement after the lookup must use the stored form.
func expectRedeem(mock sqlmock.Sqlmock, custID, typed, stored string, creditOMR, maxRedemptions int) {
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(
		`FROM promo_codes WHERE upper(code) = upper($1)`,
	)).WithArgs(typed).
		WillReturnRows(sqlmock.NewRows([]string{
			"code", "credit_omr", "active", "max_redemptions", "times_redeemed", "deleted_at",
		}).AddRow(stored, creditOMR, true, maxRedemptions, 0, nil))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM promo_redemptions")).
		WithArgs(custID, stored).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO promo_redemptions")).
		WithArgs(custID, stored).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE promo_codes SET times_redeemed")).
		WithArgs(stored).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO credit_ledger (customer_id, amount_omr, reason)")).
		WithArgs(custID, creditOMR, "promo:"+stored).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}

// expectBalanceMicro programs the exact balance read (both ledger columns,
// in micro-OMR) — the checkout's routing read and the read under the lock.
func expectBalanceMicro(mock sqlmock.Sqlmock, custID string, balanceMicro int64) {
	mock.ExpectQuery(regexp.QuoteMeta(
		"SELECT COALESCE(CAST(SUM(amount_omr) AS BIGINT) * 1000000",
	)).WithArgs(custID).
		WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(balanceMicro))
}

// expectLockedBalance programs the customer row lock that serialises every
// spend of one customer's credit, and the balance read under it.
func expectLockedBalance(mock sqlmock.Sqlmock, custID string, balanceMicro int64) {
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM customers WHERE id = $1 FOR NO KEY UPDATE`)).
		WithArgs(custID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(custID))
	expectBalanceMicro(mock, custID, balanceMicro)
}

// expectCreditOnlyCheckout programs CreditOnlyCheckout: the lock, the balance
// under it, the order, the EXACT debit (-debitBaisa in amount_micro_omr) and
// the subscription.
func expectCreditOnlyCheckout(mock sqlmock.Sqlmock, custID string, lockedBalanceMicro int64, orderID string, debitBaisa int64, subID string) {
	mock.ExpectBegin()
	expectLockedBalance(mock, custID, lockedBalanceMicro)
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO orders")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(orderID, time.Now()))
	if debitBaisa > 0 {
		mock.ExpectExec(regexp.QuoteMeta(
			"INSERT INTO credit_ledger (customer_id, amount_omr, amount_micro_omr, reason, order_id, created_at)",
		)).WithArgs(custID, -micro(debitBaisa), "order-payment", orderID).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO subscriptions")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).
			AddRow(subID, time.Now(), time.Now()))
	mock.ExpectCommit()
}

// expectGrantSurroundings programs the voucher rollback's decision under the
// customer lock: the balance before the grant and the non-metering movement
// since (micro-OMR), which decide whether an order spent the grant.
func expectGrantSurroundings(mock sqlmock.Sqlmock, custID, code string, beforeMicro, afterMicro int64) {
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM customers WHERE id = $1 FOR NO KEY UPDATE`)).
		WithArgs(custID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(custID))
	mock.ExpectQuery(regexp.QuoteMeta(`WITH g AS (`)).
		WithArgs(custID, "promo:"+code).
		WillReturnRows(sqlmock.NewRows([]string{"before", "after"}).AddRow(beforeMicro, afterMicro))
}

func expectNoStripe(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta(
		"SELECT stripe_secret_key, stripe_webhook_secret, stripe_public_key, updated_at",
	)).WillReturnRows(sqlmock.NewRows([]string{
		"stripe_secret_key", "stripe_webhook_secret", "stripe_public_key", "updated_at",
	}).AddRow("", "", "", time.Now()))
}

// creditLeftBaisa reads credit_balance_baisa from a checkout answer.
func creditLeftBaisa(t *testing.T, rec *httptest.ResponseRecorder) int64 {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	v, ok := body["credit_balance_baisa"].(float64)
	if !ok {
		return -1
	}
	return int64(v)
}

// TestCheckout_CreditPaysExactlyTheQuotedTotal: plan S costs 2.490 OMR and the
// customer holds a 5 OMR voucher. The ledger is debited exactly 2.490 (not 3),
// the answer reports 2.510 left, and the order row carries 2490 baisa.
//
// Fails before the fix: the debit was `-3` in amount_omr (whole OMR rounded
// up) and the answer said 2 OMR left.
func TestCheckout_CreditPaysExactlyTheQuotedTotal(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	catalog := fakeCatalogBaisa(t, "plan-s", 2490)
	defer catalog.Close()
	h := &Handler{Store: store.New(db), CatalogURL: catalog.URL}

	expectCustomer(mock, "user-exact", "cust-exact", "tenant-exact")
	expectRedeem(mock, "cust-exact", "VCH-EXACT-0000001", "VCH-EXACT-0000001", 5, 1)
	expectBalanceMicro(mock, "cust-exact", micro(5000))
	expectCreditOnlyCheckout(mock, "cust-exact", micro(5000), "order-exact", 2490, "sub-exact")

	rec := postCheckout(t, h, checkoutRequest{
		PlanID: "plan-s", TenantID: "tenant-exact", PromoCode: "VCH-EXACT-0000001",
	}, "user-exact")
	if rec.Code != http.StatusOK {
		raw, _ := io.ReadAll(rec.Body)
		t.Fatalf("want 200, got %d (body=%s)", rec.Code, raw)
	}
	var resp checkoutResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.PaidByCredit {
		t.Fatalf("paid_by_credit: want true (%+v)", resp)
	}
	if got := creditLeftBaisa(t, rec); got != 2510 {
		t.Errorf("credit_balance_baisa: want 2510 (5.000 - 2.490), got %d", got)
	}
	if resp.QuoteResponse == nil || resp.AmountBaisa != 2490 {
		t.Errorf("amount_baisa: want 2490, got %+v", resp.QuoteResponse)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("store interactions (the debit must be -2,490,000 micro-OMR, not -3 OMR): %v", err)
	}
}

// TestCheckout_ExactCreditCoversAnOrderTheRoundedDebitRefused: after the first
// S order the customer holds 2.510; a second 2.490 order is covered and leaves
// 0.020. Before the fix the first debit took 3 and the balance was read in
// whole OMR, so this order fell through to the Stripe path.
func TestCheckout_ExactCreditCoversAnOrderTheRoundedDebitRefused(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	catalog := fakeCatalogBaisa(t, "plan-s", 2490)
	defer catalog.Close()
	h := &Handler{Store: store.New(db), CatalogURL: catalog.URL}

	expectCustomer(mock, "user-second", "cust-second", "tenant-second")
	expectBalanceMicro(mock, "cust-second", micro(2510))
	expectCreditOnlyCheckout(mock, "cust-second", micro(2510), "order-second", 2490, "sub-second")

	rec := postCheckout(t, h, checkoutRequest{PlanID: "plan-s", TenantID: "tenant-second"}, "user-second")
	if rec.Code != http.StatusOK {
		raw, _ := io.ReadAll(rec.Body)
		t.Fatalf("want 200 (2.510 of credit covers 2.490), got %d (body=%s)", rec.Code, raw)
	}
	if got := creditLeftBaisa(t, rec); got != 20 {
		t.Errorf("credit_balance_baisa: want 20, got %d", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("store interactions: %v", err)
	}
}

// TestCheckout_LowerCaseVoucherRedeemsTheStoredCode: issue stores VCH-… upper
// case; the customer types it in lower case. The preview accepts it, so the
// checkout must too, and every later statement (the redemption row, the
// counter, the ledger grant) uses the STORED code.
//
// Fails before the fix: the redemption looked the typed string up exactly
// (`WHERE code = $1`) and answered 400 "promo code not found".
func TestCheckout_LowerCaseVoucherRedeemsTheStoredCode(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	catalog := fakeCatalogBaisa(t, "plan-s", 2490)
	defer catalog.Close()
	h := &Handler{Store: store.New(db), CatalogURL: catalog.URL}

	expectCustomer(mock, "user-case", "cust-case", "tenant-case")
	expectRedeem(mock, "cust-case", "vch-abcdefgh2345", "VCH-ABCDEFGH2345", 5, 1)
	expectBalanceMicro(mock, "cust-case", micro(5000))
	expectCreditOnlyCheckout(mock, "cust-case", micro(5000), "order-case", 2490, "sub-case")

	rec := postCheckout(t, h, checkoutRequest{
		PlanID: "plan-s", TenantID: "tenant-case", PromoCode: "  vch-abcdefgh2345 ",
	}, "user-case")
	if rec.Code != http.StatusOK {
		raw, _ := io.ReadAll(rec.Body)
		t.Fatalf("want 200 (the preview accepted this code), got %d (body=%s)", rec.Code, raw)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("store interactions: %v", err)
	}
}

// TestRedeemVoucherPreview_MatchesByTheSameRuleAsRedemption: the preview sends
// the typed code to the same case-insensitive lookup the checkout redeems by —
// not an upper-cased copy to an exact one — so a code the legacy admin upsert
// stored in lower case is found by both, not by the checkout alone.
func TestRedeemVoucherPreview_MatchesByTheSameRuleAsRedemption(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	h := &Handler{Store: store.New(db)}
	mock.ExpectQuery(regexp.QuoteMeta(
		`FROM promo_codes WHERE upper(code) = upper($1) AND deleted_at IS NULL`,
	)).WithArgs("legacy-lower-code-01").
		WillReturnRows(sqlmock.NewRows([]string{
			"code", "credit_omr", "description", "plan_tier", "active", "max_redemptions", "times_redeemed", "created_at", "deleted_at",
		}).AddRow("legacy-lower-code-01", 5, "", "", true, 0, 0, time.Now(), nil))

	r := httptest.NewRequest(http.MethodPost, "/billing/vouchers/redeem-preview",
		strings.NewReader(`{"code":" legacy-lower-code-01 "}`))
	rec := httptest.NewRecorder()
	h.RedeemVoucherPreview(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("store interactions: %v", err)
	}
}

// TestCheckout_CreditSpentConcurrently_NoOrderAnd409: the routing read saw
// 5.000, but by the time this checkout holds the customer's lock a concurrent
// checkout has spent 4.490 of it (0.510 left). Nothing is written and the
// answer is 409.
//
// Fails before the fix: CreditOnlyCheckout took no lock and re-read nothing,
// so both checkouts committed a completed order against one credit.
func TestCheckout_CreditSpentConcurrently_NoOrderAnd409(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	catalog := fakeCatalogBaisa(t, "plan-m", 4490)
	defer catalog.Close()
	h := &Handler{Store: store.New(db), CatalogURL: catalog.URL}

	expectCustomer(mock, "user-race", "cust-race", "tenant-race")
	expectBalanceMicro(mock, "cust-race", micro(5000))
	mock.ExpectBegin()
	expectLockedBalance(mock, "cust-race", micro(510)) // the other order took 4.490
	mock.ExpectRollback()

	rec := postCheckout(t, h, checkoutRequest{PlanID: "plan-m", TenantID: "tenant-race"}, "user-race")
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("store interactions (no order may be inserted): %v", err)
	}
}

// TestCheckout_VoucherRollbackKeepsCreditAnotherOrderSpent: request A redeems
// voucher X (+10) but, by the time it reads the balance, request B has spent
// those 10 on its own order; A is not covered and the Sovereign has no Stripe,
// so A rolls the voucher back. Taking the +10 grant back now would leave B's
// order paid with credit that no longer exists (balance -10). The rollback
// must see that and leave the redemption standing.
//
// Fails before the fix: the rollback deleted the grant unconditionally.
func TestCheckout_VoucherRollbackKeepsCreditAnotherOrderSpent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	catalog := fakeCatalogBaisa(t, "plan-l", 7990)
	defer catalog.Close()
	h := &Handler{Store: store.New(db), CatalogURL: catalog.URL}

	expectCustomer(mock, "user-a", "cust-ab", "tenant-a")
	expectRedeem(mock, "cust-ab", "VCH-RACE-00000001", "VCH-RACE-00000001", 10, 1)
	expectBalanceMicro(mock, "cust-ab", 0) // B already spent the 10
	expectNoStripe(mock)
	// The rollback: the redemption row is deleted inside the tx, then the
	// ledger under the lock shows nothing before the grant and B's 10.000
	// order since → the grant is spent, the tx is rolled back and nothing
	// changes.
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(
		`DELETE FROM promo_redemptions WHERE customer_id = $1 AND code = $2`)).
		WithArgs("cust-ab", "VCH-RACE-00000001").
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectGrantSurroundings(mock, "cust-ab", "VCH-RACE-00000001", 0, -10_000_000)
	mock.ExpectRollback()

	rec := postCheckout(t, h, checkoutRequest{
		PlanID: "plan-l", TenantID: "tenant-a", PromoCode: "VCH-RACE-00000001",
	}, "user-a")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (not covered, no Stripe), got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("store interactions (the spent grant must not be deleted): %v", err)
	}
}

// TestCheckout_VoucherRollbackRestoresTheVoucherOfACustomerInDebt: the
// customer owes 0.300 from post-paid metering, redeems a 5 OMR voucher and
// orders plan L (7.990). 4.700 does not cover it and Stripe is not configured,
// so the checkout answers 503 and no order exists — the voucher must be given
// back: nothing was paid since the grant, so the debt from before it is not an
// order spending it.
//
// Fails with a balance rule (balance − grant < 0): 4.700 − 5.000 < 0 kept the
// redemption, the grant and times_redeemed for an order that never existed,
// and a retry answered "already redeemed by this customer" (TBD-V9 / #2000).
func TestCheckout_VoucherRollbackRestoresTheVoucherOfACustomerInDebt(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	catalog := fakeCatalogBaisa(t, "plan-l", 7990)
	defer catalog.Close()
	h := &Handler{Store: store.New(db), CatalogURL: catalog.URL}

	expectCustomer(mock, "user-d", "cust-debt", "tenant-d")
	expectRedeem(mock, "cust-debt", "VCH-DEBT-00000001", "VCH-DEBT-00000001", 5, 1)
	expectBalanceMicro(mock, "cust-debt", 4_700_000) // -0.300 + 5.000
	expectNoStripe(mock)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(
		`DELETE FROM promo_redemptions WHERE customer_id = $1 AND code = $2`)).
		WithArgs("cust-debt", "VCH-DEBT-00000001").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// -0.300 before the grant, no order since.
	expectGrantSurroundings(mock, "cust-debt", "VCH-DEBT-00000001", -300_000, 0)
	mock.ExpectExec(regexp.QuoteMeta(
		`UPDATE promo_codes
		   SET times_redeemed = GREATEST(times_redeemed - 1, 0)
		 WHERE code = $1`)).
		WithArgs("VCH-DEBT-00000001").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(
		`DELETE FROM credit_ledger
		  WHERE customer_id = $1
		    AND reason = $2
		    AND order_id IS NULL`)).
		WithArgs("cust-debt", "promo:VCH-DEBT-00000001").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	rec := postCheckout(t, h, checkoutRequest{
		PlanID: "plan-l", TenantID: "tenant-d", PromoCode: "VCH-DEBT-00000001",
	}, "user-d")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (not covered, no Stripe), got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("store interactions (the voucher of a customer in debt must be rolled back): %v", err)
	}
}
