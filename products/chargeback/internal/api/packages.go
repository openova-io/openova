package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/access"
	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// PACKAGES AND THE ENTITLEMENT MATRIX (DESIGN.md §22, founder direction
// 2026-10-10). One document — packagesDocument — answers the public
// storefront at GET /public/packages and the console at
// GET /pricebooks/{id}/packages, so the comparison table a prospect sees and
// the matrix an operator edits cannot drift. Its shape is the storefront's
// contract:
//
//	{
//	  "currency": "OMR", "price_book": "OpenOva plans", "prices_as_of": "2026-09-11",
//	  "packages": [{"sku": "plan.s", "name": "S", "price_month": "5.000",
//	                "includes": {"vcpu": 2, "memory_gb": 4, "bandwidth_mbps": 50}}, …],
//	  "features": [{"key": "backup", "name": "Backup", "blurb": "…", "kind": "boolean",
//	                "cells": {"plan.s": {"state": "optional", "addon_sku": "addon.backup",
//	                                     "price_month": "1.500", "included_from": "plan.xl"},
//	                          "plan.xl": {"state": "included"}}},
//	               {"key": "bandwidth", "name": "Bandwidth", "kind": "quantity", "unit": "Mbps",
//	                "cells": {"plan.s": {"state": "included", "quantity": 50}, …}}]
//	}
//
// Money is a string at the currency's minor unit (price_month = unit price ×
// 730, rounded once); packages are ordered by price, features by their sort
// order; "included_from" names the cheapest package that includes the
// feature and is omitted when none does. Writes are rating.manage.

type packagesDoc struct {
	Currency   string           `json:"currency"`
	PriceBook  string           `json:"price_book"`
	PricesAsOf string           `json:"prices_as_of"`
	Packages   []packageDoc     `json:"packages"`
	Features   []packageFeature `json:"features"`
}

type packageDoc struct {
	SKU        string                   `json:"sku"`
	Name       string                   `json:"name"`
	PriceMonth string                   `json:"price_month"`
	Includes   map[string]store.Decimal `json:"includes"`
}

type packageFeature struct {
	Key   string                 `json:"key"`
	Name  string                 `json:"name"`
	Blurb string                 `json:"blurb,omitempty"`
	Kind  string                 `json:"kind"`
	Unit  string                 `json:"unit,omitempty"`
	Cells map[string]packageCell `json:"cells"`
}

type packageCell struct {
	State        string         `json:"state"`
	AddonSKU     string         `json:"addon_sku,omitempty"`
	PriceMonth   string         `json:"price_month,omitempty"`
	IncludedFrom string         `json:"included_from,omitempty"`
	Quantity     *store.Decimal `json:"quantity,omitempty"`
	Note         string         `json:"note,omitempty"`
}

// trimDec drops the trailing zeros of a decimal ("50.000000" → "50").
func trimDec(d store.Decimal) store.Decimal {
	s := strings.TrimSpace(string(d))
	if !strings.Contains(s, ".") {
		return store.Decimal(s)
	}
	s = strings.TrimRight(s, "0")
	return store.Decimal(strings.TrimSuffix(s, "."))
}

// includesKey is the key a quantity feature's included quantity sits under in
// a package's `includes`: the feature key and its unit, lower-case
// ("bandwidth" + "Mbps" → bandwidth_mbps).
func includesKey(f store.Feature) string {
	unit := strings.ToLower(strings.TrimSpace(f.Unit))
	var b strings.Builder
	for _, c := range unit {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
		default:
			b.WriteByte('_')
		}
	}
	if u := strings.Trim(b.String(), "_"); u != "" {
		return f.Key + "_" + u
	}
	return f.Key
}

