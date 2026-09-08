package synth

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/anomaly"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

func defaultScenario() *Scenario { return DefaultScenario(DefaultWindow(), DefaultSeed) }

func generateAll(s *Scenario) []Output {
	out := make([]Output, 0, len(s.Customers))
	for _, c := range s.Customers {
		out = append(out, s.Generate(c))
	}
	return out
}

// csvBytes renders every customer's records the way the seeding command
// uploads them, so a byte comparison covers quantities, labels and ordering.
func csvBytes(t *testing.T, s *Scenario) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, out := range generateAll(s) {
		fmt.Fprintf(&buf, "== %s\n", out.Customer.Slug)
		if err := WriteCSV(&buf, out.Records); err != nil {
			t.Fatalf("write csv: %v", err)
		}
		for _, r := range out.Resources {
			fmt.Fprintf(&buf, "%s|%s|%s|%s|%s|%s|%s\n", r.ID, r.Kind, r.Name, r.Region,
				r.FirstSeen.Format(time.RFC3339), r.LastSeen.Format(time.RFC3339), r.DeletedAt.Format(time.RFC3339))
		}
	}
	return buf.Bytes()
}

// TestDeterministicBytes is the contract that lets the command re-run over a
// seeded database and upsert in place: the same seed must produce identical
// bytes, run after run.
func TestDeterministicBytes(t *testing.T) {
	a := csvBytes(t, defaultScenario())
	b := csvBytes(t, defaultScenario())
	if !bytes.Equal(a, b) {
		t.Fatalf("same seed produced different bytes: %d vs %d", len(a), len(b))
	}
	if len(a) == 0 {
		t.Fatal("scenario produced no bytes at all")
	}
}

// A different seed must actually change the data, or the jitter is inert and
// the determinism test above would pass on a constant.
func TestDifferentSeedDiffers(t *testing.T) {
	a := csvBytes(t, defaultScenario())
	b := csvBytes(t, DefaultScenario(DefaultWindow(), DefaultSeed+1))
	if bytes.Equal(a, b) {
		t.Fatal("a different seed produced identical bytes; the jitter is not seeded")
	}
}

// Generating a sub-window must agree hour for hour with the full run — the
// property that makes --from/--to safe to narrow on a re-run.
func TestSubWindowAgreesWithFullWindow(t *testing.T) {
	full := defaultScenario()
	sub := DefaultScenario(Window{From: date(2026, 7, 1, 0), To: date(2026, 8, 1, 0)}, DefaultSeed)
	c := full.Customer("gulf-retail")
	want := map[string]float64{}
	for _, r := range full.Generate(c).Records {
		if r.Start.Month() == time.July {
			want[r.ResourceID+"|"+r.SKU+"|"+r.Start.Format(time.RFC3339)] = r.Quantity
		}
	}
	got := sub.Generate(sub.Customer("gulf-retail")).Records
	if len(got) != len(want) {
		t.Fatalf("sub-window produced %d records, full window has %d in July", len(got), len(want))
	}
	for _, r := range got {
		k := r.ResourceID + "|" + r.SKU + "|" + r.Start.Format(time.RFC3339)
		if w, ok := want[k]; !ok {
			t.Fatalf("sub-window produced a record the full window does not have: %s", k)
		} else if w != r.Quantity {
			t.Fatalf("%s: sub-window quantity %v, full window %v", k, r.Quantity, w)
		}
	}
}

// TestNoNegativeOrZeroQuantities — a usage row with a non-positive quantity is
// never a fact, it is a generator bug that would rate as a negative charge.
func TestNoNegativeOrZeroQuantities(t *testing.T) {
	for _, out := range generateAll(defaultScenario()) {
		for _, r := range out.Records {
			if r.Quantity <= 0 || math.IsNaN(r.Quantity) || math.IsInf(r.Quantity, 0) {
				t.Fatalf("%s %s %s at %s: quantity %v", out.Customer.Slug, r.ResourceID, r.SKU, r.Start, r.Quantity)
			}
			if r.Quantity != Round6(r.Quantity) {
				t.Fatalf("%s %s: quantity %v is not rounded to 6 decimals", out.Customer.Slug, r.SKU, r.Quantity)
			}
		}
	}
}

