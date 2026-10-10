package synth

import (
	"math"
	"testing"
)

// The step-up rule for growth (DESIGN.md §22.11): each package's overage
// unit (1 vCPU + 2 GB at its own overage rates) stays ABOVE the next
// package's own unit price, so growing past a package is never cheaper than
// stepping up to the next one; and each rate is its package's unit price
// plus 10 %, to the baisa of the split.
func TestGrowOverageRatesStayAboveTheNextPackage(t *testing.T) {
	unitPrice := func(p Package) float64 { return p.Monthly / p.VCPU } // 1 unit = 1 vCPU + 2 GB
	for i, p := range Packages {
		overageUnit := p.OverageVCPUMonth + 2*p.OverageMemGBMonth
		// The rate is the package's unit price plus 10 %, within the
		// rounding of a three-decimal split.
		if math.Abs(overageUnit-unitPrice(p)*1.1) > 0.002 {
			t.Errorf("%s: overage unit %.3f is not its unit price %.4f + 10 %% (%.4f)", p.Name, overageUnit, unitPrice(p), unitPrice(p)*1.1)
		}
		if i+1 == len(Packages) {
			continue
		}
		next := Packages[i+1]
		if overageUnit <= unitPrice(next) {
			t.Errorf("%s grows at %.3f a unit, at or below %s's own %.4f — stepping up would be dearer than growing", p.Name, overageUnit, next.Name, unitPrice(next))
		}
	}
}

// The grow ceilings: the XL shape for S, M and L, twice it for XL, and never
// below a package's own headline.
func TestGrowCeilings(t *testing.T) {
	for _, p := range Packages {
		c := GrowCeilingOf(p)
		want := Ceiling{8, 16, 250, 1000}
		if p.Slug == "xl" {
			want = Ceiling{16, 32, 500, 2000}
		}
		if c != want {
			t.Errorf("%s ceiling = %+v, want %+v", p.Name, c, want)
		}
		if c.VCPU < p.VCPU || c.MemoryGB < p.MemoryGB || c.DiskGB < p.DiskGB {
			t.Errorf("%s ceiling %+v below its headline", p.Name, c)
		}
	}
}
