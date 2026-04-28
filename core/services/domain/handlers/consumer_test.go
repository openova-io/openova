package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/openova-io/openova/core/services/shared/events"
)

// fakeDomainDeleter records the arguments passed to DeleteDomainsByTenant so
// the consumer's behavior can be asserted without a live FerretDB.
type fakeDomainDeleter struct {
	calls []string
	n     int64
	err   error
}

func (f *fakeDomainDeleter) DeleteDomainsByTenant(_ context.Context, tenantID string) (int64, error) {
	f.calls = append(f.calls, tenantID)
	if f.err != nil {
		return 0, f.err
	}
	return f.n, nil
}

func mkTenantDeletedEvent(tenantID, slug string) *events.Event {
	payload, _ := json.Marshal(map[string]string{"id": tenantID, "slug": slug})
	return &events.Event{
		ID:       "evt-dom-td",
		Type:     "tenant.deleted",
		TenantID: tenantID,
		Data:     payload,
	}
}

// TestHandleTenantDeleted_DeletesAllDomainsForTenant is the primary #95
// regression guard. Given a valid tenant.deleted event, the consumer must
// call DeleteDomainsByTenant with the carried tenant ID exactly once.
func TestHandleTenantDeleted_DeletesAllDomainsForTenant(t *testing.T) {
	fake := &fakeDomainDeleter{n: 3}
	c := &TenantConsumer{deleter: fake}

	if err := c.handleTenantDeleted(context.Background(), mkTenantDeletedEvent("tenant-42", "acme")); err != nil {
		t.Fatalf("handleTenantDeleted: %v", err)
	}
	if got := fake.calls; len(got) != 1 || got[0] != "tenant-42" {
		t.Fatalf("expected one delete for tenant-42, got %v", got)
	}
}

// TestHandleTenantDeleted_FallsBackToEnvelopeTenantID: older or partial
// publishers may emit an empty inner body; the envelope TenantID still lets
// us cascade. This matches the discipline used in the billing consumer.
func TestHandleTenantDeleted_FallsBackToEnvelopeTenantID(t *testing.T) {
	fake := &fakeDomainDeleter{}
	c := &TenantConsumer{deleter: fake}
	evt := &events.Event{
		ID:       "evt",
		Type:     "tenant.deleted",
		TenantID: "tenant-envelope",
		Data:     json.RawMessage(`{}`),
	}
	if err := c.handleTenantDeleted(context.Background(), evt); err != nil {
		t.Fatalf("handleTenantDeleted: %v", err)
	}
	if got := fake.calls; len(got) != 1 || got[0] != "tenant-envelope" {
		t.Fatalf("want fallback to envelope id, got %v", got)
	}
}

// TestHandleTenantDeleted_EmptyTenantIDSkipsSilently: no id at all means we
// have nothing to cascade. Don't touch the store; don't error (the commit
// must happen so the partition advances).
func TestHandleTenantDeleted_EmptyTenantIDSkipsSilently(t *testing.T) {
	fake := &fakeDomainDeleter{}
	c := &TenantConsumer{deleter: fake}
	evt := &events.Event{
		ID:   "evt",
		Type: "tenant.deleted",
		Data: json.RawMessage(`{}`),
	}
	if err := c.handleTenantDeleted(context.Background(), evt); err != nil {
		t.Fatalf("handleTenantDeleted: %v", err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("store must not be called when no tenant id is available, got %v", fake.calls)
	}
}

// TestHandleTenantDeleted_MalformedPayloadCommitsPastPoisonPill ensures a
// non-object payload doesn't wedge the partition — log + nil error so the
// offset advances.
func TestHandleTenantDeleted_MalformedPayloadCommitsPastPoisonPill(t *testing.T) {
	fake := &fakeDomainDeleter{}
	c := &TenantConsumer{deleter: fake}
	evt := &events.Event{
		ID:       "evt",
		Type:     "tenant.deleted",
		TenantID: "tenant-x",
		Data:     json.RawMessage(`"not an object"`),
	}
	if err := c.handleTenantDeleted(context.Background(), evt); err != nil {
		t.Fatalf("handleTenantDeleted: %v", err)
	}
	if len(fake.calls) != 0 {
		t.Fatal("store must not be called when payload is malformed")
	}
}

// TestHandleTenantDeleted_StoreErrorPropagates: a real DB failure must
// surface so the consumer does NOT commit and the event is redelivered.
func TestHandleTenantDeleted_StoreErrorPropagates(t *testing.T) {
	fake := &fakeDomainDeleter{err: fmt.Errorf("ferretdb: connection refused")}
	c := &TenantConsumer{deleter: fake}
	if err := c.handleTenantDeleted(context.Background(), mkTenantDeletedEvent("tenant-42", "acme")); err == nil {
		t.Fatal("expected error to propagate so broker redelivers, got nil")
	}
}
