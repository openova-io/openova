// organization_create.go — TBD-C16 (#1722). Closes the voucher-checkout
// → Organization-CR loop that was missing from the convergence chain.
//
// Before this file existed the flow stalled here:
//
//	voucher accept → tenant-service CreateOrg
//	   → writes Tenant row in Mongo
//	   → publishes tenant.created on `sme.tenant.events`
//	            ↓                          (DROP — no consumer)
//	provisioning consumer switch     (case "tenant.created" missing)
//	            ↓
//	organization-controller has nothing to reconcile
//	            ↓
//	no vCluster / Keycloak group / Gitea org / per-tenant HTTPRoute
//
// The handler in this file:
//
//  1. Decodes the wire payload (Tenant fields + owner_email enrichment
//     added in the publish-side companion change).
//  2. Validates the inputs the Organization CR requires (slug, plan,
//     owner email, parent domain). Missing required values publish a
//     `provision.org_create_failed` error event so operators can see
//     the stall rather than a silent drop.
//  3. POSTs the Organization CR via the cluster-scoped apiserver path
//     `/apis/orgs.openova.io/v1/organizations`. The organization-
//     controller (in core/controllers/organization) then fans out the
//     vCluster + Keycloak group + Gitea org + UserAccess CRs.
//
// Idempotency: 409 AlreadyExists is treated as benign (the same
// tenant.created may redeliver via NATS-JetStream replay or Kafka
// consumer-group rebalance). 404 cannot happen on a Create — anything
// non-201/non-409 is a hard error and surfaces as a provisioning error
// event for the UI.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 every knob flows through env →
// Handler field → CR — no hardcoded parent domain / tier / billing
// mode. The parent domain comes from Handler.TenantParentDomain (set
// by main.go from TENANT_PARENT_DOMAIN env). Tier defaults to "sme"
// (the only tier the SME-pool wizard issues vouchers for as of TBD-C16)
// and is overridable via the message payload when a corporate flow
// emits the same event later.

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/openova-io/openova/core/services/shared/events"
)

// tenantCreatedPayload is the wire shape this consumer decodes on
// `tenant.created`. #3687 (fold #3690/#3673) collapses the former
// per-service copy into the ONE canonical struct in the shared module
// (events.TenantCreatedPayload) — the same type the producer emits and
// the bootstrap-API funnel maps onto, killing the 3-way drift the #3687
// §3.1 audit measured. Kept as a local alias so existing call sites +
// tests read unchanged; the underlying type is now shared, not a copy.
type tenantCreatedPayload = events.TenantCreatedPayload

// handleTenantCreated is the consumer for `tenant.created` (subject
// `catalyst.tenant.created` on NATS, topic `sme.tenant.events` on
// Redpanda). It mints the Organization CR that organization-controller
// reconciles into the vCluster / Keycloak group / Gitea org triplet.
//
// Returning a non-nil error triggers a Nak on the broker so redelivery
// retries the create — important for transient apiserver outages. The
// 409 AlreadyExists short-circuit covers the redelivery happy path
// where the CR already exists.
func (h *Handler) handleTenantCreated(ctx context.Context, event *events.Event) error {
	var data tenantCreatedPayload
	if err := json.Unmarshal(event.Data, &data); err != nil {
		// Poison-pill payload — ack the event (return nil) so the
		// broker doesn't redeliver forever, but surface the error
		// event so the UI sees the stall.
		slog.Error("tenant.created consumer: malformed payload",
			"tenant_id", event.TenantID,
			"event_id", event.ID,
			"error", err,
		)
		h.publishEvent(ctx, "provision.org_create_failed", event.TenantID, map[string]string{
			"event_id": event.ID,
			"error":    fmt.Sprintf("malformed tenant.created payload: %v", err),
		})
		return nil
	}
	return h.createOrganizationCR(ctx, data)
}

