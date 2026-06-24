# Gap Analysis & Action Backlog — omantel.biz Sovereign (2026-06-24/25)

> **Single consolidated, in-repo entry point for delivery agents.** Each finding links to its canonical GitHub issue (the deep #3370-grade detail lives there); this file is the navigable index + the prioritized action order. Verified live against the permanent omantel.biz Sovereign (dep `4635277cae4ffed9`, kubeconfig `~/.kube/omantel-sovereign.yaml` / contexts `omantel-region-a`,`-b`) and `origin/main`.

## 0. Honest state of the platform
Delivery is **real, not fabricated** — every shipped PR is merged and running (catalyst-api/ui + charts verified live). **But 0 of 5 DoD pillars are *genuinely* shipped**: the gap is **convergence + a completed fresh-prov walk**, not missing code. Multi-region DR is real (cnpg-pair Continuum Healthy, region-a lease held, region-b streaming); per-Org TLS isolation works; console/marketplace/agenity all 200.

## 1. Backlog rebuild (canonical: #4279)
The 16 open + 9 major closes were triaged live: **6 closed** (done — #4143/#4181/#4196/#4249/#4257/#4265), **3 merged** (redundant — #3829/#4018→#4212, #4246→#4272), **2 reopened** (premature closes — #3376, #4180), **#4212** refined to a #3370 EPIC, **4 gap tickets** created (#4275–#4278). Full per-issue verdict + live evidence: **issue #4279** (30 KB comment).

## 2. The four named bugs — root cause + owning ticket
| Bug | Root cause (file:line) | Ticket |
|---|---|---|
| **Marketplace org-create redirect → unreachable domain** | (a) funnel redirects to the per-Org console host *before* its DNS/TLS provision → NXDOMAIN, negative-cached 300s (`core/marketplace/src/lib/config.ts:276`); (b) the purchased app then 404s — `tenant-<slug>-apps` Kustomization `GitRepository "flux-system" not found` (`per_org_flux.go:215`) | **#4273**, **#3376** |
| **/billing/orders empty** | page is native React; the `/api/v1/org/billing/orders` backend handler returns stub/empty data (data-wiring, not UI) | **#4274** |
| **/billing/revenue empty** | same backend-stub root cause | **#4274** |
| **UAT.md inaccuracies** | see §4 | tracked in #4279 |

## 3. Walk-through anomalies (founder, 2026-06-25) — root-caused + fixed
- **① Catalog logo/name editing "dropped"** → **NOT deleted code.** The owner's session was resolving below `admin` tier; the editor is gated on `useCatalogAdmin()`. Owner is in fact already in Keycloak `/sovereign-admins` (live-verified) — durable fix honors `OPERATOR_EMAIL→admin` even when KC is reachable + shows both light/dark logos in a profile-picture view (admin *and* non-admin, with an "edit requires admin" hint). **#4280 → PR #4289 (MERGED).**
- **② Postgres provision fail + "platform" label** → `shared-pg-e` Failed = stale catalog seed forbidding `active-hot-standby` (`catalog-seed/blueprints.yaml:6393`, pinned 0.1.6 vs platform 0.2.5); `shared-pg-d` Pending = the `rtz` vCluster has no CNPG operator (resolved architecturally by the #4293 EPIC — customer Postgres becomes a namespace inside the Org-vcluster). "platform" = by-design host-shared label, not a bug. **#4282 → PR #4287; seed-sync in #4287.**
- **③ "Everything under flux-system"** → **NOT a breach.** Only the Flux HelmRelease *records* live in `flux-system`; workloads run in `spec.targetNamespace` (verified: only ~8 pods in flux-system = the Flux controllers). Fix = a console "Target Namespace" column. **#4281 → PR #4287.**

## 4. UAT.md reconciliation (the principal ledger liability)
`docs/ledger/UAT.md` headline `182 ✅ · 37 ❌ · 3 ⛔` is **stale**. True verdict-cell counts: **205 ✅ · 32 ❌ · 7 ⛔ · 0 ⚠️-verdict** (the 6 raw ⚠️ glyphs are incidental prose). Required edits: re-stamp the headline; re-stamp agenity rows 218–223 to the live `Dashboardws.Tnm8UdB3.js` (0.9.7) against a fresh prov; **add 3 ❌ rows** for the live defects with no row — openclaw 0/1 (#4272), bp-newapi missing-SA hook (#4278), the systemic `tenant-<slug>-apps` source-ref (#3376).

## 5. The GitOps-source-auth root fix (generic, not postgres)
The Flux→Gitea `authentication required` failure is **systemic** — 3 of 21 local-Gitea sources dead because `secretRef` is wired per-controller and drifted (application/catalog-sovereign/env legs missing it; org leg correct). Permanent fix = a single `core/controllers/pkg/fluxsource` builder that makes an unauthenticated local-Gitea source a compile/CI failure. **#4285 → PR #4288 (MERGED), supersedes #4284.**

## 6. Architecture EPIC — vcluster boundary correction (#4293)
Founder-confirmed inversion: **one vcluster = one Organization** (control-plane isolation only); platform planes (mgmt/rtz/dmz) → native host namespaces with per-component default-deny CNP.
- **#4290 (A)** collapse the 4 Org-provisioning doors → org-controller single source (re-point ~14 `tenant-<slug>` sites). *(building)*
- **#4292 (B)** Org-vcluster as enforced boundary: enable `sync.toHost.networkPolicies` (set nowhere today → intra-Org policy silently inert), plan-templated ResourceQuota (S/M/L/XL = 2/4/8/16 vCPU) + LimitRange + Guaranteed/Burstable QoS split. *(fires after A)*
- **#4291 (C)** de-vcluster mgmt/rtz/dmz via `placement.yaml` — safe because every singleton operator already runs host-side. *(building)*
Open tier decision (default chosen, flippable): free/S share the host namespace; paid M+ get a dedicated vcluster.

## 7. Prioritized action order for delivery agents
1. **P0 — customer can't use their purchase:** #3376 (funnel app 404) + #4273 (NXDOMAIN race) + the GitOps-auth roll (#4288).
2. **P1 — agentic North Star:** #4111 / #4277 (openbao auto-seed) / #4272 (openclaw netpol) / #4180 / #4276.
3. **Architecture inversion:** #4293 (A #4290 → B #4292; C #4291 in parallel) — code → fresh-prov validation → *then* a sequenced live migration (never rip-and-replace omantel.biz).
4. **Operator polish / hygiene:** #4274 (billing data), the UAT.md re-stamp (§4), the ~35 lagging catalog seeds.

_Canonical deep detail per item lives in the linked GitHub issues; this file is the index. Provenance: gap-analysis workflow `wf_3b8cf2f5-3c1` (#4279), anomaly RCA `wf_230ccbea-836`, GitOps-auth sweep `wf_1badb64e-3f3` (#4285), vcluster EPIC `wf_f6cd8308-7ff` (#4293)._
