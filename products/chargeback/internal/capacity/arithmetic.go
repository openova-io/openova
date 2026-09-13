package capacity

import (
	"math"
	"math/big"
)

// The arithmetic of a pool. Everything here is exact rational arithmetic on
// *big.Rat, except the ratios and the day counts, which are estimates and
// have no exact form.
//
// THE FORMULA, per pool, per resource:
//
//	raw      = machines × per_machine
//	usable   = raw − reserve
//	G        = Σ guaranteed placements' consumption, AT 1:1, ALWAYS
//	sellable = G + (usable − G) × ratio
//
// IT IS NOT usable × ratio − G. Guaranteed capacity consumes PHYSICAL
// capacity; only what physically remains is multiplied. Every guaranteed unit
// sold therefore removes ratio × worth of oversubscribed room — which is what
// makes the guarantee real rather than a label.
// TestSellableIsNotUsableTimesRatioMinusG pins the two apart.
//
// ADMISSION. A guaranteed order is accepted only if it fits `usable` at 1:1
// (GuaranteedCeiling): never sell a guaranteed unit that is not physically
// backed. A burstable order is accepted while the nominal sold (G + B) is
// within `sellable` — equivalently while Remaining covers it.
//
// SPOT IS EXCLUDED FROM ADMISSION ACCOUNTING ON BOTH SIDES. It holds no
// reservation, so it appears neither in G nor in the nominal sold, and a
// guaranteed or burstable order is never refused on account of it. Its own
// room is what physically remains after the other two:
//
//	physical_free = usable − (G + B/ratio)
//	spot_room     = physical_free × ratio      (= Remaining; see below)
//	spot_reclaim  = spot − spot_room, floored at 0
//
// When that room shrinks the excess is reclaimable. BSS decides HOW MUCH must
// be freed; the platform decides WHICH instances — no eviction is implemented
// here, only the requirement is recorded.
//
// ONE IDENTITY WORTH KNOWING, because it collapses two constraints into one:
//
//	Remaining = sellable − (G + B) = (usable − G)·ratio − B
//	          = (usable − G − B/ratio)·ratio = physical_free × ratio
//
// The nominal envelope and the physical hardware run out at the same instant.
// That is why a basket's fit needs only ONE division (Fit, below) and why the
// soft wall is also the moment the hardware is fully committed.

// ResourceState is one (pool, resource): the hardware, the policy and what is
// running on it. Every field is a rational; nil reads as zero, except Ratio,
// where nil reads as 1 (no oversubscription).
type ResourceState struct {
	// Machines is the pool's machine count, PerMachine the amount of this
	// resource in ONE machine. Raw is their product: a pool is a set of
	// identical machines, and "add two servers" is a change to Machines.
	Machines   *big.Rat
	PerMachine *big.Rat
	// Reserve is redundancy and maintenance headroom held back, in the
	// resource's own units — N+1 is one machine's worth.
	Reserve *big.Rat
	// Ratio is the overcommit ratio of THIS resource on THIS pool. vCPU may
	// run 4:1 while RAM on the same machines runs 1:1 or 1.5:1 with
	// ballooning; a ratio is never global.
	Ratio *big.Rat
	// Guaranteed, Burstable and Spot are the current consumption of this
	// resource by class, in NOMINAL units — what was sold, before any
	// oversubscription is undone.
	Guaranteed *big.Rat
	Burstable  *big.Rat
	Spot       *big.Rat
}

