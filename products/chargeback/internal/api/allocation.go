package api

import (
	"errors"
	"net/http"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Allocation (#6867, ADR-0014 D3 case 3, DESIGN.md §2.8 / §3.8).
//
// GET  /api/v1/allocation/settings — the editable basis.
// PUT  /api/v1/allocation/settings — replace it (audited: allocation.settings).
// GET  /api/v1/allocation?from&to  — the split: Organization rows + the
//      platform-overhead line, each with share, allocated cloud cost, rated
//      revenue and margin; the pool the money came from; totals.
//
// All operator-only. The rows span every customer, so a customer-scoped
// caller must never reach them — requireOperator answers 401/403 before any
// query.

func (h *Handler) getAllocationSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	s, err := h.Store.GetAllocationSettings(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (h *Handler) putAllocationSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	var in store.AllocationSettings
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	s, err := h.Store.UpdateAllocationSettings(r.Context(), in)
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, store.ErrNotFound):
		// The only lookup the update does is the Sovereign customer; a 404
		// here would read as "settings not found", which is the wrong story.
		writeErr(w, http.StatusBadRequest, "sovereign_customer_id does not name an existing customer")
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "allocation.settings", map[string]any{
		"weights": s.Weights, "overhead_policy": s.OverheadPolicy, "pool": s.Pool,
		"manual_amount": s.ManualAmount, "currency": s.Currency, "sovereign_customer_id": s.SovereignCustomerID,
	})
	writeJSON(w, http.StatusOK, s)
}

// allocation serves the split for the window (default: the current calendar
// month). Money is exact: every currency figure is computed server-side in
// big.Rat and the rows sum to the pool — see store.splitAllocation.
func (h *Handler) allocation(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.requireOperator(w, r)
	if !ok {
		return
	}
	ms := monthStart(h.Now())
	from, to, ok := h.parseRangeFrom(r, ms, ms.AddDate(0, 1, 0))
	if !ok {
		writeErr(w, http.StatusBadRequest, "from/to must be RFC3339 or YYYY-MM-DD with from < to")
		return
	}
	res, err := h.Store.Allocation(r.Context(), sess.Scope(), from, to)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
