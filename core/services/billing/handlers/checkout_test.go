package handlers

// Tests for POST /billing/checkout focused on the "voucher covers 100%
// of the order" short-circuit. The bug: when a voucher (promo code)
// redeemed in the same request supplies credit equal to or greater
// than the order total, the handler MUST skip Stripe entirely and
// settle the order from credit. Before the fix the handler fell
// through to the "payment processor not configured" 503 because the
// `creditBalance >= totalOMR` branch was being side-stepped on some
// settings-paths. The explicit `remainingOMR := totalOMR -
// creditBalance; if remainingOMR <= 0` guard makes the short-circuit
// unconditional and order-independent of Stripe settings.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/golang-jwt/jwt/v5"

	"github.com/openova-io/openova/core/services/billing/store"
	"github.com/openova-io/openova/core/services/shared/middleware"
)

// withCustomerClaims attaches a normal-customer JWT-claims context so the
// Checkout handler (which calls UserIDFromContext, not the operator-only
// requireVoucherIssuer guard) accepts the request.
func withCustomerClaims(r *http.Request, userID, email string) *http.Request {
	ctx := middleware.WithClaims(r.Context(), jwt.MapClaims{
		"sub":   userID,
		"email": email,
	})
	return r.WithContext(ctx)
}

// fakeCatalogServer returns an httptest.Server that serves the minimal
// `/catalog/plans` shape computeOrderTotal expects. Tests point
// h.CatalogURL at this server's URL.
func fakeCatalogServer(t *testing.T, planID string, priceOMR int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/catalog/plans", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": planID, "stripe_price_id": "", "price_omr": priceOMR},
		})
	})
	return httptest.NewServer(mux)
}

// TestCheckout_VoucherCoversFullTotal_SkipsStripe is the regression test
// for "Marketplace checkout returns 503 when voucher already covers 100%
// of total". A customer with no prior credit redeems a promo code worth
// exactly the plan price; Stripe is NOT configured in this Sovereign yet
// (StripeSecretKey == ""). Before the fix the handler returned 503; after
// it must complete the order from credit, dispatch the in-tx settlement,
// and return 200 with `paid_by_credit: true`. The fact that no Stripe
// settings probe runs is the load-bearing assertion — we assert it by
// NEVER programming a SELECT against the billing_settings table.
func TestCheckout_VoucherCoversFullTotal_SkipsStripe(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	catalog := fakeCatalogServer(t, "plan-starter", 50)
	defer catalog.Close()

	h := &Handler{
		Store:      store.New(db),
		CatalogURL: catalog.URL,
		// Producer left nil — dispatchOrderPlaced no-ops when there's
		// no broker wired, which is what the "Stripe not configured
		// yet" Sovereigns also do during voucher-only weeks.
	}

	// 1. GetCustomerByUserID — pre-existing customer (skip CreateCustomer).
	mock.ExpectQuery(regexp.QuoteMeta(
		"SELECT id, user_id, tenant_id, stripe_customer_id, email, created_at",
	)).WithArgs("user-acme-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "tenant_id", "stripe_customer_id", "email", "created_at",
		}).AddRow("cust-acme", "user-acme-1", "tenant-acme", nil, "ops@acme.test", time.Now()))

	// 2. computeOrderTotal hits catalog over HTTP (mocked above) → 50 OMR.

	// 3. RedeemPromoCode — full transaction.
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(
		"SELECT credit_omr, active, max_redemptions, times_redeemed, deleted_at",
	)).WithArgs("WELCOME50").
		WillReturnRows(sqlmock.NewRows([]string{
			"credit_omr", "active", "max_redemptions", "times_redeemed", "deleted_at",
		}).AddRow(50, true, 0, 0, nil))
	mock.ExpectQuery(regexp.QuoteMeta(
		"SELECT COUNT(*) FROM promo_redemptions",
	)).WithArgs("cust-acme", "WELCOME50").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(regexp.QuoteMeta(
		"INSERT INTO promo_redemptions",
	)).WithArgs("cust-acme", "WELCOME50").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(
		"UPDATE promo_codes SET times_redeemed",
	)).WithArgs("WELCOME50").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(
		"INSERT INTO credit_ledger (customer_id, amount_omr, reason)",
	)).WithArgs("cust-acme", 50, "promo:WELCOME50").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	// 4. GetCreditBalance — returns 50 (the just-redeemed credit).
	mock.ExpectQuery(regexp.QuoteMeta(
		"SELECT COALESCE(CAST(SUM(amount_omr) AS BIGINT)",
	)).WithArgs("cust-acme").
		WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(int64(50)))

	// 5. CreditOnlyCheckout — the short-circuit path. Order INSERT,
	//    credit-spend ledger row, subscription INSERT, COMMIT. NO
	//    settings probe and NO Stripe call.
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(
		"INSERT INTO orders",
	)).WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).
		AddRow("order-acme-1", time.Now()))
	mock.ExpectExec(regexp.QuoteMeta(
		"INSERT INTO credit_ledger (customer_id, amount_omr, reason, order_id)",
	)).WithArgs("cust-acme", -50, "order-payment", "order-acme-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(
		"INSERT INTO subscriptions",
	)).WillReturnRows(sqlmock.NewRows([]string{
		"id", "created_at", "updated_at",
	}).AddRow("sub-acme-1", time.Now(), time.Now()))
	mock.ExpectCommit()

	body, _ := json.Marshal(checkoutRequest{
		PlanID:    "plan-starter",
		TenantID:  "tenant-acme",
		PromoCode: "WELCOME50",
	})
	req := httptest.NewRequest(http.MethodPost, "/billing/checkout", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withCustomerClaims(req, "user-acme-1", "ops@acme.test")

	rec := httptest.NewRecorder()
	h.Checkout(rec, req)

	if rec.Code != http.StatusOK {
		raw, _ := io.ReadAll(rec.Body)
		t.Fatalf("want 200, got %d (body=%s)", rec.Code, string(raw))
	}

	var resp checkoutResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.PaidByCredit {
		t.Errorf("paid_by_credit: want true, got false (response=%+v)", resp)
	}
	if resp.OrderID != "order-acme-1" {
		t.Errorf("order_id: want order-acme-1, got %q", resp.OrderID)
	}
	if resp.SessionURL != "" {
		t.Errorf("session_url: want empty (Stripe must be skipped), got %q", resp.SessionURL)
	}
	if resp.CreditBalance != 0 {
		t.Errorf("credit_balance: want 0 (full voucher consumed by order), got %d",
			resp.CreditBalance)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		// A failure here typically means the handler probed
		// billing_settings (the 503 path) instead of short-circuiting
		// — exactly the regression this test guards.
		t.Fatalf("unexpected store interactions (regression — handler likely fell through to Stripe path): %v", err)
	}
}

