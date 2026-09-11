package collections

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/mail"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Evaluator walks every open invoice once a day (the budget evaluator's
// scheduling pattern): each reminder stage whose day has arrived is sent
// once and recorded; the escalation fires once at the configured overdue
// age and either notifies or suspends; a suspension this product holds is
// lifted once the customer is settled. Idempotence rests on the store —
// RecordReminder inserts ON CONFLICT DO NOTHING on (statement, kind, stage)
// and reports whether THIS call inserted — so a restart, a second replica
// or an overlapping tick can never send a stage twice.
//
// It runs ONLY when this product owns collections (internal mode). With an
// external billing system every reminder and every escalation is theirs;
// we suspend on their explicit command alone.
type Evaluator struct {
	Store    *store.Store
	Mail     mail.Sender
	Enforcer *Enforcer
	// PublicURL is where the reminder links the invoice.
	PublicURL string
	// Owns reports whether this product runs collections; nil = always.
	Owns func(ctx context.Context) (bool, error)
	// Now defaults to time.Now.
	Now func() time.Time
	// Interval between evaluations; default 24 hours.
	Interval time.Duration
	// InitialDelay before the first evaluation after start; default two
	// minutes, so a fresh process does not compete with startup work.
	InitialDelay time.Duration
}

// Report counts what one evaluation did.
type Report struct {
	Invoices int `json:"invoices"`
	// Disputed counts the open invoices this pass PASSED OVER because the
	// customer disputes them (DESIGN.md §16). They are still owed; they are
	// simply not chased until an operator resolves the dispute, and saying
	// so here is what tells an operator why a reminder did not go out.
	Disputed    int    `json:"disputed"`
	Reminders   int    `json:"reminders"`
	Escalations int    `json:"escalations"`
	Suspended   int    `json:"suspended"`
	Resumed     int    `json:"resumed"`
	Mails       int    `json:"mails"`
	Errors      int    `json:"errors"`
	Skipped     bool   `json:"skipped"`
	SkipReason  string `json:"skip_reason,omitempty"`
}

func (e *Evaluator) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

// Run blocks until ctx is done: one evaluation after InitialDelay, then one
// per Interval.
func (e *Evaluator) Run(ctx context.Context) {
	delay := e.InitialDelay
	if delay <= 0 {
		delay = 2 * time.Minute
	}
	interval := e.Interval
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(delay):
	}
	e.tick(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.tick(ctx)
		}
	}
}

func (e *Evaluator) tick(ctx context.Context) {
	rep, err := e.RunOnce(ctx)
	if err != nil {
		slog.Warn("collections evaluator", "error", err)
		return
	}
	slog.Info("collections evaluator", "invoices", rep.Invoices, "disputed", rep.Disputed, "reminders", rep.Reminders, "escalations", rep.Escalations, "suspended", rep.Suspended, "resumed", rep.Resumed, "mails", rep.Mails, "errors", rep.Errors, "skipped", rep.Skipped)
}

// RunOnce evaluates every open invoice at the current instant.
func (e *Evaluator) RunOnce(ctx context.Context) (Report, error) {
	return e.RunAt(ctx, e.now())
}

