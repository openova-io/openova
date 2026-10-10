package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"sort"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/access"
	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// PACKAGES AND THE ENTITLEMENT MATRIX (DESIGN.md §22, founder direction
// 2026-10-10; the package LADDER of 0.1.61). One document — packagesDocument
// — answers the public storefront at GET /public/packages and the console
// at GET /pricebooks/{id}/packages, so the comparison table a prospect sees
// and the matrix an operator edits cannot drift. Its shape is the
// storefront's contract (DESIGN.md §22.4):
//
//	{ "currency": "OMR", "price_book": "OpenOva plans", "prices_as_of": "2026-10-10",
//	  "groups": [{"key": "capacity", "name": "Capacity"}, …],
//	  "floor": [{"key": "ssl", "name": "Unlimited free SSL", "blurb": "…"}, …],
//	  "packages": [{"sku": "plan.m", "name": "M", "tagline": "", "price_month": "4.490",
//	                "recommended": true, "annual_months_free": 0,
//	                "shape": {"vcpu": 2, "memory_gb": 4, "vcpu_guaranteed": 0.33, "memory_gb_guaranteed": 1.33, "disk_gb": 50},
//	                "step_up": {"next_sku": "plan.l", "next_name": "L", "gap_month": "3.500",
//	                            "bundled_addon_keys": [], "bundled_addons_sum_month": "0.000", "rule_holds": true},
//	                "includes": {"vcpu": 2, "memory_gb": 4, "bandwidth_mbps": 100, "disk_gb": 50}}, …],
//	  "features": [{"key": "backup", "name": "Backup", "group": "resilience", "kind": "boolean", "addon_sku": "addon.backup", "teaser": false,
//	                "cells": {"plan.s": {"state": "optional", "addon_sku": "addon.backup", "price_month": "1.500", "included_from": "plan.xl"},
//	                          "plan.xl": {"state": "included"}}},
//	               {"key": "bandwidth", "group": "capacity", "kind": "quantity", "unit": "Mbps", "addon_sku": "eip.bandwidth_mbps",
//	                "cells": {"plan.s": {"state": "included", "quantity": 50, "overage": "hard_cap"}, …}},
//	               {"key": "dr_topology", "group": "resilience", "kind": "level", "levels": ["single region", "active-passive"],
//	                "cells": {"plan.s": {"state": "included", "level": 0, "included_from": "plan.xl"}, "plan.xl": {"state": "included", "level": 1}}},
//	               {"key": "gitea_iac", "group": "access", "kind": "access",
//	                "cells": {"plan.s": {"state": "not_offered"}, "plan.m": {"state": "included", "note": "read"}, …}}] }
//
// Money is a string at the currency's minor unit (price_month = unit price ×
// 730, rounded once); packages are ordered by price, features by their sort
// order; "included_from" names the cheapest package that includes the
// feature (for a level feature, the first package at a level above) and is
// omitted when none does; a TEASER feature publishes a not-offered cell as
// "teaser" with that hint. The STEP-UP RULE is computed here, never stored:
// for a package P with a next package N, gap_month = N − P, the bundled
// add-ons are the features optional (or a purchasable next level) on P and
// included on N, and the rule holds when their add-on prices on P sum to at
// least the gap. Writes are rating.manage.

type packagesDoc struct {
	Currency   string           `json:"currency"`
	PriceBook  string           `json:"price_book"`
	PricesAsOf string           `json:"prices_as_of"`
	Groups     []groupDoc       `json:"groups"`
	Floor      []floorItem      `json:"floor"`
	Packages   []packageDoc     `json:"packages"`
	Features   []packageFeature `json:"features"`
}

// iconDoc is an icon as the document names it (DESIGN.md §22.10): src is a
// PATH relative to the origin that serves the document — the console and the
// storefront resolve it against the document's URL — alt is the name of the
// thing it stands for, bg the tile colour behind it (features only, and only
// when set). Absent when nothing is set: never null, never "".
type iconDoc struct {
	Src string `json:"src"`
	Alt string `json:"alt"`
	BG  string `json:"bg,omitempty"`
}

// iconOf is the published icon of an item, or nil when it has none.
func iconOf(id, alt, bg string) *iconDoc {
	if id == "" {
		return nil
	}
	return &iconDoc{Src: store.IconSrc(id), Alt: alt, BG: bg}
}

