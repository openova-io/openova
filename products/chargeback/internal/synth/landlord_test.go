package synth

import (
	"bytes"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"
	"time"
)

// The landlord backfill exists to make the join between the synthetic past
// and the real collection invisible. Every test below asserts one half of
// that: the backfill stops exactly where the real data starts, and it arrives
// there in the measured shape.

func landlordScenario(t *testing.T) *Scenario {
	t.Helper()
	sc, err := LandlordScenario(LandlordDefaultSlug, LandlordStoryStart, LandlordDefaultUntil, DefaultSeed)
	if err != nil {
		t.Fatalf("landlord scenario: %v", err)
	}
	return sc
}

func landlordOutput(t *testing.T) (*Scenario, Output) {
	t.Helper()
	sc := landlordScenario(t)
	return sc, sc.Generate(sc.Landlord())
}

// hourShape collects one hour of records into a SKU → (resource count, total
// quantity) map — the same view LandlordEndState describes.
func hourShape(recs []Record, at time.Time) map[string]SKUShape {
	out := map[string]SKUShape{}
	for _, r := range recs {
		if !r.Start.Equal(at) {
			continue
		}
		s := out[r.SKU]
		s.SKU, s.Unit = r.SKU, r.Unit
		s.Count++
		s.Quantity += r.Quantity
		out[r.SKU] = s
	}
	return out
}

func landlordCSV(t *testing.T, sc *Scenario) []byte {
	t.Helper()
	var buf bytes.Buffer
	out := sc.Generate(sc.Landlord())
	if err := WriteCSV(&buf, out.Records); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	for _, r := range out.Resources {
		fmt.Fprintf(&buf, "%s|%s|%s|%s|%s|%s|%s\n", r.ID, r.Kind, r.Name, r.Region,
			r.FirstSeen.Format(time.RFC3339), r.LastSeen.Format(time.RFC3339), r.DeletedAt.Format(time.RFC3339))
	}
	return buf.Bytes()
}

// TestLandlordDeterministicBytes — the backfill lands on a live ledger and is
// re-run there, so the same seed must upsert the same values rather than
// rewrite the history every night.
func TestLandlordDeterministicBytes(t *testing.T) {
	a := landlordCSV(t, landlordScenario(t))
	b := landlordCSV(t, landlordScenario(t))
	if !bytes.Equal(a, b) {
		t.Fatalf("same seed produced different bytes: %d vs %d", len(a), len(b))
	}
	if len(a) == 0 {
		t.Fatal("the backfill produced no bytes at all")
	}
}

// A different seed must move the volume roster and the addresses, or the
// determinism above is a property of a constant.
func TestLandlordDifferentSeedDiffers(t *testing.T) {
	other, err := LandlordScenario(LandlordDefaultSlug, LandlordStoryStart, LandlordDefaultUntil, DefaultSeed+1)
	if err != nil {
		t.Fatalf("landlord scenario: %v", err)
	}
	if bytes.Equal(landlordCSV(t, landlordScenario(t)), landlordCSV(t, other)) {
		t.Fatal("a different seed produced identical bytes; nothing is seeded")
	}
}