// ResourceMath is everything derived from a ResourceState. Every field is
// non-nil whatever the input.
type ResourceMath struct {
	Raw      *big.Rat
	Usable   *big.Rat
	Ratio    *big.Rat
	Sellable *big.Rat

	// Guaranteed is physical and nominal at once (guaranteed is 1:1).
	Guaranteed *big.Rat
	// Burstable and Spot are nominal; their Physical siblings are those
	// divided by the ratio — the hardware they actually occupy at their
	// planned share.
	Burstable         *big.Rat
	BurstablePhysical *big.Rat
	Spot              *big.Rat
	SpotPhysical      *big.Rat

	// SoldNominal is G + B. Spot is not sold capacity and is not in it.
	SoldNominal *big.Rat
	// Remaining is sellable − SoldNominal, floored at 0: how much more
	// nominal capacity this pool can take before anything is throttled or
	// reclaimed. It is what the basket headroom divides.
	Remaining *big.Rat
	// GuaranteedCeiling is usable − G, floored at 0: the ADMISSION test for a
	// guaranteed order, at 1:1, against nothing else. Burstable is throttled
	// and spot is reclaimed to make room, so neither can refuse a guarantee;
	// this is the quantity the hard wall closes on.
	GuaranteedCeiling *big.Rat

	// PhysicalUsed is G + B/ratio — the hardware the admitted classes hold at
	// their planned share. PhysicalFree is usable − PhysicalUsed, floored at
	// 0. When another resource binds, that free hardware is STRANDED: it
	// cannot be sold, because every unit sold also needs the resource that
	// ran out.
	PhysicalUsed *big.Rat
	PhysicalFree *big.Rat

	// SpotRoom is the nominal spot that fits in PhysicalFree (= Remaining);
	// SpotReclaim is how much nominal spot is over it and must be freed.
	SpotRoom    *big.Rat
	SpotReclaim *big.Rat

	// Sized is false when the resource carries no capacity at all (no
	// machines, or nothing per machine). UtilisationPct is nil then — a
	// percentage of nothing would render as good news.
	Sized          bool
	UtilisationPct *float64
	Status         string
	// Overcommitted is true when SoldNominal already exceeds sellable: the
	// pool is past its envelope, not merely near it. Over is by how much.
	Overcommitted bool
	Over          *big.Rat
}

func nz(r *big.Rat) *big.Rat {
	if r == nil {
		return new(big.Rat)
	}
	return new(big.Rat).Set(r)
}

func ratioOf(r *big.Rat) *big.Rat {
	if r == nil || r.Sign() <= 0 {
		return big.NewRat(1, 1)
	}
	return new(big.Rat).Set(r)
}

func floor0(r *big.Rat) *big.Rat {
	if r.Sign() < 0 {
		return new(big.Rat)
	}
	return r
}

// Compute derives a resource's figures from its state.
func Compute(s ResourceState) ResourceMath {
	machines, per := nz(s.Machines), nz(s.PerMachine)
	rat := ratioOf(s.Ratio)
	g, b, sp := nz(s.Guaranteed), nz(s.Burstable), nz(s.Spot)

	raw := new(big.Rat).Mul(machines, per)
	usable := floor0(new(big.Rat).Sub(raw, nz(s.Reserve)))

	// sellable = G + (usable − G) × ratio. The guaranteed part is physical
	// and is NOT multiplied; only the physical remainder is. Guaranteed past
	// usable leaves no remainder to multiply, so the bracket floors at 0 and
	// sellable is G itself — which is the state "oversold on the guarantee",
	// not "there is still oversubscribed room".
	ceiling := floor0(new(big.Rat).Sub(usable, g))
	sellable := new(big.Rat).Add(g, new(big.Rat).Mul(ceiling, rat))

	bPhys := new(big.Rat).Quo(b, rat)
	spPhys := new(big.Rat).Quo(sp, rat)

	sold := new(big.Rat).Add(g, b)
	remaining := floor0(new(big.Rat).Sub(sellable, sold))

	physUsed := new(big.Rat).Add(g, bPhys)
	physFree := floor0(new(big.Rat).Sub(usable, physUsed))

	// SpotRoom is PhysicalFree × ratio, which is Remaining exactly (see the
	// identity in the package comment); written as the product so the reason
	// it is that number stays visible.
	spotRoom := new(big.Rat).Mul(physFree, rat)
	spotReclaim := floor0(new(big.Rat).Sub(sp, spotRoom))

	out := ResourceMath{
		Raw: raw, Usable: usable, Ratio: rat, Sellable: sellable,
		Guaranteed: g, Burstable: b, BurstablePhysical: bPhys, Spot: sp, SpotPhysical: spPhys,
		SoldNominal: sold, Remaining: remaining, GuaranteedCeiling: ceiling,
		PhysicalUsed: physUsed, PhysicalFree: physFree,
		SpotRoom: spotRoom, SpotReclaim: spotReclaim,
		Over: new(big.Rat),
	}
	out.Sized = raw.Sign() > 0
	switch {
	case !out.Sized:
		// No capacity at all: no percentage. An unsized resource NEVER reads
		// ok — a figure read from a structurally empty field would render as
		// good news.
	case sellable.Sign() > 0:
		pct, _ := new(big.Rat).Quo(sold, sellable).Float64()
		pct *= 100
		out.UtilisationPct = &pct
	default:
		// Sized, but the reserve took all of it: nothing is sellable, which
		// is full, not empty.
		full := 100.0
		out.UtilisationPct = &full
	}
	if sold.Cmp(sellable) > 0 {
		out.Overcommitted = true
		out.Over = new(big.Rat).Sub(sold, sellable)
	}
	pct := 0.0
	if out.UtilisationPct != nil {
		pct = *out.UtilisationPct
	}
	out.Status = Status(pct, out.Sized)
	return out
}

