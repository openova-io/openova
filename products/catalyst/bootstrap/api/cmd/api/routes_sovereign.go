package main

import "github.com/go-chi/chi/v5"

func registerPublicSovereign(r chi.Router, d routeDeps) {
	h := d.h
	// /api/v1/sovereign/self — Sovereign self-discovery (deployment id +
	// FQDN) used by the catalyst-ui SovereignConsoleRedirect React
	// component to map clean `/console/<page>` URLs on a Sovereign to the
	// canonical deployment-scoped `/provision/<self-id>/<page>` pages
	// that render the same byte-byte data the mothership serves at
	// console.openova.io/sovereign/provision/<id>/<page>.
	//
	// Outside RequireSession: the response carries only public
	// identifiers (deployment id + FQDN — both visible in URLs anyway).
	// Bypassing the session gate keeps the redirect helper usable on
	// the very first browser hit before the operator has logged in.
	// Mothership returns 404 (CATALYST_OTECH_FQDN unset) — the UI hook
	// silently falls back to URL params there. See sovereign_self.go.
	r.Get("/api/v1/sovereign/self", h.HandleSovereignSelf)

	// D16 PR F (2026-05-17 t138 bug fix): mothership POSTs each
	// secondary-region kubeconfig here at handover. Same auth model as
	// /api/v1/internal/deployments/import — no operator session exists
	// on the child yet, validation is by depID+regionKey safe-id regex.
	// Earlier registration inside the auth group (rg) caused mothership
	// POSTs to 401, suppressing the D16 fan-out silently. Bytes never
	// leave the chroot disk or enter logged structs (INVIOLABLE-PRINCIPLES #10).
	r.Post("/api/v1/sovereign/secondary-kubeconfig", h.HandleSovereignSecondaryKubeconfig)
}

