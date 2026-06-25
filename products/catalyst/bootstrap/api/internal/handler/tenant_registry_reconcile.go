// tenant_registry_reconcile.go — reconcile the tenant registry from
// Organization CRs (issue #4179, criterion 4d — the SIGNED-IN landing).
//
// THE GAP this file closes (the #4179 FINAL layer):
//
// A fresh marketplace signup's customer is redirected to their console host
// `console.<slug>.<parentDomain>` (#4184), which now RESOLVES (#4236) and
// presents a SAN-matching TLS cert (#4242). But landing SIGNED-IN failed:
// /auth/org-handover (org_handover.go) → resolveOrgScope (org_scope.go)
// validates the request host against the tenant registry and REFUSES any host
// it doesn't know as a tenant_kind=org console — returning
// `invalid audience: not an org console host`. The customer saw the login page
// but could never get a session.
//
// WHY the registry was empty for funnel Orgs:
//   - The BSS door (organization_provisioning.go Step 6 / org_tenant_org_cr.go)
//     WRITES the TenantRegistration at STSTenantRegistered.
//   - The MARKETPLACE funnel door (tenant-service POST /api/tenant/orgs →
//     tenant.created → provisioning-service createOrganizationCR) mints the
//     Organization CR + DNS/TLS/route via the org-controller but NEVER runs the
//     catalyst-api provisioning pipeline, so the registry row was never written.
//
// THE FIX (single source of truth, covers BOTH doors): the Organization CR is
// the one object both doors create. catalyst-api OWNS the flat-file tenant
// registry (its private PVC at CATALYST_DEPLOYMENTS_DIR/-tenant-registry.json —
// the org-controller, a separate process, can't write it). So catalyst-api
// reconciles its OWN registry FROM the Organization CRs: list every Org CR,
// register `console.<slug>.<parentDomain> → {orgID, Org}`, idempotently. This
// is the same single-source consolidation #4236 (DNS) + #4242 (TLS) applied to
// registration — write it where the reconciler already converges every Org,
// regardless of which door minted it.
//
// The Put is idempotent (upsert keyed by host); a steady-state re-reconcile is a
// no-op. Best-effort: a transient apiserver/list failure is logged and retried
// on the next tick — it never crashes the API. Out-of-cluster / CI (no dynamic
// client) is a logged skip, matching every other dynamic-client path here.

package handler

import (
	"context"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// tenantRegistryReconcileInterval is the steady-state cadence at which
// catalyst-api re-syncs its tenant registry from the Organization CRs. A new
// funnel Org becomes session-capable within one interval of its CR landing.
// 60s matches the org-controller's own per-Org Flux interval — fast enough that
// a fresh signup lands signed-in promptly, cheap enough that a List of a
// few-thousand-Org cluster is negligible.
const tenantRegistryReconcileInterval = 60 * time.Second

// ReconcileTenantRegistryFromOrgCRs runs the registry reconcile loop until ctx
// is cancelled. It performs one sync immediately (so a restart re-registers
// every existing funnel Org before serving the first handover) then re-syncs on
// a ticker. Wired from main.go as a background goroutine, mirroring
// SelfHealSecondaryKubeconfigsOnDisk's async-startup idiom.
//
// No-op (returns immediately) when the tenant registry isn't wired — there is
// nothing to reconcile into.
func (h *Handler) ReconcileTenantRegistryFromOrgCRs(ctx context.Context) {
	if h == nil || h.tenantRegistry == nil {
		return
	}
	h.reconcileTenantRegistryOnce(ctx)
	ticker := time.NewTicker(tenantRegistryReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.reconcileTenantRegistryOnce(ctx)
		}
	}
}