// groupDoc is one group heading, with its icon when one is set.
type groupDoc struct {
	Key  string   `json:"key"`
	Name string   `json:"name"`
	Icon *iconDoc `json:"icon,omitempty"`
}

type floorItem struct {
	Key   string   `json:"key"`
	Name  string   `json:"name"`
	Blurb string   `json:"blurb,omitempty"`
	Icon  *iconDoc `json:"icon,omitempty"`
}

// packageShape is the vCPU and memory headline with the guaranteed floors
// under it, and the disk — the package settings when written, the catalog's
// constants otherwise. Numbers on the wire.
type packageShape struct {
	VCPU               *store.Decimal `json:"vcpu,omitempty"`
	MemoryGB           *store.Decimal `json:"memory_gb,omitempty"`
	VCPUGuaranteed     *store.Decimal `json:"vcpu_guaranteed,omitempty"`
	MemoryGBGuaranteed *store.Decimal `json:"memory_gb_guaranteed,omitempty"`
	DiskGB             *store.Decimal `json:"disk_gb,omitempty"`
}

// packageStepUp is the step-up rule of one package against the next.
type packageStepUp struct {
	NextSKU               string   `json:"next_sku"`
	NextName              string   `json:"next_name"`
	GapMonth              string   `json:"gap_month"`
	BundledAddonKeys      []string `json:"bundled_addon_keys"`
	BundledAddonsSumMonth string   `json:"bundled_addons_sum_month"`
	RuleHolds             bool     `json:"rule_holds"`
}

type packageDoc struct {
	SKU              string                   `json:"sku"`
	Name             string                   `json:"name"`
	Tagline          string                   `json:"tagline"`
	PriceMonth       string                   `json:"price_month"`
	Recommended      bool                     `json:"recommended"`
	AnnualMonthsFree int                      `json:"annual_months_free"`
	Shape            packageShape             `json:"shape"`
	StepUp           *packageStepUp           `json:"step_up,omitempty"`
	Includes         map[string]store.Decimal `json:"includes"`
	// Icon, Accent and Badge brand the column (DESIGN.md §22.10), each
	// omitted when unset.
	Icon   *iconDoc `json:"icon,omitempty"`
	Accent string   `json:"accent,omitempty"`
	Badge  string   `json:"badge,omitempty"`
	// Grow is the package's grow mode (DESIGN.md §22.11), omitted when the
	// package does not allow it.
	Grow *growDoc `json:"grow,omitempty"`
}

// growDoc is what grow mode means on one package: the ceiling the quota may
// be raised to and the rates the usage above the allowance is billed at —
// the package's own compute rates, the book's flat disk and bandwidth meter
// prices.
type growDoc struct {
	Allowed      bool              `json:"allowed"`
	Ceiling      store.GrowCeiling `json:"ceiling"`
	OverageRates []overageRateDoc  `json:"overage_rates"`
}

// overageRateDoc is one rate above the allowance, per unit per month at the
// currency's minor unit — the hourly unit price × 730, rounded once.
type overageRateDoc struct {
	Key        string `json:"key"`
	SKU        string `json:"sku"`
	Unit       string `json:"unit"`
	PriceMonth string `json:"price_month"`
}

type packageFeature struct {
	Key      string                 `json:"key"`
	Name     string                 `json:"name"`
	Blurb    string                 `json:"blurb,omitempty"`
	Group    string                 `json:"group"`
	Kind     string                 `json:"kind"`
	Unit     string                 `json:"unit,omitempty"`
	Levels   []string               `json:"levels,omitempty"`
	AddonSKU string                 `json:"addon_sku,omitempty"`
	Teaser   bool                   `json:"teaser"`
	Icon     *iconDoc               `json:"icon,omitempty"`
	Cells    map[string]packageCell `json:"cells"`
}

// nextLevelAddon is the purchasable next level of a level feature on one
// package: the add-on SKU and its price per month.
type nextLevelAddon struct {
	AddonSKU   string `json:"addon_sku"`
	PriceMonth string `json:"price_month,omitempty"`
}

