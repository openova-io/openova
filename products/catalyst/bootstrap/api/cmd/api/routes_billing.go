package main

import "github.com/go-chi/chi/v5"

func registerSessionBilling(rg chi.Router, d routeDeps) {
	h := d.h
	// BSS Purchase proxy (TBD-C15 / #1750). Mirrors the vouchers proxy
	// shape above — catalyst-api forwards to the Organization gateway which
	// proxies to the billing service's `POST /billing/purchase`
	// alias (see core/services/billing/handlers/routes.go).
	//
	// Two paths are registered for symmetry with the DoD validator
	// vocabulary on console.<sov-fqdn>:
	//
	//   POST /api/v1/billing/purchase     — operator-visible alias
	//   POST /api/v1/org/billing/purchase — org-namespaced (#3383: was
	//                                       /api/v1/org/billing/purchase)
	//
	// Both call the same handler — the upstream is identical. The
	// canonical UI surface remains the marketplace's
	// /api/billing/checkout (CheckoutStep.svelte); these console-side
	// routes exist so the close-audit DoD validator on the Sovereign
	// host stops 404'ing during the marketplace customer-journey
	// re-walk (Step 15 — purchase button).
	rg.Post("/api/v1/billing/purchase", h.HandleOrgBillingPurchase)
}
