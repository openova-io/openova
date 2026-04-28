package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stripe/stripe-go/v81"

	"github.com/openova-io/openova/core/services/billing/store"
	"github.com/openova-io/openova/core/services/shared/events"
)

// fakeCanceller records every Cancel call so the test can assert what the
// consumer asked Stripe to do without hitting the real SDK.
type fakeCanceller struct {
	calls []string
	// err is returned from Cancel when non-nil, used to simulate Stripe
	// failures like "resource_missing" or transient 5xxs.
	err error
}

func (f *fakeCanceller) Cancel(id string, _ *stripe.SubscriptionCancelParams) (*stripe.Subscription, error) {
	f.calls = append(f.calls, id)
	if f.err != nil {
		return nil, f.err
	}
	return &stripe.Subscription{ID: id, Status: stripe.SubscriptionStatusCanceled}, nil
}

// expectSettingsWithKey mocks the Settings lookup so the consumer sees a
// configured Stripe key and proceeds with API calls.
func expectSettingsWithKey(mock sqlmock.Sqlmock, key string) {
	mock.ExpectQuery(regexp.QuoteMeta(
		"SELECT stripe_secret_key, stripe_webhook_secret, stripe_public_key, updated_at",
	)).WillReturnRows(
		sqlmock.NewRows([]string{"stripe_secret_key", "stripe_webhook_secret", "stripe_public_key", "updated_at"}).
			AddRow(key, "whsec", "pk_test", time.Now()),
	)
}

// mkTenantDeletedEvent builds the event shape published by the tenant
// service — envelope TenantID + inner {id, slug} payload.
func mkTenantDeletedEvent(tenantID, slug string) *events.Event {
	payload, _ := json.Marshal(map[string]string{"id": tenantID, "slug": slug})
	return &events.Event{
		ID:       "evt-tenant-deleted",
		Type:     "tenant.deleted",
		TenantID: tenantID,
		Data:     payload,
	}
}

