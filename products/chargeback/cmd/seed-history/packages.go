package main

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/synth"
)

// The showcase PACKAGE LADDER (DESIGN.md §7, §22.7). The seeder writes the
// features of the ladder and every cell onto the plans book, prices the
// add-ons and the two meters in it, writes each package's settings (shape,
// recommended), converges the four plan prices to the pricing workbook's,
// and gives Nizwa Fintech's platform Source the backup add-on — all through
// the product's own API, as the operator, so every rule (an optional feature
// needs a priced add-on, a metered overage needs a priced meter, a level
// indexes into its levels, a floor item has no cell) is the product's rather
// than this tool's.
//
// Everything is matched before it is written: a feature by key, a cell by
// (plan, feature), a price by SKU, the settings by plan, the add-ons as a
// set. The API audits every write, so re-sending what is already there would
// stack an audit entry per run — the decommission-note defect of apply.go, in
// a new place.
//
// The one thing this command re-prices: the four plan items of the plans
// book, to the sheet's Target column. The product creates the book at the
// catalog's constants (S 5 · M 9 · L 16 · XL 30); the approved ladder is
// priced at 2.490 · 4.490 · 7.990 · 13.990, and the add-ons derived from the
// step-up rule hold only against those gaps. Written only when a price
// differs, logged when it is, and NOT restored by --purge: the plans are the
// product's.

// apiFeature is a feature as GET /features returns it.
type apiFeature struct {
	ID        string   `json:"id"`
	Key       string   `json:"key"`
	Name      string   `json:"name"`
	Blurb     string   `json:"blurb"`
	Kind      string   `json:"kind"`
	Group     string   `json:"group"`
	Unit      string   `json:"unit"`
	AddonSKU  string   `json:"addon_sku"`
	Levels    []string `json:"levels"`
	Teaser    bool     `json:"teaser"`
	SortOrder int      `json:"sort_order"`
}

