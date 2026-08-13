// settlement_launch_reconciler.go — #6242. A settled order whose launch call was
// lost must still reach the tenant service.
//
// THE DEFECT
// ----------
// Since #4956 the marketplace funnel creates the Organization with
// `defer_launch: true`: the Org row is persisted but parked at
// `pending_payment`, and it is launched ONLY when billing settles the checkout.
// That launch is one HTTP call — `POST /tenant/internal/tenants/{id}/launch`,
// made from dispatchOrderPlaced (handlers.go) with a 3s timeout.
//
// By the time it is made the customer has already paid. `CreditOnlyCheckout`
// has committed the order as `completed` and burned the voucher redemption; on
// the Stripe leg the webhook has already settled. The call is therefore the
// LAST leg of a transaction whose earlier legs are durable — and it was the only
// one with no recovery. Before this file, every failure path in launchTenant
// ended in a bare `return` after an slog line, so a tenant service that was
// rolling, a NetworkPolicy that had not converged, a 3s timeout, or a billing
// pod restarting between the DB commit and the POST left the customer with a
// paid order and an Organization that would never provision. `grep -rn
// pending_payment --include=*.go core/services/` found twelve non-test sites and
// not one of them was a sweeper.
//
// The code said so itself, twice, in its own log strings:
//
//	launchTenant: tenant launch call failed — paid Org may be left parked at pending_payment (#4956)
//	launchTenant: tenant launch non-200 — paid Org may be left parked at pending_payment (#4956)
//
// A comment that names the hole is not a producer that closes it.
//
// WHY THE ORDERS TABLE IS THE RIGHT DRIVER
// ----------------------------------------
// `order.placed` is published to NATS and is durable, but on a Sovereign it is
// an observer — it launches nothing (see dispatchOrderPlaced's own comment). The
// durable record of "this customer paid" is the `orders` row, written inside the
// same transaction that spends the credit. Sweeping THAT is what makes the
// recovery survive a process restart: an in-memory retry queue dies with the
// pod that owned it, and the pod dying between commit and POST is one of the
// exact failures this exists to survive.
//
// WHY RE-OFFERING IS SAFE
// -----------------------
// `InternalLaunchTenant` (core/services/tenant/handlers/handlers.go) wins an
// atomic `pending_payment -> provisioning` transition before firing anything,
// and answers `{"launched": false}` for a tenant already past that state. So the
// idempotency is enforced by the endpoint, on the row, under a CAS — not by this
// sweep's bookkeeping. That is deliberate: a filter HERE deciding which orders
// "still need" launching would be a second opinion that can be stale, and a
// stale skip is exactly the failure being fixed. This file's job is to keep
// OFFERING; the tenant service's job is to decide.
//
// WHAT IT DELIBERATELY DOES NOT DO
// --------------------------------
// It does not flip any order or tenant state, and it never reports a verdict
// about an Organization. A sweep that could not reach the tenant service knows
// only that it could not reach the tenant service — it says so and tries again
// on the next tick, rather than recording a conclusion it did not observe.
//
// Refs #6242 · #4956 (the settlement gate) · #3376 · #3860.

package handlers

import (
	"context"
	"log/slog"
	"time"

	"github.com/openova-io/openova/core/services/billing/store"
)

// Settlement-launch reconciler defaults. All three are overridable by the
// caller so main.go can tune them from env per Inviolable Principle #4.
const (
	// DefaultSettlementLaunchInterval is how often the sweep runs. A funnel
	// customer is watching a provisioning page, so the recovery has to be
	// measured in a minute or two, not an hour.
	DefaultSettlementLaunchInterval = 2 * time.Minute

	// DefaultSettlementLaunchLookback bounds how far back a settled order is
	// still re-offered. Long enough to cover a tenant-service outage plus the
	// operator noticing; short enough that an order the customer has long
	// since abandoned becomes a support case rather than something that
	// silently provisions weeks later.
	DefaultSettlementLaunchLookback = 24 * time.Hour

	// DefaultSettlementLaunchBatch caps one sweep so a large backlog cannot
	// turn a recovery into a thundering herd against the tenant service.
	DefaultSettlementLaunchBatch = 100
)

// settlementLaunchStore is the store subset the reconciler needs. Narrow by
// design so a test can drive the real Run loop without a database.
type settlementLaunchStore interface {
	ListOrdersAwaitingLaunch(ctx context.Context, lookback time.Duration, limit int) ([]store.Order, error)
}

