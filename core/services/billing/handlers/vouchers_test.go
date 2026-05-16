package handlers

// #118 — voucher CRD propagation smoke test.
//
// The voucher schema is identical on every Sovereign because all of them
// run the same SHA-pinned billing image. This test verifies the API shape
// exposed by /billing/vouchers/redeem-preview (the public landing
// endpoint introduced in #117) so a deploy regression is caught at CI
// time. The other three endpoints (issue, list, revoke) reuse the
// existing AdminUpsertPromo / AdminListPromos / AdminDeletePromo logic
// and are covered by their existing tests; the preview path is brand new
// and deserves targeted coverage.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/openova-io/openova/core/services/billing/store"
)

// TestRedeemVoucherPreview_404OnUnknownCode confirms an unknown code
// returns 404 with no body leak. This is the same path soft-deleted
// codes follow (#91) so an attacker cannot tell tombstones apart from
// never-existed codes.
func TestRedeemVoucherPreview_404OnUnknownCode(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta(
		`SELECT code, credit_omr, description, active, max_redemptions, times_redeemed, created_at, deleted_at
			 FROM promo_codes WHERE code = $1 AND deleted_at IS NULL`,
	)).WithArgs("DOES-NOT-EXIST").WillReturnError(sql.ErrNoRows)

	h := &Handler{Store: store.New(db)}

	body, _ := json.Marshal(map[string]string{"code": "does-not-exist"})
	r := httptest.NewRequest("POST", "/billing/vouchers/redeem-preview", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.RedeemVoucherPreview(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (body=%s)", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock unmet: %v", err)
	}
}

// TestRedeemVoucherPreview_200OnValidCode confirms a live, accepting code
// returns the expected JSON shape and never leaks `times_redeemed` or
// `max_redemptions`.
func TestRedeemVoucherPreview_200OnValidCode(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{
		"code", "credit_omr", "description", "active",
		"max_redemptions", "times_redeemed", "created_at", "deleted_at",
	}).AddRow("LAUNCH-50", 50, "Launch credit", true, 0, 3, time.Now(), nil)

	mock.ExpectQuery(regexp.QuoteMeta(
		`SELECT code, credit_omr, description, active, max_redemptions, times_redeemed, created_at, deleted_at
			 FROM promo_codes WHERE code = $1 AND deleted_at IS NULL`,
	)).WithArgs("LAUNCH-50").WillReturnRows(rows)

	h := &Handler{Store: store.New(db)}
	body, _ := json.Marshal(map[string]string{"code": "launch-50"}) // case-insensitive
	r := httptest.NewRequest("POST", "/billing/vouchers/redeem-preview", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.RedeemVoucherPreview(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}

	var got map[string]any
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["code"] != "LAUNCH-50" {
		t.Errorf("code: got %v, want LAUNCH-50", got["code"])
	}
	if got["credit_omr"].(float64) != 50 {
		t.Errorf("credit_omr: got %v, want 50", got["credit_omr"])
	}
	if got["accepting_redemptions"] != true {
		t.Errorf("accepting_redemptions: got %v, want true", got["accepting_redemptions"])
	}
	// Non-leak: these MUST NOT appear in the public response.
	if _, leak := got["times_redeemed"]; leak {
		t.Error("times_redeemed leaked into public preview response")
	}
	if _, leak := got["max_redemptions"]; leak {
		t.Error("max_redemptions leaked into public preview response")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock unmet: %v", err)
	}
}

