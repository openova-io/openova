package synth

import (
	"math"
	"sort"
	"strings"
	"time"
)

// AnnualDivisor is hours per year: unit_price = annual_price / 8760, the
// default of every price book in the product.
const AnnualDivisor = 8760

// Rate is one price-book item as an annual list price.
type Rate struct {
	SKU         string
	Unit        string
	Annual      float64
	Description string
}

// UnitPrice is the per-hour rate at the 8 decimals price_items carry.
func (r Rate) UnitPrice() float64 { return math.Round(r.Annual/AnnualDivisor*1e8) / 1e8 }

// CloudBookName is the name earlier versions of the seeding command CREATED
// for the cloud-layer sources, and it is now only ever reused, never made.
//
// Making it was the defect (founder, hw307, 2026-09-10). The operator's own
// card on that Sovereign is called "National Cloud 2026 list" and carries 134
// items; the seeder made "National Cloud list 2026" with the nine below. One
// word apart, and NOT equivalent: nat.1 was 0.11322489 against the operator's
// 0.06037935 — nearly double — bill_stopped was `compute` against `none`, and
// several other rates differ in the last digits because the two were derived
// independently. The showcase was therefore priced from a different rate card
// than the real customer sitting next to it in the same console, so the demo
// was not comparing like with like.
//
// The command now RESOLVES a book (cmd/seed-history/book.go) and creates one
// only when nothing can be resolved — under ShowcaseCloudBookName, which no
// operator would mistake for their own.
const CloudBookName = "National Cloud list 2026"

// ShowcaseCloudBookName is what the command calls a cloud book it has to
// create because the Sovereign has none to borrow. It says who made it, so a
// later reader never has to guess whether it is the operator's card.
const ShowcaseCloudBookName = "Showcase cloud rates (seed-history)"

// SeederCloudBookNames are the cloud-book names this tool has ever created.
// Only a book named one of these may be repaired away: never a book the
// seeder did not make.
func SeederCloudBookNames() []string { return []string{ShowcaseCloudBookName, CloudBookName} }

// IsSeederCloudBookName reports whether name is one of them (case-insensitive,
// trimmed: a book list is operator input).
func IsSeederCloudBookName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, n := range SeederCloudBookNames() {
		if name == strings.ToLower(n) {
			return true
		}
	}
	return false
}

// CloudBookNamePattern matches the rate cards an operator of a National Cloud
// Sovereign names their own — "National Cloud 2026 list", "national cloud
// list 2026", "National Cloud (Duqm) list". Word order and case vary, so the
// two words are matched independently and in either order.
func LooksLikeNationalCloudBook(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "national") && strings.Contains(n, "cloud")
}

// NationalCloudRates are the National Cloud list prices in OMR per year for
// the SKUs the showcase meters. The eight hourly rates below reproduce, to
// the last decimal, the unit prices the hw307 book "National Cloud list
// 2026" rated the August 2026 statement with
// (docs/sessions/2026-08-31/chargeback-walk/statement-2026-08.csv):
// ecs.m7n.2xlarge.8 0.29983790 · ecs.m7n.xlarge.8 0.15213927 · eip
// 0.02853881 · eip.bandwidth_mbps 0.01716667 · elb 0.01923059 · evs.ssd.gb
// 0.00022831 · nat.1 0.11322489 · ecs.s7n.2xlarge.2 0.14621575. The VPC is
// not charged on the National Cloud list, so vpc is priced at 0 — stated
// here because it is an assumption, not a walked number.
var NationalCloudRates = []Rate{
	{SKU: "ecs.m7n.xlarge.8", Unit: "instance-hour", Annual: 1332.74, Description: "ECS m7n.xlarge.8 — 4 vCPU 32 GB (National Cloud list)"},
	{SKU: "ecs.m7n.2xlarge.8", Unit: "instance-hour", Annual: 2626.58, Description: "ECS m7n.2xlarge.8 — 8 vCPU 64 GB (National Cloud list)"},
	{SKU: "ecs.s7n.2xlarge.2", Unit: "instance-hour", Annual: 1280.85, Description: "ECS s7n.2xlarge.2 — 8 vCPU 16 GB (National Cloud list)"},
	{SKU: "evs.ssd.gb", Unit: "gb-hour", Annual: 2.00, Description: "EVS SSD block storage per GB (National Cloud list)"},
	{SKU: "eip", Unit: "hour", Annual: 250.00, Description: "Elastic IP (National Cloud list)"},
	{SKU: "eip.bandwidth_mbps", Unit: "mbps-hour", Annual: 150.38, Description: "EIP bandwidth per Mbps (National Cloud list)"},
	{SKU: "elb", Unit: "hour", Annual: 168.46, Description: "Elastic load balancer (National Cloud list)"},
	{SKU: "nat.1", Unit: "hour", Annual: 991.85, Description: "NAT gateway, small spec (National Cloud list)"},
	{SKU: "vpc", Unit: "hour", Annual: 0, Description: "VPC — not charged on the National Cloud list (assumption)"},
}

