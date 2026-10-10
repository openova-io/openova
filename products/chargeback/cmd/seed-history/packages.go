package main

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/synth"
)

// The showcase PACKAGE MATRIX (DESIGN.md §7, §22). The seeder writes the
// fifteen features and the baseline matrix onto the plans book, prices the
// five add-ons and the bandwidth meter in it, and gives Nizwa Fintech's
// platform Source the backup add-on — all through the product's own API, as
// the operator, so every rule (an optional feature needs a priced add-on, a
// quantity feature needs its quantity, an included feature cannot be taken as
// an add-on) is the product's rather than this tool's.
//
// Everything is matched before it is written: a feature by key, a cell by
// (plan, feature), a price by SKU, the add-ons as a set. The API audits every
// write, so re-sending what is already there would stack an audit entry per
// run — the decommission-note defect of apply.go, in a new place.

// apiFeature is a feature as GET /features returns it.
type apiFeature struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	Name      string `json:"name"`
	Blurb     string `json:"blurb"`
	Kind      string `json:"kind"`
	Unit      string `json:"unit"`
	AddonSKU  string `json:"addon_sku"`
	SortOrder int    `json:"sort_order"`
}

// apiPackages is the packages document (GET /pricebooks/{id}/packages) — the
// cells this command compares a re-run against.
type apiPackages struct {
	Features []struct {
		Key   string `json:"key"`
		Cells map[string]struct {
			State    string       `json:"state"`
			Quantity *json_number `json:"quantity"`
		} `json:"cells"`
	} `json:"features"`
}

// json_number is a decimal on the wire read as text, so 50 and 50.000000
// compare as numbers.
type json_number string

func (n *json_number) UnmarshalJSON(b []byte) error {
	*n = json_number(strings.Trim(string(b), `"`))
	return nil
}

func (c *client) listFeatures() ([]apiFeature, error) {
	var out struct {
		Features []apiFeature `json:"features"`
	}
	err := c.do("GET", "/api/v1/features", nil, &out)
	return out.Features, err
}

func (c *client) createFeature(body map[string]any) (apiFeature, error) {
	var out apiFeature
	err := c.do("POST", "/api/v1/features", body, &out)
	return out, err
}

func (c *client) patchFeature(id string, body map[string]any) (apiFeature, error) {
	var out apiFeature
	err := c.do("PATCH", "/api/v1/features/"+url.PathEscape(id), body, &out)
	return out, err
}

func (c *client) getPackages(bookID string) (apiPackages, error) {
	var out apiPackages
	err := c.do("GET", "/api/v1/pricebooks/"+url.PathEscape(bookID)+"/packages", nil, &out)
	return out, err
}

func (c *client) putPackageCell(bookID, planSKU, featureKey string, body map[string]any) error {
	return c.do("PUT", "/api/v1/pricebooks/"+url.PathEscape(bookID)+"/packages/"+url.PathEscape(planSKU)+"/features/"+url.PathEscape(featureKey), body, nil)
}

func (c *client) putSourceAddons(customerID, sourceID string, keys []string) error {
	return c.do("PUT", "/api/v1/customers/"+url.PathEscape(customerID)+"/sources/"+url.PathEscape(sourceID)+"/addons", map[string]any{"addons": keys}, nil)
}

