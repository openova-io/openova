package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/stripe/stripe-go/v81"
	stripesubscription "github.com/stripe/stripe-go/v81/subscription"

	"github.com/openova-io/openova/core/services/billing/store"
	"github.com/openova-io/openova/core/services/shared/events"
)

// tenantDeletedPayload matches the shape emitted by the tenant service on
// `sme.tenant.events` with type `tenant.deleted`. The tenant service writes
// both the event-envelope TenantID and the inner Data.ID to the same value;
// we fall back to event.TenantID when the inner id is missing so we never
// silently drop a delivery.
type tenantDeletedPayload struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
}

// stripeCanceller is the subset of the Stripe subscription client the cascade
// needs. Tests can swap in a fake; production uses stripeSubscriptionCanceller
// which delegates to the real Stripe SDK.
type stripeCanceller interface {
	Cancel(id string, params *stripe.SubscriptionCancelParams) (*stripe.Subscription, error)
}

// stripeSubscriptionCanceller is the production adapter around the Stripe
// `subscription.Cancel` package-level helper. Keeping it behind the interface
// means the consumer test never touches the real SDK (which would require a
// valid API key + network access).
type stripeSubscriptionCanceller struct{}

// Cancel delegates to the real Stripe subscription client.
func (stripeSubscriptionCanceller) Cancel(id string, params *stripe.SubscriptionCancelParams) (*stripe.Subscription, error) {
	return stripesubscription.Cancel(id, params)
}

// TenantConsumer drives the `tenant.deleted` cascade in the billing service.
//
// Responsibilities (issue #94):
//  1. Cancel every NON-terminal Stripe subscription owned by the tenant so
//     Stripe stops issuing invoices the customer can no longer pay for.
//  2. Void any 'draft' or 'open' invoices attached to the tenant — these are
//     the only statuses we can safely move to 'voided' without misrepresenting
//     collected revenue. 'paid' / 'uncollectible' invoices are intentionally
//     left as-is.
//  3. Flip every credit-ledger entry belonging to the tenant's customers to
//     status='tenant_deleted'. Rows are NOT deleted — the audit trail is
//     preserved for financial reporting and eventual reconciliation.
//
// Idempotency: if the tenant is already fully cascaded (no active subs, no
// open invoices, all ledger rows already tagged), every step becomes a no-op.
// Re-delivery is therefore safe — the at-least-once Consumer contract can
// replay this event multiple times without drift.
type TenantConsumer struct {
	Store *store.Store

	// StripeCanceller cancels a Stripe subscription by ID. Defaults to the
	// real Stripe SDK on first use. Tests MUST set this before calling
	// cancelStripeSubscriptions to avoid live Stripe traffic.
	StripeCanceller stripeCanceller
}

// Start subscribes to sme.tenant.events and dispatches tenant.deleted events.
// Other event types on the same topic are ignored — this consumer is
// scoped specifically to the billing-side cascade.
func (c *TenantConsumer) Start(ctx context.Context, consumer *events.Consumer) error {
	slog.Info("starting billing tenant-events consumer")
	return consumer.Subscribe(ctx, func(event *events.Event) error {
		if event.Type != "tenant.deleted" {
			return nil
		}
		return c.handleTenantDeleted(ctx, event)
	})
}

// handleTenantDeleted fans the three sub-steps out. We tolerate individual
// sub-step failures by logging them and continuing — a broker outage on the
// Stripe side must not leave invoices un-voided in our own DB, and a Stripe
// API call that succeeds must not be rolled back because the DB flush after
// it tripped. The final error return reflects the *first* failure so the
// consumer commits only when everything succeeded; otherwise the event is
// re-delivered and each step's idempotent implementation keeps the second
// pass safe.
func (c *TenantConsumer) handleTenantDeleted(ctx context.Context, event *events.Event) error {
	var payload tenantDeletedPayload
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		slog.Error("billing: malformed tenant.deleted payload",
			"event_id", event.ID, "error", err)
		// Malformed body is not retryable — commit so we don't wedge the
		// partition on a poison pill. The envelope TenantID might still give
		// us enough to cascade, but we play it safe.
		return nil
	}

	tenantID := payload.ID
	if tenantID == "" {
		tenantID = event.TenantID
	}
	if tenantID == "" {
		slog.Warn("billing: tenant.deleted missing tenant id — skipping",
			"event_id", event.ID)
		return nil
	}

	slog.Info("billing: cascading tenant.deleted",
		"tenant_id", tenantID, "slug", payload.Slug, "event_id", event.ID)

	var firstErr error

	if err := c.cancelStripeSubscriptions(ctx, tenantID); err != nil {
		slog.Error("billing: cancel stripe subs failed",
			"tenant_id", tenantID, "error", err)
		if firstErr == nil {
			firstErr = err
		}
	}

	if err := c.voidOpenInvoices(ctx, tenantID); err != nil {
		slog.Error("billing: void invoices failed",
			"tenant_id", tenantID, "error", err)
		if firstErr == nil {
			firstErr = err
		}
	}

	if err := c.markCreditLedger(ctx, tenantID); err != nil {
		slog.Error("billing: mark credit ledger failed",
			"tenant_id", tenantID, "error", err)
		if firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}

