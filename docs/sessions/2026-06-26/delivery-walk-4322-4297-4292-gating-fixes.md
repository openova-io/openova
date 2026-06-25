# 2026-06-26 — omantel.biz (kom4dc dep 4635277cae4ffed9) delivery walk: #4360 close + gating-fix delivery

## Goal
LIVE delivery-and-walk to close #4360 (egress) + #4322 (wordpress cart) + #4297/#4292 (m-tier keystone).

## Delivery state at session start
- bp-plane-isolation 0.1.2 (#4361) **delivered, Ready=True** — #4360 egress fix is durable (no hot-patch).
- bp-gitea HR **rollback-looping 1.2.43→1.2.42**: the #4367 git-data preservation hook defaulted its
  guard image to a bare docker.io ref → kyverno harbor-proxy-pull DENIED the pre-upgrade Job → blocked
  the WHOLE catalyst-platform chain (bp-catalyst-platform / continuum / sandbox / self-sovereign-cutover).
- org-services/provisioning **CrashLoop** `nats connect: no such host` — NATS_URL default still pointed at
  the dead mgmt-vcluster-syncer-mangled name after the #4291/#4325 de-vcluster re-home.

## Closed
- **#4360 — CLOSED (VERIFIED-PASS)**. bp-plane-isolation 0.1.2 egress rule now carries
  `namespaceSelector:{}` (Cilium-correct in-cluster allow); gitea `/api/healthz` database:ping = pass;
  policy is Helm/Flux-owned, not a hot-patch.

## Shipped (gating fixes)
- **gitea hook harbor-proxy** — operator/peer merged equivalent as **#4377** (chart 1.2.44). My duplicate
  PRs #4370 closed. Live: bp-gitea converged **Ready=True on 1.2.44** (release v22); guard pod ran with
  `harbor.openova.io/proxy-dockerhub/bitnamilegacy/kubectl:1.30.7` (Kyverno admitted).
- **#4378 (MERGED, 1.4.851)** — org-services NATS_URL default → host `nats-jetstream.nats-system.svc`
  (4 locations) + the 3 stale controller pins (environment/blueprint/application) 895f961→8c532a7 (delivers
  #4363) + slot-13 pin sync (deploy-bot 1.4.850 bump never synced it). Superseded peer's conflicting #4372.
  Live: org-services-config NATS_URL now host-ns; catalyst-platform rolling 1.4.851.

## Keystone code verification (#4297/#4292)
The keystone is fully implemented and LIVE in org-controller image 405ee6d (#4316 / 2fc31ca):
- per_org_flux.go: vcluster-tier Orgs get apps Kustomization `spec.kubeConfig.secretRef` → apps reconcile
  INTO the Org vcluster; host-tier (free/S) apply to host ns. Tier-gate `boundaryIsVcluster` (free/S=host,
  m/l/xl=vcluster).
- manifests.go: ResourceQuota + LimitRange (Guaranteed via maxLimitRequestRatio 1:1) + K8s default-deny NP
  + CiliumNetworkPolicy `allow-gateway-and-apiserver` (fromEntities ingress/host/remote-node, toEntities
  kube-apiserver), all co-located in the apps tree.
- LIVE GAP: all 164 reconciled Orgs are `apps_boundary_vcluster:false` (host-tier). No m-tier Org has ever
  exercised the vcluster keystone path live — needs an m-tier funnel Org once provisioning recovers.

## New issues filed
- **#4369** — bp-gitea 1.2.43 guard image bare docker.io ref → kyverno deny (fixed by #4377).
- **#4373** — org-services NATS_URL stale mgmt-vcluster mangled name (fixed by #4378).
- **Pending observation**: the #4354/#4377 gitdata-guard hardcodes pgCluster=gitea-pg and probes it in the
  gitea ns; on a SHARED-PG Sovereign (gitea uses shared-pg in shared-data ns) PG_POD is empty → guard falls
  to case-4 fresh-prov exit 0 (does NOT deadlock) BUT each kubectl call took ~2.5min (gitea ns lacks a
  toEntities:[kube-apiserver] CNP → Cilium throttles apiserver egress). Guard still completed; gitea Ready.

## #4322/#4297/#4292 status
Code + charts all delivered (wordpress-tenant 0.4.19 carries the #4322 oidc fixes). Live VERIFICATION gated
on provisioning recovering (NATS + gitea token), which is in flight as the catalyst-platform 1.4.851 roll
completes. Left at status/uat with the precise convergence path.
