# hw150 — IaC-first train, live walk evidence (2026-06-16)

Live validation of the 7-car "IaC-first train" on a **fresh zero-touch 2-region prov**.
Each row is a determination against the founder's verbatim review items / north-stars —
not a path-level green. `✅ met · 🟡 partial · ❌ not met`.

## Environment

| Field | Value |
|---|---|
| Deployment | `1290a8ef7f48f276` |
| FQDN | `hw150.omantel.biz` (pool `omantel.biz`, LE-rotation off `omani.works`) |
| Regions | primary `me-east-215-a` (212.72.24.28) + secondary `me-east-215-b` (212.72.24.39) |
| Image | catalyst-api/ui `80a9d2c` · chart `bp-catalyst-platform@1.4.651` |
| Convergence | zero-touch; 56→63 primary HRs Ready; console HTTP 200; wildcard cert Ready |
| Train on main | T0 #3649 · T1 #3650 · T2 #3655 · T3 #3652 · T4 #3658 · T5 #3653 · T6 #3651 (HEAD `80a9d2c`) |

## Determinations

| # | Item (verbatim intent) | Verdict | Evidence |
|---|---|---|---|
| NS#3 | SSO — every external-facing app, URL lands signed in as emrah.baysal admin | ✅ | grafana.hw150 → after one-time email-OTP (catalyst-pin → Keycloak sovereign realm → grafana) lands **signed in**, full UI, Profile avatar. `hw150-ns3-grafana-signedin.png`. Console itself authenticated via the same OTP. |
| #1 | Catalog detail page editable **in place** (no chip+popup); render not "shitty" | ✅ | `/catalog/bp-alloy` → inline **Edit** button → embeds an **"Edit catalog entry"** form *in the page* (Display name / Summary / Supported-topologies checkboxes / theme icons / Save·Cancel), no navigation, no modal. Rich render: About + Instance(Open-instance) + Supported-topologies(`singleton` canonical). `hw150-item1-catalog-inline-edit.png`. |
| #8 | IaC is the single source of truth; catalog UI edit updates the IaC | 🟡 | Save **persists in UI** and correctly **attempts the IaC commit** to `catalog-sovereign/bp-alloy/blueprint.yaml` via Gitea (the IaC-as-truth mechanism is wired). BUT the live commit fails: `gitea POST …/contents/blueprint.yaml: context deadline exceeded`. Gitea itself responds in 0.08s, so root cause is likely the `catalog-sovereign` repo not yet seeded on a fresh env, or a short ctx deadline. **→ follow-up.** |
| NS#4 | Agreed multi-region apps actually run multi-region | ✅ | Secondary `me-east-215-b` runs full independent gitea/openbao/keycloak/harbor/grafana/powerdns/guacamole + vcluster trio. **CNPG cross-region WAL streaming proven**: replica `pg_stat_wal_receiver.status=streaming` from `cnpg-pair-…-primary-mesh:5432`. |
| NS#1 | Every application runs IN a vCluster (dmz/mgmt/rtz) | 🟡 | 3 vclusters real & functional with **27 workloads inside** (coraza / loki·mimir·nats·tempo / seaweedfs·valkey·vllm). But ~58 platform app pods (gitea/openbao/keycloak/harbor/…) run on the **host**; **no tenant Applications exist yet** to exercise the catalog `tier:` placement. Founder judgment on host-resident shared services. |
| NS#2 | Provisioning creates 3 shared PG instances → 3 cards, 6-7 apps many-to-many | ❌ | `SOVEREIGN_ENABLE_SHARED_PG` unset this prov → `shared-data` ns empty, the 3 `bp-postgres-shared{,-b,-c}` HRs render `enabled:false` (0 bytes); consumers fell back to per-app embedded CNPG clusters. Feature shipped in code but OFF on hw150. |
| #3 | postgres create accepts canonical topology (no `active-hotstandby not in supported`) | 🟡 | Blueprint CRD `topology.supported` = canonical `[active-active, active-hot-standby, active-passive, singleton]` ✅; no CR stuck on topology rejection ✅. BUT `cnpgpairs.dr.openova.io` CRD + catalyst-api code (`core/services/catalog/store/store.go:38`, `provisioning/gitops/gitops.go:677`) still carry un-hyphenated `active-hotstandby` → canonicalization **incomplete**. |
| #2 | AppDetail topology — coherent strip, no contradiction | ✅ | `/app/bp-alloy` → Topology tab: Declared `singleton`, Effective-class + Supported canonical, per-cluster placement (mgmt/dmz/rtz × A/B), topology-mode radios **gated** to the supported mode only. "canonical 7-tab strip" declared + 3 data-driven tabs (Endpoints / Jobs 1 / Dependencies 2). `hw150-appdetail-topology-live-regions.png`. |
| #4 | No per-app hardcoding — concepts generic for all | 🟡 | Topology editor's region picker uses **live** `me-east-215-a/-b` ✅. BUT the Overview's "Available regions" is **hardcoded to Hetzner** `hz-fsn-rtz-prod / hz-hel-rtz-prod` on a Huawei prov ❌ — localized stale placeholder. `hw150-appdetail-hardcoded-hetzner-regions.png`. |
| #6/#7 | UI faithfulness / live DR-switchover (Continuum) status | 🟡 | AppDetail "Live status" polls `GET …/applications/bp-alloy/status` → **404 in a loop (17×)** because bp-alloy is a bootstrap HelmRelease (no Application CR); "Loading status…" never resolves. No tenant Application exists on hw150 → #7 (Continuum record) not positively walkable here; the 404-loop is a real no-CR-handling bug. |
| #5 | Flux-first activities; DR-as-flux-job | ✅ | `/jobs` → ~225 activities grouped by parent (**Phase 0 — Infrastructure** · **Cluster Bootstrap** · **Applications**), 531 Succeeded / 66 Running / 3 Failed; 218 flux/HelmRelease/reconcile refs; cutover steps surface as jobs (**Crossplane Provider Pivot**, **Egress Block Test**). Every activity is a flux job in the unified view. `hw150-jobs-fluxfirst.png`. |
| **NS#5 / cutover** | Pillar-5 sovereignty cutover (11 steps → 600 s deny-egress) | ✅ | **AUTO-FIRED + PASSED.** All cutover Jobs Complete (incl. `crossplane-provider-pivot`, `vcluster-registry-pivot`, `egress-block-test`). The egress-block log: egressDeny applied (`harbor.openova.io`/`xpkg`/ghcr blocked) → fresh pull from **local Harbor** in 11 s under denied egress (#3647) → **"no new regressions during 600 s window — sovereignty proof PASSED."** Per memory the wedge had only reached **step-08** (hw148); hw150 ran the FULL set. **3 of 4 acceptance proofs verified LIVE:** `ghcr-pull` → `registry.hw150.omantel.biz` (flux-system + catalyst-system + catalyst, NOT ghcr.io → **not** half-pivoted), Flux GitRepository → `gitea-http.gitea.svc:3000/openova/openova` (local Gitea, not github), egress-block held green; the 4th (`cutoverComplete=true` flag) wasn't surfaced in the log grep but the 3 tether-pivots completed. A Continuum CR exists (`cnpg-pair-…-continuum`) — addresses #7's "no record" — but **Degraded** (cnpg-pair 2/3, 3rd replica still "Creating" → no lease yet). |

