package main

import "github.com/go-chi/chi/v5"

func registerSessionFleet(rg chi.Router, d routeDeps) {
	h := d.h
	// EPIC-6 (#1101) slice U-Fleet — multi-Sovereign fleet view.
	// Read-only aggregator that backs the new live DashboardPage,
	// per-Sovereign card detail rollup, and cross-Sovereign
	// Applications table. Per ADR-0001 §2.7 the data is read
	// LIVE from each Sovereign's CRs (no separate fleet DB);
	// per INVIOLABLE-PRINCIPLES #5 the per-tier visibility gate
	// is centralised in fleetCallerVisibility() on the handler
	// side. See internal/handler/fleet.go for the contract +
	// per-Sov timeout (4s) so a slow cluster doesn't stall the
	// whole dashboard.
	rg.Get("/api/v1/fleet/sovereigns", h.HandleFleetSovereigns)
	rg.Get("/api/v1/fleet/sovereigns/{id}/summary", h.HandleFleetSovereignSummary)
	rg.Get("/api/v1/fleet/applications", h.HandleFleetApplications)
	// TBD-E14 — fleet-wide treemap surface (mothership only).
	// One cell per Sovereign; layer 2+ deep-links to the per-Sov
	// /dashboard treemap. See handler/fleet_treemap.go.
	rg.Get("/api/v1/fleet/treemap", h.HandleFleetTreemap)
	rg.Get("/api/v1/fleet/continuum", h.HandleFleetContinuum)
	rg.Get("/api/v1/fleet/sovereigns/{id}/dr-summary", h.HandleFleetSovereignDRSummary)
}
