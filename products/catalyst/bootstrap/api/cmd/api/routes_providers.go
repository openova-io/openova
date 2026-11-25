package main

import "github.com/go-chi/chi/v5"

// The cloud profiles (internal/providersapi), in the session group of POST /api/v1/deployments.
func registerSessionProviders(rg chi.Router, d routeDeps) {
	rg.Get("/api/v1/providers", d.providers.List)
	rg.Get("/api/v1/providers/{provider}", d.providers.Profile)
}