// packagesDocument builds the one document from a book, its matrix cells and
// the features in matrix order. Only features with at least one cell in the
// book appear; a plan with no cell for a feature reads not_offered.
func packagesDocument(pb store.PriceBook, features []store.Feature, cells []store.Entitlement) (packagesDoc, error) {
	digits := store.MinorUnitDigits(pb.Currency)
	doc := packagesDoc{Currency: pb.Currency, PriceBook: pb.Name, PricesAsOf: pb.UpdatedAt.UTC().Format("2006-01-02"), Packages: []packageDoc{}, Features: []packageFeature{}}
	items := map[string]store.PriceItem{}
	for _, it := range pb.Items {
		items[it.SKU] = it
	}
	// The packages: the book's plan items, cheapest first.
	type plan struct {
		sku, slug string
		monthly   store.Decimal
	}
	var plans []plan
	for _, it := range pb.Items {
		if !strings.HasPrefix(it.SKU, store.PlanSKUPrefix) {
			continue
		}
		slug := strings.TrimPrefix(it.SKU, store.PlanSKUPrefix)
		if !store.PlanBillable(slug) || strings.Contains(slug, ".") {
			continue
		}
		m, err := rating.MonthlyAt(it.UnitPrice, digits)
		if err != nil {
			return doc, fmt.Errorf("plan %s: %w", it.SKU, err)
		}
		plans = append(plans, plan{sku: it.SKU, slug: slug, monthly: m})
	}
	sort.SliceStable(plans, func(i, j int) bool {
		a, _ := rating.CompareDecimals(plans[i].monthly, plans[j].monthly)
		return a < 0
	})
	cellOf := map[string]map[string]store.Entitlement{} // feature id → plan sku → cell
	for _, e := range cells {
		if cellOf[e.FeatureID] == nil {
			cellOf[e.FeatureID] = map[string]store.Entitlement{}
		}
		cellOf[e.FeatureID][e.PlanSKU] = e
	}
	// Which quantity features each plan includes, for `includes`.
	includes := map[string]map[string]store.Decimal{}
	for _, p := range plans {
		inc := map[string]store.Decimal{}
		if v, m, ok := store.PlanShape(p.slug); ok {
			inc["vcpu"] = store.Decimal(fmt.Sprint(v))
			inc["memory_gb"] = store.Decimal(fmt.Sprint(m))
		}
		includes[p.sku] = inc
	}
	for _, f := range features {
		byPlan := cellOf[f.ID]
		if len(byPlan) == 0 {
			continue
		}
		includedFrom := ""
		for _, p := range plans {
			if c, ok := byPlan[p.sku]; ok && c.State == store.EntitlementIncluded {
				includedFrom = p.sku
				break
			}
		}
		pf := packageFeature{Key: f.Key, Name: f.Name, Blurb: f.Blurb, Kind: f.Kind, Unit: f.Unit, Cells: map[string]packageCell{}}
		for _, p := range plans {
			cell := packageCell{State: store.EntitlementNotOffered}
			if c, ok := byPlan[p.sku]; ok {
				cell.State = c.State
				cell.Note = c.Note
				if c.IncludedQuantity != nil && f.Kind == store.FeatureKindQuantity {
					q := trimDec(*c.IncludedQuantity)
					cell.Quantity = &q
					if c.State == store.EntitlementIncluded {
						includes[p.sku][includesKey(f)] = q
					}
				}
				if c.State == store.EntitlementOptional && f.AddonSKU != "" {
					cell.AddonSKU = f.AddonSKU
					if it, priced := items[f.AddonSKU]; priced {
						m, err := rating.MonthlyAt(it.UnitPrice, digits)
						if err != nil {
							return doc, fmt.Errorf("add-on %s: %w", f.AddonSKU, err)
						}
						cell.PriceMonth = string(m)
					}
				}
			}
			if cell.State != store.EntitlementIncluded && includedFrom != "" {
				cell.IncludedFrom = includedFrom
			}
			pf.Cells[p.sku] = cell
		}
		doc.Features = append(doc.Features, pf)
	}
	for _, p := range plans {
		doc.Packages = append(doc.Packages, packageDoc{SKU: p.sku, Name: store.PlanName(p.slug), PriceMonth: string(p.monthly), Includes: includes[p.sku]})
	}
	return doc, nil
}

