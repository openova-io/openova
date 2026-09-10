package synth

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// The landlord backfill (founder direction 2026-09-10).
//
// The six showcase customers above were the ONLY history on hw307, and they
// stop on 1 September while the real collection begins at 2026-09-02
// 10:21:09Z. Two things were wrong with that, both visible on the overview
// chart the founder was looking at:
//
//  1. A HOLE. Nothing at all was written for 1 September or the morning of
//     2 September, so the daily series had an empty bucket in the middle —
//     "step 1st is empty".
//  2. NO CONTINUITY. The Sovereign's own landlord customer (Omantel, the real
//     Huawei project) had no past whatsoever, so the chart jumped from about
//     150 OMR a day of showcase customers straight to about 594 OMR a day of
//     real usage with an entirely different service mix — "the actual usage
//     was already there from the beginning, you failed to show the
//     continuity".
//
// The fix is to give the landlord a synthetic past that CONVERGES on its real
// present. The end state below is the measured shape of the landlord's real
// cloud-layer usage on hw307 (sampled 5 September 2026), and the backfill
// grows into exactly that shape and then stops one hour before the first real
// record. Read left to right the series now says: the Sovereign has been
// running since June, six customers were on it, they left at the end of
// August, and the platform carries on.
//
// Everything here is a RESERVATION, not traffic. An EIP is billed for the
// bandwidth it has provisioned whether or not a byte flows through it — which
// is why bandwidth is 494 of the 594 OMR a day — so the bandwidth quantity is
// constant per EIP per hour and steps only when an EIP is added. The same
// holds for the EIP itself, the NAT gateways, the load balancers, the
// instance-hours and each volume's size. Jittering any of them would make the
// data contradict the billing model it is meant to illustrate. The one thing
// that moves is the EVS TOTAL, and it moves because volumes are created, not
// because a size wobbles.

// LandlordDefaultSlug is the customer the backfill attaches to unless
// --landlord names another: the Sovereign's own landlord on hw307. It is a
// REAL customer — the seeding command finds it, never creates or edits it,
// and IsSyntheticSlug is false for it so no purge can ever remove it.
const LandlordDefaultSlug = "hw307-omani-works"

// LandlordSourceSuffix names the synthetic source the backfill writes to. The
// rows live on the SAME customer (so the explorer grouped by customer shows
// one continuous series) but on their OWN source, so a purge removes exactly
// the backfill and never touches the real ledger.
const LandlordSourceSuffix = "-history"

// LandlordSourceName is the project_id/name of the synthetic source for a
// landlord slug: demo-<slug>-history.
func LandlordSourceName(slug string) string {
	return SlugPrefix + strings.TrimSpace(strings.ToLower(slug)) + LandlordSourceSuffix
}

// Story dates. Like the showcase above these are fixed; the window clips what
// is generated, it does not move the story.
var (
	// LandlordStoryStart is when the landlord's synthetic past begins — the
	// same 1 June the showcase customers begin, so the two series start
	// together.
	LandlordStoryStart = date(2026, 6, 1, 0)
	// LandlordStep1 and LandlordStep2 are the two growth steps: the first
	// pair of instances and the fifth EIP arrive on 1 July, the second pair
	// and the sixth EIP on 1 August. From LandlordStep2 the compute and
	// network shape is already the converged one.
	LandlordStep1 = date(2026, 7, 1, 0)
	LandlordStep2 = date(2026, 8, 1, 0)
	// LandlordEVSGrowthEnd is when the last volume is provisioned. Storage
	// keeps growing after the compute steps — that is what a live platform
	// looks like — but it settles before the join so the final days of the
	// backfill are exactly the measured end state.
	LandlordEVSGrowthEnd = date(2026, 8, 20, 0)

	// LandlordFirstRealRecord is the first REAL usage row on hw307: the
	// Sovereign was provisioned that morning. No synthetic row may start at
	// or after it.
	LandlordFirstRealRecord = time.Date(2026, 9, 2, 10, 21, 9, 0, time.UTC)
	// LandlordDefaultUntil is the hour boundary before it — the exclusive end
	// of the backfill, so the last hour written is 09:00 on 2 September. The
	// seeding command DISCOVERS this from the customer's own earliest
	// non-synthetic record; this constant is the measured fallback used by
	// --dry-run, which has no database to ask.
	LandlordDefaultUntil = LandlordFirstRealRecord.Truncate(time.Hour)
)