// createOrganizationCR builds the Organization CR from the decoded
// payload, validates required fields, and POSTs it to the apiserver.
// Validation failures publish `provision.org_create_failed` and return
// nil (the event is acked — redelivery won't fix bad inputs).
// Apiserver failures other than 409 return non-nil so the broker
// redelivers.
func (h *Handler) createOrganizationCR(ctx context.Context, data tenantCreatedPayload) error {
	slug := strings.TrimSpace(data.Slug)
	if !validTenantSlug(slug) {
		return h.failOrgCreate(ctx, data.ID, slug,
			fmt.Sprintf("invalid slug %q (must match [a-z][a-z0-9-]{2,30})", slug))
	}
	if strings.TrimSpace(data.OwnerEmail) == "" {
		return h.failOrgCreate(ctx, data.ID, slug,
			"missing owner_email — cannot mint Organization CR without an owner roster entry")
	}
	// Parent domain resolution: payload override → Handler default.
	// Both may legitimately be empty on a Sovereign that hasn't opted
	// into the SME-pool flow yet. In that case the Organization CR
	// still mints (the controller skips the HTTPRoute step on empty
	// parent), so we do NOT fail here — but we log loud so operators
	// notice they're running half-configured.
	parentDomain := strings.ToLower(strings.TrimSpace(data.ParentDomain))
	if parentDomain == "" {
		parentDomain = strings.ToLower(strings.TrimSpace(h.TenantParentDomain))
	}
	if parentDomain == "" {
		slog.Warn("tenant.created consumer: no parent domain configured — Organization will mint without TenantPublic HTTPRoute",
			"slug", slug)
	}

	tier := strings.TrimSpace(data.Tier)
	if tier == "" {
		tier = "sme"
	}
	billingMode := strings.TrimSpace(data.BillingMode)
	if billingMode == "" {
		billingMode = "real"
	}
	displayName := strings.TrimSpace(data.Name)
	if displayName == "" {
		displayName = slug
	}

	// Build the cluster-scoped Organization CR. ParentDomain seeds the
	// TenantPublic block so the organization-controller's HTTPRoute
	// reconciler can render `<slug>.<parentDomain>` immediately — the
	// subsequent product-install patch via patchOrgTenantPublic adds
	// backendService once the product is Ready.
	org := map[string]any{
		"apiVersion": "orgs.openova.io/v1",
		"kind":       "Organization",
		"metadata": map[string]any{
			"name": slug,
			"labels": map[string]any{
				"openova.io/tenant-id": data.ID,
				"openova.io/source":    "tenant-created-event",
			},
		},
		"spec": map[string]any{
			"slug":         slug,
			"displayName":  displayName,
			"kind":         "customer",
			"tier":         tier,
			"billingMode":  billingMode,
			"sovereignRef": h.SovereignFQDN,
			"owners": []map[string]any{
				{
					"email": strings.TrimSpace(data.OwnerEmail),
					"role":  "owner",
				},
			},
		},
	}
	// Seed TenantPublic only when a parent domain is configured —
	// otherwise the field stays absent so the controller skips the
	// HTTPRoute render rather than emit one with an empty parent.
	if parentDomain != "" {
		org["spec"].(map[string]any)["tenantPublic"] = map[string]any{
			"parentDomain": parentDomain,
			"subdomain":    slug,
		}
	}

	payload, err := json.Marshal(org)
	if err != nil {
		return h.failOrgCreate(ctx, data.ID, slug,
			fmt.Sprintf("marshal Organization CR: %v", err))
	}

	path := "/apis/orgs.openova.io/v1/organizations"
	if _, err := h.k8sRequest("POST", path, payload); err != nil {
		// 409 AlreadyExists — same tenant.created redelivered; the
		// controller already has the CR and is reconciling.
		if strings.Contains(err.Error(), "status 409") ||
			strings.Contains(err.Error(), "AlreadyExists") {
			slog.Info("tenant.created consumer: Organization CR already exists — idempotent ack",
				"slug", slug, "tenant_id", data.ID)
			return nil
		}
		// 404 on Create implies the CRD itself is missing — operator
		// misconfiguration. Publish the error event and ACK (returning
		// nil) so the broker doesn't pin the partition retrying a
		// problem the operator must fix.
		if strings.Contains(err.Error(), "status 404") {
			return h.failOrgCreate(ctx, data.ID, slug,
				fmt.Sprintf("Organization CRD missing on cluster — install organization-controller chart: %v", err))
		}
		// Anything else (5xx, network, 401) — return non-nil so the
		// broker redelivers. Log loud so operators see the stall.
		slog.Error("tenant.created consumer: Organization CR create failed",
			"slug", slug, "tenant_id", data.ID, "error", err)
		return fmt.Errorf("create Organization %s: %w", slug, err)
	}

	slog.Info("tenant.created consumer: Organization CR created",
		"slug", slug,
		"tenant_id", data.ID,
		"parent_domain", parentDomain,
		"tier", tier,
		"sovereign", h.SovereignFQDN,
	)
	h.publishEvent(ctx, "provision.org_created", data.ID, map[string]string{
		"slug":          slug,
		"tier":          tier,
		"parent_domain": parentDomain,
		"sovereign":     h.SovereignFQDN,
	})
	return nil
}

// failOrgCreate logs + publishes a `provision.org_create_failed` event
// for visibility and returns nil so the broker acks (the input is bad
// — redelivery won't help). The slug may be empty when validation
// itself failed on the slug.
func (h *Handler) failOrgCreate(ctx context.Context, tenantID, slug, reason string) error {
	slog.Error("tenant.created consumer: Organization create rejected",
		"tenant_id", tenantID, "slug", slug, "reason", reason)
	h.publishEvent(ctx, "provision.org_create_failed", tenantID, map[string]string{
		"slug":   slug,
		"reason": reason,
	})
	return nil
}
