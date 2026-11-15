package store

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/offer"
)

// Plan revenue (DESIGN.md §2.8). What an Organization actually pays is its
// catalog plan, a monthly bundle. The platform collector meters it as one
// `plan.<slug>` record per hour (unit plan-hour, quantity 1 for a full hour)
// on the Organization's openova-org source, and the "OpenOva plans" rate
// card below prices it. The k8s.vcpu / k8s.mem_gb / k8s.pvc_gb meters stay
// UNPRICED in that book: they are the allocation basis (§2.8), and the plan
// bundles are not identifiable per resource (M = 2×S, L = 4×S, XL = 8×S), so
// any per-vCPU rate would be invented.
const (
	// PlanBookName is the rate card OrgSync creates on a Sovereign when it
	// is absent. Looked up by name (case-insensitive), never re-created or
	// re-priced once it exists — the operator may edit it.
	PlanBookName = "OpenOva plans"
	// PlanBookDivisor is hours per year: unit_price = annual_price / 8760,
	// which for a monthly price m is m×12/8760 = m/730 per plan-hour.
	PlanBookDivisor = 8760
	// PlanKind is the usage_records.resource_kind of a plan line.
	PlanKind = "plan"
	// PlanUnit is the unit of a plan line.
	PlanUnit = "plan-hour"
	// PlanSKUPrefix prefixes the plan slug: plan.s, plan.m, plan.l, plan.xl.
	PlanSKUPrefix = "plan."
	// PlanFlexi is pay per use: no plan line, no plan price.
	PlanFlexi = "flexi"
)

// catalogPlan is one catalog plan as the Sovereign sells it: its slug and
// display name. Its PRICE is not here. The four sized plans are priced by the
// engine's default offer (internal/offer/default-offer.json, DESIGN.md
// §22.12), the one plan price list the engine has, and PlanBookItems reads
// them from it. The catalog service seeds the same four figures
// (core/services/catalog/handlers/seed.go seedPlanRows: 2490 / 4490 / 7990 /
// 13990 baisa); the chargeback module keeps the D5 invariant of importing
// nothing from Catalyst, so TestPlanBookItemsAreTheDefaultOffer holds the
// file to those figures instead. The org-controller's planQuotaTable
// (core/controllers/organization/internal/gitops/manifests.go) sizes the
// same rows: S = 1 vCPU / 2 GB, M = 2/4, L = 4/8, XL = 8/16.
type catalogPlan struct {
	Slug string
	Name string
}

var catalogPlans = []catalogPlan{
	{Slug: "s", Name: "S"},
	{Slug: "m", Name: "M"},
	{Slug: "l", Name: "L"},
	{Slug: "xl", Name: "XL"},
	{Slug: PlanFlexi, Name: "Flexi"},
}

// ValidPlanSlug reports whether slug names a catalog plan (s, m, l, xl,
// flexi). The empty string is "no plan", which is valid on a customer but
// is not a plan.
func ValidPlanSlug(slug string) bool {
	for _, p := range catalogPlans {
		if p.Slug == slug {
			return true
		}
	}
	return false
}

// NormalizePlanSlug lower-cases and trims a plan slug.
func NormalizePlanSlug(slug string) string {
	return strings.ToLower(strings.TrimSpace(slug))
}

// PlanName is the catalog display name of a plan (S, M, L, XL, Flexi); an
// unknown slug is shown upper-cased rather than hidden.
func PlanName(slug string) string {
	for _, p := range catalogPlans {
		if p.Slug == slug {
			return p.Name
		}
	}
	return strings.ToUpper(slug)
}

// PlanBillable reports whether a plan slug produces a plan line: every
// plan except "no plan" and flexi (pay per use).
func PlanBillable(slug string) bool {
	return slug != "" && slug != PlanFlexi
}

// PlanSKU is the usage SKU of a plan: plan.<slug>.
func PlanSKU(slug string) string { return PlanSKUPrefix + slug }

// PlanBookDescription is the note written on the plans book itself, so an
// operator reading the two platform books side by side sees at once which is
// the committed card and which is the pay-per-use one. It names no price:
// the prices are the book's plan items. No apostrophes — the backfill
// migration embeds it in SQL.
const PlanBookDescription = "The committed catalog packages S, M, L and XL, each priced by its plan.<slug> item of this book and billed as one flat plan.<slug> subscription line per Organization per hour: annual = monthly x 12, hourly = annual / 8760 = monthly / 730. " +
	"The k8s.vcpu / k8s.mem_gb meters are deliberately UNPRICED here - under a plan they are the allocation basis, and a bundle has no identifiable per-resource split, so any per-vCPU rate under this book would be invented; grow mode bills compute above a package at the rates of its package settings. " +
	"An Organization on the uncapped flexi plan is billed the other way round, per use and with no plan line, by the " + PAYGBookName + " book."

// PlanBookItems are the four priced plans of the "OpenOva plans" book, read
// from the engine's default offer (internal/offer): the annual price as the
// offer states it (monthly × 12), unit_price = annual_price / 8760 rounded to
// the 8 decimals price_items carry (= monthly / 730 per plan-hour, exactly as
// the divisor implies), and the offer's description. Flexi is pay per use
// and has no item.
func PlanBookItems() []PriceItem {
	var out []PriceItem
	for _, it := range offer.MustDefault().PlanItems() {
		pi, err := offerPriceItem(it, PlanBookDivisor)
		if err != nil {
			panic("the default offer compiled into this binary prices " + it.SKU + " badly: " + err.Error())
		}
		out = append(out, pi)
	}
	return out
}

// offerPriceItem is one item of the default offer as a book stores it: the
// annual price as written, the unit price through the book's divisor.
func offerPriceItem(it offer.PriceItem, divisor int) (PriceItem, error) {
	annual, ok := new(big.Rat).SetString(strings.TrimSpace(it.AnnualPrice))
	if !ok || annual.Sign() < 0 {
		return PriceItem{}, fmt.Errorf("annual price %q is not a non-negative number", it.AnnualPrice)
	}
	a := Decimal(strings.TrimSpace(it.AnnualPrice))
	return PriceItem{
		SKU:         it.SKU,
		Unit:        it.Unit,
		UnitPrice:   Decimal(new(big.Rat).Quo(annual, big.NewRat(int64(divisor), 1)).FloatString(8)),
		AnnualPrice: &a,
		Description: it.Description,
	}, nil
}

// PlanMonthly is the monthly price of a sized plan in the default offer
// (annual / 12), nil for flexi or an unknown slug.
func PlanMonthly(slug string) *big.Rat {
	for _, it := range offer.MustDefault().PlanItems() {
		if it.SKU != PlanSKU(NormalizePlanSlug(slug)) {
			continue
		}
		annual, ok := new(big.Rat).SetString(strings.TrimSpace(it.AnnualPrice))
		if !ok {
			return nil
		}
		return annual.Quo(annual, big.NewRat(12, 1))
	}
	return nil
}

// EnsurePlanBook returns the "OpenOva plans" rate card — the PLATFORM-scope
// book every Organization's openova-org source is assigned to — creating and
// pricing it when absent. created reports whether this call made it. An
// existing book is returned untouched — never re-priced, never re-created —
// so an operator's edits survive every restart and resync.
func (s *Store) EnsurePlanBook(ctx context.Context) (pb PriceBook, created bool, err error) {
	return s.ensureManagedPlatformBook(ctx, PlanBookName, PlanBookDescription, PlanBookDivisor, PlanBookItems())
}
