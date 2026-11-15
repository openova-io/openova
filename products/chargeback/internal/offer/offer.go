// Package offer holds the DEFAULT OFFER the billing engine ships with
// (DESIGN.md §22.12): the package ladder a fresh Sovereign publishes before a
// sovereign-admin has entered anything, and the icons it names.
//
// default-offer.json is data, not code. Its document is the offer exactly as
// GET /api/v1/public/packages published it on 20 October 2026; its
// price_items are the plans book's items of the same day. The store writes it
// once (store.EnsureDefaultOffer), and the store's built-in plan prices are
// read from it (store.PlanBookItems), so the engine has one plan price list.
//
// The package imports nothing of the engine: the store imports it.
package offer

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"sync"
)

// Files is default-offer.json and the icons/ directory: the SVGs the offer
// names, their LICENSES.md and the showcase manifest cmd/seed-history reads.
//
//go:embed default-offer.json icons
var Files embed.FS

// Icon is an icon as the offer names it: a file of icons/, the text that
// stands for it, the tile colour behind it (features and floor items only).
type Icon struct {
	File string `json:"file"`
	Alt  string `json:"alt"`
	BG   string `json:"bg,omitempty"`
}

// Group is one heading of the matrix and its icon.
type Group struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Icon *Icon  `json:"icon,omitempty"`
}

// FloorItem is a platform-wide item: on every package, never priced, no cell.
type FloorItem struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Blurb string `json:"blurb,omitempty"`
	Icon  *Icon  `json:"icon,omitempty"`
}

// Shape is a package's headline and its guaranteed floors.
type Shape struct {
	VCPU               json.Number `json:"vcpu,omitempty"`
	MemoryGB           json.Number `json:"memory_gb,omitempty"`
	VCPUGuaranteed     json.Number `json:"vcpu_guaranteed,omitempty"`
	MemoryGBGuaranteed json.Number `json:"memory_gb_guaranteed,omitempty"`
	DiskGB             json.Number `json:"disk_gb,omitempty"`
}

// Ceiling is the most grow mode raises a package's quota to.
type Ceiling struct {
	VCPU          json.Number `json:"vcpu,omitempty"`
	MemoryGB      json.Number `json:"memory_gb,omitempty"`
	DiskGB        json.Number `json:"disk_gb,omitempty"`
	BandwidthMbps json.Number `json:"bandwidth_mbps,omitempty"`
}

// OverageRate is one published rate above the allowance, per unit per month.
type OverageRate struct {
	Key        string `json:"key"`
	SKU        string `json:"sku"`
	Unit       string `json:"unit"`
	PriceMonth string `json:"price_month"`
}

// Grow is a package's grow mode.
type Grow struct {
	Allowed      bool          `json:"allowed"`
	Ceiling      Ceiling       `json:"ceiling"`
	OverageRates []OverageRate `json:"overage_rates"`
}

// StepUp is the published step-up rule of a package against the next. The
// engine computes it; the offer carries it so the test can compare.
type StepUp struct {
	NextSKU               string   `json:"next_sku"`
	NextName              string   `json:"next_name"`
	GapMonth              string   `json:"gap_month"`
	BundledAddonKeys      []string `json:"bundled_addon_keys"`
	BundledAddonsSumMonth string   `json:"bundled_addons_sum_month"`
	RuleHolds             bool     `json:"rule_holds"`
}

// Package is one package of the ladder.
type Package struct {
	SKU              string                 `json:"sku"`
	Name             string                 `json:"name"`
	Tagline          string                 `json:"tagline"`
	PriceMonth       string                 `json:"price_month"`
	Recommended      bool                   `json:"recommended"`
	AnnualMonthsFree int                    `json:"annual_months_free"`
	Shape            Shape                  `json:"shape"`
	StepUp           *StepUp                `json:"step_up,omitempty"`
	Includes         map[string]json.Number `json:"includes"`
	Icon             *Icon                  `json:"icon,omitempty"`
	Accent           string                 `json:"accent,omitempty"`
	Badge            string                 `json:"badge,omitempty"`
	Grow             *Grow                  `json:"grow,omitempty"`
}

// NextLevelAddon is the purchasable next level of a level cell.
type NextLevelAddon struct {
	AddonSKU   string `json:"addon_sku"`
	PriceMonth string `json:"price_month,omitempty"`
}

// Cell is one feature on one package, as the document publishes it.
type Cell struct {
	State          string          `json:"state"`
	AddonSKU       string          `json:"addon_sku,omitempty"`
	PriceMonth     string          `json:"price_month,omitempty"`
	IncludedFrom   string          `json:"included_from,omitempty"`
	Quantity       json.Number     `json:"quantity,omitempty"`
	Overage        string          `json:"overage,omitempty"`
	Level          *int            `json:"level,omitempty"`
	NextLevelAddon *NextLevelAddon `json:"next_level_addon,omitempty"`
	Note           string          `json:"note,omitempty"`
	GrowOnly       bool            `json:"grow_only,omitempty"`
}

// Feature is one row of the matrix with its cell per package SKU.
type Feature struct {
	Key      string          `json:"key"`
	Name     string          `json:"name"`
	Blurb    string          `json:"blurb,omitempty"`
	Group    string          `json:"group"`
	Kind     string          `json:"kind"`
	Unit     string          `json:"unit,omitempty"`
	Levels   []string        `json:"levels,omitempty"`
	AddonSKU string          `json:"addon_sku,omitempty"`
	Teaser   bool            `json:"teaser"`
	Icon     *Icon           `json:"icon,omitempty"`
	Cells    map[string]Cell `json:"cells"`
}