// TestRedeemVoucherPreview_410OnCappedCode confirms a code that exists but
// has hit its redemption cap returns 410 Gone and still includes the
// credit/description so the landing page can show "campaign ended".
func TestRedeemVoucherPreview_410OnCappedCode(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{
		"code", "credit_omr", "description", "active",
		"max_redemptions", "times_redeemed", "created_at", "deleted_at",
	}).AddRow("CAPPED", 25, "Cap reached", true, 5, 5, time.Now(), nil)

	mock.ExpectQuery(regexp.QuoteMeta(
		`SELECT code, credit_omr, description, active, max_redemptions, times_redeemed, created_at, deleted_at
			 FROM promo_codes WHERE code = $1 AND deleted_at IS NULL`,
	)).WithArgs("CAPPED").WillReturnRows(rows)

	h := &Handler{Store: store.New(db)}
	body, _ := json.Marshal(map[string]string{"code": "CAPPED"})
	r := httptest.NewRequest("POST", "/billing/vouchers/redeem-preview", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.RedeemVoucherPreview(w, r)

	if w.Code != http.StatusGone {
		t.Fatalf("expected 410 Gone, got %d", w.Code)
	}

	var got map[string]any
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["accepting_redemptions"] != false {
		t.Errorf("accepting_redemptions: got %v, want false", got["accepting_redemptions"])
	}
	if got["credit_omr"].(float64) != 25 {
		t.Errorf("credit_omr should be present in 410 body: got %v", got["credit_omr"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock unmet: %v", err)
	}
}

// TestRedeemVoucherPreview_400OnEmptyCode confirms an empty code is
// rejected at the boundary, before the DB is hit. This is what the
// /redeem landing page's manual-entry form would trip on.
func TestRedeemVoucherPreview_400OnEmptyCode(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	h := &Handler{Store: store.New(db)}
	body, _ := json.Marshal(map[string]string{"code": "   "})
	r := httptest.NewRequest("POST", "/billing/vouchers/redeem-preview", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.RedeemVoucherPreview(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// captureRoundTripper records every request it serves so D28 tests can
// assert the IssueVoucher → notification-service POST without standing
// up a real httptest.Server (which would pull cluster-DNS resolution
// into the test path).
type captureRoundTripper struct {
	mu        sync.Mutex
	requests  []capturedRequest
	respondWith *http.Response
	respondErr  error
}

type capturedRequest struct {
	Method string
	URL    string
	Body   []byte
}

func (c *captureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	body := []byte{}
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		body = b
		_ = req.Body.Close()
	}
	c.mu.Lock()
	c.requests = append(c.requests, capturedRequest{
		Method: req.Method, URL: req.URL.String(), Body: body,
	})
	c.mu.Unlock()
	if c.respondErr != nil {
		return nil, c.respondErr
	}
	if c.respondWith != nil {
		return c.respondWith, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"status":"sent"}`))),
		Header:     make(http.Header),
	}, nil
}

// TestIssueVoucher_SendsEmail_WhenRecipientPresent — D28. With a
// recipient_email on the body, IssueVoucher must POST a voucher-issued
// payload to the configured notification service URL after a successful
// upsert. The store row never carries the email; it is request-only.
func TestIssueVoucher_SendsEmail_WhenRecipientPresent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	// Match the UpsertPromoCode SQL — code normalises to upper.
	mock.ExpectExec(regexp.QuoteMeta(
		`INSERT INTO promo_codes (code, credit_omr, description, active, max_redemptions)
			 VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (code) DO UPDATE
			 SET credit_omr = EXCLUDED.credit_omr,
			     description = EXCLUDED.description,
			     active = EXCLUDED.active,
			     max_redemptions = EXCLUDED.max_redemptions,
			     deleted_at = NULL`,
	)).WithArgs("GIFT-50", 50, "Q4 launch", true, 0).
		WillReturnResult(sqlmock.NewResult(0, 1))

	rt := &captureRoundTripper{}
	h := &Handler{
		Store:              store.New(db),
		NotificationURL:    "http://notification.sme.svc.cluster.local:8087/notification/send",
		SovereignFQDN:      "omani.works",
		NotificationClient: &http.Client{Transport: rt},
	}

	body, _ := json.Marshal(map[string]any{
		"code":            "gift-50", // lower-case; handler upper-cases
		"credit_omr":      50,
		"description":     "Q4 launch",
		"active":          true,
		"recipient_email": "alice@example.test",
	})
	r := httptest.NewRequest("POST", "/billing/vouchers/issue", bytes.NewReader(body))
	r = withSuperadmin(r)
	w := httptest.NewRecorder()
	h.IssueVoucher(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("issue voucher: expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock unmet: %v", err)
	}

	// One captured notification call, with the right URL + payload.
	if len(rt.requests) != 1 {
		t.Fatalf("expected 1 notification POST, got %d", len(rt.requests))
	}
	got := rt.requests[0]
	if got.Method != "POST" {
		t.Errorf("notification method: got %q, want POST", got.Method)
	}
	if got.URL != "http://notification.sme.svc.cluster.local:8087/notification/send" {
		t.Errorf("notification URL: got %q", got.URL)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.Body, &payload); err != nil {
		t.Fatalf("decode notification body: %v (raw=%s)", err, string(got.Body))
	}
	if payload["to"] != "alice@example.test" {
		t.Errorf("notification to: got %v, want alice@example.test", payload["to"])
	}
	if payload["template"] != "voucher-issued" {
		t.Errorf("notification template: got %v, want voucher-issued", payload["template"])
	}
	dataAny, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("notification data not an object: %v", payload["data"])
	}
	if dataAny["code"] != "GIFT-50" {
		t.Errorf("data.code: got %v, want GIFT-50 (upper-cased)", dataAny["code"])
	}
	if dataAny["credit_omr"].(float64) != 50 {
		t.Errorf("data.credit_omr: got %v, want 50", dataAny["credit_omr"])
	}
	if dataAny["sovereign_fqdn"] != "omani.works" {
		t.Errorf("data.sovereign_fqdn: got %v, want omani.works", dataAny["sovereign_fqdn"])
	}
	if dataAny["description"] != "Q4 launch" {
		t.Errorf("data.description: got %v, want Q4 launch", dataAny["description"])
	}
}

// TestIssueVoucher_NoEmail_WhenRecipientAbsent — D28. With no
// recipient_email on the body, the upsert succeeds and NO notification
// call is made. Guards against accidental email-on-empty.
func TestIssueVoucher_NoEmail_WhenRecipientAbsent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(regexp.QuoteMeta(
		`INSERT INTO promo_codes (code, credit_omr, description, active, max_redemptions)
			 VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (code) DO UPDATE
			 SET credit_omr = EXCLUDED.credit_omr,
			     description = EXCLUDED.description,
			     active = EXCLUDED.active,
			     max_redemptions = EXCLUDED.max_redemptions,
			     deleted_at = NULL`,
	)).WithArgs("LAUNCH", 100, "", false, 0).
		WillReturnResult(sqlmock.NewResult(0, 1))

	rt := &captureRoundTripper{}
	h := &Handler{
		Store:              store.New(db),
		NotificationURL:    "http://notification.sme.svc.cluster.local:8087/notification/send",
		SovereignFQDN:      "omani.works",
		NotificationClient: &http.Client{Transport: rt},
	}

	body, _ := json.Marshal(map[string]any{
		"code":       "launch",
		"credit_omr": 100,
	})
	r := httptest.NewRequest("POST", "/billing/vouchers/issue", bytes.NewReader(body))
	r = withSuperadmin(r)
	w := httptest.NewRecorder()
	h.IssueVoucher(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("issue voucher: expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}
	if len(rt.requests) != 0 {
		t.Errorf("expected 0 notification POSTs, got %d", len(rt.requests))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock unmet: %v", err)
	}
}

// TestIssueVoucher_NotificationFailure_DoesNotFailUpsert — D28. The
// notification call is best-effort: a failed POST logs but the
// IssueVoucher response remains 200 because the row is already
// persisted. The operator can re-issue the same code to re-fire the
// email.
func TestIssueVoucher_NotificationFailure_DoesNotFailUpsert(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(regexp.QuoteMeta(
		`INSERT INTO promo_codes (code, credit_omr, description, active, max_redemptions)
			 VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (code) DO UPDATE
			 SET credit_omr = EXCLUDED.credit_omr,
			     description = EXCLUDED.description,
			     active = EXCLUDED.active,
			     max_redemptions = EXCLUDED.max_redemptions,
			     deleted_at = NULL`,
	)).WithArgs("FAIL-MAIL", 10, "", false, 0).
		WillReturnResult(sqlmock.NewResult(0, 1))

	rt := &captureRoundTripper{
		respondWith: &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":"smtp down"}`))),
			Header:     make(http.Header),
		},
	}
	h := &Handler{
		Store:              store.New(db),
		NotificationURL:    "http://notification.sme.svc.cluster.local:8087/notification/send",
		SovereignFQDN:      "x.test",
		NotificationClient: &http.Client{Transport: rt},
	}

	body, _ := json.Marshal(map[string]any{
		"code":            "fail-mail",
		"credit_omr":      10,
		"recipient_email": "bob@example.test",
	})
	r := httptest.NewRequest("POST", "/billing/vouchers/issue", bytes.NewReader(body))
	r = withSuperadmin(r)
	w := httptest.NewRecorder()
	h.IssueVoucher(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("upsert should still succeed despite mail failure: got %d (body=%s)", w.Code, w.Body.String())
	}
	if len(rt.requests) != 1 {
		t.Errorf("expected 1 notification attempt, got %d", len(rt.requests))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock unmet: %v", err)
	}
}

// TestIssueVoucher_403WithoutVoucherIssuerRole — sanity check that the
// pre-existing role gate still fires even with the D28 wire in place.
func TestIssueVoucher_403WithoutVoucherIssuerRole(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	h := &Handler{Store: store.New(db)}
	body, _ := json.Marshal(map[string]any{"code": "X", "credit_omr": 1})
	r := httptest.NewRequest("POST", "/billing/vouchers/issue", bytes.NewReader(body))
	// no claims context → role == ""
	w := httptest.NewRecorder()
	h.IssueVoucher(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}
