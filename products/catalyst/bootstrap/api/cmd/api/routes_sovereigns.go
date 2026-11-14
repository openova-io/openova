package main

import "github.com/go-chi/chi/v5"

func registerSessionSovereigns(rg chi.Router, d routeDeps) {
	h := d.h
	// K8s data-plane endpoints — list + SSE stream + sync map per
	// Sovereign cluster (issue #321). Per ADR-0001 §5 the catalyst-api
	// is the consolidator; reads flow off the in-process Indexer,
	// never directly from the apiserver.
	rg.Get("/api/v1/sovereigns/{id}/k8s/{kind}", h.HandleK8sList)
	rg.Get("/api/v1/sovereigns/{id}/k8s/stream", h.HandleK8sStream)
	rg.Get("/api/v1/sovereigns/{id}/k8s/sync", h.HandleK8sSync)
	// EPIC-4 X1 (#1099) — Pod-log WebSocket. Same chi.Group as the
	// rest of /k8s/* so RequireSession gates the upgrade handshake
	// the same way it gates the SSE/REST surface.
	rg.Get("/api/v1/sovereigns/{id}/k8s/logs/{ns}/{pod}/{container}", h.HandleK8sLogs)
	// EPIC-4 X2+E (#1099) — exec session creation (Guacamole),
	// exec WebSocket fallback (X1-style), session list + replay.
	// Tier-developer or higher for create + WS fallback;
	// tier-admin or higher for list; admin/owner for replay.
	rg.Post("/api/v1/sovereigns/{id}/k8s/exec/{ns}/{pod}/{container}/session", h.HandleK8sExecSession)
	rg.Get("/api/v1/sovereigns/{id}/k8s/exec/{ns}/{pod}/{container}", h.HandleK8sExecWebSocket)
	rg.Get("/api/v1/sovereigns/{id}/sessions", h.HandleK8sSessionsList)
	rg.Get("/api/v1/sovereigns/{id}/sessions/{sessionId}/replay", h.HandleK8sSessionReplay)
	// Wave 5.69b (#2375, EPIC #1099 #1097) — Sovereign-console
	// sidebar-entries derived from installed Blueprints'
	// spec.consoleUI.sidebarEntry (Wave 5.69 CRD field, #2370).
	// SovereignSidebar.tsx subscribes + appends nav entries
	// between hardcoded BSS (order=60) and Settings.
	rg.Get("/api/v1/sovereigns/{id}/console-ui/sidebar-entries", h.HandleConsoleUISidebarEntries)
	// EPIC #6723 lane C — the per-Sovereign sidebar MAPPING (founder
	// 2026-08-31: "the left menu or sub-menus of the sovereign console
	// can be connected to the respective applications, like Agenity").
	// GET returns the stored overrides; PUT validates + persists them
	// as ConfigMap catalyst-system/console-ui-sidebar on the Sovereign's
	// cluster. Both are sovereign-admin gated inside the handler
	// (requireSidebarAdmin) on top of RequireSession; the merged view
	// the console renders is the sidebar-entries GET above.
	rg.Get("/api/v1/sovereigns/{id}/console-ui/sidebar-overrides", h.HandleConsoleUISidebarOverridesGet)
	rg.Put("/api/v1/sovereigns/{id}/console-ui/sidebar-overrides", h.HandleConsoleUISidebarOverridesPut)
	// qa-loop iter-7 Fix #39 — canonical UAT-matrix vocabulary
	// surface for "issue a remote-shell session". Same business
	// logic as POST /k8s/exec/.../session but the matrix-canonical
	// query-param + response-field shape (`sessionId`,
	// `guacamoleUrl`, `recordingPath`). See
	// internal/handler/shells_issue.go for the full contract.
	// Tier-developer or higher (same gate as the underlying
	// HandleK8sExecSession).
	rg.Post("/api/v1/sovereigns/{id}/shells/issue", h.HandleShellsIssue)
	// qa-loop iter-9 Fix #43, Cluster-B (TC-231): canonical
	// kubectl-style alias for /sessions — items envelope of
	// recorded shell sessions on the Sovereign. Same handler.
	rg.Get("/api/v1/sovereigns/{id}/shells/sessions", h.HandleK8sSessionsList)
	// qa-loop iter-9 Fix #43, Cluster-A (TC-376): canonical
	// kubectl-style alias for POST /k8s/exec/.../session — issues
	// a Guacamole session against the named pod's default
	// container. The handler resolves a default container name
	// when the URL omits it (matches `kubectl exec POD --`
	// behaviour).
	rg.Post("/api/v1/sovereigns/{id}/k8s/pods/{ns}/{pod}/exec", h.HandleK8sExecSession)
	// qa-loop iter-9 Fix #43, Cluster-B (TC-265): canonical
	// search-across-kinds endpoint — items envelope of K8s
	// resources whose name matches `?q=`.
	rg.Get("/api/v1/sovereigns/{id}/k8s/search", h.HandleK8sSearch)
	// EPIC-4 R1+R2+R3+R5+R6 (#1099) — Resource browser drill-down,
	// resource tree, YAML edit (apply / dry-run), per-row actions
	// (scale / restart / delete), metrics. Tier-admin gate is enforced
	// inside each mutation handler (UI hides the buttons too, but the
	// server is the authoritative gate per INVIOLABLE-PRINCIPLES #5).
	// /metrics has its own static path-prefix so chi resolves it
	// correctly against /k8s/{kind} (kind="metrics" would otherwise
	// shadow it). Static must come BEFORE dynamic.
	rg.Get("/api/v1/sovereigns/{id}/k8s/metrics/{kind}/{ns}/{name}", h.HandleK8sResourceMetrics)
	rg.Get("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}", h.HandleK8sResourceGet)
	rg.Get("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/tree", h.HandleK8sResourceTree)
	// G89 #2636 (2026-05-31): per-resource Events (direct apiserver
	// List, bypasses k8scache per TBD-V50). Different prefix
	// `events-for/{kind}/...` to avoid colliding with the cache's
	// kind-list path which already uses /k8s/{kind}/{ns}/{name}.
	rg.Get("/api/v1/sovereigns/{id}/k8s/events-for/{kind}/{ns}/{name}", h.HandleK8sEventsForResource)
	rg.Post("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/scale", h.HandleK8sResourceScale)
	rg.Post("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/restart", h.HandleK8sResourceRestart)
	rg.Post("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/dry-run", h.HandleK8sResourceDryRun)
	rg.Post("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/apply", h.HandleK8sResourceApply)
	rg.Delete("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}", h.HandleK8sResourceDelete)
	// QA-loop iter-7 Fix #34 — vocab widening: PUT alias for
	// /scale and direct resource Update via PUT on the bare path.
	// The matrix (TC-215, TC-243, TC-206, TC-244, TC-247) and
	// kubectl-style muscle memory both surface PUT for "edit
	// replicas" and "edit object yaml". The handlers underneath
	// are the same as the POST shapes; the chi router needs both
	// verbs registered explicitly because chi.Group.Post does NOT
	// silently match PUT.
	rg.Put("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/scale", h.HandleK8sResourceScale)
	rg.Put("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}", h.HandleK8sResourcePut)
	// QA-loop iter-7 Fix #34 — multi-resource server-side apply.
	// Body: {yaml: "<one-or-more-docs>"}. Returns one entry per
	// document with `created` vs `updated`. Matrix TC-271.
	rg.Post("/api/v1/sovereigns/{id}/k8s/apply", h.HandleK8sMultiApply)
	// Epic #6703 P4b — ad-hoc CronJob schedules ("New schedule" on the
	// /jobs/schedule surface). A real batch/v1 CronJob cloned from an
	// existing CronJob's jobTemplate; edit/delete gated to console-
	// origin schedules (a chart's CronJob changes in Git). See
	// internal/handler/schedules.go.
	rg.Post("/api/v1/sovereigns/{id}/schedules", h.HandleScheduleCreate)
	rg.Patch("/api/v1/sovereigns/{id}/schedules/{ns}/{name}", h.HandleScheduleUpdate)
	rg.Delete("/api/v1/sovereigns/{id}/schedules/{ns}/{name}", h.HandleScheduleDelete)
	// Compliance score aggregator (EPIC-1 #1096 slice S) — joins
	// Kyverno PolicyReports + 5 custom evaluators + EnvironmentPolicy
	// weights into per-resource + per-Application + per-Org +
	// per-Sovereign weighted scores. SSE for live updates, REST for
	// snapshots, Prometheus /metrics + NATS JetStream KV
	// `policy-rollup` for replayable history.
	rg.Get("/api/v1/sovereigns/{id}/compliance/scorecard", h.HandleComplianceScorecard)
	rg.Get("/api/v1/sovereigns/{id}/compliance/policies", h.HandleCompliancePolicies)
	rg.Get("/api/v1/sovereigns/{id}/compliance/policies/{name}", h.HandleCompliancePolicyByName)
	rg.Get("/api/v1/sovereigns/{id}/compliance/violations", h.HandleComplianceViolations)
	// #4085 — per-resource compliance findings for the resource
	// detail view's Compliance tab (kind+ns+name → its own policy
	// pass/fail/skip table).
	rg.Get("/api/v1/sovereigns/{id}/compliance/resource", h.HandleComplianceResource)
	rg.Get("/api/v1/sovereigns/{id}/compliance/stream", h.HandleComplianceStream)
	// Wave-2 Family-E (#1583/Family-E): runtime + supply-chain
	// compliance aggregators. Falco runtime alerts (C11-008),
	// Trivy SBOM + CVE reports (C11-010), per-Pod + cluster-wide.
	rg.Get("/api/v1/sovereigns/{id}/compliance/falco", h.HandleComplianceFalco)
	rg.Get("/api/v1/sovereigns/{id}/compliance/sbom", h.HandleComplianceSBOMPod)
	rg.Get("/api/v1/sovereigns/{id}/compliance/sbom/summary", h.HandleComplianceSBOMSummary)
	// QA-loop iter-11 Fix #48 — Networking page surface. Each
	// endpoint joins live K8s objects from the in-process k8scache
	// Indexer (Cilium NetworkPolicies, ClusterMesh ConfigMaps,
	// NetBird Deployments, Hubble relay/UI) into
	// the wire shape the UI's NetworkingPage subscribes to.
	// Per docs/INVIOLABLE-PRINCIPLES.md #2 every byte traces back
	// to a real K8s object — no fixture data, no stub rows.
	rg.Get("/api/v1/sovereigns/{id}/networking/policies", h.HandleNetworkingPolicies)
	rg.Get("/api/v1/sovereigns/{id}/networking/clustermesh", h.HandleNetworkingClusterMesh)
	rg.Get("/api/v1/sovereigns/{id}/networking/netbird", h.HandleNetworkingNetBird)
	rg.Get("/api/v1/sovereigns/{id}/networking/hubble", h.HandleNetworkingHubble)
	// EPIC-1 #1096 slice X — EnvironmentPolicy mode toggle backend
	// for the slice U PolicyModeToggle widget. Writes
	// EnvironmentPolicy.spec.compliance.modes; the EnvironmentPolicy
	// controller (separately reconciled) flips Kyverno's per-namespace
	// validationFailureAction. Requires tier-admin or higher per
	// INVIOLABLE-PRINCIPLES #5.
	rg.Put("/api/v1/sovereigns/{id}/environments/{env}/policy", h.HandleEnvironmentPolicyMode)

	// Marketplace mode toggle (issue #710 wave 3b). The Sovereign
	// Settings → Marketplace page POSTs here to enable/disable the
	// marketplace HTTPRoutes + storefront branding on a live
	// Sovereign. The handler edits the per-Sovereign overlay file
	// at clusters/<fqdn>/bootstrap-kit/13-bp-catalyst-platform.yaml
	// in the GitOps repo and pushes the commit; Flux on the target
	// Sovereign reconciles within ~1 min and the chart re-renders.
	// Per the founder's 2026-05-04 GitOps rule, NO ConfigMap-shortcut
	// path exists — every change is a git commit on the audit trail.
	rg.Post("/api/v1/sovereigns/{id}/marketplace", h.HandleSetMarketplace)
	rg.Get("/api/v1/sovereigns/{id}/marketplace", h.HandleGetMarketplace)

	// EPIC-3 (#1098) slice A1+A2 — find-or-create role assignment
	// + access-matrix endpoints. The /rbac/assign endpoint is the
	// ergonomic wrapper the multi-grant editor (slice U1) calls
	// when an operator picks a tier + scope combination; idempotent
	// re-assigns no-op or update the existing UserAccess. The
	// access-matrix endpoint feeds the EPIC-3 U7 access-matrix UI
	// with one pre-computed users × applications × tier grid.
	rg.Post("/api/v1/sovereigns/{id}/rbac/assign", h.HandleRBACAssign)
	rg.Get("/api/v1/sovereigns/{id}/rbac/access-matrix", h.HandleRBACAccessMatrix)

	// EPIC-3 (#1098) slice U8 — RBAC audit trail endpoints. List is
	// paginated; stream is SSE. Both filter to the rbac-* audit-type
	// namespace and the requested SovereignID. Backed by the
	// in-process audit.Bus (also forwarding to NATS catalyst.audit
	// when CATALYST_NATS_URL is set per ADR-0001 §3).
	rg.Get("/api/v1/sovereigns/{id}/audit/rbac", h.HandleRBACAuditList)
	rg.Get("/api/v1/sovereigns/{id}/audit/rbac/stream", h.HandleRBACAuditStream)

	// EPIC-3 (#1098) slice U2/U3/U4 — Keycloak proxy endpoints
	// powering the multi-grant editor's user picker (U2),
	// sovereign-admin group browser (U3), and realm/role browser
	// (U4). All proxy to the Sovereign realm's KC Admin REST API
	// via the wired h.kc client. U2 requires tier-admin or higher
	// (mirrors /rbac/assign); U3 + U4 require sovereign-admin
	// (admin or owner) per INVIOLABLE-PRINCIPLES #5.
	rg.Get("/api/v1/sovereigns/{id}/keycloak/users", h.HandleKeycloakUsersSearch)
	rg.Get("/api/v1/sovereigns/{id}/keycloak/groups", h.HandleKeycloakGroupsList)
	rg.Post("/api/v1/sovereigns/{id}/keycloak/groups", h.HandleKeycloakGroupsCreate)
	rg.Put("/api/v1/sovereigns/{id}/keycloak/groups/{groupId}", h.HandleKeycloakGroupsUpdate)
	rg.Delete("/api/v1/sovereigns/{id}/keycloak/groups/{groupId}", h.HandleKeycloakGroupsDelete)
	rg.Get("/api/v1/sovereigns/{id}/keycloak/roles", h.HandleKeycloakRolesList)
	rg.Get("/api/v1/sovereigns/{id}/keycloak/roles/{name}/members", h.HandleKeycloakRoleMembers)
	rg.Get("/api/v1/sovereigns/{id}/keycloak/clients/{clientId}/roles", h.HandleKeycloakClientRolesList)

	// qa-loop iter-1 Fix #104 — Keycloak admin proxy for the
	// matrix-asserted endpoints (TC-124 / TC-125 / TC-159 / TC-160 /
	// TC-161 / TC-176 / TC-190 / TC-285). Keycloak is NOT externally
	// exposed on the chroot Sovereign and the matrix runner cannot
	// `kubectl exec`, so without this proxy these 8 TCs were stuck
	// at FAIL/BLOCKED. The proxy gates every endpoint with the
	// sovereign-admin tier (admin or owner) — same gate as the U3/U4
	// browser endpoints — and uses the catalyst-api SA credential to
	// perform the privileged Keycloak Admin call in-cluster (the
	// operator's password / SA secret never leaves the cluster).
	rg.Get("/api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/roles", h.HandleKeycloakAdminRealmRolesList)
	rg.Get("/api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/roles/{role}/composites", h.HandleKeycloakAdminRoleComposites)
	rg.Get("/api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/identity-provider/instances", h.HandleKeycloakAdminIdPList)
	rg.Post("/api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/identity-provider/instances", h.HandleKeycloakAdminIdPCreate)
	rg.Post("/api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/identity-provider/instances/{alias}/mappers", h.HandleKeycloakAdminIdPMapperCreate)
	rg.Post("/api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/protocol/openid-connect/token", h.HandleKeycloakAdminTokenMint)
	rg.Get("/api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/clients", h.HandleKeycloakAdminClientsByClientID)
	rg.Get("/api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/clients/{client}/service-account-user/role-mappings/realm", h.HandleKeycloakAdminClientServiceAccountRoles)

	// EPIC-2 (#1097) slice I — live install flow. Operators submit
	// Application install requests here; the handler validates
	// parameters against Blueprint.spec.configSchema (via the
	// promoted core/controllers/pkg/validate package) and creates
	// the Application CR per ADR-0001 §2.7. The application-
	// controller (slice C4 #1133) reconciles the rest. The
	// preview endpoint runs the SAME renderer the controller uses
	// (core/controllers/pkg/render, promoted in this slice) so a
	// "looks-good in preview" cannot diverge from the actual
	// install. Both endpoints require tier-admin or higher per
	// docs/INVIOLABLE-PRINCIPLES.md #5.
	rg.Post("/api/v1/sovereigns/{id}/applications", h.HandleApplicationInstall)
	rg.Post("/api/v1/sovereigns/{id}/applications/preview", h.HandleApplicationPreview)
	rg.Get("/api/v1/sovereigns/{id}/applications/{name}/status", h.HandleApplicationStatus)
	rg.Get("/api/v1/sovereigns/{id}/applications/{name}/stream", h.HandleApplicationStream)
	// #3982 — REAL placement targets derived from the live runtime
	// (where the component's Pods actually run, across BOTH region
	// clusters in k8scache). Wires the #3969 placement MODEL to reality
	// so the Topology tab stops showing a uniform false `singleton`.
	// Works for bootstrap HelmReleases (no Application CR) AND CR apps.
	rg.Get("/api/v1/sovereigns/{id}/applications/{name}/placement", h.HandleApplicationPlacement)
	// qa-loop iter-11 Fix #45 Cluster-C: full Application detail
	// (identity + spec + status) so the Sovereign Console SPA's
	// AppDetail page can synthesise an ApplicationDescriptor on the
	// fly when the Application isn't part of the wizard's
	// `selectedComponents` (the typical chroot Sovereign case —
	// Apps installed via `kubectl apply -f application.yaml` or
	// the catalyst-api install endpoint, NOT via the wizard).
	// Without this route the SPA fell into the "App not found"
	// surface for every chroot-installed Application.
	rg.Get("/api/v1/sovereigns/{id}/applications/{name}", h.HandleApplicationGet)
	// qa-loop iter-9 Fix #43, Cluster-B (TC-104): canonical items
	// envelope listing of installed Applications across all Org
	// namespaces on the Sovereign cluster.
	rg.Get("/api/v1/sovereigns/{id}/applications", h.HandleApplicationList)

	// EPIC-2 (#1097) slice T+O+P — Application page bundle.
	// PUT/DELETE on the Application CR + topology / upgrade preview
	// + Blueprint publishing (per-Org) + Curate (sovereign-admin).
	// Per ADR-0001 §2.7 the CR is still the source of truth; PUT/
	// DELETE simply patches/removes it and the application-controller
	// (slice C4 #1133) reconciles. Preview endpoints REUSE the
	// install-preview renderer so "looks-good" is byte-identical to
	// the actual write. Blueprint publishing flows through the
	// unified Gitea client per ADR-0001 §4.3.
	rg.Put("/api/v1/sovereigns/{id}/applications/{name}", h.HandleApplicationUpdate)
	rg.Delete("/api/v1/sovereigns/{id}/applications/{name}", h.HandleApplicationDelete)
	rg.Post("/api/v1/sovereigns/{id}/applications/{name}/topology/preview", h.HandleApplicationTopologyPreview)
	rg.Post("/api/v1/sovereigns/{id}/applications/{name}/upgrade/preview", h.HandleApplicationUpgradePreview)
	rg.Post("/api/v1/sovereigns/{id}/blueprints/publish", h.HandleBlueprintPublish)
	rg.Post("/api/v1/sovereigns/{id}/blueprints/curate", h.HandleBlueprintCurate)
	rg.Get("/api/v1/sovereigns/{id}/blueprints/curatable", h.HandleBlueprintListCuratable)
	// Slice Z3 follow-up: YamlEditor's flux-managed Apply path
	// routes through here. The handler creates a branch + commits
	// new content + opens a PR on `<org>/shared-blueprints` so the
	// edit lands via the GitOps flow rather than side-stepping flux.
	rg.Post("/api/v1/sovereigns/{id}/blueprints/edit-pr", h.HandleBlueprintEditPR)

	// Continuum surfaces are READ-ONLY (ADR-0015 D8). The Continuum CR
	// is delivered from Git (the application-controller renders it into
	// every region file from Application.spec.placement) and Flux would
	// revert any API write; a topology transition is started by editing
	// the Application's placement, and carried out by Continuum as
	// ordered Git commits. Deleted with the imperative sequencer: POST
	// failback / failback/approve, PUT continuum/{name}, PUT
	// continuum/{name}/settings, POST continuum/{name}/switchover/preview.
	rg.Get("/api/v1/sovereigns/{id}/continuums/{name}", h.HandleContinuumGet)
	rg.Get("/api/v1/sovereigns/{id}/audit/continuum", h.HandleContinuumAuditList)
	rg.Get("/api/v1/sovereigns/{id}/audit/continuum/stream", h.HandleContinuumAuditStream)

	// Singular `/continuum/` read aliases: enriched GET, status SSE,
	// fleet aggregate, per-Sovereign summary (continuum_extras.go).
	rg.Get("/api/v1/sovereigns/{id}/continuum/{name}", h.HandleContinuumGetEnriched)
	rg.Get("/api/v1/sovereigns/{id}/continuum/{name}/stream", h.HandleContinuumStream)

	// qa-loop iter-1 prefetch Fix #110 (Continuum DR third batch):
	// adds the rest of the DR contract the SovereignConsole renders +
	// the matrix is expected to assert on going forward —
	// replication-status, switchover-history, quorum status,
	// sovereign-wide replication roll-up,
	// per-Application DR settings GET/PUT. See
	// continuum_dr_extras.go for shapes + fallback semantics.
	// The GET endpoints gate on viewer.
	rg.Get("/api/v1/sovereigns/{id}/continuum/{name}/replication-status", h.HandleContinuumReplicationStatus)
	rg.Get("/api/v1/sovereigns/{id}/continuum/{name}/switchover/history", h.HandleContinuumSwitchoverHistory)
	rg.Get("/api/v1/sovereigns/{id}/continuum/{name}/settings", h.HandleContinuumSettingsGet)
	rg.Get("/api/v1/sovereigns/{id}/dr/quorum/status", h.HandleDRQuorumStatus)
	rg.Get("/api/v1/sovereigns/{id}/dr/replication-status", h.HandleDRReplicationStatus)
}
