package handlers

// Tests for POST /billing/metering/record (#798 §C). The handler is
// exercised through the standard httptest.ResponseRecorder pattern
// with a JWT context that carries the operator-admin role required by
// requireVoucherIssuer.

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/golang-jwt/jwt/v5"

	"github.com/openova-io/openova/core/services/billing/store"
	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/shared/middleware"
)

// withSuperadmin attaches a superadmin JWT-claims context to a
// request — the same shape middleware.JWTAuth installs in production.
func withSuperadmin(r *http.Request) *http.Request {
	ctx := middleware.WithClaims(r.Context(), jwt.MapClaims{
		"sub":  "operator-1",
		"role": "superadmin",
	})
	return r.WithContext(ctx)
}

// withSovereignAdmin attaches a sovereign-admin JWT-claims context.
func withSovereignAdmin(r *http.Request) *http.Request {
	ctx := middleware.WithClaims(r.Context(), jwt.MapClaims{
		"sub":  "operator-2",
		"role": "sovereign-admin",
	})
	return r.WithContext(ctx)
}

// withCustomerRole attaches a regular-customer claims context (NOT
// authorised for /metering/record).
func withCustomerRole(r *http.Request) *http.Request {
	ctx := middleware.WithClaims(r.Context(), jwt.MapClaims{
		"sub":  "customer-1",
		"role": "customer",
	})
	return r.WithContext(ctx)
}

// makeRequest serialises a UsageRecordedPayload into a POST request
// hitting /billing/metering/record with the given auth context.
func makeRequest(t *testing.T, payload events.UsageRecordedPayload, withAuth func(*http.Request) *http.Request) *http.Request {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/billing/metering/record", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if withAuth != nil {
		r = withAuth(r)
	}
	return r
}

// TestRecordMetering_HappyPath: superadmin POSTs a valid envelope; the
// handler validates, resolves the customer, calls RecordUsage, and
// returns 200 with the new balance.
func TestRecordMetering_HappyPath(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	h := &Handler{
		Store: store.New(db),
		MeteringCustomerResolver: fakeResolver{
			cust: &store.Customer{ID: "cust-uuid", UserID: "user-uuid-1", TenantID: "tenant-acme"},
		},
	}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO credit_ledger")).
		WithArgs("cust-uuid", int64(-234), "usage:newapi:qwen3-coder", "req-http-1", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "existed"}).AddRow("ledger-http-1", false))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(CAST(SUM(amount_omr) AS BIGINT) * 1000000")).
		WithArgs("cust-uuid").
		WillReturnRows(sqlmock.NewRows([]string{"sum"}).AddRow(int64(-234)))
	mock.ExpectCommit()

	req := makeRequest(t, makeEnvelope("req-http-1", -234), withSuperadmin)
	w := httptest.NewRecorder()
	h.RecordMetering(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp MeteringRecordResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.LedgerEntryID != "ledger-http-1" {
		t.Errorf("ledger_entry_id = %q, want ledger-http-1", resp.LedgerEntryID)
	}
	if resp.BalanceAfterMicroOMR != -234 {
		t.Errorf("balance_after_micro_omr = %d, want -234", resp.BalanceAfterMicroOMR)
	}
	if resp.BalanceAfterOMR != 0 {
		t.Errorf("balance_after_omr = %d, want 0 (rounds to zero whole OMR)", resp.BalanceAfterOMR)
	}
	if resp.Duplicate {
		t.Error("duplicate = true on first insert")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestRecordMetering_DuplicateReturns200WithFlag: the SAME request_id
// arriving a second time still returns 200 (the operation is
// idempotent) but with duplicate=true so the caller knows the amount
// was NOT applied a second time.
func TestRecordMetering_DuplicateReturns200WithFlag(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	h := &Handler{
		Store: store.New(db),
		MeteringCustomerResolver: fakeResolver{
			cust: &store.Customer{ID: "cust-uuid", UserID: "user-uuid-1", TenantID: "tenant-acme"},
		},
	}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO credit_ledger")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "existed"}).AddRow("ledger-existing", true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(CAST(SUM(amount_omr) AS BIGINT) * 1000000")).
		WillReturnRows(sqlmock.NewRows([]string{"sum"}).AddRow(int64(-234)))
	mock.ExpectCommit()

	req := makeRequest(t, makeEnvelope("req-dup-http", -234), withSuperadmin)
	w := httptest.NewRecorder()
	h.RecordMetering(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("duplicate must still return 200, got %d", w.Code)
	}
	var resp MeteringRecordResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Duplicate {
		t.Error("expected duplicate=true on existing row")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestRecordMetering_BalanceAfterAfterMultipleCharges: simulate two
// charges in sequence and assert the running balance is reflected in
// the response of the second call.
func TestRecordMetering_BalanceAfterAfterMultipleCharges(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	h := &Handler{
		Store: store.New(db),
		MeteringCustomerResolver: fakeResolver{
			cust: &store.Customer{ID: "cust-uuid", UserID: "user-uuid-1", TenantID: "tenant-acme"},
		},
	}

	// First charge: -234 micro-OMR. Balance after = -234.
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO credit_ledger")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "existed"}).AddRow("l1", false))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE")).
		WillReturnRows(sqlmock.NewRows([]string{"sum"}).AddRow(int64(-234)))
	mock.ExpectCommit()

	r1 := makeRequest(t, makeEnvelope("req-multi-1", -234), withSuperadmin)
	w1 := httptest.NewRecorder()
	h.RecordMetering(w1, r1)
	if w1.Code != http.StatusOK {
		t.Fatalf("first charge: %d %s", w1.Code, w1.Body.String())
	}

	// Second charge: -456 micro-OMR. Balance after = -690.
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO credit_ledger")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "existed"}).AddRow("l2", false))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE")).
		WillReturnRows(sqlmock.NewRows([]string{"sum"}).AddRow(int64(-690)))
	mock.ExpectCommit()

	r2 := makeRequest(t, makeEnvelope("req-multi-2", -456), withSuperadmin)
	w2 := httptest.NewRecorder()
	h.RecordMetering(w2, r2)
	if w2.Code != http.StatusOK {
		t.Fatalf("second charge: %d %s", w2.Code, w2.Body.String())
	}
	var resp2 MeteringRecordResponse
	json.Unmarshal(w2.Body.Bytes(), &resp2)
	if resp2.BalanceAfterMicroOMR != -690 {
		t.Errorf("expected running balance -690 micro-OMR, got %d", resp2.BalanceAfterMicroOMR)
	}
}

