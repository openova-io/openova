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
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/openova-io/openova/core/services/billing/store"
	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
	"github.com/openova-io/openova/core/services/shared/respond"
	"github.com/openova-io/openova/core/services/shared/voucher"
)

// issueVoucherRequest is the wire shape for POST /billing/vouchers/issue.
//
// It embeds store.PromoCode so all the existing JSON tags continue to
// work (Code, CreditOMR, Description, Active, MaxRedemptions, etc.) and
// adds one request-only field — `recipient_email`. The store.PromoCode
// row never carries the email; D28's contract is "fire-and-forget
// delivery on issue", not "persist who the voucher was gifted to". A
// separate audit/CRM table is the right home for that, deferred to D28b.
//
// On re-issue (same code), the email re-fires alongside the upsert —
// per #91 the upsert resurrects soft-deleted rows and we mirror that
// semantics on the email path so the operator can re-deliver a lost
// voucher by simply re-issuing it. The handler comment documents this.
type issueVoucherRequest struct {
	store.PromoCode
	RecipientEmail string `json:"recipient_email,omitempty"`
}

// IssueVoucher creates or updates a voucher. Identical semantics to
// AdminUpsertPromo (#91 resurrects soft-deleted codes on conflict).
//
// D28 — if the request body carries `recipient_email`, the handler fires
// a `voucher-issued` notification to the recipient via the notification
// service AFTER a successful upsert. The recipient email is NOT
// persisted on the PromoCode row; it is request-only. A re-issue of the
// same code (which resurrects a soft-deleted row per #91) re-fires the
// email — this matches operator intent: "I lost the email, please
// re-send" is satisfied by re-issuing the same code. The notification
// call is best-effort: a failure logs but does NOT roll back or fail the
// voucher upsert, since the row is already saved and the operator can
// re-fire by calling the endpoint again.
func (h *Handler) IssueVoucher(w http.ResponseWriter, r *http.Request) {
	if err := requireVoucherIssuer(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	var req issueVoucherRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	p := req.PromoCode
	if p.CreditOMR <= 0 {
		respond.Error(w, http.StatusBadRequest, "credit_omr is required")
		return
	}
	// Normalize the code to uppercase to match the admin UI's convention
	// (BillingPage.svelte uppercases on save). Public redemption is also
	// case-insensitive — see RedeemVoucherPreview.
	p.Code = strings.ToUpper(strings.TrimSpace(p.Code))
	// #5223 (UAT row 72) — plan_tier is a catalog plan slug (s/m/l/xl…);
	// normalise to lowercase to match catalog Plan.Slug. Empty = credit-only
	// voucher (any plan). Not validated against the catalog here — billing
	// has no catalog dependency and the field is operator-informational.
	p.PlanTier = strings.ToLower(strings.TrimSpace(p.PlanTier))
	// #3376 DoD-6 — voucher-code entropy. An omitted code auto-generates a
	// high-entropy one (crypto/rand, ~60 bits); a supplied code must pass
	// the server-side strength minimum so a guessable code (the walk's
	// `WALKMART2026` shape) can't be issued. The redeem path resists
	// brute-force only if the code space is large.
	if p.Code == "" {
		gen, err := voucher.GenerateCode()
		if err != nil {
			respond.Error(w, http.StatusInternalServerError, "failed to generate voucher code")
			return
		}
		p.Code = gen
	} else if err := voucher.ValidateCodeStrength(p.Code); err != nil {
		respond.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.Store.UpsertPromoCode(r.Context(), &p); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to save voucher")
		return
	}

	// D28 — fire the gifting email if a recipient was supplied. The
	// notification service owns SMTP delivery; we never touch stalwart
	// directly from billing. A non-nil error here is logged but does NOT
	// fail the response — the row is already persisted and re-issuing
	// the same code re-fires the email.
	recipient := strings.TrimSpace(req.RecipientEmail)
	if recipient != "" {
		// D28 fire-and-forget (#3057): dispatch on a DETACHED background
		// context in a goroutine. The previous synchronous call bound the
		// email send to the request context, so a slow mothership-relay
		// handshake (mail.openova.io:587) held the handler open past the
		// upstream gateway's ~3s proxy timeout → the operator saw a 502
		// even though the voucher row had already persisted. Detaching
		// guarantees an immediate 200; the send completes (or logs) in the
		// background. Re-issuing the same code re-fires, per the D28 brief.
		h.emailDispatchWG.Add(1)
		go func(recipient string, p store.PromoCode) {
			defer h.emailDispatchWG.Done()
			if err := h.sendVoucherIssuedEmail(context.Background(), recipient, p); err != nil {
				slog.Error("voucher email dispatch failed",
					"code", p.Code, "recipient", recipient, "err", err.Error())
			}
		}(recipient, p)
	}

	respond.OK(w, p)
}

// notificationSendRequest mirrors notification/handlers.sendRequest so we
// can encode it without importing the notification package (which would
// create a service-to-service Go dependency we don't want).
type notificationSendRequest struct {
	To       string         `json:"to"`
	Subject  string         `json:"subject,omitempty"`
	Template string         `json:"template"`
	Data     map[string]any `json:"data"`
}

// sendVoucherIssuedEmail POSTs a voucher-issued notification to the
// notification service. Returns an error only on local issues (bad URL,
// transport failure, non-2xx response) — the caller logs but does not
// propagate to the HTTP response.
//
// Uses h.NotificationClient if set so tests can inject a round-tripper;
// production wires a 5s-timeout default in main.go.
//
// Auth (#1999 / TBD-V8 fix): notification's HTTP surface
// (`/notification/`) is gated by the same shared HS256 JWTAuth
// middleware that every other Organization microservice uses
// (core/services/shared/middleware/jwt.go). Pre-#1999 this dispatch
// carried no Authorization header → notification 401'd silently →
// voucher row persisted, HTTP 200 to operator, no email ever sent.
//
// Fix: when h.JWTSecret is populated, mint a fresh short-lived HS256
// service-to-service token signed with the SAME `org-services-secrets/JWT_SECRET`
// bytes notification verifies against, and forward it as
// `Authorization: Bearer …`. The mint helper is the same one
// catalyst-api's RS256→HS256 bridge uses (sharedauth.MintOrgAccessToken),
// so the wire contract on the receive side is symmetric — claims carry
// sub="org-billing", role="superadmin" so any future per-role gating in
// notification recognises this as a privileged service caller (today
// notification's middleware only checks signature validity; the role is
// future-proofing, not gating).
//
// Empty h.JWTSecret falls back to the legacy no-header path so a stale
// chart that doesn't wire JWT_SECRET into the billing Pod keeps the
// best-effort fire-and-forget semantics rather than crashing the upsert
// (mirrors the optional:true contract on catalyst-api's
// CATALYST_ORG_JWT_SECRET secretKeyRef — see chart api-deployment.yaml).
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 the minted token is NEVER
// logged — only the recipient email + template name are.
func (h *Handler) sendVoucherIssuedEmail(ctx context.Context, recipient string, p store.PromoCode) error {
	if h.NotificationURL == "" {
		// Notification not configured — log via caller, exit clean.
		return nil
	}
	payload := notificationSendRequest{
		To:       recipient,
		Template: "voucher-issued",
		Data: map[string]any{
			"code":           p.Code,
			"credit_omr":     p.CreditOMR,
			"description":    p.Description,
			"sovereign_fqdn": h.SovereignFQDN,
		},
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	// 5s budget per the D28 brief. A separate ctx is fine — the request
	// context may be very tight (e.g. test fixtures with short deadlines)
	// and we want the email dispatch to be resilient to that.
	dispatchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(dispatchCtx, http.MethodPost, h.NotificationURL, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// Service-to-service auth (#1999 / TBD-V8). Mint a fresh HS256
	// token with the SAME org-services-secrets/JWT_SECRET bytes notification
	// verifies against. Empty h.JWTSecret → legacy unauth path; the
	// dispatch will 401 but the voucher row already persisted so the
	// failure is logged-not-fatal (matches existing best-effort
	// semantics documented on IssueVoucher).
	if len(h.JWTSecret) > 0 {
		tok, mintErr := sharedauth.MintOrgAccessToken(
			h.JWTSecret,
			"org-billing",
			"org-billing@openova.internal",
			"superadmin",
		)
		if mintErr != nil {
			return mintErr
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	client := h.NotificationClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return &notificationErrorf{status: resp.StatusCode}
	}
	return nil
}

// notificationErrorf is a tiny named error so callers can distinguish
// transport vs. status failures via errors.As without a sentinel value.
type notificationErrorf struct{ status int }

func (e *notificationErrorf) Error() string {
	return "notification service returned status " + http.StatusText(e.status)
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

