package collections

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/platform"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Enforcer is the platform suspension hook (DESIGN.md §9.7). Every
// suspension and resumption is executed here, whatever asked for it — the
// collections escalation, a prepaid wallet at zero, an operator, or an
// explicit command imported from the external billing system — and every
// one is recorded on the customer's suspension trail and in the audit log,
// with the platform's answer.
//
// The platform is called for an Organization customer (the seam is
// platform.Client → the sovereign-admin API → the Organization CR). An
// external customer has no Organization to suspend: its status flips here
// and collection stops, which is all the platform could have done for it.
type Enforcer struct {
	Store    *store.Store
	Platform platform.Client
}

func (e *Enforcer) platform() platform.Client {
	if e.Platform == nil {
		return platform.Nop{}
	}
	return e.Platform
}

func orgSlug(c store.Customer) string {
	if c.OrgSlug != nil && strings.TrimSpace(*c.OrgSlug) != "" {
		return *c.OrgSlug
	}
	return c.Slug
}

// Suspend suspends the customer, at the platform when it is an Organization.
// Idempotent: a customer this product already suspended is left as it is.
func (e *Enforcer) Suspend(ctx context.Context, customerID, reason, source, actor string) (store.Suspension, error) {
	c, err := e.Store.GetCustomer(ctx, store.OperatorScope, customerID)
	if err != nil {
		return store.Suspension{}, err
	}
	if c.PlatformSuspendedAt != nil {
		return store.Suspension{CustomerID: c.ID, Action: "suspend", Source: c.SuspensionSource, Reason: c.SuspensionReason, OK: true, At: *c.PlatformSuspendedAt}, nil
	}
	if source == "" {
		source = store.SuspendSourceOperator
	}
	rec := store.Suspension{CustomerID: c.ID, Action: "suspend", Source: source, Reason: reason, Actor: actor, OK: true}
	if c.Kind == "organization" {
		if perr := e.platform().SuspendOrganization(ctx, orgSlug(c), reason); perr != nil {
			// The platform refused or is unreachable: recorded as a failed
			// attempt, the customer's OWN status still flips so collection
			// stops, and the next evaluator pass tries the platform again.
			rec.OK, rec.Error = false, perr.Error()
			slog.Warn("enforcement: platform suspend failed", "customer", c.Slug, "source", source, "error", perr)
		}
	}
	out, err := e.Store.RecordSuspension(ctx, rec)
	if err != nil {
		return store.Suspension{}, err
	}
	if !rec.OK {
		if serr := e.Store.SetCustomerStatus(ctx, c.ID, "suspended"); serr != nil {
			slog.Warn("enforcement: set customer status", "customer", c.Slug, "error", serr)
		}
	}
	_ = e.Store.Audit(ctx, &c.ID, actorOr(actor), "customer.suspend", map[string]any{"source": source, "reason": reason, "platform": e.platform().Name(), "ok": rec.OK, "error": rec.Error, "organization": orgSlug(c), "kind": c.Kind})
	if !rec.OK {
		return out, errors.New(rec.Error)
	}
	return out, nil
}

// Resume lifts a suspension this product asked for. Idempotent.
func (e *Enforcer) Resume(ctx context.Context, customerID, reason, source, actor string) (store.Suspension, error) {
	c, err := e.Store.GetCustomer(ctx, store.OperatorScope, customerID)
	if err != nil {
		return store.Suspension{}, err
	}
	if c.PlatformSuspendedAt == nil && c.Status != "suspended" {
		return store.Suspension{CustomerID: c.ID, Action: "resume", Source: source, OK: true, At: time.Now().UTC()}, nil
	}
	if source == "" {
		source = store.SuspendSourceOperator
	}
	rec := store.Suspension{CustomerID: c.ID, Action: "resume", Source: source, Reason: reason, Actor: actor, OK: true}
	if c.Kind == "organization" && c.PlatformSuspendedAt != nil {
		if perr := e.platform().ResumeOrganization(ctx, orgSlug(c)); perr != nil {
			rec.OK, rec.Error = false, perr.Error()
			slog.Warn("enforcement: platform resume failed", "customer", c.Slug, "source", source, "error", perr)
		}
	}
	out, err := e.Store.RecordSuspension(ctx, rec)
	if err != nil {
		return store.Suspension{}, err
	}
	_ = e.Store.Audit(ctx, &c.ID, actorOr(actor), "customer.resume", map[string]any{"source": source, "reason": reason, "platform": e.platform().Name(), "ok": rec.OK, "error": rec.Error, "organization": orgSlug(c), "kind": c.Kind})
	if !rec.OK {
		return out, errors.New(rec.Error)
	}
	return out, nil
}

// Settle resumes a customer this product suspended for COLLECTIONS once
// nothing overdue is outstanding, or for its WALLET once it holds credit
// again (DESIGN.md §9.7 "resumes it when the balance is settled"). Called
// after every payment, allocation and credit note, and by the evaluator.
// An operator's or an imported suspension is never lifted by a payment:
// whoever asked for it lifts it.
func (e *Enforcer) Settle(ctx context.Context, customerID string, now time.Time) (bool, error) {
	c, err := e.Store.GetCustomer(ctx, store.OperatorScope, customerID)
	if err != nil {
		return false, err
	}
	if c.PlatformSuspendedAt == nil {
		return false, nil
	}
	switch c.SuspensionSource {
	case store.SuspendSourceCollections:
		open, err := e.Store.ListOpenInvoices(ctx, store.CustomerScope(c.ID))
		if err != nil {
			return false, err
		}
		for _, inv := range open {
			if DaysPastDue(inv.DueAt, now) > 0 {
				return false, nil
			}
		}
		_, err = e.Resume(ctx, c.ID, "overdue invoices settled", store.SuspendSourceCollections, "system")
		return err == nil, err
	case store.SuspendSourceWallet:
		bal, err := e.Store.GetAccountBalance(ctx, store.OperatorScope, c.ID)
		if err != nil {
			return false, err
		}
		if store.CompareDecimal(bal.AvailableCredit, "0") <= 0 && store.CompareDecimal(bal.Outstanding, "0") > 0 {
			return false, nil
		}
		_, err = e.Resume(ctx, c.ID, fmt.Sprintf("balance restored: %s available", bal.AvailableCredit), store.SuspendSourceWallet, "system")
		return err == nil, err
	}
	return false, nil
}

func actorOr(actor string) string {
	if strings.TrimSpace(actor) == "" {
		return "system"
	}
	return actor
}
