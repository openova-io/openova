package main

import "github.com/go-chi/chi/v5"

func registerPublicInternal(r chi.Router, d routeDeps) {
	h := d.h
	// /api/v1/internal/deployments/import — Sovereign-side receiver for
	// the full deployment record POSTed by the contabo mother at
	// handover time. Mother's fireHandover() ships the record here after
	// the JWT mint completes; child persists it locally so the child's
	// own /api/v1/deployments/{id}/* endpoints answer byte-byte-identical
	// to the mother's view. Closes the data half of the mother→child
	// contract (PR #976 closed the URL routing half).
	//
	// Outside RequireSession: cross-cluster ingress at handover happens
	// before any operator session exists on the child. Validation is
	// instead done by FQDN match against CATALYST_OTECH_FQDN env — a
	// record claiming a different FQDN is rejected.
	r.Post("/api/v1/internal/deployments/import", h.HandleDeploymentImport)

	// /api/v1/internal/jobs/import — Sovereign-side receiver for the
	// mother's handover jobs snapshot (Refs #3263 #3277). The chroot's
	// jobs store is seeded only by its own bootstrap watch (region-a);
	// the secondary regions' install rows live solely in the mothership
	// store, so the chroot /jobs page showed half the regions until the
	// mother started shipping its rows here at handover. Same auth model
	// as /api/v1/internal/deployments/import — no operator session
	// exists on the child at handover time; validation is by FQDN match
	// against CATALYST_OTECH_FQDN + the safe-id regex on deploymentId.
	// Idempotent: jobs.Store.ImportSnapshot never regresses a terminal
	// row and preserves the chroot's own region-a rows.
	r.Post("/api/v1/internal/jobs/import", h.HandleJobsImport)

	// In-cluster Self-Sovereignty Cutover trigger (issue #935 Bug 2).
	// The bp-self-sovereign-cutover chart's auto-trigger Helm
	// post-install Job (templates/10-auto-trigger-job.yaml, added in
	// chart 0.1.16 per issue #933) hits this endpoint to fire the
	// cutover automatically — there is no human session, no browser
	// cookie. The Job authenticates via its projected ServiceAccount
	// token (Authorization: Bearer <SA token bytes>) and the handler
	// validates the token via the apiserver's TokenReview API + checks
	// the resolved username matches the canonical
	// `bp-self-sovereign-cutover-runner` SA the chart authored for this
	// purpose. Same blast radius as the chart's RBAC, expressed at the
	// HTTP edge instead of only at the K8s API edge.
	//
	// MUST live outside RequireSession because the in-cluster Job has
	// no `catalyst_session` cookie. The session-cookie group below
	// rejects every Job request with 401 `unauthenticated`, which is
	// what was hanging cutover on otech113 2026-05-05 (issue #935).
	r.Post("/api/v1/internal/cutover/trigger", h.HandleCutoverInternalTrigger)

	// Billing enforcement from the chargeback application (products/
	// chargeback DESIGN.md §9, EPIC #6867). The collections Enforcer is a
	// Pod with a projected ServiceAccount token and no operator session, so
	// the operator-only suspend/resume routes inside RequireSession below
	// refused every call it made and the Organization was never suspended
	// at the platform. These twins authenticate EXACTLY like the cutover
	// trigger above — TokenReview on the bearer + the allow-list in
	// internal/handler/internal_auth.go (system:serviceaccount:chargeback:
	// chargeback) — and then run the same CR stamp the operator routes do.
	r.Post("/api/v1/internal/organizations/{id}/suspend", h.HandleInternalSuspendOrganization)
	r.Post("/api/v1/internal/organizations/{id}/resume", h.HandleInternalResumeOrganization)
	// #6971 — the BSS spend-limit stop on a grow Organization: the same
	// ServiceAccount door and allow-list, stamping spec.overageStoppedUntil
	// (the org-controller then renders the Organization capped until that
	// instant) and clearing it.
	r.Post("/api/v1/internal/organizations/{id}/overage-stop", h.HandleInternalOverageStopOrganization)
	r.Post("/api/v1/internal/organizations/{id}/overage-resume", h.HandleInternalOverageResumeOrganization)
}
