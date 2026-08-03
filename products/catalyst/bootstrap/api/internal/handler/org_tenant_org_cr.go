// org_tenant_org_cr.go — #3687 master root. The Organization funnel
// (POST /api/v1/org/tenants → runOrgTenantPipeline) provisioned a
// vCluster GitOps overlay + DNS + Keycloak + a tenant-registry row, but
// NEVER minted the canonical Organization CR (orgs.openova.io/v1). On a
// fresh converged Sovereign that left `kubectl get organizations -A` = 0
// — the dead object-model the founder named the master root: every
// operator day-2 surface (Dashboard / Showback / Users) keys off the
// Organization CR, and the org-controller has nothing to reconcile into
// the per-Org vCluster / Keycloak group / Gitea org.
//
// The marketplace SPA door (core/services/tenant CreateOrg → tenant.created
// → core/services/provisioning createOrganizationCR) already POSTs this CR
// over the NATS/JetStream events.Event envelope. The catalyst-api BSS door
// (this funnel) did NOT — and the catalyst-api's existing tenant-event
// publisher (internal/natspub) emits a BARE TenantEvent over CORE NATS,
// which the provisioning-service's JetStream events.Event MultiSubscriber
// does not consume. So bridging via that publisher would silently drop.
//
// This file closes the gap the deterministic way: at the
// STSTenantRegistered → STSDone transition the handler POSTs the
// Organization CR DIRECTLY via its in-cluster dynamic client, mirroring
// core/services/provisioning/handlers/organization_create.go's
// createOrganizationCR exactly (same GVR, same spec shape the org-
// controller reconciles, same 409-AlreadyExists idempotency). No cross-
// service event hop, no JetStream dependency — the CR materialises within
// the same request the Organization pipeline runs in, so `kubectl get
// organizations -A` is ≥1 right after the funnel completes.
//
// Mirrors the established in-handler dynamic-client idiom (namespace_ensure.go's
// ensureOrgNamespace): a free helper takes a dynamic.Interface, builds an
// unstructured CR, and Creates idempotently. The handler resolves the
// client via sovereignDepsFor() (the same seam the /api/v1/sovereign/*
// console views use; test-overridable via SetSovereignDepsFactory).

package handler

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// organizationGVR is the cluster-scoped Organization CR the org-controller
// reconciles. MUST match core/controllers/organization (orgapi/types.go is
// registered cluster-scoped) and the provisioning consumer's POST path
// `/apis/orgs.openova.io/v1/organizations`.
func organizationGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "orgs.openova.io", Version: "v1", Resource: "organizations"}
}

// orgSlugRE mirrors the provisioning-side guard (organization_create.go's
// validTenantSlug) AND the tenant-service input boundary: a slug becomes the
// Organization name, the vCluster name, the Keycloak group leaf and the
// Gitea Org name, so it must be a DNS-/namespace-/repo-safe 3-31 token
// starting with a letter. The Organization funnel's validSubdomain accepts RFC-1123
// labels that this rejects (digit-leading, 1-2 chars), so we re-check here
// and skip Org-CR creation (loud warn, pipeline NOT failed) rather than POST
// a CR the org-controller's CEL validation would reject.
var orgSlugRE = regexp.MustCompile(`^[a-z][a-z0-9-]{2,30}$`)