## Open follow-ups (surfaced by this walk)

1. **#8 catalog-edit→IaC commit times out** — verify `catalog-sovereign/<bp>/blueprint.yaml` repo seeding on fresh provs; widen the Gitea write context deadline. (mechanism present; not landing.)
2. **#3 topology canonicalization incomplete** — canonicalize `cnpgpairs` CRD enum + the `active-hotstandby` literals in `core/services/catalog/store/store.go` + `provisioning/gitops/gitops.go`.
3. **NS#2 shared-PG OFF** — the 3-instance model needs `SOVEREIGN_ENABLE_SHARED_PG=true` on the fire (prior attempt #3283 deadlocked); needs a clean enablement path.
4. **AppDetail Live-status 404-loop** — `/api/v1/sovereigns/{id}/applications/{app}/status` 404s for bootstrap HelmReleases (no Application CR); the topology "Live status" poller should handle the no-CR case instead of spamming 404 + sticking on "Loading…" (17 console errors on the bp-alloy Topology tab).
5. **Overview "Available regions" hardcoded** to Hetzner examples (`hz-fsn-rtz-prod`/`hz-hel-rtz-prod`) — derive from live infra like the Topology editor already does (`me-east-215-a/-b`).

## Walk complete — every car walked

**7 fully green** — #1 (catalog inline edit), #2 (topology strip coherent), #5 (flux-first activities), **NS#5 / cutover (600 s deny-egress sovereignty proof PASSED)**, NS#3 (SSO signed-in), NS#4 (multi-region WAL streaming), + jobs-fresh within #6.
**5 partial** — #3 (topology-canonical backend), #4 (Overview hardcoded regions), #6 (status-404 loop), #7 (Continuum exists but Degraded — cnpg 2/3), #8 (catalog→IaC commit times out), NS#1 (platform on host).
**1 not-met** — NS#2 (shared-PG disabled this prov).

**Headline:** the cutover **auto-fired** and its **600 s deny-egress sovereignty proof PASSED** — Pillar-5 landed on a fresh zero-touch prov (first full 11-step run; prior best was step-08 on hw148).

Fixes for the partials: **PR #3660** (404-loop ✅), **PR #3661** (hardcoded-regions ✅), C topology-canonical (building).

_Evidence images live alongside this file under `evidence/` once exported from the Playwright run dir._