// packagesOf assembles the document for one book.
func (h *Handler) packagesOf(r *http.Request, pb store.PriceBook) (packagesDoc, error) {
	features, err := h.Store.ListFeatures(r.Context())
	if err != nil {
		return packagesDoc{}, err
	}
	cells, err := h.Store.PackageCells(r.Context(), pb.ID)
	if err != nil {
		return packagesDoc{}, err
	}
	return packagesDocument(pb, features, cells)
}

// writeJSONCacheable is writeJSON for a public document a browser or a CDN
// may keep for `seconds`: list prices change by the day, not by the request.
func writeJSONCacheable(w http.ResponseWriter, status int, v any, seconds int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", seconds))
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("write response", "error", err)
	}
}

// publicPackages — GET /api/v1/public/packages: the plans book's packages
// and matrix, unauthenticated, cacheable for a minute, CORS like the catalog.
func (h *Handler) publicPackages(w http.ResponseWriter, r *http.Request) {
	pb, err := h.Store.GetPriceBookByName(r.Context(), store.PlanBookName)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "no packages are published yet")
		return
	}
	if err != nil {
		storeErr(w, err)
		return
	}
	if pb.Items, err = h.Store.ListPriceItems(r.Context(), pb.ID); err != nil {
		storeErr(w, err)
		return
	}
	doc, err := h.packagesOf(r, pb)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSONCacheable(w, http.StatusOK, doc, 60)
}

// getPackages — GET /api/v1/pricebooks/{id}/packages: the SAME document for
// one book, behind the book's read guard.
func (h *Handler) getPackages(w http.ResponseWriter, r *http.Request) {
	pb, ok := h.bookForRead(w, r)
	if !ok {
		return
	}
	doc, err := h.packagesOf(r, pb)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

// ---------------------------------------------------------------------------
// features
// ---------------------------------------------------------------------------

type featureBody struct {
	Key       string  `json:"key"`
	Name      *string `json:"name"`
	Blurb     *string `json:"blurb"`
	Kind      *string `json:"kind"`
	Unit      *string `json:"unit"`
	AddonSKU  *string `json:"addon_sku"`
	SortOrder *int    `json:"sort_order"`
}

func (h *Handler) listFeatures(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireBookReader(w, r); !ok {
		return
	}
	list, err := h.Store.ListFeatures(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"features": list})
}

func (h *Handler) getFeature(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireBookReader(w, r); !ok {
		return
	}
	f, err := h.Store.GetFeature(r.Context(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (h *Handler) createFeature(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.RatingManage); !ok {
		return
	}
	var in featureBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	fi := store.FeatureInput{Key: in.Key}
	if in.Name != nil {
		fi.Name = *in.Name
	}
	if in.Blurb != nil {
		fi.Blurb = *in.Blurb
	}
	if in.Kind != nil {
		fi.Kind = *in.Kind
	}
	if in.Unit != nil {
		fi.Unit = *in.Unit
	}
	if in.AddonSKU != nil {
		fi.AddonSKU = *in.AddonSKU
	}
	if in.SortOrder != nil {
		fi.SortOrder = *in.SortOrder
	}
	if strings.TrimSpace(fi.Key) == "" || strings.TrimSpace(fi.Name) == "" {
		writeErr(w, http.StatusBadRequest, "key and name are required")
		return
	}
	f, err := h.Store.CreateFeature(r.Context(), fi)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "feature.create", map[string]any{"id": f.ID, "key": f.Key, "kind": f.Kind, "addon_sku": f.AddonSKU})
	writeJSON(w, http.StatusCreated, f)
}

func (h *Handler) patchFeature(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.RatingManage); !ok {
		return
	}
	var in featureBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if in.Key != "" {
		writeErr(w, http.StatusBadRequest, "a feature key cannot be renamed; the matrix, the add-ons and the invoices name it")
		return
	}
	p := store.FeaturePatch{Name: in.Name, Blurb: in.Blurb, Kind: in.Kind, Unit: in.Unit, AddonSKU: in.AddonSKU, SortOrder: in.SortOrder}
	if p.Name == nil && p.Blurb == nil && p.Kind == nil && p.Unit == nil && p.AddonSKU == nil && p.SortOrder == nil {
		writeErr(w, http.StatusBadRequest, "nothing to update: give name, blurb, kind, unit, addon_sku or sort_order")
		return
	}
	f, err := h.Store.UpdateFeature(r.Context(), r.PathValue("id"), p)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "feature.update", map[string]any{"id": f.ID, "key": f.Key, "kind": f.Kind, "addon_sku": f.AddonSKU})
	writeJSON(w, http.StatusOK, f)
}

