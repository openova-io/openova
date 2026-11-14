package main

import "github.com/go-chi/chi/v5"

func registerSessionDashboard(rg chi.Router, d routeDeps) {
	h := d.h
	// Sovereign Dashboard treemap (resource utilisation). Read-only.
	// V1 emits a static placeholder shape — see dashboard.go header
	// for the metrics-server upgrade plan.
	rg.Get("/api/v1/dashboard/treemap", h.GetDashboardTreemap)
}