// reconcileTenantRegistryOnce lists every Organization CR and registers each
// Org's console host into the tenant registry. Idempotent + best-effort:
// per-Org failures are logged and skipped so one malformed CR never starves the
// rest; a List failure is logged and the next tick retries.
func (h *Handler) reconcileTenantRegistryOnce(ctx context.Context) {
	if h.tenantRegistry == nil {
		return
	}
	deps, err := h.sovereignDepsFor()
	if err != nil || deps == nil || deps.dyn == nil {
		// Out-of-cluster / CI: no apiserver to list from. The registry keeps
		// whatever the BSS pipeline already wrote; the funnel-Org rows simply
		// aren't synced here. On a real Sovereign this branch never fires.
		h.log.Info("tenant-registry-reconcile: skipped — no in-cluster dynamic client",
			"err", err)
		return
	}

	list, err := deps.dyn.Resource(organizationGVR()).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		h.log.Warn("tenant-registry-reconcile: list Organizations failed — will retry next tick",
			"err", err)
		return
	}

	sovereignFQDN := strings.TrimSpace(h.orgTenantDeps.OTECHFQDN)
	registered, skipped := 0, 0
	for i := range list.Items {
		reg, ok := tenantRegistrationFromOrgCR(&list.Items[i], sovereignFQDN)
		if !ok {
			skipped++
			continue
		}
		// Skip the write when the registry already has an identical row for
		// this host so a steady-state reconcile produces zero file churn (Put
		// rewrites the whole flat file). A registry row only changes when the
		// host first appears or its realm/namespace fields change.
		if cur, found := h.tenantRegistry.Get(reg.Host); found && tenantRegistrationEqual(cur, reg) {
			continue
		}
		if err := h.tenantRegistry.Put(reg); err != nil {
			h.log.Warn("tenant-registry-reconcile: Put failed for Org console host — will retry next tick",
				"host", reg.Host, "tenant_id", reg.TenantID, "err", err)
			skipped++
			continue
		}
		registered++
		h.log.Info("tenant-registry-reconcile: registered Org console host",
			"host", reg.Host, "tenant_id", reg.TenantID)
	}
	if registered > 0 {
		h.log.Info("tenant-registry-reconcile: pass complete",
			"orgs", len(list.Items), "registered", registered, "skipped", skipped)
	}
}

// ensureTenantRegisteredForHost performs an ON-DEMAND, single-host registry
// sync: it lists Organization CRs, finds the one whose pool console host
// equals `host`, and registers it immediately — without waiting for the next
// 60s ReconcileTenantRegistryFromOrgCRs tick.
//
// THE RACE this closes (the #3376 terminal "signed-in" gate): a marketplace
// funnel stranger completes checkout, the Organization CR lands, and the
// marketplace IMMEDIATELY redirects the browser to
// `console.<slug>.<pool>/auth/org-handover?token=…`. The funnel door never
// runs the BSS provisioning pipeline (which writes the registry synchronously
// at Step 6), so until the periodic reconcile loop next fires (up to 60s) the
// registry has no row for this host → resolveOrgScope MISSES → AuthOrgHandover
// returns 401 `invalid audience` → the stranger sees /login instead of landing
// signed-in. This makes the registration deterministic at handover time rather
// than dependent on the periodic tick's timing.
//
// Returns true when a row for `host` is present after the call (either it
// already existed or this call just wrote it). Best-effort: any apiserver /
// list / Put failure returns false and is logged — AuthOrgHandover then falls
// back to refusing, exactly as before this method existed (no regression — the
// periodic loop still backfills the row within one interval).
func (h *Handler) ensureTenantRegisteredForHost(ctx context.Context, host string) bool {
	if h == nil || h.tenantRegistry == nil {
		return false
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	// Already known? Nothing to do — the steady-state path.
	if _, found := h.tenantRegistry.Get(host); found {
		return true
	}

	deps, err := h.sovereignDepsFor()
	if err != nil || deps == nil || deps.dyn == nil {
		// Out-of-cluster / CI: no apiserver to resolve the CR from. Refuse and
		// let the caller surface the existing 401 — identical to pre-#3376.
		h.log.Info("tenant-registry-ensure: skipped — no in-cluster dynamic client",
			"host", host, "err", err)
		return false
	}

	list, err := deps.dyn.Resource(organizationGVR()).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		h.log.Warn("tenant-registry-ensure: list Organizations failed",
			"host", host, "err", err)
		return false
	}

	sovereignFQDN := strings.TrimSpace(h.orgTenantDeps.OTECHFQDN)
	for i := range list.Items {
		reg, ok := tenantRegistrationFromOrgCR(&list.Items[i], sovereignFQDN)
		if !ok || reg.Host != host {
			continue
		}
		if err := h.tenantRegistry.Put(reg); err != nil {
			h.log.Warn("tenant-registry-ensure: Put failed for Org console host",
				"host", reg.Host, "tenant_id", reg.TenantID, "err", err)
			return false
		}
		h.log.Info("tenant-registry-ensure: on-demand registered Org console host",
			"host", reg.Host, "tenant_id", reg.TenantID)
		return true
	}
	// No Org CR matches this host yet — the CR has not been created (or its
	// pool console host differs). Refuse; the caller surfaces 401.
	h.log.Info("tenant-registry-ensure: no Organization CR matches host",
		"host", host, "orgs", len(list.Items))
	return false
}

