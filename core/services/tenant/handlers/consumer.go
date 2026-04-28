package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"

	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/tenant/store"
)

// ConsumerHandler reacts to events from other services to keep tenant state
// in sync — e.g., marking a tenant "active" once provisioning completes.
type ConsumerHandler struct {
	Store *store.Store

	// unmarshalErrors is a process-local counter of malformed event payloads.
	// Exposed via MalformedPayloadCount for tests + future /metrics scraping.
	// It is a simple integer so there is no external dependency (counter-only
	// semantics line up with a Prometheus counter when #72 DLQ metric lands).
	unmarshalErrors atomic.Uint64
}

// MalformedPayloadCount returns the number of events whose data payload
// failed json.Unmarshal since the process started. Safe for concurrent reads.
func (c *ConsumerHandler) MalformedPayloadCount() uint64 {
	return c.unmarshalErrors.Load()
}

// unmarshalPayload decodes event.Data into out. On failure it bumps the
// malformed-payload counter and logs at ERROR with a truncated payload head so
// operators can spot schema drift without drowning disk. Returns nil (i.e.
// "move on and commit the offset") in all cases: the event envelope itself
// parsed fine — the inner schema drifted, and blocking the partition on a
// poison pill would wedge every future tenant update. Issue #72 tracks DLQ;
// until that lands we log + count + commit, which is strictly better than the
// previous silent drop that wedged the console UI on "Installing…".
func (c *ConsumerHandler) unmarshalPayload(event *events.Event, kind string, out any) bool {
	if err := json.Unmarshal(event.Data, out); err != nil {
		c.unmarshalErrors.Add(1)
		slog.Error("tenant consumer: malformed event payload",
			"type", event.Type,
			"kind", kind,
			"tenant_id", event.TenantID,
			"event_id", event.ID,
			"error", err,
			"payload_head", payloadHead(event.Data, 256),
		)
		return false
	}
	return true
}

// payloadHead returns the first n bytes of raw as a string, with an ellipsis
// if truncated. Keeps log lines bounded for high-volume topics.
func payloadHead(raw []byte, n int) string {
	if len(raw) <= n {
		return string(raw)
	}
	return string(raw[:n]) + "…"
}

// Start subscribes to the given consumer and dispatches events.
func (c *ConsumerHandler) Start(ctx context.Context, consumer *events.Consumer) error {
	slog.Info("starting tenant event consumer")
	return consumer.Subscribe(ctx, func(event *events.Event) error {
		switch event.Type {
		case "provision.completed":
			return c.onProvisionCompleted(ctx, event)
		case "provision.failed":
			return c.onProvisionFailed(ctx, event)
		case "provision.app_ready":
			return c.onAppReady(ctx, event)
		case "provision.app_removed":
			return c.onAppRemoved(ctx, event)
		case "provision.app_failed":
			return c.onAppFailed(ctx, event)
		case "provision.tenant_removed":
			return c.onTenantRemoved(ctx, event)
		default:
			return nil
		}
	})
}

func (c *ConsumerHandler) onProvisionCompleted(ctx context.Context, event *events.Event) error {
	if event.TenantID == "" {
		return nil
	}
	if err := c.Store.UpdateTenantStatus(ctx, event.TenantID, "active"); err != nil {
		slog.Error("failed to mark tenant active", "tenant_id", event.TenantID, "error", err)
		return err
	}
	slog.Info("tenant activated", "tenant_id", event.TenantID)
	return nil
}

func (c *ConsumerHandler) onProvisionFailed(ctx context.Context, event *events.Event) error {
	if event.TenantID == "" {
		return nil
	}
	var payload struct {
		Error string `json:"error"`
	}
	// Best-effort parse: the status transition below does not need the
	// detail, but schema drift still needs to be visible. A malformed payload
	// is logged by unmarshalPayload and we continue with an empty detail.
	c.unmarshalPayload(event, "provision.failed", &payload)

	if err := c.Store.UpdateTenantStatus(ctx, event.TenantID, "failed"); err != nil {
		slog.Error("failed to mark tenant failed", "tenant_id", event.TenantID, "error", err)
		return err
	}
	slog.Warn("tenant marked as failed", "tenant_id", event.TenantID, "reason", payload.Error)
	return nil
}

// appEventPayload is the shape published by provisioning for per-app events.
type appEventPayload struct {
	AppSlug   string   `json:"app_slug"`
	AppID     string   `json:"app_id"`
	DeployIDs []string `json:"deploy_ids"`
	Action    string   `json:"action"`
	Error     string   `json:"error"`
}

