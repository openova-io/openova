package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// discountBody is the create/replace document for a discount or campaign.
// customer_id null (or absent) makes it a GLOBAL campaign for every
// customer (#6867). Value accepts a JSON number or a numeric string and is
// kept as the exact decimal text the rating run multiplies with.
type discountBody struct {
	CustomerID *string       `json:"customer_id"`
	Name       string        `json:"name"`
	Kind       string        `json:"kind"`
	Value      store.Decimal `json:"value"`
	SKU        string        `json:"sku"`
	StartsAt   string        `json:"starts_at"`
	EndsAt     string        `json:"ends_at"`
	Active     *bool         `json:"active"`
}

// validateDiscount turns a body into a store input, or names the first
// problem. It is the single validation for every create/replace path so the
// customer-scoped and global routes cannot drift apart.
func validateDiscount(in discountBody) (store.DiscountInput, string) {
	var out store.DiscountInput
	if strings.TrimSpace(in.Name) == "" {
		return out, "name is required"
	}
	if in.Kind != "percent" && in.Kind != "fixed" {
		return out, "kind must be percent or fixed"
	}
	if strings.TrimSpace(string(in.Value)) == "" {
		return out, "value is required"
	}
	// A percentage above 100 is almost always a typo for a fixed amount, and
	// it would silently zero every bill for the campaign's lifetime.
	if in.Kind == "percent" {
		if v, err := parseDecimalPercent(string(in.Value)); err != nil || v < 0 || v > 100 {
			return out, "a percent discount must be between 0 and 100"
		}
	} else if v, err := parseDecimalPercent(string(in.Value)); err != nil || v < 0 {
		return out, "a fixed discount must not be negative"
	}
	starts, err := optTime(in.StartsAt)
	if err != nil {
		return out, "starts_at must be RFC3339 or YYYY-MM-DD"
	}
	ends, err := optTime(in.EndsAt)
	if err != nil {
		return out, "ends_at must be RFC3339 or YYYY-MM-DD"
	}
	if starts != nil && ends != nil && !starts.Before(*ends) {
		return out, "starts_at must be before ends_at"
	}
	if in.CustomerID != nil && strings.TrimSpace(*in.CustomerID) == "" {
		in.CustomerID = nil
	}
	out = store.DiscountInput{
		CustomerID: in.CustomerID, Name: in.Name, Kind: in.Kind,
		Value: store.Decimal(strings.TrimSpace(string(in.Value))),
		SKU:   strings.TrimSpace(in.SKU), StartsAt: starts, EndsAt: ends, Active: in.Active,
	}
	return out, ""
}

func discountAudit(d store.Discount) map[string]any {
	return map[string]any{
		"discount_id": d.ID, "name": d.Name, "kind": d.Kind, "value": string(d.Value), "sku": d.SKU,
		"scope": d.ScopeLabel(), "active": d.Active,
	}
}

// listDiscounts returns what applies to a customer: its own discounts plus
// the global campaigns, which carry customer_id null.
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

// listAllDiscounts is the operator's single place for every discount and
// campaign across customers (DESIGN.md §2.6).
func (h *Handler) listAllDiscounts(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	ds, err := h.Store.ListAllDiscounts(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"discounts": ds})
}

// createDiscount adds a discount or a time-boxed campaign for the customer
// in the path (#6862). A customer_id in the body must agree with the path.
//
// Operator-only: a discount changes what a customer is charged, so a customer
// principal must never be able to grant themselves one.
func (h *Handler) createDiscount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	var in discountBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if in.CustomerID != nil && *in.CustomerID != "" && *in.CustomerID != id {
		writeErr(w, http.StatusBadRequest, "customer_id in the body does not match the customer in the path")
		return
	}
	in.CustomerID = &id
	h.storeDiscount(w, r, in)
}

// createGlobalDiscount adds a discount for one customer (customer_id set) or
// a campaign for every customer (customer_id null).
func (h *Handler) createGlobalDiscount(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	var in discountBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	h.storeDiscount(w, r, in)
}

func (h *Handler) storeDiscount(w http.ResponseWriter, r *http.Request, in discountBody) {
	di, msg := validateDiscount(in)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if di.CustomerID != nil {
		if _, err := h.Store.GetCustomer(r.Context(), store.OperatorScope, *di.CustomerID); err != nil {
			storeErr(w, err)
			return
		}
	}
	d, err := h.Store.CreateDiscount(r.Context(), di)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, d.CustomerID, "discount.create", discountAudit(d))
	writeJSON(w, http.StatusCreated, d)
}

func (h *Handler) getDiscount(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	d, err := h.Store.GetDiscount(r.Context(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// updateDiscount replaces every field (PUT). Same validation as create.
func (h *Handler) updateDiscount(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	var in discountBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	di, msg := validateDiscount(in)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if di.CustomerID != nil {
		if _, err := h.Store.GetCustomer(r.Context(), store.OperatorScope, *di.CustomerID); err != nil {
			storeErr(w, err)
			return
		}
	}
	id := r.PathValue("id")
	prev, err := h.Store.GetDiscount(r.Context(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	d, err := h.Store.UpdateDiscount(r.Context(), id, di)
	if err != nil {
		storeErr(w, err)
		return
	}
	details := discountAudit(d)
	details["previous_scope"] = prev.ScopeLabel()
	h.audit(r, d.CustomerID, "discount.update", details)
	writeJSON(w, http.StatusOK, d)
}

// deleteDiscount removes a discount. Statements already rated keep their
// frozen breakdown; prefer PATCH active=false when the campaign should stay
// visible in the list.
func (h *Handler) deleteDiscount(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	d, err := h.Store.GetDiscount(r.Context(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	if err := h.Store.DeleteDiscount(r.Context(), id); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, d.CustomerID, "discount.delete", discountAudit(d))
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
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
	id := r.PathValue("id")
	if err := h.Store.SetDiscountActive(r.Context(), id, *in.Active); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "discount.active", map[string]any{"discount_id": id, "active": *in.Active})
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "active": *in.Active})
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

// parseDecimalPercent parses a value for range checking only; the stored
// value stays the exact string so rating keeps its precision.
func parseDecimalPercent(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%g", &f)
	return f, err
}
