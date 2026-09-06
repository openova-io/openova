package api

import (
	"context"
	"net/http"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/recommend"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Recommendations (#6867, DESIGN.md §3.7): the store gathers, the pure
// rules in internal/recommend decide, the handler writes
// {rows, total_monthly_saving, currency}.

const (
	// unpricedWindow is the usage window the unpriced-sku rule looks at.
	unpricedWindow = 30 * 24 * time.Hour
	// cpuWindow is the utilisation window the low-cpu rule averages over.
	cpuWindow = 7 * 24 * time.Hour
)

func (h *Handler) gatherRecommendations(ctx context.Context, scope store.Scope, customerID string) (recommend.Input, error) {
	now := h.Now().UTC()
	in := recommend.Input{Now: now}
	var err error
	if in.Books, err = h.Store.CustomerBooks(ctx, scope, customerID); err != nil {
		return in, err
	}
	if in.Resources, err = h.Store.LiveResources(ctx, scope, customerID); err != nil {
		return in, err
	}
	if in.Sources, err = h.Store.SourceHealths(ctx, scope, customerID); err != nil {
		return in, err
	}
	if in.Unpriced, err = h.Store.UnpricedUsageByCustomer(ctx, scope, customerID, now.Add(-unpricedWindow), now); err != nil {
		return in, err
	}
	if in.CPUUtil, err = h.Store.CPUUtilMeans(ctx, scope, customerID, now.Add(-cpuWindow), now); err != nil {
		return in, err
	}
	return in, nil
}

func (h *Handler) recommendations(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireOperator(w, r)
	if !ok {
		return
	}
	h.writeRecommendations(w, r, s.Scope(), "")
}

func (h *Handler) customerRecommendations(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireCustomer(w, r, id, false)
	if !ok {
		return
	}
	h.writeRecommendations(w, r, s.Scope(), id)
}

func (h *Handler) writeRecommendations(w http.ResponseWriter, r *http.Request, scope store.Scope, customerID string) {
	in, err := h.gatherRecommendations(r.Context(), scope, customerID)
	if err != nil {
		storeErr(w, err)
		return
	}
	rows := recommend.Evaluate(in)
	writeJSON(w, http.StatusOK, map[string]any{
		"rows":                 rows,
		"total_monthly_saving": recommend.Total(rows),
		"currency":             recommend.Currency(rows, in.Books),
	})
}
