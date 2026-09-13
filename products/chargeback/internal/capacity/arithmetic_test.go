package capacity

import (
	"math/big"
	"testing"
)

// The arithmetic of a pool, pinned on the two worked cases the model was
// specified from (founder conversation 2026-09-13) and on the four rules
// those cases exist to prove:
//
//	sellable = G + (usable − G) × ratio, NOT usable × ratio − G
//	spot is excluded from admission accounting on both sides
//	the binding resource is the one with the least room, by fraction
//	the order-by date is the wall minus the procurement lead time

func rat(s string) *big.Rat {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		panic("bad rational " + s)
	}
	return r
}

// derefOrNil renders a *float64 for a failure message: the VALUE, never the
// pointer — a test whose message reads "0xc000010460" says nothing.
func derefOrNil(p *float64) any {
	if p == nil {
		return "nil"
	}
	return *p
}

func eq(t *testing.T, name string, got *big.Rat, want string) {
	t.Helper()
	if got.Cmp(rat(want)) != 0 {
		t.Errorf("%s = %s, want %s", name, got.FloatString(3), want)
	}
}

// m7nA is the pool of the worked example: 10 servers × 64 vCPU / 512 GiB,
// N+1 reserve (one server's worth), vCPU 4:1 and RAM 1:1. Sold: 40
// guaranteed and 32 burstable m7n.2xlarge (8 vCPU / 64 GiB each).
//
//	guaranteed  40 × 8  = 320 vCPU   40 × 64 = 2,560 GiB
//	burstable   32 × 8  = 256 vCPU   32 × 64 = 2,048 GiB   (nominal)
func m7nA(ramPerMachine, ramRatio string) map[string]ResourceMath {
	return map[string]ResourceMath{
		ResourceVCPU: Compute(ResourceState{
			Machines: rat("10"), PerMachine: rat("64"), Reserve: rat("64"), Ratio: rat("4"),
			Guaranteed: rat("320"), Burstable: rat("256"),
		}),
		ResourceMemoryGiB: Compute(ResourceState{
			Machines: rat("10"), PerMachine: rat(ramPerMachine), Reserve: rat(ramPerMachine), Ratio: rat(ramRatio),
			Guaranteed: rat("2560"), Burstable: rat("2048"),
		}),
	}
}

// Case one of the worked example: 512 GiB per server, RAM at 1:1.
//
//	usable      576 vCPU   4,608 GiB
//	used        384 of 576 vCPU (320 guaranteed + 256/4 burstable)
//	            4,608 of 4,608 GiB
//
// RAM IS THE BINDING RESOURCE AND THE POOL IS FULL. 192 physical vCPU are
// stranded and unsellable: nothing more sells, of any class, because every VM
// also needs RAM.
func TestWorkedExampleRAMBindsAndVCPUIsStranded(t *testing.T) {
	all := m7nA("512", "1")
	cpu, ram := all[ResourceVCPU], all[ResourceMemoryGiB]

	eq(t, "vcpu usable", cpu.Usable, "576")
	eq(t, "ram usable", ram.Usable, "4608")
	// sellable = G + (usable − G) × ratio.
	eq(t, "vcpu sellable", cpu.Sellable, "1344") // 320 + (576−320)×4
	eq(t, "ram sellable", ram.Sellable, "4608")  // 2560 + (4608−2560)×1

	// Physically: 320 + 256/4 = 384 of 576 vCPU; 2,560 + 2,048/1 = 4,608 GiB.
	eq(t, "vcpu physical used", cpu.PhysicalUsed, "384")
	eq(t, "ram physical used", ram.PhysicalUsed, "4608")
	eq(t, "vcpu physical free (stranded)", cpu.PhysicalFree, "192")
	eq(t, "ram physical free", ram.PhysicalFree, "0")

	eq(t, "vcpu remaining", cpu.Remaining, "768")
	eq(t, "ram remaining", ram.Remaining, "0")

	if got := Binding(all); got != ResourceMemoryGiB {
		t.Fatalf("binding resource = %q, want memory_gib: RAM has 0 %% of its sellable left, vCPU has 57 %%", got)
	}
	if ram.Status != StatusCritical {
		t.Fatalf("a full pool is critical, got %s", ram.Status)
	}

	// Nothing more sells, of any class. One more 2xlarge, guaranteed:
	one := map[string]BasketDemand{
		ResourceVCPU:      {Guaranteed: rat("8")},
		ResourceMemoryGiB: {Guaranteed: rat("64")},
	}
	fit := Fit(all, one)
	if fit.Units == nil || fit.Units.Int64() != 0 || fit.Binding != ResourceMemoryGiB {
		t.Fatalf("one more guaranteed 2xlarge = %v on %q, want 0 on memory_gib", fit.Units, fit.Binding)
	}
	// vCPU alone would take 24 more (192 stranded ÷ 8), which is exactly why
	// a per-SKU headroom column printed per resource was a wrong answer.
	if fit.Per[ResourceVCPU].Int64() != 24 {
		t.Fatalf("vcpu alone allows %v, want 24", fit.Per[ResourceVCPU])
	}
	// And burstable is refused too: the envelope is full.
	if AdmitBurstable(ram, rat("64")) {
		t.Fatal("a burstable order must be refused when the RAM envelope is full")
	}
}