// TestLandlordStopsBeforeTheFirstRealRecord — the whole point of discovering
// the cut. A row at or after the first real record would double-bill an hour
// the collector already owns.
func TestLandlordStopsBeforeTheFirstRealRecord(t *testing.T) {
	if !LandlordDefaultUntil.Before(LandlordFirstRealRecord) {
		t.Fatalf("the default cut %s is not before the first real record %s",
			LandlordDefaultUntil.Format(time.RFC3339), LandlordFirstRealRecord.Format(time.RFC3339))
	}
	if !LandlordDefaultUntil.Equal(LandlordDefaultUntil.Truncate(time.Hour)) {
		t.Fatalf("the default cut %s is not a whole hour", LandlordDefaultUntil.Format(time.RFC3339))
	}
	_, out := landlordOutput(t)
	if len(out.Records) == 0 {
		t.Fatal("the backfill produced no records")
	}
	var last time.Time
	for _, r := range out.Records {
		if !r.Start.Before(LandlordDefaultUntil) {
			t.Fatalf("record %s %s starts at %s, on or after the cut %s",
				r.ResourceID, r.SKU, r.Start.Format(time.RFC3339), LandlordDefaultUntil.Format(time.RFC3339))
		}
		if r.End.After(LandlordDefaultUntil) {
			t.Fatalf("record %s %s ends at %s, after the cut", r.ResourceID, r.SKU, r.End.Format(time.RFC3339))
		}
		if r.Start.After(last) {
			last = r.Start
		}
	}
	want := LandlordDefaultUntil.Add(-time.Hour)
	if !last.Equal(want) {
		t.Fatalf("the last backfilled hour is %s, want %s — the hour immediately before the real data",
			last.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	if !last.Before(LandlordFirstRealRecord) {
		t.Fatalf("the last backfilled hour %s is not before the first real record %s", last, LandlordFirstRealRecord)
	}
}

// TestLandlordSeamMatchesTheMeasuredRealDay is the seam itself: the last full
// day of the backfill, priced with this package's own National Cloud rates,
// must land within LandlordSeamTolerance of what a real day of that shape
// rated on hw307. It fails the moment the end state drifts off the measured
// numbers, which is exactly what made the chart jump before.
func TestLandlordSeamMatchesTheMeasuredRealDay(t *testing.T) {
	sc, out := landlordOutput(t)
	day, ok := sc.Window.LastFullDay()
	if !ok {
		t.Fatal("the backfill window holds no whole day")
	}
	prices := Prices(NationalCloudRates)
	total, bandwidth := 0.0, 0.0
	next := day.Add(24 * time.Hour)
	for _, r := range out.Records {
		if r.Start.Before(day) || !r.Start.Before(next) {
			continue
		}
		c, priced := Cost(r, prices)
		if !priced {
			t.Fatalf("%s is unpriced on the National Cloud list; the seam cannot be measured", r.SKU)
		}
		total += c
		if r.SKU == "eip.bandwidth_mbps" {
			bandwidth += c
		}
	}
	drift := math.Abs(total-LandlordMeasuredDayOMR) / LandlordMeasuredDayOMR
	t.Logf("seam: %s prices at %.2f OMR (%.2f of it EIP bandwidth) against a measured real day of %.2f — %.2f %% apart",
		day.Format("2006-01-02"), total, bandwidth, LandlordMeasuredDayOMR, drift*100)
	if drift > LandlordSeamTolerance {
		t.Fatalf("the last full day (%s) prices at %.2f OMR, the measured real day is %.2f — %.2f %% apart, tolerance %.0f %%",
			day.Format("2006-01-02"), total, LandlordMeasuredDayOMR, drift*100, LandlordSeamTolerance*100)
	}
	// The founder asked why bandwidth dominates the bill: because reserved
	// size is billed whether or not traffic flows. If that ever stops being
	// true of the data, the data has stopped telling the truth about the
	// billing model.
	if share := bandwidth / total; share < 0.75 {
		t.Fatalf("EIP bandwidth is %.1f %% of the day (%.2f of %.2f OMR); the measured day is ~83 %%",
			share*100, bandwidth, total)
	}
}

// TestLandlordEndStateMatchesTheMeasuredShape — the final hour must equal the
// measured table exactly, SKU by SKU, in resource count and quantity.
func TestLandlordEndStateMatchesTheMeasuredShape(t *testing.T) {
	sc, out := landlordOutput(t)
	last := sc.Window.To.Add(-time.Hour)
	got := hourShape(out.Records, last)
	if len(got) != len(LandlordEndState) {
		t.Fatalf("the final hour meters %d SKUs, the measured shape has %d: %v", len(got), len(LandlordEndState), skuKeys(got))
	}
	for _, want := range LandlordEndState {
		g, ok := got[want.SKU]
		if !ok {
			t.Errorf("%s is missing from the final hour", want.SKU)
			continue
		}
		if g.Unit != want.Unit {
			t.Errorf("%s: unit %q, want %q", want.SKU, g.Unit, want.Unit)
		}
		if g.Count != want.Count {
			t.Errorf("%s: %d resources in the final hour, measured %d", want.SKU, g.Count, want.Count)
		}
		if math.Abs(g.Quantity-want.Quantity) > 1e-6 {
			t.Errorf("%s: %.6f %s in the final hour, measured %.6f", want.SKU, g.Quantity, g.Unit, want.Quantity)
		}
	}
}

func skuKeys(m map[string]SKUShape) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestLandlordGrowthStepsLandOnTheRightDays — 6 instances through June, 8
// from 1 July, 10 from 1 August; four EIPs (800 Mbps) becoming five (900) on
// 1 July and six (1,200) on 1 August; everything else flat.
func TestLandlordGrowthStepsLandOnTheRightDays(t *testing.T) {
	_, out := landlordOutput(t)
	cases := []struct {
		at       time.Time
		what     string
		large    int
		eipCount int
		eipMbps  float64
	}{
		{LandlordStoryStart, "the first hour", LandlordECSLargeStart, LandlordEIPCountStart, LandlordEIPMbpsStart},
		{LandlordStep1.Add(-time.Hour), "the hour before the first step", LandlordECSLargeStart, LandlordEIPCountStart, LandlordEIPMbpsStart},
		{LandlordStep1, "the first step", LandlordECSLargeMid, LandlordEIPCountStart + 1, 900},
		{LandlordStep2.Add(-time.Hour), "the hour before the second step", LandlordECSLargeMid, LandlordEIPCountStart + 1, 900},
		{LandlordStep2, "the second step", LandlordECSLargeEnd, LandlordEIPCountEnd, LandlordEIPMbpsEnd},
	}
	for _, tc := range cases {
		got := hourShape(out.Records, tc.at)
		if n := got[LandlordECSLargeSKU].Count; n != tc.large {
			t.Errorf("%s (%s): %d × %s, want %d", tc.what, tc.at.Format(time.RFC3339), n, LandlordECSLargeSKU, tc.large)
		}
		if n := got[LandlordECSSmallSKU].Count; n != LandlordECSSmallCount {
			t.Errorf("%s: %d × %s, want %d throughout", tc.what, n, LandlordECSSmallSKU, LandlordECSSmallCount)
		}
		if n := got["eip"].Count; n != tc.eipCount {
			t.Errorf("%s: %d EIPs, want %d", tc.what, n, tc.eipCount)
		}
		if q := got["eip.bandwidth_mbps"].Quantity; math.Abs(q-tc.eipMbps) > 1e-6 {
			t.Errorf("%s: %.0f Mbps reserved, want %.0f", tc.what, q, tc.eipMbps)
		}
		if n := got["nat.1"].Count; n != LandlordNATCount {
			t.Errorf("%s: %d NAT gateways, want %d throughout", tc.what, n, LandlordNATCount)
		}
		if n := got["elb"].Count; n != LandlordELBCount {
			t.Errorf("%s: %d load balancers, want %d throughout", tc.what, n, LandlordELBCount)
		}
	}
}

// TestLandlordBandwidthIsAReservationNotTraffic — each EIP reports the SAME
// number every hour it exists, and the hourly total changes on exactly the
// two days an EIP is added. Jitter here would say bandwidth is metered
// traffic, which is precisely the misreading the founder asked about.
func TestLandlordBandwidthIsAReservationNotTraffic(t *testing.T) {
	_, out := landlordOutput(t)
	perResource := map[string]map[float64]bool{}
	perHour := map[time.Time]float64{}
	for _, r := range out.Records {
		if r.SKU != "eip.bandwidth_mbps" {
			continue
		}
		if perResource[r.ResourceID] == nil {
			perResource[r.ResourceID] = map[float64]bool{}
		}
		perResource[r.ResourceID][r.Quantity] = true
		perHour[r.Start] += r.Quantity
	}
	if len(perResource) != LandlordEIPCountEnd {
		t.Fatalf("%d EIPs metered bandwidth, want %d", len(perResource), LandlordEIPCountEnd)
	}
	for id, seen := range perResource {
		if len(seen) != 1 {
			t.Errorf("EIP %s reported %d different bandwidths; a reservation has exactly one", id, len(seen))
		}
		for q := range seen {
			if q != 300 && q != 100 {
				t.Errorf("EIP %s reserved %v Mbps; the roster only provisions 300 and 100", id, q)
			}
		}
	}
	hours := make([]time.Time, 0, len(perHour))
	for h := range perHour {
		hours = append(hours, h)
	}
	sort.Slice(hours, func(i, j int) bool { return hours[i].Before(hours[j]) })
	changes := map[string]float64{}
	for i := 1; i < len(hours); i++ {
		if perHour[hours[i]] != perHour[hours[i-1]] {
			changes[hours[i].Format(time.RFC3339)] = perHour[hours[i]]
		}
	}
	want := map[string]float64{
		LandlordStep1.Format(time.RFC3339): 900,
		LandlordStep2.Format(time.RFC3339): float64(LandlordEIPMbpsEnd),
	}
	if len(changes) != len(want) {
		t.Fatalf("reserved bandwidth changed at %d hours, want exactly the two addition days: %v", len(changes), changes)
	}
	for at, q := range want {
		if changes[at] != q {
			t.Errorf("at %s reserved bandwidth is %v, want %v", at, changes[at], q)
		}
	}
}

// TestLandlordStorageGrowsOverAGrowingVolumeCount — the one thing allowed to
// drift, and only because volumes are created: each volume's own size is
// constant, the count climbs 70 → 102 and the total 1,400 → 2,281 GB.
func TestLandlordStorageGrowsOverAGrowingVolumeCount(t *testing.T) {
	sc, out := landlordOutput(t)

	perResource := map[string]map[float64]bool{}
	for _, r := range out.Records {
		if r.SKU != "evs.ssd.gb" {
			continue
		}
		if perResource[r.ResourceID] == nil {
			perResource[r.ResourceID] = map[float64]bool{}
		}
		perResource[r.ResourceID][r.Quantity] = true
	}
	if len(perResource) != LandlordEVSCountEnd {
		t.Fatalf("%d volumes, want %d", len(perResource), LandlordEVSCountEnd)
	}
	for id, seen := range perResource {
		if len(seen) != 1 {
			t.Errorf("volume %s reported %d different sizes; a provisioned volume has one", id, len(seen))
		}
		for q := range seen {
			if q != math.Trunc(q) || q < landlordMinVolumeGB {
				t.Errorf("volume %s is %v GB; sizes are whole numbers of at least %d", id, q, landlordMinVolumeGB)
			}
		}
	}

	first := hourShape(out.Records, sc.Window.From)["evs.ssd.gb"]
	if first.Count != LandlordEVSCountStart || math.Abs(first.Quantity-LandlordEVSGBStart) > 1e-6 {
		t.Errorf("the first hour has %d volumes totalling %.0f GB, want %d / %d",
			first.Count, first.Quantity, LandlordEVSCountStart, LandlordEVSGBStart)
	}
	last := hourShape(out.Records, sc.Window.To.Add(-time.Hour))["evs.ssd.gb"]
	if last.Count != LandlordEVSCountEnd || math.Abs(last.Quantity-LandlordEVSGBEnd) > 1e-6 {
		t.Errorf("the last hour has %d volumes totalling %.0f GB, want %d / %d",
			last.Count, last.Quantity, LandlordEVSCountEnd, LandlordEVSGBEnd)
	}

	// The staircase must be monotone and must track the straight line it is
	// built from: never more than one volume's worth away from it.
	roster := LandlordEVSRoster(sc.Seed, LandlordDefaultSlug)
	biggest := 0
	for _, v := range roster {
		if v.SizeGB > biggest {
			biggest = v.SizeGB
		}
	}
	prev := 0.0
	for d := sc.Window.From; d.Before(sc.Window.To); d = d.Add(24 * time.Hour) {
		got := hourShape(out.Records, d)["evs.ssd.gb"].Quantity
		if got < prev {
			t.Fatalf("storage fell from %.0f to %.0f GB at %s", prev, got, d.Format("2006-01-02"))
		}
		prev = got
		want := Linear(d, LandlordStoryStart, LandlordEVSGrowthEnd, LandlordEVSGBStart, LandlordEVSGBEnd)
		if math.Abs(got-want) > float64(biggest) {
			t.Errorf("%s: %.0f GB provisioned, the ramp is at %.0f — more than one volume (%d GB) off",
				d.Format("2006-01-02"), got, want, biggest)
		}
	}
}

// TestLandlordLeavesNoEmptyDay is the founder's first complaint, as an
// assertion: "step 1st is empty". Every UTC day from the window start to the
// cut must carry usage, 1 September included.
func TestLandlordLeavesNoEmptyDay(t *testing.T) {
	sc, out := landlordOutput(t)
	byDay := map[string]int{}
	for _, r := range out.Records {
		byDay[r.Start.Format("2006-01-02")]++
	}
	for d := sc.Window.From; d.Before(sc.Window.To); d = d.Add(24 * time.Hour) {
		if byDay[d.Format("2006-01-02")] == 0 {
			t.Errorf("%s has no usage at all — that is the empty bucket in the middle of the chart", d.Format("2006-01-02"))
		}
	}
	// The showcase window closes on 1 September; the day the backfill must
	// cover, and the morning after it, are the two the old data missed.
	for _, day := range []string{"2026-09-01", "2026-09-02"} {
		if byDay[day] == 0 {
			t.Errorf("%s is empty; that is exactly the hole this backfill exists to close", day)
		}
	}
}

// TestLandlordHandsResourcesOverAliveN — a machine still metering in the last
// hour did not go away; the real collection took it over. Writing a deleted_at
// there would put a false fact on the resources page.
func TestLandlordHandsResourcesOverAlive(t *testing.T) {
	sc, out := landlordOutput(t)
	if len(out.Resources) == 0 {
		t.Fatal("the backfill declared no resources")
	}
	for _, r := range out.Resources {
		if !r.DeletedAt.IsZero() {
			t.Errorf("%s (%s) is marked deleted at %s; the landlord's machines are still running",
				r.ID, r.Kind, r.DeletedAt.Format(time.RFC3339))
		}
		if !r.LastSeen.Equal(sc.Window.To) {
			t.Errorf("%s was last seen at %s, want the cut %s", r.ID, r.LastSeen.Format(time.RFC3339), sc.Window.To.Format(time.RFC3339))
		}
	}
	// The showcase customers must keep their decommission dates: the flag is
	// the backfill's, not everybody's.
	for _, o := range generateAll(defaultScenario()) {
		for _, r := range o.Resources {
			if r.DeletedAt.IsZero() {
				t.Fatalf("showcase resource %s (%s) has no deleted_at; every showcase resource is gone before the window closes", r.ID, o.Customer.Slug)
			}
		}
	}
}

// TestLandlordShapesLookLikeTheRealOnes — ids and labels are what a reader
// compares against the real rows sitting next to them in the same table.
func TestLandlordShapesLookLikeTheRealOnes(t *testing.T) {
	_, out := landlordOutput(t)
	kinds := map[string]int{}
	for _, r := range out.Resources {
		kinds[r.Kind]++
		switch r.Kind {
		case "ecs", "evs", "eip", "nat", "elb":
			if !strings.HasPrefix(r.ID, r.Kind+"-") || len(r.ID) != len(r.Kind)+9 {
				t.Errorf("%s id %q is not <kind>-<8 hex>", r.Kind, r.ID)
			}
		}
		if r.Region != RegionA && r.Region != RegionB {
			t.Errorf("%s (%s) is in region %q, not one of the two National Cloud regions", r.ID, r.Kind, r.Region)
		}
		// An EIP is region-scoped and carries no AZ; everything else does,
		// and on hw307 the collector records the region string there.
		if r.Kind != "eip" {
			if az, _ := r.Attrs["az"].(string); az != r.Region {
				t.Errorf("%s: az %q, want the region %q", r.ID, az, r.Region)
			}
		}
	}
	want := map[string]int{
		"ecs": LandlordECSLargeEnd + LandlordECSSmallCount,
		"eip": LandlordEIPCountEnd,
		"evs": LandlordEVSCountEnd,
		"nat": LandlordNATCount,
		"elb": LandlordELBCount,
	}
	for kind, n := range want {
		if kinds[kind] != n {
			t.Errorf("%d %s resources, want %d", kinds[kind], kind, n)
		}
	}
	// EIP records carry the real collector's three labels, and the address
	// never collides with the platform's own bastion.
	seen := 0
	for _, r := range out.Records {
		if r.SKU != "eip" {
			continue
		}
		seen++
		for _, k := range []string{"name", "status", "enterprise_project"} {
			if r.Labels[k] == "" {
				t.Fatalf("EIP record %s has no %q label", r.ResourceID, k)
			}
		}
		if st := r.Labels["status"]; st != "ACTIVE" && st != "ELB" {
			t.Fatalf("EIP %s has status %q, want ACTIVE or ELB", r.ResourceID, st)
		}
		if r.Labels["name"] == "212.72.24.20" {
			t.Fatalf("EIP %s was given the platform's own bastion address", r.ResourceID)
		}
	}
	if seen == 0 {
		t.Fatal("no EIP records at all")
	}
}

// TestLandlordPurgeReachesTheBackfillAndNothingReal — the backfill hangs off a
// REAL customer, so the customer-slug selector can never reach it; the source
// name is what a purge must match instead.
func TestLandlordPurgeReachesTheBackfillAndNothingReal(t *testing.T) {
	if IsSyntheticSlug(LandlordDefaultSlug) {
		t.Fatalf("the purge would delete the real landlord customer %q", LandlordDefaultSlug)
	}
	name := LandlordSourceName(LandlordDefaultSlug)
	if name != "demo-hw307-omani-works-history" {
		t.Fatalf("the backfill source is named %q", name)
	}
	if !IsSyntheticSourceName(name) {
		t.Fatalf("the purge would leave the backfill source %q behind, stranding three months of synthetic rows on a live ledger", name)
	}
	for _, n := range []string{"demo-gulf-retail", "demo-nizwa-fintech", "demo-x"} {
		if !IsSyntheticSourceName(n) {
			t.Errorf("the purge would leave the showcase source %q behind", n)
		}
	}
	// Real project ids from live databases: a Huawei project, an Organization
	// slug, the landlord's own slug, and the near-misses the prefix must not
	// swallow.
	for _, n := range []string{
		"0a1b2c3d4e5f60718293a4b5c6d7e8f9", "acmewalk307", "openova",
		LandlordDefaultSlug, "demo", "demo-", "demonstration", "hw307-demo",
	} {
		if IsSyntheticSourceName(n) {
			t.Errorf("the purge would delete the real source %q", n)
		}
	}
	if !strings.Contains(SQLSourcePredicate, SlugPrefix+"_%") {
		t.Errorf("SQL predicate %q does not require a character after %q", SQLSourcePredicate, SlugPrefix)
	}
	if !strings.Contains(SQLSourcePredicate, "project_id") {
		t.Errorf("SQL predicate %q does not select on the source name", SQLSourcePredicate)
	}
}

// TestLandlordScenarioRefusesASyntheticSlug — pointing the backfill at a
// demo- customer would put its rows behind the customer purge and delete them
// with the showcase.
func TestLandlordScenarioRefusesASyntheticSlug(t *testing.T) {
	if _, err := LandlordScenario(SlugPrefix+"gulf-retail", LandlordStoryStart, LandlordDefaultUntil, DefaultSeed); err == nil {
		t.Fatal("a demo- slug was accepted as the landlord")
	}
	if _, err := LandlordScenario("", LandlordStoryStart, LandlordDefaultUntil, DefaultSeed); err == nil {
		t.Fatal("an empty slug was accepted as the landlord")
	}
	if _, err := LandlordScenario(LandlordDefaultSlug, LandlordDefaultUntil, LandlordStoryStart, DefaultSeed); err == nil {
		t.Fatal("a window that ends before it starts was accepted")
	}
}

// TestLastFullDay pins the helper the seam test measures with: the window
// ends mid-morning, so the last WHOLE day is the one before.
func TestLastFullDay(t *testing.T) {
	day, ok := Window{From: LandlordStoryStart, To: LandlordDefaultUntil}.LastFullDay()
	if !ok {
		t.Fatal("no full day in the backfill window")
	}
	if got := day.Format("2006-01-02"); got != "2026-09-01" {
		t.Fatalf("last full day %s, want 2026-09-01", got)
	}
	if _, ok := (Window{From: LandlordStoryStart, To: LandlordStoryStart.Add(time.Hour)}).LastFullDay(); ok {
		t.Fatal("a one-hour window reported a full day")
	}
}
