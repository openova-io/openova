# #3373 vCluster Placement Migration Plan

**Status:** PLAN (read-only). The build executes this AFTER #3374 SSO goes green.
**Author:** placement planner (read-only), 2026-06-14.
**Source of truth read:** `clusters/_template/bootstrap-kit/placement.yaml`, issue #3373 DoD,
the three vCluster isolation netpols + values (`platform/bp-{mgmt,dmz,rtz}-vcluster/chart`),
the #3423 sme→NATS fix (commit `cc3a7abcf`, bp-mgmt-vcluster 0.2.9), and **live hw133**
(`kubectl --kubeconfig /tmp/hw133-cond.yaml`, env `catalyst-hw133-omani-works`, ~7h uptime).

This plan does NOT edit any chart, slot, placement.yaml, or the live env. The single write is this doc.

---

## 0. The governing constraint — CRD-sync is a vCluster Free-tier feature, NOT pure-OSS

This is the load-bearing fact that decides the whole route-bearing batch.

- placement.yaml lines 161-165 record that the #3373 PROMOTE-NOW conversion of keycloak was
  **reverted on the 2026-06-13 hw130 outage**: the syncer copies **0 sync-set CRDs** on live
  `vc-mgmt`; keycloak install failed with **`no matches for kind HTTPRoute`**.
- The bp-mgmt-vcluster chart **already declares** coupling-fix (b) — the `customResources`
  toHost sync set for `httproutes.gateway.networking.k8s.io` + `externalsecrets.external-secrets.io`
  (`platform/bp-mgmt-vcluster/chart/values.yaml:309-322`). It is present in the values but
  **silently no-ops on the live build**.
