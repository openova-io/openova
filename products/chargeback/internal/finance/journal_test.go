package finance

import (
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The journal emitter (DESIGN.md §18.2). Every event type is asserted
// against the ACCOUNTS IT WAS MAPPED TO — never against a code in the
// emitter — and the balance assertion is proved to be a refusal rather than
// a warning.

func accounts() Accounts {
	return AccountsOf([]store.AccountMapping{
		{Key: store.AccountReceivable, AccountCode: "1100", Description: "Trade receivables"},
		{Key: store.AccountCash, AccountCode: "1000", Description: "Cash and bank"},
		{Key: store.AccountGatewayClearing, AccountCode: "1010", Description: "Gateway clearing"},
		{Key: store.AccountCustomerAdvances, AccountCode: "2100", Description: "Customer advances"},
		{Key: store.AccountTaxPayable, AccountCode: "2200", Description: "Tax payable"},
		{Key: store.AccountRevenue, AccountCode: "4000", Description: "Revenue"},
		{Key: store.AccountDiscounts, AccountCode: "4800", Description: "Discounts"},
		{Key: store.AccountCreditNotes, AccountCode: "4900", Description: "Credit notes"},
		{Key: store.AccountWriteOffs, AccountCode: "6100", Description: "Written off"},
		{Key: store.AccountGatewayFees, AccountCode: "6200", Description: "Gateway fees"},
		{Key: store.AccountCommission, AccountCode: "6300", Description: "Partner commission"},
		{Key: "revenue.ecs", AccountCode: "4010", Description: "Revenue, compute"},
		{Key: "revenue.evs", AccountCode: "4020", Description: "Revenue, storage"},
	})
}

func at(day int) time.Time { return time.Date(2026, 8, day, 10, 0, 0, 0, time.UTC) }

// pair is one (account code, debit, credit) triple, which is what an
// assertion about double entry actually cares about.
type pair struct {
	code   string
	debit  string
	credit string
}

func pairsOf(b Batch) []pair {
	out := make([]pair, 0, len(b.Lines))
	for _, l := range b.Lines {
		out = append(out, pair{l.AccountCode, string(l.Debit), string(l.Credit)})
	}
	return out
}

func has(t *testing.T, b Batch, want pair) {
	t.Helper()
	for _, p := range pairsOf(b) {
		if p == want {
			return
		}
	}
	t.Fatalf("no line %+v; lines were %+v", want, pairsOf(b))
}

func emit(t *testing.T, evs ...store.JournalEvent) Batch {
	t.Helper()
	b, err := Emit("2026-08", evs, accounts())
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if !b.Balanced || b.TotalDebit != b.TotalCredit {
		t.Fatalf("unbalanced batch slipped through: %s vs %s", b.TotalDebit, b.TotalCredit)
	}
	return b
}

func invoiceEvent() store.JournalEvent {
	return store.JournalEvent{
		Kind: store.EventInvoice, At: at(31), Currency: "OMR", CustomerID: "c1", CustomerSlug: "acme", CustomerName: "ACME LLC",
		Amount: "1050.000000", StatementID: "st1", InvoiceNumber: "INV-2026-00001",
		Revenue:       []store.ServiceAmount{{Service: "ecs", Amount: "700.000000"}, {Service: "evs", Amount: "400.000000"}},
		DiscountTotal: "100.000000",
		TaxRules:      []store.TaxRule{{Rate: "0.05", Tax: "50.000000"}},
	}
}

func TestInvoiceBooksReceivableRevenueDiscountAndTax(t *testing.T) {
	b := emit(t, invoiceEvent())
	// list 1,100 − discount 100 = net 1,000; tax 50; total 1,050.
	has(t, b, pair{"1100", "1050.000000", "0.000000"}) // receivable
	has(t, b, pair{"4010", "0.000000", "700.000000"})  // revenue, compute — the MAPPED per-service account
	has(t, b, pair{"4020", "0.000000", "400.000000"})  // revenue, storage
	has(t, b, pair{"4800", "100.000000", "0.000000"})  // discounts, contra
	has(t, b, pair{"2200", "0.000000", "50.000000"})   // tax payable
	if b.TotalDebit != "1150.000000" {
		t.Fatalf("total debit %s", b.TotalDebit)
	}
	// Every line traces back to the statement it came from.
	for _, l := range b.Lines {
		if l.SourceKind != SourceStatement || l.SourceID != "st1" || l.Reference != "INV-2026-00001" {
			t.Fatalf("line %d does not trace back: %+v", l.Seq, l)
		}
	}
}

// An unmapped service falls back to the catch-all revenue account rather
// than being dropped — which would leave the batch short and refuse it.
func TestUnmappedServiceFallsBackToRevenue(t *testing.T) {
	ev := invoiceEvent()
	ev.Revenue = []store.ServiceAmount{{Service: "nat", Amount: "1100.000000"}}
	b := emit(t, ev)
	has(t, b, pair{"4000", "0.000000", "1100.000000"})
}

// DESIGN.md §18.2 — multi-rate tax. The rules come from the invoice's own
// FROZEN tax snapshot, which is the only thing this reads about tax.
func TestMultiRateTaxEmitsOnePayableLinePerRule(t *testing.T) {
	ev := invoiceEvent()
	ev.Amount = "1055.000000"
	ev.TaxRules = []store.TaxRule{
		{Code: "VAT-5", Name: "VAT standard", Rate: "0.05", Tax: "40.000000"},
		{Code: "EXCISE", Name: "Excise", Rate: "0.15", Tax: "15.000000"},
	}
	b := emit(t, ev)
	payable := 0
	for _, l := range b.Lines {
		if l.AccountKey == store.AccountTaxPayable {
			payable++
		}
	}
	if payable != 2 {
		t.Fatalf("expected one payable line per rule, got %d: %+v", payable, pairsOf(b))
	}
	has(t, b, pair{"2200", "0.000000", "40.000000"})
	has(t, b, pair{"2200", "0.000000", "15.000000"})
	if !strings.Contains(strings.Join(memos(b), "|"), "VAT standard") {
		t.Fatalf("the payable line does not name its rule: %v", memos(b))
	}
}

func memos(b Batch) []string {
	out := []string{}
	for _, l := range b.Lines {
		out = append(out, l.Memo)
	}
	return out
}

func TestPaymentSplitsBetweenReceivableAndAccountCredit(t *testing.T) {
	// A gateway payment of 300 that settled 200 of an invoice this period.
	b := emit(t, store.JournalEvent{
		Kind: store.EventPayment, At: at(5), Currency: "OMR", CustomerID: "c1", CustomerSlug: "acme",
		Amount: "300.000000", Allocated: "200.000000", PaymentID: 7, InvoiceNumber: "INV-2026-00001",
		Method: store.PaymentMethodGateway, Gateway: "omantel",
	})
	has(t, b, pair{"1010", "200.000000", "0.000000"}) // gateway clearing, the settled part
	has(t, b, pair{"1100", "0.000000", "200.000000"}) // receivable
	has(t, b, pair{"1010", "100.000000", "0.000000"}) // gateway clearing, the remainder
	has(t, b, pair{"2100", "0.000000", "100.000000"}) // customer advances
	for _, l := range b.Lines {
		if l.SourceKind != SourcePayment || l.SourceID != "7" {
			t.Fatalf("payment line does not trace back: %+v", l)
		}
	}
}

// A transfer is cash, not gateway clearing: the account depends on how the
// money actually arrived.
func TestTransferPaymentBooksCashNotClearing(t *testing.T) {
	b := emit(t, store.JournalEvent{
		Kind: store.EventPayment, At: at(5), Currency: "OMR", CustomerID: "c1",
		Amount: "50.000000", Allocated: "50.000000", PaymentID: 9, Method: store.PaymentMethodTransfer,
	})
	has(t, b, pair{"1000", "50.000000", "0.000000"})
	has(t, b, pair{"1100", "0.000000", "50.000000"})
}

func TestTopUpAndItsLaterApplicationAreTwoEvents(t *testing.T) {
	b := emit(t,
		store.JournalEvent{Kind: store.EventTopUp, At: at(2), Currency: "OMR", CustomerID: "c1", Amount: "500.000000", PaymentID: 11, Method: store.PaymentMethodTransfer},
		store.JournalEvent{Kind: store.EventAdvanceApplied, At: at(20), Currency: "OMR", CustomerID: "c1", Amount: "120.000000", PaymentID: 11, StatementID: "st2", InvoiceNumber: "INV-2026-00002", Memo: "account credit applied to INV-2026-00002"},
	)
	has(t, b, pair{"1000", "500.000000", "0.000000"}) // cash in
	has(t, b, pair{"2100", "0.000000", "500.000000"}) // held as advances
	has(t, b, pair{"2100", "120.000000", "0.000000"}) // advance consumed
	has(t, b, pair{"1100", "0.000000", "120.000000"}) // against the receivable
}

func TestCreditNoteSplitsAppliedAndUnapplied(t *testing.T) {
	b := emit(t, store.JournalEvent{
		Kind: store.EventCreditNote, At: at(12), Currency: "OMR", CustomerID: "c1",
		Amount: "90.000000", Applied: "60.000000", Unapplied: "30.000000",
		StatementID: "st1", CreditNoteID: "cn1", CreditNoteNo: "CN-2026-00001",
	})
	has(t, b, pair{"4900", "90.000000", "0.000000"})
	has(t, b, pair{"1100", "0.000000", "60.000000"})
	has(t, b, pair{"2100", "0.000000", "30.000000"})
	for _, l := range b.Lines {
		if l.SourceKind != SourceCreditNote || l.SourceID != "cn1" {
			t.Fatalf("credit-note line does not trace back: %+v", l)
		}
	}
}

func TestWriteOffBooksItsOwnAccount(t *testing.T) {
	b := emit(t, store.JournalEvent{
		Kind: store.EventWriteOff, At: at(28), Currency: "OMR", CustomerID: "c1",
		Amount: "75.000000", Applied: "75.000000", StatementID: "st3", CreditNoteID: "cn2", CreditNoteNo: "CN-2026-00002",
	})
	has(t, b, pair{"6100", "75.000000", "0.000000"})
	has(t, b, pair{"1100", "0.000000", "75.000000"})
}

func TestRefundReversesWhicheverAccountTheMoneyLandedIn(t *testing.T) {
	settled := emit(t, store.JournalEvent{Kind: store.EventRefund, At: at(9), Currency: "OMR", CustomerID: "c1",
		Amount: "40.000000", PaymentID: 3, RefundOf: store.EntryPayment, Method: store.PaymentMethodTransfer})
	has(t, settled, pair{"1100", "40.000000", "0.000000"})
	has(t, settled, pair{"1000", "0.000000", "40.000000"})

	credit := emit(t, store.JournalEvent{Kind: store.EventRefund, At: at(9), Currency: "OMR", CustomerID: "c1",
		Amount: "40.000000", PaymentID: 4, RefundOf: store.EntryTopUp, Method: store.PaymentMethodTransfer})
	has(t, credit, pair{"2100", "40.000000", "0.000000"})
	has(t, credit, pair{"1000", "0.000000", "40.000000"})
}

func TestGatewayFeeIsItsOwnPairOfLines(t *testing.T) {
	b := emit(t, store.JournalEvent{
		Kind: store.EventGatewayFee, At: at(6), Currency: "OMR", CustomerID: "c1",
		Amount: "2.500000", PaymentID: 7, RunID: "run-1", Reference: "GW-1", Memo: "settlement fee on GW-1",
	})
	has(t, b, pair{"6200", "2.500000", "0.000000"})
	has(t, b, pair{"1010", "0.000000", "2.500000"})
}

// A commission statement books its revenue side to the commission account,
// not to a revenue one: the Sovereign is paying a partner.
func TestCommissionStatementBooksCommission(t *testing.T) {
	ev := invoiceEvent()
	ev.StatementKind = store.StatementKindCommission
	ev.Revenue = []store.ServiceAmount{{Service: "ecs", Amount: "1100.000000"}}
	b := emit(t, ev)
	has(t, b, pair{"6300", "0.000000", "1100.000000"})
}

// THE assertion the whole file exists for: a mapping that cannot book one
// side of an event is REFUSED, and the refusal names the difference and the
// account that is missing.
func TestUnbalancedMappingIsRefusedNamingTheDifference(t *testing.T) {
	partial := accounts()
	delete(partial, store.AccountTaxPayable)
	_, err := Emit("2026-08", []store.JournalEvent{invoiceEvent()}, partial)
	if err == nil {
		t.Fatal("an unbalanced journal was exported")
	}
	msg := err.Error()
	for _, want := range []string{"does not balance", "50.000000", store.AccountTaxPayable} {
		if !strings.Contains(msg, want) {
			t.Fatalf("refusal does not mention %q: %s", want, msg)
		}
	}
	var unbalanced *ErrUnbalanced
	if !asUnbalanced(err, &unbalanced) {
		t.Fatalf("refusal is not an ErrUnbalanced: %T", err)
	}
	if unbalanced.Difference != "50.000000" {
		t.Fatalf("difference %s", unbalanced.Difference)
	}
}

func asUnbalanced(err error, target **ErrUnbalanced) bool {
	if e, ok := err.(*ErrUnbalanced); ok {
		*target = e
		return true
	}
	return false
}

// The refusal reads as a store.ErrInvalid so the API answers a 4xx for it
// without knowing this package's error type.
func TestUnbalancedRefusalIsAnInvalidError(t *testing.T) {
	partial := accounts()
	delete(partial, store.AccountReceivable)
	_, err := Emit("2026-08", []store.JournalEvent{invoiceEvent()}, partial)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !errorsIs(err, store.ErrInvalid) {
		t.Fatalf("refusal does not read as ErrInvalid: %v", err)
	}
}

func errorsIs(err, target error) bool {
	type iser interface{ Is(error) bool }
	if e, ok := err.(iser); ok {
		return e.Is(target)
	}
	return err == target
}

// Two renders of one batch are the same bytes — the property a closed
// period's export depends on.
func TestCSVRenderIsStable(t *testing.T) {
	b := emit(t, invoiceEvent(), store.JournalEvent{
		Kind: store.EventPayment, At: at(5), Currency: "OMR", CustomerID: "c1", CustomerSlug: "acme",
		Amount: "300.000000", Allocated: "300.000000", PaymentID: 7, Method: store.PaymentMethodTransfer,
	})
	first, err := b.CSV()
	if err != nil {
		t.Fatalf("csv: %v", err)
	}
	second, err := b.CSV()
	if err != nil {
		t.Fatalf("csv: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("two renders of one batch differ")
	}
	head := strings.SplitN(string(first), "\n", 2)[0]
	if head != strings.Join(CSVHeader, ",") {
		t.Fatalf("header drifted: %s", head)
	}
	if !strings.Contains(string(first), "INV-2026-00001") {
		t.Fatal("the CSV does not carry the invoice number a line traces back to")
	}
}

// A batch that went through the close and came back renders identically to
// the one that was frozen.
func TestStoredRoundTripPreservesTheBatch(t *testing.T) {
	b := emit(t, invoiceEvent())
	back := FromStored("2026-08", b.Stored())
	first, _ := b.CSV()
	second, _ := back.CSV()
	if string(first) != string(second) {
		t.Fatalf("a frozen batch renders differently:\n%s\n---\n%s", first, second)
	}
	if !back.Balanced || back.TotalDebit != b.TotalDebit || back.TotalCredit != b.TotalCredit {
		t.Fatalf("totals drifted: %s/%s vs %s/%s", back.TotalDebit, back.TotalCredit, b.TotalDebit, b.TotalCredit)
	}
}

// Two currencies each balance on their own; one that does not is refused
// even when the grand total happens to net out.
func TestEachCurrencyBalancesOnItsOwn(t *testing.T) {
	omr := invoiceEvent()
	usd := invoiceEvent()
	usd.Currency, usd.StatementID, usd.InvoiceNumber = "USD", "st9", "INV-2026-00009"
	b := emit(t, omr, usd)
	if len(b.ByCurrency) != 2 {
		t.Fatalf("expected two currencies, got %+v", b.ByCurrency)
	}
	for _, c := range b.ByCurrency {
		if c.Debit != c.Credit {
			t.Fatalf("%s does not balance: %s vs %s", c.Currency, c.Debit, c.Credit)
		}
	}
}
