// org_console_tls_reconcile.go — #5635. Reconcile the per-Org console gateway
// surface from the Organization CRs, in EVERY region, for EVERY Org.
//
// THE GAP this file closes.
//
// The per-Org console gateway surface (wildcard Certificate + the
// `console-https-<slug>`/`console-http-<slug>` listener pair on
// cilium-gateway-console + the console HTTPRoute for
// `console.<slug>.<parent>`) has TWO producers, and only one of them fans the
// surface out to every region:
//
//   - BSS door — catalyst-api `provisionOrgConsoleTLS` (org_console_tls.go),
//     multi-region since #5511/PR #5524. Fires from
//     runOrganizationPipeline step 7 and HandleReconcileOrganization ONLY,
//     both keyed on catalyst-api's OWN OrganizationProvisionRecord store.
//   - MARKETPLACE-funnel door — the org-controller's reconcileTenantConsoleTLS
//     + reconcileTenantRoute (core/controllers/organization/…), which run on
//     every Organization-CR reconcile but write to the org-controller's OWN
//     cluster only. There is no per-region client seam in that process.
//
// A funnel Org never runs the catalyst-api pipeline (tenant-service →
// `tenant.created` → provisioning-service `createOrganizationCR`, which stamps
// `openova.io/source: tenant-created-event` on the CR — organization_create.go:162).
// So its surface is written by the single-region producer and NOTHING ever
// converges it into the second region. Proven live on hw292
// (dep 1c56518035a83e03, 2026-08-03):
//
//	catalyst-system HTTPRoutes        region-A                     region-B
//	console.uatco.omani.homes    catalyst-ui-console-uatco-…     ABSENT
//	                             (managed-by=catalyst,
//	                              openova.io/managed-by=
//	                              organization-controller)
//	console.r17probe.omani.homes catalyst-ui-r17probe-…          PRESENT
//	                             (managed-by=catalyst-api)
//
// One shared EIP round-robins both regions' cilium-envoy, so a per-Org console
// host that only one region can serve resets ~1/N of all customer connections
// — the per-region split-write class (#5394/#5406/#5414/#5416/#5511), reached
// here through a PRODUCER SPLIT rather than through a missing fan-out.
//
// THE FIX — the #4179 consolidation, applied to the gateway surface. The
// Organization CR is the ONE object both doors create. catalyst-api is the ONE
// process that holds a per-region client for every region (h.k8sCache, the
// seam #5524 already wired into orgConsoleTLSTargets). So catalyst-api
// reconciles the surface FROM the Organization CRs: list every Org CR, derive
// the same free-subdomain provisioning record either door would have produced,
// and run the existing multi-region provisionOrgConsoleTLS. That makes the
// fan-out
//
//	UNCONDITIONAL — keyed on the CR, so the funnel door and the BSS door are
//	                covered by one seam, and
//	RECONCILED    — re-run on a ticker, so an Org whose surface is missing
//	                from a region CONVERGES instead of staying however it was
//	                first written (the backfill direction; write-once was the
//	                defect shape).
//
// Every write it performs is idempotent: the Certificate is Create/
// AlreadyExists, the listener pair is a per-Org-field-manager SSA whose merge
// is a no-op when the spec matches, the HTTPRoute is Create/AlreadyExists and
// is ADOPTED rather than duplicated when the org-controller's equivalent route
// already serves that region (#5635, orgConsoleRouteAlreadyServed). A healthy
// Sovereign therefore sees zero object churn from this loop.
//
// Best-effort throughout: a List failure logs and retries on the next tick; a
// per-Org failure is logged inside provisionOrgConsoleTLS and never starves
// the remaining Orgs; out-of-cluster / CI (no dynamic client) is a logged skip.
// RBAC is already granted — the ClusterRole carries organizations get/list/
// watch, certificates get/list/watch+create, gateways get/list/watch+patch and
// httproutes get/list/watch+create (products/catalyst/chart/templates/
// clusterrole-cutover-driver.yaml), so this adds no chart surface.

package handler

import (
	"context"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// orgConsoleTLSReconcileInterval is the steady-state cadence at which
// catalyst-api re-converges every Org's console gateway surface across every
// region. Deliberately slower than the 60s tenant-registry loop: each Org's
// pass ends in the #5511 listener-admission read-back, which is near-instant
// on a healthy Gateway but is bounded by orgConsoleListenerAdmitBudget (60s)
// when a region's cilium-operator has missed the append. Five minutes keeps a
// missing region's console door closed for at most one interval while leaving
// the apiserver load negligible (one List + a handful of no-op Gets per Org).
// A package var, not a const, so tests shrink it.
var orgConsoleTLSReconcileInterval = 5 * time.Minute

// ReconcileOrgConsoleTLSFromOrgCRs runs the per-Org console gateway surface
// reconcile loop until ctx is cancelled. It performs one pass immediately (so
// a catalyst-api restart re-converges every existing Org — including every
// funnel Org the BSS pipeline never saw — before the first tick) and then
// re-converges on a ticker. Wired from main.go as a background goroutine,
// mirroring ReconcileTenantRegistryFromOrgCRs's async-startup idiom.
func (h *Handler) ReconcileOrgConsoleTLSFromOrgCRs(ctx context.Context) {
	if h == nil {
		return
	}
	h.reconcileOrgConsoleTLSOnce(ctx)
	ticker := time.NewTicker(orgConsoleTLSReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.reconcileOrgConsoleTLSOnce(ctx)
		}
	}
}