- Root cause (confirmed against loft.sh docs): **CRD Sync / `customResources` toHost is a
  vCluster _Free-tier_ feature that requires the vCluster Platform connection for license
  validation — it is NOT part of the pure-OSS build.** Pages without a tier badge are OSS;
  CRD Sync carries a FREE badge (Platform activation required). hw133's vClusters run the
  pure loft-sh/vcluster subchart (Apache-2.0 OSS, no platform account, no license key), so
  the `customResources` block is accepted by Helm but the OSS syncer never registers the CRDs
  inside the vCluster apiserver → 0 sync-set CRDs → any chart shipping an HTTPRoute or
  ExternalSecret CR fails to install inside the vCluster.

  Sources:
  [Open Source vs Free tier](https://www.vcluster.com/docs/vcluster/0.32.0/introduction/oss-vs-free) ·
  [toHost custom resources](https://www.vcluster.com/docs/vcluster/configure/vcluster-yaml/sync/to-host/advanced/custom-resources) ·
  [vCluster Free launch (CRD Sync listed as Free-tier)](https://www.vcluster.com/blog/launching-vcluster-free-get-enterprise-features-at-no-cost)

**Decision pending (founder):** which of three handlings for **route-bearing + ExternalSecret-bearing**
apps —
- **(L) License path** — activate the vCluster Free-tier platform connection on the three
  runtime vClusters so `customResources` toHost sync actually engages (unblocks coupling-fix (b)
  + (c) as already coded; then route-bearing apps re-home cleanly).
- **(B) Host-bridge path** — keep the app's HTTPRoute + ExternalSecret as **host-side**
  resources (rendered into the app's host namespace by the slot, backendRef pre-aliased to the
  syncer-mangled Service `<svc>-x-<innerNs>-x-<tier>-vcluster`), while the Deployment/StatefulSet
  re-homes into the vCluster. The pod moves; its route + secret stay host-rendered. No license.
- **(H) Keep host-side** — document a §4 exception and leave the app on host until (L) or (B) lands.

Until the founder picks, **this plan defers every route-bearing / ExternalSecret-bearing app**
(it does NOT migrate them) and migrates only the non-route, non-ExternalSecret stateless/stateful
set whose wire is plain Service DNS (which the OSS `services` sync already mirrors — proven live
by nats-jetstream/loki/mimir/tempo). The host-minimum guard never wedges because nothing
route-bearing flips without the founder's call.

---

## 1. Live ground-truth on hw133 (read-only, 2026-06-14)

Already inside their vCluster (6 — matches placement.yaml exactly):

| App | Live placement (host ns → vCluster) | Evidence (pod) |
|---|---|---|
| nats-jetstream | mgmt | `nats-jetstream-0-x-nats-system-x-mgmt-vcluster` |
| loki | mgmt | `loki-0-x-loki-x-mgmt-vcluster` |
| mimir | mgmt | `mimir-*-x-mimir-x-mgmt-vcluster` |
| tempo | mgmt | `tempo-0-x-tempo-x-mgmt-vcluster` |
| coraza | dmz | `coraza-bp-coraza-*-x-coraza-x-dmz-vcluster` |
| sandbox | rtz | `sandbox-controller-*-x-catalyst-syste-…` (ImagePullBackOff, separate concern) |

Still on host (20 — the migration backlog this plan covers):
keycloak (`keycloak` ns), gitea (`gitea`), harbor (`harbor`), grafana (`grafana`),
openbao (`openbao`), shared-pg / shared-pg-b / shared-pg-c (`shared-data`),
cnpg-pair (`cnpg`), valkey (`valkey`), seaweedfs (`seaweedfs`),
catalyst-platform + guacamole + k8s-ws-proxy + openova-flow server/emitter (`catalyst-system`),
powerdns-admin (`powerdns-admin`), sso-bridge (`sso-bridge`), newapi (`newapi`),
vllm (target rtz; not currently scheduled on hw133).

The `-x-<innerNs>-x-<tier>-vcluster` Service name-mangling is the **fix-(a) aliasing target**:
any host consumer or HTTPRoute backendRef of a re-homed app must point at the mangled Service
name, not the bare name.

---

## 2. The mechanism — a promote is ONE field, then `render-slot-placement.py fix`

`scripts/render-slot-placement.py` is the generator. Flipping a slot's `vcluster:` value in
placement.yaml + running `fix` stamps, per slot (from the script's own field list, lines 22-28):

- `spec.targetNamespace` → the inner vCluster namespace,
- `spec.storageNamespace` → same (vCluster slots only; the hw92 unpinned-PVC lesson),
- `spec.kubeConfig.secretRef {name: vc-<tier>, key: config}` → **the one pivot** that lands the HR in the vCluster,
- `metadata.labels."catalyst.openova.io/vcluster"` → the tier,
- the `bp-<tier>-vcluster` dependsOn edge,
- and (fix-a) the `gateway.backendService` alias value for vCluster-synced Services.

`scripts/audit-placement-conformance.py {rendered|live}` is the DoD-4 gate (declared == actual,
zero undeclared host workloads). The build runs `check` (drift) + `live` (conformance) after each batch.

**No hand-edited slot YAML.** Every migration row below = flip `vcluster:` in placement.yaml +
`fix` + (where listed) one `allowedIngressNamespaces` addition in the target vCluster's values.yaml.

---

## 3. Per-app migration table (the 20 host-resident apps)

Legend — **Route?** = ships an HTTPRoute (public FQDN). **Sec?** = ships an ExternalSecret CR.
Either ⇒ blocked by the §0 OSS CRD-sync wall ⇒ **DEFER pending founder (L/B/H)**.
**Carve-out** = the `allowedIngressNamespaces` entry the target vCluster needs so host consumers
keep reaching the app's syncer-mangled Service after it moves (mirrors the #3423 sme→NATS fix).

| # | App (slot) | Cur ns → target | Route? | Sec? | Host consumers needing carve-out | CNPG / stateful | Batch |
|---|---|---|---|---|---|---|---|
| 1 | valkey (17) | valkey → **rtz** | no | no | `sme` (catalyst-platform SME services: auth/gateway via `VALKEY_ADDR`) — already has the `valkey-cross-ns-policy` CNP idiom (#861) to re-point | stateless (bitnami replication) | **A** |
| 2 | seaweedfs (18) | seaweedfs → **rtz** | no | no | `gitea`, `harbor`, `guacamole` (catalyst-system), `mgmt` (loki/mimir/tempo S3), `velero` — S3 `:8333` | stateful PVCs (master/volume/filer); `seaweedfs-s3-secret` reflected by `bp-reflector` (cross-ns mirror survives the move) | **A** |
| 3 | vllm (39) | vllm → **rtz** | no | no | none found (no host consumer wires `vllm.vllm.svc`) | stateless (GPU inference) | **A** |
| 4 | shared-pg (16a) | shared-data → **rtz** | no | no | `keycloak` (`shared-pg-rw.shared-data:5432`), gitea, harbor (ADR-0010 reuse) | **CNPG Cluster CR** — toHost reflection REQUIRED (see §4) | **C** |
| 5 | shared-pg-b (16c) | shared-data → **rtz** | no | no | `grafana` (`shared-pg-b-rw.shared-data:5432`) | **CNPG Cluster CR** — toHost reflection | **C** |
| 6 | shared-pg-c (16d) | shared-data → **rtz** | no | no | catalyst-platform mesh (post-adoption, slot 16d header) | **CNPG Cluster CR** — toHost reflection | **C** |
| 7 | cnpg-pair (16b) | cnpg → **rtz** | no | no | bp-continuum (region-kill switchover); catalyst-api region-graph | **CNPG Cluster CR** (the region-kill pair) — toHost reflection | **C** |
| 8 | openbao (08) | openbao → **mgmt** | **yes** (`bao.<fqdn>`) | likely | `catalyst-system` (catalyst-api `CATALYST_OPENBAO_ADDR=openbao.openbao:8200`), `newapi`, `sso-bridge` | stateful (raft); CNPG audit-pg | **D (deferred)** |
| 9 | keycloak (09) | keycloak → **mgmt** | **yes** (`auth.<fqdn>`) | yes | gitea, harbor, grafana, guacamole, newapi, catalyst-platform, sso-bridge (every SSO client) | uses shared-pg (consumer, no own CR) | **D (deferred)** |
| 10 | gitea (10) | gitea → **mgmt** | **yes** (`gitea.<fqdn>`) | yes | catalyst-platform (Flux source post-cutover), sso-bridge | **CNPG Cluster CR** (gitea-pg) + shared-pg consumer | **D (deferred)** |
| 11 | harbor (19) | harbor → **mgmt** | **yes** (`harbor.<fqdn>`/`registry.`) | yes | every image pull (cluster-wide), catalyst-platform | **CNPG Cluster CR** (harbor-pg) + shared-pg consumer | **D (deferred)** |
| 12 | grafana (25) | grafana → **mgmt** | **yes** (`grafana.<fqdn>`) | yes | operator dashboards; reads loki/mimir/tempo (already mgmt) | shared-pg-b consumer | **D (deferred)** |
| 13 | powerdns-admin (11a) | powerdns-admin → **mgmt** | **yes** | maybe | UI only; talks to host powerdns (stays host-minimum) | none | **D (deferred)** |
| 14 | newapi (80) | newapi → **mgmt** | **yes** | yes | gateway clients | **CNPG Cluster CR** (newapi-pg) | **D (deferred)** |
| 15 | openova-flow-server (56) | catalyst-system → **mgmt** | **yes** (via oidc-gate `openova-flow`) | maybe | openova-flow-emitter, oidc-gate (host slot 13c, on the gateway datapath) | **CNPG Cluster CR** (openova-flow-pg) | **D (deferred)** |
| 16 | guacamole (52) | catalyst-system → **mgmt** | **yes** (`guacamole.<fqdn>`) | maybe | k8s-ws-proxy (host DaemonSet), seaweedfs (recording PUT), keycloak, NATS | none (guacd local) | **D (deferred)** |
| 17 | catalyst-platform (13) | catalyst-system → **mgmt** | **yes** (console + marketplace + tenant-public routes) | yes | the control plane itself; `sme` + `catalyst` extraNamespaces; consumes NATS/valkey/openbao/seaweedfs/shared-pg-c | **CNPG Cluster CR** (catalyst-pg) | **D (deferred, last)** |
| 18 | sso-bridge (13b) | sso-bridge → **mgmt** | no | no (reads via reconciler) | catalyst-platform (gitea SSO bootstrap), keycloak, openbao | stateless reconciler | **B** (see note) |
| 19 | k8s-ws-proxy (51) | catalyst-system → **mgmt** | no | no | guacamole (tunnels exec via the per-node DaemonSet) | **DaemonSet** (per-node host access) — see §6 | **B / keep-host** |
| 20 | openova-flow-emitter (57) | catalyst-system → **mgmt** | no | no | openova-flow-server | stateless (DaemonSet-style emitter) | **B** (pairs with server, batch D) |

---

## 4. CNPG / stateful concern (rows 4-7, 10, 11, 14, 15, 17)

The MGMT vCluster values already ship the CNPG toHost reflection set
(`platform/bp-mgmt-vcluster/chart/values.yaml:302-308`): `clusters` / `poolers` /
`scheduledbackups.postgresql.cnpg.io` `customResources` toHost. **rtz already carries the
identical block** — VERIFIED: `platform/bp-rtz-vcluster/chart/values.yaml:169-189` ships all
three CNPG kinds PLUS the #3373 coupling-fix (b) `httproutes` + `externalsecrets` entries. So no
parity addition is needed for rtz; the same OSS-vs-Free-tier gating caveat below applies to it
verbatim (the block is declared but no-ops on the pure-OSS syncer). dmz needs none (no CNPG/route
app re-homes into dmz in this backlog).

**Critical caveat:** the CNPG toHost reflection is the SAME `customResources` mechanism as the
HTTPRoute/ExternalSecret sync in §0 — so it is equally a **Free-tier feature, not pure-OSS**.
The CNPG-backed shared-pg / cnpg-pair re-home into rtz therefore **also depends on the founder's
§0 decision (L)**. If the platform stays pure-OSS, the bundled-CNPG apps cannot reflect their
Cluster CR out of the vCluster to the host cnpg-operator (slot 16, host-minimum) → **batch C is
gated behind §0 (L) just like the route-bearing batch D.** The cnpg-operator deliberately stays
on host substrate (G92.6 #2665); without the toHost reflection the Cluster CR lands inside the
vCluster apiserver where the operator can't see it and no PostgreSQL provisions. This was the
#3373 §3 landmine for gitea/harbor and it is identical for shared-pg.

This caveat upgrades the recommendation in §0: the **(L) license path is the single unblock for
BOTH route-bearing AND CNPG-stateful** re-homes. (B) host-bridge only helps the route + secret;
it does not help a bundled-CNPG Cluster CR (the operator is host-side, so the CR must reflect out
— which is the very Free-tier feature). For CNPG apps the realistic choices are **(L) license**
or **(H) keep host-side**.

---

## 5. Recommended migration batches (ordered)

**Batch A — non-route, non-ExternalSecret, non-CNPG stateless/stateful (SAFE on pure-OSS now).**
`services` sync (OSS) carries the Service back into the vCluster view; PVCs sync is OSS. No CRD-sync dependency.
1. valkey (17) → rtz
2. vllm (39) → rtz
3. seaweedfs (18) → rtz

Each = flip `vcluster: host → rtz` in placement.yaml + `render-slot-placement.py fix` +
the rtz `allowedIngressNamespaces` additions in §7. After: `audit-placement-conformance.py live`
+ verify host consumers (sme→valkey, gitea/harbor/guacamole/mgmt→seaweedfs S3) still wire.

**Batch B — stateless host-side reconcilers / DaemonSet-adjacent (case-by-case).**
4. openova-flow-emitter (57) → mgmt — pairs with server; migrate WITH batch D (server is route-bearing).
5. sso-bridge (13b) → mgmt — stateless; but it drives keycloak/openbao/gitea SSO bootstrap, so
   migrate only AFTER those are settled (it reaches them cross-vCluster). Defer to after batch D.
6. k8s-ws-proxy (51) → mgmt — **DaemonSet with per-node host access** (tunnels kubectl-exec). A
   DaemonSet's value is host-node reach; re-homing into a vCluster breaks the per-node model
   unless the syncer mirrors the DaemonSet pods to host. **Recommend keep-host with a §4 exception**
   (it is closer to host-minimum infra than an Application). See §6.

**Batch C — CNPG-bundled stateful (GATED behind §0 (L) license OR keep host).**
7-10. shared-pg (16a), shared-pg-b (16c), shared-pg-c (16d), cnpg-pair (16b) → rtz.
Do NOT migrate on pure-OSS — the Cluster CR cannot reflect to the host cnpg-operator (§4). If
founder picks (L), migrate all four together (they share `shared-data`/`cnpg` ns + the region-kill
pair), then re-point every consumer (keycloak/gitea/harbor/grafana/catalyst-platform) at the
syncer-mangled `*-rw.shared-data-x-…` Service via fix-(a) and add the rtz carve-outs in §7.

**Batch D — route-bearing + ExternalSecret-bearing (GATED behind §0 (L) license OR host-bridge (B)).**
Order within D (least-blast-radius first, control-plane root last):
11. powerdns-admin (11a) → mgmt (UI only; smallest blast radius)
12. grafana (25) → mgmt (operator dashboard; depends only on already-mgmt loki/mimir/tempo + shared-pg-b)
13. openova-flow server+emitter (56/57) → mgmt (oidc-gate fronted, isolated app)
14. guacamole (52) → mgmt (depends on keycloak + seaweedfs + k8s-ws-proxy)
15. newapi (80) → mgmt
16. openbao (08) → mgmt (secret store; many consumers — settle before keycloak/gitea/harbor)
17. keycloak (09) → mgmt (every SSO client depends on it — the #2697 revert app; migrate only with CRD-sync PROVEN on a non-critical app first, e.g. grafana)
18. gitea (10) → mgmt (Flux source post-cutover; CNPG + route + secret)
19. harbor (19) → mgmt (cluster-wide image pulls; CNPG + route + secret — highest blast radius among components)
20. catalyst-platform (13) → mgmt (**the control-plane root — migrate LAST**; its console/marketplace/tenant routes + the `sme`/`catalyst` extraNamespaces + every event-bus/secret wire)

---

## 6. DO-NOT-MIGRATE on pure-OSS / keep host-side (with reasons)

| App | Reason | Recommended call |
|---|---|---|
| **k8s-ws-proxy (51)** | DaemonSet whose entire purpose is per-node host reach (tunnels kubectl-exec sessions to each node). vCluster re-home breaks the per-node model on the OSS build (no DaemonSet host-mirror). It is infra-adjacent, not an Application. | **Keep host (§4 exception)**, or revisit under (L) if the syncer can mirror DaemonSet pods to host. Draft PR #3350 explored the recipe; treat as keep-host until proven. |
| **All Batch C CNPG apps** (shared-pg×3, cnpg-pair) | The host cnpg-operator (slot 16, host-minimum) reconciles the Cluster CR; reflecting it out of the vCluster is the Free-tier `customResources` toHost feature (NOT pure-OSS). | **Gate behind (L) license**, else **keep host** with §4 exception. Pure-OSS migration would wedge ("Cluster CR not seen by operator → no PostgreSQL"). |
| **All Batch D route/secret apps** (10 apps) | HTTPRoute + ExternalSecret CRs need the Free-tier CRD-sync to install inside the vCluster (the keycloak `no matches for kind HTTPRoute` failure, placement.yaml:161-165). | **Gate behind (L) license OR host-bridge (B)** (render HTTPRoute + ExternalSecret host-side, re-home only the pod). Do NOT flip on pure-OSS — wedges the env. |
| **powerdns-admin (11a)** | UI talks to host powerdns (slot 11, host-minimum authoritative DNS). Listed `mgmt` default "for founder veto" in placement.yaml:185-186. | Migrate in batch D **only if** founder confirms the mgmt default (else keep host). |

**Net:** on the **current pure-OSS build**, only **Batch A (3 apps: valkey, vllm, seaweedfs)**
migrates cleanly with zero new platform spend. The other **17** need the founder's §0 decision —
**(L)** unblocks the most (all route + all CNPG, the cleanest single lever), **(B)** unblocks
route-bearing-but-not-CNPG apps without a license, **(H)** keeps them host-side under documented §4 exceptions.

---

## 7. Exact `allowedIngressNamespaces` additions required (per vCluster)

Mirrors the #3423 sme→NATS carve-out (commit `cc3a7abcf`): when an app moves INTO a vCluster, its
host-side consumers sit OUTSIDE the vCluster's isolation netpol, so each consumer's host namespace
must be added to that tier's `allowedIngressNamespaces` or the consumer's wire is denied at the netpol.

### bp-rtz-vcluster (`platform/bp-rtz-vcluster/chart/values.yaml:66`)
Current: `dmz`, `flux-system`. Add (for Batch A + Batch C consumers):

| Add entry | Unblocks (consumer → moved app) | Batch |
|---|---|---|
| `sme` | catalyst-platform SME services → valkey (`VALKEY_ADDR`) | A |
| `catalyst-system` | catalyst-api / guacamole → seaweedfs S3 (`:8333`); catalyst-api → shared-pg/cnpg-pair | A + C |
| `gitea` | gitea → seaweedfs S3 (object storage) **and** gitea → shared-pg (if gitea stays host while shared-pg moves) | A + C |
| `harbor` | harbor → seaweedfs S3 **and** harbor → shared-pg | A + C |
| `keycloak` | keycloak → shared-pg (`shared-pg-rw`) — if keycloak stays host while shared-pg moves to rtz | C |
| `grafana` | grafana → shared-pg-b (`shared-pg-b-rw`) — if grafana stays host while shared-pg-b moves | C |
| `mgmt` | loki/mimir/tempo (already mgmt) → seaweedfs S3 backups | A |
| `velero` | velero (host slot 34) → seaweedfs S3 (CNPG WAL / cluster backup) | A |

(For Batch A alone, the minimum additions are: `sme`, `catalyst-system`, `gitea`, `harbor`,
`mgmt`, `velero`. The keycloak/grafana entries are only needed once Batch C moves shared-pg and
those consumers are still host-side; if they move to mgmt in batch D the wire becomes mgmt→rtz and
the entries are still required — add them when batch C lands.)

### bp-mgmt-vcluster (`platform/bp-mgmt-vcluster/chart/values.yaml:134`)
Current: `dmz`, `flux-system`, `sme` (the #3423 NATS carve-out). Add (for Batch D consumers,
**only when §0 (L)/(B) is chosen and batch D executes**):

| Add entry | Unblocks (consumer → moved app) | Batch |
|---|---|---|
| `catalyst-system` | catalyst-api → openbao (`CATALYST_OPENBAO_ADDR`); catalyst-api → keycloak/harbor; k8s-ws-proxy (host) → guacamole | D |
| `newapi` | newapi → openbao + keycloak | D |
| `sso-bridge` | sso-bridge → keycloak + openbao (if sso-bridge stays host while they move) | D |
| `oidc-gate` | oidc-gate (host slot 13c, on gateway datapath) → openova-flow-server | D |
| `gateway-system` + `kube-system` | host Cilium Gateway → keycloak/gitea/harbor/grafana routes (the syncer-mangled Service backendRef) | D |
| `velero` | velero → harbor (registry backup) if applicable | D |
| `rtz` | catalyst-platform / keycloak / harbor / grafana (now in mgmt) → shared-pg / cnpg-pair (in rtz) — the cross-vCluster DB wire | C+D |

### bp-dmz-vcluster (`platform/bp-dmz-vcluster/chart/values.yaml:73`)
Current: `mgmt`, `rtz`, `kube-system`, `gateway-system`, `flux-system`, `allowWorldIngress:true`.
**No additions required** — only coraza targets dmz today and no §4 app re-homes INTO dmz in this
backlog (coraza is already there). Leave as-is.

---

## 8. Summary for the build (post-#3374)

- **Migrate cleanly NOW on pure-OSS (Batch A, 3 apps):** valkey, vllm, seaweedfs → rtz.
  Mechanism: flip `vcluster:` in placement.yaml + `render-slot-placement.py fix` + the 6 minimum
  rtz `allowedIngressNamespaces` additions (§7). Verify via `audit-placement-conformance.py live`.
- **Need OSS-gating handling — founder §0 decision required (17 apps):**
  - **Batch C (4 CNPG apps):** gated behind **(L) license** (CNPG toHost reflection is Free-tier) or **keep host**.
  - **Batch D (10 route/secret apps):** gated behind **(L) license** or **(B) host-bridge** (render HTTPRoute + ExternalSecret host-side, re-home only the pod).
  - **Batch B (3 reconcilers):** openova-flow-emitter + sso-bridge migrate with/after batch D; **k8s-ws-proxy → keep host (DaemonSet, §4 exception).**
- **Single highest-leverage unblock:** the **(L) vCluster Free-tier platform connection** — it
  simultaneously engages the already-coded coupling-fix (b)+(c) `customResources` toHost sync,
  unblocking BOTH the route-bearing batch D AND the CNPG batch C. Without it, only Batch A ships
  and the rest stays host-side under §4 exceptions.
- **Migration order:** A (now) → [founder picks L/B/H] → C (CNPG, all four together) →
  D least-blast-radius-first (powerdns-admin → grafana → openova-flow → guacamole → newapi →
  openbao → keycloak → gitea → harbor → **catalyst-platform LAST**) → B reconcilers fold in.
- **Never hand-patch live (zero-touch);** every flip is a placement.yaml data change + generator
  run + a values-file `allowedIngressNamespaces` addition, then a fresh/rolled-env conformance walk.
