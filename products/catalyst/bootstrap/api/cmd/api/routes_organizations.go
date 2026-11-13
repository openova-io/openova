package main

import "github.com/go-chi/chi/v5"

func registerSessionOrganizations(rg chi.Router, d routeDeps) {
	h := d.h
	// Organization provisioning pipeline (issue #804). Marketplace
	// signup → vCluster + bp-* charts + DNS + cert + SSO clients
	// + Organization registry. State machine surfaced as steps[] in
	// the response so the SPA can render a progress timeline. The
	// reconciler is event-driven (NATS heartbeat-to-self per
	// ADR-0003 §3.5) so a Pod restart never strands a half-
	// provisioned Organization. See organization_provisioning.go for
	// the full state machine and organization_gitops.go for the
	// GitOps overlay generator.
	//
	// #3383: canonical routes are `/api/v1/organizations[...]`. The
	// legacy `/api/v1/org/tenants[...]` paths stay as one-release
	// deprecation aliases (same handlers) so the marketplace SPA +
	// funnel-walk evidence survive the rename.
	rg.Post("/api/v1/organizations", h.HandleCreateOrganization)
	rg.Get("/api/v1/organizations", h.HandleListOrganizations)
	rg.Get("/api/v1/organizations/{id}", h.HandleGetOrganization)
	rg.Post("/api/v1/organizations/{id}/reconcile", h.HandleReconcileOrganization)
	rg.Delete("/api/v1/organizations/{id}", h.HandleDeleteOrganization)
	// Billing enforcement (products/chargeback DESIGN.md §9.7): the
	// NARROWEST reverse seam from a billing decision to the platform —
	// stamp spec.suspended on the Organization CR, which the
	// org-controller honours by parking the per-Org Flux reconciliation
	// and surfacing a Suspended condition. Operator-only (RequireSession
	// + OrgScopeGuard above); the chargeback application calls it with a
	// bearer session. See internal/handler/org_suspend.go.
	rg.Post("/api/v1/organizations/{id}/suspend", h.HandleSuspendOrganization)
	rg.Post("/api/v1/organizations/{id}/resume", h.HandleResumeOrganization)
}
