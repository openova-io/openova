package store

import (
	"context"
	"errors"
	"fmt"
	"math/big"
)

// Pay per use — the "Organization PAYG" rate card (founder direction
// 2026-09-10, EPIC #6867).
//
// Flexi is the fifth catalog plan and the only uncapped one: the
// org-controller's planQuotaTable
// (core/controllers/organization/internal/gitops/manifests.go) gives it no
// CPU/memory ceiling and Burstable QoS, so there is no bundle to sell and no
// flat line to charge — which is why the collector emits no plan.flexi
// record. It is therefore billed the other way the platform can bill: PER
// USE, off the same k8s.vcpu / k8s.mem_gb / k8s.pvc_gb meters the collector
// already writes for every Organization, priced by THIS book instead of the
// plans book. The two shapes never overlap: a sized Organization pays its
// plan line and its meters are unpriced, a flexi Organization pays its
// meters and emits no plan line.
const (
	// PAYGBookName is the pay-per-use rate card the Organization sync
	// assigns to a flexi Organization's platform source, creating it when
	// absent exactly as it does the plans book. The name is the one an
	// operator already sees on a Sovereign; it is never renamed.
	PAYGBookName = "Organization PAYG"
	// PAYGBookDivisor is hours per year, as the plans book uses: a monthly
	// rate m becomes an annual price m×12 and a unit price m×12/8760 =
	// m/730, so both platform books convert money the same way and a divisor
	// the operator changes recomputes both alike.
	PAYGBookDivisor = 8760
	// paygUnitGiB is the memory every sized plan bundles per vCPU (S 2 vCPU
	// / 4 GiB, M 4/8, L 8/16, XL 16/32 — planQuotaTable). It is what makes
	// the ladder below one number per plan instead of two.
	paygUnitGiB = 2
	// The pay-per-use premium over the entry commitment, as an exact
	// fraction: 10 %.
	paygPremiumNum = 11
	paygPremiumDen = 10
)

// paygShapeVCPU is the vCPU ceiling planQuotaTable enforces for each sized
// plan. Flexi has no row here: it has no ceiling, which is the whole reason
// this book exists.
var paygShapeVCPU = map[string]int64{"s": 2, "m": 4, "l": 8, "xl": 16}

// committedUnitRate is what a sized plan charges per UNIT per month, where a
// unit is 1 vCPU + paygUnitGiB GiB — the shape every plan is built from. It
// turns the founder's four prices into one comparable ladder:
//
//	S   5 OMR / 2 units  = 2.5      M  9 OMR / 4 units  = 2.25
//	L  16 OMR / 8 units  = 2.0      XL 30 OMR / 16 units = 1.875
//
// which is a volume discount: the more an Organization commits to, the less
// each unit costs. Returns nil for a plan with no fixed shape (flexi).
func committedUnitRate(slug string) *big.Rat {
	vcpu, ok := paygShapeVCPU[slug]
	if !ok {
		return nil
	}
	for _, p := range catalogPlans {
		if p.Slug == slug {
			return new(big.Rat).SetFrac64(p.MonthlyOMR, vcpu)
		}
	}
	return nil
}

// paygUnitRate is the pay-per-use price of one unit (1 vCPU + 2 GiB) per
// month: the ENTRY commitment's rate plus a premium.
//
// Pay per use commits to nothing — the Organization can scale to zero and
// stop paying that hour — so it must not undercut the cheapest thing an
// Organization can commit to, or every customer would take flexi and the
// ladder above would price nothing. S is the entry commitment at 2.5 per
// unit-month; the premium is 10 %, giving 2.75, which sits above every rung
// of the committed ladder (2.5 / 2.25 / 2.0 / 1.875) while staying close
// enough to the entry rung that flexi is a real product and not a penalty.
func paygUnitRate() *big.Rat {
	base := committedUnitRate("s")
	return base.Mul(base, big.NewRat(paygPremiumNum, paygPremiumDen))
}

