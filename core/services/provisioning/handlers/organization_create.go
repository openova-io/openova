// organization_create.go — TBD-C16 (#1722). Closes the voucher-checkout
// → Organization-CR loop that was missing from the convergence chain.
//
// Before this file existed the flow stalled here:
//
//	voucher accept → tenant-service CreateOrg
//	   → writes Tenant row in Mongo
//	   → publishes tenant.created on `org.tenant.events`
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
// Idempotency: 409 AlreadyExists is benign only when the existing
// Organization is this tenant's (the same tenant.created may redeliver via
// NATS-JetStream replay). A name held by another tenant's Organization is
// refused; one held by an Organization being removed is redelivered until
// the name is free, and reported on the last delivery
// (organizationAlreadyExists). 400/422 (the Organization
// kind refuses the object) and 404 (no CRD) are permanent and surface as a
// provisioning error event; anything else is redelivered.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 every knob flows through env →
// Handler field → CR — no hardcoded parent domain / tier / billing
// mode. The parent domain comes from Handler.TenantParentDomain (set
// by main.go from TENANT_PARENT_DOMAIN env). Tier defaults to "org"
// (the only tier the Organization-pool wizard issues vouchers for as of TBD-C16)
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
// `catalyst.tenant.created` on NATS, topic `org.tenant.events` on
// Redpanda). It mints the Organization CR that organization-controller
// reconciles into the vCluster / Keycloak group / Gitea org triplet.
//
// Returning a non-nil error triggers a Nak on the broker so redelivery
// retries the create — important for transient apiserver outages. A 409
// AlreadyExists is acked only when the existing CR is this tenant's (the
// redelivery happy path); see organizationAlreadyExists.
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
	return h.createOrganizationCROnDelivery(ctx, data, events.IsFinalDelivery(event))
}

// createOrganizationCR builds the Organization CR from the decoded
// payload, validates required fields, and POSTs it to the apiserver.
// Validation failures publish `provision.org_create_failed` and return
// nil (the event is acked — redelivery won't fix bad inputs).
// Apiserver failures other than 409 return non-nil so the broker
// redelivers.
func (h *Handler) createOrganizationCR(ctx context.Context, data tenantCreatedPayload) error {
	return h.createOrganizationCROnDelivery(ctx, data, false)
}