type packageCell struct {
	State          string          `json:"state"`
	AddonSKU       string          `json:"addon_sku,omitempty"`
	PriceMonth     string          `json:"price_month,omitempty"`
	IncludedFrom   string          `json:"included_from,omitempty"`
	Quantity       *store.Decimal  `json:"quantity,omitempty"`
	Overage        string          `json:"overage,omitempty"`
	Level          *int            `json:"level,omitempty"`
	NextLevelAddon *nextLevelAddon `json:"next_level_addon,omitempty"`
	Note           string          `json:"note,omitempty"`
	// GrowOnly: the feature (or, on a level, the next level) is available
	// on this package only in grow mode, billed as usage — state optional,
	// no add-on, no price (DESIGN.md §22.11).
	GrowOnly bool `json:"grow_only,omitempty"`
}

// CellStateTeaser is the published state of a not-offered cell on a teaser
// feature: "available on <included_from>" rather than a dash.
const CellStateTeaser = "teaser"

// trimDec drops the trailing zeros of a decimal ("50.000000" → "50").
func trimDec(d store.Decimal) store.Decimal {
	s := strings.TrimSpace(string(d))
	if !strings.Contains(s, ".") {
		return store.Decimal(s)
	}
	s = strings.TrimRight(s, "0")
	return store.Decimal(strings.TrimSuffix(s, "."))
}