// Document is the published packages document without its date.
type Document struct {
	Currency  string      `json:"currency"`
	PriceBook string      `json:"price_book"`
	Groups    []Group     `json:"groups"`
	Floor     []FloorItem `json:"floor"`
	Packages  []Package   `json:"packages"`
	Features  []Feature   `json:"features"`
}

// PriceItem is one item of the plans book: SKU, unit, annual price and the
// description the book carries.
type PriceItem struct {
	SKU         string `json:"sku"`
	Unit        string `json:"unit"`
	AnnualPrice string `json:"annual_price"`
	Description string `json:"description"`
}

// Offer is default-offer.json.
type Offer struct {
	Comment    []string    `json:"comment"`
	PriceItems []PriceItem `json:"price_items"`
	Document   Document    `json:"document"`
}

// PlanPrefix is the SKU prefix of a package's plan item.
const PlanPrefix = "plan."

var (
	once     sync.Once
	defaultO Offer
	loadErr  error
)

// Default returns the offer the engine ships with, parsed and checked once.
func Default() (Offer, error) {
	once.Do(func() {
		b, err := Files.ReadFile("default-offer.json")
		if err != nil {
			loadErr = err
			return
		}
		defaultO, loadErr = Parse(b)
	})
	return defaultO, loadErr
}

// MustDefault is Default for package-level initialisation: the file is
// compiled into the binary, so a parse error is a defect of the build, never
// of a deployment, and TestDefaultOfferParses fails on it first.
func MustDefault() Offer {
	o, err := Default()
	if err != nil {
		panic("the default offer compiled into this binary does not parse: " + err.Error())
	}
	return o
}

// Parse reads an offer and checks what the writer relies on: every package
// is a priced plan item, every icon a file of icons/, every add-on and meter
// a cell names is priced.
func Parse(b []byte) (Offer, error) {
	var o Offer
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		return o, fmt.Errorf("default offer: %w", err)
	}
	priced := map[string]bool{}
	for _, it := range o.PriceItems {
		if it.SKU == "" || it.Unit == "" || it.AnnualPrice == "" {
			return o, fmt.Errorf("default offer: a price item needs a sku, a unit and an annual price: %+v", it)
		}
		if priced[it.SKU] {
			return o, fmt.Errorf("default offer: %s is priced twice", it.SKU)
		}
		priced[it.SKU] = true
	}
	if len(o.Document.Packages) == 0 {
		return o, fmt.Errorf("default offer: no package")
	}
	for _, p := range o.Document.Packages {
		if !strings.HasPrefix(p.SKU, PlanPrefix) || !priced[p.SKU] {
			return o, fmt.Errorf("default offer: package %s is not a priced plan item", p.SKU)
		}
	}
	for _, ic := range o.icons() {
		if _, err := fs.Stat(Files, "icons/"+ic.File); err != nil {
			return o, fmt.Errorf("default offer: icon %q: %w", ic.File, err)
		}
	}
	for _, f := range o.Document.Features {
		for sku, c := range f.Cells {
			if c.AddonSKU != "" && !priced[c.AddonSKU] {
				return o, fmt.Errorf("default offer: cell %s × %s names add-on %s, which is not priced", sku, f.Key, c.AddonSKU)
			}
		}
		if f.AddonSKU != "" && !priced[f.AddonSKU] {
			return o, fmt.Errorf("default offer: feature %s is priced or metered at %s, which is not priced", f.Key, f.AddonSKU)
		}
	}
	return o, nil
}

// icons lists every icon the document names.
func (o Offer) icons() []Icon {
	var out []Icon
	add := func(ic *Icon) {
		if ic != nil {
			out = append(out, *ic)
		}
	}
	for _, g := range o.Document.Groups {
		add(g.Icon)
	}
	for _, f := range o.Document.Floor {
		add(f.Icon)
	}
	for _, p := range o.Document.Packages {
		add(p.Icon)
	}
	for _, f := range o.Document.Features {
		add(f.Icon)
	}
	return out
}

// IconFiles lists the icon files the offer names, once each, in first-use
// order.
func (o Offer) IconFiles() []string {
	seen := map[string]bool{}
	var out []string
	for _, ic := range o.icons() {
		if !seen[ic.File] {
			seen[ic.File] = true
			out = append(out, ic.File)
		}
	}
	return out
}

// IconBytes returns the bytes of one icon file.
func IconBytes(file string) ([]byte, error) {
	return Files.ReadFile("icons/" + file)
}

// PlanItems are the offer's plan.<slug> price items, in the order the file
// lists them.
func (o Offer) PlanItems() []PriceItem {
	var out []PriceItem
	for _, it := range o.PriceItems {
		if strings.HasPrefix(it.SKU, PlanPrefix) {
			out = append(out, it)
		}
	}
	return out
}

// PackageOf returns the package with the given SKU.
func (o Offer) PackageOf(sku string) (Package, bool) {
	for _, p := range o.Document.Packages {
		if p.SKU == sku {
			return p, true
		}
	}
	return Package{}, false
}
