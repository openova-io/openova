package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/access"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Currency rates (#6867 follow-up, DESIGN.md §3.10). Operator-only: an
// exchange rate changes every customer's figure on every screen.
//
// GET    /api/v1/currencies         → {reporting_currency, rates: [{code, per_base, source, updated_at}]}
// GET    /api/v1/currencies/{code}  → one rate (the reporting currency answers per_base 1, source "reporting")
// PUT    /api/v1/currencies/{code}  {per_base} → the stored rate; audit currency.rate
// DELETE /api/v1/currencies/{code}  → {deleted, code}; audit currency.rate {deleted: true}
//
// per_base is how many units of {code} one unit of the reporting currency
// buys (1 OMR = 2.6 USD → PUT /currencies/USD {"per_base": 2.6}). The
// reporting currency is allocation_settings.currency (PUT
// /allocation/settings changes it); writing a rate for it is refused with
// 400 "reporting currency" — its rate is 1 by definition.

type currencyRatesDoc struct {
	ReportingCurrency string               `json:"reporting_currency"`
	Rates             []store.CurrencyRate `json:"rates"`
}

type currencyRateBody struct {
	PerBase store.Decimal `json:"per_base"`
	// Source names where the rate came from ("manual" when omitted); a
	// future importer records its feed here.
	Source *string `json:"source"`
}

// invalidMessage is a store.ErrInvalid message without its sentinel prefix,
// so the 400 body reads "per_base must be …", not "invalid: per_base must …".
func invalidMessage(err error) string {
	return strings.TrimPrefix(err.Error(), store.ErrInvalid.Error()+": ")
}

func (h *Handler) listCurrencies(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.MeteringRead); !ok {
		return
	}
	reporting, err := h.Store.ReportingCurrency(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	rates, err := h.Store.ListCurrencyRates(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, currencyRatesDoc{ReportingCurrency: reporting, Rates: rates})
}

func (h *Handler) getCurrency(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.MeteringRead); !ok {
		return
	}
	code, ok := store.NormalizeCurrencyCode(r.PathValue("code"))
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	reporting, err := h.Store.ReportingCurrency(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	if code == reporting {
		// Not a stored row, but the one every rate is relative to.
		writeJSON(w, http.StatusOK, store.CurrencyRate{Code: code, PerBase: "1", Source: "reporting"})
		return
	}
	rate, err := h.Store.GetCurrencyRate(r.Context(), code)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rate)
}

func (h *Handler) putCurrency(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.RatingManage); !ok {
		return
	}
	code := r.PathValue("code")
	var body currencyRateBody
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if strings.TrimSpace(string(body.PerBase)) == "" {
		writeErr(w, http.StatusBadRequest, "per_base is required")
		return
	}
	source := ""
	if body.Source != nil {
		source = *body.Source
	}
	// The previous value, for the audit trail; absent on a first write.
	var previous *store.Decimal
	if prev, err := h.Store.GetCurrencyRate(r.Context(), code); err == nil {
		previous = &prev.PerBase
	}
	rate, err := h.Store.PutCurrencyRate(r.Context(), code, body.PerBase, source)
	switch {
	case errors.Is(err, store.ErrInvalid):
		// Includes ErrReportingCurrency: "reporting currency OMR: its rate is 1 by definition".
		writeErr(w, http.StatusBadRequest, invalidMessage(err))
		return
	case err != nil:
		storeErr(w, err)
		return
	}
	details := map[string]any{"code": rate.Code, "per_base": string(rate.PerBase), "source": rate.Source}
	if previous != nil {
		details["previous_per_base"] = string(*previous)
	}
	h.audit(r, nil, "currency.rate", details)
	writeJSON(w, http.StatusOK, rate)
}

func (h *Handler) deleteCurrency(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.RatingManage); !ok {
		return
	}
	code := r.PathValue("code")
	prev, err := h.Store.GetCurrencyRate(r.Context(), code)
	if err != nil {
		storeErr(w, err)
		return
	}
	if err := h.Store.DeleteCurrencyRate(r.Context(), code); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "currency.rate", map[string]any{"code": prev.Code, "deleted": true, "previous_per_base": string(prev.PerBase)})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "code": prev.Code})
}
