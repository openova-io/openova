package main

import "github.com/go-chi/chi/v5"

func registerPublicSubdomains(r chi.Router, d routeDeps) {
	h := d.h
	// /api/v1/subdomains/check — public, read-only availability query.
	// Same model as a username-availability check on a signup form: an
	// anonymous visitor lands on the wizard's Domain step BEFORE they
	// authenticate (PIN issue happens AFTER they pick a subdomain), so
	// requiring a session cookie here would block the only flow that
	// matters. The handler routes the call to PDM (managed pool) or to
	// a DNS lookup (BYO) — both are read-only with no state change and
	// negligible information disclosure ("is this subdomain taken?"
	// is the same answer DNS itself surfaces to anyone).
	r.Post("/api/v1/subdomains/check", h.CheckSubdomain)
}
