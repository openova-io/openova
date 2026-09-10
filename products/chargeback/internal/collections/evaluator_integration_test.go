package collections

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/platform"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

type recMail struct {
	mu   sync.Mutex
	msgs []string
}

func (m *recMail) Send(_ context.Context, to, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, to+"|"+subject+"|"+body)
	return nil
}

func (m *recMail) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.msgs)
}

func (m *recMail) subjects() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []string{}
	for _, s := range m.msgs {
		out = append(out, strings.SplitN(s, "|", 3)[1])
	}
	return out
}

func postpaid() store.Commercial {
	return store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPostpaid, PaymentMethod: store.PaymentMethodTransfer}
}

// issueDue writes and issues a statement whose due date is `terms` days after
// the issue instant (now), so the test can walk the calendar from there.
func issueDue(t *testing.T, st *store.Store, customerID, period, total string, terms int) store.Statement {
	t.Helper()
	ctx := context.Background()
	start, _ := time.Parse("2006-01-02", period)
	d, err := st.WriteDraftStatement(ctx, store.StatementDraft{CustomerID: customerID, PeriodStart: start, PeriodEnd: start.AddDate(0, 1, -1), Currency: "OMR",
		Subtotal: store.Decimal(total), TaxRate: "0", Tax: "0", Total: store.Decimal(total)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateStatementInvoice(ctx, d.ID, store.StatementInvoicePatch{PaymentTermsDays: &terms}); err != nil {
		t.Fatal(err)
	}
	s, _, err := st.IssueStatementOnce(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func always(context.Context) (bool, error) { return true, nil }

// Reminders fire once per stage and never twice, however often the
// evaluator runs, and the escalation suspends the Organization at the
// configured age through the platform seam; settling the invoice resumes it.
func TestIntegrationRemindersFireOncePerStageAndEscalationSuspendsThenResumes(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	if _, err := st.UpdateBillingSettings(ctx, store.BillingSettings{DiscountRule: store.DefaultDiscountRule, InvoicePrefix: "INV", CommercialProvider: store.ProviderInternal,
		ReminderDays: []int{-3, 0, 7, 14, 30}, EscalationDays: 45, EscalationAction: store.EscalationSuspend}); err != nil {
		t.Fatal(err)
	}
	slug := "acme-org"
	c, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: slug, Name: "Acme Org", AdminEmail: "ap@acme.example", Kind: "organization", OrgSlug: slug, Commercial: postpaid()})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertCustomerUser(ctx, c.ID, "fin@acme.example", "admin"); err != nil {
		t.Fatal(err)
	}
	inv := issueDue(t, st, c.ID, "2026-01-01", "1000.000000", 30)
	due := *inv.DueAt

	mail := &recMail{}
	plat := &platform.Fake{}
	enf := &Enforcer{Store: st, Platform: plat}
	ev := &Evaluator{Store: st, Mail: mail, Enforcer: enf, PublicURL: "https://billing.t99.omani.works", Owns: always}

	run := func(day int) Report {
		t.Helper()
		rep, err := ev.RunAt(ctx, due.AddDate(0, 0, day).Add(time.Hour))
		if err != nil {
			t.Fatalf("run at day %d: %v", day, err)
		}
		return rep
	}
	// Ten days before due: nothing is due to be sent.
	if rep := run(-10); rep.Reminders != 0 || rep.Mails != 0 {
		t.Fatalf("day -10 = %+v", rep)
	}
	// Three days before: the first stage, to both admins. Running again the
	// same day sends nothing more.
	if rep := run(-3); rep.Reminders != 1 || rep.Mails != 2 {
		t.Fatalf("day -3 = %+v", rep)
	}
	if rep := run(-3); rep.Reminders != 0 || rep.Mails != 0 {
		t.Fatalf("day -3 again = %+v, must send nothing twice", rep)
	}
	if subj := mail.subjects(); len(subj) != 2 || !strings.Contains(subj[0], "is due on") {
		t.Fatalf("subjects = %v", subj)
	}
	// On the due date: stage 0.
	if rep := run(0); rep.Reminders != 1 {
		t.Fatalf("day 0 = %+v", rep)
	}
	// A skipped run: at day 20 the 7 and 14 stages are both owed, and each
	// is sent exactly once.
	if rep := run(20); rep.Reminders != 2 {
		t.Fatalf("day 20 = %+v", rep)
	}
	if rep := run(21); rep.Reminders != 0 {
		t.Fatalf("day 21 = %+v", rep)
	}
	if rep := run(30); rep.Reminders != 1 || rep.Escalations != 0 {
		t.Fatalf("day 30 = %+v", rep)
	}
	if got := mail.count(); got != 10 {
		t.Fatalf("mails so far = %d, want 5 stages × 2 admins", got)
	}
	// Day 44: not yet. Day 45: the escalation, once, and the platform is
	// asked to suspend the Organization.
	if rep := run(44); rep.Escalations != 0 || rep.Suspended != 0 {
		t.Fatalf("day 44 = %+v", rep)
	}
	rep := run(45)
	if rep.Escalations != 1 || rep.Suspended != 1 {
		t.Fatalf("day 45 = %+v", rep)
	}
	calls := plat.Recorded()
	if len(calls) != 1 || calls[0].Action != "suspend" || calls[0].Slug != slug || !strings.Contains(calls[0].Reason, "45 days overdue") {
		t.Fatalf("platform calls = %+v", calls)
	}
	after, _ := st.GetCustomer(ctx, store.OperatorScope, c.ID)
	if after.PlatformSuspendedAt == nil || after.SuspensionSource != store.SuspendSourceCollections || after.Status != "suspended" {
		t.Fatalf("customer after escalation = suspended_at=%v source=%s status=%s", after.PlatformSuspendedAt, after.SuspensionSource, after.Status)
	}
	// Day 60: nothing escalates twice, nothing resumes while it is still owed.
	if rep := run(60); rep.Escalations != 0 || rep.Suspended != 0 || rep.Resumed != 0 {
		t.Fatalf("day 60 = %+v", rep)
	}
	if len(plat.Recorded()) != 1 {
		t.Fatalf("the platform must be asked once: %+v", plat.Recorded())
	}
	// Settling the invoice resumes the Organization on the next pass.
	if _, _, err := st.RecordStatementPayment(ctx, inv.ID, store.PaymentInput{Amount: "1000.000000", Reference: "TRF-LATE"}); err != nil {
		t.Fatal(err)
	}
	if rep := run(61); rep.Resumed != 1 {
		t.Fatalf("day 61 = %+v, want the suspension lifted", rep)
	}
	calls = plat.Recorded()
	if len(calls) != 2 || calls[1].Action != "resume" || calls[1].Slug != slug {
		t.Fatalf("platform calls = %+v", calls)
	}
	resumed, _ := st.GetCustomer(ctx, store.OperatorScope, c.ID)
	if resumed.PlatformSuspendedAt != nil || resumed.Status != "active" {
		t.Fatalf("customer after settlement = %+v", resumed)
	}
	// The trail is on the customer.
	trail, err := st.ListSuspensions(ctx, store.OperatorScope, c.ID)
	if err != nil || len(trail) != 2 || trail[0].Action != "resume" || trail[1].Action != "suspend" || !trail[1].OK {
		t.Fatalf("suspension trail = %+v (err %v)", trail, err)
	}
	// And the reminder rows say exactly what went out.
	sent, err := st.ListReminders(ctx, inv.ID)
	if err != nil || len(sent) != 6 {
		t.Fatalf("reminder rows = %+v (err %v)", sent, err)
	}
}

// A platform that refuses is recorded as a failed attempt, the customer's
// own status still flips, and the next pass tries again.
func TestIntegrationPlatformRefusalIsRecordedAndRetried(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	if _, err := st.UpdateBillingSettings(ctx, store.BillingSettings{DiscountRule: store.DefaultDiscountRule, InvoicePrefix: "INV", CommercialProvider: store.ProviderInternal,
		ReminderDays: []int{}, EscalationDays: 10, EscalationAction: store.EscalationSuspend}); err != nil {
		t.Fatal(err)
	}
	c, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "flaky-org", Name: "Flaky", AdminEmail: "ap@flaky.example", Kind: "organization", OrgSlug: "flaky-org", Commercial: postpaid()})
	if err != nil {
		t.Fatal(err)
	}
	inv := issueDue(t, st, c.ID, "2026-01-01", "10.000000", 0)
	plat := &platform.Fake{Err: context.DeadlineExceeded}
	enf := &Enforcer{Store: st, Platform: plat}
	ev := &Evaluator{Store: st, Mail: &recMail{}, Enforcer: enf, Owns: always}
	rep, err := ev.RunAt(ctx, inv.DueAt.AddDate(0, 0, 10).Add(time.Hour))
	if err != nil || rep.Escalations != 1 || rep.Suspended != 0 || rep.Errors != 1 {
		t.Fatalf("refused pass = %+v (err %v)", rep, err)
	}
	after, _ := st.GetCustomer(ctx, store.OperatorScope, c.ID)
	if after.PlatformSuspendedAt != nil || after.Status != "suspended" {
		t.Fatalf("a refused platform suspend still flips the customer here: %+v", after.Status)
	}
	trail, _ := st.ListSuspensions(ctx, store.OperatorScope, c.ID)
	if len(trail) != 1 || trail[0].OK || !strings.Contains(trail[0].Error, "deadline") {
		t.Fatalf("trail = %+v", trail)
	}
	// The platform recovers; the operator's explicit suspend goes through.
	plat.Err = nil
	if _, err := enf.Suspend(ctx, c.ID, "operator", store.SuspendSourceOperator, "ops@nc.example"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetCustomer(ctx, store.OperatorScope, c.ID); got.PlatformSuspendedAt == nil {
		t.Fatal("the retry must record the platform suspension")
	}
}

// In external mode the evaluator runs nothing: collections are the billing
// system's, and enforcement waits for its explicit command.
func TestIntegrationEvaluatorSkipsWhenCollectionsAreExternal(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	c, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "ext", Name: "Ext", AdminEmail: "ap@ext.example", Commercial: postpaid()})
	if err != nil {
		t.Fatal(err)
	}
	inv := issueDue(t, st, c.ID, "2026-01-01", "10.000000", 0)
	mail := &recMail{}
	ev := &Evaluator{Store: st, Mail: mail, Enforcer: &Enforcer{Store: st, Platform: &platform.Fake{}}, Owns: func(context.Context) (bool, error) { return false, nil }}
	rep, err := ev.RunAt(ctx, inv.DueAt.AddDate(0, 0, 100))
	if err != nil || !rep.Skipped || rep.Reminders != 0 || mail.count() != 0 {
		t.Fatalf("external mode pass = %+v (err %v, mails %d)", rep, err, mail.count())
	}
}