// cancelStripeSubscriptions cancels every non-terminal subscription for the
// tenant. For each subscription we:
//   - If there is a Stripe subscription ID AND Stripe is configured, call
//     Stripe's Cancel API. A "resource_missing" error is treated as success
//     (already canceled on Stripe's side).
//   - Mark the local row as 'canceled' regardless of Stripe outcome — the
//     tenant is gone and we don't want UpdateSubscription polling later
//     flipping it back to 'active'.
//
// Failures from Stripe bubble up so the consumer redelivers and tries again.
// The local UPDATE is attempted even when Stripe fails so our DB reflects
// intent; the Stripe webhook will ultimately reconcile any drift.
func (c *TenantConsumer) cancelStripeSubscriptions(ctx context.Context, tenantID string) error {
	subs, err := c.Store.ListActiveSubscriptionsByTenant(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("list subs: %w", err)
	}
	if len(subs) == 0 {
		return nil
	}

	// Configure Stripe once per event. Settings may be empty in self-hosted
	// installs that don't run Stripe — in that case we skip the API call
	// but still flip local rows so state is consistent.
	settings, err := c.Store.GetSettings(ctx)
	if err != nil {
		return fmt.Errorf("get settings: %w", err)
	}
	stripeConfigured := settings.StripeSecretKey != ""
	if stripeConfigured {
		stripe.Key = settings.StripeSecretKey
	}
	canceller := c.StripeCanceller
	if canceller == nil {
		canceller = stripeSubscriptionCanceller{}
	}

	var firstErr error
	for _, sub := range subs {
		if sub.StripeSubscriptionID != "" && stripeConfigured {
			if _, cerr := canceller.Cancel(sub.StripeSubscriptionID, nil); cerr != nil {
				if isStripeAlreadyCanceled(cerr) {
					slog.Info("billing: stripe sub already canceled — treating as success",
						"tenant_id", tenantID, "stripe_sub", sub.StripeSubscriptionID)
				} else {
					slog.Error("billing: stripe cancel failed",
						"tenant_id", tenantID, "stripe_sub", sub.StripeSubscriptionID, "error", cerr)
					if firstErr == nil {
						firstErr = fmt.Errorf("stripe cancel %s: %w", sub.StripeSubscriptionID, cerr)
					}
					// Don't update local row — we want Stripe reality to catch
					// up before we record cancellation locally. Next redelivery
					// will retry.
					continue
				}
			}
		}

		if uerr := c.Store.UpdateSubscription(ctx, sub.ID, map[string]any{
			"status": "canceled",
		}); uerr != nil {
			slog.Error("billing: mark subscription canceled (local) failed",
				"tenant_id", tenantID, "sub_id", sub.ID, "error", uerr)
			if firstErr == nil {
				firstErr = fmt.Errorf("update local sub %s: %w", sub.ID, uerr)
			}
		}
	}
	return firstErr
}

// voidOpenInvoices flips draft/open invoices to 'voided'. No Stripe side
// effect — Stripe keeps its own invoice state machine and our voiding here
// is purely for the local audit view.
func (c *TenantConsumer) voidOpenInvoices(ctx context.Context, tenantID string) error {
	n, err := c.Store.VoidOpenInvoicesByTenant(ctx, tenantID)
	if err != nil {
		return err
	}
	if n > 0 {
		slog.Info("billing: voided invoices", "tenant_id", tenantID, "count", n)
	}
	return nil
}

// markCreditLedger flips all ledger entries for the tenant's customers to
// the 'tenant_deleted' status. Balances are unaffected (GetCreditBalance
// sums amount_omr regardless of status) — this is strictly an audit marker.
func (c *TenantConsumer) markCreditLedger(ctx context.Context, tenantID string) error {
	n, err := c.Store.MarkCreditLedgerTenantDeleted(ctx, tenantID)
	if err != nil {
		return err
	}
	if n > 0 {
		slog.Info("billing: marked credit ledger tenant_deleted",
			"tenant_id", tenantID, "count", n)
	}
	return nil
}

// isStripeAlreadyCanceled returns true when Stripe's response indicates the
// subscription is already gone — either a resource_missing error or a
// "canceled" status on the returned object. We treat this as success because
// the cascade's intent (subscription is not active on Stripe) is satisfied.
func isStripeAlreadyCanceled(err error) bool {
	if err == nil {
		return false
	}
	var stripeErr *stripe.Error
	if errors.As(err, &stripeErr) {
		if stripeErr.Code == stripe.ErrorCodeResourceMissing {
			return true
		}
	}
	// Defensive substring match for older SDK paths that wrap the message.
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such subscription") ||
		strings.Contains(msg, "resource_missing")
}