// apiPackages is the packages document (GET /pricebooks/{id}/packages) — the
// cells and settings this command compares a re-run against.
type apiPackages struct {
	Packages []struct {
		SKU              string `json:"sku"`
		Tagline          string `json:"tagline"`
		PriceMonth       string `json:"price_month"`
		Recommended      bool   `json:"recommended"`
		AnnualMonthsFree int    `json:"annual_months_free"`
		Shape            struct {
			VCPU               *json_number `json:"vcpu"`
			MemoryGB           *json_number `json:"memory_gb"`
			VCPUGuaranteed     *json_number `json:"vcpu_guaranteed"`
			MemoryGBGuaranteed *json_number `json:"memory_gb_guaranteed"`
			DiskGB             *json_number `json:"disk_gb"`
		} `json:"shape"`
		StepUp *struct {
			NextSKU   string `json:"next_sku"`
			GapMonth  string `json:"gap_month"`
			Sum       string `json:"bundled_addons_sum_month"`
			RuleHolds bool   `json:"rule_holds"`
		} `json:"step_up"`
	} `json:"packages"`
	Features []struct {
		Key   string `json:"key"`
		Cells map[string]struct {
			State          string       `json:"state"`
			Quantity       *json_number `json:"quantity"`
			Overage        string       `json:"overage"`
			Level          *int         `json:"level"`
			Note           string       `json:"note"`
			NextLevelAddon *struct {
				AddonSKU string `json:"addon_sku"`
			} `json:"next_level_addon"`
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

func (n *json_number) text() string {
	if n == nil {
		return ""
	}
	return string(*n)
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

func (c *client) deletePackageCell(bookID, planSKU, featureKey string) error {
	return c.do("DELETE", "/api/v1/pricebooks/"+url.PathEscape(bookID)+"/packages/"+url.PathEscape(planSKU)+"/features/"+url.PathEscape(featureKey), nil, nil)
}

func (c *client) putPackageSettings(bookID, planSKU string, body map[string]any) error {
	return c.do("PUT", "/api/v1/pricebooks/"+url.PathEscape(bookID)+"/packages/"+url.PathEscape(planSKU)+"/settings", body, nil)
}

func (c *client) patchPriceItem(bookID, sku string, body map[string]any) error {
	return c.do("PATCH", "/api/v1/pricebooks/"+url.PathEscape(bookID)+"/items/"+url.PathEscape(sku), body, nil)
}

func (c *client) putSourceAddons(customerID, sourceID string, keys []string) error {
	return c.do("PUT", "/api/v1/customers/"+url.PathEscape(customerID)+"/sources/"+url.PathEscape(sourceID)+"/addons", map[string]any{"addons": keys}, nil)
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// ensurePackages converges the plans book on the ladder: the add-on prices
// and the two meters (merged into the book, never re-pricing an item the
// operator set — putPriceItems is merge=true and skips a SKU already
// priced), the four plan prices, the features, each package's settings and
// every cell.
func (s *seeder) ensurePackages(planBookID string) error {
	if planBookID == "" {
		return nil
	}
	// 1. The add-on SKUs and the meters, priced in the plans book — only
	//    where the book does not price them yet.
	var book struct {
		Items []struct {
			SKU         string       `json:"sku"`
			AnnualPrice *json_number `json:"annual_price"`
		} `json:"items"`
	}
	if err := s.api.do("GET", "/api/v1/pricebooks/"+url.PathEscape(planBookID), nil, &book); err != nil {
		return fmt.Errorf("read the plans book: %w", err)
	}
	priced := map[string]string{}
	for _, it := range book.Items {
		priced[it.SKU] = it.AnnualPrice.text()
	}
	var items []apiPriceItem
	for _, a := range synth.AddonRates {
		if _, ok := priced[a.SKU]; ok {
			continue
		}
		desc := fmt.Sprintf("%s add-on: %s OMR/month, billed per plan-hour like the plan it extends", a.Name, ftoa(math.Round(a.AnnualPrice()/12*1000)/1000))
		if a.Annual > 0 {
			desc = fmt.Sprintf("%s add-on: %s OMR/year (the rate card's Elastic IP list price, discount 0), billed per plan-hour like the plan it extends", a.Name, ftoa(a.Annual))
		}
		items = append(items, apiPriceItem{SKU: a.SKU, Unit: "plan-hour", AnnualPrice: ftoa(a.AnnualPrice()), Description: desc})
	}
	for _, m := range synth.MeterRates {
		if _, ok := priced[m.SKU]; ok {
			continue
		}
		items = append(items, apiPriceItem{SKU: m.SKU, Unit: m.Unit, AnnualPrice: ftoa(m.Annual), Description: m.Description})
	}
	if len(items) > 0 {
		if err := s.api.putPriceItems(planBookID, items); err != nil {
			return fmt.Errorf("price the add-ons in the plans book: %w", err)
		}
		s.infof("packages: %d add-on and meter rate(s) priced in the plans book", len(items))
	} else {
		s.infof("packages: every add-on and meter rate already priced in the plans book; left untouched")
	}

	// 2. The four plan prices, converged to the sheet's Target column — the
	//    one re-pricing this command does, and only where a price differs.
	repriced := 0
	for _, p := range synth.Packages {
		sku := "plan." + p.Slug
		annual := math.Round(p.Monthly*12*1e6) / 1e6
		have, ok := priced[sku]
		if !ok {
			return fmt.Errorf("the plans book prices no %s; the product's EnsurePlanBook creates the four plans", sku)
		}
		if numEqText(have, ftoa(annual)) {
			continue
		}
		if err := s.api.patchPriceItem(planBookID, sku, map[string]any{"annual_price": ftoa(annual)}); err != nil {
			return fmt.Errorf("price %s at the sheet's %s OMR/month: %w", sku, ftoa(p.Monthly), err)
		}
		s.infof("packages: %s re-priced %s → %s OMR/year (the workbook's %s OMR/month)", sku, have, ftoa(annual), ftoa(p.Monthly))
		repriced++
	}
	if repriced == 0 {
		s.infof("packages: the four plan prices already at the workbook's; left untouched")
	}

	// 3. The features, by key. A feature whose kind changes, or that becomes
	//    a floor item, first loses its cells on the plans book — the product
	//    refuses either while a package carries it.
	doc, err := s.api.getPackages(planBookID)
	if err != nil {
		return fmt.Errorf("read the matrix: %w", err)
	}
	type haveCell struct {
		state, qty, overage, note string
		level                     *int
		nextLevel                 bool
	}
	have := map[string]map[string]haveCell{}
	for _, f := range doc.Features {
		have[f.Key] = map[string]haveCell{}
		for plan, c := range f.Cells {
			state := c.State
			if state == "teaser" {
				// A teaser is a not-offered cell published with its hint.
				state = synth.NotOffered
			}
			have[f.Key][plan] = haveCell{state: state, qty: c.Quantity.text(), overage: c.Overage, note: c.Note, level: c.Level, nextLevel: c.NextLevelAddon != nil}
		}
	}
	// The rows the book really carries: the document reads "not offered"
	// for a missing row and for a not_offered row alike, and the ladder
	// writes every row so the matrix is complete.
	rowsOf, err := s.cellRows(planBookID)
	if err != nil {
		return err
	}
	existing, err := s.api.listFeatures()
	if err != nil {
		return fmt.Errorf("list features: %w", err)
	}
	byKey := map[string]apiFeature{}
	for _, f := range existing {
		byKey[f.Key] = f
	}
	created, converged, cleared := 0, 0, 0
	for i, f := range synth.Features {
		levels := f.Levels
		if levels == nil {
			levels = []string{}
		}
		want := map[string]any{"name": f.Name, "blurb": f.Blurb, "kind": f.Kind, "group": f.Group, "unit": f.Unit, "addon_sku": f.AddonSKU, "levels": levels, "teaser": f.Teaser, "sort_order": i + 1}
		cur, ok := byKey[f.Key]
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
		if cur.Kind != f.Kind || (f.Group == synth.GroupFloor && cur.Group != synth.GroupFloor) {
			for plan := range rowsOf[f.Key] {
				if err := s.api.deletePackageCell(planBookID, plan, f.Key); err != nil && !isNotFound(err) {
					return fmt.Errorf("clear cell %s × %s before changing the feature: %w", plan, f.Key, err)
				}
				cleared++
			}
			delete(have, f.Key)
			delete(rowsOf, f.Key)
		}
		if cur.Name != f.Name || cur.Blurb != f.Blurb || cur.Kind != f.Kind || cur.Group != f.Group || cur.Unit != f.Unit || cur.AddonSKU != f.AddonSKU || !sameStrings(cur.Levels, f.Levels) || cur.Teaser != f.Teaser || cur.SortOrder != i+1 {
			if _, err := s.api.patchFeature(cur.ID, want); err != nil {
				return fmt.Errorf("converge feature %q: %w", f.Key, err)
			}
			converged++
		}
	}
	s.infof("packages: %d feature(s) created, %d brought back to the ladder, %d already as the ladder has them; %d cell(s) cleared for a kind or floor change", created, converged, len(synth.Features)-created-converged, cleared)

	// 4. Each package's settings: the shape and the recommended flag, written
	//    whole when anything differs.
	settingsWritten := 0
	for _, p := range synth.Packages {
		sku := "plan." + p.Slug
		same := false
		for _, dp := range doc.Packages {
			if dp.SKU != sku {
				continue
			}
			same = dp.Tagline == p.Tagline && dp.Recommended == p.Recommended && dp.AnnualMonthsFree == p.AnnualMonthsFree &&
				numEqText(dp.Shape.VCPU.text(), ftoa(p.VCPU)) && numEqText(dp.Shape.MemoryGB.text(), ftoa(p.MemoryGB)) &&
				numEqText(dp.Shape.VCPUGuaranteed.text(), ftoa(p.VCPUGuaranteed)) && numEqText(dp.Shape.MemoryGBGuaranteed.text(), ftoa(p.MemoryGBGuaranteed)) &&
				numEqText(dp.Shape.DiskGB.text(), ftoa(p.DiskGB))
		}
		if same {
			continue
		}
		body := map[string]any{"tagline": p.Tagline, "recommended": p.Recommended, "annual_months_free": p.AnnualMonthsFree,
			"vcpu": ftoa(p.VCPU), "memory_gb": ftoa(p.MemoryGB), "vcpu_guaranteed": ftoa(p.VCPUGuaranteed), "memory_gb_guaranteed": ftoa(p.MemoryGBGuaranteed), "disk_gb": ftoa(p.DiskGB)}
		if err := s.api.putPackageSettings(planBookID, sku, body); err != nil {
			return fmt.Errorf("settings of %s: %w", sku, err)
		}
		settingsWritten++
	}
	s.infof("packages: %d package setting(s) written, the rest already as the ladder has them", settingsWritten)

	// 5. The cells, each written only when it differs. A floor item has none.
	written := 0
	for _, f := range synth.Features {
		if f.Group == synth.GroupFloor {
			continue
		}
		for _, p := range synth.PlanSlugs {
			want, ok := f.Cells[p]
			if !ok {
				continue
			}
			planSKU := "plan." + p
			wantQty, wantOverage := "", ""
			if f.Kind == synth.FeatureQuantity && want.HasQuantity {
				wantQty, wantOverage = ftoa(want.Quantity), want.Overage
			}
			cur, has := have[f.Key][planSKU]
			// A level cell is published as included with its level (optional
			// = the next level purchasable); compare on what the document says.
			wantState := want.State
			if f.Kind == synth.FeatureLevel && want.State == synth.Optional {
				wantState = synth.Included
			}
			same := has && rowsOf[f.Key][planSKU] && cur.state == wantState && numEqText(cur.qty, wantQty) && cur.overage == wantOverage && cur.note == want.Note
			if f.Kind == synth.FeatureLevel && want.State != synth.NotOffered {
				same = same && cur.level != nil && *cur.level == want.Level && cur.nextLevel == (want.State == synth.Optional)
			}
			if same {
				continue
			}
			body := map[string]any{"state": want.State, "note": want.Note}
			if wantQty != "" {
				body["included_quantity"] = wantQty
				body["overage"] = wantOverage
			}
			if f.Kind == synth.FeatureLevel {
				body["level"] = want.Level
			}
			if err := s.api.putPackageCell(planBookID, planSKU, f.Key, body); err != nil {
				return fmt.Errorf("cell %s × %s: %w", planSKU, f.Key, err)
			}
			written++
		}
	}
	s.infof("packages: %d cell(s) written, the rest already as the ladder has them", written)
	return nil
}

// cellRows reads which (feature key, plan) rows the plans book really
// carries, in one query.
func (s *seeder) cellRows(planBookID string) (map[string]map[string]bool, error) {
	rows, err := s.db.QueryContext(s.ctx, `SELECT f.key, e.plan_sku FROM package_entitlements e JOIN features f ON f.id = e.feature_id WHERE e.price_book_id = $1`, planBookID)
	if err != nil {
		return nil, fmt.Errorf("read the matrix rows: %w", err)
	}
	defer rows.Close()
	out := map[string]map[string]bool{}
	for rows.Next() {
		var key, plan string
		if err := rows.Scan(&key, &plan); err != nil {
			return nil, err
		}
		if out[key] == nil {
			out[key] = map[string]bool{}
		}
		out[key][plan] = true
	}
	return out, rows.Err()
}

func isNotFound(err error) bool {
	var es *errStatus
	return errors.As(err, &es) && es.Code == 404
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// numEqText compares two decimals as text numbers ("" = none).
func numEqText(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	x, err1 := strconv.ParseFloat(a, 64)
	y, err2 := strconv.ParseFloat(b, 64)
	return err1 == nil && err2 == nil && math.Abs(x-y) < 1e-9
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
