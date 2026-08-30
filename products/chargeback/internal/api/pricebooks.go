package api

import (
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

func (h *Handler) listPriceBooks(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAuth(w, r); !ok {
		return
	}
	list, err := h.Store.ListPriceBooks(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pricebooks": list})
}

type priceBookBody struct {
	Name          string `json:"name"`
	Currency      string `json:"currency"`
	AnnualDivisor int    `json:"annual_divisor"`
	BillStopped   string `json:"bill_stopped"`
	EffectiveFrom string `json:"effective_from"`
}

func (b priceBookBody) validate(create bool) string {
	if create && strings.TrimSpace(b.Name) == "" {
		return "name is required"
	}
	if b.AnnualDivisor < 0 {
		return "annual_divisor must be positive"
	}
	if b.BillStopped != "" && b.BillStopped != "compute" && b.BillStopped != "storage-only" && b.BillStopped != "none" {
		return "bill_stopped must be compute, storage-only or none"
	}
	if b.EffectiveFrom != "" && !store.ValidDate(b.EffectiveFrom) {
		return "effective_from must be YYYY-MM-DD"
	}
	return ""
}

func (h *Handler) createPriceBook(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	var in priceBookBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if msg := in.validate(true); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	pb, err := h.Store.CreatePriceBook(r.Context(), store.PriceBookInput(in))
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "pricebook.create", map[string]any{"id": pb.ID, "name": pb.Name})
	writeJSON(w, http.StatusCreated, pb)
}

func (h *Handler) getPriceBook(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAuth(w, r); !ok {
		return
	}
	pb, err := h.Store.GetPriceBook(r.Context(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pb)
}

func (h *Handler) updatePriceBook(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	var in priceBookBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if msg := in.validate(false); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	pb, err := h.Store.UpdatePriceBook(r.Context(), r.PathValue("id"), store.PriceBookInput(in))
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "pricebook.update", map[string]any{"id": pb.ID})
	writeJSON(w, http.StatusOK, pb)
}

// putPriceItems replaces (default) or merges (?merge=true) the SKU list.
func (h *Handler) putPriceItems(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	var in struct {
		Items []store.PriceItem `json:"items"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	pb, err := h.Store.GetPriceBook(r.Context(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	for i := range in.Items {
		it := &in.Items[i]
		if strings.TrimSpace(it.SKU) == "" || strings.TrimSpace(it.Unit) == "" {
			writeErr(w, http.StatusBadRequest, "every item needs sku and unit")
			return
		}
		if it.AnnualPrice != nil && *it.AnnualPrice != "" {
			up, err := rating.UnitPrice(string(*it.AnnualPrice), pb.AnnualDivisor)
			if err != nil {
				writeErr(w, http.StatusBadRequest, it.SKU+": "+err.Error())
				return
			}
			it.UnitPrice = up
		} else if it.UnitPrice == "" {
			writeErr(w, http.StatusBadRequest, it.SKU+": unit_price or annual_price is required")
			return
		}
	}
	n, err := h.Store.PutPriceItems(r.Context(), id, in.Items, r.URL.Query().Get("merge") != "true")
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "pricebook.items.put", map[string]any{"id": id, "items": n})
	pb, err = h.Store.GetPriceBook(r.Context(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pb)
}

// importPriceBook loads a CSV (multipart field "file" or raw text/csv).
func (h *Handler) importPriceBook(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	pb, err := h.Store.GetPriceBook(r.Context(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	var body io.Reader = io.LimitReader(r.Body, maxBodyBytes)
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct == "multipart/form-data" {
		if err := r.ParseMultipartForm(maxBodyBytes); err != nil {
			writeErr(w, http.StatusBadRequest, "multipart: "+err.Error())
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			writeErr(w, http.StatusBadRequest, "multipart field \"file\" is required")
			return
		}
		defer f.Close()
		body = f
	}
	items, errs, err := rating.ParsePriceBookCSV(body, pb.AnnualDivisor)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	n, err := h.Store.PutPriceItems(r.Context(), id, items, r.URL.Query().Get("merge") != "true")
	if err != nil {
		storeErr(w, err)
		return
	}
	if errs == nil {
		errs = []rating.ImportError{}
	}
	h.audit(r, nil, "pricebook.import", map[string]any{"id": id, "items": n, "errors": len(errs)})
	writeJSON(w, http.StatusOK, map[string]any{"imported": n, "errors": errs})
}

func (h *Handler) priceBookTemplate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="pricebook-template.csv"`)
	_, _ = io.WriteString(w, rating.PriceBookCSVTemplate)
}