// The converged end state, measured on hw307 on 5 September 2026. Every
// number below is per HOUR.
const (
	LandlordECSLargeSKU = "ecs.m7n.2xlarge.8"
	LandlordECSSmallSKU = "ecs.m7n.xlarge.8"

	// LandlordECSLargeStart / Mid / End — 6 through June, 8 from 1 July,
	// 10 from 1 August.
	LandlordECSLargeStart = 6
	LandlordECSLargeMid   = 8
	LandlordECSLargeEnd   = 10
	// LandlordECSSmallCount, LandlordNATCount and LandlordELBCount never
	// change across the window.
	LandlordECSSmallCount = 2
	LandlordNATCount      = 2
	LandlordELBCount      = 2

	// EIP bandwidth is provisioned size, in Mbps. Two 300 and two 100 at the
	// start; one 100 added on 1 July and one 300 on 1 August, reaching
	// 3 × 300 + 3 × 100 = 1,200 Mbps across six EIPs.
	LandlordEIPCountStart = 4
	LandlordEIPCountEnd   = 6
	LandlordEIPMbpsStart  = 800
	LandlordEIPMbpsEnd    = 1200

	// EVS: the total grows over a resource count growing 70 → 102, each
	// volume a constant whole number of GB.
	LandlordEVSCountStart = 70
	LandlordEVSCountEnd   = 102
	LandlordEVSGBStart    = 1400
	LandlordEVSGBEnd      = 2281

	// LandlordMeasuredDayOMR is what one real day of that shape costs on the
	// National Cloud list: 2026-09-03 through 09-06 each rated ≈594.1 OMR, of
	// which ≈494 is EIP bandwidth. TestLandlordSeamMatchesTheMeasuredRealDay
	// prices the backfill's last full day with this package's own rate table
	// and requires it within 2 % of this number — that is the seam, and it is
	// the assertion that fails if the end state ever drifts off the measured
	// shape.
	//
	// The tolerance is not slack, it is a known and measured gap. This
	// package's NationalCloudRates are the ones the hw307 book rated the
	// August 2026 statement with, where nat.1 is 0.11322489 an hour; the
	// operator's own card on that Sovereign prices the same SKU at
	// 0.06037935, which is 2.54 OMR a day across two gateways. Priced here
	// the day is 596.63, priced on the operator's card 594.09 — and 594.09 is
	// the number the console shows, because the seeding command resolves the
	// operator's card and never mints its own (cmd/seed-history/book.go).
	// Verified end to end: the backfill's last day and the first real day
	// both 594.09, a 0.00 % seam.
	LandlordMeasuredDayOMR = 594.1
	// LandlordSeamTolerance is that 2 %.
	LandlordSeamTolerance = 0.02
)

// SKUShape is one metered SKU of the end state: how many resources report it
// in one hour and what they report in total.
type SKUShape struct {
	SKU      string
	Unit     string
	Count    int
	Quantity float64
}

// LandlordEndState is the measured shape the backfill converges on, ascending
// by SKU. TestLandlordEndStateMatchesTheMeasuredShape asserts the generated
// final hour equals it exactly.
var LandlordEndState = []SKUShape{
	{SKU: "ecs.m7n.2xlarge.8", Unit: "instance-hour", Count: LandlordECSLargeEnd, Quantity: LandlordECSLargeEnd},
	{SKU: "ecs.m7n.xlarge.8", Unit: "instance-hour", Count: LandlordECSSmallCount, Quantity: LandlordECSSmallCount},
	{SKU: "eip", Unit: "hour", Count: LandlordEIPCountEnd, Quantity: LandlordEIPCountEnd},
	{SKU: "eip.bandwidth_mbps", Unit: "mbps-hour", Count: LandlordEIPCountEnd, Quantity: LandlordEIPMbpsEnd},
	{SKU: "elb", Unit: "hour", Count: LandlordELBCount, Quantity: LandlordELBCount},
	{SKU: "evs.ssd.gb", Unit: "gb-hour", Count: LandlordEVSCountEnd, Quantity: LandlordEVSGBEnd},
	{SKU: "nat.1", Unit: "hour", Count: LandlordNATCount, Quantity: LandlordNATCount},
}

// landlordEIP is one Elastic IP of the landlord: a provisioned size in Mbps,
// the region it lives in, when it was created and whether it fronts a load
// balancer (the two region gateways do).
type landlordEIP struct {
	Mbps   float64
	Region string
	From   time.Time
	Status string
}

