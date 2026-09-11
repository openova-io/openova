package api

import (
	"errors"
	"net/http"

	"github.com/openova-io/openova/products/chargeback/internal/access"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Tax rules and tax categories (DESIGN.md §17) — the Configure → Tax screen.
//
//	GET    /api/v1/tax/rules                  metering.read
//	POST   /api/v1/tax/rules                  settings.manage
//	PUT    /api/v1/tax/rules/{id}             settings.manage
//	DELETE /api/v1/tax/rules/{id}             settings.manage
//	GET    /api/v1/tax/categories             metering.read
//	PUT    /api/v1/tax/categories             settings.manage
//	DELETE /api/v1/tax/categories/{sku}       settings.manage
//
// Reading is metering.read, the same gate the billing settings read is
// behind: a rate is not a secret and every role that can read a bill can see
// why it was taxed. WRITING is settings.manage, exactly as the deliverable
// requires — a tax rule changes what every future invoice charges.

func (h *Handler) listTaxRules(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.MeteringRead); !ok {
		return
	}
	rules, err := h.Store.ListTaxRules(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules, "kinds": store.TaxKinds})
}

func (h *Handler) createTaxRule(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.SettingsManage); !ok {
		return
	}
	var in store.TaxRule
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	rule, err := h.Store.CreateTaxRule(r.Context(), in)
	if !h.taxRuleErr(w, err) {
		return
	}
	h.audit(r, nil, "tax.rule.create", map[string]any{"id": rule.ID, "name": rule.Name, "country": rule.Country, "region": rule.Region,
		"category": rule.Category, "kind": rule.Kind, "rate": rule.Rate, "effective_from": rule.EffectiveFrom, "effective_to": rule.EffectiveTo})
	writeJSON(w, http.StatusCreated, rule)
}

func (h *Handler) updateTaxRule(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.SettingsManage); !ok {
		return
	}
	var in store.TaxRule
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	rule, err := h.Store.UpdateTaxRule(r.Context(), r.PathValue("id"), in)
	if !h.taxRuleErr(w, err) {
		return
	}
	h.audit(r, nil, "tax.rule.update", map[string]any{"id": rule.ID, "name": rule.Name, "country": rule.Country, "region": rule.Region,
		"category": rule.Category, "kind": rule.Kind, "rate": rule.Rate, "effective_from": rule.EffectiveFrom, "effective_to": rule.EffectiveTo})
	writeJSON(w, http.StatusOK, rule)
}

func (h *Handler) deleteTaxRule(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.SettingsManage); !ok {
		return
	}
	id := r.PathValue("id")
	err := h.Store.DeleteTaxRule(r.Context(), id)
	if !h.taxRuleErr(w, err) {
		return
	}
	// A deleted rule is NOT removed from the invoices it rated: their tax
	// summary is frozen and still names this id.
	h.audit(r, nil, "tax.rule.delete", map[string]any{"id": id})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listTaxCategories(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.MeteringRead); !ok {
		return
	}
	cats, err := h.Store.ListTaxCategories(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"categories": cats})
}

func (h *Handler) putTaxCategory(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.SettingsManage); !ok {
		return
	}
	var in store.TaxCategoryRule
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	cat, err := h.Store.PutTaxCategory(r.Context(), in)
	if !h.taxRuleErr(w, err) {
		return
	}
	h.audit(r, nil, "tax.category.put", map[string]any{"sku": cat.SKU, "category": cat.Category})
	writeJSON(w, http.StatusOK, cat)
}

func (h *Handler) deleteTaxCategory(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.SettingsManage); !ok {
		return
	}
	sku := r.PathValue("sku")
	if !h.taxRuleErr(w, h.Store.DeleteTaxCategory(r.Context(), sku)) {
		return
	}
	h.audit(r, nil, "tax.category.delete", map[string]any{"sku": sku})
	w.WriteHeader(http.StatusNoContent)
}

// taxRuleErr writes the response for a store error and reports whether the
// handler may continue. ErrInvalid is 400 with the message, ErrConflict 409.
func (h *Handler) taxRuleErr(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, invalidMessage(err))
	default:
		storeErr(w, err)
	}
	return false
}