// deleteFeature refuses with 409 — naming the package cells and the Sources
// that still depend on the feature — rather than letting a feature vanish
// from the matrix that explains an invoice.
func (h *Handler) deleteFeature(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.RatingManage); !ok {
		return
	}
	f, err := h.Store.GetFeature(r.Context(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	dep, err := h.Store.DeleteFeature(r.Context(), f.ID)
	if err != nil {
		if store.IsConflict(err) {
			writeErrDetails(w, http.StatusConflict, conflictMessage(err), dep)
			return
		}
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "feature.delete", map[string]any{"id": f.ID, "key": f.Key})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": f.ID, "key": f.Key})
}

// ---------------------------------------------------------------------------
// the matrix cell
// ---------------------------------------------------------------------------

// packageCellBody is one cell as the console writes it. addon_monthly, when
// given, prices the feature's add-on SKU in this book per month in the same
// write (annual = monthly × 12, unit price through the book's divisor — the
// arithmetic the plans themselves use), so "optional at 1.500 a month" is one
// save and the optional-needs-a-price rule is checked against what was just
// priced.
type packageCellBody struct {
	State            string         `json:"state"`
	IncludedQuantity *store.Decimal `json:"included_quantity"`
	Note             *string        `json:"note"`
	AddonMonthly     *store.Decimal `json:"addon_monthly"`
}

func (h *Handler) putPackageCell(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.RatingManage); !ok {
		return
	}
	id, planSKU, ref := r.PathValue("id"), r.PathValue("plan"), r.PathValue("feature")
	var in packageCellBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if !store.ValidEntitlementState(strings.ToLower(strings.TrimSpace(in.State))) {
		writeErr(w, http.StatusBadRequest, "state must be included, optional or not_offered")
		return
	}
	if h.derivedBookWriteRefused(w, r, id) {
		return
	}
	pb, err := h.Store.GetPriceBook(r.Context(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	f, err := h.Store.GetFeature(r.Context(), ref)
	if err != nil {
		storeErr(w, err)
		return
	}
	if in.AddonMonthly != nil && strings.TrimSpace(string(*in.AddonMonthly)) != "" {
		if f.AddonSKU == "" {
			writeErr(w, http.StatusBadRequest, f.Key+" has no add-on SKU to price; set one on the feature first")
			return
		}
		if f.Kind != store.FeatureKindBoolean {
			writeErr(w, http.StatusBadRequest, f.Key+" is a quantity feature; its SKU "+f.AddonSKU+" is priced per unit on the Items tab")
			return
		}
		if strings.HasPrefix(strings.TrimSpace(string(*in.AddonMonthly)), "-") {
			writeErr(w, http.StatusBadRequest, "addon_monthly must not be negative")
			return
		}
		annual, err := rating.Amount(*in.AddonMonthly, "12")
		if err != nil {
			writeErr(w, http.StatusBadRequest, "addon_monthly: "+err.Error())
			return
		}
		unit, err := rating.UnitPrice(string(annual), pb.AnnualDivisor)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "addon_monthly: "+err.Error())
			return
		}
		desc := fmt.Sprintf("%s add-on: %s %s/month, billed per plan-hour like the plan it extends", f.Name, trimDec(*in.AddonMonthly), pb.Currency)
		if _, err := h.Store.GetPriceItem(r.Context(), pb.ID, f.AddonSKU); errors.Is(err, store.ErrNotFound) {
			if _, err := h.Store.AddPriceItem(r.Context(), pb.ID, store.PriceItem{SKU: f.AddonSKU, Unit: store.PlanUnit, UnitPrice: unit, AnnualPrice: &annual, Description: desc}); err != nil {
				storeErr(w, err)
				return
			}
		} else if err != nil {
			storeErr(w, err)
			return
		} else if _, err := h.Store.UpdatePriceItem(r.Context(), pb.ID, f.AddonSKU, store.PriceItemPatch{UnitPrice: &unit, AnnualPrice: &annual}); err != nil {
			storeErr(w, err)
			return
		}
		if err := h.rederiveForBook(r, pb.ID); err != nil {
			storeErr(w, err)
			return
		}
		h.audit(r, nil, "pricebook.item.update", map[string]any{"id": pb.ID, "sku": f.AddonSKU, "unit_price": string(unit), "addon_of": f.Key})
	}
	note := ""
	if in.Note != nil {
		note = *in.Note
	} else if cur, err := h.Store.GetEntitlement(r.Context(), pb.ID, planSKU, f.ID); err == nil {
		note = cur.Note
	}
	cell, err := h.Store.PutEntitlement(r.Context(), pb.ID, planSKU, f.ID, store.EntitlementInput{State: in.State, IncludedQuantity: in.IncludedQuantity, Note: note})
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "pricebook.package.put", map[string]any{"id": pb.ID, "plan": planSKU, "feature": f.Key, "state": cell.State, "included_quantity": cell.IncludedQuantity})
	writeJSON(w, http.StatusOK, cell)
}

