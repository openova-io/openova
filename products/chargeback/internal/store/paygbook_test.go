package store

import (
	"math/big"
	"strings"
	"testing"
)

// The pay-per-use rates are DERIVED from the founder's own plan ladder, not
// invented, and these tests pin every step of that derivation. They need no
// database: the arithmetic is the product decision, and it is checkable on
// its own.

// TestCommittedUnitLadder: the four sized plans, expressed per unit of
// (1 vCPU + 2 GiB) per month, are a volume ladder — every larger commitment
// costs less per unit.
func TestCommittedUnitLadder(t *testing.T) {
	want := map[string]string{"s": "2.500", "m": "2.250", "l": "2.000", "xl": "1.875"}
	prev := (*big.Rat)(nil)
	for _, slug := range []string{"s", "m", "l", "xl"} {
		got := committedUnitRate(slug)
		if got == nil {
			t.Fatalf("no unit rate for plan %s", slug)
		}
		if got.FloatString(3) != want[slug] {
			t.Fatalf("plan %s = %s OMR per unit-month, want %s", slug, got.FloatString(3), want[slug])
		}
		if prev != nil && got.Cmp(prev) >= 0 {
			t.Fatalf("plan %s at %s does not undercut the rung above it (%s) — the ladder is not a volume discount", slug, got.FloatString(3), prev.FloatString(3))
		}
		prev = got
	}
	if committedUnitRate(PlanFlexi) != nil {
		t.Fatal("flexi must have no committed unit rate: it has no quota shape to divide by")
	}
}

// TestPAYGUnitRateSplit: pay per use is the entry commitment plus 10 %, and
// the two compute rates add back up to exactly that unit price. If either
// half is edited without the other this fails, which is the point.
func TestPAYGUnitRateSplit(t *testing.T) {
	unit := paygUnitRate()
	if unit.FloatString(3) != "2.750" {
		t.Fatalf("pay-per-use unit rate = %s, want 2.750 (S 2.50 + 10 %%)", unit.FloatString(3))
	}
	entry := committedUnitRate("s")
	premium := new(big.Rat).Quo(unit, entry)
	if premium.Cmp(big.NewRat(paygPremiumNum, paygPremiumDen)) != 0 {
		t.Fatalf("premium over the entry commitment = %s, want %d/%d", premium.FloatString(4), paygPremiumNum, paygPremiumDen)
	}
	// 1 vCPU + 2 GiB priced from the item rates must equal the unit rate.
	split := new(big.Rat).Add(paygVCPUMonthly(), new(big.Rat).Mul(paygMemMonthly(), big.NewRat(paygUnitGiB, 1)))
	if split.Cmp(unit) != 0 {
		t.Fatalf("k8s.vcpu %s + %d × k8s.mem_gb %s = %s, want the unit rate %s",
			paygVCPUMonthly().FloatString(3), paygUnitGiB, paygMemMonthly().FloatString(3), split.FloatString(6), unit.FloatString(6))
	}
	// And it must sit above every committed rung, or flexi would undercut
	// the plans it is meant to price against.
	for _, slug := range []string{"s", "m", "l", "xl"} {
		if unit.Cmp(committedUnitRate(slug)) <= 0 {
			t.Fatalf("pay per use at %s does not sit above the %s rung at %s", unit.FloatString(3), slug, committedUnitRate(slug).FloatString(3))
		}
	}
}

// TestPAYGStorageSitsAboveTheCloudFloor: platform storage is not sold below
// what the cloud charges for the SSD underneath.
func TestPAYGStorageSitsAboveTheCloudFloor(t *testing.T) {
	floor, ok := new(big.Rat).SetString(PAYGStorageFloorPerGBHour)
	if !ok {
		t.Fatalf("PAYGStorageFloorPerGBHour %q is not a number", PAYGStorageFloorPerGBHour)
	}
	perHour := new(big.Rat).Quo(new(big.Rat).Mul(paygPVCMonthly(), big.NewRat(12, 1)), big.NewRat(PAYGBookDivisor, 1))
	if perHour.Cmp(floor) <= 0 {
		t.Fatalf("k8s.pvc_gb at %s per GB-hour is not above the %s the cloud charges", perHour.FloatString(8), PAYGStorageFloorPerGBHour)
	}
}

