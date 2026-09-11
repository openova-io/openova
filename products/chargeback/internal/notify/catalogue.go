// Package notify is notification management (DESIGN.md §21): the catalogue
// of events the product can emit, the templates that render them, the
// preferences that decide who receives which, the channels that carry them
// and the log of every delivery attempt.
//
// Before this package every message the product sent was composed at its
// call site — a subject string and a body string built by whichever function
// happened to need one — and nothing recorded that it had gone out. There
// was no list of what the product sends, no way for a recipient to choose,
// and no answer to "was the customer told".
//
// The shape is the ordinary one for a BSS:
//
//	EVENT      a stable key and a documented payload  (catalogue.go)
//	TEMPLATE   subject and body per (event, locale)   (template.go, locale_en.go)
//	PREFERENCE which events reach whom, on which channel (store, resolve.go)
//	CHANNEL    how a message is carried               (channel.go)
//	DELIVERY   one row per attempt, with its outcome  (store, notify.go)
//
// Every send in the product goes through Notifier.Send. Nothing composes a
// subject and body of its own any more, which is what makes the catalogue an
// inventory rather than a wish.
package notify

import "sort"

// Event keys. A key is STABLE: it is written into preference rows and into
// the delivery log, and renaming one silently re-enables an event a customer
// switched off. Add events; do not rename them.
const (
	// EventAuthPIN is the one-time sign-in code (internal/api/auth.go).
	EventAuthPIN = "auth.pin"
	// EventCustomerInvite is the activation link mailed to a customer's
	// admin (internal/api/customers.go).
	EventCustomerInvite = "customer.invite"
	// EventStatementIssued is the statement notification — the invoice
	// (internal/api/statements.go).
	EventStatementIssued = "statement.issued"
	// EventBudgetThreshold is a budget threshold crossing
	// (internal/budget/evaluator.go).
	EventBudgetThreshold = "budget.threshold"
	// EventCollectionsReminder is one dunning reminder stage
	// (internal/collections/evaluator.go).
	EventCollectionsReminder = "collections.reminder"
	// EventCollectionsEscalation is the dunning escalation notice
	// (internal/collections/evaluator.go).
	EventCollectionsEscalation = "collections.escalation"
	// EventAccountLowBalance is the prepaid low-balance warning
	// (internal/collections/wallet.go).
	EventAccountLowBalance = "account.low_balance"
	// EventReportScheduled is a scheduled cost report
	// (internal/report/scheduler.go).
	EventReportScheduled = "report.scheduled"
)

// Category groups events for the console. It carries no behaviour.
const (
	CategoryAccess      = "access"
	CategoryBilling     = "billing"
	CategoryCollections = "collections"
	CategoryCost        = "cost"
)

// Field is one value a template may reference, with what it means. It is the
// CONTRACT between the call site that builds a payload and the template that
// renders it: TestTemplatesOnlyReferenceDeclaredFields holds them together,
// so a template that reaches for a field nobody supplies fails the build
// rather than mailing a blank.
type Field struct {
	Name string `json:"name"`
	Desc string `json:"description"`
}

// Event is one notification the product can emit.
type Event struct {
	// Key is the stable identifier, e.g. "collections.reminder".
	Key string `json:"key"`
	// Title and Desc are what an operator reads in the console.
	Title string `json:"title"`
	Desc  string `json:"description"`
	// Category groups the event in the console.
	Category string `json:"category"`
	// Mandatory marks a notice a recipient may NOT switch off: an invoice,
	// a dunning notice, the code that is the only way to sign in. A
	// preference row that disables one is refused when written and ignored
	// when read (resolve.go), and a mandatory event always keeps its
	// Channels even if a preference names only channels that cannot carry
	// it — otherwise "receive it by SMS only" would be a way to switch off
	// an invoice by the back door.
	Mandatory bool `json:"mandatory"`
	// Channels are the channels the event goes out on when no preference
	// says otherwise, in order. For a mandatory event they are also the
	// floor: a preference may ADD a channel and may never remove one.
	Channels []string `json:"channels"`
	// DefaultOn is whether an unset preference receives the event. Every
	// event in this catalogue ships DefaultOn, because every one of them
	// was being sent unconditionally before §21 existed and an unset
	// preference must reproduce exactly what the product did.
	DefaultOn bool `json:"default_on"`
	// Payload documents the fields a template may reference.
	Payload []Field `json:"payload"`
	// Source names the code that emits the event, so the catalogue points
	// at the call site rather than describing it.
	Source string `json:"source"`
}

