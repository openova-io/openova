package notify

import (
	"sort"
	"strings"
	"testing"
)

// The catalogue and the template set have to agree, and the English
// templates have to render — character for character — the messages this
// product was already sending before §21 existed. Those two properties are
// what make the refactor a refactor rather than a rewrite of every notice
// the product sends, and they are asserted here rather than assumed.

func TestCatalogueIsWellFormedAndEveryEventHasAnEnglishTemplate(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range Events() {
		if e.Key == "" || e.Title == "" || e.Desc == "" || e.Category == "" || e.Source == "" {
			t.Errorf("event %+v is missing a field", e)
		}
		if seen[e.Key] {
			t.Errorf("duplicate event key %q", e.Key)
		}
		seen[e.Key] = true
		if len(e.Channels) == 0 {
			t.Errorf("%s declares no channel", e.Key)
		}
		for _, c := range e.Channels {
			if !ValidChannel(c) {
				t.Errorf("%s declares unknown channel %q", e.Key, c)
			}
		}
		if len(e.Payload) == 0 && e.Key != EventAuthPIN {
			t.Errorf("%s documents no payload", e.Key)
		}
		if _, locale, ok := TemplateFor(e.Key, DefaultLocale); !ok || locale != DefaultLocale {
			t.Errorf("%s has no %s template", e.Key, DefaultLocale)
		}
		// Every event in the shipped catalogue was being sent
		// unconditionally before §21. An unset preference must reproduce
		// that exactly, so none of them may ship default-off.
		if !e.DefaultOn {
			t.Errorf("%s ships default-off; every event this catalogue carries was already being sent unconditionally", e.Key)
		}
	}
	if len(seen) != len(Keys()) {
		t.Fatalf("Keys() = %d, catalogue = %d", len(Keys()), len(seen))
	}
}

// A template that reaches for a field no call site supplies renders a blank
// where a figure should be — the failure that is invisible until a customer
// reads it. The catalogue's declared payload is the contract; this holds the
// two together.
func TestTemplatesOnlyReferenceDeclaredFields(t *testing.T) {
	for _, doc := range TemplateDocs() {
		e, ok := Lookup(doc.Event)
		if !ok {
			t.Fatalf("template for unknown event %q", doc.Event)
		}
		declared := map[string]bool{}
		for _, f := range e.Payload {
			declared[f.Name] = true
		}
		for _, ref := range referencedFields(doc.Subject + "\n" + doc.Body) {
			if !declared[ref] {
				t.Errorf("%s (%s) references .%s, which the catalogue does not declare", doc.Event, doc.Locale, ref)
			}
		}
	}
}

// Every DECLARED payload field should be reachable from the English
// templates or from the console; a field nobody reads is a field a call site
// keeps computing for nothing. `stage` is the deliberate exception: it is
// carried so a translator (or an operator reading the delivery log) can tell
// which reminder stage a notice belongs to, and English does not print it.
func TestDeclaredFieldsAreUsedOrDeliberatelyNot(t *testing.T) {
	allowUnused := map[string]bool{"collections.reminder.stage": true, "statement.issued.statement_id": true,
		"statement.issued.period": true, "report.scheduled.schedule_id": true,
		"report.scheduled.schedule_name": true, "report.scheduled.cadence": true}
	for _, e := range Events() {
		used := map[string]bool{}
		for _, doc := range TemplateDocs() {
			if doc.Event != e.Key {
				continue
			}
			for _, ref := range referencedFields(doc.Subject + "\n" + doc.Body) {
				used[ref] = true
			}
		}
		for _, f := range e.Payload {
			if !used[f.Name] && !allowUnused[e.Key+"."+f.Name] {
				t.Errorf("%s declares .%s and no template reads it", e.Key, f.Name)
			}
		}
	}
}

