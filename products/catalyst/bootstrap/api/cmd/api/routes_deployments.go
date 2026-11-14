package main

import (
	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

func registerPublicDeployments(r chi.Router, d routeDeps) {
	h := d.h
	// /api/v1/deployments/in-flight-count — public, read-only count of
	// deployments in any Phase-0 in-flight status (pending /
	// provisioning / tofu-applying / flux-bootstrapping). The CI
	// deploy-bot (.github/workflows/catalyst-build.yaml) polls this
	// before pushing a values.yaml image-SHA bump, to avoid rolling the
	// catalyst-api Pod mid-tofu-apply (the OpenTofu workdir lives on a
	// /tmp emptyDir that dies with the Pod, abandoning the prov and
	// leaking Hetzner resources). MUST live outside RequireSession —
	// the deploy-bot has no session cookie and runs from a GHA runner.
	// Same posture as /healthz, /readyz, /api/v1/version. The response
	// is count+IDs only; no FQDNs or owner emails. See handler/
	// deployments_in_flight_count.go for the full rationale and the
	// t13/t17/t21 incident history that motivated this gate.
	r.Get("/api/v1/deployments/in-flight-count", h.InFlightCount)

	// Unauthenticated cloud-init postback (issue #183, Option D + #634).
	// The new Sovereign's control plane PUTs its rewritten kubeconfig
	// here with `Authorization: Bearer <postback-token>`. PutKubeconfig
	// has its own SHA-256-hash-vs-stored-hash compare — it MUST live
	// outside the session-cookie middleware because cloud-init has no
	// browser cookies. Putting this inside the RequireSession group
	// rejected every postback with 401 {"error":"unauthenticated"} and
	// stuck Phase-1 in PENDING forever (caught live on otech23).
	r.Put("/api/v1/deployments/{id}/kubeconfig", h.PutKubeconfig)

	// #3132 — the control-plane cloud-init pushes /var/log/cloud-init-output.log
	// here on a loop (same bearer router as the kubeconfig PUT; cloud-init has no
	// session cookie). The log cannot be PULLED on kom4dc HCS — no ECS
	// console-output API, no reachable sshd (verified hw106) — so the node pushes
	// it; this is the only diagnosis path for a Phase-1 cloud-init failure (#3129).
	r.Put("/api/v1/deployments/{id}/cloudinit-log", h.PutCloudInitLog)
}

func registerSessionDeployments(rg chi.Router, d routeDeps) {
	h := d.h
	// NOTE: wizard pre-submit validation endpoints
	// (/credentials/validate, /credentials/object-storage/validate,
	// /sshkey/generate, /registrar/{r}/validate, /subdomains/check)
	// are registered OUTSIDE the session group at the top of this
	// function — they are called from the wizard BEFORE the operator
	// authenticates and gating them on a session cookie produces a
	// 401 every time, blocking the only flow that matters.
	rg.Post("/api/v1/deployments", h.CreateDeployment)
	// List endpoint (issue #747) — wizard auto-redirect to /provision/<id>
	// when the signed-in operator already has an in-flight deployment.
	// Filtered server-side by the X-User-Email header injected by
	// RequireSession; the ?owner= query param is a client hint that is
	// silently overridden when a session is present.
	rg.Get("/api/v1/deployments", h.ListDeployments)
	rg.Get("/api/v1/deployments/{id}", h.GetDeployment)
	// Record-only delete (issue #178). Removes the deployment from
	// the in-memory map + on-disk store + kubeconfig file. Does NOT
	// touch Hetzner — for the "kill the kid" path the operator's UI
	// POSTs /wipe instead (which already chains record-delete on
	// success). Refuses adopted (422) + in-flight (409) deployments
	// to keep the customer breadcrumb + Commit safety intact.
	rg.Delete("/api/v1/deployments/{id}", h.DeleteDeployment)
	rg.Get("/api/v1/deployments/{id}/logs", h.StreamLogs)
	// Buffered event history endpoint (issue #180). Returns the full event
	// slice + state JSON so the wizard's ProvisionPage can render history
	// for a deployment that already finished — the SSE replay-on-connect
	// covers the same path, but the GET is a stateless fast-path test
	// + reconnect target.
	rg.Get("/api/v1/deployments/{id}/events", h.GetDeploymentEvents)
	// Kubeconfig endpoint — wizard StepSuccess "Download kubeconfig"
	// button + Sovereign Admin break-glass download + the source the
	// internal/helmwatch HelmRelease watcher reads from when the
	// catalyst-api Pod cold-starts mid-Phase-1 and has to reattach
	// to a deployment whose kubeconfig is on the PVC.
	rg.Get("/api/v1/deployments/{id}/kubeconfig", h.GetKubeconfig)
	// #3132 — GET the last-uploaded cloud-init log for post-mortem
	// diagnosis of a Phase-1 failure (ownership-checked, like the
	// kubeconfig GET). The PUT side is on the bearer router above.
	rg.Get("/api/v1/deployments/{id}/cloudinit-log", h.GetCloudInitLog)
	// Phase-retry endpoint for the wizard's failed-phase UX (issue #125).
	// Phase 0 retries re-run `tofu apply` against the existing workdir;
	// Phase 1 retries emit operator instructions per the architectural
	// contract (Flux owns Phase 1 reconciliation).
	rg.Post("/api/v1/deployments/{id}/phases/{phase}/retry", h.RetryPhase)
	// Cancel & Wipe endpoint (issue #318). Operator-triggered purge of a
	// failed or abandoned deployment: tofu destroy + Hetzner orphan purge
	// + PDM release + local state cleanup. Idempotent. Returns 200 with a
	// PurgeReport summary. The wizard's failed-state banner renders the
	// operator confirmation modal that POSTs here.
	rg.Post("/api/v1/deployments/{id}/wipe", h.WipeDeployment)
	// DNS re-publish endpoint (Refs #3265 #3263) — idempotent re-trigger
	// that writes the CURRENT CanonicalSovereignSubdomains list into the
	// parent PowerDNS zone for an EXISTING deployment.  Used when the
	// canonical subdomain list grows after a Sovereign was provisioned (e.g.
	// hw126 was provisioned before #3265 added "newapi" + "pdns-admin"), so
	// operators can pick up the new A records WITHOUT a full re-provision.
	// Inputs (FQDN, LB IP, parent zones) are reconstructed entirely from
	// the persisted Deployment record — no external API calls.  The
	// PowerDNS PATCH is REPLACE-idempotent; re-running is always safe.
	rg.Post("/api/v1/deployments/{id}/republish-dns", h.HandleRepublishDNS)
	// Handover JWT — issue #605 (minting) + issue #606 (consumption).
	// MintHandoverToken: Catalyst-Zero operator finalises a deployment;
	// wizard StepSuccess POSTs here to get a one-time RS256 JWT, then
	// redirects the operator's browser to the Sovereign console URL.
	// GetHandoverPublicKey: Sovereigns fetch the JWK at boot to seed
	// their CATALYST_HANDOVER_JWT_PUBLIC_KEY_PATH (or via cloud-init).
	// AuthHandover: Sovereign-side receiver — validates the JWT, creates
	// the operator in Keycloak, exchanges for a session, sets cookies,
	// and redirects to /console/dashboard.
	rg.Post("/api/v1/deployments/{id}/mint-handover-token", h.MintHandoverToken)
	// Subdomain-only release endpoint (issue #489). Releases the PDM
	// allocation row for a failed-or-abandoned deployment WITHOUT
	// requiring the operator to re-enter their HetznerToken. Lets a
	// franchise customer retry under the same pool subdomain after a
	// botched provision instead of being forced to pick acmeN+1. Does
	// NOT touch Hetzner — the Cancel & Wipe flow remains the canonical
	// path for live cloud cleanup. Refuses on in-flight deployments
	// (409), wiped deployments (410), or adopted Sovereigns (422).
	rg.Delete("/api/v1/deployments/{id}/release-subdomain", h.ReleaseSubdomain)
	// Jobs/Executions REST surface — the canvas + per-job detail
	// pages read this in parallel to the existing SSE events feed.
	// All endpoints are read-only; every mutation flows through the
	// helmwatch bridge in internal/jobs. Each Job carries parentId +
	// childIds so the FE can render the recursive Job tree without
	// any batch-specific endpoint (issue #351).
	rg.Get("/api/v1/deployments/{depId}/jobs", h.ListJobs)
	// The jobs stream: one hint frame per store write for the deployment,
	// so a reader refetches instead of polling (internal/jobs/stream.go).
	rg.Get("/api/v1/deployments/{depId}/jobs/stream", jobs.StreamHandler(h.JobsReadAccess))
	rg.Get("/api/v1/deployments/{depId}/jobs/{jobId}", h.GetJob)
	// Generic Flux-native remediation (issue #3646 §5c). ONE write
	// route dispatching on the leaf's typed Kind re-drives ANY stuck
	// reconcile (HelmRelease / Kustomization / CronJob / reconciler
	// Deployment / batch Job) from the console — the operator never
	// drops to kubectl. Owner-checked (404 cross-tenant) + RBAC-gated
	// to operator tier (403 otherwise); writes a new Execution with
	// the operator identity; NEVER shells out (annotation via the
	// in-cluster dynamic client only). Inside RequireSession so the
	// operator's claims are populated for the RBAC gate + audit line.
	rg.Post("/api/v1/deployments/{depId}/jobs/{jobId}/retry", h.RetryJob)
	// Backfill endpoints — give the FE an explicit handshake to
	// re-attach the helmwatch goroutine after a Pod restart and to
	// snapshot the in-memory informer cache. The bridge seeds a Job
	// per HR observed on initial-list so HRs that have been
	// Ready=True for an hour materialise rows immediately rather
	// than only on state transitions.
	rg.Post("/api/v1/deployments/{depId}/refresh-watch", h.RefreshWatch)
	rg.Get("/api/v1/deployments/{depId}/components/state", h.GetComponentsState)
	// #3925 surface B — the bounded Flux dependency DAG for the
	// Convergence-Monitor Reconciliation page. Nodes = HelmReleases +
	// Kustomizations (the declared desired set), edges = real
	// dependsOn, coloured by live reconcile state (Reconciled /
	// Reconciling / Drifted / Degraded). Read-only. Scanner/Job nodes
	// are EXCLUDED by construction (it's built from reconcilers only).
	rg.Get("/api/v1/deployments/{depId}/reconciliation", h.GetReconciliationDAG)
	// #3996 — lightweight ArgoCD/Flux MANAGEMENT surface on the recon
	// lens. LIST every manageable Flux reconciler (HelmRelease /
	// Kustomization / GitRepository / OCIRepository / HelmRepository /
	// HelmChart) with live status + last-reconcile + revision + suspended
	// flag; DRILL into one object's owning-controller LOGS filtered to
	// that object; and TRIGGER reconcile/suspend/resume via the
	// in-cluster dynamic client (annotation / spec.suspend patch — NEVER
	// a shell-out). The action route is owner-checked (404 cross-tenant)
	// + operator-RBAC-gated (403 otherwise), the SAME gate the jobs-retry
	// endpoint uses. Read routes are inside RequireSession so claims are
	// populated; the action route reads them for the RBAC gate + audit.
	rg.Get("/api/v1/deployments/{depId}/reconcilers", h.ListReconcilers)
	rg.Get("/api/v1/deployments/{depId}/reconcilers/{kind}/{ns}/{name}/logs", h.GetReconcilerLogs)
	rg.Post("/api/v1/deployments/{depId}/reconcilers/{kind}/{ns}/{name}/{action}", h.ReconcilerAction)
	// Sovereign Infrastructure surface — read only (issue #227).
	// The endpoints compose from the deployment record + live
	// cluster informer cache. They write nothing.
	rg.Get("/api/v1/deployments/{depId}/infrastructure/topology", h.GetInfrastructureTopology)
	rg.Get("/api/v1/deployments/{depId}/infrastructure/compute", h.GetInfrastructureCompute)
	rg.Get("/api/v1/deployments/{depId}/infrastructure/storage", h.GetInfrastructureStorage)
	rg.Get("/api/v1/deployments/{depId}/infrastructure/network", h.GetInfrastructureNetwork)

	// Sovereign IAM — UserAccess CR editor (issue #323). The UI's
	// /sovereign/users page calls these endpoints to list / create /
	// update / delete UserAccess CRs against the Sovereign cluster.
	// The CRD shape (`access.openova.io/v1alpha1`) is shipped by
	// issue #322's chart; catalyst-api consumes it via dynamic
	// client so there's no Go-type build dependency between the
	// two PRs.
	rg.Get("/api/v1/deployments/{depId}/admin/user-access", h.ListUserAccess)
	rg.Post("/api/v1/deployments/{depId}/admin/user-access", h.CreateUserAccess)
	rg.Put("/api/v1/deployments/{depId}/admin/user-access/{name}", h.UpdateUserAccess)
	rg.Delete("/api/v1/deployments/{depId}/admin/user-access/{name}", h.DeleteUserAccess)
}
