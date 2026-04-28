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
	"net/http"
	"net/http/httptest"
	"regexp"
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