func trimDecPtr(d *store.Decimal) *store.Decimal {
	if d == nil || strings.TrimSpace(string(*d)) == "" {
		return nil
	}
	t := trimDec(*d)
	return &t
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

// packageShapeOf is a package's shape: the settings where written, the catalog's
// constants (PlanShape) for the headline otherwise.
func packageShapeOf(slug string, ps *store.PackageSettings) packageShape {
	var sh packageShape
	if v, m, ok := store.PlanShape(slug); ok {
		vd, md := store.Decimal(fmt.Sprint(v)), store.Decimal(fmt.Sprint(m))
		sh.VCPU, sh.MemoryGB = &vd, &md
	}
	if ps == nil {
		return sh
	}
	if d := trimDecPtr(ps.VCPU); d != nil {
		sh.VCPU = d
	}
	if d := trimDecPtr(ps.MemoryGB); d != nil {
		sh.MemoryGB = d
	}
	sh.VCPUGuaranteed = trimDecPtr(ps.VCPUGuaranteed)
	sh.MemoryGBGuaranteed = trimDecPtr(ps.MemoryGBGuaranteed)
	sh.DiskGB = trimDecPtr(ps.DiskGB)
	return sh
}

// packagesDocument builds the one document from a book, its matrix cells,
// its package settings, the features in matrix order and the icon of each
// group that has one. Only features with at least one cell in the book appear
// (floor items always do, as the floor); a plan with no cell for a feature
// reads not_offered.
func packagesDocument(pb store.PriceBook, features []store.Feature, cells []store.Entitlement, settings map[string]store.PackageSettings, groupIcons map[string]string) (packagesDoc, error) {
	digits := store.MinorUnitDigits(pb.Currency)
	doc := packagesDoc{Currency: pb.Currency, PriceBook: pb.Name, PricesAsOf: pb.UpdatedAt.UTC().Format("2006-01-02"), Groups: []groupDoc{}, Floor: []floorItem{}, Packages: []packageDoc{}, Features: []packageFeature{}}
	for _, g := range store.FeatureGroups {
		doc.Groups = append(doc.Groups, groupDoc{Key: g.Key, Name: g.Name, Icon: iconOf(groupIcons[g.Key], g.Name, "")})
	}
	items := map[string]store.PriceItem{}
	for _, it := range pb.Items {
		items[it.SKU] = it
	}
	monthly := func(sku string) (string, error) {
		it, priced := items[sku]
		if !priced {
			return "", nil
		}
		m, err := rating.MonthlyAt(it.UnitPrice, digits)
		if err != nil {
			return "", fmt.Errorf("%s: %w", sku, err)
		}
		return string(m), nil
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
	// The shape and the `includes` of each plan: the shape (settings, else
	// the catalog's constants) plus every quantity feature the package
	// includes.
	shapes := map[string]packageShape{}
	includes := map[string]map[string]store.Decimal{}
	for _, p := range plans {
		var ps *store.PackageSettings
		if s, ok := settings[p.sku]; ok {
			ps = &s
		}
		sh := packageShapeOf(p.slug, ps)
		shapes[p.sku] = sh
		inc := map[string]store.Decimal{}
		if sh.VCPU != nil {
			inc["vcpu"] = *sh.VCPU
		}
		if sh.MemoryGB != nil {
			inc["memory_gb"] = *sh.MemoryGB
		}
		includes[p.sku] = inc
	}
	// The published cells per plan, kept for the step-up rule below.
	published := map[string]map[string]packageCell{} // feature key → plan sku → cell
	kinds := map[string]store.Feature{}
	for _, f := range features {
		if f.Group == store.FeatureGroupFloor {
			doc.Floor = append(doc.Floor, floorItem{Key: f.Key, Name: f.Name, Blurb: f.Blurb, Icon: iconOf(f.IconID, f.Name, f.IconBG)})
			continue
		}
		byPlan := cellOf[f.ID]
		if len(byPlan) == 0 {
			continue
		}
		kinds[f.Key] = f
		// included_from: the cheapest package that includes the feature; for
		// a level feature, the first package at a level above the cell's.
		firstIncluded := ""
		for _, p := range plans {
			if c, ok := byPlan[p.sku]; ok && c.State == store.EntitlementIncluded {
				firstIncluded = p.sku
				break
			}
		}
		firstLevelAbove := func(level int) string {
			for _, p := range plans {
				if c, ok := byPlan[p.sku]; ok && c.State != store.EntitlementNotOffered && c.Level != nil && *c.Level > level {
					return p.sku
				}
			}
			return ""
		}
		pf := packageFeature{Key: f.Key, Name: f.Name, Blurb: f.Blurb, Group: f.Group, Kind: f.Kind, Unit: f.Unit, Levels: f.Levels, AddonSKU: f.AddonSKU, Teaser: f.Teaser, Icon: iconOf(f.IconID, f.Name, f.IconBG), Cells: map[string]packageCell{}}
		published[f.Key] = map[string]packageCell{}
		for _, p := range plans {
			cell := packageCell{State: store.EntitlementNotOffered}
			c, has := byPlan[p.sku]
			if has {
				cell.State = c.State
				cell.Note = c.Note
			}
			switch f.Kind {
			case store.FeatureKindQuantity:
				if has && c.State == store.EntitlementIncluded {
					if c.IncludedQuantity != nil {
						q := trimDec(*c.IncludedQuantity)
						cell.Quantity = &q
						includes[p.sku][includesKey(f)] = q
					}
					cell.Overage = c.Overage
					if cell.Overage == "" {
						cell.Overage = store.OverageMetered
					}
				}
			case store.FeatureKindLevel:
				if has && c.State != store.EntitlementNotOffered && c.Level != nil {
					lvl := *c.Level
					cell.Level = &lvl
					if c.GrowOnly {
						// The next level comes with grow mode: published
						// optional, grow-only, with no add-on and no price.
						cell.State, cell.GrowOnly = store.EntitlementOptional, true
						break
					}
					if c.State == store.EntitlementOptional && f.AddonSKU != "" {
						pm, err := monthly(f.AddonSKU)
						if err != nil {
							return doc, fmt.Errorf("add-on %w", err)
						}
						cell.NextLevelAddon = &nextLevelAddon{AddonSKU: f.AddonSKU, PriceMonth: pm}
					}
					// A level is always something the package has: the
					// published state is included, the next level the add-on.
					cell.State = store.EntitlementIncluded
				}
			default: // boolean, access
				if has && c.State == store.EntitlementOptional && c.GrowOnly {
					cell.GrowOnly = true
				} else if has && c.State == store.EntitlementOptional && f.AddonSKU != "" {
					cell.AddonSKU = f.AddonSKU
					pm, err := monthly(f.AddonSKU)
					if err != nil {
						return doc, fmt.Errorf("add-on %w", err)
					}
					cell.PriceMonth = pm
				}
			}
			// The hint, and the teaser.
			switch {
			case f.Kind == store.FeatureKindLevel && cell.Level != nil:
				cell.IncludedFrom = firstLevelAbove(*cell.Level)
			case f.Kind == store.FeatureKindLevel:
				cell.IncludedFrom = firstLevelAbove(-1)
			case cell.State != store.EntitlementIncluded:
				cell.IncludedFrom = firstIncluded
			}
			if cell.State == store.EntitlementNotOffered && f.Teaser && cell.IncludedFrom != "" {
				cell.State = CellStateTeaser
			}
			pf.Cells[p.sku] = cell
			published[f.Key][p.sku] = cell
		}
		doc.Features = append(doc.Features, pf)
	}
	// The packages, with their settings and the step-up rule against the next.
	for i, p := range plans {
		pd := packageDoc{SKU: p.sku, Name: store.PlanName(p.slug), PriceMonth: string(p.monthly), Shape: shapes[p.sku], Includes: includes[p.sku]}
		if s, ok := settings[p.sku]; ok {
			pd.Tagline, pd.Recommended, pd.AnnualMonthsFree = s.Tagline, s.Recommended, s.AnnualMonthsFree
			pd.Icon, pd.Accent, pd.Badge = iconOf(s.IconID, pd.Name, ""), s.Accent, s.Badge
			g, err := growDocOf(s, pb, items, digits)
			if err != nil {
				return doc, fmt.Errorf("package %s: %w", p.sku, err)
			}
			pd.Grow = g
		}
		if i+1 < len(plans) {
			n := plans[i+1]
			gap := new(big.Rat).Sub(ratOf(n.monthly), ratOf(p.monthly))
			sum := new(big.Rat)
			keys := []string{}
			for _, pf := range doc.Features {
				cp, cn := published[pf.Key][p.sku], published[pf.Key][n.sku]
				f := kinds[pf.Key]
				bundled, price := false, ""
				switch f.Kind {
				case store.FeatureKindBoolean:
					// A grow-only feature is not an add-on: nothing is
					// bundled by the step.
					bundled = cp.State == store.EntitlementOptional && !cp.GrowOnly && cn.State == store.EntitlementIncluded
					price = cp.PriceMonth
				case store.FeatureKindLevel:
					if cp.NextLevelAddon != nil && cp.Level != nil && cn.Level != nil && *cn.Level > *cp.Level {
						bundled, price = true, cp.NextLevelAddon.PriceMonth
					}
				}
				if !bundled {
					continue
				}
				keys = append(keys, pf.Key)
				if price != "" {
					sum.Add(sum, ratOf(store.Decimal(price)))
				}
			}
			// The rule: what the next package bundles is worth at least the
			// step. A step that bundles nothing makes no such claim, and
			// holds.
			pd.StepUp = &packageStepUp{
				NextSKU: n.sku, NextName: store.PlanName(n.slug),
				GapMonth:              gap.FloatString(digits),
				BundledAddonKeys:      keys,
				BundledAddonsSumMonth: sum.FloatString(digits),
				RuleHolds:             len(keys) == 0 || sum.Cmp(gap) >= 0,
			}
		}
		doc.Packages = append(doc.Packages, pd)
	}
	return doc, nil
}

// growDocOf is a package's grow block, nil when the package does not allow
// grow. The compute rates are the package's own (its settings, per unit per
// month, converted as every book item is: annual = × 12, unit through the
// book's divisor); disk and bandwidth are the book's flat meter prices. Each
// price_month is the unit price × 730 rounded once, so the storefront shows
// the figure the statement rates at.
func growDocOf(s store.PackageSettings, pb store.PriceBook, items map[string]store.PriceItem, digits int) (*growDoc, error) {
	if !s.GrowAllowed {
		return nil, nil
	}
	g := &growDoc{Allowed: true, OverageRates: []overageRateDoc{},
		Ceiling: store.GrowCeiling{VCPU: s.GrowCeilingVCPU, MemoryGB: s.GrowCeilingMemoryGB, DiskGB: s.GrowCeilingDiskGB, BandwidthMbps: s.GrowCeilingBandwidthMbps}}
	compute := func(key, sku, unit string, monthly *store.Decimal) error {
		if monthly == nil {
			return nil
		}
		annual, err := rating.Amount(*monthly, "12")
		if err != nil {
			return err
		}
		up, err := rating.UnitPrice(string(annual), pb.AnnualDivisor)
		if err != nil {
			return err
		}
		m, err := rating.MonthlyAt(up, digits)
		if err != nil {
			return err
		}
		g.OverageRates = append(g.OverageRates, overageRateDoc{Key: key, SKU: sku, Unit: unit, PriceMonth: string(m)})
		return nil
	}
	if err := compute("vcpu", store.SKUVCPU, "vCPU", s.OverageVCPUMonth); err != nil {
		return nil, err
	}
	if err := compute("memory", store.SKUMem, "GB", s.OverageMemGBMonth); err != nil {
		return nil, err
	}
	for _, m := range []struct{ key, sku, unit string }{{"disk", store.SKUPVC, "GB"}, {"bandwidth", store.SKUBandwidth, "Mbps"}} {
		it, ok := items[m.sku]
		if !ok {
			continue
		}
		pm, err := rating.MonthlyAt(it.UnitPrice, digits)
		if err != nil {
			return nil, err
		}
		g.OverageRates = append(g.OverageRates, overageRateDoc{Key: m.key, SKU: m.sku, Unit: m.unit, PriceMonth: string(pm)})
	}
	return g, nil
}

func ratOf(d store.Decimal) *big.Rat {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(string(d)))
	if !ok {
		return new(big.Rat)
	}
	return r
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
	settings, err := h.Store.PackageSettingsOf(r.Context(), pb.ID)
	if err != nil {
		return packagesDoc{}, err
	}
	groupIcons, err := h.Store.FeatureGroupIcons(r.Context())
	if err != nil {
		return packagesDoc{}, err
	}
	return packagesDocument(pb, features, cells, settings, groupIcons)
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
	Key       string    `json:"key"`
	Name      *string   `json:"name"`
	Blurb     *string   `json:"blurb"`
	Kind      *string   `json:"kind"`
	Group     *string   `json:"group"`
	Unit      *string   `json:"unit"`
	AddonSKU  *string   `json:"addon_sku"`
	Levels    *[]string `json:"levels"`
	Teaser    *bool     `json:"teaser"`
	SortOrder *int      `json:"sort_order"`
	// IconID ("" clears) and IconBG ("#RRGGBB", "" clears) — DESIGN.md §22.10.
	IconID *string `json:"icon_id"`
	IconBG *string `json:"icon_bg"`
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
	groups, err := h.featureGroups(r)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"features": list, "groups": groups})
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
	if in.Group != nil {
		fi.Group = *in.Group
	}
	if in.Unit != nil {
		fi.Unit = *in.Unit
	}
	if in.AddonSKU != nil {
		fi.AddonSKU = *in.AddonSKU
	}
	if in.Levels != nil {
		fi.Levels = *in.Levels
	}
	if in.Teaser != nil {
		fi.Teaser = *in.Teaser
	}
	if in.SortOrder != nil {
		fi.SortOrder = *in.SortOrder
	}
	if in.IconID != nil {
		fi.IconID = *in.IconID
	}
	if in.IconBG != nil {
		fi.IconBG = *in.IconBG
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
	h.audit(r, nil, "feature.create", map[string]any{"id": f.ID, "key": f.Key, "kind": f.Kind, "group": f.Group, "addon_sku": f.AddonSKU, "icon_id": f.IconID, "icon_bg": f.IconBG})
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
	p := store.FeaturePatch{Name: in.Name, Blurb: in.Blurb, Kind: in.Kind, Group: in.Group, Unit: in.Unit, AddonSKU: in.AddonSKU, Levels: in.Levels, Teaser: in.Teaser, SortOrder: in.SortOrder, IconID: in.IconID, IconBG: in.IconBG}
	if p.Name == nil && p.Blurb == nil && p.Kind == nil && p.Group == nil && p.Unit == nil && p.AddonSKU == nil && p.Levels == nil && p.Teaser == nil && p.SortOrder == nil && p.IconID == nil && p.IconBG == nil {
		writeErr(w, http.StatusBadRequest, "nothing to update: give name, blurb, kind, group, unit, addon_sku, levels, teaser, sort_order, icon_id or icon_bg")
		return
	}
	f, err := h.Store.UpdateFeature(r.Context(), r.PathValue("id"), p)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "feature.update", map[string]any{"id": f.ID, "key": f.Key, "kind": f.Kind, "group": f.Group, "addon_sku": f.AddonSKU, "icon_id": f.IconID, "icon_bg": f.IconBG})
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
// priced. It applies to a boolean add-on and to the next level of a level
// feature; a quantity feature's SKU is priced per unit on the Items tab.
type packageCellBody struct {
	State            string         `json:"state"`
	IncludedQuantity *store.Decimal `json:"included_quantity"`
	Overage          *string        `json:"overage"`
	Level            *int           `json:"level"`
	Note             *string        `json:"note"`
	AddonMonthly     *store.Decimal `json:"addon_monthly"`
	// GrowOnly marks an optional boolean or level cell as available on the
	// package only in grow mode, billed as usage (DESIGN.md §22.11). Left
	// out keeps the one that is there.
	GrowOnly *bool `json:"grow_only"`
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
		switch f.Kind {
		case store.FeatureKindQuantity:
			writeErr(w, http.StatusBadRequest, f.Key+" is a quantity feature; its SKU "+f.AddonSKU+" is priced per unit on the Items tab")
			return
		case store.FeatureKindAccess:
			writeErr(w, http.StatusBadRequest, f.Key+" is a platform door; it is never priced")
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
		if f.Kind == store.FeatureKindLevel {
			desc = fmt.Sprintf("%s — the next level: %s %s/month, billed per plan-hour like the plan it extends", f.Name, trimDec(*in.AddonMonthly), pb.Currency)
		}
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
	cur, curErr := h.Store.GetEntitlement(r.Context(), pb.ID, planSKU, f.ID)
	ei := store.EntitlementInput{State: in.State, IncludedQuantity: in.IncludedQuantity, Level: in.Level}
	if in.Overage != nil {
		ei.Overage = *in.Overage
	} else if curErr == nil {
		ei.Overage = cur.Overage
	}
	// A note, a quantity or a level left out keeps the one that is there.
	if in.Note != nil {
		ei.Note = *in.Note
	} else if curErr == nil {
		ei.Note = cur.Note
	}
	if in.IncludedQuantity == nil && curErr == nil {
		ei.IncludedQuantity = cur.IncludedQuantity
	}
	if in.Level == nil && curErr == nil {
		ei.Level = cur.Level
	}
	if in.GrowOnly != nil {
		ei.GrowOnly = *in.GrowOnly
	} else if curErr == nil {
		ei.GrowOnly = cur.GrowOnly
	}
	if in.GrowOnly == nil && !strings.EqualFold(strings.TrimSpace(in.State), store.EntitlementOptional) {
		// grow-only is a kind of optional: a cell moved to another state
		// without naming it drops it (one that names it is refused).
		ei.GrowOnly = false
	}
	cell, err := h.Store.PutEntitlement(r.Context(), pb.ID, planSKU, f.ID, ei)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "pricebook.package.put", map[string]any{"id": pb.ID, "plan": planSKU, "feature": f.Key, "state": cell.State, "included_quantity": cell.IncludedQuantity, "overage": cell.Overage, "level": cell.Level, "grow_only": cell.GrowOnly})
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
// the package settings
// ---------------------------------------------------------------------------

// packageSettingsBody is PUT /pricebooks/{id}/packages/{plan}/settings: the
// package's tagline, the recommended flag, the term rule and its shape,
// written whole (DESIGN.md §22.1).
type packageSettingsBody struct {
	Tagline            string         `json:"tagline"`
	Recommended        bool           `json:"recommended"`
	AnnualMonthsFree   int            `json:"annual_months_free"`
	VCPU               *store.Decimal `json:"vcpu"`
	MemoryGB           *store.Decimal `json:"memory_gb"`
	VCPUGuaranteed     *store.Decimal `json:"vcpu_guaranteed"`
	MemoryGBGuaranteed *store.Decimal `json:"memory_gb_guaranteed"`
	DiskGB             *store.Decimal `json:"disk_gb"`
	// The column's branding (DESIGN.md §22.10): an icon, a "#RRGGBB" accent,
	// a badge of at most 24 characters. Whole, like the rest: absent = none.
	IconID string `json:"icon_id"`
	Accent string `json:"accent"`
	Badge  string `json:"badge"`
	// Grow (DESIGN.md §22.11): whether a customer may choose grow mode, the
	// most it raises the quota to per dimension, and the package's compute
	// overage rates per unit per month. Whole, like the rest: when
	// grow_allowed, all six are required.
	GrowAllowed              bool           `json:"grow_allowed"`
	GrowCeilingVCPU          *store.Decimal `json:"grow_ceiling_vcpu"`
	GrowCeilingMemoryGB      *store.Decimal `json:"grow_ceiling_memory_gb"`
	GrowCeilingDiskGB        *store.Decimal `json:"grow_ceiling_disk_gb"`
	GrowCeilingBandwidthMbps *store.Decimal `json:"grow_ceiling_bandwidth_mbps"`
	OverageVCPUMonth         *store.Decimal `json:"overage_vcpu_month"`
	OverageMemGBMonth        *store.Decimal `json:"overage_mem_gb_month"`
}

func (h *Handler) putPackageSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.RatingManage); !ok {
		return
	}
	id, planSKU := r.PathValue("id"), r.PathValue("plan")
	var in packageSettingsBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if h.derivedBookWriteRefused(w, r, id) {
		return
	}
	ps, err := h.Store.PutPackageSettings(r.Context(), id, planSKU, store.PackageSettingsInput{
		Tagline: in.Tagline, Recommended: in.Recommended, AnnualMonthsFree: in.AnnualMonthsFree,
		VCPU: in.VCPU, MemoryGB: in.MemoryGB, VCPUGuaranteed: in.VCPUGuaranteed, MemoryGBGuaranteed: in.MemoryGBGuaranteed, DiskGB: in.DiskGB,
		IconID: in.IconID, Accent: in.Accent, Badge: in.Badge,
		GrowAllowed: in.GrowAllowed, GrowCeilingVCPU: in.GrowCeilingVCPU, GrowCeilingMemoryGB: in.GrowCeilingMemoryGB,
		GrowCeilingDiskGB: in.GrowCeilingDiskGB, GrowCeilingBandwidthMbps: in.GrowCeilingBandwidthMbps,
		OverageVCPUMonth: in.OverageVCPUMonth, OverageMemGBMonth: in.OverageMemGBMonth,
	})
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "pricebook.package.settings", map[string]any{"id": id, "plan": planSKU, "recommended": ps.Recommended, "annual_months_free": ps.AnnualMonthsFree, "vcpu": ps.VCPU, "memory_gb": ps.MemoryGB, "disk_gb": ps.DiskGB, "icon_id": ps.IconID, "accent": ps.Accent, "badge": ps.Badge,
		"grow_allowed": ps.GrowAllowed, "overage_vcpu_month": ps.OverageVCPUMonth, "overage_mem_gb_month": ps.OverageMemGBMonth})
	writeJSON(w, http.StatusOK, ps)
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

