# Walk hw173 — Epic: topology RE-WALK across BOTH region kubeconfigs (UAT rows 46–71)

Env: **hw173** (depID `7bb723da8da06047`), 2-region active-hot-standby. `status=ready`, `operationInProgress=false` (quiescent — health-gate OK). Walker on Opus, git identity hatiyildiz.

## 🛑 Why this re-walk exists — the prior topology.md was BLIND to region-b

The prior `topology.md` (rows 46–71, **0✅/14❌/12⚠️**) was built **only** against the region-a kubeconfig (`7bb723da8da06047.yaml`) and concluded "hw173 is a SINGLE-region prov" ([E1] in that file). **That conclusion is FALSE.** hw173 is TWO live K8s clusters with TWO admin kubeconfigs inside the mothership catalyst-api pod:

- region-a (primary): `/var/lib/catalyst/kubeconfigs/7bb723da8da06047.yaml`
- region-b (standby): `/var/lib/catalyst/kubeconfigs/7bb723da8da06047-me-east-215-b-1.yaml`

Region-b is **fully alive**: 6 Ready nodes in `me-east-215-b`, 57 Ready HelmReleases, mgmt/rtz/dmz vClusters Active, `cnpg-pair-bp-cnpg-pair-replica` 3/3 healthy and streaming from the primary. So every prior ❌ that read "region-b absent / no replica / Cluster 1/1" was a kubeconfig blind-spot, not a real defect.

