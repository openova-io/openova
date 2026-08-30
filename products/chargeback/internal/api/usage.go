package api

import (
	"net/http"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// parseRange reads from/to (RFC3339 or YYYY-MM-DD); default = last 30 days.
func (h *Handler) parseRange(r *http.Request) (time.Time, time.Time, bool) {
	now := h.Now().UTC()
	from, to := now.Add(-30*24*time.Hour), now
	parse := func(s string) (time.Time, bool) {
		if s == "" {
			return time.Time{}, true
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.UTC(), true
		}
		if t, err := time.Parse("2006-01-02", s); err == nil {
			return t.UTC(), true
		}
		return time.Time{}, false
	}
	if t, ok := parse(r.URL.Query().Get("from")); !ok {
		return from, to, false
	} else if !t.IsZero() {
		from = t
	}
	if t, ok := parse(r.URL.Query().Get("to")); !ok {
		return from, to, false
	} else if !t.IsZero() {
		to = t
	}
	if !to.After(from) {
		return from, to, false
	}
	return from, to, true
}

func (h *Handler) customerUsage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireCustomer(w, r, id, false)
	if !ok {
		return
	}
	from, to, ok := h.parseRange(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "from/to must be RFC3339 or YYYY-MM-DD with from < to")
		return
	}
	groupBy := r.URL.Query().Get("group_by")
	switch groupBy {
	case "", "sku":
		groupBy = "sku"
	case "resource", "day":
	default:
		writeErr(w, http.StatusBadRequest, "group_by must be sku, resource or day")
		return
	}
	rows, err := h.Store.QueryUsage(r.Context(), s.Scope(), id, store.UsageQuery{From: from, To: to, GroupBy: groupBy})
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "group_by": groupBy, "rows": rows})
}

func (h *Handler) customerInventory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireCustomer(w, r, id, false)
	if !ok {
		return
	}
	items, err := h.Store.ListCustomerInventory(r.Context(), s.Scope(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"inventory": items})
}
