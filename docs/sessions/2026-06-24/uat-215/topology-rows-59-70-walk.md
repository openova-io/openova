# UAT-215 topology epic (#3375) — rows 59–70 god-mode walk

**Env:** omantel.biz PERMANENT Sovereign, dep `4635277cae4ffed9`, regions me-east-215-a (primary) + me-east-215-b.
**Date:** 2026-06-24. **Walker:** signed-in sovereign-admin (minted handover JWT), live kubectl via mothership pod + live API + Playwright browser.
**Health-gate:** PASS — front-doors live (console 200, guacamole/pdns 302, api 404=live), mothership pod Running, JWT valid. Sovereign-level console rollup chip reads **CONVERGING** (secondary-region mgmt-vCluster not fully converged — see rows 67/70).

## Live evidence

### CNPG clusters (primary region) — `kubectl get cluster.postgresql.cnpg.io -A`
```
shared-data   shared-pg     3/3 READY  Cluster in healthy state
shared-data   shared-pg-b   3/3 READY  Cluster in healthy state
shared-data   shared-pg-c   3/3 READY  Cluster in healthy state
cnpg          cnpg-pair-bp-cnpg-pair-primary  3/3 READY  healthy
```
→ active-hot-standby CNPG **provisioning machinery is proven green** (3× shared-pg trios Ready).

### Continuum / CNPGPair (the live DR objects) — DR surface is ABSENT
```
kubectl get continuum -A          -> No resources found
kubectl get cnpgpair -A           -> No resources found
GET /api/v1/sovereigns/$DEP/continuums/shared-pg -> 404 continuum-not-found
```
CRD `continuums.dr.openova.io` + `cnpgpairs.dr.openova.io` exist, but **zero objects** → no live DR/lease/replication-lag status anywhere.

### shared-pg Application status (API) + Topology tab (Playwright)
- API: `phase: Ready`, `spec.placement: active-hot-standby`, condition = "Helm upgrade succeeded … bp-postgres@0.2.5". No DR/lease/lag fields.
- Browser `/app/shared-pg` → Topology tab renders **`Placement → Pattern: singleton`**, ONE card `hw-me-east-215-a-rtz-prod · mgmt · ● PRIMARY · serves writes`, Status = "n/a — bootstrap component (HelmRelease, no Application CR)". **No region-b standby, no Switchover, no replication-lag, no Continuum.**
- Screenshot: `evidence/topo-row62-sharedpg-topology-singleton-no-continuum.png`

### Cloud / regions view — rows 65/66 GREEN
- API `infrastructure/topology`: pattern=multi-region, **2 regions** (me-east-215-a + me-east-215-b), each 1 healthy cluster, 6 nodes each, status=healthy. No phantom region.
- Browser `/cloud`: **Region 2/2 · Cluster 2/2 · LoadBalancer 2/2 · WorkerNode 24/24**, both me-east-215-a/-b with full pools. Screenshot: `evidence/topo-row65-cloud-region-cluster-2of2.png`

### App health both regions — `kubectl get hr/pods` primary (A) + region-B kubeconfig (B)
| app | region A | region B |
|---|---|---|
| grafana | 3/3 Running, HR `bp-grafana@1.0.16` **True** | pod **2/3 CreateContainerConfigError** — `secret "grafana-sso-oidc-credentials-…" not found` (HR True but pod degraded) |
| keycloak | 1/1 Running, HR **True** | **1/1 Running**, HR `bp-keycloak@1.4.38` **True** (prior region-B blocker CLEARED) |
| guacamole | server+guacd Running, HR `bp-guacamole@0.2.27` **True** | HR **False** — post-install `guacamole-bp-guacamole-jdbc-seed` job DeadlineExceeded; guacamole-pg-initdb **Pending** |
| powerdns-admin | 1/1 Running, HR `bp-powerdns-admin@0.1.18` **True**, dashboard serves (302→/oidc/login) | HR Unknown, pod Init:0/1 (still installing) |

Region-B grafana/guacamole blocker = **#4158** (OPEN — "Secondary region: mgmt-vCluster keycloak (+gitea/harbor/grafana) FailedMount — mangled vc-mgmt DB-secret never cross-region synced"; #4159 fix MERGED but grafana SSO secret + guacamole DB not yet converged in region B).

## Verdicts
| row | verdict | reason |
|---|---|---|
| 59 | ❌ #3375 | singleton-Provision WRITE out of read-only scope (#4179/#4180); singleton RENDER proven live (shared-pg Topology = Pattern:singleton, single PRIMARY, no Switchover) |
| 60 | ❌ #3375 | active-hot-standby-Provision WRITE out of scope; 3 shared-pg CNPG trios Ready prove the mode provisions, but NO Continuum/CNPGPair object → the "2-region pair + armed Switchover" the mode should yield is NOT realized |
| 61 | ❌ #3375 | depends on the row-60 Provision write (out of scope) |
| 62 | ❌ #3375 | live Continuum DR status absent — Continuum CR count 0, API 404, Topology tab renders singleton/no DR section |
| 63 | ❌ #3375 | grafana Topology tab renders NO DR/Switchover section at all (not even an honest "Switchover unavailable" disabled state) |
| 64 | ❌ #3375 | no replication-lag field rendered (no DR section exists to host it) |
| 65 | ✅ | Cloud Region 2/2 (me-east-215-a/-b), no phantom region |
| 66 | ✅ | Cloud Cluster 2/2 HEALTHY, one per region |
| 67 | ❌ #4158 | grafana region-A 3/3 Running but region-B pod 2/3 CreateContainerConfigError (`grafana-sso-oidc-credentials` secret not delivered to region B); console rollup CONVERGING — NOT Healthy in both regions |
| 68 | ✅ | powerdns-admin 1/1 Running, HR Ready, dashboard serves (CNPG DB host resolved, no translate-host error) |
| 69 | ✅ | keycloak Running in BOTH regions (region-A 1/1 + region-B 1/1, both HR True) — JGroups DB-host resolves, no UnknownHostException; prior region-B blocker cleared |
| 70 | ❌ #4158 | guacamole region-A Running + HR Ready, but region-B HR False (jdbc-seed DeadlineExceeded, guacamole-pg-initdb Pending) — NOT Healthy in both regions |

Counts: **3 ✅ · 9 ❌ · 0 ⚠️ · 0 ⛔.**
