package capacity

import (
	"math/big"
	"testing"
)

func r(n int64) *big.Rat { return big.NewRat(n, 1) }

func rs(t *testing.T, s string) *big.Rat {
	t.Helper()
	v, ok := new(big.Rat).SetString(s)
	if !ok {
		t.Fatalf("bad rational %q", s)
	}
	return v
}

func wantRat(t *testing.T, name string, got *big.Rat, want string) {
	t.Helper()
	if got.Cmp(rs(t, want)) != 0 {
		t.Errorf("%s = %s, want %s", name, got.FloatString(3), want)
	}
}

// The founder's case, verbatim: 100 of capacity, 60 of it dedicated to
// guaranteed, 40 meant for overcommitment at 2.5. Burstable arrives FIRST and
// takes everything it is allowed. It must stop at 40 physical (100 nominal),
// and guaranteed must still have its 60.
func TestFloorKeepsBurstableOutOfGuaranteedRoom(t *testing.T) {
	base := ResourceState{Machines: r(1), PerMachine: r(100), Ratio: rs(t, "2.5")}

	// Without a floor the envelope is the whole machine: 250 nominal, which
	// is every physical unit, and nothing is left for a guarantee.
	open := Compute(base)
	wantRat(t, "no floor: envelope", open.BurstableEnvelope, "250")
	if !AdmitBurstable(open, r(250)) {
		t.Fatal("no floor: 250 burstable must be admissible — that is the defect the floor closes")
	}

	withFloor := base
	withFloor.Floor = r(60)
	m := Compute(withFloor)
	wantRat(t, "envelope", m.BurstableEnvelope, "100")
	wantRat(t, "sellable", m.Sellable, "160")
	wantRat(t, "floor free", m.FloorFree, "60")
	if !AdmitBurstable(m, r(100)) {
		t.Error("100 nominal burstable (40 physical) must be admitted")
	}
	if AdmitBurstable(m, rs(t, "100.001")) {
		t.Error("burstable past 100 nominal would draw on the guaranteed floor and must be refused")
	}

	// Burstable has now sold its entire envelope — "already consumed 100 %".
	withFloor.Burstable = r(100)
	full := Compute(withFloor)
	wantRat(t, "burstable room when full", full.BurstableRoom, "0")
	if AdmitBurstable(full, r(1)) {
		t.Error("a full envelope admits no more burstable")
	}
	if !AdmitGuaranteed(full, r(60)) {
		t.Error("guaranteed must still get its 60 after burstable took everything it could")
	}
	wantRat(t, "remaining is the idle floor", full.Remaining, "60")
	if full.Overcommitted {
		t.Error("a full envelope is full, not overcommitted")
	}
}

// Burstable that was sold before the floor existed (or that simply grew) is
// past the envelope even while the total looks roomy. It must read as
// overcommitted and critical — judged on the envelope, never on the total.
func TestBurstablePastTheEnvelopeIsCriticalEvenWithAnIdleFloor(t *testing.T) {
	m := Compute(ResourceState{Machines: r(1), PerMachine: r(100), Ratio: rs(t, "2.5"), Floor: r(60), Burstable: r(150)})
	if !m.Overcommitted {
		t.Fatal("150 burstable against an envelope of 100 is overcommitted")
	}
	wantRat(t, "over", m.Over, "50")
	if m.Status != StatusCritical {
		t.Errorf("status = %s, want critical: utilisation reads %.1f %% only because the floor is idle", m.Status, *m.UtilisationPct)
	}
	if *m.UtilisationPct >= 100 {
		t.Fatalf("vacuous: this test means something only while the total reads under 100 %% (got %.1f)", *m.UtilisationPct)
	}
}

// Guaranteed landing inside its own floor costs burstable nothing; past the
// floor each unit removes ratio × worth of envelope.
func TestGuaranteedIsFreeInsideTheFloorAndCostsRatioPastIt(t *testing.T) {
	st := ResourceState{Machines: r(1), PerMachine: r(100), Ratio: rs(t, "2.5"), Floor: r(60)}
	st.Guaranteed = r(30)
	wantRat(t, "G=30 envelope", Compute(st).BurstableEnvelope, "100")
	st.Guaranteed = r(60)
	wantRat(t, "G=60 envelope", Compute(st).BurstableEnvelope, "100")
	st.Guaranteed = r(70)
	wantRat(t, "G=70 envelope", Compute(st).BurstableEnvelope, "75")
	wantRat(t, "G=70 sellable", Compute(st).Sellable, "145")
}

// Spot may run in the idle floor: it is reclaimed the moment a guarantee
// wants the room, so the floor never strands hardware.
func TestSpotMayUseTheIdleFloor(t *testing.T) {
	m := Compute(ResourceState{Machines: r(1), PerMachine: r(100), Ratio: r(1), Floor: r(60), Spot: r(80)})
	wantRat(t, "spot room", m.SpotRoom, "100")
	wantRat(t, "spot reclaim", m.SpotReclaim, "0")
	m = Compute(ResourceState{Machines: r(1), PerMachine: r(100), Ratio: r(1), Floor: r(60), Guaranteed: r(50), Spot: r(80)})
	wantRat(t, "spot reclaim once guaranteed lands", m.SpotReclaim, "30")
}