// LandlordEIPs is the EIP roster, in creation order. The first four exist
// from the start; the fifth arrives on LandlordStep1 and the sixth on
// LandlordStep2.
var LandlordEIPs = []landlordEIP{
	{Mbps: 300, Region: RegionA, Status: "ELB"},
	{Mbps: 300, Region: RegionB, Status: "ELB"},
	{Mbps: 100, Region: RegionA, Status: "ACTIVE"},
	{Mbps: 100, Region: RegionB, Status: "ACTIVE"},
	{Mbps: 100, Region: RegionA, From: LandlordStep1, Status: "ACTIVE"},
	{Mbps: 300, Region: RegionB, From: LandlordStep2, Status: "ACTIVE"},
}

// LandlordScenario builds the one-customer backfill for an EXISTING landlord
// customer: its synthetic past over [from, until), converging on
// LandlordEndState. until is exclusive and must be a whole hour strictly
// before the customer's first real record.
func LandlordScenario(slug string, from, until time.Time, seed uint64) (*Scenario, error) {
	slug = strings.TrimSpace(strings.ToLower(slug))
	if slug == "" {
		return nil, fmt.Errorf("landlord slug is empty")
	}
	if IsSyntheticSlug(slug) {
		return nil, fmt.Errorf("landlord slug %q carries the %q prefix, which marks a showcase customer a purge deletes; the landlord is a real customer", slug, SlugPrefix)
	}
	w := Window{From: from.UTC(), To: until.UTC()}
	if err := w.Validate(); err != nil {
		return nil, err
	}
	s := &Scenario{Window: w, Seed: seed, CloudBookName: CloudBookName, PlanBookName: PlanBookName}
	s.Customers = []*Customer{landlordCustomer(slug, until, seed)}
	return s, nil
}

// Landlord returns the backfill customer of a landlord scenario.
func (s *Scenario) Landlord() *Customer {
	if len(s.Customers) == 1 && s.Customers[0].Backfill {
		return s.Customers[0]
	}
	return nil
}

// landlordCustomer describes the existing customer well enough to generate
// for it. Billing mode and price book are deliberately absent: the seeding
// command reads them off the live customer and its real source, and never
// writes any of them back.
func landlordCustomer(slug string, until time.Time, seed uint64) *Customer {
	c := &Customer{
		Slug:  slug,
		Name:  slug,
		Layer: LayerCloud,
		Kind:  "external",
		Source: Source{
			Name: LandlordSourceName(slug), Kind: SourceKindFile,
			Layer: LayerCloud, Region: RegionA,
		},
		Joined:   LandlordStoryStart,
		Left:     until.UTC(),
		Backfill: true,
	}
	b := builder{seed, slug, c.Left}
	short := landlordShortName(slug)
	c.Resources = append(c.Resources, landlordECS(b, short)...)
	c.Resources = append(c.Resources, landlordEIPSpecs(b)...)
	c.Resources = append(c.Resources, landlordEVS(b, short, seed, slug)...)
	c.Resources = append(c.Resources, landlordFixed(b, short)...)
	return c
}

// landlordShortName is the first label of the slug ("hw307-omani-works" →
// "hw307"), which is how the provisioner names the Sovereign's own machines.
func landlordShortName(slug string) string {
	if i := strings.IndexByte(slug, '-'); i > 0 {
		return slug[:i]
	}
	return slug
}

// landlordRegion alternates the two National Cloud regions so every count in
// the growth curve (6, 8, 10) stays balanced across them.
func landlordRegion(idx int) string {
	if idx%2 == 1 {
		return RegionB
	}
	return RegionA
}

// landlordECS is the compute roster: LandlordECSLargeEnd m7n.2xlarge.8 nodes
// arriving 6 / 8 / 10 across the two steps, plus LandlordECSSmallCount
// m7n.xlarge.8 edge nodes present throughout.
//
// The `az` attribute is the region string, which is what the collector
// records on hw307; it is not the azOf() form the showcase customers use.
func landlordECS(b builder, short string) []ResourceSpec {
	out := make([]ResourceSpec, 0, LandlordECSLargeEnd+LandlordECSSmallCount)
	for i := 0; i < LandlordECSLargeEnd; i++ {
		from := time.Time{}
		switch {
		case i >= LandlordECSLargeMid:
			from = LandlordStep2
		case i >= LandlordECSLargeStart:
			from = LandlordStep1
		}
		region := landlordRegion(i)
		name := fmt.Sprintf("%s-%s-node-%02d", short, region, i/2+1)
		out = append(out, landlordInstance(b, name, LandlordECSLargeSKU, "m7n.2xlarge.8", 8, 65536, region, from))
	}
	for i := 0; i < LandlordECSSmallCount; i++ {
		region := landlordRegion(i)
		name := fmt.Sprintf("%s-%s-gw-%02d", short, region, i/2+1)
		out = append(out, landlordInstance(b, name, LandlordECSSmallSKU, "m7n.xlarge.8", 4, 32768, region, time.Time{}))
	}
	return out
}