// TestHandleTenantDeleted_CancelsActiveSubsAndVoidsInvoices is the primary
// regression test for issue #94. It walks through the full cascade against a
// mocked DB and a fake Stripe canceller, asserting that each side effect
// fires in order with the right arguments.
func TestHandleTenantDeleted_CancelsActiveSubsAndVoidsInvoices(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	c := &TenantConsumer{
		Store:           store.New(db),
		StripeCanceller: &fakeCanceller{},
	}

	// 1. List active subs for the tenant — one has a Stripe ID, one doesn't
	// (a credit-only subscription).
	mock.ExpectQuery(regexp.QuoteMeta("FROM subscriptions\n\t\t WHERE tenant_id = $1")).
		WithArgs("tenant-42").
		WillReturnRows(
			sqlmock.NewRows([]string{
				"id", "customer_id", "tenant_id", "stripe_subscription_id", "plan_id", "status",
				"current_period_start", "current_period_end", "created_at", "updated_at",
			}).
				AddRow("sub-1", "cust-1", "tenant-42", "sub_stripe_1", "plan-a", "active", nil, nil, time.Now(), time.Now()).
				AddRow("sub-2", "cust-2", "tenant-42", nil, "plan-a", "active", nil, nil, time.Now(), time.Now()),
		)
	// 2. Settings lookup — Stripe IS configured.
	expectSettingsWithKey(mock, "sk_test")
	// 3. UpdateSubscription x2 — marked canceled locally. The SET clause is
	// dynamic; match on the UPDATE target and allow any args.
	mock.ExpectExec(regexp.QuoteMeta("UPDATE subscriptions SET")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE subscriptions SET")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// 4. Void open invoices — returns 3 voided rows (arbitrary, only the
	// call itself matters for the regression guard).
	mock.ExpectExec(regexp.QuoteMeta("UPDATE invoices SET status = 'voided'")).
		WithArgs("tenant-42").
		WillReturnResult(sqlmock.NewResult(0, 3))
	// 5. Flip credit-ledger rows to 'tenant_deleted'.
	mock.ExpectExec(regexp.QuoteMeta("UPDATE credit_ledger")).
		WithArgs("tenant-42").
		WillReturnResult(sqlmock.NewResult(0, 2))

	fake := c.StripeCanceller.(*fakeCanceller)
	if err := c.handleTenantDeleted(context.Background(), mkTenantDeletedEvent("tenant-42", "acme")); err != nil {
		t.Fatalf("handleTenantDeleted: %v", err)
	}

	if len(fake.calls) != 1 || fake.calls[0] != "sub_stripe_1" {
		t.Fatalf("expected Stripe Cancel to be called once for sub_stripe_1, got %v", fake.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}

// TestHandleTenantDeleted_IdempotentWhenAlreadyCascaded confirms re-delivery
// of a tenant.deleted event does nothing harmful: no active subs, no open
// invoices, and the credit-ledger UPDATE returns 0 rows with status already
// set. The consumer must still return nil and not panic.
func TestHandleTenantDeleted_IdempotentWhenAlreadyCascaded(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	c := &TenantConsumer{
		Store:           store.New(db),
		StripeCanceller: &fakeCanceller{},
	}

	// Empty sub list — short-circuits before Settings is even consulted.
	mock.ExpectQuery(regexp.QuoteMeta("FROM subscriptions")).
		WithArgs("tenant-42").
		WillReturnRows(
			sqlmock.NewRows([]string{
				"id", "customer_id", "tenant_id", "stripe_subscription_id", "plan_id", "status",
				"current_period_start", "current_period_end", "created_at", "updated_at",
			}),
		)
	// No open invoices.
	mock.ExpectExec(regexp.QuoteMeta("UPDATE invoices SET status = 'voided'")).
		WithArgs("tenant-42").
		WillReturnResult(sqlmock.NewResult(0, 0))
	// Credit ledger already flipped.
	mock.ExpectExec(regexp.QuoteMeta("UPDATE credit_ledger")).
		WithArgs("tenant-42").
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := c.handleTenantDeleted(context.Background(), mkTenantDeletedEvent("tenant-42", "acme")); err != nil {
		t.Fatalf("handleTenantDeleted (idempotent): %v", err)
	}
	if calls := c.StripeCanceller.(*fakeCanceller).calls; len(calls) != 0 {
		t.Fatalf("no Stripe calls expected when no active subs, got %v", calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}

// TestHandleTenantDeleted_StripeAlreadyCanceledIsSuccess: Stripe returns a
// resource_missing error for an already-canceled subscription. The cascade
// must treat this as success and still flip the local row so state stays
// consistent.
func TestHandleTenantDeleted_StripeAlreadyCanceledIsSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	c := &TenantConsumer{
		Store: store.New(db),
		StripeCanceller: &fakeCanceller{
			err: &stripe.Error{Code: stripe.ErrorCodeResourceMissing, Msg: "No such subscription"},
		},
	}

	mock.ExpectQuery(regexp.QuoteMeta("FROM subscriptions")).
		WithArgs("tenant-42").
		WillReturnRows(
			sqlmock.NewRows([]string{
				"id", "customer_id", "tenant_id", "stripe_subscription_id", "plan_id", "status",
				"current_period_start", "current_period_end", "created_at", "updated_at",
			}).
				AddRow("sub-1", "cust-1", "tenant-42", "sub_stripe_gone", "plan-a", "active", nil, nil, time.Now(), time.Now()),
		)
	expectSettingsWithKey(mock, "sk_test")
	// Local UPDATE still happens despite the Stripe error.
	mock.ExpectExec(regexp.QuoteMeta("UPDATE subscriptions SET")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE invoices SET status = 'voided'")).
		WithArgs("tenant-42").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE credit_ledger")).
		WithArgs("tenant-42").
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := c.handleTenantDeleted(context.Background(), mkTenantDeletedEvent("tenant-42", "acme")); err != nil {
		t.Fatalf("handleTenantDeleted: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}

// TestHandleTenantDeleted_StripeTransientErrorPropagates: a non-recoverable
// Stripe error (e.g. 5xx, rate limit) must propagate so the consumer does
// NOT commit the offset and the event is redelivered.
func TestHandleTenantDeleted_StripeTransientErrorPropagates(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	c := &TenantConsumer{
		Store: store.New(db),
		StripeCanceller: &fakeCanceller{
			err: fmt.Errorf("stripe: 503 service unavailable"),
		},
	}

	mock.ExpectQuery(regexp.QuoteMeta("FROM subscriptions")).
		WithArgs("tenant-42").
		WillReturnRows(
			sqlmock.NewRows([]string{
				"id", "customer_id", "tenant_id", "stripe_subscription_id", "plan_id", "status",
				"current_period_start", "current_period_end", "created_at", "updated_at",
			}).
				AddRow("sub-1", "cust-1", "tenant-42", "sub_stripe_err", "plan-a", "active", nil, nil, time.Now(), time.Now()),
		)
	expectSettingsWithKey(mock, "sk_test")
	// NO local UPDATE on the errored sub — we skip and retry next delivery.
	// Other steps (invoices, credit-ledger) still run so unrelated cascades
	// make forward progress.
	mock.ExpectExec(regexp.QuoteMeta("UPDATE invoices SET status = 'voided'")).
		WithArgs("tenant-42").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE credit_ledger")).
		WithArgs("tenant-42").
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := c.handleTenantDeleted(context.Background(), mkTenantDeletedEvent("tenant-42", "acme")); err == nil {
		t.Fatal("expected error to propagate so broker redelivers, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}

// TestHandleTenantDeleted_MalformedPayloadCommits confirms a payload we can't
// unmarshal does not re-deliver forever — the handler logs + returns nil so
// the broker commits past the poison pill.
func TestHandleTenantDeleted_MalformedPayloadCommits(t *testing.T) {
	c := &TenantConsumer{Store: nil} // nil store would panic if touched
	evt := &events.Event{
		ID:       "evt-bad",
		Type:     "tenant.deleted",
		TenantID: "tenant-x",
		Data:     json.RawMessage(`"not an object"`),
	}
	if err := c.handleTenantDeleted(context.Background(), evt); err != nil {
		t.Fatalf("malformed payload should return nil, got %v", err)
	}
}
