package main

import (
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func registerPublicProbes(r chi.Router, d routeDeps) {
	h := d.h
	// /healthz is LIVENESS — always 200 if the process is up and the
	// HTTP server is serving. /readyz is READINESS — 200 only when
	// the primary Sovereign's informers are synced (or no Sovereigns
	// registered yet). The chart wires livenessProbe → /healthz and
	// readinessProbe → /readyz; failing readiness pulls the Pod out
	// of the Service rotation without restarting it. See the Health
	// + Ready handler comments and issue #530 for the crashloop
	// failure mode this split prevents.
	r.Get("/healthz", h.Health)
	r.Get("/readyz", h.Ready)
	r.Handle("/metrics", promhttp.Handler())

	// The pprof routes, when CATALYST_PPROF_ENABLED is true (#5352).
	if d.pprofEnabled {
		r.Mount("/debug", middleware.Profiler())
	}
}