func (h *Handler) deletePackageCell(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.RatingManage); !ok {
		return
	}
	id, planSKU, ref := r.PathValue("id"), r.PathValue("plan"), r.PathValue("feature")
	if h.derivedBookWriteRefused(w, r, id) {
		return
	}
	if err := h.Store.DeleteEntitlement(r.Context(), id, planSKU, ref); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "pricebook.package.delete", map[string]any{"id": id, "plan": planSKU, "feature": ref})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "plan": planSKU, "feature": ref})
}

// ---------------------------------------------------------------------------
// the Source's add-ons
// ---------------------------------------------------------------------------

// putSourceAddons — PUT /api/v1/customers/{id}/sources/{sid}/addons
// {"addons": ["backup"]}: replaces the optional features the Organization has
// taken. customers.manage on the customer, like the Source's price book —
// what is billed is the operator's decision. The store refuses an included
// feature (redundant) and one the package does not offer, naming it.
func (h *Handler) putSourceAddons(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("id")
	if _, ok := h.requirePermission(w, r, access.CustomersManage, cid); !ok {
		return
	}
	src, err := h.Store.GetSource(r.Context(), store.OperatorScope, r.PathValue("sid"))
	if err != nil {
		storeErr(w, err)
		return
	}
	if src.CustomerID != cid {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	var in struct {
		Addons []string `json:"addons"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if in.Addons == nil {
		writeErr(w, http.StatusBadRequest, `body must be {"addons": [<feature key>, …]} — an empty list drops every add-on`)
		return
	}
	updated, err := h.Store.SetSourceAddons(r.Context(), src.ID, in.Addons)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &cid, "source.addons", map[string]any{"source_id": src.ID, "addons": updated.Addons})
	writeJSON(w, http.StatusOK, struct {
		store.CostSource
		Collecting bool `json:"collecting"`
	}{updated, h.collectingFor(r, updated)})
}