// AdmitGuaranteed reports whether `amount` more of a guaranteed shape is
// physically backed: it must fit usable at 1:1, and nothing else refuses it —
// burstable is throttled and spot is reclaimed to make the room.
func AdmitGuaranteed(m ResourceMath, amount *big.Rat) bool {
	return nz(amount).Cmp(m.GuaranteedCeiling) <= 0
}

// AdmitBurstable reports whether `amount` more nominal burstable fits within
// the envelope. Spot is on neither side of this test.
func AdmitBurstable(m ResourceMath, amount *big.Rat) bool {
	return nz(amount).Cmp(m.Remaining) <= 0
}

// ---------------------------------------------------------------------------
// the binding resource
// ---------------------------------------------------------------------------

// Binding is the resource of a pool whose remaining room is smallest, as a
// FRACTION of what that resource could sell. Resources are measured in
// different units — 768 vCPU and 0 GiB cannot be compared as numbers — so
// the comparison has to be dimensionless.
//
// The result is the resource the pool runs out of FIRST, and it is the main
// procurement signal: a pool can be RAM-bound with a third of its vCPU
// stranded and unsellable, and that case must read correctly rather than
// average away.
//
// Unsized resources are skipped (they carry no information); "" when none is
// sized.
func Binding(all map[string]ResourceMath) string {
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	SortResources(keys)
	best, out := 0.0, ""
	for _, k := range keys {
		m := all[k]
		if !m.Sized {
			continue
		}
		frac := 0.0
		if m.Sellable.Sign() > 0 {
			frac, _ = new(big.Rat).Quo(m.Remaining, m.Sellable).Float64()
		}
		if out == "" || frac < best {
			best, out = frac, k
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// basket headroom
// ---------------------------------------------------------------------------

// BasketDemand is what ONE basket adds to one resource, split by class. All
// three are NOMINAL amounts of the resource.
type BasketDemand struct {
	Guaranteed *big.Rat
	Burstable  *big.Rat
	Spot       *big.Rat
}

// BasketFit is how many more of a basket fit on a pool.
type BasketFit struct {
	// Units is nil when NOTHING could be measured: no resource the basket
	// touches is sized on this pool, or the basket asks for nothing. A basket
	// with no sized resource must SAY so, never show a number.
	Units *big.Int
	// Binding names the resource that produced Units.
	Binding string
	// Per is, per resource, how many baskets that resource alone allows —
	// absent for a resource the basket does not touch or the pool has not
	// sized. It is a diagnostic, never an answer on its own: the answer is
	// Units, the minimum over Per.
	Per map[string]*big.Int
}

// Fit is how many more whole baskets fit, and which resource binds.
//
// Per resource, with g, b, s the basket's per-basket nominal demand by class
// and k baskets:
//
//	envelope   (G + k·g) + (B + k·b) ≤ sellable(G + k·g)
//	           → k·(b + g·ratio) ≤ (usable − G)·ratio − B = Remaining
//	           → k ≤ Remaining / (b + g·ratio)
//	spot room  S + k·s ≤ SpotRoom      → k ≤ (SpotRoom − S)/s
//
// The first line is BOTH the nominal envelope and the physical hardware:
// Remaining = PhysicalFree × ratio, so dividing by (b + g·ratio) is the same
// as dividing PhysicalFree by (g + b/ratio). A guaranteed unit costs g·ratio
// of the envelope, not g, because it takes g physically AND removes
// g·(ratio−1) of oversubscribed room — that is the whole point of the
// guarantee, expressed as a division.
//
// The second line is the only place spot appears, and it constrains SPOT
// ONLY: a basket with no spot in it is never limited by spot, and spot never
// limits the other two.
func Fit(all map[string]ResourceMath, demand map[string]BasketDemand) BasketFit {
	out := BasketFit{Per: map[string]*big.Int{}}
	keys := make([]string, 0, len(demand))
	for k := range demand {
		keys = append(keys, k)
	}
	SortResources(keys)
	var best *big.Int
	for _, res := range keys {
		m, ok := all[res]
		if !ok || !m.Sized {
			continue
		}
		d := demand[res]
		g, b, s := nz(d.Guaranteed), nz(d.Burstable), nz(d.Spot)
		var limit *big.Int
		take := func(num, den *big.Rat) {
			if den.Sign() <= 0 {
				return
			}
			k := floorRat(new(big.Rat).Quo(floor0(num), den))
			if limit == nil || k.Cmp(limit) < 0 {
				limit = k
			}
		}
		take(new(big.Rat).Set(m.Remaining), new(big.Rat).Add(b, new(big.Rat).Mul(g, m.Ratio)))
		if s.Sign() > 0 {
			take(new(big.Rat).Sub(m.SpotRoom, m.Spot), s)
		}
		if limit == nil {
			continue
		}
		out.Per[res] = limit
		if best == nil || limit.Cmp(best) < 0 {
			best, out.Binding = limit, res
		}
	}
	out.Units = best
	return out
}

// floorRat is the integer floor of a rational, floored at 0.
func floorRat(r *big.Rat) *big.Int {
	if r.Sign() <= 0 {
		return new(big.Int)
	}
	return new(big.Int).Quo(r.Num(), r.Denom())
}

// ---------------------------------------------------------------------------
// the walls and the date that matters
// ---------------------------------------------------------------------------

// Walls are the two dates a pool's resource is heading for, in days from the
// measurement hour, plus the lead time before them.
type Walls struct {
	// Soft is when the projected total (guaranteed + burstable) reaches
	// `sellable` — equivalently when the hardware is fully committed, since
	// Remaining = PhysicalFree × ratio. Spot starts being reclaimed and
	// burstable starts throttling. It is never later than Hard.
	Soft *float64
	// Hard is when projected GUARANTEED alone reaches `usable`. Buy
	// hardware; no policy avoids it, because there is nothing left to
	// throttle or reclaim.
	Hard *float64
	// Days is the nearer of the two, Wall which one it was.
	Days *float64
	Wall string
	// OrderBy is Days − lead_time_days. THIS is the date that matters: an
	// alert keyed on the wall itself fires too late by construction, by
	// exactly the procurement lead time.
	OrderBy *float64
}

// Wall kinds.
const (
	WallSoft = "soft"
	WallHard = "hard"
)

// Project computes the walls of one resource from the growth of each class,
// in nominal resource units per day.
//
//	soft: B + b·t = (usable − G − g·t)·ratio  → t = Remaining/(b + g·ratio)
//	hard: G + g·t = usable                    → t = GuaranteedCeiling/g
//
// The soft wall moves when GUARANTEED grows, not only when burstable does:
// selling a guaranteed unit removes ratio × worth of oversubscribed room, so
// guaranteed growth pulls the soft wall in faster than its own size. With no
// burstable at all the soft wall is Remaining/(g·ratio) = PhysicalFree/g.
//
// Spot growth is not an input: spot is reclaimed, not walled.
//
// A wall is nil when nothing that would reach it is growing, or when the
// resource is not sized. Days beyond a century are nil too: "runs out in
// 4,000 years" is arithmetic, not a plan.
func Project(m ResourceMath, growthGuaranteed, growthBurstable *float64, leadTimeDays int) Walls {
	var out Walls
	if !m.Sized {
		return out
	}
	g, b := deref(growthGuaranteed), deref(growthBurstable)
	rat, _ := m.Ratio.Float64()

	if den := b + g*rat; den > 0 {
		room, _ := m.Remaining.Float64()
		out.Soft = days(room / den)
	}
	if g > 0 {
		room, _ := m.GuaranteedCeiling.Float64()
		out.Hard = days(room / g)
	}
	switch {
	case out.Soft != nil && out.Hard != nil:
		if *out.Soft <= *out.Hard {
			out.Days, out.Wall = out.Soft, WallSoft
		} else {
			out.Days, out.Wall = out.Hard, WallHard
		}
	case out.Soft != nil:
		out.Days, out.Wall = out.Soft, WallSoft
	case out.Hard != nil:
		out.Days, out.Wall = out.Hard, WallHard
	}
	if out.Days != nil {
		d := *out.Days - float64(leadTimeDays)
		out.OrderBy = &d
	}
	return out
}

// maxProjectionDays is how far ahead a wall is worth reporting: beyond a
// century the trend is arithmetic, not information.
const maxProjectionDays = 365 * 100

// days rounds a day count to one decimal, or nil when it is not a number or
// is beyond the horizon.
func days(d float64) *float64 {
	if math.IsNaN(d) || math.IsInf(d, 0) || d < 0 || d > maxProjectionDays {
		return nil
	}
	r := math.Round(d*10) / 10
	return &r
}

func deref(p *float64) float64 {
	if p == nil || math.IsNaN(*p) || math.IsInf(*p, 0) {
		return 0
	}
	return *p
}