// TestRecordMetering_RejectsCustomerRole: a non-operator role hitting
// /metering/record gets a 403, NEVER 200. End users do not call this
// endpoint; their LLM traffic flows through NewAPI → sidecar → NATS.
func TestRecordMetering_RejectsCustomerRole(t *testing.T) {
	h := &Handler{Store: nil}
	req := makeRequest(t, makeEnvelope("req-customer", -234), withCustomerRole)
	w := httptest.NewRecorder()
	h.RecordMetering(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for customer role, got %d", w.Code)
	}
}

// TestRecordMetering_AcceptsSovereignAdmin: the franchisee Organization-admin
// pre-flight balance check uses sovereign-admin — must be accepted.
func TestRecordMetering_AcceptsSovereignAdmin(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	h := &Handler{
		Store: store.New(db),
		MeteringCustomerResolver: fakeResolver{
			cust: &store.Customer{ID: "cust-uuid", UserID: "user-uuid-1", TenantID: "tenant-acme"},
		},
	}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO credit_ledger")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "existed"}).AddRow("ledger-sa", false))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE")).
		WillReturnRows(sqlmock.NewRows([]string{"sum"}).AddRow(int64(-234)))
	mock.ExpectCommit()

	req := makeRequest(t, makeEnvelope("req-sa", -234), withSovereignAdmin)
	w := httptest.NewRecorder()
	h.RecordMetering(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for sovereign-admin, got %d: %s", w.Code, w.Body.String())
	}
}

// TestRecordMetering_RejectsMissingRequestID: a payload without
// metadata.request_id is rejected at the validation layer with 400 —
// the idempotency guard cannot work without it.
func TestRecordMetering_RejectsMissingRequestID(t *testing.T) {
	h := &Handler{Store: nil}
	env := makeEnvelope("", -234)
	req := makeRequest(t, env, withSuperadmin)
	w := httptest.NewRecorder()
	h.RecordMetering(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 missing request_id, got %d", w.Code)
	}
}

// TestRecordMetering_RejectsPositiveAmount: a positive amount means
// "credit grant" which must NEVER come through this endpoint — top-
// ups go through /billing/checkout.
func TestRecordMetering_RejectsPositiveAmount(t *testing.T) {
	h := &Handler{Store: nil}
	env := makeEnvelope("req-pos", 100)
	req := makeRequest(t, env, withSuperadmin)
	w := httptest.NewRecorder()
	h.RecordMetering(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 positive amount, got %d", w.Code)
	}
}

// TestRecordMetering_RejectsMalformedJSON: a body that is not JSON
// returns 400; no panic.
func TestRecordMetering_RejectsMalformedJSON(t *testing.T) {
	h := &Handler{Store: nil}
	r := httptest.NewRequest(http.MethodPost, "/billing/metering/record",
		bytes.NewReader([]byte("{not json")))
	r = withSuperadmin(r)
	w := httptest.NewRecorder()
	h.RecordMetering(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 malformed json, got %d", w.Code)
	}
}

// TestRecordMetering_CustomerNotFoundReturns404: the resolver returns
// an error → handler returns 404 (caller bug, not 5xx).
func TestRecordMetering_CustomerNotFoundReturns404(t *testing.T) {
	h := &Handler{
		Store: nil,
		MeteringCustomerResolver: fakeResolver{
			err: errors.New("customer not found"),
		},
	}
	req := makeRequest(t, makeEnvelope("req-nf", -234), withSuperadmin)
	w := httptest.NewRecorder()
	h.RecordMetering(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 customer not found, got %d", w.Code)
	}
}