// Case two: the same pool with 1,024 GiB per server and RAM ballooning at
// 1.5:1. RAM's sellable more than doubles, and VCPU BINDS INSTEAD — the
// binding resource moves with the ratios, which is the point of both cases.
func TestWorkedExampleVCPUBindsOnceRAMBalloons(t *testing.T) {
	all := m7nA("1024", "1.5")
	cpu, ram := all[ResourceVCPU], all[ResourceMemoryGiB]

	eq(t, "ram usable", ram.Usable, "9216")
	eq(t, "ram sellable", ram.Sellable, "12544") // 2560 + (9216−2560)×1.5
	eq(t, "vcpu sellable", cpu.Sellable, "1344")

	if got := Binding(all); got != ResourceVCPU {
		t.Fatalf("binding resource = %q, want vcpu: vCPU has 57 %% of its sellable left, RAM 63 %%", got)
	}

	// The founder's conversation sized the case from the GUARANTEED FLOOR:
	// (12,544 − 2,560)/64 = 156 by RAM against (1,344 − 320)/8 = 128 by vCPU,
	// "so vCPU binds". Those two figures are pinned here because they are what
	// the example states; they count from G and so do not deduct the 32
	// burstable already sold.
	eq(t, "ram sellable above the guaranteed floor", new(big.Rat).Sub(ram.Sellable, ram.Guaranteed), "9984")  // 156 × 64
	eq(t, "vcpu sellable above the guaranteed floor", new(big.Rat).Sub(cpu.Sellable, cpu.Guaranteed), "1024") // 128 × 8

	// What the module reports is how many MORE fit, which deducts everything
	// already sold and charges a guaranteed unit the ratio × envelope it
	// actually costs. The binding resource — what the example proves — is the
	// same either way.
	one := map[string]BasketDemand{
		ResourceVCPU:      {Guaranteed: rat("8")},
		ResourceMemoryGiB: {Guaranteed: rat("64")},
	}
	fit := Fit(all, one)
	if fit.Binding != ResourceVCPU {
		t.Fatalf("fit binds on %q, want vcpu", fit.Binding)
	}
	// vCPU: remaining 768 ÷ (8 × 4) = 24. RAM: remaining 7,936 ÷ (64 × 1.5) = 82.
	if fit.Units.Int64() != 24 || fit.Per[ResourceMemoryGiB].Int64() != 82 {
		t.Fatalf("fit = %v (vcpu) / %v (ram), want 24 / 82", fit.Units, fit.Per[ResourceMemoryGiB])
	}
}