// putSourceOverage — PUT /api/v1/customers/{id}/sources/{sid}/overage
// {"overage_mode": "grow", "grow_ceiling": {"vcpu": "4", …}, "spend_limit_month":
// "25.000"}: the customer's choice at the package's allowance (DESIGN.md
// §22.11), written whole. customers.manage on the customer, the permission
// the add-ons route takes. The store refuses grow on a package that does not
// offer it, a ceiling outside [headline, the package's ceiling], and a
// ceiling or a spend limit in capped mode, naming the reason.
func (h *Handler) putSourceOverage(w http.ResponseWriter, r *http.Request) {
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
		OverageMode     string             `json:"overage_mode"`
		GrowCeiling     *store.GrowCeiling `json:"grow_ceiling"`
		SpendLimitMonth *store.Decimal     `json:"spend_limit_month"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if strings.TrimSpace(in.OverageMode) == "" {
		writeErr(w, http.StatusBadRequest, `body must name {"overage_mode": "capped" | "grow"}`)
		return
	}
	updated, err := h.Store.SetSourceOverage(r.Context(), src.ID, store.OverageInput{Mode: in.OverageMode, Ceiling: in.GrowCeiling, SpendLimitMonth: in.SpendLimitMonth})
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, &cid, "source.overage", map[string]any{"source_id": src.ID, "overage_mode": updated.OverageMode, "grow_ceiling": updated.GrowCeiling, "spend_limit_month": updated.SpendLimitMonth})
	writeJSON(w, http.StatusOK, struct {
		store.CostSource
		Collecting bool `json:"collecting"`
	}{updated, h.collectingFor(r, updated)})
}
