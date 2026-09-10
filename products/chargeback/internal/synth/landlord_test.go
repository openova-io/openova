package synth

import (
	"bytes"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/collector/huawei"
	"github.com/openova-io/openova/products/chargeback/internal/store"
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
// rewrite the history every night. The traffic gauge is jittered, so this is
// also the assertion that the jitter is a function of (seed, address, hour)
// and of nothing else.
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

// A different seed must move the volume roster, the addresses and the
// traffic, or the determinism above is a property of a constant.
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
// rates on hw307 WITHOUT the reservation line. It fails the moment the end
// state drifts off the measured numbers, which is exactly what made the chart
// jump before.
//
// Both sides of the comparison exclude eip.bandwidth_mbps. The real side
// because the rows the old collector recorded under that SKU before
// 10 September 09:00Z describe a charge the cloud never made — every one of
// the landlord's addresses is traffic-billed — and seed-history
// --neutralise-reservations removes them; the synthetic side because it never
// emits the SKU at all, which this test asserts so the exclusion can never
// hide a regression. eip.traffic_gb is on both sides and priced on neither:
// it is the ONE SKU allowed to be unpriced here, until the operator enters a
// traffic rate.
func TestLandlordSeamMatchesTheMeasuredRealDay(t *testing.T) {
	sc, out := landlordOutput(t)
	day, ok := sc.Window.LastFullDay()
	if !ok {
		t.Fatal("the backfill window holds no whole day")
	}
	prices := Prices(NationalCloudRates)
	if _, priced := prices[EIPTrafficSKU]; priced {
		t.Fatalf("%s carries a rate in this package; no traffic price is invented here (DESIGN.md §8.2), the operator enters theirs", EIPTrafficSKU)
	}
	total, trafficGB, trafficRows := 0.0, 0.0, 0
	next := day.Add(24 * time.Hour)
	for _, r := range out.Records {
		if r.Start.Before(day) || !r.Start.Before(next) {
			continue
		}
		if r.SKU == EIPReservationSKU {
			t.Fatalf("%s reserved %v Mbps at %s; a traffic-billed address reserves nothing, and a reservation here would put the fictional bandwidth band back on the chart",
				r.ResourceID, r.Quantity, r.Start.Format(time.RFC3339))
		}
		c, priced := Cost(r, prices)
		if !priced {
			if r.SKU == EIPTrafficSKU {
				trafficGB += r.Quantity
				trafficRows++
				continue
			}
			t.Fatalf("%s is unpriced on the National Cloud list; the seam cannot be measured", r.SKU)
		}
		total += c
	}
	if trafficRows != 24*LandlordEIPCountEnd {
		t.Fatalf("%d traffic rows on the last full day, want %d (every address, every hour)", trafficRows, 24*LandlordEIPCountEnd)
	}
	drift := math.Abs(total-LandlordMeasuredDayOMR) / LandlordMeasuredDayOMR
	t.Logf("seam: %s prices at %.2f OMR against a measured real day of %.2f without the reservation line — %.2f %% apart; %.2f GB of traffic over %d address-hours is unpriced on both sides",
		day.Format("2006-01-02"), total, LandlordMeasuredDayOMR, drift*100, trafficGB, trafficRows)
	if drift > LandlordSeamTolerance {
		t.Fatalf("the last full day (%s) prices at %.2f OMR, the measured real day is %.2f — %.2f %% apart, tolerance %.0f %%",
			day.Format("2006-01-02"), total, LandlordMeasuredDayOMR, drift*100, LandlordSeamTolerance*100)
	}
	// The whole of the gap is nat.1 — this package's rate against the
	// operator's — so the day minus its NAT line must sit UNDER the measured
	// day. If it ever sits over, something other than nat.1 has drifted and
	// the tolerance is hiding it.
	nat := float64(LandlordNATCount*24) * prices["nat.1"]
	if total-nat > LandlordMeasuredDayOMR {
		t.Fatalf("without its %.2f OMR of nat.1 the day is %.2f, above the measured %.2f: the drift is no longer the known nat.1 gap",
			nat, total-nat, LandlordMeasuredDayOMR)
	}
}

// TestLandlordEndStateMatchesTheMeasuredShape — the final hour must equal the
// measured table, SKU by SKU: resource count, unit and quantity for every
// reservation, and for the traffic gauge the count and unit exactly with the
// quantity inside its jitter band around the curve.
func TestLandlordEndStateMatchesTheMeasuredShape(t *testing.T) {
	sc, out := landlordOutput(t)
	last := sc.Window.To.Add(-time.Hour)
	got := hourShape(out.Records, last)
	if len(got) != len(LandlordEndState) {
		t.Fatalf("the final hour meters %d SKUs, the measured shape has %d: %v", len(got), len(LandlordEndState), skuKeys(got))
	}
	gauges := 0
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
		if want.Gauge {
			gauges++
			band := LandlordTrafficJitter * want.Quantity
			if math.Abs(g.Quantity-want.Quantity) > band {
				t.Errorf("%s: %.4f %s in the final hour, the curve expects %.4f ± %.0f %%",
					want.SKU, g.Quantity, g.Unit, want.Quantity, LandlordTrafficJitter*100)
			}
			continue
		}
		if math.Abs(g.Quantity-want.Quantity) > 1e-6 {
			t.Errorf("%s: %.6f %s in the final hour, measured %.6f", want.SKU, g.Quantity, g.Unit, want.Quantity)
		}
	}
	if gauges != 1 {
		t.Fatalf("%d gauges in the end state, want exactly one: the traffic meter", gauges)
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
// from 1 July, 10 from 1 August; four addresses becoming five on 1 July and
// six on 1 August, each one billing its hour and metering its traffic;
// everything else flat.
func TestLandlordGrowthStepsLandOnTheRightDays(t *testing.T) {
	_, out := landlordOutput(t)
	cases := []struct {
		at       time.Time
		what     string
		large    int
		eipCount int
	}{
		{LandlordStoryStart, "the first hour", LandlordECSLargeStart, LandlordEIPCountStart},
		{LandlordStep1.Add(-time.Hour), "the hour before the first step", LandlordECSLargeStart, LandlordEIPCountStart},
		{LandlordStep1, "the first step", LandlordECSLargeMid, LandlordEIPCountStart + 1},
		{LandlordStep2.Add(-time.Hour), "the hour before the second step", LandlordECSLargeMid, LandlordEIPCountStart + 1},
		{LandlordStep2, "the second step", LandlordECSLargeEnd, LandlordEIPCountEnd},
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
		if n := got[EIPTrafficSKU].Count; n != tc.eipCount {
			t.Errorf("%s: %d addresses metered traffic, want every one of the %d", tc.what, n, tc.eipCount)
		}
		if want := LandlordTrafficHourGB(tc.at); math.Abs(got[EIPTrafficSKU].Quantity-want) > LandlordTrafficJitter*want {
			t.Errorf("%s: %.4f GB of traffic, the curve expects %.4f ± %.0f %%", tc.what, got[EIPTrafficSKU].Quantity, want, LandlordTrafficJitter*100)
		}
		if _, reserved := got[EIPReservationSKU]; reserved {
			t.Errorf("%s: a reservation is metered; these addresses are traffic-billed", tc.what)
		}
		if n := got["nat.1"].Count; n != LandlordNATCount {
			t.Errorf("%s: %d NAT gateways, want %d throughout", tc.what, n, LandlordNATCount)
		}
		if n := got["elb"].Count; n != LandlordELBCount {
			t.Errorf("%s: %d load balancers, want %d throughout", tc.what, n, LandlordELBCount)
		}
	}
}

// TestLandlordAddressesMeterTrafficNotAReservation — no address ever emits
// eip.bandwidth_mbps; every address emits eip.traffic_gb, in gb, every hour
// it exists, and the volume is never zero or negative. The old assertion here
// held the opposite (a constant reservation per address); it was true of the
// old collector's records and false of the cloud, which is the defect.
func TestLandlordAddressesMeterTrafficNotAReservation(t *testing.T) {
	sc, out := landlordOutput(t)
	perHourTraffic := map[time.Time]int{}
	perHourEIP := map[time.Time]int{}
	perResource := map[string]map[float64]bool{}
	smallest := math.Inf(1)
	for _, r := range out.Records {
		switch r.SKU {
		case EIPReservationSKU:
			t.Fatalf("%s reserved %v Mbps at %s; the landlord's addresses are traffic-billed and reserve nothing",
				r.ResourceID, r.Quantity, r.Start.Format(time.RFC3339))
		case "eip":
			perHourEIP[r.Start]++
		case EIPTrafficSKU:
			if r.Unit != EIPTrafficUnit {
				t.Fatalf("%s metered traffic in %q, want %q", r.ResourceID, r.Unit, EIPTrafficUnit)
			}
			if r.Quantity <= 0 {
				t.Fatalf("%s moved %v GB at %s; an hour of traffic is never zero or negative", r.ResourceID, r.Quantity, r.Start.Format(time.RFC3339))
			}
			if r.Quantity < smallest {
				smallest = r.Quantity
			}
			if perResource[r.ResourceID] == nil {
				perResource[r.ResourceID] = map[float64]bool{}
			}
			perResource[r.ResourceID][r.Quantity] = true
			perHourTraffic[r.Start]++
		}
	}
	if len(perResource) != LandlordEIPCountEnd {
		t.Fatalf("%d addresses metered traffic, want %d", len(perResource), LandlordEIPCountEnd)
	}
	for id, seen := range perResource {
		// A gauge with jitter cannot report the same number all summer; if it
		// does, the jitter is not being applied and the data reads as a
		// reservation again.
		if len(seen) < 100 {
			t.Errorf("address %s reported only %d distinct hourly volumes over three months; traffic is a gauge, not a reservation", id, len(seen))
		}
	}
	for h := sc.Window.From; h.Before(sc.Window.To); h = h.Add(time.Hour) {
		if perHourTraffic[h] != perHourEIP[h] {
			t.Fatalf("at %s %d addresses billed their hour but %d metered traffic", h.Format(time.RFC3339), perHourEIP[h], perHourTraffic[h])
		}
		if perHourTraffic[h] == 0 {
			t.Fatalf("no traffic at all at %s", h.Format(time.RFC3339))
		}
	}
	// The floor of the curve: the night trough on a small address on a
	// weekend hour, at the bottom of the jitter band. It is well clear of the
	// 6 decimals the ledger stores, so no hour can ever round to zero.
	floor := LandlordTrafficNightGB * LandlordTrafficWeight(100) * LandlordTrafficWeekendFactor * (1 - LandlordTrafficJitter)
	if smallest < floor-1e-9 {
		t.Fatalf("an hour fell to %.6f GB, below the curve's floor of %.6f", smallest, floor)
	}
	if floor < 1e-3 {
		t.Fatalf("the curve's floor is %.6f GB, close enough to the ledger's 6 decimals to round to nothing", floor)
	}
	t.Logf("smallest hourly volume %.6f GB, curve floor %.6f GB", smallest, floor)
}

// trafficByResource is each address's pipe size and its traffic records.
func trafficByResource(out Output) (mbps map[string]float64, recs map[string][]Record) {
	mbps = map[string]float64{}
	for _, r := range out.Resources {
		if r.Kind == "eip" {
			mbps[r.ID], _ = r.Attrs["bandwidth_mbps"].(float64)
		}
	}
	recs = map[string][]Record{}
	for _, r := range out.Records {
		if r.SKU == EIPTrafficSKU {
			recs[r.ResourceID] = append(recs[r.ResourceID], r)
		}
	}
	return mbps, recs
}

// TestLandlordTrafficFollowsTheDailyCurve — per address, the generated night
// hours average the night figure, the working day the day figure and 20:00
// local the peak, each times the address's weight, with the jitter averaged
// out over three months of weekdays. Night < day < peak, and the ratios
// between them are the profile's.
func TestLandlordTrafficFollowsTheDailyCurve(t *testing.T) {
	_, out := landlordOutput(t)
	mbps, recs := trafficByResource(out)
	if len(recs) != LandlordEIPCountEnd {
		t.Fatalf("%d addresses metered traffic, want %d", len(recs), LandlordEIPCountEnd)
	}
	type acc struct {
		sum float64
		n   int
	}
	mean := func(a acc) float64 { return a.sum / float64(a.n) }
	for id, rs := range recs {
		var night, day, peak acc
		for _, r := range rs {
			if LandlordWeekendFactor(r.Start) != 1 {
				continue
			}
			switch lh := landlordLocalHour(r.Start); {
			case lh >= 2 && lh <= 4:
				night.sum, night.n = night.sum+r.Quantity, night.n+1
			case lh >= 8 && lh <= 15:
				day.sum, day.n = day.sum+r.Quantity, day.n+1
			case lh == 20:
				peak.sum, peak.n = peak.sum+r.Quantity, peak.n+1
			}
		}
		if night.n < 60 || day.n < 160 || peak.n < 20 {
			t.Fatalf("address %s: too few samples to average (%d night, %d day, %d peak)", id, night.n, day.n, peak.n)
		}
		w := LandlordTrafficWeight(mbps[id])
		for _, band := range []struct {
			name string
			got  float64
			want float64
		}{
			{"night", mean(night), LandlordTrafficNightGB * w},
			{"day", mean(day), LandlordTrafficDayGB * w},
			{"peak", mean(peak), LandlordTrafficPeakGB * w},
		} {
			if math.Abs(band.got-band.want)/band.want > 0.1 {
				t.Errorf("address %s (%.0f Mbps): %s averages %.4f GB/h, the curve says %.4f", id, mbps[id], band.name, band.got, band.want)
			}
		}
		if !(mean(night) < mean(day) && mean(day) < mean(peak)) {
			t.Errorf("address %s: night %.4f, day %.4f, peak %.4f — not ascending", id, mean(night), mean(day), mean(peak))
		}
	}
}

// TestLandlordTrafficWeekendFactor — Friday and Saturday carry 70 % of a
// weekday's volume, hour for hour: exactly on the curve, and within the
// jitter's averaging on the generated records.
func TestLandlordTrafficWeekendFactor(t *testing.T) {
	fri := LandlordStep2
	for fri.Weekday() != time.Friday {
		fri = fri.Add(24 * time.Hour)
	}
	noon := fri.Add(12 * time.Hour)
	for _, size := range []float64{100, 300} {
		thu := LandlordTrafficGB(noon.Add(-24*time.Hour), size)
		if got := LandlordTrafficGB(noon, size) / thu; math.Abs(got-LandlordTrafficWeekendFactor) > 1e-9 {
			t.Errorf("Friday carries %.3f of Thursday on a %.0f-Mbps address, want %.2f", got, size, LandlordTrafficWeekendFactor)
		}
		if got := LandlordTrafficGB(noon.Add(24*time.Hour), size) / thu; math.Abs(got-LandlordTrafficWeekendFactor) > 1e-9 {
			t.Errorf("Saturday carries %.3f of Thursday on a %.0f-Mbps address, want %.2f", got, size, LandlordTrafficWeekendFactor)
		}
		if got := LandlordTrafficGB(noon.Add(48*time.Hour), size) / thu; math.Abs(got-1) > 1e-9 {
			t.Errorf("Sunday carries %.3f of Thursday; Sunday is a working day in Oman", got)
		}
	}

	// On the generated records, over the converged month: the mean weekend
	// hour against the mean weekday hour.
	_, out := landlordOutput(t)
	var wk, we struct {
		sum float64
		n   int
	}
	for _, r := range out.Records {
		if r.SKU != EIPTrafficSKU || r.Start.Before(LandlordStep2) || !r.Start.Before(LandlordStep2.AddDate(0, 1, 0)) {
			continue
		}
		if LandlordWeekendFactor(r.Start) == 1 {
			wk.sum, wk.n = wk.sum+r.Quantity, wk.n+1
		} else {
			we.sum, we.n = we.sum+r.Quantity, we.n+1
		}
	}
	if wk.n == 0 || we.n == 0 {
		t.Fatalf("August has %d weekday and %d weekend traffic rows", wk.n, we.n)
	}
	got := (we.sum / float64(we.n)) / (wk.sum / float64(wk.n))
	if math.Abs(got-LandlordTrafficWeekendFactor) > 0.05 {
		t.Fatalf("a generated weekend hour averages %.3f of a weekday hour, want %.2f", got, LandlordTrafficWeekendFactor)
	}
	t.Logf("weekend hour = %.3f × weekday hour over %d + %d rows", got, we.n, wk.n)
}

// TestLandlordWeeklyTrafficLandsNearTheMeasuredMean — over a full week with
// all six addresses present, the sum is within 25 % of
// 6 × 0.1 GB × 168 hours, the measured ~0.1 GB per address per hour; and the
// three 300-Mbps addresses carry about twice the three 100-Mbps ones.
func TestLandlordWeeklyTrafficLandsNearTheMeasuredMean(t *testing.T) {
	_, out := landlordOutput(t)
	mbps, _ := trafficByResource(out)
	from := LandlordStep2.AddDate(0, 0, 7) // a whole week, well inside the converged month
	to := from.Add(7 * 24 * time.Hour)
	sum, big, small, rows := 0.0, 0.0, 0.0, 0
	for _, r := range out.Records {
		if r.SKU != EIPTrafficSKU || r.Start.Before(from) || !r.Start.Before(to) {
			continue
		}
		sum += r.Quantity
		rows++
		if mbps[r.ResourceID] >= LandlordBigEIPMbps {
			big += r.Quantity
		} else {
			small += r.Quantity
		}
	}
	if rows != LandlordEIPCountEnd*168 {
		t.Fatalf("%d traffic rows in the week, want %d", rows, LandlordEIPCountEnd*168)
	}
	want := LandlordEIPCountEnd * LandlordTrafficMeanGB * 168
	off := math.Abs(sum-want) / want
	t.Logf("week %s: %.2f GB across six addresses against %.2f measured (%.1f %% off); big addresses %.2f, small %.2f (ratio %.2f)",
		from.Format("2006-01-02"), sum, want, off*100, big, small, big/small)
	if off > 0.25 {
		t.Fatalf("the week sums to %.2f GB, %.1f %% off the measured %.2f (tolerance 25 %%)", sum, off*100, want)
	}
	if ratio := big / small; math.Abs(ratio-LandlordTrafficRatio) > 0.1*LandlordTrafficRatio {
		t.Fatalf("the 300-Mbps addresses carry %.2f × the 100-Mbps ones, want about %.0f", ratio, LandlordTrafficRatio)
	}
	// The weights are normalised over the full roster, so the six of them
	// average to exactly 1 — that is what makes the profile's mean the
	// roster's mean.
	total := 0.0
	for _, e := range LandlordEIPs {
		total += LandlordTrafficWeight(e.Mbps)
	}
	if math.Abs(total-float64(len(LandlordEIPs))) > 1e-9 {
		t.Fatalf("the roster's weights sum to %.6f, want %d", total, len(LandlordEIPs))
	}
}

// TestLandlordTrafficConstantsMatchTheCollector — the synthetic rows sit in
// the same table as the collector's, so the SKU, its unit and the charge-mode
// values must be the collector's own. The attr KEYS (bandwidth_charge_mode,
// bandwidth_share_type) are the lister's unexported attrChargeMode /
// attrShareType and DESIGN.md §8.2 states them.
func TestLandlordTrafficConstantsMatchTheCollector(t *testing.T) {
	if EIPTrafficSKU != huawei.SKUEIPTraffic {
		t.Fatalf("traffic SKU %q; the collector writes %q", EIPTrafficSKU, huawei.SKUEIPTraffic)
	}
	if EIPTrafficSKU != store.SKUEIPTraffic {
		t.Fatalf("traffic SKU %q; the store reads %q", EIPTrafficSKU, store.SKUEIPTraffic)
	}
	if EIPTrafficUnit != huawei.UnitEIPTraffic {
		t.Fatalf("traffic unit %q; the collector writes %q", EIPTrafficUnit, huawei.UnitEIPTraffic)
	}
	if EIPChargeModeTraffic != huawei.ChargeModeTraffic || EIPChargeModeBandwidth != huawei.ChargeModeBandwidth {
		t.Fatalf("charge modes %q/%q; the collector records %q/%q", EIPChargeModeTraffic, EIPChargeModeBandwidth, huawei.ChargeModeTraffic, huawei.ChargeModeBandwidth)
	}
	if EIPShareTypePer != huawei.ShareTypePer {
		t.Fatalf("share type %q; the collector records %q", EIPShareTypePer, huawei.ShareTypePer)
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

// TestLandlordShapesLookLikeTheRealOnes — ids, labels and attrs are what a
// reader compares against the real rows sitting next to them in the same
// table. An address's inventory row must say how the cloud bills it, the way
// the collector's does since 0.1.26, or --neutralise-reservations could not
// tell a synthetic address from a real one whose charge mode is unknown.
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
			continue
		}
		if mode, _ := r.Attrs[EIPChargeModeAttr].(string); mode != EIPChargeModeTraffic {
			t.Errorf("EIP %s: %s %q, want %q — the real addresses on hw307 are all traffic-billed", r.ID, EIPChargeModeAttr, mode, EIPChargeModeTraffic)
		}
		if share, _ := r.Attrs[EIPShareTypeAttr].(string); share != EIPShareTypePer {
			t.Errorf("EIP %s: %s %q, want %q", r.ID, EIPShareTypeAttr, share, EIPShareTypePer)
		}
		if size, _ := r.Attrs["bandwidth_mbps"].(float64); size != 100 && size != 300 {
			t.Errorf("EIP %s: bandwidth_mbps %v, the roster only has 100 and 300-Mbps pipes", r.ID, r.Attrs["bandwidth_mbps"])
		}
		if r.Attrs[LabelKey] != LabelValue {
			t.Errorf("EIP %s carries no synthetic mark; the neutralisation must be able to skip it", r.ID)
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
