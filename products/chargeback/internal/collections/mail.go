package collections

import (
	"fmt"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The reminder and escalation mails: plain text, one screen, the facts an
// accounts-payable clerk needs — which invoice, how much, when it was due,
// where to see it.

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

// reminderMail renders one reminder stage: before, on, or after the due date.
func reminderMail(inv store.OpenInvoice, stage, days int, link string) (subject, body string) {
	name := invoiceName(inv)
	amount := fmt.Sprintf("%s %s", money(inv.Outstanding), inv.Currency)
	due := inv.DueAt.Format("2 January 2006")
	var when string
	switch {
	case days < 0:
		when = fmt.Sprintf("is due on %s, in %d day%s", due, -days, plural(-days))
		subject = fmt.Sprintf("Reminder: %s for %s is due on %s", strings.ToUpper(name[:1])+name[1:], amount, due)
	case days == 0:
		when = fmt.Sprintf("is due today, %s", due)
		subject = fmt.Sprintf("Due today: %s for %s", strings.ToUpper(name[:1])+name[1:], amount)
	default:
		when = fmt.Sprintf("was due on %s and is %d day%s overdue", due, days, plural(days))
		subject = fmt.Sprintf("Overdue: %s for %s, %d day%s past due", strings.ToUpper(name[:1])+name[1:], amount, days, plural(days))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Hello %s,\n\n", inv.CustomerName)
	fmt.Fprintf(&b, "This is a reminder that %s for %s %s.\n\n", name, amount, when)
	fmt.Fprintf(&b, "Period:      %s to %s\n", inv.PeriodStart, inv.PeriodEnd)
	fmt.Fprintf(&b, "Issued:      %s\n", inv.IssuedAt.Format("2 January 2006"))
	fmt.Fprintf(&b, "Due:         %s\n", due)
	fmt.Fprintf(&b, "Outstanding: %s\n\n", amount)
	fmt.Fprintf(&b, "You can view the invoice here:\n%s\n\n", link)
	b.WriteString("If you have already paid, please disregard this message; payments can take a few days to be recorded.\n")
	_ = stage
	return subject, b.String()
}

// escalationMail renders the escalation notice: what happens now.
func escalationMail(inv store.OpenInvoice, action string, days int, link string) (subject, body string) {
	name := invoiceName(inv)
	amount := fmt.Sprintf("%s %s", money(inv.Outstanding), inv.Currency)
	subject = fmt.Sprintf("Action required: %s for %s is %d days overdue", name, amount, days)
	var b strings.Builder
	fmt.Fprintf(&b, "Hello %s,\n\n", inv.CustomerName)
	fmt.Fprintf(&b, "%s for %s was due on %s and is now %d days overdue.\n\n", strings.ToUpper(name[:1])+name[1:], amount, inv.DueAt.Format("2 January 2006"), days)
	if action == store.EscalationSuspend {
		b.WriteString("Service for your account is being suspended until the outstanding balance is settled. It resumes automatically once payment is recorded.\n\n")
	} else {
		b.WriteString("Please settle the outstanding balance now to avoid a suspension of service.\n\n")
	}
	fmt.Fprintf(&b, "You can view the invoice here:\n%s\n", link)
	return subject, b.String()
}

// LowBalanceMail is the prepaid wallet alert (DESIGN.md §9.5).
func LowBalanceMail(c store.Customer, available store.Decimal, threshold store.Decimal, currency, link string) (subject, body string) {
	subject = fmt.Sprintf("Low balance: %s %s left on your account", money(available), currency)
	var b strings.Builder
	fmt.Fprintf(&b, "Hello %s,\n\n", c.Name)
	fmt.Fprintf(&b, "Your prepaid balance is %s %s, below the %s %s alert threshold.\n\n", money(available), currency, money(threshold), currency)
	if c.SuspendAtZero {
		b.WriteString("When the balance reaches zero, service is suspended until it is topped up.\n\n")
	}
	fmt.Fprintf(&b, "Top up here:\n%s\n", link)
	return subject, b.String()
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
