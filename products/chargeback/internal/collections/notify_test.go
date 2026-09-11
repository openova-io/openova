package collections

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/notify"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The three notices this package emits used to be composed here, as a
// subject string and a body string. They now travel as catalogue events
// (DESIGN.md §21), which means two halves have to keep agreeing: the PAYLOAD
// this package builds and the TEMPLATE that renders it. These tests hold the
// pair together against the exact bytes the old renderers produced —
// transcribed from reminderMail, escalationMail and LowBalanceMail as they
// stood before §21, not read back out of the template.

type capture struct {
	mu   sync.Mutex
	sent []string
}

func (c *capture) Send(_ context.Context, to, subject, body string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, to+"\x00"+subject+"\x00"+body)
	return nil
}

func (c *capture) one(t *testing.T) (to, subject, body string) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sent) != 1 {
		t.Fatalf("captured %d messages, want 1: %q", len(c.sent), c.sent)
	}
	parts := strings.SplitN(c.sent[0], "\x00", 3)
	return parts[0], parts[1], parts[2]
}

// emit runs one event through a notifier over the capturing sender — the
// same path the evaluator and the wallet take.
func emit(t *testing.T, event string, payload map[string]any) (subject, body string) {
	t.Helper()
	cap := &capture{}
	n := &notify.Notifier{Channels: notify.DefaultChannels(cap)}
	if _, err := n.Send(context.Background(), notify.Request{Event: event, To: "ap@acme.example", Payload: payload}); err != nil {
		t.Fatalf("send %s: %v", event, err)
	}
	_, subject, body = cap.one(t)
	return subject, body
}

// openInvoice is the invoice these notices are built from: due 2 February
// 2026, 1000 OMR outstanding, covering January.
func openInvoice(number string) store.OpenInvoice {
	return store.OpenInvoice{
		StatementID: "st-1", CustomerID: "c-acme", CustomerName: "Acme Org", InvoiceNumber: number,
		PeriodStart: "2026-01-01", PeriodEnd: "2026-01-31", Currency: "OMR", Outstanding: "1000.000000",
		IssuedAt:   time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
		DueAt:      time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC),
		AdminEmail: "ap@acme.example",
	}
}

func TestReminderRendersExactlyWhatTheOldRendererDid(t *testing.T) {
	link := "https://billing.t99.omani.works/statements/st-1"
	const tail = "Period:      2026-01-01 to 2026-01-31\nIssued:      1 January 2026\nDue:         2 February 2026\nOutstanding: 1000 OMR\n\n" +
		"You can view the invoice here:\n" + "https://billing.t99.omani.works/statements/st-1" + "\n\n" +
		"If you have already paid, please disregard this message; payments can take a few days to be recorded.\n"

	for _, tc := range []struct {
		name    string
		days    int
		subject string
		opening string
	}{
		{
			name: "three days before due", days: -3,
			subject: "Reminder: Invoice INV-000123 for 1000 OMR is due on 2 February 2026",
			opening: "Hello Acme Org,\n\nThis is a reminder that invoice INV-000123 for 1000 OMR is due on 2 February 2026, in 3 days.\n\n",
		},
		{
			name: "on the due date", days: 0,
			subject: "Due today: Invoice INV-000123 for 1000 OMR",
			opening: "Hello Acme Org,\n\nThis is a reminder that invoice INV-000123 for 1000 OMR is due today, 2 February 2026.\n\n",
		},
		{
			name: "one day after — the singular", days: 1,
			subject: "Overdue: Invoice INV-000123 for 1000 OMR, 1 day past due",
			opening: "Hello Acme Org,\n\nThis is a reminder that invoice INV-000123 for 1000 OMR was due on 2 February 2026 and is 1 day overdue.\n\n",
		},
		{
			name: "thirty days after", days: 30,
			subject: "Overdue: Invoice INV-000123 for 1000 OMR, 30 days past due",
			opening: "Hello Acme Org,\n\nThis is a reminder that invoice INV-000123 for 1000 OMR was due on 2 February 2026 and is 30 days overdue.\n\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			subject, body := emit(t, notify.EventCollectionsReminder, reminderPayload(openInvoice("INV-000123"), tc.days, tc.days, link))
			if subject != tc.subject {
				t.Errorf("subject\n got %q\nwant %q", subject, tc.subject)
			}
			if want := tc.opening + tail; body != want {
				t.Errorf("body\n got %q\nwant %q", body, want)
			}
		})
	}

	// A statement with no invoice number is named by its period, and the
	// subject capitalises that phrase — the one piece of prose the old
	// renderer built with a manual first-letter upper-case.
	subject, body := emit(t, notify.EventCollectionsReminder, reminderPayload(openInvoice(""), 0, 0, link))
	if subject != "Due today: The statement for 2026-01 for 1000 OMR" {
		t.Errorf("subject = %q", subject)
	}
	if !strings.Contains(body, "reminder that the statement for 2026-01 for 1000 OMR is due today") {
		t.Errorf("body = %q", body)
	}
}

