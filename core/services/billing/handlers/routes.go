package handlers

import "net/http"

// Routes returns an http.Handler with all billing endpoints registered.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	// Checkout — creates order, settles from credit or creates Stripe session.
	mux.HandleFunc("POST /billing/checkout", h.Checkout)

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

	// Admin — promo codes.
	mux.HandleFunc("GET /billing/admin/promos", h.AdminListPromos)
	mux.HandleFunc("POST /billing/admin/promos", h.AdminUpsertPromo)
	mux.HandleFunc("DELETE /billing/admin/promos/{code}", h.AdminDeletePromo)

	// Admin — revenue + orders.
	mux.HandleFunc("GET /billing/admin/revenue", h.AdminRevenue)
	mux.HandleFunc("GET /billing/admin/orders", h.AdminOrders)

	return mux
}
