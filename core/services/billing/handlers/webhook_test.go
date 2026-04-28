package handlers

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stripe/stripe-go/v81/webhook"

	"github.com/openova-io/openova/core/services/billing/store"
)

// signedPayload produces a Stripe-Signature header the SDK's
// webhook.ConstructEvent will accept. The secret must match what
// GetSettings() returns from the mocked DB row.
func signedPayload(t *testing.T, body []byte, secret string) string {
	t.Helper()
	sp := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload:   body,
		Secret:    secret,
		Timestamp: time.Now(),
	})
	return sp.Header
}

// expectSettingsLookup mocks the `GetSettings` query so the handler finds the
// webhook secret it needs to validate the signature.
func expectSettingsLookup(mock sqlmock.Sqlmock, secret string) {
	mock.ExpectQuery(regexp.QuoteMeta(
		"SELECT stripe_secret_key, stripe_webhook_secret, stripe_public_key, updated_at",
	)).WillReturnRows(
		sqlmock.NewRows([]string{"stripe_secret_key", "stripe_webhook_secret", "stripe_public_key", "updated_at"}).
			AddRow("sk_test", secret, "pk_test", time.Now()),
	)
}

func newWebhookTestHandler(t *testing.T) (*Handler, sqlmock.Sqlmock, *sql.DB) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	return &Handler{Store: store.New(db)}, mock, db
}

// newCheckoutEventBody returns a Stripe event JSON body of type
// checkout.session.completed with the supplied event ID. The payload
// intentionally omits a Customer so the handler takes the early-return
// path after the order status is updated — that's enough to assert both
// the idempotency behavior and the store UPDATE contract without needing
// to mock every downstream query.
func newCheckoutEventBody(eventID, orderID, tenantID string) []byte {
	body := map[string]any{
		"id":   eventID,
		"type": "checkout.session.completed",
		"data": map[string]any{
			"object": map[string]any{
				"id": "cs_test_session",
				"metadata": map[string]string{
					"order_id":  orderID,
					"tenant_id": tenantID,
				},
			},
		},
	}
	b, _ := json.Marshal(body)
	return b
}

