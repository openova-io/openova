package handlers

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/tenant/store"
)

// tenantDeletedPayload mirrors the shape the tenant service itself emits on
// sme.tenant.events with type tenant.deleted. The envelope TenantID and the
// inner ID are both populated by the publisher, but we fall back to the
// envelope so a truncated body can't block the cascade.
type tenantDeletedPayload struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
}

// membersDeleter is the narrow slice of the store this consumer needs. An
// interface keeps unit tests free of FerretDB.
type membersDeleter interface {
	DeleteMembersByTenant(ctx context.Context, tenantID string) (int64, error)
}

// MembersCleanupConsumer drives the member-cleanup half of the
// tenant.deleted cascade (issue #96).
//
// When a tenant is soft-deleted, the tenant service publishes tenant.deleted
// and the tenant row flips to status="deleted". The hard-delete happens
// later when the provisioning service reports provision.tenant_removed.
// Between those two events, member rows for the deleted tenant would
// otherwise linger and skew authz checks (non-superadmin membership
// lookups, tenant-scoped IDOR guards).
//
// This consumer closes the gap by purging members as soon as the soft-delete
// event lands. The late hard-delete (DeleteTenant) does the same cascade on
// its side — idempotency-by-construction is what keeps both paths safe to
// run without coordination.
type MembersCleanupConsumer struct {
	// Store is wired from main. members() is the only collection touched.
	Store *store.Store
	// deleter is the test seam. Defaults to Store when nil.
	deleter membersDeleter
}

func (c *MembersCleanupConsumer) membersDeleter() membersDeleter {
	if c.deleter != nil {
		return c.deleter
	}
	return c.Store
}

// Start subscribes to sme.tenant.events and dispatches tenant.deleted.
func (c *MembersCleanupConsumer) Start(ctx context.Context, consumer *events.Consumer) error {
	slog.Info("starting tenant members-cleanup consumer")
	return consumer.Subscribe(ctx, func(event *events.Event) error {
		if event.Type != "tenant.deleted" {
			return nil
		}
		return c.handleTenantDeleted(ctx, event)
	})
}

// handleTenantDeleted purges member rows for the deleted tenant. A malformed
// payload is committed past (log + nil return) so we don't wedge the
// partition on a poison pill. A real DB error propagates so the consumer
// redelivers and the member purge eventually succeeds.
func (c *MembersCleanupConsumer) handleTenantDeleted(ctx context.Context, event *events.Event) error {
	var payload tenantDeletedPayload
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		slog.Error("tenant-members-cleanup: malformed tenant.deleted payload",
			"event_id", event.ID, "error", err)
		return nil
	}
	tenantID := payload.ID
	if tenantID == "" {
		tenantID = event.TenantID
	}
	if tenantID == "" {
		slog.Warn("tenant-members-cleanup: missing tenant id — skipping",
			"event_id", event.ID)
		return nil
	}

	slog.Info("tenant-members-cleanup: cascading tenant.deleted",
		"tenant_id", tenantID, "slug", payload.Slug, "event_id", event.ID)

	n, err := c.membersDeleter().DeleteMembersByTenant(ctx, tenantID)
	if err != nil {
		slog.Error("tenant-members-cleanup: delete members failed",
			"tenant_id", tenantID, "error", err)
		return err
	}
	slog.Info("tenant-members-cleanup: complete",
		"tenant_id", tenantID, "deleted_members", n)
	return nil
}
