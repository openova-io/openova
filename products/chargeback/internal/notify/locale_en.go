package notify

// The ENGLISH catalogue (DESIGN.md §21.3). One entry per event; every one of
// them renders, character for character, the message that event's call site
// composed by hand before §21 existed. That is not a nicety: a refactor that
// quietly reworded a dunning reminder or dropped a line from an invoice
// notification would be a defect nobody would notice until a customer did.
// TestEnglishTemplatesRenderTheMessageTheProductAlreadySent pins each one
// against the exact bytes.
//
// A SECOND LOCALE IS THIS FILE AGAIN, under another name, with another tag —
// locale_ar.go calling RegisterLocale("ar", …). Nothing else changes: not the
// catalogue, not the notifier, not a call site, not the schema. No Arabic is
// written here; none has been provided, and inventing a translation of a
// legal dunning notice would be worse than having none.
func init() {
	RegisterLocale(DefaultLocale, map[string]Template{

		EventAuthPIN: {
			Subject: `Your sign-in code`,
			Body:    `Your chargeback sign-in code is {{.code}}. It expires in {{.minutes}} minutes.`,
		},

		EventCustomerInvite: {
			Subject: `Activate your chargeback account`,
			Body: `Hello {{.customer_name}},

Activate your chargeback account and connect your cloud projects:

{{.url}}

The link expires on {{.expires}}.
`,
		},

		// The statement and the report are RENDERED DOCUMENTS, not prose: a
		// money waterfall, a ranked line table and a column-aligned summary
		// come out of internal/report, which is where the arithmetic and the
		// currency's minor unit are. The template carries them whole. §21.3
		// says plainly what that costs a second locale.
		EventStatementIssued: {
			Subject: `{{.subject}}`,
			Body:    `{{.document}}`,
		},

		EventReportScheduled: {
			Subject: `{{.subject}}`,
			Body:    `{{.document}}`,
		},

		EventCollectionsReminder: {
			Subject: `{{if lt .days 0}}Reminder: {{.invoice_title}} for {{.amount}} is due on {{.due_date}}{{else if eq .days 0}}Due today: {{.invoice_title}} for {{.amount}}{{else}}Overdue: {{.invoice_title}} for {{.amount}}, {{.days}} day{{plural .days}} past due{{end}}`,
			Body: `Hello {{.customer_name}},

This is a reminder that {{.invoice_name}} for {{.amount}} {{if lt .days 0}}is due on {{.due_date}}, in {{abs .days}} day{{plural (abs .days)}}{{else if eq .days 0}}is due today, {{.due_date}}{{else}}was due on {{.due_date}} and is {{.days}} day{{plural .days}} overdue{{end}}.

Period:      {{.period_start}} to {{.period_end}}
Issued:      {{.issued_date}}
Due:         {{.due_date}}
Outstanding: {{.amount}}

You can view the invoice here:
{{.link}}

If you have already paid, please disregard this message; payments can take a few days to be recorded.
`,
		},

		EventCollectionsEscalation: {
			Subject: `Action required: {{.invoice_name}} for {{.amount}} is {{.days}} days overdue`,
			Body: `Hello {{.customer_name}},

{{.invoice_title}} for {{.amount}} was due on {{.due_date}} and is now {{.days}} days overdue.

{{if .suspending}}Service for your account is being suspended until the outstanding balance is settled. It resumes automatically once payment is recorded.
{{else}}Please settle the outstanding balance now to avoid a suspension of service.
{{end}}
You can view the invoice here:
{{.link}}
`,
		},

		EventAccountLowBalance: {
			Subject: `Low balance: {{.available}} {{.currency}} left on your account`,
			Body: `Hello {{.customer_name}},

Your prepaid balance is {{.available}} {{.currency}}, below the {{.threshold}} {{.currency}} alert threshold.

{{if .suspend_at_zero}}When the balance reaches zero, service is suspended until it is topped up.

{{end}}Top up here:
{{.link}}
`,
		},

		EventBudgetThreshold: {
			Subject: `Budget {{.budget_name}}: {{.threshold}}% of {{.amount}} {{.currency}} reached for {{.period}}`,
			Body: `Budget {{printf "%q" .budget_name}} ({{.scope}}) has reached {{.threshold}}% of its {{.amount}} {{.currency}} cap for {{.period}}.

Actual so far: {{.actual}} {{.currency}} ({{.pct_actual}}% of the budget)
{{if .forecast}}Month-end forecast: {{.forecast}} {{.currency}}{{if .pct_forecast}} ({{.pct_forecast}}% of the budget){{end}}
{{end}}Status: {{.status}}
`,
		},
	})
}