// landlordInstance is one ECS billed at exactly one instance-hour per hour it
// exists — a reservation, never jittered.
func landlordInstance(b builder, name, sku, flavor string, vcpus, ramMB int, region string, from time.Time) ResourceSpec {
	return ResourceSpec{
		ID: b.id("ecs", name), Kind: "ecs", Name: name, Region: region, From: from,
		Attrs:  map[string]any{"flavor": flavor, "vcpus": vcpus, "ram_mb": ramMB, "status": "ACTIVE", "az": region},
		Labels: map[string]string{"flavor": flavor, "az": region, "status": "ACTIVE"},
		Meter: func(time.Time, Jitter) []Line {
			return []Line{{SKU: sku, Unit: "instance-hour", Quantity: 1}}
		},
	}
}

// landlordEIPSpecs turns the roster into resources. Each EIP bills one hour
// of itself and its PROVISIONED bandwidth every hour — constant, stepping
// only when an EIP is added, because a reserved pipe is billed whether or not
// traffic flows through it.
func landlordEIPSpecs(b builder) []ResourceSpec {
	out := make([]ResourceSpec, 0, len(LandlordEIPs))
	for i, e := range LandlordEIPs {
		key := fmt.Sprintf("eip-%02d", i+1)
		ip := b.omanIP(key)
		mbps := e.Mbps
		out = append(out, ResourceSpec{
			ID: b.id("eip", key), Kind: "eip", Name: ip, Region: e.Region, From: e.From,
			Attrs: map[string]any{
				"bandwidth_mbps": mbps, "public_ip_address": ip, "type": "5_bgp",
				"status": e.Status, "enterprise_project": "0",
			},
			Labels: map[string]string{"status": e.Status, "enterprise_project": "0"},
			Meter: func(time.Time, Jitter) []Line {
				return []Line{
					{SKU: "eip", Unit: "hour", Quantity: 1},
					{SKU: "eip.bandwidth_mbps", Unit: "mbps-hour", Quantity: mbps},
				}
			},
		})
	}
	return out
}

// landlordFixed is the per-region NAT gateways and load balancers: one unit
// an hour each, present for the whole window.
func landlordFixed(b builder, short string) []ResourceSpec {
	var out []ResourceSpec
	for i := 0; i < LandlordNATCount; i++ {
		region := landlordRegion(i)
		name := fmt.Sprintf("%s-%s-nat", short, region)
		out = append(out, landlordUnit(b, "nat", name, "nat.1", region, map[string]any{"spec": "1"}))
	}
	for i := 0; i < LandlordELBCount; i++ {
		region := landlordRegion(i)
		name := fmt.Sprintf("%s-%s-elb", short, region)
		out = append(out, landlordUnit(b, "elb", name, "elb", region, nil))
	}
	return out
}

func landlordUnit(b builder, kind, name, sku, region string, attrs map[string]any) ResourceSpec {
	a := map[string]any{"status": "ACTIVE", "az": region}
	for k, v := range attrs {
		a[k] = v
	}
	return ResourceSpec{
		ID: b.id(kind, name), Kind: kind, Name: name, Region: region, Attrs: a,
		Meter: func(time.Time, Jitter) []Line {
			return []Line{{SKU: sku, Unit: "hour", Quantity: 1}}
		},
	}
}

// LandlordEVSPlan is the storage roster: one entry per volume, in creation
// order, with its constant size in whole GB and the hour it was provisioned
// (zero = present from the start).
type LandlordEVSPlan struct {
	SizeGB int
	At     time.Time
	Region string
}

// landlordMinVolumeGB is the smallest EVS volume the platform provisions.
const landlordMinVolumeGB = 10

