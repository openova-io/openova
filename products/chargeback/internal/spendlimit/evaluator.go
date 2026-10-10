// Package spendlimit enforces a grow Source's monthly spend limit while the
// period runs (DESIGN.md §22.11, founder direction 2026-10-10): the limit
// STOPS the usage above the package, it does not forgive it on the bill.
//
// Every hour the evaluator rates the current period so far for each customer
// with a spend-limited grow Source — through rating.AccruedOverage, the same
// pipeline the statement run uses — and when a Source's usage above its
// package has reached its limit it:
//
//  1. records the stop once per (Source, period) in spend_stops (an insert
//     that a second replica or an overlapping tick cannot repeat);
//  2. holds the Organization at its package headline until the next period
//     start — platform.OverageClient.StopOverage, which stamps
//     spec.overageStoppedUntil on the Organization CR through the
//     sovereign-admin API's ServiceAccount door; the org-controller renders
//     the ResourceQuota and the application storage quota as capped until
//     then. A failed call is recorded and retried every tick;
//  3. tells the customer: the overage.spend_limit_reached notice to the
//     customer's admin, and an audit entry. The Source document carries
//     overage_stopped_until for the console.
//
// At the next period start the stop is lifted (ResumeOverage, cleared_at):
// the controller already returns to grow on its own at that instant, so the
// resume only tidies the CR. A stop is lifted early when the customer
// raises the limit above what has accrued, or leaves grow mode.
package spendlimit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/notify"
	"github.com/openova-io/openova/products/chargeback/internal/platform"
	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Evaluator walks the spend-limited grow Sources on a schedule.
type Evaluator struct {
	Store    *store.Store
	Platform platform.OverageClient
	// Notify sends the notice; nil sends none (the stop is still enforced
	// and audited).
	Notify *notify.Notifier
	// Accrued rates the period so far; nil is rating.AccruedOverage.
	Accrued func(ctx context.Context, st *store.Store, c store.Customer, from, now time.Time) (map[string]*big.Rat, error)
	// Now defaults to time.Now.
	Now func() time.Time
	// Interval between evaluations; default one hour, the collector's grain.
	Interval time.Duration
	// InitialDelay before the first evaluation; default two minutes.
	InitialDelay time.Duration
}

// Report counts what one evaluation did.
type Report struct {
	Sources int `json:"sources"`
	Stopped int `json:"stopped"`
	Applied int `json:"applied"`
	Lifted  int `json:"lifted"`
	Errors  int `json:"errors"`
}

func (e *Evaluator) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

// Run blocks until ctx is done.
func (e *Evaluator) Run(ctx context.Context) {
	delay, interval := e.InitialDelay, e.Interval
	if delay <= 0 {
		delay = 2 * time.Minute
	}
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
		slog.Warn("spend-limit evaluator", "error", err)
		return
	}
	if rep.Stopped+rep.Applied+rep.Lifted+rep.Errors > 0 {
		slog.Info("spend-limit evaluator", "sources", rep.Sources, "stopped", rep.Stopped, "applied", rep.Applied, "lifted", rep.Lifted, "errors", rep.Errors)
	}
}

// PeriodOf is the billing period containing t: [first of the month, first
// of the next).
func PeriodOf(t time.Time) (time.Time, time.Time) {
	t = t.UTC()
	from := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	return from, from.AddDate(0, 1, 0)
}

