package main

import "github.com/go-chi/chi/v5"

func registerPublicCredentials(r chi.Router, d routeDeps) {
	h := d.h
	// Wizard pre-submit credential validators — also pre-auth surfaces.
	// The operator types Hetzner / S3 / Dynadot creds in the wizard
	// BEFORE PIN auth (so a typo surfaces at the prompt, not 5 minutes
	// into `tofu apply`). Each endpoint is read-only — it probes the
	// credential against the upstream API and returns 200/400. No state
	// change on success. Same pre-auth treatment as /subdomains/check.
	r.Post("/api/v1/credentials/validate", h.ValidateCredentials)
	r.Post("/api/v1/credentials/object-storage/validate", h.ValidateObjectStorageCredentials)
}