// The three monthly rates this book charges, exact. The first two split
// paygUnitRate across the two things a unit is made of:
//
//	k8s.vcpu    2.000 OMR per vCPU per month
//	k8s.mem_gb  0.375 OMR per GiB per month  → 2.000 + 2 × 0.375 = 2.75 ✓
//	k8s.pvc_gb  0.219 OMR per GB per month
//
// TestPAYGUnitRateSplit checks that identity, so the split and the ladder can
// never drift apart. Storage is not part of a unit — no plan bundles any — so
// its rate is set against cost instead: the cloud charges this Sovereign
// PAYGStorageFloorPerGBHour for the SSD underneath (= 0.1667 per GB-month),
// and platform storage is not sold below what it is bought for. 0.219 per
// GB-month is that floor plus about 31 %.
func paygVCPUMonthly() *big.Rat { return big.NewRat(2, 1) }
func paygMemMonthly() *big.Rat  { return big.NewRat(3, 8) }
func paygPVCMonthly() *big.Rat  { return big.NewRat(219, 1000) }

// PAYGStorageFloorPerGBHour is what the cloud charges for the SSD under a
// platform PVC — the price k8s.pvc_gb must stay above. Stated here so the
// floor the rate was set against is checkable rather than asserted.
const PAYGStorageFloorPerGBHour = "0.00022831"

// paygRate is one pay-per-use SKU: its monthly rate and how that rate was
// arrived at, in the words the operator reads on the item.
type paygRate struct {
	SKU     string
	Unit    string
	Per     string // what one unit of quantity is
	Monthly func() *big.Rat
	Why     string
}

func paygRates() []paygRate {
	return []paygRate{
		{SKU: SKUVCPU, Unit: UnitVCPU, Per: "vCPU", Monthly: paygVCPUMonthly,
			Why: "the compute share of the 2.75 OMR pay-per-use unit (1 vCPU + 2 GiB), which is the S plan rate of 2.50 per unit-month plus a 10 percent no-commitment premium"},
		{SKU: SKUMem, Unit: UnitMem, Per: "GiB", Monthly: paygMemMonthly,
			Why: "the memory share of the same 2.75 OMR unit: 2.00 for the vCPU leaves 0.75 for the 2 GiB a unit carries, so 0.375 per GiB"},
		{SKU: SKUPVC, Unit: UnitPVC, Per: "GB", Monthly: paygPVCMonthly,
			Why: "set against cost rather than the plan ladder, because no plan bundles storage: the cloud charges " + PAYGStorageFloorPerGBHour + " OMR per GB-hour (0.1667 per GB-month) for the SSD underneath, and platform storage is not sold below what it is bought for"},
	}
}

// PAYGBookDescription is the note written on the book itself: the whole
// derivation, so the operator can see where every number came from before
// changing any of them. It deliberately carries no apostrophes — the
// one-time normalisation migration embeds it in SQL.
const PAYGBookDescription = "Pay per use for flexi Organizations (founder direction 2026-09-10). " +
	"Flexi is the one catalog plan with no quota ceiling, so it has no bundle to sell and no plan.<slug> line; it is billed off the k8s.* meters instead, and this book is what prices them. " +
	"Derivation: the sized plans are S 5 / M 9 / L 16 / XL 30 OMR per month for 2 / 4 / 8 / 16 vCPU with 2 GiB per vCPU, which is 2.50 / 2.25 / 2.00 / 1.875 OMR per unit-month for a unit of 1 vCPU + 2 GiB - a volume ladder. " +
	"Pay per use commits to nothing and so must sit above the entry commitment: 2.50 + 10 percent = 2.75 OMR per unit-month, split as 2.00 per vCPU and 0.375 per GiB. " +
	"Storage sits outside the ladder because no plan bundles any: 0.219 per GB-month, about 31 percent above the " + PAYGStorageFloorPerGBHour + " OMR per GB-hour the cloud charges for the SSD underneath. " +
	"A flexi Organization on 4 vCPU + 8 GiB around the clock therefore pays about 11 OMR a month against 9 for the committed M plan, and near zero while it is idle. " +
	"Every rate is annual = monthly x 12 and hourly = annual / 8760 = monthly / 730. Edit the items to change any of it."

