package main

import "github.com/go-chi/chi/v5"

func registerPublicVersion(r chi.Router, d routeDeps) {
	h := d.h
	// /api/v1/version — build identification (git SHA + chart version).
	// Always 200, no auth gate (probe-friendly). Operators + the QA
	// matrix probe this to confirm "what version is live right now"
	// without scraping the Pod spec. See handler/version.go for the
	// truth-resolution rules + wire shape.
	r.Get("/api/v1/version", h.HandleVersion)
}