// RunAt evaluates at an explicit instant — what a test drives day by day.
func (e *Evaluator) RunAt(ctx context.Context, now time.Time) (Report, error) {
	var rep Report
	if e.Owns != nil {
		owns, err := e.Owns(ctx)
		if err != nil {
			return rep, err
		}
		if !owns {
			rep.Skipped, rep.SkipReason = true, "collections are owned by the external billing system"
			return rep, nil
		}
	}
	settings, err := e.Store.GetBillingSettings(ctx)
	if err != nil {
		return rep, err
	}
	open, err := e.Store.ListOpenInvoices(ctx, store.OperatorScope)
	if err != nil {
		return rep, err
	}
	touched := map[string]bool{}
	for _, inv := range open {
		rep.Invoices++
		touched[inv.CustomerID] = true
		// DESIGN.md §16 — an invoice the customer disputes is not chased:
		// no reminder stage, no escalation and no suspension flow from it
		// while the dispute is open. The money stays outstanding and on the
		// balance; resolving the dispute (either way) clears the flag and
		// the schedule picks up from the day it then is.
		if inv.DisputedAt != nil {
			rep.Disputed++
			continue
		}
		days := DaysPastDue(inv.DueAt, now)
		// Reminders: every stage whose day has arrived and was not sent.
		for _, stage := range settings.ReminderDays {
			if days < stage {
				continue
			}
			inserted, err := e.Store.RecordReminder(ctx, inv.StatementID, "reminder", stage, nil)
			if err != nil {
				rep.Errors++
				continue
			}
			if !inserted {
				continue
			}
			rep.Reminders++
			subject, body := reminderMail(inv, stage, days, e.link(inv))
			sent := e.send(ctx, inv, subject, body)
			rep.Mails += len(sent)
			_ = e.Store.Audit(ctx, &inv.CustomerID, "system", "collections.reminder", map[string]any{"statement_id": inv.StatementID, "invoice_number": inv.InvoiceNumber, "stage": stage, "days_past_due": days, "outstanding": inv.Outstanding, "recipients": sent})
		}
		// Escalation: once, at the configured overdue age.
		if settings.EscalationDays > 0 && days >= settings.EscalationDays {
			inserted, err := e.Store.RecordReminder(ctx, inv.StatementID, "escalation", settings.EscalationDays, nil)
			if err != nil {
				rep.Errors++
				continue
			}
			if inserted {
				rep.Escalations++
				subject, body := escalationMail(inv, settings.EscalationAction, days, e.link(inv))
				sent := e.send(ctx, inv, subject, body)
				rep.Mails += len(sent)
				_ = e.Store.Audit(ctx, &inv.CustomerID, "system", "collections.escalation", map[string]any{"statement_id": inv.StatementID, "invoice_number": inv.InvoiceNumber, "action": settings.EscalationAction, "days_past_due": days, "outstanding": inv.Outstanding, "recipients": sent})
				if settings.EscalationAction == store.EscalationSuspend && e.Enforcer != nil {
					reason := fmt.Sprintf("invoice %s is %d days overdue (%s %s outstanding)", inv.InvoiceNumber, days, inv.Outstanding, inv.Currency)
					if _, err := e.Enforcer.Suspend(ctx, inv.CustomerID, reason, store.SuspendSourceCollections, "system"); err != nil {
						rep.Errors++
					} else {
						rep.Suspended++
					}
				}
			}
		}
	}
	// Resumption: every customer this product suspended is re-checked, not
	// only the ones with an open invoice — a customer with none left is
	// exactly the one to resume.
	if e.Enforcer != nil {
		suspended, err := e.Store.ListCustomers(ctx, store.OperatorScope)
		if err != nil {
			return rep, err
		}
		for _, c := range suspended {
			if c.PlatformSuspendedAt == nil {
				continue
			}
			resumed, err := e.Enforcer.Settle(ctx, c.ID, now)
			if err != nil {
				rep.Errors++
				continue
			}
			if resumed {
				rep.Resumed++
			}
		}
	}
	return rep, nil
}

func (e *Evaluator) link(inv store.OpenInvoice) string {
	return strings.TrimRight(e.PublicURL, "/") + "/statements/" + inv.StatementID
}

// send mails the customer's admin and every admin user; it reports who was
// reached and records the recipients on the reminder row.
func (e *Evaluator) send(ctx context.Context, inv store.OpenInvoice, subject, body string) []string {
	recipients := e.recipients(ctx, inv)
	sent := []string{}
	for _, to := range recipients {
		if e.Mail == nil {
			break
		}
		if err := e.Mail.Send(ctx, to, subject, body); err != nil {
			slog.Warn("collections: send reminder", "statement", inv.StatementID, "to", to, "error", err)
			continue
		}
		sent = append(sent, to)
	}
	return sent
}

func (e *Evaluator) recipients(ctx context.Context, inv store.OpenInvoice) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || !strings.Contains(s, "@") || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	add(inv.AdminEmail)
	if users, err := e.Store.ListCustomerUsers(ctx, inv.CustomerID); err == nil {
		for _, u := range users {
			if u.Role == "admin" {
				add(u.Email)
			}
		}
	}
	return out
}
