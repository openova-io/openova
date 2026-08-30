package api

import (
	"net/http"
	"time"
)

// overview is the operator landing payload: customers by status, usage over
// the last 30 days, and the rated total of the most recent period.
func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	counts, err := h.Store.CustomerCountsByStatus(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	usage, err := h.Store.UsageSince(r.Context(), h.Now().Add(-30*24*time.Hour), 20)
	if err != nil {
		storeErr(w, err)
		return
	}
	period, total, n, err := h.Store.LastPeriodTotal(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	sources, err := h.Store.SourceStatusCounts(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"profile":        h.Config.Profile,
		"customers":      counts,
		"sources":        sources,
		"usage_last_30d": usage,
		"last_period":    map[string]any{"period": period, "total": total, "statements": n},
	})
}