func registerSessionSovereign(rg chi.Router, d routeDeps) {
	h := d.h
	// Sovereign Console populated views (issue #933). Read-only
	// endpoints the Console pages on console.<sov-fqdn>/console/*
	// hit to render LIVE local-cluster data (HelmReleases, Jobs,
	// catalog, nodes/namespaces/ingresses). Without these the
	// freshly-handed-over Sovereign Console renders only
	// placeholders — useless on day one.
	//
	// All four endpoints use rest.InClusterConfig + the
	// catalyst-api ServiceAccount on the Sovereign cluster, so
	// they continue to function after the Self-Sovereignty
	// Cutover (issue #792) severs every mothership-side tether.
	// On the mothership these endpoints also work (catalyst-api
	// runs in-cluster on contabo too), but the mothership
	// Console serves the per-deployment endpoints instead — the
	// /api/v1/sovereign/* surface is the customer-side Console's
	// data plane.
	rg.Get("/api/v1/sovereign/status", h.HandleSovereignStatus)
	rg.Get("/api/v1/sovereign/jobs", h.HandleSovereignJobs)
	rg.Get("/api/v1/sovereign/apps", h.HandleSovereignApps)
	rg.Get("/api/v1/sovereign/cloud", h.HandleSovereignCloud)
	// PATCH /api/v1/sovereign/apps/{slug}/publish — operator-admin
	// toggle to publish/unpublish a SaaS app on this Sovereign's
	// marketplace. Replaces the deleted /catalog page (PR #1058);
	// chip lives on each AppsPage card, proxies to the in-cluster
	// Organization catalog service.
	rg.Patch("/api/v1/sovereign/apps/{slug}/publish", h.HandleSovereignAppPublish)

	// EPIC-3 (#1098) — Sovereign-prefix RBAC access-matrix surface
	// (TBD-F4 / C6-007). Chroot-friendly mirror of
	// /api/v1/sovereigns/{id}/rbac/access-matrix; the id is resolved
	// server-side from SOVEREIGN_FQDN + the catalyst_session cookie
	// (same chain as HandleSovereignSelf). Same query-param filters
	// and response shape as the per-deployment endpoint, so the
	// AccessMatrixPage in the chroot can call this path without
	// first round-tripping to /sovereign/self.
	rg.Get("/api/v1/sovereign/rbac/matrix", h.HandleSovereignRBACMatrix)

	// C6-006 / #1739 — Sovereign-prefix RBAC assign surface. Same
	// wire contract as /api/v1/sovereigns/{id}/rbac/assign but the
	// active deployment id is resolved server-side from
	// SOVEREIGN_FQDN + the catalyst_session cookie (mirrors
	// HandleSovereignRBACMatrix one line above). The Sovereign
	// Console's Members UI hits this seam directly so it doesn't
	// have to round-trip /sovereign/self → /sovereigns/{id}/rbac/assign
	// per click; without it every chroot-side assign 404'd or
	// stamped a generic 500 `rbac-assign-failed: resource not
	// found` when the FE constructed the path with an empty id.
	rg.Post("/api/v1/sovereign/rbac/assign", h.HandleSovereignRBACAssign)

	// TBD-E16 — Sovereign-prefix UserAccess listing. Chroot-friendly
	// mirror of /api/v1/deployments/{depId}/admin/user-access; the
	// depId is resolved server-side from SOVEREIGN_FQDN +
	// catalyst_session cookie (same chain as HandleSovereignSelf).
	// Operator-side smoke probes / external monitors can hit a
	// single stable URL without first round-tripping
	// /sovereign/self. Same response shape as the per-deployment
	// endpoint (userAccessListResponse).
	rg.Get("/api/v1/sovereign/users", h.HandleSovereignUsers)

	// Self-Sovereignty Cutover (issue #792 — parent epic #790). The
	// post-handover step that severs a Sovereign's remaining
	// tethers to the openova-io mothership: gitea-mirror,
	// harbor-projects, harbor-prewarm, registry-pivot DaemonSet,
	// flux-gitrepository-patch, helmrepository-patches,
	// catalyst-api-env-patch, and an egress-block self-test.
	// PodSpec ConfigMaps are shipped by bp-self-sovereign-cutover
	// (issue #791); catalyst-api creates the actual Jobs.
	// Operator-admin gating is provided by RequireSession above —
	// only authenticated console operators can fire this.
	rg.Post("/api/v1/sovereign/cutover/start", h.HandleCutoverStart)
	rg.Get("/api/v1/sovereign/cutover/status", h.HandleCutoverStatus)
	rg.Get("/api/v1/sovereign/cutover/events", h.HandleCutoverEvents)

	// Multi-domain Sovereign — admin "Add another parent domain" flow
	// + live DNS propagation status panel (issue #829, parent #825).
	// LIST returns the operator's parent-domain pool (primary +
	// org-pool entries). POST queues a new domain through the
	// NS-flip → PowerDNS-zone-create → cert-issue pipeline. Per
	// issue #827 (this PR) the zone-create step now invokes the
	// real PowerDNS REST API via internal/powerdns.Client when the
	// catalyst-api Pod has CATALYST_POWERDNS_API_URL +
	// CATALYST_POWERDNS_API_KEY wired (Sovereign-side); otherwise
	// the call falls back to the stub no-op so contabo-side
	// installs stay green.
	// /propagation fans out to 5 public DNS resolvers via Go's
	// net.Resolver and reports per-resolver convergence so the
	// operator can see the gTLD 48h NS TTL window settle.
	rg.Get("/api/v1/sovereign/parent-domains", h.ListParentDomains)
	rg.Post("/api/v1/sovereign/parent-domains", h.AddParentDomain)
	rg.Delete("/api/v1/sovereign/parent-domains/{name}", h.DeleteParentDomain)
	rg.Get("/api/v1/sovereign/parent-domains/{name}/propagation", h.GetPropagation)
	// D16 secondary-kubeconfig moved OUT of auth group in PR F
	// (2026-05-17). Now at top-level r.Post (alongside
	// /api/v1/internal/deployments/import) so mothership handover
	// POSTs aren't 401'd before any operator session exists.
}