// A floor larger than the hardware ring-fences all of it and no more.
func TestFloorIsClampedToUsable(t *testing.T) {
	m := Compute(ResourceState{Machines: r(1), PerMachine: r(100), Reserve: r(10), Ratio: r(4), Floor: r(500)})
	wantRat(t, "floor", m.Floor, "90")
	wantRat(t, "envelope", m.BurstableEnvelope, "0")
	wantRat(t, "sellable", m.Sellable, "90")
}

func TestFitIsPiecewiseAroundTheFloor(t *testing.T) {
	all := func(st ResourceState) map[string]ResourceMath { return map[string]ResourceMath{"vcpu": Compute(st)} }
	st := ResourceState{Machines: r(1), PerMachine: r(100), Ratio: rs(t, "2.5"), Floor: r(60)}

	// Pure burstable, 10 per basket: 100 of envelope → 10.
	got := Fit(all(st), map[string]BasketDemand{"vcpu": {Burstable: r(10)}})
	if got.Units.Int64() != 10 {
		t.Errorf("burstable-only fit = %v, want 10", got.Units)
	}
	// Pure guaranteed, 10 per basket: 6 inside the floor for free, then each
	// costs 25 of a 100 envelope → 4 more. 10 in all, the whole machine.
	got = Fit(all(st), map[string]BasketDemand{"vcpu": {Guaranteed: r(10)}})
	if got.Units.Int64() != 10 {
		t.Errorf("guaranteed-only fit = %v, want 10", got.Units)
	}
	// Mixed 10 G + 10 B per basket. Inside the floor burstable binds at 10
	// baskets but the floor is used up at 6, so past it: (100·2.5 − 0)/(10 + 25) = 7.14 → 7.
	got = Fit(all(st), map[string]BasketDemand{"vcpu": {Guaranteed: r(10), Burstable: r(10)}})
	if got.Units.Int64() != 7 {
		t.Errorf("mixed fit = %v, want 7", got.Units)
	}
	// Burstable already past its envelope: a guaranteed-only basket still
	// runs to the edge of the floor, because landing in its own room
	// throttles nothing further — and stops there.
	st.Burstable = r(150)
	got = Fit(all(st), map[string]BasketDemand{"vcpu": {Guaranteed: r(10)}})
	if got.Units.Int64() != 6 {
		t.Errorf("guaranteed fit with burstable overrun = %v, want 6 (the floor)", got.Units)
	}
	got = Fit(all(st), map[string]BasketDemand{"vcpu": {Burstable: r(1)}})
	if got.Units.Int64() != 0 {
		t.Errorf("burstable fit with burstable overrun = %v, want 0", got.Units)
	}
}

// With no floor the piecewise line must be the old single division, to the
// unit — every pool that exists today has no floor.
func TestFitWithNoFloorIsTheOldFormula(t *testing.T) {
	st := ResourceState{Machines: r(4), PerMachine: r(64), Reserve: r(64), Ratio: r(4), Guaranteed: r(40), Burstable: r(100)}
	m := Compute(st)
	for _, d := range []BasketDemand{{Guaranteed: r(8)}, {Burstable: r(8)}, {Guaranteed: r(2), Burstable: r(6)}} {
		g, b := nz(d.Guaranteed), nz(d.Burstable)
		old := floorRat(new(big.Rat).Quo(m.Remaining, new(big.Rat).Add(b, new(big.Rat).Mul(g, m.Ratio))))
		got := Fit(map[string]ResourceMath{"vcpu": m}, map[string]BasketDemand{"vcpu": d})
		if got.Units.Cmp(old) != 0 {
			t.Errorf("demand %+v: fit %v, old formula %v", d, got.Units, old)
		}
	}
}

func TestSoftWallWaitsForTheFloorToFill(t *testing.T) {
	m := Compute(ResourceState{Machines: r(1), PerMachine: r(100), Ratio: rs(t, "2.5"), Floor: r(60), Guaranteed: r(20), Burstable: r(50)})
	g := 2.0
	// Guaranteed grows 2/day, burstable not at all. 20 days fill the floor at
	// no cost to the envelope; after that (80·2.5 − 50)/(2·2.5) = 30 days.
	w := Project(m, &g, nil, 0)
	if w.Soft == nil || *w.Soft != 30 {
		t.Fatalf("soft wall = %v, want 30", w.Soft)
	}
	if w.Hard == nil || *w.Hard != 40 {
		t.Fatalf("hard wall = %v, want 40", w.Hard)
	}
	b := 5.0
	// Burstable alone: 50 of room at 5/day.
	w = Project(m, nil, &b, 0)
	if w.Soft == nil || *w.Soft != 10 {
		t.Fatalf("burstable-only soft wall = %v, want 10", w.Soft)
	}
}