// reconcileOrgConsoleTLSOnce lists every Organization CR and re-ensures each
// pool-parent Org's console gateway surface in EVERY region. Idempotent +
// best-effort: a CR that cannot produce a free-subdomain surface is skipped
// (BYO / no pool parent / invalid slug — resolveOrgConsoleTLSNames decides,
// so this loop and the two doors can never drift onto different criteria), a
// List failure is logged and the next tick retries.
func (h *Handler) reconcileOrgConsoleTLSOnce(ctx context.Context) {
	deps, err := h.sovereignDepsFor()
	if err != nil || deps == nil || deps.dyn == nil {
		// Out-of-cluster / CI: no apiserver to list from. On a real Sovereign
		// this branch never fires.
		h.log.Info("org-console-tls-reconcile: skipped — no in-cluster dynamic client",
			"err", err)
		return
	}

	list, err := deps.dyn.Resource(organizationGVR()).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		h.log.Warn("org-console-tls-reconcile: list Organizations failed — will retry next tick",
			"err", err)
		return
	}

	sovereignFQDN := strings.TrimSpace(h.orgTenantDeps.OTECHFQDN)
	reconciled, skipped := 0, 0
	for i := range list.Items {
		rec, ok := orgConsoleTLSRecordFromOrgCR(&list.Items[i], sovereignFQDN)
		if !ok {
			skipped++
			continue
		}
		// The SAME multi-region emitter the BSS door runs — host region plus
		// every secondary registered in h.k8sCache (orgConsoleTLSTargets),
		// with the #5511 per-region listener-admission read-back. Nothing
		// about it is create-time-only, which is precisely why re-running it
		// here backfills a region the Org's original producer never reached.
		h.provisionOrgConsoleTLS(ctx, rec)
		reconciled++
	}
	if reconciled > 0 {
		h.log.Info("org-console-tls-reconcile: pass complete — per-Org console gateway surface re-ensured in every region (#5635)",
			"orgs", len(list.Items), "reconciled", reconciled, "skipped", skipped)
	}
}

// orgConsoleTLSRecordFromOrgCR builds the free-subdomain provisioning record
// for an Organization CR — the input resolveOrgConsoleTLSNames consumes — so
// an Org minted by EITHER door yields byte-identical resource names.
//
// Mirrors tenantRegistrationFromOrgCR's derivation (tenant_registry_reconcile.go)
// field-for-field: slug from `spec.slug` falling back to the CR name, pool
// parent from `spec.tenantPublic.parentDomain`, subdomain from
// `spec.tenantPublic.subdomain` falling back to the slug — the same three
// fields the org-controller's own orgConsoleTLSNamesForOrg reads.
//
// Returns (zero, false) when the CR cannot produce a pool-parent console
// surface: no parentDomain (a single-domain Org rides the Sovereign apex
// wildcard, which already covers console.<slug>.<sovFQDN> — no per-Org
// resource applies) or a slug that fails orgSlugRE. DomainMode is set to
// free-subdomain because a pool parentDomain on the CR IS the free-subdomain
// case; a BYO Org carries its own per-host Certificate and a CNAME and never
// sets tenantPublic.parentDomain.
func orgConsoleTLSRecordFromOrgCR(obj *unstructured.Unstructured, sovereignFQDN string) (store.OrganizationProvisionRecord, bool) {
	slug := strings.ToLower(strings.TrimSpace(nestedString(obj.Object, "spec", "slug")))
	if slug == "" {
		slug = strings.ToLower(strings.TrimSpace(obj.GetName()))
	}

	parentDomain := strings.ToLower(strings.TrimSpace(
		nestedString(obj.Object, "spec", "tenantPublic", "parentDomain")))
	if parentDomain == "" {
		return store.OrganizationProvisionRecord{}, false
	}

	subdomain := strings.ToLower(strings.TrimSpace(
		nestedString(obj.Object, "spec", "tenantPublic", "subdomain")))
	if subdomain == "" {
		subdomain = slug
	}

	organizationID := strings.TrimSpace(obj.GetLabels()["openova.io/tenant-id"])
	if organizationID == "" {
		organizationID = slug
	}

	rec := store.OrganizationProvisionRecord{
		OrganizationID: organizationID,
		Subdomain:      subdomain,
		DomainMode:     store.OrganizationDomainFreeSubdomain,
		ParentDomain:   parentDomain,
		OTECHFQDN:      sovereignFQDN,
		CompanyName:    strings.TrimSpace(nestedString(obj.Object, "spec", "displayName")),
	}
	// Single decision point: the emitter's own resolver decides whether this
	// Org gets a surface, so the reconcile loop can never accept an Org the
	// emitter would then skip (or vice versa).
	if _, ok := resolveOrgConsoleTLSNames(rec); !ok {
		return store.OrganizationProvisionRecord{}, false
	}
	return rec, true
}