// TestWindowEndExcludesSeptember — the whole point of the exercise: the real
// data starts on 2 September and no synthetic row may touch 1 September or
// later.
func TestWindowEndExcludesSeptember(t *testing.T) {
	cut := date(2026, 9, 1, 0)
	for _, out := range generateAll(defaultScenario()) {
		for _, r := range out.Records {
			if !r.Start.Before(cut) {
				t.Fatalf("%s: record starts at %s, on or after 2026-09-01", out.Customer.Slug, r.Start)
			}
			if r.End.After(cut) {
				t.Fatalf("%s: record ends at %s, after 2026-09-01", out.Customer.Slug, r.End)
			}
		}
		for _, res := range out.Resources {
			if res.LastSeen.After(cut) || res.DeletedAt.After(cut) {
				t.Fatalf("%s: resource %s last_seen %s deleted %s after the cut", out.Customer.Slug, res.ID, res.LastSeen, res.DeletedAt)
			}
			if res.DeletedAt.IsZero() {
				t.Fatalf("%s: resource %s is never decommissioned", out.Customer.Slug, res.ID)
			}
		}
	}
}

// A window that runs past the cut must still stop at each customer's Left —
// the guard has to live in the generator, not only in the default window.
func TestWindowPastTheCutStillStopsAtDecommission(t *testing.T) {
	s := DefaultScenario(Window{From: date(2026, 6, 1, 0), To: date(2026, 10, 1, 0)}, DefaultSeed)
	cut := date(2026, 9, 1, 0)
	for _, out := range generateAll(s) {
		for _, r := range out.Records {
			if !r.Start.Before(cut) {
				t.Fatalf("%s: an over-long window leaked a record at %s", out.Customer.Slug, r.Start)
			}
		}
	}
}

// TestAugust22SpikeIsDetected runs the PRODUCT's own detector over the
// generated series, exactly as the API does for the (customer, kind) pair, and
// requires 22 August to be flagged at z ≥ 3 against its trailing 14 days.
// Asserting through internal/anomaly rather than a local z-score is what makes
// this a test of the showcase and not of a copy of the rule.
func TestAugust22SpikeIsDetected(t *testing.T) {
	s := defaultScenario()
	c := s.Customer("gulf-retail")
	if c == nil {
		t.Fatal("gulf-retail is not in the scenario")
	}
	series := DailyCostByKind(s.Generate(c).Records, Prices(NationalCloudRates), "eip")
	pts := make([]anomaly.DayValue, 0, len(series))
	for _, d := range series {
		pts = append(pts, anomaly.DayValue{Day: d.Day, Value: d.Value})
	}
	// The promo of 14-16 July is a genuine cost event and the detector is
	// right to flag it too; anything OUTSIDE the two scripted events would be
	// noise the showcase does not intend, so it fails the test.
	scripted := map[string]bool{"2026-07-14": true, "2026-07-15": true, "2026-07-16": true}
	var flag *anomaly.Flag
	for _, f := range anomaly.Detect(pts, anomaly.Options{}) {
		switch {
		case f.Day == "2026-08-22":
			cp := f
			flag = &cp
		case scripted[f.Day]:
		default:
			t.Errorf("anomaly on %s belongs to no scripted event: actual %.2f expected %.2f", f.Day, f.Actual, f.Expected)
		}
	}
	if flag == nil {
		t.Fatal("the 22 August bandwidth spike was NOT flagged by the product's anomaly rule")
	}
	if flag.Score < 3 {
		t.Fatalf("22 August scored %.2f, below the z >= 3 the rule requires", flag.Score)
	}
	if flag.Actual <= flag.Expected {
		t.Fatalf("22 August actual %.2f is not above the baseline %.2f", flag.Actual, flag.Expected)
	}
}

