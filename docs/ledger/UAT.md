# UAT — OpenOva Catalyst — fresh acceptance page (hw100)

> **Tests-first.** Fresh **2-region** prov **hw100** replaces wiped hw99. Every row is `☐` **pending** until walked LIVE on the **production React tree** (`products/catalyst/bootstrap/ui/`) on the real Sovereign — never a mock, never the dead Svelte console (`products/catalyst/console/`), never a CLOSED/merged status. A row earns its evidence link ONLY when it is actually walked. Authored 2026-06-08 after hw99 (1 functional cluster + falsified 2-region evidence) was wiped on founder order. **Sandbox is OUT of scope.**
>
> **This page is PER-APPLICATION.** Generic "works for grafana/gitea/harbor/openbao" rows are banned — every `ssoEnabled` app gets its **own** explicit single-click-SSO row (§2), and every app carrying a non-trivial topology gets its **own** explicit topology row (§3). The per-app data below is **extracted from `platform/*/blueprint.yaml`** (`spec.sso`, `spec.endpoints[].ssoEnabled`, `spec.topology.defaults.multi-region`, `spec.multiInstance.enabled`) and cross-checked against the topology audit + the #2744 SSO-fan-out tiers.
>
> Sources: 5-pillar DoD (`../DOD.md`); G117 EPIC #2737 (`#2740/#2741/#2742/#2743/#2744/#2745/#2674`); SSO fan-out tiers #2744; spire gap #3084; topology matrix `../sessions/2026-06-02-per-blueprint-topology-audit.md` (10 active-hot-standby / 14 active-active / 20 active-passive / 44 singleton).
>
> Legend: ✅ pass · ◑ partial · ❌ fail · ⛔ blocked · ☐ pending (default).

## 1. Pillars (marketplace → Org → multi-region → CNPG failover → cutover)

