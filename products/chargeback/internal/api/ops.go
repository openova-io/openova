package api

import (
	"context"
	"net/http"
	"time"
)

func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": h.Version})
}

// readyz checks the database.
func (h *Handler) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := h.Store.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "degraded", "db": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "db": "ok"})
}

// metricsHandler renders the Prometheus text exposition.
func (h *Handler) metricsHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if counts, err := h.Store.SourceStatusCounts(ctx); err == nil {
		for st, n := range counts {
			h.Metrics.Set("chargeback_sources", "Cost sources by status", map[string]string{"status": st}, float64(n))
		}
	}
	if counts, err := h.Store.CustomerCountsByStatus(ctx); err == nil {
		for st, n := range counts {
			h.Metrics.Set("chargeback_customers", "Customers by status", map[string]string{"status": st}, float64(n))
		}
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_ = h.Metrics.Write(w)
}