// createOrganizationCROnDelivery is createOrganizationCR for one delivery of
// tenant.created. final is the last delivery JetStream allows
// (events.IsFinalDelivery): an error returned on it is never redelivered, so
// the two waits below that answer "not yet" decide on it instead — a catalog
// that still cannot name the plan creates the Organization at the logged "s"
// (the floor before the wait existed), and a name still held by an
// Organization being removed is reported. Either way the order's timeline,
// which waits for its own Organization (awaitOwnOrganization), ends the order
// visibly with provision.failed if no Organization of its own appears.
func (h *Handler) createOrganizationCROnDelivery(ctx context.Context, data tenantCreatedPayload, final bool) error {
	slug := strings.TrimSpace(data.Slug)
	if !validTenantSlug(slug) {
		return h.failOrgCreate(ctx, data.ID, slug,
			fmt.Sprintf("invalid slug %q (must match [a-z][a-z0-9-]{2,30})", slug))
	}
	if strings.TrimSpace(data.OwnerEmail) == "" {
		return h.failOrgCreate(ctx, data.ID, slug,
			"missing owner_email — cannot mint Organization CR without an owner roster entry")
	}
	// Parent domain resolution (#4999) — see resolveOrgParentDomain. The Org CR's
	// pool == the pool the per-Org apps-HTTPRoute renders under (the apps
	// generator follows the SAME resolved zone via consumer.go's scoped clone),
	// so honoring the customer's served pick keeps console==apps under the chosen
	// TLD instead of collapsing both onto the primary apps pool.
	parentDomain := resolveOrgParentDomain(h.AppsParentDomain, h.PoolDomains, data.ParentDomain)
	if parentDomain == "" {
		slog.Warn("tenant.created consumer: no parent domain configured — Organization will mint without TenantPublic HTTPRoute",
			"slug", slug)
	} else if pd := strings.ToLower(strings.TrimSpace(data.ParentDomain)); pd != "" && pd != parentDomain {
		// The customer's funnel pick is NOT a served pool zone → we fell back to
		// the apps pool (never provision under a zone the Sovereign can't serve,
		// the #4421 dead-IP guard). Log loud so an operator notices the funnel is
		// offering a pool the Sovereign doesn't serve (fix the offer or add the
		// zone to TENANT_POOL_DOMAINS).
		slog.Warn("tenant.created consumer: payload parent_domain is not a served pool zone — fell back to the Sovereign apps pool (#4999)",
			"slug", slug, "payload_parent_domain", pd, "resolved_parent_domain", parentDomain)
	}

	tier := strings.TrimSpace(data.Tier)
	if tier == "" {
		tier = "org"
	}
	// Resolve the purchased plan identifier → canonical slug (s|m|l|xl|flexi) so
	// the Organization CR carries the SINGLE truth-source for the resource cap
	// the org-controller materializes (Workstream B, #4292). The funnel posts
	// the slug directly and the BSS door posts a plan UUID; resolvePlanSlug
	// handles BOTH (#4473) — a known slug short-circuits, a UUID is looked up in
	// the catalog, and only a genuinely-unknown value falls back to a LOGGED "s"
	// default. Before #4473 the funnel slug fell through to "s" unconditionally,
	// downgrading every paid funnel Org to the S boundary.
	//
	// The plan is written ONCE, here, and nothing corrects spec.planSlug later
	// (resolveTenantPlanSlug trusts it as authoritative). So a catalog that
	// cannot answer is a reason to redeliver, never to stamp a guessed "s" on a
	// paid Organization.
	planSlug, err := h.resolveCreationPlanSlug(ctx, data.PlanID, data.PackageSKU)
	switch {
	case err != nil && final:
		// The last delivery: a redelivery is not coming, and acking with no
		// Organization drops the paid order. Create it at the logged "s".
		slog.Error("tenant.created consumer: plan still not resolvable on the last delivery — creating the Organization at plan 's'",
			"slug", slug, "tenant_id", data.ID, "plan_id", data.PlanID, "error", err)
		planSlug = "s"
	case err != nil:
		slog.Warn("tenant.created consumer: plan not resolvable yet — redelivering instead of guessing",
			"slug", slug, "tenant_id", data.ID, "plan_id", data.PlanID, "error", err)
		return err
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
			"planSlug":     planSlug,
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
	// #6971 item 8 — the purchase the Organization was created from. Only
	// when the payload carries one: a legacy producer (or the sovereign-admin
	// door, which mints without an order) leaves spec.commerce absent, so the
	// CR is byte-identical to before.
	if block, ok := organizationCommerceBlock(data); ok {
		org["spec"].(map[string]any)["commerce"] = block
	}

	payload, err := json.Marshal(org)
	if err != nil {
		return h.failOrgCreate(ctx, data.ID, slug,
			fmt.Sprintf("marshal Organization CR: %v", err))
	}

	path := "/apis/orgs.openova.io/v1/organizations"
	if _, err := h.k8sRequest("POST", path, payload); err != nil {
		// 409 AlreadyExists — an Organization of this name exists. That is a
		// redelivery ONLY when it is this tenant's: a reused name (the
		// previous customer's Organization still present, or still
		// Terminating) must never be acked as one.
		if strings.Contains(err.Error(), "status 409") ||
			strings.Contains(err.Error(), "AlreadyExists") {
			return h.organizationAlreadyExists(ctx, data.ID, slug, final)
		}
		// 404 on Create implies the CRD itself is missing — operator
		// misconfiguration. Publish the error event and ACK (returning
		// nil) so the broker doesn't pin the partition retrying a
		// problem the operator must fix.
		if strings.Contains(err.Error(), "status 404") {
			return h.failOrgCreate(ctx, data.ID, slug,
				fmt.Sprintf("Organization CRD missing on cluster — install organization-controller chart: %v", err))
		}
		// 400 / 422 — the apiserver refused the object itself (the
		// Organization kind's schema: a reserved slug, a name over its
		// maxLength). The same object can never be accepted, so a
		// redelivery only burns MaxDeliver and then drops the paid order
		// with no failure event. Report it and ACK.
		if strings.Contains(err.Error(), "status 422") ||
			strings.Contains(err.Error(), "status 400") {
			return h.failOrgCreate(ctx, data.ID, slug,
				fmt.Sprintf("the Organization was refused by the cluster and can never be created as requested: %v", err))
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
		"plan_slug", planSlug,
		"package_sku", data.PackageSKU,
		"order_id", data.OrderID,
		"sovereign", h.SovereignFQDN,
	)
	h.publishEvent(ctx, "provision.org_created", data.ID, map[string]string{
		"slug":          slug,
		"tier":          tier,
		"plan_slug":     planSlug,
		"package_sku":   strings.ToLower(strings.TrimSpace(data.PackageSKU)),
		"order_id":      strings.TrimSpace(data.OrderID),
		"parent_domain": parentDomain,
		"sovereign":     h.SovereignFQDN,
	})
	return nil
}

// organizationCommerceBlock renders the payload's purchase (#6971 item 8)
// as the Organization CR's `spec.commerce` object — the field paths the
// chargeback OpenOva adapter reads:
//
//	packageSKU   ← package_sku   (lower-cased, trimmed)
//	addons[]     ← addons        (events.BSSAddonSKUs: BSS SKUs only, no catalog ids)
//	priceSource  ← price_source  (trimmed)
//	orderID      ← order_id      (trimmed)
//	overageMode  ← overage_mode  ("capped" | "grow"; anything else dropped)
//	growCeiling  ← grow_ceiling  ({vcpu, memoryGB, diskGB, bandwidthMbps}, non-zero only)
//	spendLimitMonth ← spend_limit_month (trimmed)
//	prepaidAmount ← prepaid_amount (trimmed; what the checkout collected for
//	                the package and its BSS add-ons, credited by BSS on the
//	                first statement)
//
// Returns ok=false when nothing is set, so the caller leaves the block
// absent instead of minting `commerce: {}`. Pure, so the CR shape is
// unit-tested without an apiserver (k8sRequest is env-gated).
func organizationCommerceBlock(data tenantCreatedPayload) (map[string]any, bool) {
	// Normalise through the shared helper so the consumer and every producer
	// agree on exactly one shape (WithCommerce is the producer-side twin).
	p := events.TenantCreatedPayload{}.WithCommerce(events.TenantCommerce{
		PackageSKU:  data.PackageSKU,
		Addons:      data.Addons,
		PriceSource: data.PriceSource,
		OrderID:     data.OrderID,
		// Overage (founder model 2026-10-10).
		OverageMode:     data.OverageMode,
		GrowCeiling:     data.GrowCeiling,
		SpendLimitMonth: data.SpendLimitMonth,
		PrepaidAmount:   data.PrepaidAmount,
	})
	if !p.HasCommerce() {
		return nil, false
	}
	block := map[string]any{}
	if p.OverageMode != "" {
		block["overageMode"] = p.OverageMode
	}
	if !p.GrowCeiling.IsZero() {
		ceiling := map[string]any{}
		for _, d := range []struct {
			key string
			v   float64
		}{
			{"vcpu", p.GrowCeiling.VCPU},
			{"memoryGB", p.GrowCeiling.MemoryGB},
			{"diskGB", p.GrowCeiling.DiskGB},
			{"bandwidthMbps", p.GrowCeiling.BandwidthMbps},
		} {
			if d.v != 0 {
				ceiling[d.key] = d.v
			}
		}
		block["growCeiling"] = ceiling
	}
	if p.SpendLimitMonth != "" {
		block["spendLimitMonth"] = p.SpendLimitMonth
	}
	if p.PrepaidAmount != "" {
		block["prepaidAmount"] = p.PrepaidAmount
	}
	if p.PackageSKU != "" {
		block["packageSKU"] = p.PackageSKU
	}
	if len(p.Addons) > 0 {
		block["addons"] = p.Addons
	}
	if p.PriceSource != "" {
		block["priceSource"] = p.PriceSource
	}
	if p.OrderID != "" {
		block["orderID"] = p.OrderID
	}
	return block, true
}

// resolveOrgParentDomain picks the org-pool parent zone the Organization CR's
// spec.tenantPublic.parentDomain is stamped with (#4999, superseding #4421).
// Returns lowercased/trimmed. It MIRRORS the tenant-service resolver of the same
// name so both doors + the apps generator agree on ONE zone per Org.
//
// THE INVARIANT (#4421) it upholds: the pool the org-controller's DNS writer
// (tenant_dns.go) + console route (tenant_route.go) key off MUST equal the pool
// the per-Org apps-HTTPRoute generator renders product hosts under — else the
// per-Org A-records land in one zone while `<app>.<slug>.<otherZone>` has no
// record and dead-IPs on a stale apex wildcard. #4999 keeps that invariant but
// no longer COLLAPSES both onto the single apps pool: it HONORS the customer's
// served funnel pick (payloadParentDomain ∈ poolDomains) and the apps generator
// follows the SAME resolved zone (consumer.go scoped clone), so the two Orgs can
// live on two different served TLDs.
//
// A pick outside the served set (or empty) falls back to appsParentDomain (the
// EFFECTIVE pool, gitops.ResolveParentDomain(TENANT_PARENT_DOMAIN), non-empty on
// any real Sovereign) so we NEVER provision under a zone the Sovereign can't
// serve. Only a degenerate Sovereign with no apps pool wired falls through to the
// raw pick. poolDomains defaults to the canonical four .omani.X zones when unset.
func resolveOrgParentDomain(appsParentDomain string, poolDomains []string, payloadParentDomain string) string {
	pick := normalizePoolZone(payloadParentDomain)
	if pick != "" && isServedPoolDomain(pick, poolDomains) {
		return pick
	}
	if primary := normalizePoolZone(appsParentDomain); primary != "" {
		return primary
	}
	return pick
}

// orgOwnership is whose Organization an existing `<slug>` CR is, as seen by
// one tenant.
type orgOwnership int

const (
	// orgOwnedByTenant — labelled with this tenant's id and not being removed.
	orgOwnedByTenant orgOwnership = iota
	// orgTerminating — being removed (metadata.deletionTimestamp set),
	// whoever owned it. The name frees itself once its finalizers finish.
	orgTerminating
	// orgOwnedByOther — another tenant's Organization (or one that carries no
	// tenant label at all): never this order's.
	orgOwnedByOther
	// orgUnreadable — the body is not an Organization.
	orgUnreadable
)

// organizationOwnership decides whose Organization the CR in body is, from the
// `openova.io/tenant-id` label createOrganizationCR (and the catalyst-api door)
// stamps on every Organization it mints, and from metadata.deletionTimestamp.
// Pure, so the creation path (organizationAlreadyExists), the order's timeline
// and the per-Org commit path (both through checkOwnOrganization) ask one
// question.
func organizationOwnership(body []byte, tenantID string) orgOwnership {
	var org struct {
		Metadata struct {
			Labels            map[string]string `json:"labels"`
			DeletionTimestamp *string           `json:"deletionTimestamp"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &org); err != nil {
		return orgUnreadable
	}
	if org.Metadata.DeletionTimestamp != nil && strings.TrimSpace(*org.Metadata.DeletionTimestamp) != "" {
		return orgTerminating
	}
	owner := strings.TrimSpace(org.Metadata.Labels["openova.io/tenant-id"])
	if owner == "" || owner != strings.TrimSpace(tenantID) {
		return orgOwnedByOther
	}
	return orgOwnedByTenant
}

// organizationAlreadyExists handles a 409 on the Organization POST by reading
// the existing CR and asking whose it is:
//
//   - this tenant's: the same tenant.created redelivered — ack.
//   - Terminating: the previous holder of the name is being removed —
//     redeliver (non-nil), so this order creates ITS OWN Organization once the
//     name is free, instead of being acked onto one that is about to vanish.
//   - another tenant's: refuse loudly (provision.org_create_failed) and ack.
//     Acking it as a redelivery is what bound a new customer to the previous
//     customer's Organization, its vCluster, its repo and its owner roster.
//
// A read failure is transient: redeliver. On the final delivery (no
// redelivery is coming) a Terminating or unreadable holder is reported and
// acked instead of being returned into a drop; the order's timeline then fails
// the order when no Organization of its own appears.
func (h *Handler) organizationAlreadyExists(ctx context.Context, tenantID, slug string, final bool) error {
	notYet := func(err error) error {
		if final {
			return h.failOrgCreate(ctx, tenantID, slug,
				fmt.Sprintf("no Organization could be created for this order by its last delivery: %v", err))
		}
		return err
	}
	body, err := h.k8sGet("/apis/orgs.openova.io/v1/organizations/" + slug)
	if err != nil {
		// 404 here means it vanished between the POST and this read; the
		// redelivery will create it.
		return notYet(fmt.Errorf("the Organization %s exists but could not be read to check whose it is: %w", slug, err))
	}
	switch organizationOwnership(body, tenantID) {
	case orgOwnedByTenant:
		slog.Info("tenant.created consumer: this tenant's Organization CR already exists — idempotent ack",
			"slug", slug, "tenant_id", tenantID)
		return nil
	case orgTerminating:
		slog.Warn("tenant.created consumer: the name is held by an Organization being removed — redelivering until it is gone",
			"slug", slug, "tenant_id", tenantID, "final_delivery", final)
		return notYet(fmt.Errorf("the Organization %s is being removed; this order's Organization is created once it is gone", slug))
	case orgOwnedByOther:
		slog.Error("tenant.created consumer: the name belongs to another tenant's Organization — refusing the order",
			"slug", slug, "tenant_id", tenantID, "existing_body_bytes", len(body))
		return h.failOrgCreate(ctx, tenantID, slug,
			fmt.Sprintf("the name %q is held by another Organization — this order was not bound to it", slug))
	default:
		return notYet(fmt.Errorf("the Organization %s exists but its object could not be decoded", slug))
	}
}

// resolveCreationPlanSlug resolves the plan the Organization is created with.
// Unlike resolvePlanSlug it does not turn a catalog that could not answer into
// "s": the creation path writes spec.planSlug once and every later reader
// trusts it, so a guess here is a paid Organization capped at S for good.
// When the catalog could not answer, the order's own BSS package sku
// ("plan.<slug>", the form the storefront and BSS write) names the paid plan;
// only an order carrying neither returns an error (the event is redelivered).
//
// Two cases keep the historical, logged "s": no catalog configured (a
// redelivery cannot change that) and a catalog that ANSWERED without the plan
// (a real answer, not an outage).
func (h *Handler) resolveCreationPlanSlug(ctx context.Context, planID, packageSKU string) (string, error) {
	if strings.TrimSpace(h.CatalogURL) == "" {
		return h.resolvePlanSlug(ctx, planID), nil
	}
	slug, reachable := h.lookupPlanSlug(ctx, planID)
	if !reachable {
		if s, ok := planSlugFromPackageSKU(packageSKU); ok {
			slog.Warn("tenant.created consumer: catalog could not resolve plan_id — the plan is taken from the order's package sku",
				"plan_id", planID, "package_sku", packageSKU, "plan", s)
			return s, nil
		}
		return "", fmt.Errorf("catalog %s could not resolve plan_id %q and the order carries no package sku naming the plan", h.CatalogURL, planID)
	}
	if _, known := isKnownPlanSlug(planID); !known && slug == "s" {
		slog.Warn("tenant.created consumer: the catalog does not know plan_id — the Organization is created at plan 's'",
			"plan_id", planID)
	}
	return slug, nil
}

// planSlugFromPackageSKU reads the plan out of a BSS package sku: "plan.l" →
// "l". Only the "plan." prefix and a canonical plan slug are accepted.
func planSlugFromPackageSKU(sku string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(sku)), "plan.")
	if !ok {
		return "", false
	}
	return isKnownPlanSlug(rest)
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