// sellable = G + (usable − G) × ratio, and NOT usable × ratio − G.
//
// The wrong form multiplies the guaranteed capacity too, so it hands back
// (ratio − 1) × G of room that is physically spoken for: on the pool below it
// claims 1,984 sellable vCPU where 1,344 exist. Every guaranteed unit sold
// must remove ratio × worth of oversubscribed room, which is what makes the
// guarantee real rather than a label.
func TestSellableIsNotUsableTimesRatioMinusG(t *testing.T) {
	m := Compute(ResourceState{
		Machines: rat("10"), PerMachine: rat("64"), Reserve: rat("64"), Ratio: rat("4"),
		Guaranteed: rat("320"),
	})
	eq(t, "sellable", m.Sellable, "1344")

	wrong := new(big.Rat).Sub(new(big.Rat).Mul(m.Usable, rat("4")), m.Guaranteed) // 576×4 − 320
	eq(t, "the wrong form, for the record", wrong, "1984")
	if m.Sellable.Cmp(wrong) == 0 {
		t.Fatal("usable × ratio − G must not equal G + (usable − G) × ratio here")
	}

	// Selling 10 more guaranteed vCPU costs FOUR TIMES that in sellable room.
	more := Compute(ResourceState{
		Machines: rat("10"), PerMachine: rat("64"), Reserve: rat("64"), Ratio: rat("4"),
		Guaranteed: rat("330"),
	})
	eq(t, "sellable after 10 more guaranteed", more.Sellable, "1314") // 1344 − 10×(4−1)
	eq(t, "the room 10 guaranteed vCPU removed", new(big.Rat).Sub(m.Sellable, more.Sellable), "30")

	// Guaranteed past usable leaves nothing to multiply: sellable is G, not a
	// negative bracket multiplied into a larger number.
	over := Compute(ResourceState{Machines: rat("10"), PerMachine: rat("64"), Reserve: rat("64"), Ratio: rat("4"), Guaranteed: rat("600")})
	eq(t, "sellable when oversold on the guarantee", over.Sellable, "600")
	eq(t, "guaranteed ceiling when oversold", over.GuaranteedCeiling, "0")
}

// Spot is EXCLUDED from admission accounting on both sides. A pool running
// 400 nominal vCPU of spot admits exactly the same guaranteed and burstable
// orders as the same pool running none; what changes is how much spot must be
// RECLAIMED. BSS says how much; the platform decides which instances.
func TestSpotIsExcludedFromAdmissionOnBothSides(t *testing.T) {
	base := ResourceState{Machines: rat("10"), PerMachine: rat("64"), Reserve: rat("64"), Ratio: rat("4"), Guaranteed: rat("320"), Burstable: rat("256")}
	withSpot := base
	withSpot.Spot = rat("400")

	quiet, loud := Compute(base), Compute(withSpot)

	for _, f := range []struct {
		name string
		a, b *big.Rat
	}{
		{"sellable", quiet.Sellable, loud.Sellable},
		{"sold nominal", quiet.SoldNominal, loud.SoldNominal},
		{"remaining", quiet.Remaining, loud.Remaining},
		{"guaranteed ceiling", quiet.GuaranteedCeiling, loud.GuaranteedCeiling},
		{"physical free", quiet.PhysicalFree, loud.PhysicalFree},
	} {
		if f.a.Cmp(f.b) != 0 {
			t.Errorf("%s changed when spot appeared: %s vs %s — spot must be on neither side of admission", f.name, f.a.FloatString(3), f.b.FloatString(3))
		}
	}
	if !AdmitGuaranteed(loud, rat("256")) {
		t.Fatal("a guaranteed order that fits usable must never be refused on account of spot")
	}
	if !AdmitBurstable(loud, rat("768")) {
		t.Fatal("a burstable order within the envelope must never be refused on account of spot")
	}

	// Spot's own room is what physically remains: 192 free vCPU × 4 = 768
	// nominal. 400 fits, so nothing is reclaimed.
	eq(t, "spot room", loud.SpotRoom, "768")
	eq(t, "spot reclaim", loud.SpotReclaim, "0")

	// Sell 32 more guaranteed (256 vCPU) and the room collapses: the excess
	// spot becomes reclaimable, and it is the ONLY thing that changed for spot.
	tight := withSpot
	tight.Guaranteed = rat("576")
	tightM := Compute(tight)
	eq(t, "spot room once guaranteed fills the pool", tightM.SpotRoom, "0")
	eq(t, "spot to reclaim", tightM.SpotReclaim, "400")
}