// PlanBookName is the platform rate card OrgSync creates on every Sovereign
// (store.PlanBookName); the seeding command never re-prices it.
const PlanBookName = "OpenOva plans"

// PlanRates are the four priced catalog plans of the "OpenOva plans" book:
// monthly S 5 · M 9 · L 16 · XL 30 OMR × 12. TestPlanRatesMatchStore pins
// them to store.PlanBookItems so the two can never drift apart.
var PlanRates = []Rate{
	{SKU: "plan.s", Unit: "plan-hour", Annual: 60},
	{SKU: "plan.m", Unit: "plan-hour", Annual: 108},
	{SKU: "plan.l", Unit: "plan-hour", Annual: 192},
	{SKU: "plan.xl", Unit: "plan-hour", Annual: 360},
}

// PriceList maps a SKU to its unit price per hour.
type PriceList map[string]float64

// Prices turns rates into a price list.
func Prices(rates ...[]Rate) PriceList {
	out := PriceList{}
	for _, rs := range rates {
		for _, r := range rs {
			out[r.SKU] = r.UnitPrice()
		}
	}
	return out
}

// Cost prices one record; ok is false when the SKU is unpriced.
func Cost(r Record, p PriceList) (float64, bool) {
	up, ok := p[r.SKU]
	if !ok {
		return 0, false
	}
	return r.Quantity * up, true
}

// MonthlyCost sums priced cost per YYYY-MM.
func MonthlyCost(recs []Record, p PriceList) map[string]float64 {
	out := map[string]float64{}
	for _, r := range recs {
		if c, ok := Cost(r, p); ok {
			out[r.Start.UTC().Format("2006-01")] += c
		}
	}
	return out
}

// DayValue is one day's cost, the series shape internal/anomaly judges.
type DayValue struct {
	Day   string
	Value float64
}

// DailyCostByKind sums priced cost per UTC day for one resource kind,
// ascending by day — the series the product's anomaly detector sees for the
// (customer, kind) pair.
func DailyCostByKind(recs []Record, p PriceList, kind string) []DayValue {
	byDay := map[string]float64{}
	for _, r := range recs {
		if r.ResourceKind != kind {
			continue
		}
		if c, ok := Cost(r, p); ok {
			byDay[r.Start.UTC().Format("2006-01-02")] += c
		}
	}
	days := make([]string, 0, len(byDay))
	for d := range byDay {
		days = append(days, d)
	}
	sort.Strings(days)
	out := make([]DayValue, 0, len(days))
	for _, d := range days {
		out = append(out, DayValue{Day: d, Value: byDay[d]})
	}
	return out
}

// Crossing is a budget threshold first reached within a period.
type Crossing struct {
	Period    string
	Threshold int
	At        time.Time // the hour in which the cumulative cost reached the threshold
	Actual    float64   // cumulative cost at that hour
}

// Crossings walks the records hour by hour per month and reports, for every
// threshold, the first hour in which the month-to-date priced cost reached
// threshold % of amount — what the product's hourly budget evaluator would
// have recorded had it been running then.
func Crossings(recs []Record, p PriceList, amount float64, thresholds []int) []Crossing {
	if amount <= 0 || len(thresholds) == 0 {
		return nil
	}
	sorted := append([]Record(nil), recs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Start.Before(sorted[j].Start) })
	ths := append([]int(nil), thresholds...)
	sort.Ints(ths)
	var out []Crossing
	period := ""
	cum := 0.0
	next := 0
	flush := func(hour time.Time) {
		for next < len(ths) && cum >= amount*float64(ths[next])/100 {
			out = append(out, Crossing{Period: period, Threshold: ths[next], At: hour, Actual: cum})
			next++
		}
	}
	var hour time.Time
	for _, r := range sorted {
		pm := r.Start.UTC().Format("2006-01")
		if pm != period {
			period, cum, next = pm, 0, 0
		}
		if !r.Start.Equal(hour) {
			// A new hour begins: the previous hour's total is final.
			if !hour.IsZero() && hour.UTC().Format("2006-01") == period {
				flush(hour.Add(time.Hour))
			}
			hour = r.Start
		}
		if c, ok := Cost(r, p); ok {
			cum += c
		}
	}
	if !hour.IsZero() {
		flush(hour.Add(time.Hour))
	}
	return out
}
