package store

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
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

// catalogPlan is one catalog plan as the Sovereign sells it. The four
// monthly prices are COPIED from core/services/catalog/handlers/seed.go
// seedPlanRows (S 5 · M 9 · L 16 · XL 30 OMR/month; Flexi 0 = pay per use)
// — the chargeback module keeps the D5 invariant of importing nothing from
// Catalyst, so the numbers are restated here and the comment names their
// source. The org-controller's planQuotaTable
// (core/controllers/organization/internal/gitops/manifests.go) mirrors the
// same rows: S = 2 vCPU / 4 GB, M = 4/8, L = 8/16, XL = 16/32.
type catalogPlan struct {
	Slug       string
	Name       string
	MonthlyOMR int64
}

var catalogPlans = []catalogPlan{
	{Slug: "s", Name: "S", MonthlyOMR: 5},
	{Slug: "m", Name: "M", MonthlyOMR: 9},
	{Slug: "l", Name: "L", MonthlyOMR: 16},
	{Slug: "xl", Name: "XL", MonthlyOMR: 30},
	{Slug: PlanFlexi, Name: "Flexi", MonthlyOMR: 0},
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

// planNote is appended to every plan item's description. price_books has no
// description column, so the item descriptions are the one place the
// operator reads the book's intent.
const planNote = "k8s.vcpu / k8s.mem_gb / k8s.pvc_gb are deliberately not priced in this book: they are the allocation basis, and Flexi's pay-per-use rates are a product decision the founder has not made."

// PlanBookItems are the four priced plans of the "OpenOva plans" book:
// annual_price = monthly × 12, unit_price = annual_price / 8760 rounded to
// the 8 decimals price_items carry (= monthly / 730 per plan-hour, exactly
// as the divisor implies). Flexi is pay per use and has no item.
func PlanBookItems() []PriceItem {
	var out []PriceItem
	for _, p := range catalogPlans {
		if p.MonthlyOMR == 0 {
			continue
		}
		annual := p.MonthlyOMR * 12
		annualDec := Decimal(fmt.Sprintf("%d", annual))
		unit := new(big.Rat).SetFrac64(annual, PlanBookDivisor).FloatString(8)
		out = append(out, PriceItem{
			SKU:         PlanSKU(p.Slug),
			Unit:        PlanUnit,
			UnitPrice:   Decimal(unit),
			AnnualPrice: &annualDec,
			Description: fmt.Sprintf("%s plan: %d OMR/month × 12 = %d OMR/year ÷ %d h = %d/730 = %s OMR per plan-hour. %s",
				p.Name, p.MonthlyOMR, annual, PlanBookDivisor, p.MonthlyOMR, unit, planNote),
		})
	}
	return out
}

// EnsurePlanBook returns the "OpenOva plans" rate card — the PLATFORM-scope
// book every Organization's openova-org source is assigned to — creating and
// pricing it when absent. created reports whether this call made it. An
// existing book is returned untouched — never re-priced, never re-created —
// so an operator's edits survive every restart and resync.
func (s *Store) EnsurePlanBook(ctx context.Context) (pb PriceBook, created bool, err error) {
	pb, err = s.GetPriceBookByName(ctx, PlanBookName)
	if err == nil {
		return pb, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return PriceBook{}, false, err
	}
	pb, err = s.CreatePriceBook(ctx, PriceBookInput{Name: PlanBookName, Scope: LayerPlatform, Currency: "OMR", AnnualDivisor: PlanBookDivisor, BillStopped: "compute"})
	if err != nil {
		if errors.Is(err, ErrConflict) {
			// Raced with another creator (two replicas): theirs wins.
			pb, err = s.GetPriceBookByName(ctx, PlanBookName)
			return pb, false, err
		}
		return PriceBook{}, false, err
	}
	if _, err := s.PutPriceItems(ctx, pb.ID, PlanBookItems(), false); err != nil {
		return PriceBook{}, false, fmt.Errorf("price the plan book: %w", err)
	}
	pb, err = s.GetPriceBook(ctx, pb.ID)
	return pb, true, err
}
