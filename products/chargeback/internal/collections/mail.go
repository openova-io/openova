package collections

import (
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The PAYLOADS of the three notices this package emits (DESIGN.md §21): the
// reminder stage, the escalation and the prepaid low-balance alert.
//
// Until §21 each of these was a subject string and a body string built right
// here and handed straight to mail.Sender. The prose moved to the ENGLISH
// TEMPLATE CATALOGUE (internal/notify/locale_en.go) character for character;
// what stayed is the part that is genuinely this package's job — reading the
// invoice, naming it, and formatting money and dates the way the rest of the
// product does. The templates render exactly the same bytes, which
// notify.TestEnglishTemplatesRenderTheMessageTheProductAlreadySent and
// collections' own tests both pin.

func money(d store.Decimal) string {
	s := strings.TrimSpace(string(d))
	if s == "" {
		return "0"
	}
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

func invoiceName(inv store.OpenInvoice) string {
	if inv.InvoiceNumber != "" {
		return "invoice " + inv.InvoiceNumber
	}
	return "the statement for " + inv.PeriodStart[:7]
}

// titleCase capitalises the first letter of a phrase for a subject line.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// reminderPayload is what the collections.reminder template renders from.
// `days` is the SIGNED day count — negative before the due date, 0 on it,
// positive after — because that one number is what chooses between the three
// forms the notice takes, and the template makes that choice where a
// translator can see it.
func reminderPayload(inv store.OpenInvoice, stage, days int, link string) map[string]any {
	name := invoiceName(inv)
	return map[string]any{
		"customer_name": inv.CustomerName,
		"invoice_name":  name,
		"invoice_title": titleCase(name),
		"amount":        money(inv.Outstanding) + " " + inv.Currency,
		"due_date":      inv.DueAt.Format("2 January 2006"),
		"issued_date":   inv.IssuedAt.Format("2 January 2006"),
		"period_start":  inv.PeriodStart,
		"period_end":    inv.PeriodEnd,
		"days":          days,
		"stage":         stage,
		"link":          link,
	}
}

// escalationPayload is what the collections.escalation template renders
// from. `suspending` is the escalation ACTION resolved to the one thing the
// notice has to say: is service being suspended, or is it being threatened.
func escalationPayload(inv store.OpenInvoice, action string, days int, link string) map[string]any {
	name := invoiceName(inv)
	return map[string]any{
		"customer_name": inv.CustomerName,
		"invoice_name":  name,
		"invoice_title": titleCase(name),
		"amount":        money(inv.Outstanding) + " " + inv.Currency,
		"due_date":      inv.DueAt.Format("2 January 2006"),
		"days":          days,
		"suspending":    action == store.EscalationSuspend,
		"link":          link,
	}
}

// LowBalancePayload is what the account.low_balance template renders from
// (DESIGN.md §9.5).
func LowBalancePayload(c store.Customer, available, threshold store.Decimal, currency, link string) map[string]any {
	return map[string]any{
		"customer_name":   c.Name,
		"available":       money(available),
		"threshold":       money(threshold),
		"currency":        currency,
		"suspend_at_zero": c.SuspendAtZero,
		"link":            link,
	}
}
