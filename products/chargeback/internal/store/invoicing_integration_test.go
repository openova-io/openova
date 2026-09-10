package store_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

func intp(v int) *int { return &v }

func strp(v string) *string { return &v }

// postpaidTransfer is the Omantel corporate position: billed, invoiced after
// the period, paid by bank transfer against a purchase order.
func postpaidTransfer() store.Commercial {
	return store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPostpaid, PaymentMethod: store.PaymentMethodTransfer}
}

// invoiceCustomer creates a customer with the settlement shape the case needs.
func invoiceCustomer(t *testing.T, st *store.Store, in store.CustomerInput) store.Customer {
	t.Helper()
	c, err := st.CreateCustomer(context.Background(), in)
	if err != nil {
		t.Fatalf("create customer %s: %v", in.Slug, err)
	}
	return c
}

// draft writes one draft statement for a period, with a known total.
func draft(t *testing.T, st *store.Store, customerID, periodStart, total string) store.Statement {
	t.Helper()
	start, err := time.Parse("2006-01-02", periodStart)
	if err != nil {
		t.Fatal(err)
	}
	d, err := st.WriteDraftStatement(context.Background(), store.StatementDraft{
		CustomerID: customerID, PeriodStart: start, PeriodEnd: start.AddDate(0, 1, -1),
		Currency: "OMR", Subtotal: store.Decimal(total), TaxRate: "0", Tax: "0", Total: store.Decimal(total),
	})
	if err != nil {
		t.Fatalf("write draft: %v", err)
	}
	return d
}

// Every issue takes the next number of the calendar year, and the number is
// taken inside the transaction that flips the status — so N concurrent
// issues produce N numbers with no duplicate and, crucially, no GAP.
func TestIntegrationInvoiceNumbersAreGaplessUnderConcurrentIssue(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	c := invoiceCustomer(t, st, store.CustomerInput{Slug: "gapless", Name: "Gapless", AdminEmail: "ap@gapless.example", Commercial: postpaidTransfer()})

	const n = 12
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		// One statement per month; (customer, period_start) is unique.
		ids[i] = draft(t, st, c.ID, fmt.Sprintf("2020-%02d-01", i+1), "10.000000").ID
	}
	var wg sync.WaitGroup
	errs := make([]error, n)
	numbers := make([]string, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			s, transitioned, err := st.IssueStatementOnce(ctx, ids[i])
			if err != nil {
				errs[i] = err
				return
			}
			if !transitioned {
				errs[i] = fmt.Errorf("statement %d did not transition", i)
				return
			}
			numbers[i] = s.InvoiceNumber
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent issue %d: %v", i, err)
		}
	}
	year := time.Now().UTC().Year()
	want := make([]string, n)
	for i := range want {
		want[i] = store.InvoiceNumberFor(store.DefaultInvoicePrefix, year, int64(i+1))
	}
	got := append([]string{}, numbers...)
	sort.Strings(got)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("invoice numbers = %v\nwant %v (gapless 1..%d)", got, want, n)
		}
	}
	// And the sequence continues where it left off rather than restarting.
	next := draft(t, st, c.ID, "2021-01-01", "10.000000")
	s, _, err := st.IssueStatementOnce(ctx, next.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.InvoiceNumber != store.InvoiceNumberFor(store.DefaultInvoicePrefix, year, n+1) {
		t.Fatalf("next number = %s, want %s", s.InvoiceNumber, store.InvoiceNumberFor(store.DefaultInvoicePrefix, year, n+1))
	}
	// Re-issuing is idempotent and never mints a second number.
	again, transitioned, err := st.IssueStatementOnce(ctx, next.ID)
	if err != nil || transitioned || again.InvoiceNumber != s.InvoiceNumber {
		t.Fatalf("re-issue: number=%s transitioned=%v err=%v", again.InvoiceNumber, transitioned, err)
	}
}