// TestCheckout_PreExistingCreditCoversTotal_SkipsStripe locks in the
// other half of the short-circuit contract: a customer whose ledger
// balance ALREADY covers the order (no fresh promo redemption) must
// also settle without Stripe. This is the path operators see when a
// previously-issued voucher was redeemed in an earlier session and the
// customer is now upgrading to a plan it fully covers.
func TestCheckout_PreExistingCreditCoversTotal_SkipsStripe(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	catalog := fakeCatalogServer(t, "plan-pro", 100)
	defer catalog.Close()

	h := &Handler{Store: store.New(db), CatalogURL: catalog.URL}

	// GetCustomerByUserID — has 200 OMR of standing credit.
	mock.ExpectQuery(regexp.QuoteMeta(
		"SELECT id, user_id, tenant_id, stripe_customer_id, email, created_at",
	)).WithArgs("user-acme-2").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "tenant_id", "stripe_customer_id", "email", "created_at",
		}).AddRow("cust-acme-2", "user-acme-2", "tenant-acme-2", nil, "ops2@acme.test", time.Now()))

	// No promo code in request → no RedeemPromoCode queries.

	// GetCreditBalance — 200 OMR standing balance, totalOMR is 100.
	mock.ExpectQuery(regexp.QuoteMeta(
		"SELECT COALESCE(CAST(SUM(amount_omr) AS BIGINT)",
	)).WithArgs("cust-acme-2").
		WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(int64(200)))

	// CreditOnlyCheckout.
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO orders")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).
			AddRow("order-acme-2", time.Now()))
	mock.ExpectExec(regexp.QuoteMeta(
		"INSERT INTO credit_ledger (customer_id, amount_omr, reason, order_id)",
	)).WithArgs("cust-acme-2", -100, "order-payment", "order-acme-2").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO subscriptions")).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "created_at", "updated_at",
		}).AddRow("sub-acme-2", time.Now(), time.Now()))
	mock.ExpectCommit()

	body, _ := json.Marshal(checkoutRequest{
		PlanID:   "plan-pro",
		TenantID: "tenant-acme-2",
	})
	req := httptest.NewRequest(http.MethodPost, "/billing/checkout", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withCustomerClaims(req, "user-acme-2", "ops2@acme.test")

	rec := httptest.NewRecorder()
	h.Checkout(rec, req)

	if rec.Code != http.StatusOK {
		raw, _ := io.ReadAll(rec.Body)
		t.Fatalf("want 200, got %d (body=%s)", rec.Code, string(raw))
	}

	var resp checkoutResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.PaidByCredit {
		t.Errorf("paid_by_credit: want true, got false")
	}
	if resp.CreditBalance != 100 {
		// 200 standing - 100 order = 100 leftover.
		t.Errorf("credit_balance: want 100 leftover, got %d", resp.CreditBalance)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected store interactions: %v", err)
	}
}
