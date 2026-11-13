package main

import "github.com/go-chi/chi/v5"

func registerSessionRefs(rg chi.Router, d routeDeps) {
	h := d.h
	// ADR-0015 D4 — the install dialog's option lists, read from the
	// objects a create request must name (install_refs_api.go).
	rg.Get("/api/v1/refs/organizations", h.HandleListRefOrganizations)
	rg.Get("/api/v1/refs/organizations/{org}/environments", h.HandleListRefEnvironments)
	rg.Get("/api/v1/refs/regions", h.HandleListRefRegions)
}