// The prepaid wallet: the low-balance alert fires once per crossing and
// suspend-at-zero calls the platform; a top-up lifts it.
func TestIntegrationWalletAlertsOnceAndSuspendsAtZero(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	threshold := store.Decimal("100.000000")
	c, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "sme-org", Name: "SME", AdminEmail: "ap@sme.example", Kind: "organization", OrgSlug: "sme-org",
		Commercial:          store.Commercial{Charging: store.ChargingBilled, PaymentModel: store.PaymentModelPrepaid, PaymentMethod: store.PaymentMethodTransfer},
		LowBalanceThreshold: &threshold, SuspendAtZero: true})
	if err != nil {
		t.Fatal(err)
	}
	mail := &recMail{}
	plat := &platform.Fake{}
	w := &Wallet{Store: st, Mail: mail, Enforcer: &Enforcer{Store: st, Platform: plat}, PublicURL: "https://billing.t99.omani.works", Owns: always}
	now := time.Now().UTC()
	if _, err := st.RecordCustomerPayment(ctx, store.CustomerPaymentInput{CustomerID: c.ID, Payment: store.PaymentInput{Amount: "150.000000", Reference: "TOPUP-1"}}); err != nil {
		t.Fatal(err)
	}
	if out, err := w.Check(ctx, c.ID, now); err != nil || out.Alerted || out.Suspended {
		t.Fatalf("150 above a 100 threshold: %+v (err %v)", out, err)
	}
	// An issue of 80 leaves 70: below the threshold, one alert.
	issueDue(t, st, c.ID, "2026-01-01", "80.000000", 0)
	if out, err := w.Check(ctx, c.ID, now); err != nil || !out.Alerted || out.Suspended {
		t.Fatalf("70 left: %+v (err %v)", out, err)
	}
	if out, _ := w.Check(ctx, c.ID, now); out.Alerted {
		t.Fatal("the alert must not repeat while still below")
	}
	if mail.count() != 1 || !strings.Contains(mail.subjects()[0], "Low balance") {
		t.Fatalf("mails = %v", mail.subjects())
	}
	// An issue of 100 exhausts the 70 and leaves 30 owing: suspended at zero.
	issueDue(t, st, c.ID, "2026-02-01", "100.000000", 0)
	if out, err := w.Check(ctx, c.ID, now); err != nil || !out.Suspended {
		t.Fatalf("at zero: %+v (err %v)", out, err)
	}
	if calls := plat.Recorded(); len(calls) != 1 || calls[0].Action != "suspend" || calls[0].Slug != "sme-org" {
		t.Fatalf("platform = %+v", calls)
	}
	// A top-up settles the 30 (applied explicitly) and the wallet resumes.
	if _, err := st.RecordCustomerPayment(ctx, store.CustomerPaymentInput{CustomerID: c.ID, Payment: store.PaymentInput{Amount: "200.000000", Reference: "TOPUP-2"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApplyCredit(ctx, c.ID, nil, "ops"); err != nil {
		t.Fatal(err)
	}
	if out, err := w.Check(ctx, c.ID, now); err != nil || !out.Resumed {
		t.Fatalf("after top-up: %+v (err %v)", out, err)
	}
	if calls := plat.Recorded(); len(calls) != 2 || calls[1].Action != "resume" {
		t.Fatalf("platform = %+v", calls)
	}
	// 170 left, above the threshold: the alert re-arms for the next crossing.
	issueDue(t, st, c.ID, "2026-03-01", "100.000000", 0)
	if out, _ := w.Check(ctx, c.ID, now); !out.Alerted {
		t.Fatal("a fresh crossing alerts again")
	}
}