// The prefix is configurable and applies to the NEXT number only: numbers
// already on a customer's invoice never change under them.
func TestIntegrationInvoicePrefixIsConfigurable(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	c := invoiceCustomer(t, st, store.CustomerInput{Slug: "prefix", Name: "Prefix", AdminEmail: "ap@prefix.example"})
	first, _, err := st.IssueStatementOnce(ctx, draft(t, st, c.ID, "2026-01-01", "1.000000").ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.InvoiceNumber, store.DefaultInvoicePrefix+"-") {
		t.Fatalf("default prefix: %s", first.InvoiceNumber)
	}
	if _, err := st.UpdateBillingSettings(ctx, store.BillingSettings{DiscountRule: store.DefaultDiscountRule, InvoicePrefix: "omt-cb"}); err != nil {
		t.Fatal(err)
	}
	second, _, err := st.IssueStatementOnce(ctx, draft(t, st, c.ID, "2026-02-01", "1.000000").ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(second.InvoiceNumber, "OMT-CB-") {
		t.Fatalf("configured prefix: %s", second.InvoiceNumber)
	}
	// The earlier invoice keeps the number the customer received.
	reread, err := st.GetStatement(ctx, store.OperatorScope, first.ID)
	if err != nil || reread.InvoiceNumber != first.InvoiceNumber {
		t.Fatalf("issued number changed: %s → %s (err %v)", first.InvoiceNumber, reread.InvoiceNumber, err)
	}
	if _, err := st.UpdateBillingSettings(ctx, store.BillingSettings{DiscountRule: store.DefaultDiscountRule, InvoicePrefix: "no lower"}); err == nil {
		t.Fatal("an invalid prefix must be refused")
	}
}

// Issuing copies the customer's standing purchase order and terms onto the
// invoice and computes the due date from them; a per-statement override
// wins, and both are frozen afterwards.
func TestIntegrationIssueCopiesPurchaseOrderAndComputesDueDate(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	c := invoiceCustomer(t, st, store.CustomerInput{Slug: "omantel-corp", Name: "Corporate", AdminEmail: "ap@corp.example",
		Commercial: postpaidTransfer(), PORef: "PO-4471", PaymentTermsDays: intp(45)})

	d := draft(t, st, c.ID, "2026-03-01", "500.000000")
	if d.PORef != "" || d.PaymentTermsDays != nil || d.DueAt != nil || d.InvoiceNumber != "" {
		t.Fatalf("a draft carries no invoice fields yet: %+v", d)
	}
	issued, _, err := st.IssueStatementOnce(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if issued.PORef != "PO-4471" {
		t.Errorf("po_reference = %q, want the customer's standing PO", issued.PORef)
	}
	if issued.PaymentTermsDays == nil || *issued.PaymentTermsDays != 45 {
		t.Errorf("payment_terms_days = %v, want 45", issued.PaymentTermsDays)
	}
	if issued.IssuedAt == nil || issued.DueAt == nil {
		t.Fatalf("issued_at=%v due_at=%v", issued.IssuedAt, issued.DueAt)
	}
	if want := issued.IssuedAt.AddDate(0, 0, 45); !issued.DueAt.Equal(want) {
		t.Errorf("due_at = %s, want issued + 45 days = %s", issued.DueAt, want)
	}
	// A statement the operator edited before issue keeps its own values.
	d2 := draft(t, st, c.ID, "2026-04-01", "500.000000")
	terms := 14
	po := "PO-9002"
	edited, err := st.UpdateStatementInvoice(ctx, d2.ID, store.StatementInvoicePatch{PORef: &po, PaymentTermsDays: &terms})
	if err != nil {
		t.Fatal(err)
	}
	if edited.PORef != "PO-9002" {
		t.Fatalf("edited draft po = %q", edited.PORef)
	}
	issued2, _, err := st.IssueStatementOnce(ctx, d2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if issued2.PORef != "PO-9002" || issued2.PaymentTermsDays == nil || *issued2.PaymentTermsDays != 14 {
		t.Fatalf("override lost: po=%q terms=%v", issued2.PORef, issued2.PaymentTermsDays)
	}
	if want := issued2.IssuedAt.AddDate(0, 0, 14); !issued2.DueAt.Equal(want) {
		t.Errorf("due_at = %s, want %s", issued2.DueAt, want)
	}
	// Frozen once issued.
	if _, err := st.UpdateStatementInvoice(ctx, issued2.ID, store.StatementInvoicePatch{PORef: &po}); !store.IsConflict(err) {
		t.Fatalf("editing an issued invoice = %v, want a conflict", err)
	}
	// A customer that never set terms gets the net-30 default.
	plain := invoiceCustomer(t, st, store.CustomerInput{Slug: "plain", Name: "Plain", AdminEmail: "p@plain.example"})
	def, _, err := st.IssueStatementOnce(ctx, draft(t, st, plain.ID, "2026-03-01", "1.000000").ID)
	if err != nil {
		t.Fatal(err)
	}
	if def.PaymentTermsDays == nil || *def.PaymentTermsDays != store.DefaultPaymentTermsDays {
		t.Fatalf("default terms = %v, want %d", def.PaymentTermsDays, store.DefaultPaymentTermsDays)
	}
}

// Every legal transition is accepted and every illegal one refused, in the
// store rather than only in the UI.
func TestIntegrationStatementLifecycleRefusesIllegalTransitions(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	c := invoiceCustomer(t, st, store.CustomerInput{Slug: "life", Name: "Lifecycle", AdminEmail: "ap@life.example", Commercial: postpaidTransfer()})
	month := 0
	newDraft := func() store.Statement {
		month++
		return draft(t, st, c.ID, fmt.Sprintf("2026-%02d-01", month), "100.000000")
	}

	// draft → sent is refused: an invoice must be issued before it is sent.
	d := newDraft()
	if _, _, err := st.SendStatement(ctx, d.ID); !store.IsConflict(err) {
		t.Fatalf("draft → sent = %v, want a conflict", err)
	}
	// draft → paid is refused: there is no invoice to pay yet.
	if _, _, err := st.RecordStatementPayment(ctx, d.ID, store.PaymentInput{Amount: "100.000000", Reference: "X"}); !store.IsConflict(err) {
		t.Fatalf("paying a draft = %v, want a conflict", err)
	}
	// draft → cancelled is legal.
	cancelled, transitioned, err := st.CancelStatement(ctx, d.ID, "raised in error")
	if err != nil || !transitioned || cancelled.Status != store.StatusCancelled || cancelled.CancelReason != "raised in error" {
		t.Fatalf("cancel a draft: %+v transitioned=%v err=%v", cancelled, transitioned, err)
	}
	// cancelled is terminal.
	if _, _, err := st.IssueStatementOnce(ctx, d.ID); !store.IsConflict(err) {
		t.Fatalf("issuing a cancelled statement = %v, want a conflict", err)
	}
	if _, _, err := st.SendStatement(ctx, d.ID); !store.IsConflict(err) {
		t.Fatalf("sending a cancelled statement = %v, want a conflict", err)
	}
	// Cancelling again is idempotent, not an error.
	if _, transitioned, err := st.CancelStatement(ctx, d.ID, ""); err != nil || transitioned {
		t.Fatalf("re-cancel: transitioned=%v err=%v", transitioned, err)
	}

	// issued → sent → paid, the ordinary post-paid path.
	b := newDraft()
	issued, _, err := st.IssueStatementOnce(ctx, b.ID)
	if err != nil || issued.Status != store.StatusIssued {
		t.Fatalf("issue: %+v %v", issued, err)
	}
	sent, transitioned, err := st.SendStatement(ctx, b.ID)
	if err != nil || !transitioned || sent.Status != store.StatusSent || sent.SentAt == nil {
		t.Fatalf("send: %+v transitioned=%v err=%v", sent, transitioned, err)
	}
	if _, transitioned, err := st.SendStatement(ctx, b.ID); err != nil || transitioned {
		t.Fatalf("re-send: transitioned=%v err=%v", transitioned, err)
	}
	// sent → cancelled is a FULL CREDIT NOTE, never a status flip (DESIGN.md
	// §9.3): the customer holds that invoice, and the note is the document
	// that takes it back. Proven on a separate sent invoice so this one can
	// go on to be paid.
	cn := newDraft()
	if _, _, err := st.IssueStatementOnce(ctx, cn.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SendStatement(ctx, cn.ID); err != nil {
		t.Fatal(err)
	}
	voided, transitioned, err := st.CancelStatement(ctx, cn.ID, "changed our mind")
	if err != nil || !transitioned || voided.Status != store.StatusCancelled {
		t.Fatalf("cancelling a sent invoice: %+v transitioned=%v err=%v", voided.Status, transitioned, err)
	}
	if len(voided.CreditNotes) != 1 || voided.CreditNotes[0].Kind != store.CreditNoteFull || string(voided.CreditNotes[0].Total) != "100.000000" || voided.CreditNotes[0].Reason != "changed our mind" {
		t.Fatalf("a cancelled sent invoice must carry one full credit note for its total: %+v", voided.CreditNotes)
	}
	if string(voided.Balance) != "0.000000" {
		t.Fatalf("a cancelled invoice carries nothing: balance = %s", voided.Balance)
	}
	paid, _, err := st.RecordStatementPayment(ctx, b.ID, store.PaymentInput{Amount: "100.000000", Reference: "TRF-1", PaidAt: time.Now().UTC()})
	if err != nil || paid.Status != store.StatusPaid || paid.PaidAt == nil {
		t.Fatalf("pay: %+v %v", paid, err)
	}
	// paid is terminal, in every direction.
	if _, _, err := st.RecordStatementPayment(ctx, b.ID, store.PaymentInput{Amount: "1.000000", Reference: "TRF-2"}); !store.IsConflict(err) {
		t.Fatalf("paying a paid invoice = %v, want a conflict", err)
	}
	if _, _, err := st.CancelStatement(ctx, b.ID, "oops"); !store.IsConflict(err) {
		t.Fatalf("cancelling a paid invoice = %v, want a conflict", err)
	}
	if _, _, err := st.SendStatement(ctx, b.ID); !store.IsConflict(err) {
		t.Fatalf("sending a paid invoice = %v, want a conflict", err)
	}

	// issued → paid directly (a prepaid customer who never needed the post),
	// and issued → cancelled.
	e := newDraft()
	if _, _, err := st.IssueStatementOnce(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	if s, _, err := st.RecordStatementPayment(ctx, e.ID, store.PaymentInput{Amount: "100.000000", Reference: "TRF-3"}); err != nil || s.Status != store.StatusPaid {
		t.Fatalf("issued → paid: %v %v", s.Status, err)
	}
	f := newDraft()
	if _, _, err := st.IssueStatementOnce(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	if s, _, err := st.CancelStatement(ctx, f.ID, "duplicate"); err != nil || s.Status != store.StatusCancelled {
		t.Fatalf("issued → cancelled: %v %v", s.Status, err)
	}

	// An issued statement can no longer be deleted or re-rated, and neither
	// can a sent, paid or cancelled one.
	if err := st.DeleteDraftStatement(ctx, f.ID); !store.IsConflict(err) {
		t.Fatalf("deleting a cancelled statement = %v, want a conflict", err)
	}
	start, _ := time.Parse("2006-01-02", "2026-01-01")
	if _, err := st.WriteDraftStatement(ctx, store.StatementDraft{CustomerID: c.ID, PeriodStart: start, PeriodEnd: start.AddDate(0, 1, -1),
		Currency: "OMR", Subtotal: "1", TaxRate: "0", Tax: "0", Total: "1"}); !store.IsConflict(err) {
		t.Fatalf("re-rating a period whose statement is past draft = %v, want a conflict", err)
	}
}

// Part payment carries a balance; a second payment settles it; overpayment
// is refused. All exact, never float.
func TestIntegrationPartPaymentCarriesTheBalance(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	c := invoiceCustomer(t, st, store.CustomerInput{Slug: "part", Name: "Part payer", AdminEmail: "ap@part.example",
		Commercial: postpaidTransfer(), PaymentTermsDays: intp(30)})
	d := draft(t, st, c.ID, "2026-05-01", "1000.100000")
	issued, _, err := st.IssueStatementOnce(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(issued.Balance) != "1000.100000" || string(issued.Paid) != "0.000000" {
		t.Fatalf("before any payment: paid=%s balance=%s", issued.Paid, issued.Balance)
	}
	day1 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	after, first, err := st.RecordStatementPayment(ctx, d.ID, store.PaymentInput{Amount: "400.000000", PaidAt: day1, Reference: "TRF-A", Actor: "ops@example"})
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != store.StatusIssued {
		t.Errorf("a part payment must not settle the invoice: status=%s", after.Status)
	}
	if string(after.Paid) != "400.000000" || string(after.Balance) != "600.100000" {
		t.Fatalf("after part payment: paid=%s balance=%s", after.Paid, after.Balance)
	}
	if first.Reference != "TRF-A" || first.RecordedBy != "ops@example" || first.Gateway != "manual" {
		t.Fatalf("payment row = %+v", first)
	}
	// A payment is a row of its OWN against the customer, linked to the
	// invoice it was recorded against — the shape a later lane needs to
	// allocate one payment across invoices and to hold unallocated credit.
	if first.CustomerID != c.ID || first.StatementID != d.ID {
		t.Fatalf("payment must belong to the customer and link to the invoice: %+v", first)
	}
	if first.Method != store.PaymentMethodTransfer || first.Status != store.PaymentReceived {
		t.Fatalf("payment method/status = %q/%q", first.Method, first.Status)
	}
	// A PENDING payment is recorded and settles nothing: it does not move
	// the balance and cannot flip the invoice to paid.
	pendingBefore := after.Balance
	withPending, pending, err := st.RecordStatementPayment(ctx, d.ID, store.PaymentInput{Amount: "600.100000", PaidAt: day1, Reference: "TRF-PENDING", Status: store.PaymentPending})
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != store.PaymentPending || withPending.Status != store.StatusIssued || withPending.Balance != pendingBefore {
		t.Fatalf("a pending payment settled something: status=%s balance=%s (was %s)", withPending.Status, withPending.Balance, pendingBefore)
	}

	// Too much, by the smallest unit the column carries.
	if _, _, err := st.RecordStatementPayment(ctx, d.ID, store.PaymentInput{Amount: "600.100001", Reference: "TRF-OVER"}); !store.IsConflict(err) {
		t.Fatalf("overpayment = %v, want a conflict", err)
	}
	if _, _, err := st.RecordStatementPayment(ctx, d.ID, store.PaymentInput{Amount: "0", Reference: "TRF-ZERO"}); err == nil {
		t.Fatal("a zero payment must be refused")
	}
	// A gateway that delivers the same confirmation twice records one payment.
	if _, _, err := st.RecordStatementPayment(ctx, d.ID, store.PaymentInput{Amount: "1.000000", PaidAt: day1, Reference: "TRF-A"}); !store.IsConflict(err) {
		t.Fatalf("duplicate reference = %v, want a conflict", err)
	}

	// The rest settles it exactly.
	day2 := day1.AddDate(0, 0, 20)
	settled, second, err := st.RecordStatementPayment(ctx, d.ID, store.PaymentInput{Amount: "600.100000", PaidAt: day2, Reference: "TRF-B"})
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != store.StatusPaid || string(settled.Balance) != "0.000000" || string(settled.Paid) != "1000.100000" {
		t.Fatalf("after settlement: status=%s paid=%s balance=%s", settled.Status, settled.Paid, settled.Balance)
	}
	if settled.PaidAt == nil || !settled.PaidAt.Equal(day2) {
		t.Errorf("paid_at = %v, want the day the last payment arrived (%s)", settled.PaidAt, day2)
	}
	// The history carries the pending row too — it happened, it just settles
	// nothing — oldest first.
	if len(settled.Payments) != 3 || settled.Payments[0].ID != first.ID || settled.Payments[1].ID != pending.ID || settled.Payments[2].ID != second.ID {
		t.Fatalf("payment history = %+v", settled.Payments)
	}
	history, err := st.ListStatementPayments(ctx, store.CustomerScope(c.ID), d.ID)
	if err != nil || len(history) != 3 {
		t.Fatalf("customer-scoped history = %+v (err %v)", history, err)
	}
	other := invoiceCustomer(t, st, store.CustomerInput{Slug: "other", Name: "Other", AdminEmail: "o@other.example"})
	if _, err := st.ListStatementPayments(ctx, store.CustomerScope(other.ID), d.ID); err == nil {
		t.Fatal("another customer must not read this payment history")
	}
}

// A sent invoice past its due date reads overdue everywhere it is read —
// list and detail — and paying it is still legal from there.
func TestIntegrationOverdueIsDerivedOnEveryRead(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	// Terms of zero days — due on receipt — so a moment after issue it is
	// late, and the derived status says so with no sweeper anywhere.
	c := invoiceCustomer(t, st, store.CustomerInput{Slug: "late", Name: "Late payer", AdminEmail: "ap@late.example",
		Commercial: postpaidTransfer(), PaymentTermsDays: intp(0)})
	d := draft(t, st, c.ID, "2026-07-01", "250.000000")
	issued, _, err := st.IssueStatementOnce(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if issued.PaymentTermsDays == nil || *issued.PaymentTermsDays != 0 || issued.DueAt == nil || !issued.DueAt.Equal(*issued.IssuedAt) {
		t.Fatalf("due on receipt: terms=%v due=%v issued=%v", issued.PaymentTermsDays, issued.DueAt, issued.IssuedAt)
	}
	sent, _, err := st.SendStatement(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Status != store.StatusSent {
		t.Fatalf("stored status = %s, want sent — overdue is derived, never stored", sent.Status)
	}
	time.Sleep(1100 * time.Millisecond)
	one, err := st.GetStatement(ctx, store.OperatorScope, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if one.EffectiveStatus != store.StatusOverdue || one.Status != store.StatusSent {
		t.Fatalf("detail: status=%s effective=%s", one.Status, one.EffectiveStatus)
	}
	list, err := st.ListAllStatements(ctx, "")
	if err != nil || len(list) != 1 || list[0].EffectiveStatus != store.StatusOverdue {
		t.Fatalf("list = %+v (err %v)", list, err)
	}
	// overdue → paid is legal, and settles it.
	paid, _, err := st.RecordStatementPayment(ctx, d.ID, store.PaymentInput{Amount: "250.000000", Reference: "TRF-LATE"})
	if err != nil || paid.Status != store.StatusPaid || paid.EffectiveStatus != store.StatusPaid {
		t.Fatalf("overdue → paid: status=%s effective=%s err=%v", paid.Status, paid.EffectiveStatus, err)
	}
}

// The four commercial fields round-trip through create and patch, the
// deprecated billing_mode is derived on every write, and the customer cannot
// be deleted once any invoice has left draft.
func TestIntegrationCustomerCommercialRoundTrip(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	c := invoiceCustomer(t, st, store.CustomerInput{Slug: "round", Name: "Round", AdminEmail: "r@round.example"})
	if c.Charging != store.ChargingInformational || c.PaymentModel != "" || c.PaymentMethod != "" || c.GatewayName != "" {
		t.Fatalf("defaults: %+v", c.Commercial())
	}
	if c.BillingMode != store.BillingModeShowback || c.PaymentTermsDays != store.DefaultPaymentTermsDays {
		t.Fatalf("derived mode=%s terms=%d", c.BillingMode, c.PaymentTermsDays)
	}
	po, terms := "PO-77", 60
	updated, err := st.UpdateCustomer(ctx, c.ID, store.CustomerPatch{
		Charging: strp(store.ChargingBilled), PaymentModel: strp(store.PaymentModelPostpaid), PaymentMethod: strp(store.PaymentMethodTransfer),
		PORef: &po, PaymentTermsDays: &terms})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Commercial() != postpaidTransfer() || updated.PORef != "PO-77" || updated.PaymentTermsDays != 60 {
		t.Fatalf("patched = %+v", updated)
	}
	if updated.BillingMode != store.BillingModeReal {
		t.Fatalf("derived billing_mode = %s, want real", updated.BillingMode)
	}
	// An internal recharge derives chargeback; going informational derives
	// showback and clears the three fields that then mean nothing.
	internal, err := st.UpdateCustomer(ctx, c.ID, store.CustomerPatch{PaymentMethod: strp(store.PaymentMethodInternal)})
	if err != nil || internal.BillingMode != store.BillingModeChargeback {
		t.Fatalf("internal recharge: mode=%s err=%v", internal.BillingMode, err)
	}
	info, err := st.UpdateCustomer(ctx, c.ID, store.CustomerPatch{Charging: strp(store.ChargingInformational)})
	if err != nil {
		t.Fatal(err)
	}
	if info.BillingMode != store.BillingModeShowback || info.PaymentModel != "" || info.PaymentMethod != "" {
		t.Fatalf("informational = %+v (mode %s)", info.Commercial(), info.BillingMode)
	}
	// A legacy client patching billing_mode is translated, not refused.
	legacy, err := st.UpdateCustomer(ctx, c.ID, store.CustomerPatch{BillingMode: strp(store.BillingModeReal)})
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Commercial() != store.CommercialFromBillingMode(store.BillingModeReal) || legacy.BillingMode != store.BillingModeReal {
		t.Fatalf("legacy billing_mode patch = %+v (mode %s)", legacy.Commercial(), legacy.BillingMode)
	}
	// Inconsistent combinations are refused with a message that names the field.
	if _, err := st.UpdateCustomer(ctx, c.ID, store.CustomerPatch{PaymentMethod: strp("cheque")}); err == nil {
		t.Fatal("an unknown payment method must be refused")
	}
	if _, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "bad1", Name: "Bad", AdminEmail: "b@bad.example",
		Commercial: store.Commercial{Charging: store.ChargingInformational, PaymentModel: store.PaymentModelPrepaid}}); err == nil {
		t.Fatal("an informational customer with a payment model must be refused")
	}
	if _, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "bad2", Name: "Bad", AdminEmail: "b@bad.example",
		Commercial: store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPrepaid, PaymentMethod: store.PaymentMethodGateway}}); err == nil {
		t.Fatal("a gateway customer with no gateway_name must be refused")
	}
	tooLong := 400
	if _, err := st.UpdateCustomer(ctx, c.ID, store.CustomerPatch{PaymentTermsDays: &tooLong}); err == nil {
		t.Fatal("terms beyond a year must be refused")
	}
	d := draft(t, st, c.ID, "2026-08-01", "5.000000")
	if err := st.DeleteCustomer(ctx, c.ID); err != nil {
		t.Fatalf("a customer with only drafts is deletable: %v", err)
	}
	// Re-make it and take the statement past draft.
	c = invoiceCustomer(t, st, store.CustomerInput{Slug: "round", Name: "Round", AdminEmail: "r@round.example"})
	d = draft(t, st, c.ID, "2026-08-01", "5.000000")
	if _, _, err := st.IssueStatementOnce(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CancelStatement(ctx, d.ID, "void"); err != nil {
		t.Fatal(err)
	}
	err = st.DeleteCustomer(ctx, c.ID)
	if !store.IsConflict(err) {
		t.Fatalf("deleting a customer with a cancelled invoice = %v, want a conflict — it is still a financial record", err)
	}
	if !strings.Contains(err.Error(), "issued statement") {
		t.Fatalf("message = %v", err)
	}
}