// RunOnce evaluates the current period once.
func (e *Evaluator) RunOnce(ctx context.Context) (Report, error) {
	var rep Report
	now := e.now()
	from, until := PeriodOf(now)
	sources, err := e.Store.SpendLimitedSources(ctx)
	if err != nil {
		return rep, err
	}
	limited := map[string]store.CostSource{}
	byCustomer := map[string][]store.CostSource{}
	var order []string
	for _, s := range sources {
		limited[s.ID] = s
		if _, ok := byCustomer[s.CustomerID]; !ok {
			order = append(order, s.CustomerID)
		}
		byCustomer[s.CustomerID] = append(byCustomer[s.CustomerID], s)
	}
	accruedFn := e.Accrued
	if accruedFn == nil {
		accruedFn = rating.AccruedOverage
	}
	accruedOf := map[string]*big.Rat{}
	for _, cid := range order {
		c, err := e.Store.GetCustomer(ctx, store.OperatorScope, cid)
		if err != nil {
			rep.Errors++
			slog.Warn("spend-limit evaluator: customer", "customer", cid, "error", err)
			continue
		}
		acc, err := accruedFn(ctx, e.Store, c, from, now)
		if err != nil {
			if !errors.Is(err, rating.ErrNoPriceBook) {
				rep.Errors++
				slog.Warn("spend-limit evaluator: rate the period so far", "customer", c.Slug, "error", err)
			}
			continue
		}
		for _, src := range byCustomer[cid] {
			rep.Sources++
			used := acc[src.ID]
			if used == nil {
				continue
			}
			accruedOf[src.ID] = used
			limit, ok := new(big.Rat).SetString(string(*src.SpendLimitMonth))
			if !ok || used.Cmp(limit) < 0 {
				continue
			}
			inserted, err := e.Store.RecordSpendStop(ctx, store.SpendStop{SourceID: src.ID, PeriodStart: from, StoppedAt: now, Until: until,
				Accrued: store.Decimal(used.FloatString(6)), SpendLimit: *src.SpendLimitMonth})
			if err != nil {
				rep.Errors++
				slog.Warn("spend-limit evaluator: record the stop", "source", src.ID, "error", err)
				continue
			}
			if !inserted {
				continue
			}
			rep.Stopped++
			details := map[string]any{"source_id": src.ID, "period": from.Format("2006-01"), "accrued": used.FloatString(6), "spend_limit": string(*src.SpendLimitMonth), "until": until.Format(time.RFC3339)}
			if err := e.Store.Audit(ctx, &c.ID, "system", "overage.spend_limit_reached", details); err != nil {
				rep.Errors++
			}
			e.notice(ctx, c, src, used, from, until)
			if err := e.Store.MarkSpendStop(ctx, src.ID, from, "notified_at", now, ""); err != nil {
				rep.Errors++
			}
		}
	}
	// Apply the open stops of this period, lift the past ones and those the
	// customer raised the limit above (or left grow mode for).
	stops, err := e.Store.OpenSpendStops(ctx)
	if err != nil {
		return rep, err
	}
	for _, ss := range stops {
		slug, err := e.orgSlug(ctx, ss.SourceID)
		if err != nil {
			rep.Errors++
			continue
		}
		lift := !ss.Until.After(now)
		if !lift {
			src, still := limited[ss.SourceID]
			switch {
			case !still:
				lift = true // capped now, or no limit any more
			case accruedOf[ss.SourceID] != nil:
				limit, ok := new(big.Rat).SetString(string(*src.SpendLimitMonth))
				lift = ok && accruedOf[ss.SourceID].Cmp(limit) < 0
			}
		}
		if lift {
			if err := e.Platform.ResumeOverage(ctx, slug); err != nil {
				rep.Errors++
				_ = e.Store.SpendStopError(ctx, ss.SourceID, ss.PeriodStart, err.Error())
				continue
			}
			if err := e.Store.MarkSpendStop(ctx, ss.SourceID, ss.PeriodStart, "cleared_at", now, ""); err != nil {
				rep.Errors++
				continue
			}
			if ss.Until.After(now) {
				// Lifted early: the period may stop again at the new limit.
				if err := e.Store.DeleteSpendStop(ctx, ss.SourceID, ss.PeriodStart); err != nil {
					rep.Errors++
				}
			}
			rep.Lifted++
			continue
		}
		if ss.AppliedAt != nil {
			continue
		}
		reason := fmt.Sprintf("monthly spend limit %s reached for %s (%s accrued above the package)", ss.SpendLimit, ss.PeriodStart.Format("2006-01"), ss.Accrued)
		if err := e.Platform.StopOverage(ctx, slug, ss.Until, reason); err != nil {
			rep.Errors++
			_ = e.Store.SpendStopError(ctx, ss.SourceID, ss.PeriodStart, err.Error())
			slog.Warn("spend-limit evaluator: hold the Organization at its package", "org", slug, "error", err)
			continue
		}
		if err := e.Store.MarkSpendStop(ctx, ss.SourceID, ss.PeriodStart, "applied_at", now, ""); err != nil {
			rep.Errors++
			continue
		}
		rep.Applied++
	}
	return rep, nil
}

// orgSlug is the Organization a platform Source bills: its customer's
// Organization slug, else the Source's own label (the platform Source of an
// Organization is keyed by its slug).
func (e *Evaluator) orgSlug(ctx context.Context, sourceID string) (string, error) {
	src, err := e.Store.GetSource(ctx, store.OperatorScope, sourceID)
	if err != nil {
		return "", err
	}
	c, err := e.Store.GetCustomer(ctx, store.OperatorScope, src.CustomerID)
	if err != nil {
		return "", err
	}
	if c.OrgSlug != nil && *c.OrgSlug != "" {
		return *c.OrgSlug, nil
	}
	return c.Slug, nil
}

// notice sends overage.spend_limit_reached to the customer's admin. A
// failure is logged: the stop is enforced whether or not the mail went out.
func (e *Evaluator) notice(ctx context.Context, c store.Customer, src store.CostSource, used *big.Rat, from, until time.Time) {
	if e.Notify == nil || c.AdminEmail == "" {
		return
	}
	org := c.Slug
	if c.OrgSlug != nil && *c.OrgSlug != "" {
		org = *c.OrgSlug
	}
	currency := "OMR"
	if src.PriceBookID != nil {
		if pb, err := e.Store.GetPriceBook(ctx, *src.PriceBookID); err == nil && pb.Currency != "" {
			currency = pb.Currency
		}
	}
	payload := map[string]any{
		"customer_name": c.Name,
		"organization":  org,
		"package":       store.PlanName(c.PlanSlug),
		"accrued":       used.FloatString(3),
		"limit":         string(*src.SpendLimitMonth),
		"currency":      currency,
		"period":        from.Format("2006-01"),
		"until":         until.Format("2006-01-02"),
	}
	cid := c.ID
	if _, err := e.Notify.Send(ctx, notify.Request{Event: notify.EventOverageSpendLimit, To: c.AdminEmail, CustomerID: &cid, Payload: payload}); err != nil {
		slog.Warn("spend-limit evaluator: notice", "customer", c.Slug, "error", err)
	}
}