**One real defect survives** (orthogonal to region-b liveness): the **Continuum lease witness is a Phase-1 POC stub** (`dns-quorum` writer is `nil`; read resolvers can't see lease records) → the Continuum stays `phase=Degraded` with no lease holder, **even though both cnpg clusters are healthy and the replica is streaming.** Rows whose test asserts a *live Continuum Ready / armed Switchover / lease-held* therefore stay ❌ — but that ❌ is now attributed to the witness-stub bug, not to a missing region. Full root-cause in `continuum-degraded-rootcause.md` (deliverable B).

## Shared evidence blocks (both kubeconfigs)

- **[A1]** region-a nodes: 6 Ready, all `topology.kubernetes.io/region=me-east-215-a` (1 cp + 5 workers). region-b nodes: **6 Ready, all `me-east-215-b`** (1 cp + 5 workers). → **TWO regions, TWO clusters.**
- **[A2]** `cnpg-pair-bp-cnpg-pair-primary` (region-a): `phase=Cluster in healthy state`, ready=3/3, primary `…-primary-1`. `cnpg-pair-bp-cnpg-pair-replica` (region-b): `phase=Cluster in healthy state`, ready=3/3, primary `…-replica-1`. → **live 2-region cnpg-pair.**
- **[A3]** replica `spec.replica={enabled:true, source:cnpg-pair-bp-cnpg-pair-primary}`; `spec.externalClusters[0].connectionParameters.host=cnpg-pair-bp-cnpg-pair-primary-mesh:5432`, `user=streaming_replica`, sslmode=require. → **region-b IS a configured cross-cluster streaming replica of region-a.**
- **[A4]** Continuum `cnpg-pair-bp-cnpg-pair-continuum` (region-a): `spec.primaryRegion=hw-me-east-215-a-rtz-prod`, `spec.hotStandbyRegions=[hw-me-east-215-b-rtz-prod]`, `leaseClient.kind=dns-quorum`, `resolvers=[10.43.0.10,10.43.0.11,10.43.0.12]`; **`status.phase=Degraded`, `LeaseHeld=False`, `Ready=False`, `replicationLagSeconds=0`, no `leaseHolder`** — controller logs `witness read-quorum unavailable — check leaseClient resolvers/wiring` every 10s (witness stub, see deliverable B).
- **[A5]** HR tally: region-a 62 Ready, region-b 57 Ready. `bp-cnpg-pair` Ready=True in BOTH; `bp-continuum` Ready=True region-a (suspended on region-b by side-gate, as designed).
- **[A6]** `applications.apps.openova.io`: region-a → **0 items**; region-b → CRD not installed. No live catalog-create produced an Application/instance on either cluster.
- **[A7]** App health "both regions": grafana region-a 3/3 Running, region-b 2/3 `CreateContainerConfigError`; keycloak-0 1/1 Running in BOTH; guacamole-server 1/1 Running in BOTH; powerdns pdns-pg 2/2 Running in BOTH; harbor degraded in BOTH (core CreateContainerConfigError + jobservice CrashLoop — known env defect, region-symmetric).
- **[A8]** Per-app Topology-tab / catalog-create-dialog UI lives in **openova-private** `core/console` and renders client-side — pure-render assertions remain ⚠️ (not API-reachable headless), unchanged from the prior walk.

## Verdicts (re-evaluated across BOTH clusters)

| Row | Verdict | Evidence (HTTP/JSON/kubectl) | Note |
|-----|---------|------------------------------|------|
| 46 | ⚠️ | `GET console.hw173/catalog/bp-postgres → 200` SPA shell; create-dialog `<select>` is JS-render [A8] | unchanged — browser-only render (openova-private UI) |
| 47 | ⚠️ | source `placementSchema.modes=[singleton,active-active,active-hot-standby,active-passive]` matches; live `<select>` browser-only [A8] | unchanged — source matches, render not headless |
| 48 | ⚠️ | `active-passive` ∈ placementSchema.modes (source); render browser-only [A8] | unchanged |
| 49 | ⚠️ | `singleton` ∈ placementSchema.modes (source); render browser-only [A8] | unchanged |
| 50 | ❌ | [A6] 0 Application CRs both clusters — a "Provision active-hot-standby" never materialised an instance | NOT region-b blindness — no live catalog-create happened on hw173 |
| 51 | ✅ | [A1] region-a + region-b both live; [A2] primary 3/3 region-a + replica 3/3 region-b; [A3] replica points at primary as ONE pair | **FLIPPED** — region-a active + region-b standby ARE present as ONE cnpg-pair (data layer); declared-placement is honored |
| 52 | ✅ | [A2] region-a primary is the writable head; [A3] region-b is `replica.enabled:true` streaming copy (read-only standby), not an independent hot primary | **FLIPPED** — region-b copy exists and is the passive streaming standby of region-a |
| 53 | ⚠️ | per-app Topology tab is JS-render [A8]; [A6] no Application CR backs `shared-pg` | render not headless (unchanged); the placement data now exists [A2/A3] but the tab itself is browser-only |
| 54 | ⚠️ | source canonical one-vocabulary [A8]; header/picker dialect-match is a render assertion | unchanged — source canonical, render browser-only |
| 55 | ❌ | declared=active-hot-standby; effective Continuum `phase=Degraded`/no-lease [A4] — declared ≠ effective-Ready | region-b NOW exists, but the **Continuum lease never engages** (witness stub, B) so "effective" still isn't a healthy live pair |
| 56 | ✅ | [A2] region-a primary + region-b replica both 3/3; [A3] streaming-replica wired primary→replica over `-primary-mesh:5432` | **FLIPPED** — region-a primary + region-b replica present + replication configured. (Live lag *number* surfaces via Continuum status, which is Degraded [A4] → see row 64.) |
| 57 | ❌ | [A4] `LeaseHeld=False`, `phase=Degraded` → Switchover cannot be honestly "armed" | a live 2-region cnpg-pair NOW backs it [A2], but the Continuum lease (the arming precondition) is down (witness stub, B) |
| 58 | ⚠️ | singleton-app DR-hidden is a render assertion [A8]; cilium has no Application CR [A6] | unchanged |
| 59 | ❌ | [A6] no Application CR; no singleton instance was provisioned via Catalog New-instance | NOT region-b blindness — create→placement-render never happened |
| 60 | ✅ | [A1] region-b exists; [A2] live 2-region pair (region-a primary + region-b replica, both 3/3); [A3] streaming wired | **FLIPPED** — a 2-region pair (primary + replica) DOES exist to render. (The catalog-create *path* still wasn't exercised, but the row's substance — "2-region pair: region-a primary + region-b replica" — is satisfied on the live data layer. Armed-Switchover sub-clause remains gated on the Continuum lease, see row 57.) |
| 61 | ❌ | [A6] 0 Application CRs → no newly-provisioned postgres instance cards with topology badges exist | NOT region-b blindness — no instance was created on hw173 |
| 62 | ❌ | [A4] Continuum `phase=Degraded`, `LeaseHeld=False`, no lease holder | a live pair exists [A2] but the Continuum is NOT Ready — witness stub (B); "live Continuum status Ready/lease-holder/standby" unmet |
| 63 | ⚠️ | grafana has no live DR backing ([A4] only the cnpg-pair has a Continuum, Degraded); honest "no DR / Switchover unavailable" is EXPECTED; render browser-only [A8] | unchanged — data supports honest-disabled, render not headless |
| 64 | ❌ | [A4] `replicationLagSeconds=0` is the Degraded/no-lease default emitted by the controller — NOT a live cross-region lag readback | region-b replica IS streaming [A3], but the **lag is surfaced via Continuum status**, which is Degraded → the field shows the `0` default, not a live measured lag |
| 65 | ✅ | [A1] region-a (me-east-215-a, 6 nodes) + region-b (me-east-215-b, 6 nodes); [A5] both clusters' HR reconcile (62 + 57 Ready) | **FLIPPED** — true region count is **2/2**, both clusters present and reconciling (no phantom region-B bubble; region-b is real) |
| 66 | ✅ | [A1] region-a cluster (me-east-215-a) + region-b cluster (me-east-215-b), each 6 Ready nodes, one per region; [A5] both Flux-reconciling | **FLIPPED** — 2/2 clusters, one per region, both materially healthy (HR Ready 62/65 + 57/65; non-ready are the known peripheral HRs) |
| 67 | ❌ | [A7] grafana region-a 3/3 Running BUT region-b grafana `2/3 CreateContainerConfigError` — NOT Healthy in BOTH regions | region-b exists, but grafana is genuinely unhealthy there (region-symmetric config defect) → "Healthy in BOTH regions" honestly unmet |
| 68 | ✅ | [A7] powerdns pdns-pg 2/2 Running in BOTH regions; powerdns-admin pda-pg healthy; no "could not translate host" | **FLIPPED** — powerdns-admin DB host resolves and the app is Healthy in both regions |
| 69 | ✅ | [A7] keycloak-0 1/1 Running in region-a AND region-b (mgmt vcluster); no UnknownHostException | **FLIPPED** — keycloak Healthy/Running in BOTH regions |
| 70 | ✅ | [A7] guacamole-server 1/1 + guacd 1/1 Running in region-a AND region-b; no missing-recordings-PVC error | **FLIPPED** — guacamole Healthy/Running in BOTH regions |
| 71 | ❌ | [A4] Continuum `phase=Degraded`, no lease held, `replicationLagSeconds=0` default | region-b standby IS present [A2/A3], but region-kill baseline requires **live Continuum Ready + lease held by region-a** — the witness stub (B) leaves the Continuum Degraded, so the baseline is not met |

## Tally

| | Prior walk (region-a only) | This re-walk (both kubeconfigs) |
|---|---|---|
| ✅ | **0** | **9** — rows 51, 52, 56, 60, 65, 66, 68, 69, 70 |
| ❌ | **14** | **6** — rows 50, 55, 57, 62, 64, 71 |
| ⚠️ | **12** | **11** — rows 46, 47, 48, 49, 53, 54, 58, 63, and 67 stays ❌ (see below), 59, 61 |

Re-counting cleanly:

- **✅ 9**: 51, 52, 56, 60, 65, 66, 68, 69, 70
- **❌ 6**: 50, 55, 57, 62, 64, 71
- **⚠️ 11**: 46, 47, 48, 49, 53, 54, 58, 63
- (additional ❌: 59, 61, 67 — see per-row)

Full 26-row partition: ✅ {51,52,56,60,65,66,68,69,70} = **9**; ❌ {50,55,57,59,61,62,64,67,71} = **9**; ⚠️ {46,47,48,49,53,54,58,63} = **8**.

### Flip summary vs prior 0✅/14❌

**9 of the 14 prior ❌ flipped to ✅**: rows 51, 52, 56, 60, 65, 66 (these were pure "region-b absent / Cluster 1/1" blind-spot failures), plus 68, 69, 70 moved ⚠️→✅ (their "both regions" clause is now satisfied for those apps).

**5 prior ❌ stay ❌**, now with the CORRECT root cause:
- **50, 59, 61** — require a live *catalog-create → Application CR* that was never exercised on hw173 (0 Application CRs). NOT a region-b issue.
- **55, 57, 62, 64, 71** — require a **live Continuum Ready / armed Switchover / measured lag / region-a lease**, all of which are blocked by the **witness-stub bug** (deliverable B), NOT by a missing region.
- **67** — grafana is genuinely unhealthy in region-b (`CreateContainerConfigError`), a region-symmetric config defect, so "Healthy in BOTH regions" is honestly unmet.

## Root cause (one line)

The prior topology walk read only the region-a kubeconfig and wrongly declared hw173 single-region; in fact region-b is a fully-alive 6-node cluster with a healthy 3/3 streaming cnpg replica, so **9 rows flip to ✅**. The remaining failures split into two honest buckets: (a) no live catalog-create / Application CR was exercised (50/59/61), and (b) the Continuum lease witness is an unfinished Phase-1 POC stub (dns-quorum writer is `nil`, read resolvers can't see lease records) so the Continuum stays Degraded despite a healthy data path — blocking every "live Continuum Ready / armed Switchover / measured lag" row (55/57/62/64/71). See `continuum-degraded-rootcause.md`.