// The binding resource is the one with the least room AS A FRACTION of what
// it could sell. Raw amounts cannot be compared — 768 vCPU against 0 GiB is
// not a comparison of numbers — and an unsized resource is skipped rather
// than counted as empty.
func TestBindingIsFractionalAndSkipsUnsized(t *testing.T) {
	all := map[string]ResourceMath{
		// 90 % of its (small) sellable left, but a small number.
		ResourceEIP: Compute(ResourceState{Machines: rat("1"), PerMachine: rat("100"), Ratio: rat("1"), Guaranteed: rat("10")}),
		// 40 % of a huge sellable left: a far bigger absolute number, and yet
		// this is the one that runs out first.
		ResourceVCPU: Compute(ResourceState{Machines: rat("10"), PerMachine: rat("64"), Ratio: rat("1"), Guaranteed: rat("384")}),
		// Nobody has sized this one; it carries no information.
		ResourceMemoryGiB: Compute(ResourceState{Machines: rat("0"), PerMachine: rat("0")}),
	}
	if got := Binding(all); got != ResourceVCPU {
		t.Fatalf("binding = %q, want vcpu (40 %% left) over eip_addresses (90 %% left)", got)
	}
	if all[ResourceMemoryGiB].Status != StatusUnset || all[ResourceMemoryGiB].UtilisationPct != nil {
		t.Fatal("an unsized resource must read unset with no percentage, never ok")
	}
	if Binding(map[string]ResourceMath{ResourceMemoryGiB: all[ResourceMemoryGiB]}) != "" {
		t.Fatal("a pool with nothing sized has no binding resource")
	}
}

// A basket is nil, with a reason to hand to the reader, when nothing about it
// can be measured; a spot basket is limited only by the spot room.
func TestFitIsNilWhenNothingIsSized(t *testing.T) {
	unsized := map[string]ResourceMath{ResourceVCPU: Compute(ResourceState{})}
	fit := Fit(unsized, map[string]BasketDemand{ResourceVCPU: {Guaranteed: rat("8")}})
	if fit.Units != nil || fit.Binding != "" || len(fit.Per) != 0 {
		t.Fatalf("an unsized pool must report nothing, not 0: %+v", fit)
	}

	// A resource the pool does not hold at all contributes nothing either.
	sized := map[string]ResourceMath{ResourceVCPU: Compute(ResourceState{Machines: rat("1"), PerMachine: rat("64"), Ratio: rat("1")})}
	fit = Fit(sized, map[string]BasketDemand{ResourceBandwidth: {Guaranteed: rat("1")}})
	if fit.Units != nil {
		t.Fatalf("a basket the pool cannot measure must report nothing: %+v", fit)
	}

	// Spot: 64 usable at 4:1 with 32 guaranteed → physical free 32, spot room
	// 128 nominal, 64 already running → 64 nominal left, 8 per unit → 8 more.
	spot := map[string]ResourceMath{ResourceVCPU: Compute(ResourceState{
		Machines: rat("1"), PerMachine: rat("64"), Ratio: rat("4"), Guaranteed: rat("32"), Spot: rat("64"),
	})}
	fit = Fit(spot, map[string]BasketDemand{ResourceVCPU: {Spot: rat("8")}})
	if fit.Units == nil || fit.Units.Int64() != 8 {
		t.Fatalf("spot fit = %v, want 8", fit.Units)
	}
}

