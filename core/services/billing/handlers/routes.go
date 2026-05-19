package handlers

import "net/http"

// Routes returns an http.Handler with all billing endpoints registered.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	// Checkout — creates order, settles from credit or creates Stripe session.
	mux.HandleFunc("POST /billing/checkout", h.Checkout)

	// Purchase — semantic alias for /billing/checkout. The DoD validator
	// + customer-journey "Purchase" button (CheckoutStep.svelte:722) speak
	// the verb "purchase"; the in-cluster service has always named the
	// handler "checkout" (Stripe Session lineage). Registering an alias
	// here closes TBD-C15 (#1750) without renaming the canonical handler
	// or migrating every existing caller. The handler is identical — same
	// promo-code application, same Stripe-session creation, same
	// `paid_by_credit` shortcut. See `Checkout` in handlers.go for the
	// full wire contract.
	mux.HandleFunc("POST /billing/purchase", h.Checkout)

	// Webhook — Stripe callback (PUBLIC, no JWT; verified via signature).
	mux.HandleFunc("POST /billing/webhook", h.Webhook)

	// Balance — current user's credit balance.
	mux.HandleFunc("GET /billing/balance", h.GetBalance)

	// Subscription + invoices + portal.
	mux.HandleFunc("GET /billing/subscription/{tenantId}", h.GetSubscription)
	mux.HandleFunc("GET /billing/invoices/{tenantId}", h.ListInvoices)
	mux.HandleFunc("POST /billing/portal/{tenantId}", h.CreatePortalSession)

	// Admin — settings (Stripe keys).
	mux.HandleFunc("GET /billing/admin/settings", h.GetAdminSettings)
	mux.HandleFunc("PUT /billing/admin/settings", h.UpdateAdminSettings)

	// Admin — promo codes (legacy URL surface; kept for the existing
	// admin UI until it migrates to the /billing/vouchers/... namespace
	// added by #117).
	mux.HandleFunc("GET /billing/admin/promos", h.AdminListPromos)
	mux.HandleFunc("POST /billing/admin/promos", h.AdminUpsertPromo)
	mux.HandleFunc("DELETE /billing/admin/promos/{code}", h.AdminDeletePromo)

	// Vouchers (#117) — the franchise-aligned URL surface. Same store
	// for issue/list/revoke; superadmin OR sovereign-admin per
	// requireVoucherIssuer. `redeem-preview` is the public landing
	// validation endpoint (no auth, see docs/FRANCHISE-MODEL.md §3);
	// the actual redemption happens transactionally inside
	// POST /billing/checkout via the existing `promo_code` field.
	mux.HandleFunc("POST /billing/vouchers/issue", h.IssueVoucher)
	mux.HandleFunc("GET /billing/vouchers/list", h.ListVouchers)
	mux.HandleFunc("DELETE /billing/vouchers/revoke/{code}", h.RevokeVoucher)
	mux.HandleFunc("POST /billing/vouchers/redeem-preview", h.RedeemVoucherPreview)

	// Admin — revenue + orders.
	mux.HandleFunc("GET /billing/admin/revenue", h.AdminRevenue)
	mux.HandleFunc("GET /billing/admin/orders", h.AdminOrders)

	// Metering — synchronous "validate → INSERT credit_ledger →
	// return balance_after" path (#798 §C). Same payload + idempotency
	// guard as the NATS subscriber on `catalyst.usage.recorded`. Auth:
	// superadmin OR sovereign-admin per requireVoucherIssuer (the
	// operator-admin model). End-user LLM traffic flows through
	// NewAPI → metering sidecar → NATS — never this HTTP path.
	mux.HandleFunc("POST /billing/metering/record", h.RecordMetering)

	return mux
}