// TestPlanSwitchDaysProduceExactly24PlanHours — on the day a plan changes the
// Organization must still be billed for 24 plan-hours in total, split between
// the old and the new plan at the switch hour. A double-billed or dropped hour
// on the switch day is the classic off-by-one this pins.
func TestPlanSwitchDaysProduceExactly24PlanHours(t *testing.T) {
	s := defaultScenario()
	for _, c := range s.Customers {
		if len(c.PlanSwitches) == 0 {
			continue
		}
		recs := s.Generate(c).Records
		perDay := map[string]float64{}
		perDaySKU := map[string]map[string]float64{}
		for _, r := range recs {
			if r.ResourceKind != "plan" {
				continue
			}
			d := r.Start.Format("2006-01-02")
			perDay[d] += r.Quantity
			if perDaySKU[d] == nil {
				perDaySKU[d] = map[string]float64{}
			}
			perDaySKU[d][r.SKU] += r.Quantity
		}
		// Every whole day inside the customer's life carries 24 plan-hours.
		for d := c.Joined; d.Before(c.Left); d = d.AddDate(0, 0, 1) {
			key := d.Format("2006-01-02")
			if got := perDay[key]; math.Abs(got-24) > 1e-9 {
				t.Errorf("%s %s: %v plan-hours, want 24", c.Slug, key, got)
			}
		}
		// On each switch day the split matches the switch hour.
		for i, sw := range c.PlanSwitches {
			if i == 0 {
				continue
			}
			key := sw.At.Format("2006-01-02")
			prev := c.PlanSwitches[i-1].Slug
			hour := sw.At.Hour()
			if got := perDaySKU[key]["plan."+prev]; math.Abs(got-float64(hour)) > 1e-9 {
				t.Errorf("%s %s: %v hours on the outgoing %s, want %d", c.Slug, key, got, prev, hour)
			}
			if got := perDaySKU[key]["plan."+sw.Slug]; math.Abs(got-float64(24-hour)) > 1e-9 {
				t.Errorf("%s %s: %v hours on the incoming %s, want %d", c.Slug, key, got, sw.Slug, 24-hour)
			}
		}
	}
}

// A suspended Organization pays its plan and meters nothing else: Sohar Ports
// is suspended 18-24 July.
func TestSuspendedOrganizationStillPaysItsPlan(t *testing.T) {
	s := defaultScenario()
	c := s.Customer("sohar-ports")
	recs := s.Generate(c).Records
	from, to := date(2026, 7, 18, 0), date(2026, 7, 25, 0)
	plan, other := 0.0, 0.0
	for _, r := range recs {
		if !InRange(r.Start, from, to) {
			continue
		}
		if r.ResourceKind == "plan" {
			plan += r.Quantity
			continue
		}
		other += r.Quantity
	}
	if want := 7 * 24.0; math.Abs(plan-want) > 1e-9 {
		t.Errorf("suspended week billed %v plan-hours, want %v", plan, want)
	}
	if other != 0 {
		t.Errorf("suspended week metered %v of non-plan usage, want none", other)
	}
}

// TestPlatformOrganizationsBillExactlyTheirPlan — the k8s.* meters are the
// allocation basis and carry no price, so an Organization's bill is its plan
// and nothing else (DESIGN.md §2.8).
func TestPlatformOrganizationsBillExactlyTheirPlan(t *testing.T) {
	s := defaultScenario()
	prices := Prices(NationalCloudRates, PlanRates)
	for _, c := range s.Customers {
		if c.Layer != LayerPlatform {
			continue
		}
		var planCost, otherCost float64
		for _, r := range s.Generate(c).Records {
			cost, ok := Cost(r, prices)
			if !ok {
				continue
			}
			if r.ResourceKind == "plan" {
				planCost += cost
			} else {
				otherCost += cost
			}
		}
		if otherCost != 0 {
			t.Errorf("%s: %v OMR of priced non-plan usage; the k8s meters must stay unpriced", c.Slug, otherCost)
		}
		if planCost <= 0 {
			t.Errorf("%s: no plan revenue at all", c.Slug)
		}
	}
}

// TestCloudMonthlyTotalsAreRealistic — full calendar months of the three cloud
// customers land in the founder's 1,500-4,000 OMR band. Gulf Retail's June is
// the one deliberate exception: see the comment below.
func TestCloudMonthlyTotalsAreRealistic(t *testing.T) {
	s := defaultScenario()
	prices := Prices(NationalCloudRates)
	for _, c := range s.Customers {
		if c.Layer != LayerCloud {
			continue
		}
		for period, total := range MonthlyCost(s.Generate(c).Records, prices) {
			from, to, err := MonthBounds(period)
			if err != nil {
				t.Fatal(err)
			}
			// Only whole months the customer was present for are comparable;
			// Dhofar Logistics joined mid-June and left on 25 August.
			if c.Joined.After(from) || c.Left.Before(to) {
				continue
			}
			// Gulf Retail's June is 1,418 OMR — below the band by 5 %. It
			// cannot be otherwise: 1,500 is 83 % of the 1,800 budget, so a
			// June inside the band would cross the 80 % threshold and destroy
			// the 50 -> 80 -> 100 % escalation the budget is there to show.
			// The escalation is the explicitly specified behaviour, so it wins,
			// and the shortfall is recorded here rather than hidden.
			if c.Slug == SlugPrefix+"gulf-retail" && period == "2026-06" {
				if total < 1350 || total > 1450 {
					t.Errorf("%s %s: %.2f OMR, expected the documented ~1,418", c.Slug, period, total)
				}
				continue
			}
			if total < 1500 || total > 4000 {
				t.Errorf("%s %s: %.2f OMR is outside the 1,500-4,000 band", c.Slug, period, total)
			}
		}
	}
}

