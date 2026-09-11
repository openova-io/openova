package collections

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

// Wallet is what payment_model = prepaid adds on top of universal account
// credit (DESIGN.md §9.5): service depends on the balance. After an issue
// or any change to the account it checks two things for a prepaid
// customer — the low-balance alert, emitted once per crossing as the
// account.low_balance event (DESIGN.md §21), and suspend-at-zero, executed
// through the Enforcer — and lifts a wallet suspension once the balance is
// back. A postpaid customer is never touched here: its service depends on
// terms, not on the balance.
type Wallet struct {
	Store *store.Store
	Mail  mail.Sender
	// Notify routes the low-balance alert through the notification
	// catalogue; nil builds one over Mail.
	Notify    *notify.Notifier
	Enforcer  *Enforcer
	PublicURL string
	// Owns reports whether this product owns the account; nil = always.
	Owns func(ctx context.Context) (bool, error)
}

// Outcome is what one check did.
type Outcome struct {
	Alerted   bool `json:"alerted"`
	Suspended bool `json:"suspended"`
	Resumed   bool `json:"resumed"`
}

// Check evaluates one customer's wallet at `now`.
func (w *Wallet) Check(ctx context.Context, customerID string, now time.Time) (Outcome, error) {
	var out Outcome
	if w.Owns != nil {
		owns, err := w.Owns(ctx)
		if err != nil || !owns {
			return out, err
		}
	}
	c, err := w.Store.GetCustomer(ctx, store.OperatorScope, customerID)
	if err != nil {
		return out, err
	}
	if !c.IsBilled() || c.PaymentModel != store.PaymentModelPrepaid {
		return out, nil
	}
	bal, err := w.Store.GetAccountBalance(ctx, store.OperatorScope, c.ID)
	if err != nil {
		return out, err
	}
	currency, _ := w.Store.CustomerCurrency(ctx, c.ID)
	available := bal.AvailableCredit
	// The low-balance alert: once per crossing, cleared when it recovers.
	if c.LowBalanceThreshold != nil && strings.TrimSpace(string(*c.LowBalanceThreshold)) != "" {
		below := store.CompareDecimal(available, *c.LowBalanceThreshold) < 0
		alerted, err := w.lowBalanceAlerted(ctx, c.ID)
		if err != nil {
			return out, err
		}
		switch {
		case below && !alerted:
			if err := w.Store.SetLowBalanceAlerted(ctx, c.ID, &now); err != nil {
				return out, err
			}
			payload := LowBalancePayload(c, available, *c.LowBalanceThreshold, currency, strings.TrimRight(w.PublicURL, "/")+"/my/statements")
			sent := []string{}
			n := w.notifier()
			for _, to := range w.recipients(ctx, c) {
				res, err := n.Send(ctx, notify.Request{Event: notify.EventAccountLowBalance, To: to, CustomerID: &c.ID, Payload: payload})
				if err != nil {
					slog.Warn("wallet: low-balance mail", "customer", c.Slug, "to", to, "error", err)
					continue
				}
				if !res.Sent {
					continue
				}
				sent = append(sent, to)
			}
			_ = w.Store.Audit(ctx, &c.ID, "system", "account.low_balance", map[string]any{"available_credit": available, "threshold": *c.LowBalanceThreshold, "currency": currency, "recipients": sent})
			out.Alerted = true
		case !below && alerted:
			if err := w.Store.SetLowBalanceAlerted(ctx, c.ID, nil); err != nil {
				return out, err
			}
		}
	}
	// Suspend at zero: the balance is gone AND an invoice is left owing.
	zero := store.CompareDecimal(available, "0") <= 0 && store.CompareDecimal(bal.Outstanding, "0") > 0
	if c.SuspendAtZero && zero && c.PlatformSuspendedAt == nil && w.Enforcer != nil {
		reason := fmt.Sprintf("prepaid balance exhausted: %s %s outstanding", bal.Outstanding, currency)
		if _, err := w.Enforcer.Suspend(ctx, c.ID, reason, store.SuspendSourceWallet, "system"); err != nil {
			return out, err
		}
		out.Suspended = true
	}
	if !zero && c.PlatformSuspendedAt != nil && c.SuspensionSource == store.SuspendSourceWallet && w.Enforcer != nil {
		resumed, err := w.Enforcer.Settle(ctx, c.ID, now)
		if err != nil {
			return out, err
		}
		out.Resumed = resumed
	}
	return out, nil
}

// notifier is the notification path: the one wired in, or one built over
// Mail for a caller that wired only a sender.
func (w *Wallet) notifier() *notify.Notifier {
	if w.Notify != nil {
		return w.Notify
	}
	return &notify.Notifier{Channels: notify.DefaultChannels(w.Mail)}
}

func (w *Wallet) lowBalanceAlerted(ctx context.Context, customerID string) (bool, error) {
	var at *time.Time
	err := w.Store.DB().QueryRowContext(ctx, `SELECT low_balance_alerted_at FROM customers WHERE id = $1`, customerID).Scan(&at)
	return at != nil, err
}

func (w *Wallet) recipients(ctx context.Context, c store.Customer) []string {
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
	add(c.AdminEmail)
	if users, err := w.Store.ListCustomerUsers(ctx, c.ID); err == nil {
		for _, u := range users {
			if u.Role == "admin" {
				add(u.Email)
			}
		}
	}
	return out
}