// Enabled reports whether an unset preference receives the event. A
// mandatory event is received whatever anything says.
func (e Event) Enabled() bool { return e.Mandatory || e.DefaultOn }

// catalogue is every event, in display order. It is the whole inventory of
// what this product can send; a message that is not here cannot be sent,
// because Notifier.Send refuses an unknown key.
var catalogue = []Event{
	{
		Key:       EventAuthPIN,
		Title:     "Sign-in code",
		Desc:      "The one-time code that signs a person in when the Sovereign has no SSO gate in front of this console. It is the only way in, so it cannot be switched off.",
		Category:  CategoryAccess,
		Mandatory: true,
		Channels:  []string{ChannelEmail},
		DefaultOn: true,
		Source:    "internal/api/auth.go — POST /api/v1/auth/pin/request",
		Payload: []Field{
			{Name: "code", Desc: "the six-digit code"},
			{Name: "minutes", Desc: "how many minutes the code is valid for"},
		},
	},
	{
		Key:       EventCustomerInvite,
		Title:     "Account activation",
		Desc:      "The activation link a customer's admin follows to connect its cloud projects. Without it the customer cannot be onboarded at all.",
		Category:  CategoryAccess,
		Mandatory: true,
		Channels:  []string{ChannelEmail},
		DefaultOn: true,
		Source:    "internal/api/customers.go — POST /api/v1/customers/{id}/invite",
		Payload: []Field{
			{Name: "customer_name", Desc: "the customer's name"},
			{Name: "url", Desc: "the activation link"},
			{Name: "expires", Desc: "when the link expires, as 2006-01-02 15:04 UTC"},
		},
	},
	{
		Key:       EventStatementIssued,
		Title:     "Statement issued",
		Desc:      "The invoice: the period, the waterfall, the biggest lines and a link. A customer cannot switch off being told what it owes.",
		Category:  CategoryBilling,
		Mandatory: true,
		Channels:  []string{ChannelEmail},
		DefaultOn: true,
		Source:    "internal/api/statements.go — issue and POST /api/v1/statements/{id}/send",
		Payload: []Field{
			{Name: "subject", Desc: "the statement subject line, rendered by internal/report (§21.3)"},
			{Name: "document", Desc: "the plain-text statement, rendered by internal/report (§21.3)"},
			{Name: "statement_id", Desc: "the statement's id"},
			{Name: "period", Desc: "the period the statement covers, as YYYY-MM"},
		},
	},
	{
		Key:       EventCollectionsReminder,
		Title:     "Payment reminder",
		Desc:      "One dunning stage: before the due date, on it, or after it. A dunning reminder is a notice the operator is obliged to send, so it cannot be switched off.",
		Category:  CategoryCollections,
		Mandatory: true,
		Channels:  []string{ChannelEmail},
		DefaultOn: true,
		Source:    "internal/collections/evaluator.go — the daily pass",
		Payload: []Field{
			{Name: "customer_name", Desc: "the customer's name"},
			{Name: "invoice_name", Desc: "\"invoice INV-000123\", or \"the statement for 2026-08\" when it has no number"},
			{Name: "invoice_title", Desc: "the same phrase with its first letter capitalised, for a subject line"},
			{Name: "amount", Desc: "the outstanding amount with its currency"},
			{Name: "due_date", Desc: "the due date, as 2 January 2006"},
			{Name: "issued_date", Desc: "the issue date, as 2 January 2006"},
			{Name: "period_start", Desc: "the first day of the period, as YYYY-MM-DD"},
			{Name: "period_end", Desc: "the last day of the period, as YYYY-MM-DD"},
			{Name: "days", Desc: "days past due: negative before the due date, 0 on it, positive after"},
			{Name: "stage", Desc: "the reminder stage this send belongs to, as configured days from due"},
			{Name: "link", Desc: "the link to the invoice"},
		},
	},
	{
		Key:       EventCollectionsEscalation,
		Title:     "Overdue escalation",
		Desc:      "The notice that service is about to be suspended, or is being suspended, for an unpaid invoice. Mandatory: a suspension a customer was never warned about is the defect.",
		Category:  CategoryCollections,
		Mandatory: true,
		Channels:  []string{ChannelEmail},
		DefaultOn: true,
		Source:    "internal/collections/evaluator.go — the daily pass",
		Payload: []Field{
			{Name: "customer_name", Desc: "the customer's name"},
			{Name: "invoice_name", Desc: "\"invoice INV-000123\", or \"the statement for 2026-08\" when it has no number"},
			{Name: "invoice_title", Desc: "the same phrase with its first letter capitalised"},
			{Name: "amount", Desc: "the outstanding amount with its currency"},
			{Name: "due_date", Desc: "the due date, as 2 January 2006"},
			{Name: "days", Desc: "days past due"},
			{Name: "suspending", Desc: "true when the escalation action is to suspend"},
			{Name: "link", Desc: "the link to the invoice"},
		},
	},
	{
		Key:       EventAccountLowBalance,
		Title:     "Low balance",
		Desc:      "A prepaid account has fallen below its alert threshold. Sent once per crossing and again only after the balance recovers.",
		Category:  CategoryCollections,
		Mandatory: false,
		Channels:  []string{ChannelEmail},
		DefaultOn: true,
		Source:    "internal/collections/wallet.go — after any account change",
		Payload: []Field{
			{Name: "customer_name", Desc: "the customer's name"},
			{Name: "available", Desc: "the available credit, unformatted"},
			{Name: "threshold", Desc: "the alert threshold, unformatted"},
			{Name: "currency", Desc: "the account currency"},
			{Name: "suspend_at_zero", Desc: "true when service is suspended once the balance reaches zero"},
			{Name: "link", Desc: "the link to top up"},
		},
	},
	{
		Key:       EventBudgetThreshold,
		Title:     "Budget threshold",
		Desc:      "A budget has crossed one of its thresholds for the period. Recorded once per (budget, period, threshold), so a restart cannot re-send it.",
		Category:  CategoryCost,
		Mandatory: false,
		Channels:  []string{ChannelEmail},
		DefaultOn: true,
		Source:    "internal/budget/evaluator.go — the hourly pass",
		Payload: []Field{
			{Name: "budget_name", Desc: "the budget's name"},
			{Name: "scope", Desc: "the customer the budget covers, or \"all customers\""},
			{Name: "threshold", Desc: "the threshold crossed, as a whole percentage"},
			{Name: "amount", Desc: "the budget cap, trimmed of trailing zeros"},
			{Name: "currency", Desc: "the budget currency"},
			{Name: "period", Desc: "the period, as YYYY-MM"},
			{Name: "actual", Desc: "spend so far, trimmed of trailing zeros"},
			{Name: "pct_actual", Desc: "spend so far as a percentage of the cap, to one decimal"},
			{Name: "forecast", Desc: "the month-end forecast to two decimals, EMPTY when none could be made"},
			{Name: "pct_forecast", Desc: "the forecast as a percentage of the cap to one decimal, EMPTY when there is none"},
			{Name: "status", Desc: "the budget's status word"},
		},
	},
	{
		Key:       EventReportScheduled,
		Title:     "Scheduled cost report",
		Desc:      "A saved cost report for the window its cadence implies, mailed to the recipients the schedule names.",
		Category:  CategoryCost,
		Mandatory: false,
		Channels:  []string{ChannelEmail},
		DefaultOn: true,
		Source:    "internal/report/scheduler.go — the five-minute poll",
		Payload: []Field{
			{Name: "subject", Desc: "the report subject line, rendered by internal/report (§21.3)"},
			{Name: "document", Desc: "the plain-text report, rendered by internal/report (§21.3)"},
			{Name: "schedule_id", Desc: "the schedule's id"},
			{Name: "schedule_name", Desc: "the schedule's name"},
			{Name: "cadence", Desc: "daily, weekly or monthly"},
		},
	},
}

// byKey indexes the catalogue once.
var byKey = func() map[string]Event {
	m := make(map[string]Event, len(catalogue))
	for _, e := range catalogue {
		m[e.Key] = e
	}
	return m
}()

// Events returns the whole catalogue, in display order. The slice is a copy:
// the catalogue is not something a caller gets to edit.
func Events() []Event {
	out := make([]Event, len(catalogue))
	copy(out, catalogue)
	return out
}

// Lookup returns the event with that key.
func Lookup(key string) (Event, bool) {
	e, ok := byKey[key]
	return e, ok
}

// Keys lists every event key, sorted — what a test iterates.
func Keys() []string {
	out := make([]string, 0, len(catalogue))
	for _, e := range catalogue {
		out = append(out, e.Key)
	}
	sort.Strings(out)
	return out
}
