package handlers

// Voucher endpoints (#117).
//
// "Voucher" is the user-facing label for what the storage layer and admin UI
// historically call "PromoCode". The two refer to the same row in the
// billing service's promo_codes table; the rename is purely a vocabulary
// change to match docs/FRANCHISE-MODEL.md and docs/GLOSSARY.md.
//
// This file adds a parallel `/billing/vouchers/...` URL namespace that
// reuses the existing PromoCode CRUD handlers plus one new endpoint —
// `POST /billing/vouchers/redeem-preview` — for the public landing page
// (per docs/FRANCHISE-MODEL.md §3) to validate a code without consuming
// it. The actual redemption still happens inside `POST /billing/checkout`
// via the `promo_code` field, since redemption must be transactional with
// the Order + credit_ledger writes.
//
// Auth model:
//
//   POST   /billing/vouchers/issue          superadmin OR sovereign-admin
//   GET    /billing/vouchers/list           superadmin OR sovereign-admin
//   DELETE /billing/vouchers/revoke/{code}  superadmin OR sovereign-admin
//   POST   /billing/vouchers/redeem-preview unauthenticated (public landing)
//
// All four are thin shims over the existing store layer; the role gating
// matches `requireVoucherIssuer` introduced in #115.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/openova-io/openova/core/services/billing/store"
	"github.com/openova-io/openova/core/services/shared/respond"
)

// IssueVoucher creates or updates a voucher. Identical semantics to
// AdminUpsertPromo (#91 resurrects soft-deleted codes on conflict).
func (h *Handler) IssueVoucher(w http.ResponseWriter, r *http.Request) {
	if err := requireVoucherIssuer(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	var p store.PromoCode
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if p.Code == "" || p.CreditOMR <= 0 {
		respond.Error(w, http.StatusBadRequest, "code and credit_omr are required")
		return
	}
	// Normalize the code to uppercase to match the admin UI's convention
	// (BillingPage.svelte uppercases on save). Public redemption is also
	// case-insensitive — see RedeemVoucherPreview.
	p.Code = strings.ToUpper(strings.TrimSpace(p.Code))
	if err := h.Store.UpsertPromoCode(r.Context(), &p); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to save voucher")
		return
	}
	respond.OK(w, p)
}

// ListVouchers returns all live (not soft-deleted) vouchers.
func (h *Handler) ListVouchers(w http.ResponseWriter, r *http.Request) {
	if err := requireVoucherIssuer(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	list, err := h.Store.ListPromoCodes(r.Context())
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list vouchers")
		return
	}
	respond.OK(w, list)
}

// RevokeVoucher soft-deletes a voucher (per #91 — preserves the audit
// trail of past redemptions; the row stays for FK integrity with
// promo_redemptions and orders.promo_code).
func (h *Handler) RevokeVoucher(w http.ResponseWriter, r *http.Request) {
	if err := requireVoucherIssuer(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	code := r.PathValue("code")
	if code == "" {
		respond.Error(w, http.StatusBadRequest, "code path parameter is required")
		return
	}
	if err := h.Store.DeletePromoCode(r.Context(), strings.ToUpper(code)); err != nil {
		if err == sql.ErrNoRows {
			respond.Error(w, http.StatusNotFound, "voucher not found")
			return
		}
		slog.Error("revoke voucher failed", "code", code, "err", err.Error())
		respond.Error(w, http.StatusInternalServerError, "failed to revoke voucher")
		return
	}
	respond.OK(w, map[string]bool{"ok": true})
}

// VoucherPreview is the safe shape we return from RedeemVoucherPreview. It
// deliberately omits `times_redeemed` and `max_redemptions` so an attacker
// scraping the public endpoint cannot enumerate cap status; the public
// surface only confirms whether the code is currently usable and the
// credit it would grant.
type VoucherPreview struct {
	Code        string `json:"code"`
	CreditOMR   int    `json:"credit_omr"`
	Description string `json:"description"`
	Active      bool   `json:"active"`
	// AcceptingRedemptions is true iff the voucher is active AND has not
	// hit its redemption cap. The landing page uses this to show
	// "Redemptions exhausted" vs "Sign up to redeem" without leaking the
	// exact cap.
	AcceptingRedemptions bool `json:"accepting_redemptions"`
}

// previewRequest is the body shape for POST /billing/vouchers/redeem-preview.
type previewRequest struct {
	Code string `json:"code"`
}

// RedeemVoucherPreview validates a voucher code WITHOUT consuming it. This
// is the public-landing endpoint (per docs/FRANCHISE-MODEL.md §3) used by
// the `<sovereign>/redeem?code=...` page to show the customer what they
// would get before they click through to signup.
//
// Unauthenticated by design — the page is reachable before signup. To
// limit abuse, callers SHOULD be rate-limited at the ingress (the
// Sovereign's edge proxy / WAF). This handler does not own that policy.
//
// On not-found / soft-deleted / inactive / cap-reached: returns 404 with a
// generic message ("voucher not valid"). Never leaks tombstone state.
func (h *Handler) RedeemVoucherPreview(w http.ResponseWriter, r *http.Request) {
	var req previewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	code := strings.ToUpper(strings.TrimSpace(req.Code))
	if code == "" {
		respond.Error(w, http.StatusBadRequest, "code is required")
		return
	}
	p, err := h.Store.GetPromoCode(r.Context(), code)
	if err != nil {
		slog.Error("preview voucher lookup failed", "code", code, "err", err.Error())
		respond.Error(w, http.StatusInternalServerError, "voucher lookup failed")
		return
	}
	// GetPromoCode already filters out soft-deleted rows (deleted_at IS
	// NULL) — see store.GetPromoCode. nil here = not found OR retired,
	// indistinguishable to the caller as required by #91.
	if p == nil {
		respond.Error(w, http.StatusNotFound, "voucher not valid")
		return
	}

	preview := VoucherPreview{
		Code:        p.Code,
		CreditOMR:   p.CreditOMR,
		Description: p.Description,
		Active:      p.Active,
		AcceptingRedemptions: p.Active &&
			(p.MaxRedemptions == 0 || p.TimesRedeemed < p.MaxRedemptions),
	}
	if !preview.AcceptingRedemptions {
		// Surface the inactive / capped state with a 410 Gone so the
		// landing page can distinguish "code never existed" (404 above)
		// from "code is no longer accepting redemptions" (this branch).
		// The body still includes the credit / description so the page
		// can show "this campaign has ended — credit was X OMR".
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGone)
		_ = json.NewEncoder(w).Encode(preview)
		return
	}
	respond.OK(w, preview)
}

// _ keep the time import in scope for future tracing/metric work without
// triggering an unused-import error in the meantime.
var _ = time.Now
var _ = fmt.Sprintf