// settlementLauncher is the launch leg. In production this is the Handler's own
// launchTenant, so the reconciler exercises the SAME call site the settlement
// path uses — not a parallel re-implementation that could drift from it.
type settlementLauncher interface {
	launchTenant(ctx context.Context, tenantID string) error
}

// SettlementLaunchReconciler re-offers the tenant launch for every settled order
// inside the lookback window, on a ticker.
type SettlementLaunchReconciler struct {
	// Store reads the settled orders. Required; a nil Store makes Run a no-op.
	Store settlementLaunchStore

	// Launcher performs the launch call. Required; wire the billing Handler.
	Launcher settlementLauncher

	// Interval, Lookback and Batch default to the Default… constants above
	// when left zero.
	Interval time.Duration
	Lookback time.Duration
	Batch    int
}

func (r *SettlementLaunchReconciler) interval() time.Duration {
	if r.Interval > 0 {
		return r.Interval
	}
	return DefaultSettlementLaunchInterval
}

func (r *SettlementLaunchReconciler) lookback() time.Duration {
	if r.Lookback > 0 {
		return r.Lookback
	}
	return DefaultSettlementLaunchLookback
}

func (r *SettlementLaunchReconciler) batch() int {
	if r.Batch > 0 {
		return r.Batch
	}
	return DefaultSettlementLaunchBatch
}

// Run sweeps until ctx is done. It is safe to call once per process; the
// endpoint's CAS makes concurrent billing replicas safe too.
func (r *SettlementLaunchReconciler) Run(ctx context.Context) {
	if r == nil || r.Store == nil || r.Launcher == nil {
		slog.Warn("settlement-launch reconciler not wired — a settled order whose launch call is lost will stay parked at pending_payment (#6242)")
		return
	}
	interval := r.interval()
	slog.Info("settlement-launch reconciler started (#6242)",
		"interval", interval, "lookback", r.lookback(), "batch", r.batch())

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		// Sweep first, then wait. A billing pod that restarted mid-settlement
		// is the case most in need of the sweep, and making it wait a full
		// interval before the first attempt would add the interval to the
		// customer's outage for no gain.
		r.Sweep(ctx)
		select {
		case <-ctx.Done():
			slog.Info("settlement-launch reconciler stopped", "reason", ctx.Err())
			return
		case <-ticker.C:
		}
	}
}

// Sweep performs exactly one pass. Exported so a test drives the real pass
// rather than a stand-in, and so an operator surface could trigger one.
//
// Returns the number of orders whose launch call was accepted by the tenant
// service this pass. That is a DELIVERY count, not a launch count — the tenant
// service answers 200 both for "I launched it" and for "it was already
// launched", and this side deliberately does not claim to know which.
func (r *SettlementLaunchReconciler) Sweep(ctx context.Context) int {
	if r == nil || r.Store == nil || r.Launcher == nil {
		return 0
	}
	orders, err := r.Store.ListOrdersAwaitingLaunch(ctx, r.lookback(), r.batch())
	if err != nil {
		slog.Error("settlement-launch sweep: could not read settled orders — a stranded paid Org stays stranded this pass (#6242)",
			"error", err)
		return 0
	}
	if len(orders) == 0 {
		return 0
	}

	// De-duplicate by tenant: several settled orders can belong to one Org
	// (an add-on purchased after the first plan), and offering the launch once
	// per order would multiply the calls for no extra effect.
	seen := make(map[string]bool, len(orders))
	delivered := 0
	for _, o := range orders {
		if ctx.Err() != nil {
			return delivered
		}
		if o.TenantID == "" || seen[o.TenantID] {
			continue
		}
		seen[o.TenantID] = true
		if err := r.Launcher.launchTenant(ctx, o.TenantID); err != nil {
			// Not an error verdict about the Organization — only about this
			// attempt. The next tick tries again.
			slog.Warn("settlement-launch sweep: launch call did not land, will retry next pass (#6242)",
				"tenant_id", o.TenantID, "order_id", o.ID, "error", err)
			continue
		}
		delivered++
	}
	slog.Info("settlement-launch sweep complete (#6242)",
		"settled_orders", len(orders), "distinct_tenants", len(seen), "delivered", delivered)
	return delivered
}