// createOrgOrganizationCR mints the Organization CR for a completed Organization
// provision record. Best-effort: a failure is logged loud but does NOT fail
// the Organization pipeline — the org-controller's level-triggered reconcile + the
// pipeline's own STSDone idempotency mean a transiently-failed create is
// retried on the next reconcile pass, and the vCluster/DNS/Keycloak the
// pipeline already provisioned stay valid. Returns nothing (the caller
// treats Org-CR creation as a non-gating finalisation step), but surfaces
// the outcome via structured logs.
//
// The dynamic client is resolved via sovereignDepsFor() — nil-tolerant: when
// the in-cluster config is unavailable (CI / out-of-cluster catalyst-api) the
// create is skipped with an info log, matching every other dynamic-client
// path in this package.
func (h *Handler) createOrgOrganizationCR(ctx context.Context, rec store.OrganizationProvisionRecord) {
	slug := strings.ToLower(strings.TrimSpace(rec.Subdomain))
	if !orgSlugRE.MatchString(slug) {
		h.log.Warn("org-tenant: skipping Organization CR — subdomain is not a valid Org slug",
			"subdomain", rec.Subdomain,
			"expected", "[a-z][a-z0-9-]{2,30}",
			"org_tenant_id", rec.OrganizationID,
		)
		return
	}

	deps, err := h.sovereignDepsFor()
	if err != nil || deps == nil || deps.dyn == nil {
		// Out-of-cluster / CI: the Organization pipeline still completes; the Org CR
		// is simply not minted (no apiserver to POST to). On a real Sovereign
		// this branch never fires.
		h.log.Info("org-tenant: Organization CR skipped — no in-cluster dynamic client",
			"org_tenant_id", rec.OrganizationID, "err", err)
		return
	}

	if err := ensureOrganizationCR(ctx, deps.dyn, rec, h.orgTenantDeps.OTECHFQDN); err != nil {
		h.log.Error("org-tenant: Organization CR create failed — org-controller reconcile will retry",
			"slug", slug, "org_tenant_id", rec.OrganizationID, "err", err)
		return
	}
	h.log.Info("org-tenant: Organization CR ensured",
		"slug", slug, "org_tenant_id", rec.OrganizationID,
		"kind", rec.Kind, "tier", rec.Tier, "billing_mode", rec.BillingMode,
		"sovereign", h.orgTenantDeps.OTECHFQDN,
	)
}

// deleteOrgOrganizationCR deletes the canonical Organization CR for a
// record being torn down — THE trigger for the org-controller's finalizer
// cascade (#5426). The complete teardown
// (core/controllers/organization/internal/controller/organization_controller.go
// deletion-timestamp branch: publishTenantDeleted → teardownPerOrgFlux →
// teardownTenantNetworking → iac-bootstrap → per-org-realm finalizers) only
// runs when the CR is marked for deletion; before this helper existed the
// REST DELETE reaped the GitOps overlay + registry + DNS but never touched
// the CR, so the `orgs.openova.io/tenant-networking` finalizer never fired
// and every console-deleted Organization leaked its namespace, vCluster,
// Keycloak realm, per-Org Flux sources and gateway listeners (the exact
// orphan set that holds the sovereignty-cutover pre-flight closed).
//
// MUST be called AFTER the GitOps overlay reap — #4459/R17 order: deleting
// the CR while the org-tenants tree still holds the overlay lets Flux
// recreate the namespace the cascade just tore down (observed live at
// T+5m07s on hw290). With the overlay commit already pushed, anything a
// stale artifact transiently re-applies is pruned on the next source fetch,
// and the cascade itself removes the per-Org Flux sources.
//
// Best-effort like the sibling teardown steps, but a failure is logged at
// Error level (a silent skip IS the #5426 bug): the CR survives, so
// re-running the DELETE — every step of which is idempotent — retries the
// cascade trigger. NotFound is success (kubectl-side delete or a prior
// DELETE already triggered / completed the cascade). Nil-tolerant on the
// dynamic client for CI / out-of-cluster, matching createOrgOrganizationCR.
func (h *Handler) deleteOrgOrganizationCR(ctx context.Context, rec store.OrganizationProvisionRecord) {
	slug := strings.ToLower(strings.TrimSpace(rec.Subdomain))
	if !orgSlugRE.MatchString(slug) {
		// createOrgOrganizationCR never mints a CR for an invalid slug, so
		// there is nothing to cascade.
		h.log.Warn("org-tenant: skipping Organization CR delete — subdomain is not a valid Org slug",
			"subdomain", rec.Subdomain,
			"org_tenant_id", rec.OrganizationID,
		)
		return
	}

	deps, err := h.sovereignDepsFor()
	if err != nil || deps == nil || deps.dyn == nil {
		h.log.Info("org-tenant: Organization CR delete skipped — no in-cluster dynamic client",
			"org_tenant_id", rec.OrganizationID, "err", err)
		return
	}

	if err := deps.dyn.Resource(organizationGVR()).Delete(ctx, slug, metav1.DeleteOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			// Already gone — idempotent success.
			return
		}
		h.log.Error("org-tenant: Organization CR delete FAILED — finalizer cascade NOT triggered; namespace/vCluster/realm/Flux sources leak until this DELETE is retried (#5426)",
			"slug", slug, "org_tenant_id", rec.OrganizationID, "err", err)
		return
	}
	h.log.Info("org-tenant: Organization CR deleted — org-controller finalizer cascade triggered",
		"slug", slug, "org_tenant_id", rec.OrganizationID)
}