// tenantRegistrationFromOrgCR builds the TenantRegistration for an Organization
// CR, mirroring the shape the BSS pipeline writes at Step 6
// (organization_provisioning.go) byte-for-byte so both doors yield an identical
// row. Returns (zero, false) when the CR can't produce a valid pool console host
// (no slug, or no parentDomain — a single-domain Org rides the Sovereign apex
// wildcard and is registered by the BSS pipeline, never the funnel).
//
// The TenantID comes from the `openova.io/tenant-id` label the funnel stamps
// (org_tenant_org_cr.go) or the provisioning-service stamps; it falls back to
// the slug so the row is never empty (Put requires a non-empty tenant_id).
func tenantRegistrationFromOrgCR(obj *unstructured.Unstructured, sovereignFQDN string) (store.TenantRegistration, bool) {
	slug := strings.ToLower(strings.TrimSpace(nestedString(obj.Object, "spec", "slug")))
	if slug == "" {
		slug = strings.ToLower(strings.TrimSpace(obj.GetName()))
	}
	if !orgSlugRE.MatchString(slug) {
		return store.TenantRegistration{}, false
	}

	parentDomain := strings.ToLower(strings.TrimSpace(
		nestedString(obj.Object, "spec", "tenantPublic", "parentDomain")))
	if parentDomain == "" {
		// Single-domain Org (no pool parent). Its console host
		// `console.<slug>.<sovFQDN>` is covered by the Sovereign apex wildcard
		// and registered by the BSS pipeline when it runs; the funnel never
		// mints such an Org. Nothing to register here.
		return store.TenantRegistration{}, false
	}

	subdomain := strings.ToLower(strings.TrimSpace(
		nestedString(obj.Object, "spec", "tenantPublic", "subdomain")))
	if subdomain == "" {
		subdomain = slug
	}

	host := "console." + subdomain + "." + parentDomain

	tenantID := strings.TrimSpace(obj.GetLabels()["openova.io/tenant-id"])
	if tenantID == "" {
		tenantID = slug
	}

	// Realm URL + admin endpoint mirror organization_provisioning.go Step 6:
	// the issuer lives under the same parent zone as the console host, the
	// realm is `org-<subdomain>`, and the in-cluster admin endpoint targets the
	// per-Org Keycloak in the Org namespace (= slug, per the org-controller's
	// namespace convention).
	realmName := "org-" + subdomain
	realmURL := fmt.Sprintf("https://keycloak.%s.%s/realms/%s", subdomain, parentDomain, realmName)
	adminURL := fmt.Sprintf("http://keycloak-%s.%s.svc:8080", subdomain, slug)

	return store.TenantRegistration{
		Host:                  host,
		TenantID:              tenantID,
		TenantKind:            store.TenantKindOrg,
		KeycloakRealmURL:      realmURL,
		KeycloakClientID:      "catalyst-ui",
		OrganizationNamespace: slug,
		OrgKeycloakAdminURL:   adminURL,
		OrgKeycloakRealmName:  realmName,
	}, true
}

// tenantRegistrationEqual reports whether two registrations are field-for-field
// identical (host is already lowercased by both producers). Used to short-
// circuit a no-op Put so a steady-state reconcile never rewrites the flat file.
func tenantRegistrationEqual(a, b store.TenantRegistration) bool {
	return a.Host == b.Host &&
		a.TenantID == b.TenantID &&
		a.TenantKind == b.TenantKind &&
		a.KeycloakRealmURL == b.KeycloakRealmURL &&
		a.KeycloakClientID == b.KeycloakClientID &&
		a.OrganizationNamespace == b.OrganizationNamespace &&
		a.OrgKeycloakAdminURL == b.OrgKeycloakAdminURL &&
		a.OrgKeycloakRealmName == b.OrgKeycloakRealmName
}

// nestedString reads a string at the given path from an unstructured object's
// map, returning "" when the path is absent or not a string. A thin wrapper over
// unstructured.NestedString that swallows the (found, err) tuple — every caller
// here treats absent/typed-wrong identically as empty.
func nestedString(obj map[string]any, fields ...string) string {
	s, _, _ := unstructured.NestedString(obj, fields...)
	return s
}