// TestGulfRetailBudgetEscalation pins the founder's budget story exactly: the
// 1,800 OMR cap reaches 80 % in July without reaching 100 %, and reaches 100 %
// in August.
func TestGulfRetailBudgetEscalation(t *testing.T) {
	s := defaultScenario()
	c := s.Customer("gulf-retail")
	if len(c.Budgets) != 1 || c.Budgets[0].Amount != 1800 {
		t.Fatalf("expected one 1,800 OMR budget, got %+v", c.Budgets)
	}
	b := c.Budgets[0]
	crossed := map[string][]int{}
	for _, cr := range Crossings(s.Generate(c).Records, Prices(NationalCloudRates), b.Amount, b.Thresholds) {
		crossed[cr.Period] = append(crossed[cr.Period], cr.Threshold)
	}
	want := map[string][]int{
		"2026-06": {50},
		"2026-07": {50, 80},
		"2026-08": {50, 80, 100},
	}
	for period, ths := range want {
		got := crossed[period]
		if len(got) != len(ths) {
			t.Fatalf("%s crossed %v, want %v", period, got, ths)
		}
		for i := range ths {
			if got[i] != ths[i] {
				t.Fatalf("%s crossed %v, want %v", period, got, ths)
			}
		}
	}
}

// TestMigrationOverlap — Muscat Health's two ECS generations must overlap on
// 15-17 July and nowhere else: the old four are gone by 18 July, the new eight
// have all arrived.
func TestMigrationOverlap(t *testing.T) {
	s := defaultScenario()
	c := s.Customer("muscat-health")
	byHour := map[time.Time]map[string]float64{}
	for _, r := range s.Generate(c).Records {
		if r.ResourceKind != "ecs" {
			continue
		}
		if byHour[r.Start] == nil {
			byHour[r.Start] = map[string]float64{}
		}
		byHour[r.Start][r.SKU] += r.Quantity
	}
	check := func(at time.Time, oldGen, newGen float64) {
		t.Helper()
		got := byHour[at]
		if got["ecs.m7n.2xlarge.8"] != oldGen || got["ecs.m7n.xlarge.8"] != newGen {
			t.Errorf("%s: old=%v new=%v, want old=%v new=%v", at.Format(time.RFC3339),
				got["ecs.m7n.2xlarge.8"], got["ecs.m7n.xlarge.8"], oldGen, newGen)
		}
	}
	check(date(2026, 7, 1, 12), 4, 0)  // before the migration
	check(date(2026, 7, 15, 12), 4, 3) // first wave landed, old still running
	check(date(2026, 7, 17, 12), 4, 8) // all three waves landed, still overlapping
	check(date(2026, 7, 18, 12), 0, 8) // old generation retired
	check(date(2026, 8, 20, 12), 0, 8) // steady state
}

// TestDhofarBatchWindow — the burst servers run only between 02:00 and 06:00
// UTC, and the burst is 6-14 servers wide.
func TestDhofarBatchWindow(t *testing.T) {
	s := defaultScenario()
	c := s.Customer("dhofar-logistics")
	perHour := map[time.Time]int{}
	for _, r := range s.Generate(c).Records {
		if r.ResourceKind == "ecs" && strings.Contains(r.Labels["name"], "-batch-") {
			perHour[r.Start]++
		}
	}
	minSeen, maxSeen := 99, 0
	for hour, n := range perHour {
		if !HourIn(hour, 2, 6) {
			t.Fatalf("a batch server ran at %s, outside 02:00-06:00", hour.Format(time.RFC3339))
		}
		if n < minSeen {
			minSeen = n
		}
		if n > maxSeen {
			maxSeen = n
		}
	}
	if minSeen < 6 || maxSeen > 14 {
		t.Errorf("burst width %d..%d, want within 6..14", minSeen, maxSeen)
	}
	if minSeen == maxSeen {
		t.Errorf("burst width never varied (always %d); the batch is not bursty", minSeen)
	}
}