// ensureOrganizationCR builds the Organization CR from the Organization record
// and POSTs it via the dynamic client, treating AlreadyExists as success
// (idempotent — a pipeline re-run or a redelivered create lands on the same
// named CR). The spec shape mirrors
// core/controllers/organization/internal/orgapi.OrganizationSpec verbatim so
// the org-controller reconciles it into the vCluster + Keycloak group +
// Gitea org + UserAccess fan-out.
//
// sovereignFQDN seeds spec.sovereignRef (the Sovereign hosting the Org) — the
// Organization pipeline's OTECHFQDN. The orgShape fields (Kind/Tier/BillingMode) the
// funnel already resolved + stamped onto the record carry straight through;
// empties default the same way the provisioning consumer defaults them
// (kind→customer, tier→org, billingMode→real) so every door mints an
// identical shape.
func ensureOrganizationCR(ctx context.Context, dyn dynamic.Interface, rec store.OrganizationProvisionRecord, sovereignFQDN string) error {
	slug := strings.ToLower(strings.TrimSpace(rec.Subdomain))
	if !orgSlugRE.MatchString(slug) {
		return fmt.Errorf("ensureOrganizationCR: invalid slug %q", slug)
	}

	displayName := strings.TrimSpace(rec.CompanyName)
	if displayName == "" {
		displayName = slug
	}
	kind := strings.ToLower(strings.TrimSpace(rec.Kind))
	if kind != "internal" && kind != "customer" {
		kind = "customer"
	}
	tier := strings.ToLower(strings.TrimSpace(rec.Tier))
	if tier == "" {
		tier = "org"
	}
	billingMode := strings.ToLower(strings.TrimSpace(rec.BillingMode))
	if billingMode == "" {
		billingMode = "real"
	}
	// Purchased plan slug → spec.planSlug (#4292). Empty / unknown defaults to
	// "s" so the org-controller always materializes a cap rather than running
	// the boundary namespace uncapped.
	planSlug := strings.ToLower(strings.TrimSpace(rec.PlanSlug))
	switch planSlug {
	case "s", "m", "l", "xl", "flexi":
	default:
		planSlug = "s"
	}
	parentDomain := strings.ToLower(strings.TrimSpace(rec.ParentDomain))

	spec := map[string]any{
		"slug":         slug,
		"displayName":  displayName,
		"kind":         kind,
		"tier":         tier,
		"planSlug":     planSlug,
		"billingMode":  billingMode,
		"sovereignRef": strings.TrimSpace(sovereignFQDN),
		"owners": []any{
			map[string]any{
				"email": strings.TrimSpace(rec.AdminEmail),
				"role":  "owner",
			},
		},
	}
	// Seed TenantPublic only when a parent zone is configured so the
	// org-controller renders the `<slug>.<parentDomain>` HTTPRoute; absent
	// parent → the field stays unset and the controller skips the route
	// (mirrors organization_create.go's guard).
	if parentDomain != "" {
		spec["tenantPublic"] = map[string]any{
			"parentDomain": parentDomain,
			"subdomain":    slug,
		}
	}

	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "orgs.openova.io/v1",
			"kind":       "Organization",
			"metadata": map[string]any{
				"name": slug,
				"labels": map[string]any{
					"openova.io/tenant-id":         rec.OrganizationID,
					"openova.io/source":            "org-tenant-funnel",
					"app.kubernetes.io/managed-by": "catalyst-api",
				},
			},
			"spec": spec,
		},
	}

	if _, err := dyn.Resource(organizationGVR()).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Same slug re-provisioned / pipeline re-run: the CR already
			// exists and the org-controller is reconciling it. Success.
			return nil
		}
		return fmt.Errorf("create Organization %q: %w", slug, err)
	}
	return nil
}
