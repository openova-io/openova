package budget

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/mail"
	"github.com/openova-io/openova/products/chargeback/internal/notify"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Store is what the evaluator needs from the database.
type Store interface {
	Reader
	ListActiveBudgets(ctx context.Context) ([]store.Budget, error)
	RecordBudgetAlert(ctx context.Context, budgetID, period string, threshold int, actual store.Decimal) (bool, error)
	Audit(ctx context.Context, customerID *string, actor, action string, details any) error
}

// Evaluator walks every active budget on a schedule, records each threshold
// crossing once per period, writes an audit entry and mails the budget's
// recipients (DESIGN.md §3.5).
//
// Idempotence rests on the store, not on memory: RecordBudgetAlert inserts
// with ON CONFLICT DO NOTHING on (budget, period, threshold) and reports
// whether THIS call inserted. Only an insert mails and audits, so a restart,
// a second replica or an overlapping tick can never send a crossing twice.
type Evaluator struct {
	Store Store
	Mail  mail.Sender
	// Notify routes the crossing alert through the notification catalogue
	// (DESIGN.md §21); nil builds one over Mail.
	Notify *notify.Notifier
	// Now defaults to time.Now.
	Now func() time.Time
	// Interval between evaluations; default one hour.
	Interval time.Duration
	// InitialDelay before the first evaluation after start; default one
	// minute, so a fresh process does not compete with startup work.
	InitialDelay time.Duration
}

// Report counts what one evaluation did.
type Report struct {
	Budgets   int `json:"budgets"`
	Crossings int `json:"crossings"` // thresholds newly recorded
	Mails     int `json:"mails"`
	Errors    int `json:"errors"`
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
		delay = time.Minute
	}
	interval := e.Interval
	if interval <= 0 {
		interval = time.Hour
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
		slog.Warn("budget evaluator", "error", err)
		return
	}
	slog.Info("budget evaluator", "budgets", rep.Budgets, "crossings", rep.Crossings, "mails", rep.Mails, "errors", rep.Errors)
}

// RunOnce evaluates every active budget for the current month. A failure on
// one budget is counted and logged; the others are still evaluated. The
// returned error is only for the listing itself.
func (e *Evaluator) RunOnce(ctx context.Context) (Report, error) {
	var rep Report
	now := e.now()
	budgets, err := e.Store.ListActiveBudgets(ctx)
	if err != nil {
		return rep, err
	}
	for _, b := range budgets {
		rep.Budgets++
		st, err := StatusFor(ctx, e.Store, store.OperatorScope, b, now, now)
		if err != nil {
			slog.Warn("budget status", "budget_id", b.ID, "error", err)
			rep.Errors++
			continue
		}
		for _, th := range st.Thresholds {
			if !th.Crossed || th.AlertedAt != nil {
				continue
			}
			inserted, err := e.Store.RecordBudgetAlert(ctx, b.ID, st.Period, th.Pct, st.Actual)
			if err != nil {
				slog.Warn("record budget alert", "budget_id", b.ID, "threshold", th.Pct, "error", err)
				rep.Errors++
				continue
			}
			if !inserted {
				// Recorded by a concurrent evaluation between our read and
				// this insert: that one owns the mail.
				continue
			}
			rep.Crossings++
			details := map[string]any{
				"budget_id": b.ID, "threshold": th.Pct, "actual": string(st.Actual),
				"amount": string(b.Amount), "period": st.Period,
			}
			if err := e.Store.Audit(ctx, b.CustomerID, "system", "budget.threshold", details); err != nil {
				slog.Warn("audit budget threshold", "budget_id", b.ID, "error", err)
				rep.Errors++
			}
			payload := crossingPayload(b, st, th.Pct)
			n := e.notifier()
			for _, to := range b.NotifyEmails {
				res, err := n.Send(ctx, notify.Request{Event: notify.EventBudgetThreshold, To: to, CustomerID: b.CustomerID, Payload: payload})
				if err != nil {
					slog.Warn("send budget mail", "budget_id", b.ID, "to", to, "error", err)
					rep.Errors++
					continue
				}
				if !res.Sent {
					continue
				}
				rep.Mails++
			}
		}
	}
	return rep, nil
}

// notifier is the notification path: the one wired in, or one built over
// Mail for a caller that wired only a sender.
func (e *Evaluator) notifier() *notify.Notifier {
	if e.Notify != nil {
		return e.Notify
	}
	return &notify.Notifier{Channels: notify.DefaultChannels(e.Mail)}
}

// crossingPayload is what the budget.threshold template renders from
// (DESIGN.md §21). The prose that used to be here is now the English
// template; what stayed is the reading of the budget — which customer it
// covers, and the money formatted the way the rest of the product formats
// it. `forecast` and `pct_forecast` are EMPTY STRINGS when there is no
// forecast, which is what the template's {{if}} turns on: a float of 0.00
// is a forecast and must print, and only a distinct empty value can say
// "there is none".
func crossingPayload(b store.Budget, st Status, pct int) map[string]any {
	scope := "all customers"
	if b.CustomerName != nil && *b.CustomerName != "" {
		scope = *b.CustomerName
	} else if b.CustomerID != nil {
		scope = "customer " + *b.CustomerID
	}
	forecast, pctForecast := "", ""
	if st.Forecast != nil {
		forecast = fmt.Sprintf("%.2f", *st.Forecast)
		if st.PctForecast != nil {
			pctForecast = fmt.Sprintf("%.1f", *st.PctForecast)
		}
	}
	return map[string]any{
		"budget_name":  b.Name,
		"scope":        scope,
		"threshold":    pct,
		"amount":       trimDec(b.Amount),
		"currency":     b.Currency,
		"period":       st.Period,
		"actual":       trimDec(st.Actual),
		"pct_actual":   fmt.Sprintf("%.1f", st.PctActual),
		"forecast":     forecast,
		"pct_forecast": pctForecast,
		"status":       st.Status,
	}
}

// trimDec drops trailing zeros from a 6-decimal money string for prose:
// 200.000000 → 200, 12.500000 → 12.5.
func trimDec(d store.Decimal) string {
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
