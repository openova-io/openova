// Package recommend turns inventory, rates, usage and source state into
// savings and hygiene recommendations (#6867, DESIGN.md §3.7).
//
// The rules are pure: Evaluate reads an Input the store gathered and returns
// rows; nothing here touches a database, so every rule is unit-tested with
// fixtures. Savings are exact rational money rounded once at the end, like
// every rated amount in this service — a saving is a bill the customer
// would not pay, and it must agree with the rate card to the last digit.
package recommend

import (
	"fmt"
	"math"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/collector/huawei"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Rule types.
const (
	TypeStoppedInstanceBilled = "stopped-instance-billed"
	TypeUnattachedVolume      = "unattached-volume"
	TypeUnboundEIP            = "unbound-eip"
	TypeOversizedBandwidth    = "oversized-bandwidth"
	TypeLowCPU                = "low-cpu-utilisation"
	TypeUnpricedSKU           = "unpriced-sku"
	TypeStaleSource           = "stale-source"
	TypeNoPriceBook           = "no-price-book"
)

// Severities.
const (
	SeverityHigh   = "high"
	SeverityMedium = "medium"
	SeverityLow    = "low"
)

// Thresholds.
const (
	// HoursPerMonth turns an hourly rate into a monthly saving.
	HoursPerMonth = 730
	// StaleAfter is how long a verified source may go without a collection
	// tick before it is reported (the collector runs hourly).
	StaleAfter = 2 * time.Hour
	// LowCPUPct is the 7-day mean CPU % below which an instance is oversized.
	LowCPUPct = 10.0
	// MinCPUSamples is the number of hourly samples a mean needs (two days)
	// before it is trusted.
	MinCPUSamples = 48

	// Oversized-reservation thresholds (#6867).
	//
	// MinTrafficHours is how many hourly traffic samples a reservation
	// needs before it is judged — two days, like the CPU mean. Sizing a
	// pipe off one quiet afternoon is how a recommendation costs a customer
	// an outage.
	MinTrafficHours = 48
	// BandwidthHeadroom is the multiple of the BUSIEST observed hour a
	// suggested size must still cover. A pipe sized at exactly the peak has
	// no room for the next busy hour, so the suggestion doubles it.
	BandwidthHeadroom = 2.0
	// OversizedBandwidthFactor is how far above the headroom-covered peak a
	// reservation must sit before it is worth reporting. At 2 it would fire
	// on ordinary provisioning slack; at 4 the pipe is at least eight times
	// the busiest hour that was actually measured.
	OversizedBandwidthFactor = 4.0
	// MbpsPerGBHour converts one gigabyte moved in an hour into the average
	// megabits per second that took: 1 GB = 10^9 bytes = 8×10^9 bits, over
	// 3600 s, in units of 10^6 bit/s.
	MbpsPerGBHour = 8e9 / 3600 / 1e6
)

// bandwidthLadder is the ladder a suggested reservation is rounded UP to.
// A recommendation to move to 37 Mbps is not actionable; the sizes an
// operator can actually choose are round ones.
var bandwidthLadder = []float64{1, 2, 5, 10, 20, 50, 100, 200, 300, 500, 1000, 2000}

// Input is everything the rules read.
type Input struct {
	Now       time.Time
	Books     []store.CustomerBook
	Resources []store.LiveResource
	Sources   []store.SourceHealth
	Unpriced  []store.CustomerUnpricedSKU // last 30 days
	CPUUtil   []store.CPUUtilMean         // last 7 days
	// EIPTraffic is the observed outbound traffic per address or shared
	// pipe over the same window, which is what the oversized-reservation
	// rule compares a reservation against.
	EIPTraffic []store.EIPTrafficWindow
	// ReportingCurrency, when set, has Evaluate report every saving in it:
	// a saving computed from a book in another currency is divided by that
	// book's RateToBase (store.ToBase). A row whose book currency has no
	// rate keeps its book currency and carries evidence.unconverted = true,
	// so TotalIn can leave it out rather than add USD to OMR. Empty = no
	// conversion (savings in each book's own currency, as before).
	ReportingCurrency string
}

// Recommendation is one row of GET /recommendations (ui/src/api/types.ts).
type Recommendation struct {
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	Severity      string         `json:"severity"`
	CustomerID    string         `json:"customer_id"`
	CustomerName  string         `json:"customer_name"`
	ResourceID    string         `json:"resource_id,omitempty"`
	ResourceName  string         `json:"resource_name,omitempty"`
	Kind          string         `json:"kind,omitempty"`
	Title         string         `json:"title"`
	Detail        string         `json:"detail"`
	MonthlySaving store.Decimal  `json:"monthly_saving"`
	Currency      string         `json:"currency"`
	Evidence      map[string]any `json:"evidence,omitempty"`
}

// Evaluate runs every rule and returns the rows sorted by monthly saving
// descending, then severity, type and id, so the order is stable.
func Evaluate(in Input) []Recommendation {
	books := map[string]store.CustomerBook{}
	for _, b := range in.Books {
		books[b.CustomerID] = b
	}
	rows := []Recommendation{}
	rows = append(rows, resourceRules(in, books)...)
	rows = append(rows, lowCPU(in, books)...)
	rows = append(rows, oversizedBandwidth(in, books)...)
	rows = append(rows, unpricedSKUs(in, books)...)
	rows = append(rows, staleSources(in)...)
	rows = append(rows, noPriceBook(in)...)
	convertToReporting(in, books, rows)
	sort.SliceStable(rows, func(i, j int) bool {
		if c := ratOf(rows[i].MonthlySaving).Cmp(ratOf(rows[j].MonthlySaving)); c != 0 {
			return c > 0
		}
		if a, b := severityRank(rows[i].Severity), severityRank(rows[j].Severity); a != b {
			return a < b
		}
		if rows[i].Type != rows[j].Type {
			return rows[i].Type < rows[j].Type
		}
		return rows[i].ID < rows[j].ID
	})
	return rows
}

// convertToReporting rewrites each row's saving into in.ReportingCurrency
// (no-op when it is empty). A zero saving is zero in any currency and is
// simply relabelled; a non-zero saving needs the book's rate, and without
// one the row keeps its currency and is marked unconverted.
func convertToReporting(in Input, books map[string]store.CustomerBook, rows []Recommendation) {
	if in.ReportingCurrency == "" {
		return
	}
	for i := range rows {
		r := &rows[i]
		if r.Currency == "" || r.Currency == in.ReportingCurrency || ratOf(r.MonthlySaving).Sign() == 0 {
			r.Currency = in.ReportingCurrency
			continue
		}
		if v, ok := store.ToBase(r.MonthlySaving, books[r.CustomerID].RateToBase); ok {
			r.MonthlySaving, r.Currency = v, in.ReportingCurrency
			continue
		}
		if r.Evidence == nil {
			r.Evidence = map[string]any{}
		}
		r.Evidence["unconverted"] = true
	}
}

// Total sums the monthly savings exactly, whatever currency each row is in
// — meaningful only when every row shares one. Callers that converted
// (Input.ReportingCurrency) use TotalIn.
func Total(rows []Recommendation) store.Decimal {
	t := new(big.Rat)
	for _, r := range rows {
		t.Add(t, ratOf(r.MonthlySaving))
	}
	return money(t)
}

// TotalIn sums the savings of the rows in one currency, exactly; rows in
// any other currency (unconverted) are left out.
func TotalIn(rows []Recommendation, currency string) store.Decimal {
	t := new(big.Rat)
	for _, r := range rows {
		if r.Currency == currency {
			t.Add(t, ratOf(r.MonthlySaving))
		}
	}
	return money(t)
}

// UnconvertedSavings lists, per currency other than the reporting one, the
// rows whose saving could not be converted and what they sum to in that
// currency — the recommendations' twin of the explorer's unconverted list.
func UnconvertedSavings(rows []Recommendation, reporting string) []store.UnconvertedCurrency {
	sums := map[string]*store.UnconvertedCurrency{}
	var order []string
	for _, r := range rows {
		if r.Currency == "" || r.Currency == reporting {
			continue
		}
		u, ok := sums[r.Currency]
		if !ok {
			u = &store.UnconvertedCurrency{Currency: r.Currency, Cost: "0.000000"}
			sums[r.Currency] = u
			order = append(order, r.Currency)
		}
		u.Records++
		u.Cost = money(new(big.Rat).Add(ratOf(u.Cost), ratOf(r.MonthlySaving)))
	}
	sort.Strings(order)
	out := []store.UnconvertedCurrency{}
	for _, c := range order {
		out = append(out, *sums[c])
	}
	return out
}

// Currency is the currency the totals are in: the first row that carries
// one, else the first customer book in scope, else empty.
func Currency(rows []Recommendation, books []store.CustomerBook) string {
	for _, r := range rows {
		if r.Currency != "" {
			return r.Currency
		}
	}
	for _, b := range books {
		if b.HasBook && b.Currency != "" {
			return b.Currency
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// resource rules: stopped-instance-billed, unattached-volume, unbound-eip
// ---------------------------------------------------------------------------

func resourceRules(in Input, books map[string]store.CustomerBook) []Recommendation {
	var out []Recommendation
	for _, r := range in.Resources {
		book := books[r.CustomerID]
		switch r.Kind {
		case huawei.KindECS:
			status := strings.ToUpper(attrStr(r.Attrs, "status"))
			if !isStopped(status) || !book.HasBook || book.BillStopped != "compute" {
				continue
			}
			saving, skus, unpriced := resourceSaving(book, r)
			if len(unpriced) == len(skus) {
				// Nothing is billed for it: the premise of the rule does not
				// hold (the unpriced-sku rule reports the missing rate).
				continue
			}
			flavor := attrStr(r.Attrs, "flavor")
			out = append(out, Recommendation{
				ID: TypeStoppedInstanceBilled + ":" + r.CustomerID + ":" + r.ResourceID, Type: TypeStoppedInstanceBilled, Severity: SeverityHigh,
				CustomerID: r.CustomerID, CustomerName: r.CustomerName, ResourceID: r.ResourceID, ResourceName: r.Name, Kind: r.Kind,
				Title:         "Stopped instance is still billed",
				Detail:        fmt.Sprintf("%s (%s) is %s, and price book %s bills stopped compute hours (bill_stopped = compute). Delete the instance, or move the book to storage-only.", label(r), flavor, status, book.BookName),
				MonthlySaving: money(saving), Currency: book.Currency,
				Evidence: map[string]any{"status": status, "flavor": flavor, "skus": skus, "bill_stopped": book.BillStopped, "hours_per_month": HoursPerMonth},
			})
		case huawei.KindEVS:
			if attrStr(r.Attrs, "attached_to") != "" {
				continue
			}
			saving, skus, unpriced := resourceSaving(book, r)
			size := attrNum(r.Attrs, "size_gb")
			ev := map[string]any{"size_gb": size, "volume_type": attrStr(r.Attrs, "volume_type"), "status": attrStr(r.Attrs, "status"), "skus": skus, "hours_per_month": HoursPerMonth}
			if len(unpriced) > 0 {
				ev["unpriced_skus"] = unpriced
			}
			out = append(out, Recommendation{
				ID: TypeUnattachedVolume + ":" + r.CustomerID + ":" + r.ResourceID, Type: TypeUnattachedVolume, Severity: SeverityMedium,
				CustomerID: r.CustomerID, CustomerName: r.CustomerName, ResourceID: r.ResourceID, ResourceName: r.Name, Kind: r.Kind,
				Title:         "Unattached volume",
				Detail:        fmt.Sprintf("%s (%s GB %s) is attached to no instance and is billed while idle. Snapshot it and delete it.", label(r), fmtNum(size), attrStr(r.Attrs, "volume_type")),
				MonthlySaving: money(saving), Currency: book.Currency, Evidence: ev,
			})
		case huawei.KindEIP:
			// Huawei reports an EIP that is bound to no port or instance as
			// status DOWN (ACTIVE = bound and routable). That status is the
			// signal; the inventory carries no association field to test.
			status := strings.ToUpper(attrStr(r.Attrs, "status"))
			if status != "DOWN" {
				continue
			}
			saving, skus, unpriced := resourceSaving(book, r)
			bw := attrNum(r.Attrs, "bandwidth_mbps")
			ev := map[string]any{"status": status, "public_ip_address": attrStr(r.Attrs, "public_ip_address"), "bandwidth_mbps": bw, "bandwidth_name": attrStr(r.Attrs, "bandwidth_name"), "skus": skus, "hours_per_month": HoursPerMonth}
			if len(unpriced) > 0 {
				ev["unpriced_skus"] = unpriced
			}
			out = append(out, Recommendation{
				ID: TypeUnboundEIP + ":" + r.CustomerID + ":" + r.ResourceID, Type: TypeUnboundEIP, Severity: SeverityMedium,
				CustomerID: r.CustomerID, CustomerName: r.CustomerName, ResourceID: r.ResourceID, ResourceName: r.Name, Kind: r.Kind,
				Title:         "Unbound Elastic IP",
				Detail:        fmt.Sprintf("%s is DOWN (bound to nothing) and still billed for the address and %s Mbps of bandwidth. Release it.", label(r), fmtNum(bw)),
				MonthlySaving: money(saving), Currency: book.Currency, Evidence: ev,
			})
		}
	}
	return out
}

// resourceSaving is what the resource costs per month at the customer's
// rates: Σ over its SKUs of rate × multiplier × 730 h. SKUs the book does
// not price contribute nothing and are returned separately.
func resourceSaving(book store.CustomerBook, r store.LiveResource) (*big.Rat, []string, []string) {
	total := new(big.Rat)
	var skus, unpriced []string
	for _, sku := range huawei.SKUsFor(r.Kind, r.Attrs, "") {
		skus = append(skus, sku.Name)
		rate, ok := rateOf(book, sku.Name)
		if !ok {
			unpriced = append(unpriced, sku.Name)
			continue
		}
		total.Add(total, monthly(rate, sku.Multiplier))
	}
	return total, skus, unpriced
}

// ---------------------------------------------------------------------------
// low-cpu-utilisation
// ---------------------------------------------------------------------------

func lowCPU(in Input, books map[string]store.CustomerBook) []Recommendation {
	live := map[string]store.LiveResource{}
	for _, r := range in.Resources {
		live[r.SourceID+"/"+r.ResourceID] = r
	}
	var out []Recommendation
	for _, m := range in.CPUUtil {
		if m.Samples < MinCPUSamples || m.Mean >= LowCPUPct {
			continue
		}
		r, ok := live[m.SourceID+"/"+m.ResourceID]
		if !ok || r.Kind != huawei.KindECS {
			continue
		}
		flavor := attrStr(r.Attrs, "flavor")
		smaller, ok := StepDown(flavor)
		if !ok {
			continue
		}
		book := books[r.CustomerID]
		curSKU, smallSKU := "ecs."+flavor, "ecs."+smaller
		ev := map[string]any{"cpu_mean_7d_pct": math.Round(m.Mean*100) / 100, "samples": m.Samples, "flavor": flavor, "suggested_flavor": smaller, "sku": curSKU, "suggested_sku": smallSKU, "hours_per_month": HoursPerMonth}
		saving := new(big.Rat)
		cur, curOK := rateOf(book, curSKU)
		switch {
		case !curOK:
			ev["unpriced"] = true
		default:
			if small, ok := rateOf(book, smallSKU); ok {
				saving = monthly(new(big.Rat).Sub(cur, small), 1)
			} else {
				// The smaller SKU is not on the rate card: assume one size
				// step halves the price, and say so.
				saving = monthly(new(big.Rat).Quo(cur, big.NewRat(2, 1)), 1)
				ev["estimate"] = true
			}
			if saving.Sign() < 0 {
				saving = new(big.Rat)
			}
		}
		out = append(out, Recommendation{
			ID: TypeLowCPU + ":" + r.CustomerID + ":" + r.ResourceID, Type: TypeLowCPU, Severity: SeverityLow,
			CustomerID: r.CustomerID, CustomerName: r.CustomerName, ResourceID: r.ResourceID, ResourceName: r.Name, Kind: r.Kind,
			Title:         "Low CPU utilisation",
			Detail:        fmt.Sprintf("%s averaged %.1f %% CPU over %d hourly samples in the last 7 days on %s. Resize one step down to %s.", label(r), m.Mean, m.Samples, flavor, smaller),
			MonthlySaving: money(saving), Currency: book.Currency, Evidence: ev,
		})
	}
	return out
}

// sizeLadder is the doubling ladder of flavor sizes.
var sizeLadder = []string{"small", "medium", "large", "xlarge", "2xlarge", "4xlarge", "8xlarge", "16xlarge", "32xlarge", "64xlarge"}

var nxlarge = regexp.MustCompile(`^(\d+)xlarge$`)

// StepDown returns the flavor one size smaller: the size segment of
// "<family>.<size>.<mem>" moves one rung down the ladder (m7n.2xlarge.8 →
// m7n.xlarge.8); a size off the doubling ladder (3xlarge, 6xlarge, 12xlarge)
// moves to the largest rung below it. ok is false when there is no smaller
// size or the flavor has no size segment.
func StepDown(flavor string) (string, bool) {
	parts := strings.Split(flavor, ".")
	for i, p := range parts {
		smaller, isSize, ok := smallerSize(p)
		if !isSize {
			continue
		}
		if !ok {
			return "", false
		}
		parts[i] = smaller
		return strings.Join(parts, "."), true
	}
	return "", false
}

// smallerSize returns the rung below size. isSize reports whether the
// token is a size at all; ok whether a smaller one exists.
func smallerSize(size string) (smaller string, isSize, ok bool) {
	s := strings.ToLower(size)
	for i, l := range sizeLadder {
		if l == s {
			if i == 0 {
				return "", true, false
			}
			return sizeLadder[i-1], true, true
		}
	}
	m := nxlarge.FindStringSubmatch(s)
	if m == nil {
		return "", false, false
	}
	n, _ := strconv.Atoi(m[1])
	best := "xlarge"
	for _, l := range sizeLadder[4:] {
		k, _ := strconv.Atoi(strings.TrimSuffix(l, "xlarge"))
		if k < n {
			best = l
		}
	}
	if n <= 1 {
		best = "large"
	}
	return best, true, true
}

// ---------------------------------------------------------------------------
// oversized-bandwidth
// ---------------------------------------------------------------------------

// oversizedBandwidth reports a reserved pipe that dwarfs the traffic that
// actually crossed it (#6867). On a Sovereign whose reserved bandwidth is
// about forty times its compute, this is the largest saving the product can
// name — and unlike the unbound-address rule it fires on pipes that are in
// use, which is where the money actually sits.
//
// The comparison is against the BUSIEST hour observed, not the mean: a pipe
// has to carry the peak. The suggested size covers that peak with headroom
// and is then rounded up to a size an operator can actually buy.
func oversizedBandwidth(in Input, books map[string]store.CustomerBook) []Recommendation {
	live := map[string]store.LiveResource{}
	for _, r := range in.Resources {
		live[r.SourceID+"/"+r.ResourceID] = r
	}
	var out []Recommendation
	for _, tw := range in.EIPTraffic {
		if tw.Hours < MinTrafficHours {
			continue
		}
		r, ok := live[tw.SourceID+"/"+tw.ResourceID]
		if !ok || (r.Kind != huawei.KindEIP && r.Kind != huawei.KindBandwidth) {
			continue
		}
		// A traffic-billed pipe reserves nothing, so there is nothing to
		// shrink — it already pays only for what it moved.
		if huawei.BillsTraffic(r.Attrs) {
			continue
		}
		// An address on a SHARED pipe does not carry the reservation; the
		// pipe's own resource does, and that is the row this rule reports.
		if r.Kind == huawei.KindEIP && huawei.SharesBandwidth(r.Attrs) {
			continue
		}
		reserved := attrNum(r.Attrs, "bandwidth_mbps")
		if reserved <= 0 {
			continue
		}
		peakMbps := tw.PeakGB * MbpsPerGBHour
		suggested := suggestBandwidth(peakMbps)
		if suggested >= reserved || reserved < suggested*OversizedBandwidthFactor {
			continue
		}
		book := books[r.CustomerID]
		ev := map[string]any{
			"reserved_mbps": reserved, "suggested_mbps": suggested,
			"peak_hour_gb": round2(tw.PeakGB), "peak_hour_mbps": round2(peakMbps),
			"mean_hour_mbps": round2(tw.TotalGB / float64(tw.Hours) * MbpsPerGBHour),
			"total_gb":       round2(tw.TotalGB), "hours": tw.Hours,
			"sku": huawei.SKUEIPBandwidth, "headroom": BandwidthHeadroom, "hours_per_month": HoursPerMonth,
		}
		saving := new(big.Rat)
		if rate, priced := rateOf(book, huawei.SKUEIPBandwidth); priced {
			saving = monthly(rate, reserved-suggested)
		} else {
			// Without a rate the shrink is still worth doing, but this
			// product does not invent a price to make a number look big.
			ev["unpriced"] = true
		}
		out = append(out, Recommendation{
			ID: TypeOversizedBandwidth + ":" + r.CustomerID + ":" + r.ResourceID, Type: TypeOversizedBandwidth, Severity: SeverityHigh,
			CustomerID: r.CustomerID, CustomerName: r.CustomerName, ResourceID: r.ResourceID, ResourceName: r.Name, Kind: r.Kind,
			Title: "Oversized bandwidth reservation",
			Detail: fmt.Sprintf("%s reserves %s Mbps but its busiest hour in the last %d sampled hours moved %s GB — %s Mbps. Drop the reservation to %s Mbps, which still covers that peak %s times over.",
				label(r), fmtNum(reserved), tw.Hours, fmtNum(round2(tw.PeakGB)), fmtNum(round2(peakMbps)), fmtNum(suggested), fmtNum(BandwidthHeadroom)),
			MonthlySaving: money(saving), Currency: book.Currency, Evidence: ev,
		})
	}
	return out
}

// suggestBandwidth is the smallest size on the ladder that still covers the
// busiest observed hour with headroom; the top rung when the peak exceeds
// every rung, so the suggestion is never smaller than what was measured.
func suggestBandwidth(peakMbps float64) float64 {
	want := peakMbps * BandwidthHeadroom
	for _, rung := range bandwidthLadder {
		if rung >= want {
			return rung
		}
	}
	return bandwidthLadder[len(bandwidthLadder)-1]
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// ---------------------------------------------------------------------------
// unpriced-sku
// ---------------------------------------------------------------------------

func unpricedSKUs(in Input, books map[string]store.CustomerBook) []Recommendation {
	var out []Recommendation
	for _, u := range in.Unpriced {
		book, ok := books[u.CustomerID]
		// A sampled measurement is a metric, not a meter: telling the
		// operator to put a price on a gigabyte figure the cloud never
		// charged for would be wrong advice. The store's aggregates leave
		// them out for the same reason; this is the belt to that braces.
		if !ok || !book.HasBook || store.IsMetricSKU(u.SKU) {
			continue
		}
		if _, priced := rateOf(book, u.SKU); priced {
			continue
		}
		out = append(out, Recommendation{
			ID: TypeUnpricedSKU + ":" + u.CustomerID + ":" + u.SKU, Type: TypeUnpricedSKU, Severity: SeverityHigh,
			CustomerID: u.CustomerID, CustomerName: u.CustomerName,
			Title:         "Unpriced SKU " + u.SKU,
			Detail:        fmt.Sprintf("%s %s of %s across %d resources in the last 30 days has no rate in price book %s: it rates to zero on every statement.", trimDec(u.Quantity), u.Unit, u.SKU, u.Resources, book.BookName),
			MonthlySaving: "0.000000", Currency: book.Currency,
			Evidence: map[string]any{"sku": u.SKU, "quantity_30d": u.Quantity, "unit": u.Unit, "resources": u.Resources, "price_book": book.BookName},
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// stale-source
// ---------------------------------------------------------------------------

func staleSources(in Input) []Recommendation {
	var out []Recommendation
	for _, s := range in.Sources {
		var reason, title string
		switch {
		case s.Status == "failed":
			reason, title = "failed", "Source failed"
		case s.Status != "verified" || s.CustomerStatus != "active":
			// Pending sources were never collected; sources of inactive
			// customers are skipped by the collector by design.
			continue
		case s.LastError != "":
			reason, title = "error", "Source reporting errors"
		case s.LastCollectedAt == nil:
			reason, title = "never-collected", "Source never collected"
		case in.Now.Sub(*s.LastCollectedAt) > StaleAfter:
			reason, title = "stale", "Source not collecting"
		default:
			continue
		}
		name := s.Kind
		if s.Region != "" || s.ProjectID != "" {
			name = strings.TrimPrefix(s.Region+"/"+s.ProjectID, "/")
		}
		ev := map[string]any{"source_id": s.SourceID, "status": s.Status, "reason": reason, "stale_after_minutes": int(StaleAfter.Minutes())}
		detail := fmt.Sprintf("Source %s of %s ", name, s.CustomerName)
		switch reason {
		case "failed":
			detail += "failed verification"
		case "error":
			detail += "is reporting collection errors"
		case "never-collected":
			detail += "has never completed a collection"
		case "stale":
			age := in.Now.Sub(*s.LastCollectedAt).Round(time.Minute)
			ev["age_minutes"] = int(age.Minutes())
			detail += fmt.Sprintf("last collected %s ago", age)
		}
		if s.LastCollectedAt != nil {
			ev["last_collected_at"] = s.LastCollectedAt.UTC().Format(time.RFC3339)
		}
		if s.LastError != "" {
			ev["last_error"] = s.LastError
			detail += ": " + s.LastError
		}
		detail += ". Usage is not being recorded, so its statements under-bill."
		out = append(out, Recommendation{
			ID: TypeStaleSource + ":" + s.SourceID, Type: TypeStaleSource, Severity: SeverityMedium,
			CustomerID: s.CustomerID, CustomerName: s.CustomerName,
			Title: title, Detail: detail, MonthlySaving: "0.000000", Evidence: ev,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// no-price-book
// ---------------------------------------------------------------------------

// noPriceBook flags a customer none of whose sources has a book, and — since
// a book is assigned per source (DESIGN.md §2) — each individual source that
// has none while the customer's others do.
func noPriceBook(in Input) []Recommendation {
	var out []Recommendation
	for _, b := range in.Books {
		if !b.HasBook {
			out = append(out, Recommendation{
				ID: TypeNoPriceBook + ":" + b.CustomerID, Type: TypeNoPriceBook, Severity: SeverityHigh,
				CustomerID: b.CustomerID, CustomerName: b.CustomerName,
				Title:         "No price book",
				Detail:        fmt.Sprintf("%s has no price book on any of its sources: every SKU is unpriced and its statements rate to zero. Assign a rate card to each source.", b.CustomerName),
				MonthlySaving: "0.000000", Evidence: map[string]any{"customer_status": b.Status},
			})
			continue
		}
		for _, src := range b.UnbookedSources {
			out = append(out, Recommendation{
				ID: TypeNoPriceBook + ":" + b.CustomerID + ":" + src, Type: TypeNoPriceBook, Severity: SeverityHigh,
				CustomerID: b.CustomerID, CustomerName: b.CustomerName,
				Title:         "Source without a price book: " + src,
				Detail:        fmt.Sprintf("Source %s of %s has no price book: its usage is unpriced and rates to zero on every statement. Assign a rate card of the source's layer.", src, b.CustomerName),
				MonthlySaving: "0.000000", Evidence: map[string]any{"customer_status": b.Status, "source": src},
			})
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func isStopped(status string) bool {
	// The same set the cost engine bills as stopped (store.costBaseSQL).
	switch status {
	case "SHUTOFF", "STOPPED", "SHUTDOWN":
		return true
	}
	return false
}

func severityRank(s string) int {
	switch s {
	case SeverityHigh:
		return 0
	case SeverityMedium:
		return 1
	}
	return 2
}

func label(r store.LiveResource) string {
	if r.Name != "" {
		return r.Name
	}
	return r.ResourceID
}

func attrStr(attrs map[string]any, key string) string {
	switch v := attrs[key].(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

func attrNum(attrs map[string]any, key string) float64 {
	switch v := attrs[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case numberLike:
		f, _ := v.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}
	return 0
}

// numberLike lets attrs decoded with json.Decoder.UseNumber pass through.
type numberLike interface{ Float64() (float64, error) }

func fmtNum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func trimDec(d store.Decimal) string {
	s := string(d)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

func ratOf(d store.Decimal) *big.Rat {
	s := strings.TrimSpace(string(d))
	if s == "" {
		return new(big.Rat)
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return new(big.Rat)
	}
	return r
}

func rateOf(book store.CustomerBook, sku string) (*big.Rat, bool) {
	d, ok := book.Rates[sku]
	if !ok {
		return nil, false
	}
	return ratOf(d), true
}

// monthly is rate × multiplier × 730 h, exactly.
func monthly(rate *big.Rat, multiplier float64) *big.Rat {
	m := new(big.Rat).SetFloat64(multiplier)
	if m == nil {
		m = new(big.Rat)
	}
	out := new(big.Rat).Mul(rate, m)
	return out.Mul(out, big.NewRat(HoursPerMonth, 1))
}

// money renders a rational at the 6-decimal scale of every money column.
func money(r *big.Rat) store.Decimal {
	return store.Decimal(r.FloatString(6))
}
