package api

import (
	"net/http"
)

// allocation serves the Sovereign allocation view (ADR-0014 D3 case 3):
// the platform consumption split across tenant Organization rows plus the
// platform-overhead line, with each row's share of the whole.
//
// Operator-only. The rows span every customer, so a customer-scoped caller
// must never reach it — requireOperator answers 401/403 before any query.
func (h *Handler) allocation(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.requireOperator(w, r)
	if !ok {
		return
	}
	from, to, ok := h.parseRange(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "from/to must be RFC3339 or YYYY-MM-DD with from < to")
		return
	}
	rows, err := h.Store.Allocation(r.Context(), sess.Scope(), from, to)
	if err != nil {
		storeErr(w, err)
		return
	}
	// Sum the shares back up and return it. A caller can then see at a glance
	// whether the split accounts for the whole window — a split that silently
	// loses cost is worse than no split, and this makes that visible instead
	// of implicit (#6850).
	var shareTotal float64
	tenants, overhead := 0, 0
	for _, r := range rows {
		shareTotal += r.Share
		if r.Tier == "platform-overhead" {
			overhead++
		} else {
			tenants++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from":              from,
		"to":                to,
		"rows":              rows,
		"share_total":       shareTotal,
		"organization_rows": tenants,
		"platform_overhead": overhead,
	})
}