// TestEveryRecordCarriesTheSyntheticMark — the purge depends on it, so an
// unmarked row is a row that would survive --purge forever.
func TestEveryRecordCarriesTheSyntheticMark(t *testing.T) {
	for _, out := range generateAll(defaultScenario()) {
		if !IsSyntheticSlug(out.Customer.Slug) {
			t.Errorf("customer slug %q is not purgeable", out.Customer.Slug)
		}
		for _, r := range out.Records {
			if !IsSyntheticLabels(r.Labels) {
				t.Fatalf("%s %s at %s carries no synthetic label", out.Customer.Slug, r.SKU, r.Start)
			}
		}
		for _, res := range out.Resources {
			if res.Attrs[LabelKey] != LabelValue {
				t.Fatalf("%s inventory %s carries no synthetic mark", out.Customer.Slug, res.ID)
			}
		}
		for _, d := range out.Customer.Discounts {
			if !IsSyntheticName(d.Name) {
				t.Errorf("discount %q is not purgeable", d.Name)
			}
		}
		for _, b := range out.Customer.Budgets {
			if !IsSyntheticName(b.Name) {
				t.Errorf("budget %q is not purgeable", b.Name)
			}
		}
	}
	for _, d := range defaultScenario().GlobalDiscounts {
		if !IsSyntheticName(d.Name) {
			t.Errorf("global campaign %q is not purgeable", d.Name)
		}
	}
}

// TestPurgeSelectorsMatchOnlySynthetic — the Go predicates and the SQL
// predicates the command runs must agree, and neither may match a real row.
// The strings below are what a live database actually holds: the hw307
// customers, the plan book, an Organization's own discount.
func TestPurgeSelectorsMatchOnlySynthetic(t *testing.T) {
	real := []string{"acmewalk307", "openova", "acme-e2e", "demo", "demonstration", "hw307-demo"}
	for _, slug := range real {
		if IsSyntheticSlug(slug) {
			t.Errorf("the purge would delete the real customer %q", slug)
		}
	}
	for _, slug := range []string{"demo-gulf-retail", "demo-nizwa-fintech", "demo-x"} {
		if !IsSyntheticSlug(slug) {
			t.Errorf("the purge would leave the synthetic customer %q behind", slug)
		}
	}
	for _, name := range []string{"Launch campaign", "demo", "demo:", "Ramadan 10%"} {
		if IsSyntheticName(name) {
			t.Errorf("the purge would delete the real discount/budget %q", name)
		}
	}
	if !IsSyntheticName(NamePrefix + "launch campaign 5%") {
		t.Error("the purge would leave the synthetic campaign behind")
	}
	for _, labels := range []map[string]string{nil, {}, {"name": "web-1"}, {"synthetic": "false"}} {
		if IsSyntheticLabels(labels) {
			t.Errorf("the purge would delete a real usage record with labels %v", labels)
		}
	}
	if !IsSyntheticLabels(map[string]string{LabelKey: LabelValue}) {
		t.Error("the purge would leave synthetic usage behind")
	}
	// The SQL the command runs must encode the same rule as the Go predicate:
	// a prefix plus at least one more character.
	for _, tc := range []struct{ pred, prefix string }{
		{SQLCustomerPredicate, SlugPrefix},
		{SQLNamePredicate, NamePrefix},
	} {
		if !strings.Contains(tc.pred, tc.prefix+"_%") {
			t.Errorf("SQL predicate %q does not require a character after %q", tc.pred, tc.prefix)
		}
	}
	for _, pred := range []string{SQLUsagePredicate, SQLInventoryPredicate} {
		if !strings.Contains(pred, LabelKey) || !strings.Contains(pred, LabelValue) {
			t.Errorf("SQL predicate %q does not test the synthetic mark", pred)
		}
	}
}

