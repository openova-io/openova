package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// The customer account ledger, allocation, credit notes, the tax snapshot
// and universal account credit (DESIGN.md §9). Every figure is exact, and
// every assertion below fails on the lane-1 code: it had no ledger, no
// allocation, no credit note, no snapshot and no credit.

func dec(s string) store.Decimal { return store.Decimal(s) }

func balanceOf(t *testing.T, st *store.Store, customerID string) store.AccountBalance {
	t.Helper()
	b, err := st.GetAccountBalance(context.Background(), store.OperatorScope, customerID)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func issued(t *testing.T, st *store.Store, customerID, period, total string) store.Statement {
	t.Helper()
	d := draft(t, st, customerID, period, total)
	s, _, err := st.IssueStatementOnce(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("issue %s: %v", period, err)
	}
	return s
}

// The ledger balance equals invoices − payments − credit notes for a
// scripted history, with a refund putting one payment back.
func TestIntegrationLedgerBalanceIsInvoicesMinusPaymentsMinusCredits(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	c := invoiceCustomer(t, st, store.CustomerInput{Slug: "ledger", Name: "Ledger", AdminEmail: "ap@ledger.example", Commercial: postpaidTransfer()})

	// Nothing yet: zero everywhere.
	if b := balanceOf(t, st, c.ID); string(b.Balance) != "0.000000" || string(b.AvailableCredit) != "0.000000" || string(b.Outstanding) != "0.000000" {
		t.Fatalf("empty account = %+v", b)
	}
	// Two invoices: 1000 and 250 → balance 1250 owed.
	a := issued(t, st, c.ID, "2026-01-01", "1000.000000")
	b2 := issued(t, st, c.ID, "2026-02-01", "250.000000")
	if b := balanceOf(t, st, c.ID); string(b.Balance) != "1250.000000" || string(b.Outstanding) != "1250.000000" {
		t.Fatalf("after two invoices = %+v", b)
	}
	// A payment of 400 against the first: balance 850, invoice a carries 600.
	if _, _, err := st.RecordStatementPayment(ctx, a.ID, store.PaymentInput{Amount: "400.000000", Reference: "TRF-1"}); err != nil {
		t.Fatal(err)
	}
	if b := balanceOf(t, st, c.ID); string(b.Balance) != "850.000000" || string(b.AvailableCredit) != "0.000000" {
		t.Fatalf("after 400 paid = %+v", b)
	}
	// A credit note of 100.5 on the second: balance 749.5, invoice b carries 149.5.
	note, err := st.CreateCreditNote(ctx, b2.ID, store.CreditNoteInput{Reason: "service credit", Amount: "100.500000", Actor: "ops"})
	if err != nil {
		t.Fatal(err)
	}
	if string(note.Total) != "100.500000" || string(note.Applied) != "100.500000" || string(note.Unapplied) != "0.000000" {
		t.Fatalf("credit note = %+v", note)
	}
	if b := balanceOf(t, st, c.ID); string(b.Balance) != "749.500000" || string(b.Outstanding) != "749.500000" {
		t.Fatalf("after credit note = %+v", b)
	}
	got, err := st.GetStatement(ctx, store.OperatorScope, b2.ID)
	if err != nil || string(got.Balance) != "149.500000" || string(got.Credited) != "100.500000" || got.Status != store.StatusIssued {
		t.Fatalf("invoice b after credit note: balance=%s credited=%s status=%s err=%v", got.Balance, got.Credited, got.Status, err)
	}
	// Refunding the 400 puts it back: balance 1149.5 and the invoice is open again.
	pays, err := st.ListCustomerPayments(ctx, store.OperatorScope, c.ID)
	if err != nil || len(pays) != 1 {
		t.Fatalf("payments = %+v (err %v)", pays, err)
	}
	refunded, err := st.RefundPayment(ctx, pays[0].ID, "bounced", "ops")
	if err != nil || refunded.Status != store.PaymentRefunded || refunded.RefundedAt == nil {
		t.Fatalf("refund = %+v (err %v)", refunded, err)
	}
	if b := balanceOf(t, st, c.ID); string(b.Balance) != "1149.500000" || string(b.Outstanding) != "1149.500000" {
		t.Fatalf("after refund = %+v", b)
	}
	// The ledger is append-only: six rows, and the running balance ends where the view does.
	entries, err := st.ListAccountEntries(ctx, store.OperatorScope, c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{}
	for _, e := range entries {
		kinds = append(kinds, e.Kind)
	}
	want := []string{store.EntryInvoice, store.EntryInvoice, store.EntryPayment, store.EntryCreditNote, store.EntryRefund}
	if len(kinds) != len(want) {
		t.Fatalf("ledger kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("ledger kinds = %v, want %v", kinds, want)
		}
	}
	if string(entries[len(entries)-1].Balance) != "1149.500000" {
		t.Fatalf("running balance = %s, want 1149.500000", entries[len(entries)-1].Balance)
	}
}

// A payment split across two invoices, with the remainder as credit on the
// account; the credit is then applied explicitly to a third.
func TestIntegrationPaymentSplitAcrossInvoicesWithRemainderAsCredit(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	c := invoiceCustomer(t, st, store.CustomerInput{Slug: "split", Name: "Split", AdminEmail: "ap@split.example", Commercial: postpaidTransfer()})
	a := issued(t, st, c.ID, "2026-01-01", "300.000000")
	b := issued(t, st, c.ID, "2026-02-01", "200.000000")

	p, err := st.RecordCustomerPayment(ctx, store.CustomerPaymentInput{CustomerID: c.ID, Payment: store.PaymentInput{Amount: "600.000000", Reference: "TRF-SPLIT", Actor: "ops"},
		Allocations: []store.AllocationInput{{StatementID: a.ID, Amount: "300.000000"}, {StatementID: b.ID, Amount: "150.000000"}}})
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Allocated) != "450.000000" || string(p.Unallocated) != "150.000000" || len(p.Allocations) != 2 || p.Purpose != store.PurposeCollection {
		t.Fatalf("split payment = %+v", p)
	}
	ga, _ := st.GetStatement(ctx, store.OperatorScope, a.ID)
	gb, _ := st.GetStatement(ctx, store.OperatorScope, b.ID)
	if ga.Status != store.StatusPaid || string(ga.Balance) != "0.000000" || gb.Status != store.StatusIssued || string(gb.Balance) != "50.000000" {
		t.Fatalf("a=%s/%s b=%s/%s", ga.Status, ga.Balance, gb.Status, gb.Balance)
	}
	bal := balanceOf(t, st, c.ID)
	if string(bal.AvailableCredit) != "150.000000" || string(bal.Outstanding) != "50.000000" || string(bal.Balance) != "-100.000000" {
		t.Fatalf("after split = %+v", bal)
	}
	// Allocating more than the remainder, or more than an invoice carries, is refused.
	if _, err := st.AllocatePayment(ctx, p.ID, []store.AllocationInput{{StatementID: b.ID, Amount: "160.000000"}}, false, "ops"); !store.IsConflict(err) {
		t.Fatalf("over-allocating the payment = %v, want a conflict", err)
	}
	if _, err := st.AllocatePayment(ctx, p.ID, []store.AllocationInput{{StatementID: b.ID, Amount: "60.000000"}}, false, "ops"); !store.IsConflict(err) {
		t.Fatalf("over-allocating the invoice = %v, want a conflict", err)
	}
	// The remainder settles b and leaves 100 of credit.
	p, err = st.AllocatePayment(ctx, p.ID, []store.AllocationInput{{StatementID: b.ID, Amount: "50.000000"}}, false, "ops")
	if err != nil || string(p.Unallocated) != "100.000000" {
		t.Fatalf("allocate the rest: %+v (err %v)", p, err)
	}
	gb, _ = st.GetStatement(ctx, store.OperatorScope, b.ID)
	if gb.Status != store.StatusPaid {
		t.Fatalf("b = %s, want paid", gb.Status)
	}
	// A third invoice: nothing applied implicitly (postpaid, no auto-apply);
	// the operator applies the credit and the invoice is settled from it.
	cc := issued(t, st, c.ID, "2026-03-01", "80.000000")
	if gc, _ := st.GetStatement(ctx, store.OperatorScope, cc.ID); string(gc.Balance) != "80.000000" {
		t.Fatalf("credit must not be applied implicitly: balance = %s", gc.Balance)
	}
	applied, err := st.ApplyCredit(ctx, c.ID, []string{cc.ID}, "ops")
	if err != nil || string(applied[cc.ID]) != "80.000000" {
		t.Fatalf("apply credit = %+v (err %v)", applied, err)
	}
	gc, _ := st.GetStatement(ctx, store.OperatorScope, cc.ID)
	bal = balanceOf(t, st, c.ID)
	if gc.Status != store.StatusPaid || string(bal.AvailableCredit) != "20.000000" || string(bal.Balance) != "-20.000000" {
		t.Fatalf("after applying credit: %s %+v", gc.Status, bal)
	}
}

// A credit note reduces the invoice balance, refuses to exceed the invoice,
// lands as account credit on an invoice already paid, and a full one
// cancels — through the note, never a status flip.
func TestIntegrationCreditNoteReducesBalanceAndRefusesToExceedTheInvoice(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	c := invoiceCustomer(t, st, store.CustomerInput{Slug: "credit", Name: "Credit", AdminEmail: "ap@credit.example", Commercial: postpaidTransfer()})
	// Drafts cannot be credited.
	d := draft(t, st, c.ID, "2026-01-01", "500.000000")
	if _, err := st.CreateCreditNote(ctx, d.ID, store.CreditNoteInput{Reason: "x", Amount: "1"}); !store.IsConflict(err) {
		t.Fatalf("crediting a draft = %v, want a conflict", err)
	}
	a, _, err := st.IssueStatementOnce(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	// More than the invoice is refused, naming the room left.
	if _, err := st.CreateCreditNote(ctx, a.ID, store.CreditNoteInput{Reason: "too much", Amount: "500.000001"}); !store.IsConflict(err) {
		t.Fatalf("exceeding the invoice = %v, want a conflict", err)
	}
	// A lump amount is tax-inclusive: at the invoice's (zero) rate net = total.
	n1, err := st.CreateCreditNote(ctx, a.ID, store.CreditNoteInput{Reason: "outage", Amount: "120.000000", Actor: "ops"})
	if err != nil || string(n1.Total) != "120.000000" || string(n1.Subtotal) != "120.000000" || n1.Number == "" {
		t.Fatalf("n1 = %+v (err %v)", n1, err)
	}
	if got, _ := st.GetStatement(ctx, store.OperatorScope, a.ID); string(got.Balance) != "380.000000" || string(got.Credited) != "120.000000" {
		t.Fatalf("after n1: balance=%s credited=%s", got.Balance, got.Credited)
	}
	// Two notes together may not exceed the invoice either.
	if _, err := st.CreateCreditNote(ctx, a.ID, store.CreditNoteInput{Reason: "again", Amount: "380.000001"}); !store.IsConflict(err) {
		t.Fatalf("cumulative excess = %v, want a conflict", err)
	}
	// Numbering is gapless with its own prefix.
	n2, err := st.CreateCreditNote(ctx, a.ID, store.CreditNoteInput{Reason: "goodwill", Lines: []store.CreditNoteLine{{Description: "1 day free", Amount: "30.000000"}}, Actor: "ops"})
	if err != nil {
		t.Fatal(err)
	}
	year := time.Now().UTC().Year()
	if n1.Number != store.CreditNoteNumberFor("CN", year, 1) || n2.Number != store.CreditNoteNumberFor("CN", year, 2) {
		t.Fatalf("numbers = %s, %s", n1.Number, n2.Number)
	}
	// Pay the remaining 350; the invoice is paid; a further credit note
	// lands entirely as account credit.
	if _, _, err := st.RecordStatementPayment(ctx, a.ID, store.PaymentInput{Amount: "350.000000", Reference: "TRF-A"}); err != nil {
		t.Fatal(err)
	}
	n3, err := st.CreateCreditNote(ctx, a.ID, store.CreditNoteInput{Reason: "post-payment correction", Amount: "50.000000", Actor: "ops"})
	if err != nil || string(n3.Applied) != "0.000000" || string(n3.Unapplied) != "50.000000" {
		t.Fatalf("n3 = %+v (err %v)", n3, err)
	}
	bal := balanceOf(t, st, c.ID)
	if string(bal.AvailableCredit) != "50.000000" || string(bal.Balance) != "-50.000000" {
		t.Fatalf("after crediting a paid invoice = %+v", bal)
	}
	got, _ := st.GetStatement(ctx, store.OperatorScope, a.ID)
	if got.Status != store.StatusPaid || len(got.CreditNotes) != 3 {
		t.Fatalf("paid invoice with three notes: %s %d", got.Status, len(got.CreditNotes))
	}
	// Cancelling an ISSUED invoice is a full credit note (DESIGN.md §9.3).
	b := issued(t, st, c.ID, "2026-02-01", "100.000000")
	if _, _, err := st.RecordStatementPayment(ctx, b.ID, store.PaymentInput{Amount: "40.000000", Reference: "TRF-B"}); err != nil {
		t.Fatal(err)
	}
	cancelled, transitioned, err := st.CancelStatement(ctx, b.ID, "raised in error")
	if err != nil || !transitioned || cancelled.Status != store.StatusCancelled || len(cancelled.CreditNotes) != 1 || cancelled.CreditNotes[0].Kind != store.CreditNoteFull {
		t.Fatalf("cancel issued: %s transitioned=%v notes=%+v err=%v", cancelled.Status, transitioned, cancelled.CreditNotes, err)
	}
	// The 40 already paid is now credit on the account: 50 + 40.
	if bal := balanceOf(t, st, c.ID); string(bal.AvailableCredit) != "90.000000" || string(bal.Balance) != "-90.000000" || string(bal.Outstanding) != "0.000000" {
		t.Fatalf("after cancelling a part-paid invoice = %+v", bal)
	}
}

// The tax snapshot is frozen at issue: changing the customer's rate
// afterwards changes the next invoice, never the issued one.
func TestIntegrationTaxSnapshotIsFrozenOnIssue(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	if _, err := st.UpdateBillingSettings(ctx, store.BillingSettings{DiscountRule: store.DefaultDiscountRule, InvoicePrefix: "INV", CommercialProvider: store.ProviderInternal,
		TaxRate: "0.0500", TaxRegistrationNumber: "OM1100000001", LegalName: "Omantel Cloud LLC", Address: "PO Box 789, Muscat"}); err != nil {
		t.Fatal(err)
	}
	rate := dec("0.1000")
	c := invoiceCustomer(t, st, store.CustomerInput{Slug: "taxed", Name: "Taxed LLC", AdminEmail: "ap@taxed.example", Commercial: postpaidTransfer(),
		Tax: store.TaxProfile{TaxRegistrationNumber: "OM2200000002", TaxRate: &rate}})
	if got := store.EffectiveTaxRate(c, store.DefaultBillingSettings()); string(got) != "0.1000" {
		t.Fatalf("effective rate = %s, want the customer's 0.1000", got)
	}
	// The rated draft carries the customer's rate (rating.Run does this; the
	// draft here states it directly, as the run would).
	start, _ := time.Parse("2006-01-02", "2026-01-01")
	d, err := st.WriteDraftStatement(ctx, store.StatementDraft{CustomerID: c.ID, PeriodStart: start, PeriodEnd: start.AddDate(0, 1, -1), Currency: "OMR",
		Subtotal: "1000.000000", TaxRate: "0.1000", Tax: "100.000000", Total: "1100.000000"})
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := st.IssueStatementOnce(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.TaxSnapshot == nil {
		t.Fatal("an issued invoice carries a tax snapshot")
	}
	snap := *a.TaxSnapshot
	if string(snap.Rate) != "0.1000" || snap.CustomerTaxNumber != "OM2200000002" || snap.SellerTaxNumber != "OM1100000001" || snap.SellerLegalName != "Omantel Cloud LLC" || snap.SellerAddress != "PO Box 789, Muscat" || snap.Exempt {
		t.Fatalf("snapshot = %+v", snap)
	}
	// The customer becomes exempt and the Sovereign changes its number.
	exempt := true
	if _, err := st.UpdateCustomer(ctx, c.ID, store.CustomerPatch{TaxExempt: &exempt, TaxExemptReason: strp("free zone")}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateBillingSettings(ctx, store.BillingSettings{DiscountRule: store.DefaultDiscountRule, InvoicePrefix: "INV", CommercialProvider: store.ProviderInternal,
		TaxRate: "0.0500", TaxRegistrationNumber: "OM9999999999", LegalName: "Omantel Cloud LLC", Address: "PO Box 789, Muscat"}); err != nil {
		t.Fatal(err)
	}
	// The issued invoice is untouched: rate, tax, total and snapshot.
	again, _ := st.GetStatement(ctx, store.OperatorScope, a.ID)
	if string(again.TaxRate) != "0.1000" || string(again.Tax) != "100.000000" || string(again.Total) != "1100.000000" || again.TaxSnapshot.SellerTaxNumber != "OM1100000001" || again.TaxSnapshot.Exempt {
		t.Fatalf("issued invoice changed: rate=%s tax=%s total=%s snapshot=%+v", again.TaxRate, again.Tax, again.Total, again.TaxSnapshot)
	}
	// And re-rating the period is refused (lane 1) — nothing can recompute it.
	if _, err := st.WriteDraftStatement(ctx, store.StatementDraft{CustomerID: c.ID, PeriodStart: start, PeriodEnd: start.AddDate(0, 1, -1), Currency: "OMR", Subtotal: "1", TaxRate: "0", Tax: "0", Total: "1"}); !store.IsConflict(err) {
		t.Fatalf("re-rating an issued period = %v, want a conflict", err)
	}
	// The NEXT invoice sees the exemption: zero rate, and the new seller number.
	updated, _ := st.GetCustomer(ctx, store.OperatorScope, c.ID)
	settings, _ := st.GetBillingSettings(ctx)
	if got := store.EffectiveTaxRate(updated, settings); string(got) != "0" {
		t.Fatalf("an exempt customer rates at 0, got %s", got)
	}
	b := issued(t, st, c.ID, "2026-02-01", "500.000000")
	if b.TaxSnapshot == nil || !b.TaxSnapshot.Exempt || b.TaxSnapshot.ExemptReason != "free zone" || b.TaxSnapshot.SellerTaxNumber != "OM9999999999" {
		t.Fatalf("next snapshot = %+v", b.TaxSnapshot)
	}
}

// A prepaid customer: the top-up is a credit on the account, the issue
// debits the wallet at once, and an issue larger than the balance leaves
// the remainder outstanding.
func TestIntegrationPrepaidWalletDebitOnIssueAndTopUpCredit(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	c := invoiceCustomer(t, st, store.CustomerInput{Slug: "wallet", Name: "Wallet", AdminEmail: "ap@wallet.example",
		Commercial: store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPrepaid, PaymentMethod: store.PaymentMethodTransfer}})
	// A top-up: a payment with no allocation.
	top, err := st.RecordCustomerPayment(ctx, store.CustomerPaymentInput{CustomerID: c.ID, Payment: store.PaymentInput{Amount: "500.000000", Reference: "TOPUP-1", Actor: "ops"}})
	if err != nil || top.Purpose != store.PurposeCheckout || string(top.Unallocated) != "500.000000" {
		t.Fatalf("top-up = %+v (err %v)", top, err)
	}
	if bal := balanceOf(t, st, c.ID); string(bal.AvailableCredit) != "500.000000" || string(bal.Balance) != "-500.000000" {
		t.Fatalf("after top-up = %+v", bal)
	}
	entries, _ := st.ListAccountEntries(ctx, store.OperatorScope, c.ID, 0)
	if len(entries) != 1 || entries[0].Kind != store.EntryTopUp {
		t.Fatalf("a top-up posts a top_up credit: %+v", entries)
	}
	// Issue 300: settled from the wallet at issue, 200 left.
	a := issued(t, st, c.ID, "2026-01-01", "300.000000")
	if a.Status != store.StatusPaid || string(a.Balance) != "0.000000" || string(a.Paid) != "300.000000" {
		t.Fatalf("prepaid issue must settle from the wallet: %s balance=%s paid=%s", a.Status, a.Balance, a.Paid)
	}
	if bal := balanceOf(t, st, c.ID); string(bal.AvailableCredit) != "200.000000" || string(bal.Outstanding) != "0.000000" {
		t.Fatalf("after the debit = %+v", bal)
	}
	// Issue 350: 200 drawn, 150 outstanding, the wallet at zero.
	b := issued(t, st, c.ID, "2026-02-01", "350.000000")
	if b.Status != store.StatusIssued || string(b.Balance) != "150.000000" || string(b.Paid) != "200.000000" {
		t.Fatalf("partial wallet: %s balance=%s paid=%s", b.Status, b.Balance, b.Paid)
	}
	if bal := balanceOf(t, st, c.ID); string(bal.AvailableCredit) != "0.000000" || string(bal.Outstanding) != "150.000000" || string(bal.Balance) != "150.000000" {
		t.Fatalf("wallet exhausted = %+v", bal)
	}
	// A second top-up is credit; applied explicitly it settles the rest.
	if _, err := st.RecordCustomerPayment(ctx, store.CustomerPaymentInput{CustomerID: c.ID, Payment: store.PaymentInput{Amount: "1000.000000", Reference: "TOPUP-2", Actor: "ops"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApplyCredit(ctx, c.ID, nil, "ops"); err != nil {
		t.Fatal(err)
	}
	gb, _ := st.GetStatement(ctx, store.OperatorScope, b.ID)
	bal := balanceOf(t, st, c.ID)
	if gb.Status != store.StatusPaid || string(bal.AvailableCredit) != "850.000000" || string(bal.Outstanding) != "0.000000" {
		t.Fatalf("after the second top-up: %s %+v", gb.Status, bal)
	}
}

// The founder's case (DESIGN.md §9.5): a POSTPAID customer tops up, chooses
// two of three open invoices to pay from credit, and the third stays due
// with the correct balance; the same top-up with auto-apply on settles
// oldest-first at issue.
func TestIntegrationPostpaidTopUpAppliedToChosenInvoicesAndAutoApply(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	c := invoiceCustomer(t, st, store.CustomerInput{Slug: "choose", Name: "Chooser", AdminEmail: "ap@choose.example", Commercial: postpaidTransfer()})
	a := issued(t, st, c.ID, "2026-01-01", "100.000000")
	b := issued(t, st, c.ID, "2026-02-01", "200.000000")
	cc := issued(t, st, c.ID, "2026-03-01", "300.000000")
	if _, err := st.RecordCustomerPayment(ctx, store.CustomerPaymentInput{CustomerID: c.ID, Payment: store.PaymentInput{Amount: "500.000000", Reference: "TOPUP", Actor: "ops"}}); err != nil {
		t.Fatal(err)
	}
	// Postpaid: nothing moved on its own.
	for _, id := range []string{a.ID, b.ID, cc.ID} {
		if got, _ := st.GetStatement(ctx, store.OperatorScope, id); got.Status != store.StatusIssued || string(got.Paid) != "0.000000" {
			t.Fatalf("a top-up must not settle a postpaid invoice implicitly: %s paid=%s", got.Status, got.Paid)
		}
	}
	// The customer chooses the third and the first — not the second.
	applied, err := st.ApplyCredit(ctx, c.ID, []string{cc.ID, a.ID}, "ops")
	if err != nil || string(applied[cc.ID]) != "300.000000" || string(applied[a.ID]) != "100.000000" {
		t.Fatalf("apply to chosen = %+v (err %v)", applied, err)
	}
	ga, _ := st.GetStatement(ctx, store.OperatorScope, a.ID)
	gb, _ := st.GetStatement(ctx, store.OperatorScope, b.ID)
	gc, _ := st.GetStatement(ctx, store.OperatorScope, cc.ID)
	if ga.Status != store.StatusPaid || gc.Status != store.StatusPaid || gb.Status != store.StatusIssued || string(gb.Balance) != "200.000000" {
		t.Fatalf("a=%s b=%s/%s c=%s", ga.Status, gb.Status, gb.Balance, gc.Status)
	}
	bal := balanceOf(t, st, c.ID)
	if string(bal.AvailableCredit) != "100.000000" || string(bal.Outstanding) != "200.000000" || string(bal.Balance) != "100.000000" {
		t.Fatalf("balance after choosing = %+v", bal)
	}

	// The same top-up with auto-apply on: each invoice is settled at issue,
	// oldest credit first, until the credit runs out.
	on := true
	auto := invoiceCustomer(t, st, store.CustomerInput{Slug: "auto", Name: "Auto", AdminEmail: "ap@auto.example", Commercial: postpaidTransfer(), AutoApplyCredit: on})
	if _, err := st.RecordCustomerPayment(ctx, store.CustomerPaymentInput{CustomerID: auto.ID, Payment: store.PaymentInput{Amount: "500.000000", Reference: "TOPUP", Actor: "ops"}}); err != nil {
		t.Fatal(err)
	}
	x := issued(t, st, auto.ID, "2026-01-01", "100.000000")
	y := issued(t, st, auto.ID, "2026-02-01", "200.000000")
	z := issued(t, st, auto.ID, "2026-03-01", "300.000000")
	if x.Status != store.StatusPaid || y.Status != store.StatusPaid {
		t.Fatalf("auto-apply settles at issue: x=%s y=%s", x.Status, y.Status)
	}
	// 500 − 100 − 200 = 200 drawn on the third; 100 outstanding.
	if z.Status != store.StatusIssued || string(z.Paid) != "200.000000" || string(z.Balance) != "100.000000" {
		t.Fatalf("third under auto-apply: %s paid=%s balance=%s", z.Status, z.Paid, z.Balance)
	}
	if bal := balanceOf(t, st, auto.ID); string(bal.AvailableCredit) != "0.000000" || string(bal.Outstanding) != "100.000000" {
		t.Fatalf("auto-apply balance = %+v", bal)
	}
}

// The wire shape lane 1 defined does not change: a payment recorded against
// an invoice is one payment plus one allocation, paid_total and balance read
// as they did, and the payment history on the document is intact.
func TestIntegrationLaneOnePaymentShapeIsUnchanged(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	c := invoiceCustomer(t, st, store.CustomerInput{Slug: "shape", Name: "Shape", AdminEmail: "ap@shape.example", Commercial: postpaidTransfer()})
	a := issued(t, st, c.ID, "2026-01-01", "1000.100000")
	got, p, err := st.RecordStatementPayment(ctx, a.ID, store.PaymentInput{Amount: "400.000000", Reference: "TRF-1"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Paid) != "400.000000" || string(got.Balance) != "600.100000" || got.Status != store.StatusIssued {
		t.Fatalf("after part payment: paid=%s balance=%s status=%s", got.Paid, got.Balance, got.Status)
	}
	if p.StatementID != a.ID || string(p.Allocated) != "400.000000" || string(p.Unallocated) != "0.000000" || len(p.Allocations) != 1 {
		t.Fatalf("the payment is one allocation of its whole amount: %+v", p)
	}
	if len(got.Payments) != 1 || got.Payments[0].ID != p.ID || string(got.Payments[0].Allocated) != "400.000000" {
		t.Fatalf("payment history = %+v", got.Payments)
	}
	// Overpayment is still refused.
	if _, _, err := st.RecordStatementPayment(ctx, a.ID, store.PaymentInput{Amount: "600.100001", Reference: "TRF-OVER"}); !store.IsConflict(err) {
		t.Fatalf("overpayment = %v, want a conflict", err)
	}
	// A pending payment settles nothing and allocates nothing.
	if _, pp, err := st.RecordStatementPayment(ctx, a.ID, store.PaymentInput{Amount: "600.100000", Reference: "PENDING", Status: store.PaymentPending}); err != nil || string(pp.Allocated) != "0.000000" {
		t.Fatalf("pending = %+v (err %v)", pp, err)
	}
	if bal := balanceOf(t, st, c.ID); string(bal.Balance) != "600.100000" || string(bal.AvailableCredit) != "0.000000" {
		t.Fatalf("a pending payment moves nothing: %+v", bal)
	}
}