// onAppReady clears the "installing" / "uninstalling" state for the app so the
// console flips from "Installing…" to "Installed".
//
// On malformed payload we short-circuit without touching the store — the
// previous implementation swallowed the unmarshal error, then called
// ClearAppState(tenant, "") which is a no-op and left the console stuck on
// "Installing…" forever. Issue #73 fix: log + count + return early so the
// commit still happens (we don't want to re-deliver a poison pill) but the
// error is visible in logs and the /metrics counter.
func (c *ConsumerHandler) onAppReady(ctx context.Context, event *events.Event) error {
	if event.TenantID == "" {
		return nil
	}
	var p appEventPayload
	if !c.unmarshalPayload(event, "provision.app_ready", &p) {
		return nil
	}
	ids := appIDs(p)
	if len(ids) == 0 {
		slog.Warn("provision.app_ready had no app ids — payload may be drifted",
			"tenant_id", event.TenantID, "event_id", event.ID)
		return nil
	}
	// Ensure every ready app id is in tenant.Apps — including backing
	// services (postgres/mysql/redis) that come along as dependencies
	// of user-selected apps. Without this the console Deployments tab
	// only shows user-selected apps and hides the backing services the
	// user's apps actually depend on. Observed live on tenant emrah5:
	// mysql + postgres pods running but no database cards in console
	// because tenant.Apps held only [wordpress, formbricks]. Issue #118.
	if err := c.Store.AtomicAppendApps(ctx, event.TenantID, ids, nil); err != nil {
		slog.Warn("failed to append ready app ids to tenant.Apps",
			"tenant_id", event.TenantID, "ids", ids, "error", err)
		// Non-fatal: the ClearAppState pass below still runs.
	}
	for _, id := range ids {
		if err := c.Store.ClearAppState(ctx, event.TenantID, id); err != nil {
			slog.Error("failed to clear app state", "tenant_id", event.TenantID, "app_id", id, "error", err)
			return err
		}
	}
	slog.Info("app ready", "tenant_id", event.TenantID, "app_id", p.AppID, "deploy_ids", p.DeployIDs)
	return nil
}

// onAppRemoved pulls the app from Apps and clears its AppStates entry so the
// console no longer lists it. Same malformed-payload discipline as onAppReady.
func (c *ConsumerHandler) onAppRemoved(ctx context.Context, event *events.Event) error {
	if event.TenantID == "" {
		return nil
	}
	var p appEventPayload
	if !c.unmarshalPayload(event, "provision.app_removed", &p) {
		return nil
	}
	ids := appIDs(p)
	if len(ids) == 0 {
		slog.Warn("provision.app_removed had no app ids — payload may be drifted",
			"tenant_id", event.TenantID, "event_id", event.ID)
		return nil
	}
	for _, id := range ids {
		if err := c.Store.RemoveAppFromTenant(ctx, event.TenantID, id); err != nil {
			slog.Error("failed to remove app", "tenant_id", event.TenantID, "app_id", id, "error", err)
			return err
		}
	}
	slog.Info("app removed", "tenant_id", event.TenantID, "app_id", p.AppID, "deploy_ids", p.DeployIDs)
	return nil
}

// onAppFailed marks the app state as "failed" so the console can surface it.
// Same malformed-payload discipline as onAppReady — a malformed failure event
// is still a failure we need to know about, but we can't mutate state without
// valid IDs, so we log + count and move on.
func (c *ConsumerHandler) onAppFailed(ctx context.Context, event *events.Event) error {
	if event.TenantID == "" {
		return nil
	}
	var p appEventPayload
	if !c.unmarshalPayload(event, "provision.app_failed", &p) {
		return nil
	}
	ids := appIDs(p)
	if len(ids) == 0 {
		slog.Warn("provision.app_failed had no app ids — payload may be drifted",
			"tenant_id", event.TenantID, "event_id", event.ID)
		return nil
	}
	for _, id := range ids {
		if err := c.Store.SetAppState(ctx, event.TenantID, id, "failed"); err != nil {
			slog.Error("failed to set app state=failed", "tenant_id", event.TenantID, "app_id", id, "error", err)
			return err
		}
	}
	slog.Warn("app failed", "tenant_id", event.TenantID, "app_id", p.AppID, "action", p.Action, "error", p.Error)
	return nil
}

// onTenantRemoved fires once provisioning has fully torn the tenant down
// (Flux CRs deleted, Git pruned, namespace gone). We now hard-delete the
// tenant record so the console/admin lists stop showing it.
func (c *ConsumerHandler) onTenantRemoved(ctx context.Context, event *events.Event) error {
	if event.TenantID == "" {
		return nil
	}
	if err := c.Store.DeleteTenant(ctx, event.TenantID); err != nil {
		slog.Error("failed to hard-delete tenant record", "tenant_id", event.TenantID, "error", err)
		return err
	}
	slog.Info("tenant record removed", "tenant_id", event.TenantID)
	return nil
}

// appIDs returns the set of IDs to reconcile from a payload. Prefers
// DeployIDs (newly-installed set) and falls back to AppID (single-app action).
func appIDs(p appEventPayload) []string {
	if len(p.DeployIDs) > 0 {
		return p.DeployIDs
	}
	if p.AppID != "" {
		return []string{p.AppID}
	}
	return nil
}
