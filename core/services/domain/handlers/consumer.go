package handlers

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/openova-io/openova/core/services/domain/store"
	"github.com/openova-io/openova/core/services/shared/events"
)

// tenantDeletedPayload mirrors the shape emitted by the tenant service on
// `sme.tenant.events` — both the inner id and the envelope TenantID carry
// the same value, and we fall back to the envelope when the inner body is
// empty so a half-populated publisher can't silently break the cascade.
type tenantDeletedPayload struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
}

// tenantDomainDeleter is the narrow slice of the store the consumer needs.
// Keeping the dependency behind an interface lets the test supply an
// in-memory fake without standing up FerretDB.
type tenantDomainDeleter interface {
	DeleteDomainsByTenant(ctx context.Context, tenantID string) (int64, error)
}

// TenantConsumer drives the `tenant.deleted` cascade in the domain service.
//
// Responsibility (issue #95): delete every domain record owned by the
// tenant so (a) orphan subdomains don't keep the slot reserved against new
// customers registering the same name, and (b) BYOD rows for a deleted
// tenant aren't left pointing at a non-existent owner.
//
// External DNS record cleanup (Dynadot) is intentionally OUT OF SCOPE for
// this consumer. The only integration today is a human-readable instruction
// string in the BYOD response (see handlers/whois.go). If the platform ever
// programmatically creates DNS records it must add a Delete step here —
// tracked separately so we don't block the DB-side fix on that work.
//
// Idempotent by design: DeleteMany with no matches is a no-op, so a
// redelivered event simply becomes "delete 0 rows" on the second pass.
type TenantConsumer struct {
	// Store is the production-facing full store. Kept for the exported field
	// so main.go wiring stays obvious.
	Store *store.Store
	// deleter is the test seam used by unit tests. Defaults to Store on first
	// use.
	deleter tenantDomainDeleter
}

// tenantDeleter returns the configured deleter, defaulting to the real store.
// Centralising the lazy default means the constructor stays a single-field
// struct literal and tests can inject via the unexported setter below.
func (c *TenantConsumer) tenantDeleter() tenantDomainDeleter {
	if c.deleter != nil {
		return c.deleter
	}
	return c.Store
}

// Start subscribes to sme.tenant.events and dispatches tenant.deleted.
// Any other event type is ignored so we don't contend with the provisioning
// consumer group on unrelated workloads.
func (c *TenantConsumer) Start(ctx context.Context, consumer *events.Consumer) error {
	slog.Info("starting domain tenant-events consumer")
	return consumer.Subscribe(ctx, func(event *events.Event) error {
		if event.Type != "tenant.deleted" {
			return nil
		}
		return c.handleTenantDeleted(ctx, event)
	})
}

// handleTenantDeleted removes every domain record for the tenant. Returns
// an error only on real DB failures so the consumer redelivers; a payload we
// can't parse is committed past (log + no-op) to avoid wedging the partition.
func (c *TenantConsumer) handleTenantDeleted(ctx context.Context, event *events.Event) error {
	var payload tenantDeletedPayload
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		slog.Error("domain: malformed tenant.deleted payload",
			"event_id", event.ID, "error", err)
		return nil
	}
	tenantID := payload.ID
	if tenantID == "" {
		tenantID = event.TenantID
	}
	if tenantID == "" {
		slog.Warn("domain: tenant.deleted missing tenant id — skipping",
			"event_id", event.ID)
		return nil
	}

	slog.Info("domain: cascading tenant.deleted",
		"tenant_id", tenantID, "slug", payload.Slug, "event_id", event.ID)

	n, err := c.tenantDeleter().DeleteDomainsByTenant(ctx, tenantID)
	if err != nil {
		slog.Error("domain: delete domains for tenant failed",
			"tenant_id", tenantID, "error", err)
		return err
	}
	slog.Info("domain: tenant.deleted cascade complete",
		"tenant_id", tenantID, "deleted", n)
	return nil
}