// LandlordEVSRoster builds the storage plan for a seed and slug.
//
// A volume's size is provisioned, exactly like bandwidth, so it does not
// drift: what drifts is the TOTAL, and it drifts because volumes are created.
// The first LandlordEVSCountStart volumes exist from the start and sum to
// exactly LandlordEVSGBStart; the remaining ones are provisioned one at a
// time so that the running total tracks a straight line from
// LandlordEVSGBStart to LandlordEVSGBEnd across
// [LandlordStoryStart, LandlordEVSGrowthEnd), reaching LandlordEVSGBEnd over
// LandlordEVSCountEnd volumes before the window ends.
func LandlordEVSRoster(seed uint64, slug string) []LandlordEVSPlan {
	base := apportion(LandlordEVSGBStart, landlordWeights(seed, slug, "evs-base", LandlordEVSCountStart), landlordMinVolumeGB)
	grow := apportion(LandlordEVSGBEnd-LandlordEVSGBStart, landlordWeights(seed, slug, "evs-grow", LandlordEVSCountEnd-LandlordEVSCountStart), landlordMinVolumeGB)

	out := make([]LandlordEVSPlan, 0, LandlordEVSCountEnd)
	for i, gb := range base {
		out = append(out, LandlordEVSPlan{SizeGB: gb, Region: landlordRegion(i)})
	}
	span := float64(LandlordEVSGrowthEnd.Sub(LandlordStoryStart))
	added := float64(LandlordEVSGBEnd - LandlordEVSGBStart)
	cum := 0.0
	for i, gb := range grow {
		// The volume is provisioned when the straight line reaches the
		// MIDPOINT of its own step, so the staircase straddles the line
		// instead of lagging it — and so the last volume lands strictly
		// before LandlordEVSGrowthEnd.
		f := (cum + float64(gb)/2) / added
		at := LandlordStoryStart.Add(time.Duration(f * span)).Truncate(time.Hour)
		cum += float64(gb)
		out = append(out, LandlordEVSPlan{SizeGB: gb, At: at, Region: landlordRegion(LandlordEVSCountStart + i)})
	}
	return out
}

// landlordEVS turns the roster into resources, each attached to one of the
// instances and billed its constant size every hour it exists.
func landlordEVS(b builder, short string, seed uint64, slug string) []ResourceSpec {
	roster := LandlordEVSRoster(seed, slug)
	out := make([]ResourceSpec, 0, len(roster))
	for i, v := range roster {
		name := fmt.Sprintf("%s-vol-%03d", short, i+1)
		host := fmt.Sprintf("%s-%s-node-%02d", short, v.Region, (i%LandlordECSLargeEnd)/2+1)
		size := float64(v.SizeGB)
		out = append(out, ResourceSpec{
			ID: b.id("evs", name), Kind: "evs", Name: name, Region: v.Region, From: v.At,
			Attrs: map[string]any{
				"size_gb": v.SizeGB, "volume_type": "SSD", "status": "in-use",
				"attached_to": b.id("ecs", host), "az": v.Region,
			},
			Labels: map[string]string{"volume_type": "SSD", "attached_to": host},
			Meter: func(time.Time, Jitter) []Line {
				return []Line{{SKU: "evs.ssd.gb", Unit: "gb-hour", Quantity: size}}
			},
		})
	}
	return out
}

// landlordWeights draws n deterministic weights in [0.5, 1.5) from the seed.
func landlordWeights(seed uint64, slug, key string, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = 0.5 + Fraction(seed, slug, key, fmt.Sprint(i))
	}
	return out
}

// apportion splits total into len(weights) whole numbers, each at least min,
// in proportion to the weights, by largest remainder — so the parts always
// sum to EXACTLY total and the split is deterministic for equal input.
func apportion(total int, weights []float64, min int) []int {
	n := len(weights)
	out := make([]int, n)
	if n == 0 {
		return out
	}
	rest := total - min*n
	if rest < 0 {
		rest = 0
	}
	sum := 0.0
	for _, w := range weights {
		sum += w
	}
	type part struct {
		i    int
		frac float64
	}
	parts := make([]part, n)
	given := 0
	for i, w := range weights {
		exact := float64(rest) * w / sum
		whole := int(math.Floor(exact))
		out[i] = min + whole
		given += whole
		parts[i] = part{i, exact - float64(whole)}
	}
	sort.SliceStable(parts, func(a, b int) bool {
		if parts[a].frac != parts[b].frac {
			return parts[a].frac > parts[b].frac
		}
		return parts[a].i < parts[b].i
	})
	for k := 0; k < rest-given && k < n; k++ {
		out[parts[k].i]++
	}
	return out
}
