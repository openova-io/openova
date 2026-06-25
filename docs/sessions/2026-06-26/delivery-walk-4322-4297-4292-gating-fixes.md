
## FINAL OUTCOME (end of session)

### Closed (verified live)
- **#4360** — bp-plane-isolation 0.1.2 egress fix (VERIFIED-PASS, gitea db:ping pass).
- **#4369** — gitea hook harbor-proxy image (fixed by merged #4377; gitea Ready 1.2.44 / release v22, guard pod admitted).
- **#4373** — org-services NATS host-ns + controller pins 8c532a7 (fixed by merged #4378, chart 1.4.851; NATS config live, provisioning up, #4363 delivered).
- **#4382** — bp-plane-isolation gitea ingress allowlist + org-services (fixed by merged #4383, 0.1.3; provisioning init flipped HTTP 000→200).

### Merged PRs
- #4377 (gitea hook, 1.2.44 — operator/peer), #4378 (NATS+controllers, 1.4.851 — me, superseded peer #4372/#4374), #4383 (gitea ingress, 0.1.3 — me).

### Live convergence proven
gitea Ready 1.2.44 → bp-self-sovereign-cutover Ready (Pillar 5) → NATS host-ns config delivered → provisioning Running 1/1 → fresh host-tier funnel Org `g6wpwalk` (POST /tenant/orgs apps:[wordpress]) = HTTP 201 → Org CR Active + GitRepository Ready + #4292 boundary set (ResourceQuota/LimitRange/default-deny NP/gateway+apiserver CNP) applied LIVE to the host ns + apps Kustomization Ready (kubeConfig empty = host-tier tier-gate working).

### Remaining (status/uat, NOT closed)
- **#4322** — wordpress cart still doesn't land: the day-2 install commits to the wrong repo (#4384). Chart 0.4.19 oidc fixes ready but can't be exercised.
- **#4384 (NEW)** — funnel cart day-2 install commits to global openova/openova (empty-SHA tree 404) instead of per-Org <slug>/catalyst-tenant. The real remaining #4322/#4179-divergence cause. Needs a provisioning code fix.
- **#4297/#4292** — host-tier boundary + tier-gate LIVE-VERIFIED; m-tier vcluster-kubeConfig path code-confirmed (org-controller 405ee6d/#4316) but not live-walked (no m-tier Org with apps; blocked by #4384).
