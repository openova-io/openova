package finance

import (
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Gateway settlement reconciliation (DESIGN.md §18.3): the four buckets on a
// seeded file, fees carried through, and a duplicate gateway reference
// REPORTED rather than matched a second time.

const settlementFile = `gateway_reference,amount,currency,settled_date,fee
GW-1,100.000000,OMR,2026-08-05,2.500000
GW-2,49.000000,OMR,2026-08-06,1.000000
GW-3,25.000000,OMR,2026-08-07,0.750000
GW-1,100.000000,OMR,2026-08-08,2.500000
`

func day(d int) time.Time { return time.Date(2026, 8, d, 12, 0, 0, 0, time.UTC) }

func ledger() []store.SettledPayment {
	return []store.SettledPayment{
		// GW-1 agrees.
		{PaymentID: 1, Reference: "GW-1", Amount: "100.000000", Currency: "OMR", PaidAt: day(5), Gateway: "omantel", CustomerID: "c1", CustomerName: "ACME LLC"},
		// GW-2 disagrees: we recorded 50, the gateway settled 49.
		{PaymentID: 2, Reference: "GW-2", Amount: "50.000000", Currency: "OMR", PaidAt: day(6), Gateway: "omantel", CustomerID: "c1", CustomerName: "ACME LLC"},
		// GW-9 is ours alone — the gateway has not settled it.
		{PaymentID: 4, Reference: "GW-9", Amount: "10.000000", Currency: "OMR", PaidAt: day(9), Gateway: "omantel", CustomerID: "c2", CustomerName: "Globex"},
	}
}

func runOf(t *testing.T) store.ReconciliationRun {
	t.Helper()
	lines, err := ParseSettlementCSV(strings.NewReader(settlementFile))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return Reconcile("omantel", store.ReconcileFromFile, "aug.csv", "2026-08-01", "2026-08-31", lines, ledger(), "ops@sovereign.example")
}

func bucket(run store.ReconciliationRun, name string) []store.ReconciliationLine {
	out := []store.ReconciliationLine{}
	for _, l := range run.Lines {
		if l.Bucket == name {
			out = append(out, l)
		}
	}
	return out
}

func TestTheFourBucketsOnASeededFile(t *testing.T) {
	run := runOf(t)
	if run.Matched != 1 || run.Mismatched != 1 || run.MissingInLedger != 1 || run.MissingInSettlement != 1 {
		t.Fatalf("buckets: matched=%d mismatched=%d missing-in-ledger=%d missing-in-settlement=%d",
			run.Matched, run.Mismatched, run.MissingInLedger, run.MissingInSettlement)
	}
	m := bucket(run, store.BucketMatched)
	if len(m) != 1 || m[0].GatewayReference != "GW-1" || m[0].PaymentID != 1 {
		t.Fatalf("matched bucket: %+v", m)
	}
	// The mismatch reports BOTH figures and the difference; neither side is
	// believed and nothing is corrected.
	mm := bucket(run, store.BucketAmountMismatch)
	if len(mm) != 1 || mm[0].GatewayReference != "GW-2" {
		t.Fatalf("mismatch bucket: %+v", mm)
	}
	if mm[0].SettledAmount == nil || *mm[0].SettledAmount != "49.000000" || mm[0].LedgerAmount == nil || *mm[0].LedgerAmount != "50.000000" {
		t.Fatalf("the mismatch does not carry both figures: %+v", mm[0])
	}
	if mm[0].Difference == nil || *mm[0].Difference != "-1.000000" {
		t.Fatalf("difference: %+v", mm[0].Difference)
	}
	if !strings.Contains(mm[0].Detail, "49.000000") || !strings.Contains(mm[0].Detail, "50.000000") {
		t.Fatalf("the detail does not name both figures: %s", mm[0].Detail)
	}
	if gw := bucket(run, store.BucketMissingInLedger); len(gw) != 1 || gw[0].GatewayReference != "GW-3" {
		t.Fatalf("missing-in-ledger: %+v", gw)
	}
	if ours := bucket(run, store.BucketMissingInSettlement); len(ours) != 1 || ours[0].PaymentID != 4 {
		t.Fatalf("missing-in-settlement: %+v", ours)
	}
}

func TestADuplicateReferenceIsReportedNotMatchedTwice(t *testing.T) {
	run := runOf(t)
	dup := bucket(run, store.BucketDuplicate)
	if run.Duplicates != 1 || len(dup) != 1 || dup[0].GatewayReference != "GW-1" {
		t.Fatalf("duplicates: %d %+v", run.Duplicates, dup)
	}
	if !strings.Contains(dup[0].Detail, "more than once") {
		t.Fatalf("the duplicate does not say why: %s", dup[0].Detail)
	}
	// The second GW-1 line must not raise the matched count, the settled
	// total or the fee total — that is exactly how a reconciliation doubles
	// a cash figure.
	if run.Matched != 1 {
		t.Fatalf("a duplicate was matched again: matched=%d", run.Matched)
	}
	if run.SettledTotal != "174.000000" {
		t.Fatalf("settled total counted the duplicate: %s", run.SettledTotal)
	}
	if run.FeeTotal != "3.500000" {
		t.Fatalf("fee total counted the duplicate: %s", run.FeeTotal)
	}
	if dup[0].Fee != "0.000000" {
		t.Fatalf("the duplicate line carries a fee: %s", dup[0].Fee)
	}
}

// The fee rides on the line that matched, which is what the journal turns
// into its own pair of lines.
func TestFeesRideOnTheMatchedLines(t *testing.T) {
	run := runOf(t)
	m := bucket(run, store.BucketMatched)[0]
	if m.Fee != "2.500000" {
		t.Fatalf("matched fee %s", m.Fee)
	}
	mm := bucket(run, store.BucketAmountMismatch)[0]
	if mm.Fee != "1.000000" {
		t.Fatalf("mismatched fee %s", mm.Fee)
	}
	if gw := bucket(run, store.BucketMissingInLedger)[0]; gw.Fee != "0.000000" {
		t.Fatalf("a fee was recorded for a settlement with no payment behind it: %s", gw.Fee)
	}
}

// A payment with no gateway reference cannot be matched to anything, and the
// report says so rather than leaving the operator to guess.
func TestAPaymentWithNoReferenceSaysWhyItCannotMatch(t *testing.T) {
	lines, err := ParseSettlementCSV(strings.NewReader("gateway_reference,amount\nGW-1,100\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	run := Reconcile("omantel", store.ReconcileFromFile, "", "", "", lines,
		[]store.SettledPayment{{PaymentID: 5, Reference: "  ", Amount: "10.000000", PaidAt: day(3)}}, "ops@sovereign.example")
	ours := bucket(run, store.BucketMissingInSettlement)
	if len(ours) != 1 || !strings.Contains(ours[0].Detail, "no gateway reference") {
		t.Fatalf("unreferenced payment: %+v", ours)
	}
}

func TestParserAcceptsAliasesAndRejectsRubbish(t *testing.T) {
	lines, err := ParseSettlementCSV(strings.NewReader("transaction_id,settled_amount,currency_code,value_date,fees\nX-1,\"1,200.50\",omr,05/08/2026,3\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(lines) != 1 || lines[0].Reference != "X-1" || lines[0].Amount != "1200.500000" || lines[0].Currency != "OMR" || lines[0].Fee != "3.000000" {
		t.Fatalf("aliases: %+v", lines)
	}
	if lines[0].SettledAt.Format("2006-01-02") != "2026-08-05" {
		t.Fatalf("date: %s", lines[0].SettledAt)
	}
	for name, body := range map[string]string{
		"no reference column": "amount\n10\n",
		"no amount column":    "gateway_reference\nGW-1\n",
		"header only":         "gateway_reference,amount\n",
		"amount not a number": "gateway_reference,amount\nGW-1,tomorrow\n",
		"blank reference":     "gateway_reference,amount\n ,10\n",
	} {
		if _, err := ParseSettlementCSV(strings.NewReader(body)); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

// Nothing in a run touches the ledger: the input payments come back
// unchanged, because reconciliation reports and a human decides.
func TestReconcileCorrectsNothing(t *testing.T) {
	payments := ledger()
	before := append([]store.SettledPayment{}, payments...)
	runOf(t)
	_ = Reconcile("omantel", store.ReconcileFromFile, "", "", "", nil, payments, "ops@sovereign.example")
	for i := range payments {
		if payments[i] != before[i] {
			t.Fatalf("payment %d was modified by a reconciliation: %+v vs %+v", i, payments[i], before[i])
		}
	}
}