// The two walls and the date that matters. Growth is per class, because a
// blended line hides which class is moving: guaranteed growth is a hardware
// order, burstable growth is a throttling date.
func TestProjectWallsAndTheOrderByDate(t *testing.T) {
	// usable 576 vCPU at 4:1, 320 guaranteed and 256 burstable running.
	// sellable 1,344, remaining 768, guaranteed ceiling 256.
	m := Compute(ResourceState{Machines: rat("10"), PerMachine: rat("64"), Reserve: rat("64"), Ratio: rat("4"), Guaranteed: rat("320"), Burstable: rat("256")})

	g, b := 8.0, 16.0
	w := Project(m, &g, &b, 45)
	// soft: 768 ÷ (16 + 8×4) = 16 days. hard: 256 ÷ 8 = 32 days.
	if w.Soft == nil || *w.Soft != 16 {
		t.Fatalf("soft wall = %v, want 16 days", derefOrNil(w.Soft))
	}
	if w.Hard == nil || *w.Hard != 32 {
		t.Fatalf("hard wall = %v, want 32 days", derefOrNil(w.Hard))
	}
	if w.Wall != WallSoft || *w.Days != 16 {
		t.Fatalf("the nearer wall is the soft one: %+v", w)
	}
	// The date that matters: 16 days to the wall, 45 days to procure. The
	// order is already late by 29 days — an alert keyed on the wall itself
	// would fire 29 days after it was too late.
	if w.OrderBy == nil || *w.OrderBy != -29 {
		t.Fatalf("order-by = %v days, want -29", derefOrNil(w.OrderBy))
	}

	// GUARANTEED GROWTH ALONE STILL MOVES THE SOFT WALL, and faster than its
	// own size: each guaranteed unit removes ratio × of the envelope.
	// 768 ÷ (0 + 8×4) = 24 days, against a hard wall at 32.
	onlyG := Project(m, &g, nil, 0)
	if onlyG.Soft == nil || *onlyG.Soft != 24 || onlyG.Hard == nil || *onlyG.Hard != 32 {
		t.Fatalf("guaranteed-only projection = %+v, want soft 24 / hard 32", onlyG)
	}

	// Burstable alone never reaches the hard wall: no hardware order is owed.
	onlyB := Project(m, nil, &b, 0)
	if onlyB.Hard != nil {
		t.Fatalf("burstable growth alone cannot exhaust the guarantee: %+v", onlyB)
	}
	if onlyB.Soft == nil || *onlyB.Soft != 48 { // 768 ÷ 16
		t.Fatalf("burstable-only soft wall = %v, want 48", derefOrNil(onlyB.Soft))
	}

	// Nothing growing, nothing to say — and an unsized resource says nothing
	// at all rather than "never".
	if flat := Project(m, nil, nil, 10); flat.Soft != nil || flat.Hard != nil || flat.Days != nil || flat.OrderBy != nil {
		t.Fatalf("a pool that is not growing has no wall: %+v", flat)
	}
	tiny := 0.0000001
	if far := Project(m, &tiny, nil, 0); far.Soft != nil || far.Hard != nil {
		t.Fatal("a wall beyond a century is arithmetic, not a plan; it must not be reported")
	}
	if none := Project(Compute(ResourceState{}), &g, &b, 0); none.Days != nil {
		t.Fatalf("an unsized resource has no wall: %+v", none)
	}
}

// Reserve is held back before anything is sold, and N+1 is one machine's
// worth. A reserve that swallows the pool leaves nothing sellable — which is
// full, not empty, and must never read ok.
func TestReserveAndTheFullyReservedPool(t *testing.T) {
	m := Compute(ResourceState{Machines: rat("10"), PerMachine: rat("64"), Reserve: rat("64"), Ratio: rat("4")})
	eq(t, "raw", m.Raw, "640")
	eq(t, "usable", m.Usable, "576")

	all := Compute(ResourceState{Machines: rat("10"), PerMachine: rat("64"), Reserve: rat("640"), Ratio: rat("4")})
	eq(t, "usable when everything is reserved", all.Usable, "0")
	eq(t, "sellable when everything is reserved", all.Sellable, "0")
	if !all.Sized || all.Status != StatusCritical {
		t.Fatalf("a pool whose reserve took all of it is sized and full, got sized=%v status=%s", all.Sized, all.Status)
	}

	// Over-reserving cannot make usable negative and so cannot invent room.
	over := Compute(ResourceState{Machines: rat("1"), PerMachine: rat("10"), Reserve: rat("99"), Ratio: rat("2")})
	eq(t, "usable never goes below zero", over.Usable, "0")
}

// Overcommitment is reported, not clamped away: a pool already past its
// envelope says by how much, and the room it has left is 0 rather than a
// negative number rendered as a figure.
func TestOvercommittedPoolReportsTheExcess(t *testing.T) {
	m := Compute(ResourceState{Machines: rat("1"), PerMachine: rat("64"), Ratio: rat("1"), Guaranteed: rat("40"), Burstable: rat("40")})
	eq(t, "sellable", m.Sellable, "64")
	eq(t, "sold", m.SoldNominal, "80")
	eq(t, "remaining", m.Remaining, "0")
	eq(t, "over", m.Over, "16")
	if !m.Overcommitted || m.Status != StatusCritical {
		t.Fatalf("an oversold pool is critical and says so: %+v", m)
	}
}