// The golden set. Each expectation below is the message the product's own
// code produced BEFORE §21 — transcribed from the call site it came from,
// not from the template it now renders, which is the only way this test can
// catch a template that drifted.
func TestEnglishTemplatesRenderTheMessageTheProductAlreadySent(t *testing.T) {
	cases := []struct {
		name    string
		event   string
		payload map[string]any
		subject string
		body    string
		// from names the function that produced this exact text before
		// §21, so a failure points at what to compare against.
		from string
	}{
		{
			name:    "the sign-in code",
			event:   EventAuthPIN,
			from:    "internal/api/auth.go pinRequest",
			payload: map[string]any{"code": "042317", "minutes": 10},
			subject: "Your sign-in code",
			body:    "Your chargeback sign-in code is 042317. It expires in 10 minutes.",
		},
		{
			name:  "the activation invite",
			event: EventCustomerInvite,
			from:  "internal/api/customers.go inviteCustomer",
			payload: map[string]any{
				"customer_name": "Muscat Health",
				"url":           "https://billing.t99.omani.works/activate/abc123",
				"expires":       "2026-09-19 12:00 UTC",
			},
			subject: "Activate your chargeback account",
			body: "Hello Muscat Health,\n\nActivate your chargeback account and connect your cloud projects:\n\n" +
				"https://billing.t99.omani.works/activate/abc123\n\nThe link expires on 2026-09-19 12:00 UTC.\n",
		},
		{
			name:  "a reminder BEFORE the due date",
			event: EventCollectionsReminder,
			from:  "internal/collections/mail.go reminderMail, days < 0",
			payload: map[string]any{
				"customer_name": "Acme Org", "invoice_name": "invoice INV-000123", "invoice_title": "Invoice INV-000123",
				"amount": "1000 OMR", "due_date": "2 February 2026", "issued_date": "1 January 2026",
				"period_start": "2026-01-01", "period_end": "2026-01-31", "days": -3, "stage": -3,
				"link": "https://billing.t99.omani.works/statements/st-1",
			},
			subject: "Reminder: Invoice INV-000123 for 1000 OMR is due on 2 February 2026",
			body: "Hello Acme Org,\n\nThis is a reminder that invoice INV-000123 for 1000 OMR is due on 2 February 2026, in 3 days.\n\n" +
				"Period:      2026-01-01 to 2026-01-31\nIssued:      1 January 2026\nDue:         2 February 2026\nOutstanding: 1000 OMR\n\n" +
				"You can view the invoice here:\nhttps://billing.t99.omani.works/statements/st-1\n\n" +
				"If you have already paid, please disregard this message; payments can take a few days to be recorded.\n",
		},
		{
			name:  "a reminder ON the due date",
			event: EventCollectionsReminder,
			from:  "internal/collections/mail.go reminderMail, days == 0",
			payload: map[string]any{
				"customer_name": "Acme Org", "invoice_name": "the statement for 2026-01", "invoice_title": "The statement for 2026-01",
				"amount": "12.5 OMR", "due_date": "2 February 2026", "issued_date": "1 January 2026",
				"period_start": "2026-01-01", "period_end": "2026-01-31", "days": 0, "stage": 0,
				"link": "https://billing.t99.omani.works/statements/st-1",
			},
			subject: "Due today: The statement for 2026-01 for 12.5 OMR",
			body: "Hello Acme Org,\n\nThis is a reminder that the statement for 2026-01 for 12.5 OMR is due today, 2 February 2026.\n\n" +
				"Period:      2026-01-01 to 2026-01-31\nIssued:      1 January 2026\nDue:         2 February 2026\nOutstanding: 12.5 OMR\n\n" +
				"You can view the invoice here:\nhttps://billing.t99.omani.works/statements/st-1\n\n" +
				"If you have already paid, please disregard this message; payments can take a few days to be recorded.\n",
		},
		{
			name:  "a reminder ONE day past due — the singular",
			event: EventCollectionsReminder,
			from:  "internal/collections/mail.go reminderMail, days == 1",
			payload: map[string]any{
				"customer_name": "Acme Org", "invoice_name": "invoice INV-000123", "invoice_title": "Invoice INV-000123",
				"amount": "1000 OMR", "due_date": "2 February 2026", "issued_date": "1 January 2026",
				"period_start": "2026-01-01", "period_end": "2026-01-31", "days": 1, "stage": 0,
				"link": "https://billing.t99.omani.works/statements/st-1",
			},
			subject: "Overdue: Invoice INV-000123 for 1000 OMR, 1 day past due",
			body: "Hello Acme Org,\n\nThis is a reminder that invoice INV-000123 for 1000 OMR was due on 2 February 2026 and is 1 day overdue.\n\n" +
				"Period:      2026-01-01 to 2026-01-31\nIssued:      1 January 2026\nDue:         2 February 2026\nOutstanding: 1000 OMR\n\n" +
				"You can view the invoice here:\nhttps://billing.t99.omani.works/statements/st-1\n\n" +
				"If you have already paid, please disregard this message; payments can take a few days to be recorded.\n",
		},
		{
			name:  "the escalation that suspends",
			event: EventCollectionsEscalation,
			from:  "internal/collections/mail.go escalationMail, action = suspend",
			payload: map[string]any{
				"customer_name": "Acme Org", "invoice_name": "invoice INV-000123", "invoice_title": "Invoice INV-000123",
				"amount": "1000 OMR", "due_date": "2 February 2026", "days": 45, "suspending": true,
				"link": "https://billing.t99.omani.works/statements/st-1",
			},
			subject: "Action required: invoice INV-000123 for 1000 OMR is 45 days overdue",
			body: "Hello Acme Org,\n\nInvoice INV-000123 for 1000 OMR was due on 2 February 2026 and is now 45 days overdue.\n\n" +
				"Service for your account is being suspended until the outstanding balance is settled. It resumes automatically once payment is recorded.\n\n" +
				"You can view the invoice here:\nhttps://billing.t99.omani.works/statements/st-1\n",
		},
		{
			name:  "the escalation that only warns",
			event: EventCollectionsEscalation,
			from:  "internal/collections/mail.go escalationMail, action = notify",
			payload: map[string]any{
				"customer_name": "Acme Org", "invoice_name": "invoice INV-000123", "invoice_title": "Invoice INV-000123",
				"amount": "1000 OMR", "due_date": "2 February 2026", "days": 45, "suspending": false,
				"link": "https://billing.t99.omani.works/statements/st-1",
			},
			subject: "Action required: invoice INV-000123 for 1000 OMR is 45 days overdue",
			body: "Hello Acme Org,\n\nInvoice INV-000123 for 1000 OMR was due on 2 February 2026 and is now 45 days overdue.\n\n" +
				"Please settle the outstanding balance now to avoid a suspension of service.\n\n" +
				"You can view the invoice here:\nhttps://billing.t99.omani.works/statements/st-1\n",
		},
		{
			name:  "the low-balance alert, with suspend-at-zero on",
			event: EventAccountLowBalance,
			from:  "internal/collections/mail.go LowBalanceMail",
			payload: map[string]any{
				"customer_name": "Acme Org", "available": "12.5", "threshold": "50", "currency": "OMR",
				"suspend_at_zero": true, "link": "https://billing.t99.omani.works/my/statements",
			},
			subject: "Low balance: 12.5 OMR left on your account",
			body: "Hello Acme Org,\n\nYour prepaid balance is 12.5 OMR, below the 50 OMR alert threshold.\n\n" +
				"When the balance reaches zero, service is suspended until it is topped up.\n\n" +
				"Top up here:\nhttps://billing.t99.omani.works/my/statements\n",
		},
		{
			name:  "the low-balance alert, with suspend-at-zero off",
			event: EventAccountLowBalance,
			from:  "internal/collections/mail.go LowBalanceMail",
			payload: map[string]any{
				"customer_name": "Acme Org", "available": "12.5", "threshold": "50", "currency": "OMR",
				"suspend_at_zero": false, "link": "https://billing.t99.omani.works/my/statements",
			},
			subject: "Low balance: 12.5 OMR left on your account",
			body: "Hello Acme Org,\n\nYour prepaid balance is 12.5 OMR, below the 50 OMR alert threshold.\n\n" +
				"Top up here:\nhttps://billing.t99.omani.works/my/statements\n",
		},
		{
			name:  "a budget crossing with a forecast",
			event: EventBudgetThreshold,
			from:  "internal/budget/evaluator.go crossingMail, Forecast != nil",
			payload: map[string]any{
				"budget_name": "Acme cap", "scope": "Acme Org", "threshold": 80, "amount": "100", "currency": "OMR",
				"period": "2026-09", "actual": "85", "pct_actual": "85.0", "forecast": "121.43", "pct_forecast": "121.4",
				"status": "over",
			},
			subject: "Budget Acme cap: 80% of 100 OMR reached for 2026-09",
			body: "Budget \"Acme cap\" (Acme Org) has reached 80% of its 100 OMR cap for 2026-09.\n\n" +
				"Actual so far: 85 OMR (85.0% of the budget)\n" +
				"Month-end forecast: 121.43 OMR (121.4% of the budget)\n" +
				"Status: over\n",
		},
		{
			name:  "a budget crossing with no forecast",
			event: EventBudgetThreshold,
			from:  "internal/budget/evaluator.go crossingMail, Forecast == nil",
			payload: map[string]any{
				"budget_name": "Sovereign cap", "scope": "all customers", "threshold": 50, "amount": "1000", "currency": "OMR",
				"period": "2026-09", "actual": "500", "pct_actual": "50.0", "forecast": "", "pct_forecast": "",
				"status": "on track",
			},
			subject: "Budget Sovereign cap: 50% of 1000 OMR reached for 2026-09",
			body: "Budget \"Sovereign cap\" (all customers) has reached 50% of its 1000 OMR cap for 2026-09.\n\n" +
				"Actual so far: 500 OMR (50.0% of the budget)\n" +
				"Status: on track\n",
		},
		{
			// The two rendered documents pass through UNTOUCHED: the
			// template is {{.document}} and nothing may be added, trimmed
			// or re-wrapped around it. A column-aligned money table is
			// exactly what a helpful "improvement" would ruin.
			name:  "the statement notification carries the rendered document verbatim",
			event: EventStatementIssued,
			from:  "internal/report/statement.go RenderStatement",
			payload: map[string]any{
				"subject":      "Statement for Acme Org — August 2026: 1,234.560 OMR",
				"document":     "Your statement for August 2026 has been issued.\n\n  List subtotal        1,300.000 OMR\n\nTrailing space kept:   \n",
				"statement_id": "st-1", "period": "2026-08",
			},
			subject: "Statement for Acme Org — August 2026: 1,234.560 OMR",
			body:    "Your statement for August 2026 has been issued.\n\n  List subtotal        1,300.000 OMR\n\nTrailing space kept:   \n",
		},
		{
			name:  "the scheduled report carries the rendered document verbatim",
			event: EventReportScheduled,
			from:  "internal/report/render.go Render",
			payload: map[string]any{
				"subject":     "Cost report: Monthly — August 2026: 9,876.540 OMR",
				"document":    "Cost report: Monthly\nWindow: August 2026 (monthly)\n\nSUMMARY\n  Total  9,876.540 OMR\n",
				"schedule_id": "sc-1", "schedule_name": "Monthly", "cadence": "monthly",
			},
			subject: "Cost report: Monthly — August 2026: 9,876.540 OMR",
			body:    "Cost report: Monthly\nWindow: August 2026 (monthly)\n\nSUMMARY\n  Total  9,876.540 OMR\n",
		},
	}

	covered := map[string]bool{}
	for _, tc := range cases {
		covered[tc.event] = true
		t.Run(tc.name, func(t *testing.T) {
			msg, err := Render(tc.event, DefaultLocale, tc.payload)
			if err != nil {
				t.Fatalf("render %s: %v", tc.event, err)
			}
			if msg.Subject != tc.subject {
				t.Errorf("subject (was built by %s)\n got %q\nwant %q", tc.from, msg.Subject, tc.subject)
			}
			if msg.Body != tc.body {
				t.Errorf("body (was built by %s)\n got %q\nwant %q", tc.from, msg.Body, tc.body)
			}
			if strings.Contains(msg.Body, "<no value>") || strings.Contains(msg.Subject, "<no value>") {
				t.Errorf("a payload field did not resolve: %q / %q", msg.Subject, msg.Body)
			}
		})
	}
	// The golden set must cover the WHOLE catalogue: an event added without
	// a golden case is one whose wording nothing is watching.
	var missing []string
	for _, k := range Keys() {
		if !covered[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("no golden case for %v", missing)
	}
}

// A subject is a header; a line break in one is a header injection. The
// templates have none today, and this is what stops one being added.
func TestSubjectNeverCarriesALineBreak(t *testing.T) {
	msg, err := Render(EventAuthPIN, DefaultLocale, map[string]any{"code": "1\n2\n3", "minutes": 10})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(msg.Subject, "\r\n") {
		t.Fatalf("subject carries a line break: %q", msg.Subject)
	}
}

// A SECOND LOCALE IS ADDITIVE: one more RegisterLocale call, for as many or
// as few events as it has translations for, and every untranslated event
// falls back to English. Nothing else in the package changes — which is the
// property §21.3 promises and this is the proof of it.
//
// The tag "zz" is a test-only locale; no translation is invented here.
func TestASecondLocaleIsPurelyAdditiveAndFallsBackToEnglish(t *testing.T) {
	before := len(Locales())
	RegisterLocale("zz", map[string]Template{
		EventAuthPIN: {Subject: `ZZ sign-in`, Body: `ZZ code {{.code}}`},
	})
	if len(Locales()) != before+1 {
		t.Fatalf("locales = %v, want one more than %d", Locales(), before)
	}
	msg, err := Render(EventAuthPIN, "zz", map[string]any{"code": "424242", "minutes": 10})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Locale != "zz" || msg.Subject != "ZZ sign-in" || msg.Body != "ZZ code 424242" {
		t.Fatalf("zz render = %+v", msg)
	}
	// An event the new locale did not translate falls back to English, and
	// SAYS it fell back — the delivery log records the locale actually used.
	msg, err = Render(EventCustomerInvite, "zz", map[string]any{"customer_name": "A", "url": "u", "expires": "e"})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Locale != DefaultLocale {
		t.Fatalf("fallback locale = %q, want %q", msg.Locale, DefaultLocale)
	}
	if !strings.HasPrefix(msg.Body, "Hello A,") {
		t.Fatalf("fallback body = %q", msg.Body)
	}
	// An unknown locale falls back the same way.
	if msg, err := Render(EventAuthPIN, "qq", map[string]any{"code": "1", "minutes": 1}); err != nil || msg.Locale != DefaultLocale {
		t.Fatalf("unknown locale: %+v %v", msg, err)
	}
}

func TestRenderRefusesAnUnknownEvent(t *testing.T) {
	if _, err := Render("nope.not.an.event", DefaultLocale, nil); err == nil {
		t.Fatal("an unknown event must not render")
	}
}