// TestPlanRatesMatchStore pins the plan prices to the product's own book, so
// the showcase can never bill a plan at a rate the platform does not charge.
func TestPlanRatesMatchStore(t *testing.T) {
	want := map[string]string{}
	for _, it := range store.PlanBookItems() {
		want[it.SKU] = string(it.UnitPrice)
	}
	if len(want) != len(PlanRates) {
		t.Fatalf("store prices %d plans, synth knows %d", len(want), len(PlanRates))
	}
	for _, r := range PlanRates {
		w, ok := want[r.SKU]
		if !ok {
			t.Errorf("%s is not a plan the store prices", r.SKU)
			continue
		}
		if got := fmt.Sprintf("%.8f", r.UnitPrice()); got != w {
			t.Errorf("%s: synth %s, store %s", r.SKU, got, w)
		}
		if r.Unit != store.PlanUnit {
			t.Errorf("%s: unit %s, store %s", r.SKU, r.Unit, store.PlanUnit)
		}
	}
}

// TestNationalCloudRatesMatchTheWalkedBook — the eight hourly rates are the
// ones the hw307 book rated the August 2026 statement with
// (docs/sessions/2026-08-31/chargeback-walk/statement-2026-08.csv). If a rate
// here drifts, the showcase stops being comparable with the real statement.
func TestNationalCloudRatesMatchTheWalkedBook(t *testing.T) {
	walked := map[string]float64{
		"ecs.m7n.2xlarge.8":  0.29983790,
		"ecs.m7n.xlarge.8":   0.15213927,
		"ecs.s7n.2xlarge.2":  0.14621575,
		"eip":                0.02853881,
		"eip.bandwidth_mbps": 0.01716667,
		"elb":                0.01923059,
		"evs.ssd.gb":         0.00022831,
		"nat.1":              0.11322489,
	}
	got := Prices(NationalCloudRates)
	for sku, want := range walked {
		if math.Abs(got[sku]-want) > 5e-9 {
			t.Errorf("%s: %.8f, the hw307 statement rated at %.8f", sku, got[sku], want)
		}
	}
}

// TestMonthsCoverTheStory — statements are run for exactly the months a
// customer had usage in.
func TestMonthsCoverTheStory(t *testing.T) {
	s := defaultScenario()
	want := map[string][]string{
		"demo-gulf-retail":      {"2026-06", "2026-07", "2026-08"},
		"demo-muscat-health":    {"2026-06", "2026-07", "2026-08"},
		"demo-dhofar-logistics": {"2026-06", "2026-07", "2026-08"},
		"demo-nizwa-fintech":    {"2026-06", "2026-07", "2026-08"},
	}
	for slug, months := range want {
		got := s.Months(s.Customer(slug))
		if strings.Join(got, ",") != strings.Join(months, ",") {
			t.Errorf("%s: months %v, want %v", slug, got, months)
		}
	}
	// Dhofar joined on 20 June, so its June statement covers 11 days only.
	c := s.Customer("dhofar-logistics")
	recs := InPeriod(s.Generate(c).Records, "2026-06")
	for _, r := range recs {
		if r.Start.Day() < 20 {
			t.Fatalf("dhofar has a June record on day %d, before it joined", r.Start.Day())
		}
	}
	if len(recs) == 0 {
		t.Fatal("dhofar has no June usage at all")
	}
}

// TestCSVIsStableAndComplete — the chunk the command uploads round-trips the
// fields the ledger needs.
func TestCSVIsStableAndComplete(t *testing.T) {
	s := defaultScenario()
	recs := InPeriod(s.Generate(s.Customer("gulf-retail")).Records, "2026-07")
	var a, b bytes.Buffer
	if err := WriteCSV(&a, recs); err != nil {
		t.Fatal(err)
	}
	if err := WriteCSV(&b, recs); err != nil {
		t.Fatal(err)
	}
	if a.String() != b.String() {
		t.Fatal("the CSV encoder is not deterministic")
	}
	lines := strings.Split(strings.TrimRight(a.String(), "\n"), "\n")
	if len(lines) != len(recs)+1 {
		t.Fatalf("%d CSV lines for %d records", len(lines), len(recs))
	}
	if lines[0] != strings.Join(CSVHeader, ",") {
		t.Fatalf("header %q", lines[0])
	}
	if !strings.Contains(lines[1], `""synthetic"":""true""`) && !strings.Contains(lines[1], `"synthetic":"true"`) {
		t.Fatalf("the first data row carries no synthetic label: %s", lines[1])
	}
}