// ensurePackages converges the plans book on the baseline matrix: the add-on
// prices (merged into the book, never re-pricing an item the operator set —
// putPriceItems is merge=true and skips a SKU already priced), the fifteen
// features and every cell.
func (s *seeder) ensurePackages(planBookID string) error {
	if planBookID == "" {
		return nil
	}
	// 1. The add-on SKUs and the bandwidth meter, priced in the plans book —
	//    only where the book does not price them yet.
	var book struct {
		Items []struct {
			SKU string `json:"sku"`
		} `json:"items"`
	}
	if err := s.api.do("GET", "/api/v1/pricebooks/"+url.PathEscape(planBookID), nil, &book); err != nil {
		return fmt.Errorf("read the plans book: %w", err)
	}
	priced := map[string]bool{}
	for _, it := range book.Items {
		priced[it.SKU] = true
	}
	var items []apiPriceItem
	for _, a := range synth.AddonRates {
		if priced[a.SKU] {
			continue
		}
		items = append(items, apiPriceItem{
			SKU: a.SKU, Unit: "plan-hour",
			AnnualPrice: strconv.FormatFloat(a.Monthly*12, 'f', -1, 64),
			Description: fmt.Sprintf("%s add-on: %s OMR/month, billed per plan-hour like the plan it extends", a.Name, strconv.FormatFloat(a.Monthly, 'f', -1, 64)),
		})
	}
	if !priced[synth.BandwidthRate.SKU] {
		items = append(items, apiPriceItem{SKU: synth.BandwidthRate.SKU, Unit: synth.BandwidthRate.Unit, AnnualPrice: strconv.FormatFloat(synth.BandwidthRate.Annual, 'f', -1, 64), Description: synth.BandwidthRate.Description})
	}
	if len(items) > 0 {
		if err := s.api.putPriceItems(planBookID, items); err != nil {
			return fmt.Errorf("price the add-ons in the plans book: %w", err)
		}
		s.infof("packages: %d add-on rate(s) priced in the plans book", len(items))
	} else {
		s.infof("packages: every add-on rate already priced in the plans book; left untouched")
	}

	// 2. The features, by key.
	existing, err := s.api.listFeatures()
	if err != nil {
		return fmt.Errorf("list features: %w", err)
	}
	byKey := map[string]apiFeature{}
	for _, f := range existing {
		byKey[f.Key] = f
	}
	created, converged := 0, 0
	for i, f := range synth.Features {
		want := map[string]any{"name": f.Name, "blurb": f.Blurb, "kind": f.Kind, "unit": f.Unit, "addon_sku": f.AddonSKU, "sort_order": i + 1}
		have, ok := byKey[f.Key]
		if !ok {
			body := map[string]any{"key": f.Key}
			for k, v := range want {
				body[k] = v
			}
			if _, err := s.api.createFeature(body); err != nil {
				return fmt.Errorf("create feature %q: %w", f.Key, err)
			}
			created++
			continue
		}
		if have.Name != f.Name || have.Blurb != f.Blurb || have.Kind != f.Kind || have.Unit != f.Unit || have.AddonSKU != f.AddonSKU || have.SortOrder != i+1 {
			if _, err := s.api.patchFeature(have.ID, want); err != nil {
				return fmt.Errorf("converge feature %q: %w", f.Key, err)
			}
			converged++
		}
	}
	s.infof("packages: %d feature(s) created, %d brought back to the matrix, %d already as the matrix has them", created, converged, len(synth.Features)-created-converged)

	// 3. The cells, each written only when it differs.
	doc, err := s.api.getPackages(planBookID)
	if err != nil {
		return fmt.Errorf("read the matrix: %w", err)
	}
	have := map[string]map[string]struct{ state, qty string }{}
	for _, f := range doc.Features {
		have[f.Key] = map[string]struct{ state, qty string }{}
		for plan, c := range f.Cells {
			q := ""
			if c.Quantity != nil {
				q = string(*c.Quantity)
			}
			have[f.Key][plan] = struct{ state, qty string }{c.State, q}
		}
	}
	plans := []string{"s", "m", "l", "xl"}
	written := 0
	for _, f := range synth.Features {
		for _, p := range plans {
			state, ok := f.Cells[p]
			if !ok {
				continue
			}
			planSKU := "plan." + p
			wantQty := ""
			if f.Kind == synth.FeatureQuantity {
				if q, ok := f.Quantities[p]; ok {
					wantQty = strconv.FormatFloat(q, 'f', -1, 64)
				}
			}
			cur := have[f.Key][planSKU]
			if cur.state == state && numEqText(cur.qty, wantQty) {
				continue
			}
			body := map[string]any{"state": state}
			if wantQty != "" {
				body["included_quantity"] = wantQty
			}
			if err := s.api.putPackageCell(planBookID, planSKU, f.Key, body); err != nil {
				return fmt.Errorf("cell %s × %s: %w", planSKU, f.Key, err)
			}
			written++
		}
	}
	s.infof("packages: %d cell(s) written, the rest already as the matrix has them", written)
	return nil
}

// numEqText compares two decimals as text numbers ("" = none).
func numEqText(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	x, err1 := strconv.ParseFloat(a, 64)
	y, err2 := strconv.ParseFloat(b, 64)
	return err1 == nil && err2 == nil && x == y
}

// ensureAddons gives the customer's Source the add-ons the scenario names,
// written only when the set differs from what the Source already has.
func (s *seeder) ensureAddons(customerID, sourceID string, c *synth.Customer) error {
	if c.Source.Layer != synth.LayerPlatform {
		return nil
	}
	var src struct {
		Addons []string `json:"addons"`
	}
	if err := s.api.do("GET", "/api/v1/customers/"+url.PathEscape(customerID)+"/sources/"+url.PathEscape(sourceID), nil, &src); err != nil {
		return fmt.Errorf("read source %s: %w", sourceID, err)
	}
	want := append([]string(nil), c.Source.Addons...)
	have := append([]string(nil), src.Addons...)
	sort.Strings(want)
	sort.Strings(have)
	if strings.Join(want, ",") == strings.Join(have, ",") {
		if len(want) > 0 {
			s.infof("  add-ons %v already taken; left untouched", want)
		}
		return nil
	}
	if want == nil {
		want = []string{}
	}
	if err := s.api.putSourceAddons(customerID, sourceID, want); err != nil {
		return fmt.Errorf("add-ons of %s: %w", c.Slug, err)
	}
	s.infof("  add-ons set to %v", want)
	return nil
}
