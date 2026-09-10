package recommend

import (
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Oversized bandwidth reservations (#6867).
//
// Reserved bandwidth is about forty times compute on this Sovereign, so a
// pipe sized far above what crosses it is the largest single saving the
// product can name. It is also the one that most needs guard rails: a pipe
// has to carry the BUSIEST hour, not the average one, and a suggestion that
// is right on the mean is an outage waiting for the next busy hour.

func bwBook(rates map[string]store.Decimal) []store.CustomerBook {
	return []store.CustomerBook{{CustomerID: "c", CustomerName: "Acme", Status: "active", HasBook: true, BookName: "list", Currency: "OMR", BillStopped: "compute", Rates: rates}}
}

func bwInput(kind string, attrs map[string]any, hours int, totalGB, peakGB float64) Input {
	return Input{
		Now:        now,
		Books:      bwBook(map[string]store.Decimal{"eip.bandwidth_mbps": "0.00500000", "eip": "0.02000000"}),
		Resources:  []store.LiveResource{{CustomerID: "c", CustomerName: "Acme", SourceID: "s", ResourceID: "r1", Kind: kind, Name: "pipe", Attrs: attrs}},
		EIPTraffic: []store.EIPTrafficWindow{{CustomerID: "c", SourceID: "s", ResourceID: "r1", Kind: kind, Hours: hours, TotalGB: totalGB, PeakGB: peakGB}},
	}
}

func bwRows(in Input) []Recommendation { return byType(Evaluate(in))[TypeOversizedBandwidth] }

// TestSuggestedSizeCoversThePeakWithHeadroomOnARealLadder: the suggestion
// has to be a size an operator can actually buy, and it has to still cover
// the busiest hour that was measured.
func TestSuggestedSizeCoversThePeakWithHeadroomOnARealLadder(t *testing.T) {
	for _, tc := range []struct {
		peakMbps float64
		want     float64
	}{
		{0, 1},       // silent pipe: the smallest rung, never zero
		{0.4, 1},     // 0.8 with headroom
		{2.67, 10},   // 5.34 with headroom → the 10 rung
		{40, 100},    // 80 with headroom → the 100 rung
		{160, 500},   // 320 with headroom → the 500 rung
		{5000, 2000}, // above every rung: the largest, never smaller than measured
	} {
		got := suggestBandwidth(tc.peakMbps)
		if got != tc.want {
			t.Fatalf("suggestBandwidth(%v Mbps peak) = %v, want %v", tc.peakMbps, got, tc.want)
		}
		if tc.peakMbps <= bandwidthLadder[len(bandwidthLadder)-1] && got < tc.peakMbps {
			t.Fatalf("suggestion %v is below the measured peak %v", got, tc.peakMbps)
		}
	}
	// One gigabyte moved over an hour is 2.222 Mbps on average — the whole
	// rule reads a peak hour through this conversion, so pin it.
	if got := 1.0 * MbpsPerGBHour; got < 2.2222 || got > 2.2223 {
		t.Fatalf("1 GB in an hour = %v Mbps, want 2.2222", got)
	}
}

// TestOversizedRuleNeedsEnoughHistoryAndEnoughSlack: two thresholds, both
// of which exist to stop the rule recommending an outage.
func TestOversizedRuleNeedsEnoughHistoryAndEnoughSlack(t *testing.T) {
	attrs := map[string]any{"bandwidth_mbps": 300.0, "bandwidth_charge_mode": "bandwidth", "bandwidth_share_type": "PER", "status": "ACTIVE"}
	// A week of history on a quiet pipe: fires, and the saving is exact.
	rows := bwRows(bwInput("eip", attrs, 168, 84, 1.2))
	if len(rows) != 1 || rows[0].MonthlySaving != "1058.500000" {
		t.Fatalf("quiet 300 Mbps pipe = %+v", rows)
	}
	// One day of history is not enough to size anything.
	if got := bwRows(bwInput("eip", attrs, MinTrafficHours-1, 12, 1.2)); len(got) != 0 {
		t.Fatalf("fired on %d hours of history: %+v", MinTrafficHours-1, got)
	}
	// A pipe only modestly above its peak is provisioning slack, not waste:
	// a peak of 30 Mbps suggests 100, and 300 is exactly 3× that — under
	// the factor, so nothing is reported.
	busy := map[string]any{"bandwidth_mbps": 300.0, "bandwidth_charge_mode": "bandwidth", "bandwidth_share_type": "PER", "status": "ACTIVE"}
	if got := bwRows(bwInput("eip", busy, 168, 5000, 30/MbpsPerGBHour)); len(got) != 0 {
		t.Fatalf("fired on ordinary slack: %+v", got)
	}
	// And a pipe already at the suggested size is never told to shrink.
	right := map[string]any{"bandwidth_mbps": 10.0, "bandwidth_charge_mode": "bandwidth", "bandwidth_share_type": "PER", "status": "ACTIVE"}
	if got := bwRows(bwInput("eip", right, 168, 84, 1.2)); len(got) != 0 {
		t.Fatalf("fired on a right-sized pipe: %+v", got)
	}
}

// TestOversizedRuleReportsTheResourceThatCarriesTheReservation: a shared
// pipe's reservation is on the pipe, so that is the row the operator can
// act on; an address hanging off it reserves nothing of its own, and a
// traffic-billed address reserves nothing at all.
func TestOversizedRuleReportsTheResourceThatCarriesTheReservation(t *testing.T) {
	shared := map[string]any{"bandwidth_mbps": 300.0, "bandwidth_charge_mode": "bandwidth", "bandwidth_share_type": "WHOLE", "bandwidth_id": "bw-1", "status": "NORMAL"}
	rows := bwRows(bwInput("bandwidth", shared, 168, 84, 1.2))
	if len(rows) != 1 || rows[0].Kind != "bandwidth" || rows[0].MonthlySaving != "1058.500000" {
		t.Fatalf("shared pipe = %+v", rows)
	}
	// The same share type on an ADDRESS is not the reservation holder.
	if got := bwRows(bwInput("eip", shared, 168, 84, 1.2)); len(got) != 0 {
		t.Fatalf("an address on a shared pipe was told to shrink the pipe: %+v", got)
	}
	// A traffic-billed pipe already pays only for what it moved.
	metered := map[string]any{"bandwidth_mbps": 300.0, "bandwidth_charge_mode": "traffic", "bandwidth_share_type": "PER", "status": "ACTIVE"}
	if got := bwRows(bwInput("eip", metered, 168, 84, 1.2)); len(got) != 0 {
		t.Fatalf("a traffic-billed address was told to shrink a reservation it never made: %+v", got)
	}
	// Anything that is not an address or a pipe is not this rule's business.
	vm := map[string]any{"bandwidth_mbps": 300.0, "status": "ACTIVE"}
	if got := bwRows(bwInput("ecs", vm, 168, 84, 1.2)); len(got) != 0 {
		t.Fatalf("fired on an instance: %+v", got)
	}
}

// TestOversizedRuleWithoutARateSaysSoRatherThanInventingOne: the traffic
// meter ships unpriced on purpose (no National Cloud traffic price is
// invented here), and the reservation SKU can be unpriced too. A rule that
// guessed a rate would put a fabricated number in front of the operator.
func TestOversizedRuleWithoutARateSaysSoRatherThanInventingOne(t *testing.T) {
	in := bwInput("eip", map[string]any{"bandwidth_mbps": 300.0, "bandwidth_charge_mode": "bandwidth", "bandwidth_share_type": "PER", "status": "ACTIVE"}, 168, 84, 1.2)
	in.Books = bwBook(map[string]store.Decimal{"eip": "0.02000000"}) // no eip.bandwidth_mbps rate
	rows := bwRows(in)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].MonthlySaving != "0.000000" || rows[0].Evidence["unpriced"] != true {
		t.Fatalf("unpriced row = %s %+v", rows[0].MonthlySaving, rows[0].Evidence)
	}
	// The advice still names both sizes, which is the actionable part.
	if !strings.Contains(rows[0].Detail, "300 Mbps") || !strings.Contains(rows[0].Detail, "10 Mbps") {
		t.Fatalf("detail = %q", rows[0].Detail)
	}
}
