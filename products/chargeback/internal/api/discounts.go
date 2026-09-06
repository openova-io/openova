package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// listDiscounts returns a customer's discounts and campaigns.
func (h *Handler) listDiscounts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireCustomer(w, r, id, false)
	if !ok {
		return
	}
	ds, err := h.Store.ListDiscounts(r.Context(), s.Scope(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"discounts": ds})
}

// createDiscount adds a discount or a time-boxed campaign (#6862).
//
// Operator-only: a discount changes what a customer is charged, so a customer
// principal must never be able to grant themselves one.
func (h *Handler) createDiscount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	var in struct {
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		Value    string `json:"value"`
		SKU      string `json:"sku"`
		StartsAt string `json:"starts_at"`
		EndsAt   string `json:"ends_at"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if in.Kind != "percent" && in.Kind != "fixed" {
		writeErr(w, http.StatusBadRequest, "kind must be percent or fixed")
		return
	}
	if strings.TrimSpace(in.Value) == "" {
		writeErr(w, http.StatusBadRequest, "value is required")
		return
	}
	// A percentage above 100 is almost always a typo for a fixed amount, and
	// it would silently zero every bill for the campaign's lifetime.
	if in.Kind == "percent" {
		if v, err := parseDecimalPercent(in.Value); err != nil || v < 0 || v > 100 {
			writeErr(w, http.StatusBadRequest, "a percent discount must be between 0 and 100")
			return
		}
	}
	starts, err := optTime(in.StartsAt)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "starts_at must be RFC3339 or YYYY-MM-DD")
		return
	}
	ends, err := optTime(in.EndsAt)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "ends_at must be RFC3339 or YYYY-MM-DD")
		return
	}
	if starts != nil && ends != nil && !starts.Before(*ends) {
		writeErr(w, http.StatusBadRequest, "starts_at must be before ends_at")
		return
	}
	if _, err := h.Store.GetCustomer(r.Context(), store.OperatorScope, id); err != nil {
		storeErr(w, err)
		return
	}
	d, err := h.Store.CreateDiscount(r.Context(), store.DiscountInput{
		CustomerID: id, Name: in.Name, Kind: in.Kind,
		Value: store.Decimal(strings.TrimSpace(in.Value)),
		SKU:   in.SKU, StartsAt: starts, EndsAt: ends,
	})
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &id, "discount.create", map[string]any{
		"discount_id": d.ID, "name": d.Name, "kind": d.Kind, "value": string(d.Value), "sku": d.SKU,
	})
	writeJSON(w, http.StatusCreated, d)
}

// setDiscountActive enables or disables a discount. Deactivating rather than
// deleting keeps a finished campaign visible on the statements it affected.
func (h *Handler) setDiscountActive(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	var in struct {
		Active *bool `json:"active"`
	}
	if err := decode(r, &in); err != nil || in.Active == nil {
		writeErr(w, http.StatusBadRequest, "active (bool) is required")
		return
	}
	did := r.PathValue("did")
	if err := h.Store.SetDiscountActive(r.Context(), did, *in.Active); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "discount.active", map[string]any{"discount_id": did, "active": *in.Active})
	writeJSON(w, http.StatusOK, map[string]any{"id": did, "active": *in.Active})
}

func optTime(s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC()
			return &u, nil
		}
	}
	return nil, errBadTime
}

var errBadTime = &timeErr{}

type timeErr struct{}

func (e *timeErr) Error() string { return "unparseable time" }

// parseDecimalPercent parses a percent value for range checking only; the
// stored value stays the exact string so rating keeps its precision.
func parseDecimalPercent(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%g", &f)
	return f, err
}