// TestWebhook_IdempotencyShortCircuitsDuplicate is the regression test for #77.
//
// Two identical webhook POSTs arrive. The FIRST should take the full
// processing path (insert event, update order, etc). The SECOND should see
// ON CONFLICT DO NOTHING (0 rows affected), short-circuit, and NOT perform
// any further store writes. Both calls return 200.
//
// We assert the second call issues NO additional queries beyond Settings +
// MarkWebhookEventProcessed by using sqlmock's ExpectationsWereMet: any
// unexpected UPDATE would fail the test.
func TestWebhook_IdempotencyShortCircuitsDuplicate(t *testing.T) {
	h, mock, db := newWebhookTestHandler(t)
	defer db.Close()

	const secret = "whsec_test_12345"
	body := newCheckoutEventBody("evt_dup_test", "order-abc", "tenant-xyz")

	// ---- FIRST delivery: full processing path ----
	expectSettingsLookup(mock, secret)
	// Idempotency insert: 1 row affected (fresh).
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO stripe_webhook_events`)).
		WithArgs("evt_dup_test", "checkout.session.completed").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Update order status — called because orderID is present.
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE orders SET status`)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// sess.Customer is nil, so the handler returns early.

	sig := signedPayload(t, body, secret)
	req1 := httptest.NewRequest(http.MethodPost, "/billing/webhook", bytes.NewReader(body))
	req1.Header.Set("Stripe-Signature", sig)
	rec1 := httptest.NewRecorder()
	h.Webhook(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("first delivery: want 200, got %d (body=%s)", rec1.Code, rec1.Body.String())
	}

	// ---- SECOND delivery: same event_id — must short-circuit ----
	expectSettingsLookup(mock, secret)
	// Idempotency insert: 0 rows affected (duplicate).
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO stripe_webhook_events`)).
		WithArgs("evt_dup_test", "checkout.session.completed").
		WillReturnResult(sqlmock.NewResult(0, 0))
	// NO further queries expected.

	sig2 := signedPayload(t, body, secret)
	req2 := httptest.NewRequest(http.MethodPost, "/billing/webhook", bytes.NewReader(body))
	req2.Header.Set("Stripe-Signature", sig2)
	rec2 := httptest.NewRecorder()
	h.Webhook(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("duplicate delivery: want 200, got %d", rec2.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("duplicate delivery ran unexpected queries: %v", err)
	}
}

// TestWebhook_ReturnsServerErrorOnStoreFailure is the regression test for #80.
//
// Before the fix, `_ = h.Store.UpdateOrderStatus(...)` discarded errors and
// the handler always returned 200 — Stripe thought it was done even though
// our DB was inconsistent. After the fix, a failing store write must
// propagate as 500 so Stripe retries, AND the idempotency record must be
// rolled back so the retry is not rejected as a duplicate.
func TestWebhook_ReturnsServerErrorOnStoreFailure(t *testing.T) {
	h, mock, db := newWebhookTestHandler(t)
	defer db.Close()

	const secret = "whsec_test_12345"
	body := newCheckoutEventBody("evt_err_test", "order-missing", "tenant-xyz")

	expectSettingsLookup(mock, secret)
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO stripe_webhook_events`)).
		WithArgs("evt_err_test", "checkout.session.completed").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// UpdateOrderStatus fails — simulated DB outage.
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE orders SET status`)).
		WillReturnError(fmt.Errorf("connection refused"))
	// Handler must clear the event row so Stripe's retry processes fresh.
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM stripe_webhook_events`)).
		WithArgs("evt_err_test").
		WillReturnResult(sqlmock.NewResult(0, 1))

	sig := signedPayload(t, body, secret)
	req := httptest.NewRequest(http.MethodPost, "/billing/webhook", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	rec := httptest.NewRecorder()
	h.Webhook(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 on store failure, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}

// TestWebhook_InvoicePaidStoresBaisa is the regression test for #78 at the
// handler boundary. A Stripe `invoice.paid` event for 50 OMR arrives as
// AmountPaid=50000 in baisa with currency=omr. The handler must pass that
// through to CreateInvoice with AmountBaisa=50000 and AmountOMR=50 (never
// AmountOMR=50000, which was the bug).
func TestWebhook_InvoicePaidStoresBaisa(t *testing.T) {
	h, mock, db := newWebhookTestHandler(t)
	defer db.Close()

	const secret = "whsec_test_12345"

	// Build a minimal stripe.Invoice event payload.
	body := []byte(`{
	  "id": "evt_inv_test",
	  "type": "invoice.paid",
	  "data": {
	    "object": {
	      "id": "in_test_50omr",
	      "customer": "cus_test_1",
	      "amount_paid": 50000,
	      "currency": "omr",
	      "period_start": 1700000000,
	      "period_end": 1702592000
	    }
	  }
	}`)

	expectSettingsLookup(mock, secret)
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO stripe_webhook_events`)).
		WithArgs("evt_inv_test", "invoice.paid").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// GetCustomerByStripeID
	mock.ExpectQuery(regexp.QuoteMeta(`FROM customers WHERE stripe_customer_id`)).
		WithArgs("cus_test_1").
		WillReturnRows(
			sqlmock.NewRows([]string{"id", "user_id", "tenant_id", "stripe_customer_id", "email", "created_at"}).
				AddRow("cust-uuid", "user-1", "tenant-42", "cus_test_1", "a@b.co", time.Now()),
		)
	// CreateInvoice — amount_omr=50, amount_baisa=50000, currency=omr.
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO invoices`)).
		WithArgs(
			"cust-uuid", "tenant-42",
			sqlmock.AnyArg(), // stripe_invoice_id
			50,               // amount_omr
			int64(50000),     // amount_baisa
			"omr",            // currency
			"paid",           // status
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow("inv-uuid", time.Now()))

	sig := signedPayload(t, body, secret)
	req := httptest.NewRequest(http.MethodPost, "/billing/webhook", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	rec := httptest.NewRecorder()
	h.Webhook(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
}

// TestWebhook_InvoicePaidRejectsForeignCurrency locks in the currency
// sanity check: if Stripe ever sends us a non-OMR invoice (e.g. a
// misconfigured product), the handler must NOT silently store the baisa
// value as if it were OMR. It logs + returns 200 (non-retryable) without
// inserting anything.
func TestWebhook_InvoicePaidRejectsForeignCurrency(t *testing.T) {
	h, mock, db := newWebhookTestHandler(t)
	defer db.Close()

	const secret = "whsec_test_12345"
	body := []byte(`{
	  "id": "evt_inv_usd",
	  "type": "invoice.paid",
	  "data": {
	    "object": {
	      "id": "in_test_usd",
	      "customer": "cus_test_1",
	      "amount_paid": 5000,
	      "currency": "usd",
	      "period_start": 1700000000,
	      "period_end": 1702592000
	    }
	  }
	}`)

	expectSettingsLookup(mock, secret)
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO stripe_webhook_events`)).
		WithArgs("evt_inv_usd", "invoice.paid").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(`FROM customers WHERE stripe_customer_id`)).
		WithArgs("cus_test_1").
		WillReturnRows(
			sqlmock.NewRows([]string{"id", "user_id", "tenant_id", "stripe_customer_id", "email", "created_at"}).
				AddRow("cust-uuid", "user-1", "tenant-42", "cus_test_1", "a@b.co", time.Now()),
		)
	// NO insert into invoices.

	sig := signedPayload(t, body, secret)
	req := httptest.NewRequest(http.MethodPost, "/billing/webhook", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	rec := httptest.NewRecorder()
	h.Webhook(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 for non-retryable bad currency, got %d", rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
}

// TestWebhook_InvalidSignatureReturns400 locks in the existing behavior:
// signature failures are 400 (not retryable). This protects the
// idempotency fix from accidentally hiding auth errors.
func TestWebhook_InvalidSignatureReturns400(t *testing.T) {
	h, mock, db := newWebhookTestHandler(t)
	defer db.Close()

	expectSettingsLookup(mock, "whsec_real")

	body := []byte(`{"id":"evt_x","type":"invoice.paid","data":{"object":{}}}`)
	req := httptest.NewRequest(http.MethodPost, "/billing/webhook", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", "t=1,v1=deadbeef")
	rec := httptest.NewRecorder()
	h.Webhook(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 on bad signature, got %d", rec.Code)
	}
}