// PAYGBookItems are the three priced platform meters of the pay-per-use
// book: annual = monthly × 12, unit_price = annual / PAYGBookDivisor rounded
// to the 8 decimals price_items carry — the book's own divisor, so a change
// to it recomputes every rate from the annual list price exactly as it does
// in the plans book.
//
//	k8s.vcpu    2.000/month → 24.000/yr → 0.00273973 per vcpu-hour
//	k8s.mem_gb  0.375/month →  4.500/yr → 0.00051370 per gib-hour
//	k8s.pvc_gb  0.219/month →  2.628/yr → 0.00030000 per gb-hour (exact)
func PAYGBookItems() []PriceItem {
	rates := paygRates()
	out := make([]PriceItem, 0, len(rates))
	for _, r := range rates {
		monthly := r.Monthly()
		annual := new(big.Rat).Mul(monthly, big.NewRat(12, 1))
		unit := new(big.Rat).Quo(annual, big.NewRat(PAYGBookDivisor, 1))
		annualDec := Decimal(annual.FloatString(8))
		out = append(out, PriceItem{
			SKU:         r.SKU,
			Unit:        r.Unit,
			UnitPrice:   Decimal(unit.FloatString(8)),
			AnnualPrice: &annualDec,
			Description: fmt.Sprintf("%s OMR per %s per month x 12 = %s OMR per year / %d h = %s OMR per %s. Why: %s.",
				monthly.FloatString(3), r.Per, annual.FloatString(3), PAYGBookDivisor, unit.FloatString(8), r.Unit, r.Why),
		})
	}
	return out
}

// EnsurePAYGBook returns the "Organization PAYG" rate card — the
// PLATFORM-scope book a flexi Organization's platform source is assigned to —
// creating and pricing it when absent. created reports whether this call made
// it. An existing book is returned untouched, exactly as EnsurePlanBook
// leaves the plans book: the operator may have negotiated the rates, and a
// resync must never undo that.
func (s *Store) EnsurePAYGBook(ctx context.Context) (pb PriceBook, created bool, err error) {
	return s.ensureManagedPlatformBook(ctx, PAYGBookName, PAYGBookDescription, PAYGBookDivisor, PAYGBookItems())
}

// ensureManagedPlatformBook is the shape both platform books the Organization
// sync owns are created with: platform scope, OMR, the given divisor, priced
// once, never re-priced.
func (s *Store) ensureManagedPlatformBook(ctx context.Context, name, description string, divisor int, items []PriceItem) (pb PriceBook, created bool, err error) {
	pb, err = s.GetPriceBookByName(ctx, name)
	if err == nil {
		return pb, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return PriceBook{}, false, err
	}
	pb, err = s.CreatePriceBook(ctx, PriceBookInput{Name: name, Scope: LayerPlatform, Currency: "OMR", AnnualDivisor: divisor, BillStopped: "compute", Description: description})
	if err != nil {
		if errors.Is(err, ErrConflict) {
			// Raced with another creator (two replicas): theirs wins.
			pb, err = s.GetPriceBookByName(ctx, name)
			return pb, false, err
		}
		return PriceBook{}, false, err
	}
	if _, err := s.PutPriceItems(ctx, pb.ID, items, false); err != nil {
		return PriceBook{}, false, fmt.Errorf("price the %s book: %w", name, err)
	}
	pb, err = s.GetPriceBook(ctx, pb.ID)
	return pb, true, err
}

// BookForPlan names which of the two platform books the Organization sync
// owns must rate an Organization on plan slug: the pay-per-use book for
// flexi, the plans book for every sized plan — and for an unknown or absent
// plan the plans book too, which is the fallback that was already there.
// Charging per use is a decision about a plan the platform recognises; it is
// never inferred from a slug nobody defined.
func BookForPlan(slug string) string {
	if NormalizePlanSlug(slug) == PlanFlexi {
		return PAYGBookName
	}
	return PlanBookName
}

// ManagedPlatformBooks are the books the Organization sync owns and may
// re-point a source between when the Organization changes plan. A source
// sitting on any OTHER book was put there by an operator and is never
// touched.
func ManagedPlatformBooks() []string { return []string{PlanBookName, PAYGBookName} }
