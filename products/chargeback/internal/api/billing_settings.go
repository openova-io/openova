package api

import (
	"errors"
	"net/http"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Billing settings (DESIGN.md §2.11) — operator-only. Today the one setting
// is the discount combination rule: how several percent discounts on one
// rated line combine at statement time.
//
// GET /api/v1/billing-settings → {discount_rule, updated_at}
// PUT /api/v1/billing-settings {discount_rule} → the stored settings;
//     400 names the accepted rules when the value is unknown;
//     audit billing.settings {discount_rule, previous}.
//
// The rule is read at statement run time and recorded on every statement
// the run writes, so changing it never rewrites an issued bill — the next
// run states the new rule.

type billingSettingsBody struct {
	DiscountRule string `json:"discount_rule"`
	// InvoicePrefix is what an issued statement's invoice number starts with
	// (DESIGN.md §8). Absent leaves the stored prefix alone, so a client that
	// only knows about the discount rule cannot silently reset it.
	InvoicePrefix *string `json:"invoice_prefix"`
}

func (h *Handler) getBillingSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	s, err := h.Store.GetBillingSettings(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (h *Handler) putBillingSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	var in billingSettingsBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	prev, err := h.Store.GetBillingSettings(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	next := store.BillingSettings{DiscountRule: in.DiscountRule, InvoicePrefix: prev.InvoicePrefix}
	if in.InvoicePrefix != nil {
		next.InvoicePrefix = *in.InvoicePrefix
	}
	s, err := h.Store.UpdateBillingSettings(r.Context(), next)
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "billing.settings", map[string]any{"discount_rule": s.DiscountRule, "previous": prev.DiscountRule,
		"invoice_prefix": s.InvoicePrefix, "previous_invoice_prefix": prev.InvoicePrefix})
	writeJSON(w, http.StatusOK, s)
}