| # | Test group | Tested page | Test case (what you do) | What you must see | Result | Evidence |
|---|---|---|---|---|---|---|
| **Pillar 1 — Marketplace + voucher onboarding → Organization** |||||||
| TC-01 | Marketplace storefront | `marketplace.hw100.<dom>/` | Open the storefront | "Build your tenant" renders, non-empty, branded | ☐ | — |
| TC-02 | Operator issues voucher | `console.hw100.<dom>/bss/vouchers` | Operator: +Issue voucher → code+credit → submit | Voucher appears in the table, active | ☐ | — |
| TC-03 | Voucher email | recipient inbox | Open the voucher email | Delivered via the **Sovereign's own SMTP** with the redeem link | ☐ | — |
| TC-04 | Redeem voucher | `marketplace.hw100.<dom>/redeem?code=…` | Open the redeem link | "Voucher valid" + OMR credit; a garbage code → "not valid" | ☐ | — |
| TC-05 | Pick plan | `…/plans` | Pick a plan card | Advances to app picker | ☐ | — |
| TC-06 | Pick apps | `…/apps` | Select a Postgres-backed app | Advances to setup/extras | ☐ | — |
| TC-07 | Choose subdomain | `…/addons` | Type a valid subdomain | Pool picker offers a free domain; subdomain accepted | ☐ | — |
| TC-08 | Checkout (credit-only) | `…/checkout` | Sign in (email→PIN), confirm | Voucher **credit applied**, no card required; "Setting up your tenant" | ☐ | — |
| TC-09 | Organization created | "Your tenant is ready" | Follow the tenant link | Lands on `console.<orgslug>.<pool>` — real dashboard, not an error | ☐ | — |
| TC-10 | Tenant first login | tenant console | Customer PIN-login | Dashboard renders (Phase 2a) | ☐ | — |
| **Pillar 2 — Multi-region BCP topology chosen at signup** |||||||
| TC-11 | BCP at signup | `marketplace.hw100.<dom>/bcp` | Choose **active-hot-standby**, pick **two different** regions | Same-region rejected; two distinct regions accepted; provisions BOTH in one pass | ☐ | — |
| TC-12 | Cloud view = 2 REAL regions | `console.hw100.<dom>/cloud?view=graph` | Open the Cloud view | **2 regions, 2 clusters with REAL nodes in each** (not an empty 2nd-region VPC shell — the hw99 failure) | ☐ | — |
| **Pillar 3 — Two independent CNPG clusters + region-kill failover** |||||||
| TC-13 | CNPG pair across regions | `/app/$id` → Topology | Install a CNPG-backed app; read placement | One CNPG cluster **per region**, synchronous `ReplicaCluster` over ClusterMesh; both regions shown | ☐ | — |
| TC-14 | Region-kill failover | the app's FQDN | Dev kills the primary region; keep refreshing | Service resumes **≤30 s**, same FQDN; surviving region healthy; **0 transactions lost** | ☐ | — |
| **Pillar 5 — Sovereign independence (`bp-self-sovereign-cutover`)** |||||||
| TC-15 | Trigger cutover | `console.hw100.<dom>/settings` → Sovereignty | "Soft-tethered" → tap "Achieve True Sovereignty" → confirm | Progress card; 8 tether-pivot steps advance | ☐ | — |
| TC-16 | Egress-block proof | progress card | Wait through the final step | **10-min deny-egress** hold vs github.com/ghcr.io/harbor.openova.io; stays green → badge **"Independent"**, `cutoverComplete=true` | ☐ | — |
| TC-17 | Post-cutover resilience | tenant console + an app | PIN-login + tap **Open** | Both still work, now pulling exclusively from local Gitea/Harbor | ☐ | — |
| **G117 — Application lifecycle (EPIC #2737) — class ≠ instance** |||||||
| TC-G1a | Catalog class page | `console.hw100.<dom>/apps` → Catalog tab → click a class card | Click a class card | **CLASS page** `/catalog/$bp` — instances-list + New-instance only, no single-instance tabs | ☐ | — |
| TC-G1b | Instance page ≠ class | `/apps` → Deployments tab → click an instance | Click an instance | **INSTANCE page** `/app/$id` — that one instance only; **NO "New instance"**; the two clicks NEVER open the same page | ☐ | — |
| TC-G2 | Multi-instance children | `/catalog/$bp` | "+ New instance" ×3, distinct names | All accepted, no collision; class page lists all N, each → its own `/app/$id` | ☐ | — |
| TC-G4 | Endpoints tab editable | `/app/$id` → Endpoints | Add alias / edit / delete | EDITABLE; each mutation → Git-IaC PR (3 checks) → auto-merge → new FQDN serves TLS+SSO ≤2 min | ☐ | — |

## 2. PER-APP SSO single-click login — ONE ROW PER `ssoEnabled` app

> **The founder's #1 demand: an explicit single-click-login row for gitea, openbao, grafana, and every other ssoEnabled app — never a generic "works for grafana/gitea/harbor/openbao" row.**
>
> Extracted from `spec.endpoints[].ssoEnabled: true` + `spec.sso` (`realm`, `silentLogin: true`) in each `platform/<app>/blueprint.yaml`. **24 ssoEnabled blueprints, 26 ssoEnabled endpoints** (opensearch + catalyst-platform each expose two). The "Open" button replaces the raw endpoint URL on every `ssoEnabled` endpoint with `launchDefault: true`; the URL carries `prompt=none&kc_idp_hint=catalyst-pin` so the new tab lands **already signed in** — no login form, no second click. Realm is `sovereign` for Tier-1/Tier-2 control-plane apps and per-Org `{{.OrgSlug}}` for Tier-3 tenant apps (#2744). Tenant-app rows are walked from the **tenant** console `console.<orgslug>.<pool>`.

### 2.1 Tier-1 — sovereign-realm control-plane (4) — #2744 already-wired

| # | Tested page | Test case | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| SSO-grafana | `/app/bp-grafana` (instance) | Click **Open** | New tab lands in **Grafana** ALREADY signed in — silent OIDC (`prompt=none&kc_idp_hint=catalyst-pin`), **NO** login form, **NO** second click. Realm=`sovereign`. Roles mapped from KC groups (`grafana-editors`→editor, `grafana-viewers`→viewer, `sovereign-admins`→admin). | ☐ | — |
| SSO-gitea | `/app/bp-gitea` (instance) | Click **Open** | New tab lands in **Gitea** ALREADY signed in — silent OIDC, NO login form. Realm=`sovereign`. **Quirk:** Gitea OAuth uses `--use-custom-url-mapping --custom-auth-url` so the `authorize_url` carries `?kc_idp_hint=catalyst-pin` (`platform/gitea/chart` configure-oauth Job). | ☐ | — |
| SSO-harbor | `/app/bp-harbor` (instance, `ui` endpoint) | Click **Open** | New tab lands in **Harbor** ALREADY signed in — silent OIDC, NO login form. Realm=`sovereign`. **Quirk:** silent SSO rides Harbor's `oidc_extra_redirect_parms` config field set to `{"kc_idp_hint":"catalyst-pin"}` by the configure-oauth Job (`platform/harbor/chart`). (The 2nd `registry` endpoint is `ssoEnabled: false` — OCI clients use robot accounts, **no** Open button there.) | ☐ | — |
| SSO-openbao | `/app/bp-openbao` (instance, `api` endpoint) | Click **Open** | New tab lands in **OpenBao** ALREADY signed in via OIDC — Realm=`sovereign`, `default_role=operator`. **Quirk:** OpenBao's `api` endpoint is `visibility: private`; silent SSO does **NOT** ride a `prompt=none` URL param — the hashicorp/cap library (`builtin/credential/jwt/path_oidc.go createOIDCRequest`) builds the `auth_url` server-side and exposes **no** query-param knob, so silent login is delivered **only** by the KC realm IDR `defaultProvider=catalyst-pin` (NOT by URL hint). Must still land signed-in with no KC login form. | ☐ | — |

### 2.2 Tier-2 — sovereign-realm operator-facing (4) — #2744 federate-to-sovereign

| # | Tested page | Test case | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| SSO-guacamole | `/app/bp-guacamole` (instance) | Click **Open** | New tab lands in **Guacamole** ALREADY signed in — silent OIDC, NO login form. Realm=`sovereign` (`sovereign-admins`→admin). | ☐ | — |
| SSO-powerdns-admin | `/app/bp-powerdns-admin` (instance) | Click **Open** | New tab lands in **PowerDNS-Admin** ALREADY signed in — silent OIDC, NO login form. Realm=`sovereign`. Endpoint `visibility: private` (operator-only). | ☐ | — |
| SSO-netbird | `/app/bp-netbird` (instance) | Click **Open** | New tab lands in **NetBird** dashboard ALREADY signed in — silent OIDC, NO login form. Realm=`sovereign` (`sovereign-admins`→admin). | ☐ | — |
| SSO-spire | `/app/bp-spire` (instance) | Click **Open** | **⛔ #3084-BLOCKED:** `platform/spire` is a scaffold chart with **no** `spec.sso` block and **no** Keycloak OIDC client — there is currently NO ssoEnabled endpoint and NO Open button. Expectation once #3084 ships: silent OIDC into the SPIRE console at realm=`sovereign` (or explicit de-scope, since SPIRE is SPIFFE machine-identity, not human login). Until #3084: this row is **⛔ blocked**, not a pass. | ⛔ | #3084 |

### 2.3 Tier-3 — per-Org-realm tenant apps (realm=`{{.OrgSlug}}`) — #2744 2-hop broker

> Walked from the **tenant** console after Org onboarding. Each Org gets its own KC realm with a Keycloak-OIDC IdP federated to the `sovereign` realm broker (decision #6). `prompt=none&kc_idp_hint=catalyst-pin` on every Open.

| # | Tested page | Test case | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| SSO-opensearch-dashboards | `/app/bp-opensearch` (instance, `dashboards` endpoint) | Click **Open** | New tab lands in **OpenSearch Dashboards** ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}` (`opensearch-admins`→admin, `opensearch-users`→user). | ☐ | — |
| SSO-opensearch-api | `/app/bp-opensearch` (instance, `api` endpoint) | Inspect the `api` endpoint | `api` endpoint is `ssoEnabled: true` but `launchDefault: false` — **no** primary Open button (Dashboards is the launch target); API auth still OIDC-bearer at realm=`{{.OrgSlug}}`. | ☐ | — |
| SSO-matrix | `/app/bp-matrix` (instance) | Click **Open** | New tab lands in **Matrix** (Element) ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. | ☐ | — |
| SSO-langfuse | `/app/bp-langfuse` (instance) | Click **Open** | New tab lands in **Langfuse** ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. | ☐ | — |
| SSO-temporal | `/app/bp-temporal` (instance) | Click **Open** | New tab lands in **Temporal** Web UI ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. | ☐ | — |
| SSO-librechat | `/app/bp-librechat` (instance) | Click **Open** | New tab lands in **LibreChat** ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. | ☐ | — |
| SSO-openmeter | `/app/bp-openmeter` (instance) | Click **Open** | New tab lands in **OpenMeter** ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. | ☐ | — |
| SSO-wordpress-tenant | `/app/bp-wordpress-tenant` (instance) | Click **Open** | New tab lands in **WordPress** (wp-admin) ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. Hostname is per-instance `{{.AppName}}.{{.OrgSlug}}.<sov>`. | ☐ | — |
| SSO-vllm | `/app/bp-vllm` (instance) | Click **Open** | New tab lands in **vLLM** UI ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. | ☐ | — |
| SSO-kserve | `/app/bp-kserve` (instance) | Click **Open** | New tab lands in **KServe** UI ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. | ☐ | — |
| SSO-livekit | `/app/bp-livekit` (instance) | Click **Open** | New tab lands in **LiveKit** UI ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. | ☐ | — |
| SSO-llm-gateway | `/app/bp-llm-gateway` (instance) | Click **Open** | New tab lands in **LLM Gateway** UI ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. | ☐ | — |
| SSO-flink | `/app/bp-flink` (instance) | Click **Open** | New tab lands in **Flink** dashboard ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. | ☐ | — |
| SSO-neo4j | `/app/bp-neo4j` (instance, `browser` endpoint) | Click **Open** | New tab lands in **Neo4j Browser** ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. (The `bolt` endpoint is `ssoEnabled: false` — driver auth, no Open button.) | ☐ | — |
| SSO-litmus | `/app/bp-litmus` (instance) | Click **Open** | New tab lands in **LitmusChaos** ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. (Topology = singleton.) | ☐ | — |
| SSO-stalwart-tenant | `/app/bp-stalwart-tenant` (instance, `webmail` endpoint) | Click **Open** | New tab lands in **Stalwart webmail** ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. (The `smtp`/`imap` endpoints are `ssoEnabled: false` — protocol auth, no Open button.) | ☐ | — |

### 2.4 Non-SSO / no-Open endpoints — negative-assertion (must NOT show an Open button)

> Extracted apps whose front-door endpoint is `ssoEnabled: false` or that have **no** Keycloak login surface — the React console must **not** render an Open silent-SSO button for these.

| # | Tested page | Test case | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| SSO-neg-keycloak | `/app/bp-keycloak` → Endpoints | Inspect `ui`/`auth` endpoint | Keycloak's own `ui` endpoint is `ssoEnabled: false` (it IS the IdP) — **no** silent-SSO Open button; the KC admin console is a direct login. | ☐ | — |
| SSO-neg-hubble | `/app/bp-cilium` → Endpoints | Inspect `hubble` endpoint | Hubble UI endpoint exists (`hubble.<sov>`, `visibility: private`) but is **NOT** `ssoEnabled` in the blueprint → **no** Open silent-SSO button today. (Founder named hubble; current blueprint carries no `sso` block — documented gap, not a pass.) | ☐ | — |
| SSO-neg-wire | `/app/bp-{strimzi,valkey,ferretdb,clickhouse,milvus,iceberg,stunner}` → Endpoints | Inspect the wire/grpc/turn endpoint | These expose only protocol endpoints (`kafka`/`valkey`/`db`/`grpc`/`catalog`/`turn`) with `ssoEnabled: false` → **no** Open button; clients authenticate at the wire protocol, not via browser SSO. | ☐ | — |
| SSO-neg-registry | `/app/bp-harbor` → `registry` endpoint | Inspect `registry` endpoint | `registry.<sov>` is `ssoEnabled: false` — docker/OCI robot-account auth, **no** Open button (Harbor `ui` is the SSO surface — covered by SSO-harbor). | ☐ | — |

## 3. PER-APP topology — ONE ROW PER app carrying a non-trivial topology

> **The founder's #2 demand: an app-by-app topology view (openbao topology view named explicitly) — never a generic claim.**
>
> Extracted from `spec.topology.defaults.multi-region` in each `platform/<app>/blueprint.yaml`. Walked on a **2-region** Sovereign (hw100): install the app, open `/app/$id` → **Topology tab** (`products/catalyst/bootstrap/ui/src/pages/sovereign/AppDetail/TopologyTab.tsx`), read the placement + `perCluster[]` and confirm it matches the declared `spec.topology`. Counts match the audit: **10 active-hot-standby / 14 active-active / 20 active-passive / 44 singleton.**
>
> Topology → expectation mapping:
> - **active-active** → N active HRs, **one per region**, both serving live traffic (load-balanced).
> - **active-hot-standby** → 2 HRs: primary **active** + secondary **passive/warm** kept in sync, promotes on region-kill.
> - **active-passive** → primary **active** + secondary **cold/warm** standby (DR target; not serving until promoted).
> - **singleton** → **1** HR on the primary cluster only; **region-kill loses it** until restored (Velero).

### 3.1 active-hot-standby (10) — each individually

| # | Tested page | Test case | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| TOPO-catalyst-platform | `/app/bp-catalyst-platform` → Topology | Read placement | **active-hot-standby** → primary `mgmt-A` active + `mgmt-B` warm standby (Catalyst CRDs + PG via bp-cnpg-pair); `bp-continuum` flips PowerDNS for `console.<sov>`/`api.<sov>` on region-kill. `perCluster[]` shows 2 mgmt clusters. | ☐ | — |
| TOPO-keycloak | `/app/bp-keycloak` → Topology | Read placement | **active-hot-standby** → `mgmt-A` active + `mgmt-B` passive; PG via bp-cnpg-pair sync; DNS flip `auth.<sov>` ~30s on promote. | ☐ | — |
| TOPO-gitea | `/app/bp-gitea` → Topology | Read placement | **active-hot-standby** → primary HR active + secondary passive (sync); PG via bp-cnpg-pair, Git blobs on SeaweedFS S3; flip `gitea.<sov>`. | ☐ | — |
| TOPO-grafana | `/app/bp-grafana` → Topology | Read placement | **active-hot-standby** → 2 HRs: primary active + secondary passive (sync); Grafana DB on bp-cnpg-pair; flip `grafana.<sov>` ~30s. | ☐ | — |
| TOPO-harbor | `/app/bp-harbor` → Topology | Read placement | **active-hot-standby** → primary active + secondary passive; PG via bp-cnpg-pair, image blobs on object-storage replication; flip `harbor.<sov>`+`registry.<sov>`. | ☐ | — |
| TOPO-guacamole | `/app/bp-guacamole` → Topology | Read placement | **active-hot-standby** → primary active + secondary passive; PG via bp-cnpg-pair; flip `guac.<sov>` (in-progress remote-desktop sessions drop). | ☐ | — |
| TOPO-netbird | `/app/bp-netbird` → Topology | Read placement | **active-hot-standby** → primary active + secondary passive; mgmt state in PG via bp-cnpg-pair (candidate CP); flip `netbird.<sov>`. | ☐ | — |
| TOPO-spire | `/app/bp-spire` → Topology | Read placement | **active-hot-standby** declared → primary active + secondary passive; SPIRE Server datastore in PG. (SSO is #3084-blocked, but the topology declaration is present and walkable.) | ☐ | — |
| TOPO-cnpg-pair | `/app/bp-cnpg-pair` → Topology | Read placement | **active-hot-standby** → the canonical Pillar-3 pair: `rtz-A` active + `rtz-B` warm standby, **synchronous** streaming (`remote_apply`) over Cilium ClusterMesh, zero-tx-loss; `bp-continuum` lease + `cnpg promote` + PowerDNS lua-flip `db.<org>.<sov>`. **Ties TC-13/TC-14.** | ☐ | — |
| TOPO-sandbox | `/app/bp-sandbox` → Topology | (OUT of scope) | sandbox declares **active-hot-standby**, but **Sandbox is OUT of scope** for this UAT — row recorded for completeness only, not walked. | ☐ | — |

### 3.2 active-passive (20) — each individually (incl. openbao, founder-named)

| # | Tested page | Test case | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| TOPO-openbao | `/app/bp-openbao` → Topology | Read placement | **active-passive** → primary **active** (Raft 3-5 replicas) + secondary **warm** standby via async perf-replication; `bp-continuum` runs `vault operator raft transition-to-primary` on the standby; KV reads continue uninterrupted on the replica. `perCluster[]` shows primary + passive. **(Founder named this view explicitly.)** | ☐ | — |
| TOPO-stalwart-tenant | `/app/bp-stalwart-tenant` → Topology | Read placement | **active-passive** → primary active + secondary warm; PG via bp-cnpg-pair, mail blobs on tenant S3 replicated; flip `mail.<org>.<sov>`. | ☐ | — |
| TOPO-matrix | `/app/bp-matrix` → Topology | Read placement | **active-passive** → primary active + secondary warm; homeserver DB in PG via bp-cnpg-pair, media on S3; flip `matrix.<org>.<sov>`. | ☐ | — |
| TOPO-langfuse | `/app/bp-langfuse` → Topology | Read placement | **active-passive** → primary active + secondary warm; traces in PG (bp-cnpg-pair) + ClickHouse; flip `langfuse.<org>.<sov>`. | ☐ | — |
| TOPO-temporal | `/app/bp-temporal` → Topology | Read placement | **active-passive** → primary active + secondary warm; history in PG (bp-cnpg-pair) + visibility in OpenSearch; workflows resume from history on promote. | ☐ | — |
| TOPO-openmeter | `/app/bp-openmeter` → Topology | Read placement | **active-passive** → primary active + secondary warm; events in ClickHouse + state in PG (bp-cnpg-pair); flip `openmeter.<org>.<sov>`. | ☐ | — |
| TOPO-neo4j | `/app/bp-neo4j` → Topology | Read placement | **active-passive** → primary active + read-replica/standby; graph store on PVC, cross-region via backup-restore; promote read-replica or Velero restore. | ☐ | — |
| TOPO-wordpress-tenant | `/app/bp-wordpress-tenant` → Topology | Read placement | **active-passive** → primary active + secondary warm; PG via bp-cnpg-pair, media on tenant S3 replicated; flip `<site>.<org>.<sov>`. | ☐ | — |
| TOPO-ferretdb | `/app/bp-ferretdb` → Topology | Read placement | **active-passive** → primary active + secondary warm; Mongo-API over PG backend via bp-cnpg-pair (ferretdb itself stateless). | ☐ | — |
| TOPO-valkey | `/app/bp-valkey` → Topology | Read placement | **active-passive** → master + replica; Sentinel-pair across clusters via ClusterMesh; `bp-continuum` Sentinel failover (~5s reconnect). | ☐ | — |
| TOPO-milvus | `/app/bp-milvus` → Topology | Read placement | **active-passive** → primary active + standby queriers; vector index on S3 + metadata in etcd, both replicated. | ☐ | — |
| TOPO-flink | `/app/bp-flink` → Topology | Read placement | **active-passive** → jobmanager active + standby (ZK/k8s leader-election); checkpoints on S3 replicated; resumes from last checkpoint. | ☐ | — |
| TOPO-debezium | `/app/bp-debezium` → Topology | Read placement | **active-passive** → connector active on primary + restartable on standby reading latest Kafka offset (offsets in bp-strimzi, MM2-replicated). | ☐ | — |
| TOPO-loki | `/app/bp-loki` → Topology | Read placement | **active-passive** → primary active + standby querier; all chunks/index on object-storage (bucket-replicated); flip `loki.<sov>`. | ☐ | — |
| TOPO-mimir | `/app/bp-mimir` → Topology | Read placement | **active-passive** → primary active + standby querier; TSDB blocks on object-storage replicated. | ☐ | — |
| TOPO-tempo | `/app/bp-tempo` → Topology | Read placement | **active-passive** → primary active + standby querier; traces on object-storage replicated. | ☐ | — |
| TOPO-nats-jetstream | `/app/bp-nats-jetstream` → Topology | Read placement | **active-passive** → 3-node JetStream (Raft) active in mgmt + leaf-node tunnel to standby; in-flight msgs drop unless `stream.mirror` enabled. | ☐ | — |
| TOPO-newapi | `/app/bp-newapi` → Topology | Read placement | **active-passive** → stateless API active on primary + standby; DNS flip via bp-continuum. | ☐ | — |
| TOPO-sso-bridge | `/app/bp-sso-bridge` → Topology | Read placement | **active-passive** → stateless auth bridge active on primary + standby; DNS flip via bp-continuum. | ☐ | — |
| TOPO-k8s-ws-proxy | `/app/bp-k8s-ws-proxy` → Topology | Read placement | **active-passive** → stateless WebSocket proxy active + standby; new connections route to standby, in-flight WS drop. | ☐ | — |

### 3.3 active-active (14) — each individually

| # | Tested page | Test case | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| TOPO-opensearch | `/app/bp-opensearch` → Topology | Read placement | **active-active** → N active HRs, **one per region**, both ingesting; cross-cluster replication (CCR follower catches up from leader). `perCluster[]` shows ≥2 active. | ☐ | — |
| TOPO-clickhouse | `/app/bp-clickhouse` → Topology | Read placement | **active-active** → each region runs its own replica of the same shard (native replication via Keeper); both ingest. | ☐ | — |
| TOPO-strimzi | `/app/bp-strimzi` → Topology | Read placement | **active-active** → per-cluster Kafka, MirrorMaker2 replicates topics both ways; consumer offset translation via MM2. | ☐ | — |
| TOPO-iceberg | `/app/bp-iceberg` → Topology | Read placement | **active-active** → catalog (Hive/REST) active in both; data + table-metadata on S3, data plane reads same S3 from both regions. | ☐ | — |
| TOPO-livekit | `/app/bp-livekit` → Topology | Read placement | **active-active** → stateless SFU active in both regions; new rooms route to surviving region (rooms ephemeral). | ☐ | — |
| TOPO-stunner | `/app/bp-stunner` → Topology | Read placement | **active-active** → stateless TURN/STUN relay active in both; new ICE sessions route to surviving region. | ☐ | — |
| TOPO-vllm | `/app/bp-vllm` → Topology | Read placement | **active-active** → stateless GPU inference active in both; weights on shared PVC / baked image; new requests route to survivor. | ☐ | — |
| TOPO-kserve | `/app/bp-kserve` → Topology | Read placement | **active-active** → stateless model serving active in both; artifacts on S3. | ☐ | — |
| TOPO-knative | `/app/bp-knative` → Topology | Read placement | **active-active** → controller active in both; serverless Pods ephemeral; scale-to-zero unaffected. | ☐ | — |
| TOPO-librechat | `/app/bp-librechat` → Topology | Read placement | **active-active** → UI stateless active in both; chat history in PG via bp-cnpg-pair. | ☐ | — |
| TOPO-llm-gateway | `/app/bp-llm-gateway` → Topology | Read placement | **active-active** → stateless proxy active in both; secrets via External-Secrets. | ☐ | — |
| TOPO-anthropic-adapter | `/app/bp-anthropic-adapter` → Topology | Read placement | **active-active** → stateless adapter active in both; DNS load-balanced. | ☐ | — |
| TOPO-bge | `/app/bp-bge` → Topology | Read placement | **active-active** → stateless embedding service active in both; model in image. | ☐ | — |
| TOPO-nemo-guardrails | `/app/bp-nemo-guardrails` → Topology | Read placement | **active-active** → stateless policy enforcement active in both; policies from Git. | ☐ | — |

### 3.4 singleton (44) — representative set + coverage-sweep

> Singletons declare **1 HR on the primary cluster only**; region-kill loses the instance until Velero-restored. Representative explicit rows below, then one coverage-sweep row for the remainder.

| # | Tested page | Test case | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| TOPO-powerdns-admin | `/app/bp-powerdns-admin` → Topology | Read placement | **singleton** → 1 HR (admin UI state in PG); a cluster failure means edit via another cluster's instance; no cross-cluster sync. | ☐ | — |
| TOPO-litmus | `/app/bp-litmus` → Topology | Read placement | **singleton** → 1 HR; chaos experiments are intentionally cluster-scoped; region-kill loses experiment runs. | ☐ | — |
| TOPO-seaweedfs | `/app/bp-seaweedfs` → Topology | Read placement | **singleton** (per cluster) → 1 HR primary; within-cluster replicas + erasure coding; cross-cluster is async pull only — **region-kill-loses-it** warning if not replicated. | ☐ | — |
| TOPO-cnpg | `/app/bp-cnpg` → Topology | Read placement | **singleton** → 1 HR operator (stateless); the Clusters it manages get their own topology via bp-cnpg-pair. | ☐ | — |
| TOPO-cilium | `/app/bp-cilium` → Topology | Read placement | **singleton** (per cluster) → each cluster owns its identity space; ClusterMesh shares endpoint reachability only, not control state. | ☐ | — |
| TOPO-velero | `/app/bp-velero` → Topology | Read placement | **singleton** → 1 HR; backup catalog in PVC + blobs on replicated S3; restore is operator-initiated (it IS the DR tool). | ☐ | — |
| TOPO-singleton-sweep | each remaining singleton's `/app/$id` → Topology | Walk the rest | **Coverage sweep** for the remaining ~38 singletons (alloy, cert-manager(+webhooks), coraza, crossplane(+claims), external-dns, external-secrets(+stores), falco, flux, gateway-api, hcloud-*, kyverno(+policies), network-policies, opentelemetry(+operator), reflector, reloader, sealed-secrets, self-sovereign-cutover, sigstore, syft-grype, trivy, vpa, velero-hcs, qa-app, stalwart-sovereign, bp-*-vcluster, bp-vcluster-helmrepo, cluster-autoscaler-hcloud): each shows **1 HR on the primary cluster only**, no cross-cluster sync, region-kill warning where stateful. `bp-stalwart-sovereign` is **external** (mothership) — no Sovereign placement. `openclaw` is a scaffold with no topology declared yet. | ☐ | — |

## 4. PER-APP / per-zone vCluster containment

> Each App Blueprint must run **inside** its declared vCluster (mgmt / dmz / rtz), with **only** substrate prerequisites on the host cluster. No App Blueprint may schedule directly on a host node. Walked at `/app/$id` → placement (vCluster column). Grouped by zone; representative apps named per group.

| # | Zone / scope | Apps (representative) | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| VC-mgmt | **mgmt** vCluster (control plane) | bp-catalyst-platform, bp-keycloak, bp-gitea, bp-harbor, bp-grafana, bp-openbao, bp-guacamole, bp-netbird, bp-powerdns-admin, bp-spire, bp-loki/mimir/tempo, bp-nats-jetstream, bp-sso-bridge | Each runs **inside** a `mgmt` vCluster (catalyst-platform `mgmt-A`/`mgmt-B`); none scheduled on the host node. `perCluster[].vcluster` = mgmt. | ☐ | — |
| VC-rtz | **rtz** vCluster (regulated tenant) | bp-cnpg-pair, bp-ferretdb, bp-valkey, bp-opensearch, bp-clickhouse, bp-matrix, bp-langfuse, bp-temporal, bp-openmeter, bp-neo4j, bp-stalwart-tenant, bp-wordpress-tenant, bp-milvus, bp-flink, bp-strimzi | Each runs **inside** the Org's `rtz` vCluster; tenant workloads never touch a host node. `perCluster[].vcluster` = rtz. | ☐ | — |
| VC-dmz | **dmz** vCluster (DMZ tenant) | tenant Apps placed in the DMZ tier per Org policy (bp-dmz-vcluster host) | DMZ-tier tenant Apps run **inside** the `dmz` vCluster; substrate-only on host. | ☐ | — |
| VC-host-substrate | **host** cluster (substrate only) | bp-cilium, bp-cert-manager(+webhooks), bp-flux, bp-gateway-api, bp-crossplane, bp-external-secrets, bp-external-dns, bp-kyverno, bp-sealed-secrets, bp-coraza, bp-falco, bp-vpa, bp-reloader, bp-reflector, bp-opentelemetry, bp-seaweedfs, bp-velero, bp-*-vcluster operators | These ARE the host-substrate prereqs — they run on the host cluster by design (singleton per cluster). **No App-tier Blueprint** (anything in §2/§3.1–§3.3 tenant/CP list) should appear on a host node. | ☐ | — |

---

## Coverage summary

- **Per-app SSO rows (§2):** 24 ssoEnabled blueprints, **26 ssoEnabled endpoints** → explicit rows. Tier-1 (4): grafana, gitea, harbor, openbao. Tier-2 (4): guacamole, powerdns-admin, netbird, spire(⛔ #3084). Tier-3 (16 endpoints): opensearch×2, matrix, langfuse, temporal, librechat, openmeter, wordpress-tenant, vllm, kserve, livekit, llm-gateway, flink, neo4j, litmus, stalwart-tenant. Plus negative-assertion rows for keycloak/hubble/wire-protocol/registry (no Open button). **catalyst-platform** (console/api) is the console itself — its SSO is exercised by Pillar-1 login, not an Open button.
- **Per-app topology rows (§3):** every active-hot-standby (10), active-passive (20), active-active (14) app individually; singletons (44) as a representative set + coverage-sweep. Matches the audit's 10 / 20 / 14 / 44.
- **gitea / openbao / grafana each have BOTH** an explicit SSO row (SSO-gitea, SSO-openbao, SSO-grafana) **and** an explicit topology row (TOPO-gitea, TOPO-openbao, TOPO-grafana). ✔
- **Gaps flagged (not passes):** spire SSO ⛔ #3084 (scaffold, no OIDC client); hubble has an endpoint but no `sso` block (no Open button today); sandbox topology recorded but OUT of scope.