func TestEscalationRendersExactlyWhatTheOldRendererDid(t *testing.T) {
	link := "https://billing.t99.omani.works/statements/st-1"
	inv := openInvoice("INV-000123")

	subject, body := emit(t, notify.EventCollectionsEscalation, escalationPayload(inv, store.EscalationSuspend, 45, link))
	if want := "Action required: invoice INV-000123 for 1000 OMR is 45 days overdue"; subject != want {
		t.Errorf("subject\n got %q\nwant %q", subject, want)
	}
	want := "Hello Acme Org,\n\nInvoice INV-000123 for 1000 OMR was due on 2 February 2026 and is now 45 days overdue.\n\n" +
		"Service for your account is being suspended until the outstanding balance is settled. It resumes automatically once payment is recorded.\n\n" +
		"You can view the invoice here:\n" + link + "\n"
	if body != want {
		t.Errorf("suspend body\n got %q\nwant %q", body, want)
	}

	_, body = emit(t, notify.EventCollectionsEscalation, escalationPayload(inv, store.EscalationNotify, 45, link))
	want = "Hello Acme Org,\n\nInvoice INV-000123 for 1000 OMR was due on 2 February 2026 and is now 45 days overdue.\n\n" +
		"Please settle the outstanding balance now to avoid a suspension of service.\n\n" +
		"You can view the invoice here:\n" + link + "\n"
	if body != want {
		t.Errorf("notify body\n got %q\nwant %q", body, want)
	}
}

func TestLowBalanceRendersExactlyWhatTheOldRendererDid(t *testing.T) {
	link := "https://billing.t99.omani.works/my/statements"
	c := store.Customer{Name: "Acme Org", SuspendAtZero: true}

	subject, body := emit(t, notify.EventAccountLowBalance, LowBalancePayload(c, "12.500000", "50.000000", "OMR", link))
	if want := "Low balance: 12.5 OMR left on your account"; subject != want {
		t.Errorf("subject\n got %q\nwant %q", subject, want)
	}
	want := "Hello Acme Org,\n\nYour prepaid balance is 12.5 OMR, below the 50 OMR alert threshold.\n\n" +
		"When the balance reaches zero, service is suspended until it is topped up.\n\n" +
		"Top up here:\n" + link + "\n"
	if body != want {
		t.Errorf("body\n got %q\nwant %q", body, want)
	}

	c.SuspendAtZero = false
	_, body = emit(t, notify.EventAccountLowBalance, LowBalancePayload(c, "12.500000", "50.000000", "OMR", link))
	want = "Hello Acme Org,\n\nYour prepaid balance is 12.5 OMR, below the 50 OMR alert threshold.\n\n" +
		"Top up here:\n" + link + "\n"
	if body != want {
		t.Errorf("body without suspend-at-zero\n got %q\nwant %q", body, want)
	}
}

// Both collections events are MANDATORY in the catalogue. This is the clause
// that matters most in this package: a refactor that let a preference
// silence a dunning reminder would be a serious defect, and the evaluator
// never consults a preference of its own — it relies on the catalogue saying
// so.
func TestTheTwoCollectionsNoticesAreMandatory(t *testing.T) {
	for _, key := range []string{notify.EventCollectionsReminder, notify.EventCollectionsEscalation} {
		e, ok := notify.Lookup(key)
		if !ok {
			t.Fatalf("%s is not in the catalogue", key)
		}
		if !e.Mandatory {
			t.Fatalf("%s must be mandatory: a customer cannot switch off a dunning notice", key)
		}
	}
}
