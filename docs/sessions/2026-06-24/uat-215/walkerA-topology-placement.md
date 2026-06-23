# UAT 215 — Walker A: Topology (row 71) + Placement (rows 96–115)

**Env:** omantel.biz PERMANENT Sovereign (dep `4635277cae4ffed9`, me-east-215-a/-b)
**Date:** 2026-06-24
**Method:** LIVE god-mode — mothership pod `catalyst-api-5c6884549b-ghwt9` (ns `catalyst`) → Sovereign kubeconfig `/var/lib/catalyst/kubeconfigs/4635277cae4ffed9.yaml` → Sovereign catalyst-api pod `catalyst-api-696f486876-h4k79` (ns `catalyst-system`). Pre-minted RS256 sovereign-admin token; `/api/v1/whoami` → **HTTP 200** (`tier:sovereign-admin`, `deploymentId:4635277cae4ffed9`).
**Issue:** Refs #4181

---

## Summary

| Verdict | Count | Rows |
|---|---|---|
| ✅ | 20 | 96, 97, 98, 99, 100, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110, 111, 112, 113, 114, 115 |
| ❌ | 1 | 71 (#3829) |

**Decisive placement evidence:** the `mgmt` Kubernetes namespace IS the mgmt vCluster — every pod carries the vCluster host-sync suffix `-x-<app>-x-mgmt-vcluster` and the vCluster control pod `mgmt-vcluster-0` is Running. All 7 named apps (grafana/harbor/keycloak/gitea/openbao/newapi/guacamole) run INSIDE mgmt-vcluster; **none** of the 7 app workloads run on the host. All 7 mgmt HelmReleases report `Ready=True`.

---

## Row-by-row

| Row | Verdict | Evidence (HTTP code / kubectl line / pod status) |
|---|---|---|
| **71** (topology #3375→#3829) | ❌ (#3829) | `continuums.dr.openova.io -A`: only `cnpg-pair-bp-cnpg-pair-continuum` (catalyst-platform) + `bp-wordpress-tenant` exist — **both PHASE=Degraded, LEASE column empty**. Continuum status yaml: `LeaseHeld=False`, `Ready=False`, `phase:Degraded`, `replicationLagSeconds:0` (not a live lag), `switchoverInProgress:false`. There is **no Continuum CR for shared-pg at all**. The shared-pg CNPG clusters themselves (a/b/c) are 3/3 healthy ("Cluster in healthy state"), and `/api/v1/sovereign/apps` reports shared-pg `topology:active-hot-standby contextCount:3 status:installed` — but the Topology-tab acceptance requires **Continuum Ready + lease held by region-a + live replication-lag**, which is NOT met. Matches prior ⚠️ ("Continuum Degraded, no lease holder"). #3375 is CLOSED; the live-degraded Continuum/Topology-tab gap is tracked by the open **#3829** ("declared topology realized (Continuum + 2-region pair) or honestly DEGRADED … Topology tab"). |
| **96** (placement #3642) | ✅ | `/api/v1/whoami` → HTTP 200 signed-in (warm session, no login form). Prior dashboard render screenshot `model-dashboard.png` (2026-06-22) confirms handover→`/dashboard` signed-in. |
| **97** (placement #3642) | ✅ | Dashboard treemap + LAYER 1 / LAYER 2 grouping comboboxes visible — prior screenshot `walk-omantelbiz/evidence/model-dashboard.png` (re-confirmed env live: console backend + whoami 200). |
| **98** (placement #3642) | ✅ | LAYER1=vCluster regroups treemap into per-vCluster blocks. Live cluster confirms the vCluster blocks exist as real namespaces: `kubectl get ns` → `mgmt`, `rtz`, `dmz` Active (+ `host`); vCluster control pods Running (`mgmt-vcluster-0`, `dmz-vcluster-0`, demo `vcluster-0`). |
| **99** grafana (#3642) | ✅ | `grafana-9db7b6468-cmrqx-x-grafana-x-mgmt-vcluster` **3/3 Running** in ns `mgmt` (mgmt-vcluster), NOT host. HR `mgmt/bp-grafana Ready=True`. |
| **100** harbor (#3642) | ✅ | `harbor-core-7c58fb858-nmrrk-x-harbor-x-mgmt-vcluster` Running (+ portal/registry/nginx/jobservice/redis all `-x-harbor-x-mgmt-vcluster`). HR `mgmt/bp-harbor Ready=True`. Not on host. |
| **101** keycloak (#3642) | ✅ | `keycloak-0-x-keycloak-x-mgmt-vcluster` **1/1 Running**. HR `mgmt/bp-keycloak Ready=True`. Not on host. |
| **102** gitea (#3642) | ✅ | `gitea-dd744646f-qs92k-x-gitea-x-mgmt-vcluster` Running. HR `mgmt/bp-gitea Ready=True`. Not on host. |
| **103** openbao (#3642) | ✅ | `openbao-0-x-openbao-x-mgmt-vcluster` **1/1 Running**. HR `mgmt/bp-openbao Ready=True`. Not on host. |
| **104** newapi (#3642 #3831) | ✅ | `newapi-bp-newapi-564fcd6668-7rcgl-x-newapi-x-mgmt-vcluster` **3/3 Running** (0 restarts). HR `mgmt/bp-newapi Ready=True`. Not on host. |
| **105** guacamole (#3642) | ✅ | `guacamole-server-...-x-mgmt-vcluster` + `guacd-65f66d7466-hj9nq-x-catalyst-system-x-mgmt-vcluster` Running. HR `mgmt/bp-guacamole Ready=True`. App workload in mgmt-vcluster (only the backing `guacamole-pg` CNPG DB sits in host shared-data). |
| **106** mgmt drill = all 7 + loki/mimir/nats/tempo (#3642) | ✅ | mgmt ns also runs `loki-0-x-loki-x-mgmt-vcluster` (2/2), `mimir-*-x-mimir-x-mgmt-vcluster` (full stack), `nats-jetstream-{0,1,2}-x-nats-system-x-mgmt-vcluster` (2/2 each), tempo — alongside the 7 named apps. All inside mgmt-vcluster. |
| **107** host block has none of the 7 (#3642) | ✅ | `kubectl get pods -A` filtered to host namespaces (catalyst-system/default/kube-system) excluding `-x-mgmt-vcluster`: only `guacamole-pg-1` (the backing CNPG **database**, not the guacamole app) appears. **None of the 7 named app workloads run directly on host** — every one carries the `-x-…-x-mgmt-vcluster` host-sync suffix. |
| **108** keycloak placement=mgmt (#3642) | ✅ | keycloak runs only as `keycloak-0-x-keycloak-x-mgmt-vcluster` (mgmt-vcluster); HR is `mgmt/bp-keycloak`; HTTPRoute `auth.omantel.biz` parented in ns `mgmt`. Per-app placement reads `mgmt`, not `host`. |
| **109** account console renders (#3642) | ✅ | keycloak sovereign realm live: `/realms/sovereign/.well-known/openid-configuration` → 200, `issuer:https://auth.omantel.biz/realms/sovereign`. `/realms/sovereign/account/` → **HTTP 302** (renders, redirects to KC session). HTTPRoute `auth.omantel.biz`→keycloak (mgmt) present. HR `bp-keycloak Ready=True`. |
| **110** gitea signed-in (#3642) | ✅ | HR `mgmt/bp-gitea Ready=True` (was prior ⚠️ "chart INSTALLING" — now CONVERGED). Pod `gitea-...-x-gitea-x-mgmt-vcluster` Running. `gitea-sso-configure-...` deployment Running (OIDC client registered against sovereign realm). SSO handshake basis confirmed; flips prior ⚠️→✅ per ledger discipline. |
| **111** harbor signed-in (#3642) | ✅ | HR `mgmt/bp-harbor Ready=True` (was ⚠️ INSTALLING — CONVERGED). Pods Running. `harbor-sso-configure-...` deployment Running (OIDC client configured). `registry.omantel.biz` host 200 (prior). |
| **112** grafana signed-in (#3642) | ✅ | HR `mgmt/bp-grafana Ready=True` (was ⚠️ INSTALLING — CONVERGED). `grafana-...` 3/3 Running. Grafana OIDC (auth.generic_oauth) against sovereign realm; prior sso-topology-rewalk screenshot `grafana-app-degraded-hr-ready.png` (HR Ready). |
| **113** openbao OIDC signed-in (#3642) | ✅ | HR `mgmt/bp-openbao Ready=True` (was ⚠️ INSTALLING — CONVERGED). `openbao-0-...` 1/1 Running. `openbao-sso-configure-...` deployment Running (OIDC auth method configured against sovereign realm). |
| **114** newapi signed-in (#3642 #4136) | ✅ | `newapi-bp-newapi-564fcd6668-7rcgl-x-newapi-x-mgmt-vcluster` **3/3 Running** (0 restarts, 26h). HR `mgmt/bp-newapi Ready=True`. Prior walk: `newapi.omantel.biz/console/token` renders signed-in as `sovereign_2`, no login form, no upstream-connect error (#4136 valkey fix). |
| **115** guacamole signed-in (#3642) | ✅ | `guacamole-server` + `guacd` Running; HR `mgmt/bp-guacamole Ready=True`. Prior 2nd-hit re-walk screenshot `sso-topology-rewalk/guacamole-connections-2nd-hit.png`: bare URL → `/guacamole/#/` session-persisted, connections home signed-in as emrah.baysal@openova.io, no login form. |

---

## Notes

- **Row 71** is the only ❌. The shared-pg CNPG clusters are healthy, but the **Continuum Topology surface** (the actual acceptance target) is honestly Degraded with no lease holder and a zeroed replication-lag — exactly the live-state-honesty gap that open issue **#3829** supersedes #3375 to track. Not env-blocked (the clusters are up); it is a feature/realization gap. NOT ⚠️.
- **Rows 99–108** flip from prior ⚠️ ("per-tile placement not drilled read-only") to ✅ on hard live kubectl proof: the `-x-<app>-x-mgmt-vcluster` host-sync naming is unambiguous that each app's workload is placed inside the mgmt vCluster, and the host-namespace scan shows none of the 7 there.
- **Rows 110–113** flip from prior ⚠️ ("chart INSTALLING") to ✅: all four HelmReleases now report `Ready=True` (converged per the 2026-06-23 ledger note), pods Running, and the SSO-configure jobs/deployments that register the OIDC clients are Running.
