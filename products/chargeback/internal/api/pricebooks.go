package api

import (
	"encoding/csv"
	"fmt"
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

// deletePriceBook removes a rate card; 409 (with the customer names in
// details) while any customer is assigned to it.
func (h *Handler) deletePriceBook(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	pb, err := h.Store.GetPriceBook(r.Context(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	assigned, err := h.Store.DeletePriceBook(r.Context(), id)
	if err != nil {
		if store.IsConflict(err) {
			writeErrDetails(w, http.StatusConflict, err.Error(), map[string]any{"customers": assigned})
			return
		}
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "pricebook.delete", map[string]any{"id": id, "name": pb.Name, "items": len(pb.Items)})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

// clonePriceBook copies a book (header + every item) under a new name —
// the way a negotiated per-account book is made from the list book.
func (h *Handler) clonePriceBook(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	id := r.PathValue("id")
	pb, err := h.Store.ClonePriceBook(r.Context(), id, in.Name)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "pricebook.clone", map[string]any{"id": pb.ID, "name": pb.Name, "from": id, "items": len(pb.Items)})
	writeJSON(w, http.StatusCreated, pb)
}

// priceItemBody is one SKU for add/edit. unit_price and annual_price accept
// a JSON number or numeric string; when annual_price is given, unit_price is
// derived from it through the book's divisor exactly as the CSV import does.
type priceItemBody struct {
	SKU         string         `json:"sku"`
	Unit        *string        `json:"unit"`
	UnitPrice   *store.Decimal `json:"unit_price"`
	AnnualPrice *store.Decimal `json:"annual_price"`
	Description *string        `json:"description"`
}

// resolvePrice returns the (unit, annual) pair to store from a body: annual
// wins and derives unit; a direct unit price clears annual. ok=false when
// the body carries no price at all.
func resolvePrice(b priceItemBody, divisor int) (unit store.Decimal, annual *store.Decimal, ok bool, msg string) {
	if b.AnnualPrice != nil && strings.TrimSpace(string(*b.AnnualPrice)) != "" {
		up, err := rating.UnitPrice(string(*b.AnnualPrice), divisor)
		if err != nil {
			return "", nil, false, "annual_price: " + err.Error()
		}
		a := store.Decimal(strings.TrimSpace(string(*b.AnnualPrice)))
		return up, &a, true, ""
	}
	if b.UnitPrice != nil && strings.TrimSpace(string(*b.UnitPrice)) != "" {
		v := strings.TrimSpace(string(*b.UnitPrice))
		if strings.HasPrefix(v, "-") {
			return "", nil, false, "unit_price must not be negative"
		}
		return store.Decimal(v), nil, true, ""
	}
	return "", nil, false, ""
}

// addPriceItem adds one SKU to a book (409 when the SKU exists).
func (h *Handler) addPriceItem(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	var in priceItemBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	in.SKU = strings.TrimSpace(in.SKU)
	if in.SKU == "" || in.Unit == nil || strings.TrimSpace(*in.Unit) == "" {
		writeErr(w, http.StatusBadRequest, "sku and unit are required")
		return
	}
	pb, err := h.Store.GetPriceBook(r.Context(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	unit, annual, ok, msg := resolvePrice(in, pb.AnnualDivisor)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if !ok {
		writeErr(w, http.StatusBadRequest, in.SKU+": unit_price or annual_price is required")
		return
	}
	item := store.PriceItem{SKU: in.SKU, Unit: *in.Unit, UnitPrice: unit, AnnualPrice: annual}
	if in.Description != nil {
		item.Description = *in.Description
	}
	it, err := h.Store.AddPriceItem(r.Context(), id, item)
	if err != nil {
		if store.IsConflict(err) {
			writeErr(w, http.StatusConflict, "sku "+in.SKU+" is already in this price book; PATCH it instead")
			return
		}
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "pricebook.item.add", map[string]any{"id": id, "sku": it.SKU, "unit_price": string(it.UnitPrice)})
	writeJSON(w, http.StatusCreated, it)
}

// patchPriceItem edits one SKU (404 when it is not in the book).
func (h *Handler) patchPriceItem(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	id, sku := r.PathValue("id"), r.PathValue("sku")
	var in priceItemBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if in.SKU != "" && strings.TrimSpace(in.SKU) != sku {
		writeErr(w, http.StatusBadRequest, "sku cannot be renamed; delete and add it instead")
		return
	}
	pb, err := h.Store.GetPriceBook(r.Context(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	p := store.PriceItemPatch{Description: in.Description}
	if in.Unit != nil {
		if strings.TrimSpace(*in.Unit) == "" {
			writeErr(w, http.StatusBadRequest, "unit must not be empty")
			return
		}
		p.Unit = in.Unit
	}
	unit, annual, ok, msg := resolvePrice(in, pb.AnnualDivisor)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if ok {
		p.UnitPrice, p.AnnualPrice = &unit, annual
	}
	if p.Unit == nil && p.Description == nil && p.UnitPrice == nil {
		writeErr(w, http.StatusBadRequest, "nothing to update: give unit, unit_price, annual_price or description")
		return
	}
	it, err := h.Store.UpdatePriceItem(r.Context(), id, sku, p)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "pricebook.item.update", map[string]any{"id": id, "sku": sku, "unit_price": string(it.UnitPrice)})
	writeJSON(w, http.StatusOK, it)
}

func (h *Handler) deletePriceItem(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	id, sku := r.PathValue("id"), r.PathValue("sku")
	if err := h.Store.DeletePriceItem(r.Context(), id, sku); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "pricebook.item.delete", map[string]any{"id": id, "sku": sku})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "sku": sku})
}

// exportPriceBook writes the book as CSV in the import layout
// (sku,unit,annual_price,unit_price,description), so a file exported here
// round-trips through POST /pricebooks/{id}/import unchanged.
func (h *Handler) exportPriceBook(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAuth(w, r); !ok {
		return
	}
	pb, err := h.Store.GetPriceBook(r.Context(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="pricebook-%s.csv"`, csvFileToken(pb.Name)))
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"sku", "unit", "annual_price", "unit_price", "description"})
	for _, it := range pb.Items {
		annual := ""
		if it.AnnualPrice != nil {
			annual = string(*it.AnnualPrice)
		}
		_ = cw.Write([]string{it.SKU, it.Unit, annual, string(it.UnitPrice), it.Description})
	}
	cw.Flush()
}

// csvFileToken reduces a book name to a safe filename fragment.
func csvFileToken(name string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
		case c == ' ', c == '-', c == '_', c == '.':
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "export"
	}
	return s
}

// priceBookCoverage reports which SKUs the assigned customers used in the
// last 30 days and whether the book prices them (DESIGN.md §2.5).
// Operator-only: it names customers across the whole Sovereign.
func (h *Handler) priceBookCoverage(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	now := h.Now().UTC()
	cov, err := h.Store.PriceBookCoverage(r.Context(), r.PathValue("id"), now.AddDate(0, 0, -30), now)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cov)
}