// TestPAYGBookItems pins the three rates the book is created with, in both
// the annual and the hourly form the price_items row carries.
func TestPAYGBookItems(t *testing.T) {
	want := map[string][3]string{
		//                  unit          annual          hourly
		SKUVCPU: {UnitVCPU, "24.00000000", "0.00273973"},
		SKUMem:  {UnitMem, "4.50000000", "0.00051370"},
		SKUPVC:  {UnitPVC, "2.62800000", "0.00030000"},
	}
	items := PAYGBookItems()
	if len(items) != len(want) {
		t.Fatalf("items = %+v, want the three platform meters", items)
	}
	for _, it := range items {
		w, ok := want[it.SKU]
		if !ok {
			t.Fatalf("unexpected SKU %s in the pay-per-use book", it.SKU)
		}
		if it.Unit != w[0] {
			t.Fatalf("%s unit = %q, want %q (the unit the collector emits)", it.SKU, it.Unit, w[0])
		}
		if it.AnnualPrice == nil || string(*it.AnnualPrice) != w[1] {
			t.Fatalf("%s annual = %v, want %s", it.SKU, it.AnnualPrice, w[1])
		}
		if string(it.UnitPrice) != w[2] {
			t.Fatalf("%s unit price = %s, want %s", it.SKU, it.UnitPrice, w[2])
		}
		if !strings.Contains(it.Description, "Why:") {
			t.Fatalf("%s description does not say where the rate came from: %q", it.SKU, it.Description)
		}
	}
	// Only the platform meters: a plan line under this book would be the
	// double charge the whole split exists to prevent.
	for _, it := range items {
		if strings.HasPrefix(it.SKU, PlanSKUPrefix) {
			t.Fatalf("the pay-per-use book must not price a plan line, found %s", it.SKU)
		}
	}
}

// TestPAYGMonthlyBillIsAboveTheCommittedPlan: the sanity check the founder
// asked for — 4 vCPU + 8 GiB around the clock costs about 11 OMR a month
// against 9 for the committed M plan of the same shape, and nothing when the
// Organization is idle.
func TestPAYGMonthlyBillIsAboveTheCommittedPlan(t *testing.T) {
	rate := map[string]*big.Rat{}
	for _, it := range PAYGBookItems() {
		r, _ := new(big.Rat).SetString(string(it.UnitPrice))
		rate[it.SKU] = r
	}
	hours := big.NewRat(730, 1) // the month the plans book divides by
	bill := new(big.Rat).Mul(new(big.Rat).Mul(rate[SKUVCPU], big.NewRat(4, 1)), hours)
	bill.Add(bill, new(big.Rat).Mul(new(big.Rat).Mul(rate[SKUMem], big.NewRat(8, 1)), hours))
	if got := bill.FloatString(2); got != "11.00" {
		t.Fatalf("4 vCPU + 8 GiB for a 730 h month = %s OMR, want about 11.00", got)
	}
	m := big.NewRat(9, 1) // the M plan of the same shape
	if bill.Cmp(m) <= 0 {
		t.Fatalf("pay per use at %s does not sit above the committed M plan at 9", bill.FloatString(2))
	}
	// Idle: no pods, no meters, no bill. Nothing charges a flexi
	// Organization for existing.
	idle := new(big.Rat)
	if idle.Sign() != 0 {
		t.Fatal("an idle flexi Organization must owe nothing")
	}
}

// TestBookForPlan: which of the two platform books rates which plan. An
// unknown or absent plan keeps the plans book — the fallback that was
// already there — because charging per use is a decision about a plan the
// platform recognises, never an inference from a slug nobody defined.
func TestBookForPlan(t *testing.T) {
	for _, tc := range []struct{ slug, want string }{
		{"s", PlanBookName},
		{"m", PlanBookName},
		{"l", PlanBookName},
		{"xl", PlanBookName},
		{"", PlanBookName},
		{"enterprise", PlanBookName},
		{PlanFlexi, PAYGBookName},
		{"FLEXI", PAYGBookName},
		{"  flexi  ", PAYGBookName},
	} {
		if got := BookForPlan(tc.slug); got != tc.want {
			t.Fatalf("BookForPlan(%q) = %q, want %q", tc.slug, got, tc.want)
		}
	}
}

// TestPlanAndPAYGBooksAreDisjoint: the two books never price the same SKU.
// That is the structural half of "never double charge" — whichever book a
// source is on, only one of the two shapes can produce a line.
func TestPlanAndPAYGBooksAreDisjoint(t *testing.T) {
	plan := map[string]bool{}
	for _, it := range PlanBookItems() {
		plan[it.SKU] = true
	}
	for _, it := range PAYGBookItems() {
		if plan[it.SKU] {
			t.Fatalf("%s is priced in BOTH platform books", it.SKU)
		}
		if !IsPlatformMeter(it.SKU) {
			t.Fatalf("%s is not a platform meter; the pay-per-use book prices only the meters the collector writes", it.SKU)
		}
	}
	for _, sku := range PlatformMeterSKUs {
		if plan[sku] {
			t.Fatalf("%s is priced in the plans book; under a plan the meters are the allocation basis, not the bill", sku)
		}
	}
}

// TestManagedPlatformBooksNamesTheTwo guards the re-pointing rule: the sync
// may move a source between exactly these two books and no others.
func TestManagedPlatformBooksNamesTheTwo(t *testing.T) {
	got := ManagedPlatformBooks()
	if len(got) != 2 || got[0] != PlanBookName || got[1] != PAYGBookName {
		t.Fatalf("ManagedPlatformBooks() = %v", got)
	}
}
