package main

import "github.com/go-chi/chi/v5"

func registerSessionOrg(rg chi.Router, d routeDeps) {
	h := d.h
	// Organization-tier user CRUD + role mapping (issue #802, ADR-0003).
	// Owned by the unified-rbac slice of catalyst-api. Tenant
	// scoping is by X-Tenant-Host header (sent by the SPA from
	// window.location.host); the tenant must be registered with
	// tenant_kind=org. Each create fires the 3-step user-create
	// hook (Keycloak → NewAPI → K8s Secret) per ADR-0003 §3.
	// State is persisted in a flat-file user_provision_state
	// store; on partial failure the response carries the partial
	// state and the steps[] field so the SPA can render progress.
	// #3383: canonical `/api/v1/org/users`; legacy `/api/v1/org/users`
	// kept as one-release deprecation aliases (same handlers).
	rg.Post("/api/v1/org/users", h.HandleCreateOrgUser)
	rg.Get("/api/v1/org/users", h.HandleListOrgUsers)
	rg.Delete("/api/v1/org/users/{uuid}", h.HandleDeleteOrgUser)

	// #4116 — Org-scoped Application install. An Org-scoped customer
	// session (tier=org-admin) cannot reach the Sovereign-admin seam
	// `POST /api/v1/sovereigns/{id}/applications` (OrgScopeGuard 403s it,
	// the #4110/#4112 fix). This own-org route resolves the caller's own
	// namespace from the request host via the tenant registry and reuses
	// the shared install core, so the bp-agenity solo agent's openova-mcp
	// create_application tool can install Applications in the User's own
	// Org. `/api/v1/org/applications` is already on the OrgScopeGuard
	// allowlist (org_scope.go orgSafePathPrefixes) — no surface widening.
	rg.Post("/api/v1/org/applications", h.HandleOrgApplicationInstall)

	// #4113 — the TRUE per-Org Apps projection. GET companion to the
	// install POST above. An Org-scoped customer session on its own
	// console used to render the SOVEREIGN-WIDE catalog grid (the only
	// allowlisted Apps feed was /api/v1/sovereign/apps, which lists every
	// HelmRelease + Application CR cluster-wide). This route lists ONLY the
	// Application CRs + HelmReleases in the caller's OWN org-<uuid>
	// namespace (resolved server-side via the tenant registry with the
	// #4110 own-org binding) and projects each as a card with its open URL
	// derived from the Org namespace's HTTPRoute set — so agenity surfaces
	// with a working Open link. Already on the OrgScopeGuard allowlist
	// (org_scope.go orgSafePathPrefixes) — no surface widening.
	rg.Get("/api/v1/org/applications", h.HandleOrgApplications)

	// BSS landing KPI rollup (Refs #1949, TBD-A58). Read-only feed
	// for the /console/bss landing surface (BssLandingPage.tsx →
	// getBssOverview() in ui/src/lib/bss.api.ts). Pre-fix the path
	// returned 404 and the FE flipped `pendingApi=true`, so every
	// tile rendered the "API pending" pill instead of real zeros.
	// Today the handler returns a zero-filled payload — the FE
	// renders the full target-state surface (0 revenue / 0
	// customers — truthful on a fresh Sovereign) from first paint
	// (INVIOLABLE-PRINCIPLES.md #1). The non-zero projection lands
	// with the marketplace/billing wire (siblings:
	// /api/v1/org/billing/revenue, /api/v1/org/orders,
	// /api/v1/org/billing/vouchers/list, /api/v1/org/tenants).
	// #3383: canonical `/api/v1/org/*`; legacy `/api/v1/org/*` aliased.
	rg.Get("/api/v1/org/bss/overview", h.HandleGetOrgBssOverview)
	// naming-guard: alias — legacy Organization path, one-release deprecation.

	// BSS Orders rollup (Wave 6 PR 3). Read-only feed for the
	// /console/bss/orders native React table. Today the handler
	// returns an empty list — the FE renders its full empty-state
	// chrome so the operator sees the target-state surface from
	// first paint (INVIOLABLE-PRINCIPLES.md #1). The non-empty
	// projection lands with the marketplace/billing wire.
	rg.Get("/api/v1/org/orders", h.HandleListOrgOrders)
	// naming-guard: alias — legacy Organization path, one-release deprecation.

	// BSS Revenue rollup (Wave 6 PR 4). Read-only feed for the
	// /console/bss/revenue native React surface (KPI strip + line
	// chart + per-plan breakdown table). Today the handler returns
	// a zero-filled payload — the FE renders its full target-state
	// chrome so the operator sees the surface from first paint
	// (INVIOLABLE-PRINCIPLES.md #1). The non-zero projection lands
	// with the marketplace/billing wire.
	rg.Get("/api/v1/org/billing/revenue", h.HandleGetOrgBillingRevenue)
	// naming-guard: alias — legacy Organization path, one-release deprecation.

	// BSS Vouchers proxy (Wave 6 PR 5 — follow-up to FE PR #1609).
	// Forwards to the Organization gateway (gateway.org-services.svc.cluster.local:8080)
	// which proxies to the billing service's
	// `/billing/vouchers/{list,issue,revoke}` handlers
	// (core/services/billing/handlers/vouchers.go, gated by
	// requireVoucherIssuer — superadmin OR sovereign-admin per
	// docs/FRANCHISE-MODEL.md §3). The FE bss.api.ts
	// listVouchers/issueVoucher/revokeVoucher all hit these paths.
	// Revoke is registered for BOTH POST (task spec) and DELETE (FE
	// wire) — the handler always forwards as DELETE so the billing
	// service's DELETE /billing/vouchers/revoke/{code} route matches.
	// #3383: canonical `/api/v1/org/billing/vouchers/*`; legacy aliased.
	rg.Get("/api/v1/org/billing/vouchers/list", h.HandleListOrgBillingVouchers)
	rg.Post("/api/v1/org/billing/vouchers/issue", h.HandleIssueOrgBillingVoucher)
	rg.Post("/api/v1/org/billing/vouchers/revoke/{code}", h.HandleRevokeOrgBillingVoucher)
	rg.Delete("/api/v1/org/billing/vouchers/revoke/{code}", h.HandleRevokeOrgBillingVoucher)
	rg.Post("/api/v1/org/billing/purchase", h.HandleOrgBillingPurchase)
	// naming-guard: alias — legacy Organization path, one-release deprecation.

	// Organizations commerce editors (issue #3378 DoD 7/8) — the
	// console's plans/addons/bundles/industries/apps editors proxy
	// CRUD onto the EXISTING superadmin-JWT /catalog/admin/* endpoints
	// on the in-cluster Organization commerce catalog. NOT new business
	// endpoints (§6) — the same mintOrgBridgeToken → orgCatalog proxy
	// hop HandleSovereignAppPublish uses, generalized to full CRUD so
	// console.<sovereign>/api/* can reach the admin paths. Reads proxy
	// the EXISTING public /catalog/{kind} list endpoints through
	// catalyst-api too (HandleOrgCommerceList) — on the console host
	// /api/catalog/* doesn't route to the catalog service the way the
	// Organization gateway does, so a bare GET /api/catalog/plans 404'd even
	// though the plan existed (issue #3378 plans-table 404).
	// #3383: canonical `/api/v1/org/commerce/*`; legacy aliased.
	rg.Get("/api/v1/org/commerce/{kind}", h.HandleOrgCommerceList)
	rg.Post("/api/v1/org/commerce/{kind}", h.HandleOrgCommerceCreate)
	rg.Put("/api/v1/org/commerce/{kind}/{id}", h.HandleOrgCommerceUpdate)
	rg.Delete("/api/v1/org/commerce/{kind}/{id}", h.HandleOrgCommerceDelete)

	// Organizations metering feed (issue #3378 B3) — per-org
	// consumption aggregation, parent self-showback first. Lean
	// kube-metrics aggregation (no new component): reuses the
	// dashboard's per-namespace resource-request rows. The billing
	// pages read this one GET; 100% attributes to the parent on a
	// zero-sub-org estate (§5 day-one showback).
	// #3383: canonical `/api/v1/org/consumption`; legacy aliased.
	rg.Get("/api/v1/org/consumption", h.HandleSovereignConsumption)
	// naming-guard: alias — legacy Organization path, one-release deprecation.

	// Organizations Enter-org support session (issue #3378 B2) —
	// mints the audited, time-boxed (≤60min) impersonation into a
	// sub-org's OWN console as a support principal in the org's realm
	// (never the owner's identity). Reuses the handover-redemption
	// flow + the #3374/#3385 cookie fix. Writes the audit event;
	// hidden on the parent row (and rejects entering the parent).
	// #3383: canonical `/api/v1/org/organizations/{slug}/enter`; legacy aliased.
	rg.Post("/api/v1/org/organizations/{slug}/enter", h.HandleEnterOrg)
	// naming-guard: alias — legacy Organization path, one-release deprecation.
}
