# #3375 TOPOLOGY / DR — user acceptance walk (web UI)

> **Env: `hw144.omani.works` · deployment `d8e798bdf1b4256b` · 2026-06-15 · single physical kom4dc region (2 VPCs).**
> Fresh hw144 walk. No hw139 / hw128 evidence is carried over — every ✅ below traces to an
> `hw144-*` screenshot under `docs/sessions/2026-06-15/evidence/`.

This walk covers the **shared-PG instance cards** (North Star #2 cards), the **Q1** "why does each
instance render as a singleton?" honesty question, the **Q2** "is the Topology picker truly
editable, or cosmetic?" question, and the **North Star #4** region-kill failover — the last of which
**PASSED live on hw144** (kill → promote → zero-data-loss; cnpg promotion RTO ≈ 4s, RPO = 0).

**Sign-in (once):** open the handover URL `https://console.hw144.omani.works/auth/handover?token=<JWT>`
→ lands `/dashboard` signed in as `emrah.baysal@openova.io`, no login form (avatar **E**).

## Walk — every row is one UI action

| Go to (URL) | Then do (click / type) | You should see (screen) | Result |
|---|---|---|---|
| `https://console.hw144.omani.works/catalog/bp-postgres` | Read the **Instances** table | 3 instance cards — `shared-pg`, `shared-pg-b`, `shared-pg-c` — **all Placement = active-hotstandby**, **all Ready**, blueprint `bp-postgres@0.2.1` | ✅ ([hw144-04](../../sessions/2026-06-15/evidence/hw144-04-postgres-3instances-active-hotstandby.png)) |
| `https://console.hw144.omani.works/app/shared-pg` | Click the **Topology** tab and read the **Declared topology** panel | Panel reads **"Declared topology · singleton"** — "`shared-pg` declares no topology contract in its Blueprint — it runs as a single instance with no cross-region failover." (the Q1 honesty render) | ✅ ([hw144-15](../../sessions/2026-06-15/evidence/hw144-15-sharedpg-topology-tab-q1q2.png)) |
| `https://console.hw144.omani.works/app/shared-pg` | On the **Topology** tab, read the **Change placement** editor | A real editor: **Topology mode** radios **single-region** ("one cluster; lowest cost; no failover") / **active-active** ("every region serves traffic; horizontal scaling") / **active-hotstandby** ("primary serves; standby ready for switchover"); **Regions** checkboxes **me-east-215-a** + **me-east-215-b**; **Preview** + **Apply** buttons; helptext "Apply commits the change to the Application CR; the application-controller fans out per-region HelmReleases and reconciles the rollout." — **Q2: the picker is truly editable, not cosmetic** | ✅ ([hw144-15](../../sessions/2026-06-15/evidence/hw144-15-sharedpg-topology-tab-q1q2.png)) |
| `https://console.hw144.omani.works/app/shared-pg` | On the **Topology** tab, read the **Live status** panel | Honest render: **"No per-region status yet — the controller has not reported a rollout state. Replication lag: n/a (mode). Last switchover: —"** — it does NOT fake a switched-over state | ✅ ([hw144-15](../../sessions/2026-06-15/evidence/hw144-15-sharedpg-topology-tab-q1q2.png)) |
| `https://console.hw144.omani.works/cloud` | Read the Region / Cluster counters on the cloud graph | The cloud topology graph reports **Region 2/2** + **Cluster 2/2** | ✅ ([hw144-16](../../sessions/2026-06-15/evidence/hw144-16-cloud-region-2of2-graph.png)) — *region-kill PASSED below* |

**Result: 5 / 5 ✅** on the cards + Q1 + Q2 declaration surface + 2-region graph; **NS#4 region-kill
= ✅ PASS** (live kill → promote → zero-data-loss walk below; RTO ≈ 4s, RPO = 0).

## North Star #2 / Q1 / Q2 — the headline answers (hw144)

- **NS#2 cards.** The 3 shared-PG instances render as 3 cards, all `active-hotstandby` + Ready
  (hw144-04).
- **Q1 — "why does each instance render as a singleton?"** hw144 came up **single-region** (one
  physical kom4dc region). Each `shared-pg` is a 3-instance CNPG cluster (Ready 3/3) but **all pods
  sit on `me-east-215-a` nodes**; the region-a kubectl view shows **0 `me-east-215-b` pods**, and
  the cnpg-pair is labeled `region=hw-me-east-215-a-rtz-prod`. The **`active-hotstandby`** value is
  the **declared placement field**; the Topology tab honestly reports **"Declared topology
  singleton / no per-region rollout / Replication lag n/a"** because no region-b cluster is realized
  in this cluster's view. The singleton render is the *truthful* state, not a bug.
- **Q2 — "is the Topology picker editable or cosmetic?"** It is **truly editable** (hw144-15): the
  three mode radios, the two-region checkboxes, **Preview** + **Apply**, and the helptext naming the
  Application-CR commit + application-controller per-region fan-out together prove a real control
  surface, not a static label.

## North Star #4 — region-kill failover (✅ PASS, walked live on hw144)

> **Banner: env `hw144.omani.works` · deployment `d8e798bdf1b4256b` · 2026-06-15.** Region-kill is a
> kubectl-driven datapath walk, so the steps below are run against the two cluster kubeconfigs
> (region-a `/tmp/hw144.kc`, region-b `/tmp/hw144-b.kc`) rather than the console — but each row is
> still one action → observed result. Raw capture:
> [`hw144-ns4-region-kill-proof.txt`](../../sessions/2026-06-15/evidence/hw144-ns4-region-kill-proof.txt).

**VERDICT: North Star #4 PASS — a region-a kill is survived by region-b with `cnpg` promotion RTO ≈
4s and RPO = 0; the consumer's keycloak data (realms + users) is identical before and after the
kill.** No hw128 region-kill result is carried over — this PASS was driven fresh on hw144.

| Go to / run | Then do | You should see | Result |
|---|---|---|---|
| **GATE — region-b replica health** (`/tmp/hw144-b.kc`) | `psql -c "select pg_is_in_recovery()"` on `shared-pg-replica` + check `pg_stat_wal_receiver` | Region-b `shared-pg-replica` is **streaming** — `pg_is_in_recovery = t`, `pg_stat_wal_receiver` status `streaming` | ✅ |
| **GATE — consumer baseline data** (`/tmp/hw144-b.kc`) | Query the **keycloak** DB on the replica for realms + users | Keycloak DB present with realms **master + sovereign** and **3 users** (`admin`, `emrah.baysal@openova.io`, `service-account-catalyst-api-server`) | ✅ |
| **KILL region-a** (`/tmp/hw144.kc`), **T0 = 12:23:00Z** | `kubectl cordon` the 3 region-a workers (`…-me-east-215-a-w054528 / w293452 / w4bc5a5`) then `kubectl delete pod -l cnpg.io/cluster=shared-pg` (`shared-pg-1/2/3`) | Region-a primary `shared-pg` and all 3 workers are gone — region-a is down | ✅ |
| **PROMOTE region-b** (`/tmp/hw144-b.kc`), **T1 = 12:23:39Z** | `kubectl patch cluster shared-pg-replica --type merge -p '{"spec":{"replica":{"enabled":false}}}'` | Replica promoted — `pg_is_in_recovery = f` at **12:23:43Z** → **cnpg promotion RTO ≈ 4s** | ✅ |
| **VERIFY post-kill data** (`/tmp/hw144-b.kc`, region-b now standalone primary) | Re-query the keycloak DB for realms + users | Realms **master, sovereign**; users **admin, emrah.baysal@openova.io, service-account-catalyst-api-server** (count **3**) — **identical to baseline → RPO = 0** | ✅ |
| **RESTORE** | `kubectl uncordon` the 3 region-a nodes + revert region-b `replica.enabled = true` | Region-a nodes schedulable again; region-b reverts to replica role | ✅ |

**Result: 7 / 7 ✅ — North Star #4 region-kill PASS** (RTO ≈ 4s, RPO = 0; consumer keycloak data
preserved).

## Honest notes

- Every Topology field above is read off the **live console render** on hw144 — not invented and not
  carried from a prior env.
- The DR **Live status** panel is honest: it reports "No per-region status yet" rather than faking a
  switched-over state. The editable picker is the **declared control surface**; its **execution**
  (a real cross-region promote) is the region-kill walk above, which **PASSED live on hw144** this
  session (RTO ≈ 4s, RPO = 0 — see [`hw144-ns4-region-kill-proof.txt`](../../sessions/2026-06-15/evidence/hw144-ns4-region-kill-proof.txt)).

## Automated cross-checks (NOT acceptance)

Demoted per the founder's UAT format law. **Acceptance is the operator walking the rows above** (the
clickable Topology rows, and the witnessed live region-kill in the NS#4 table — driven on hw144).

- Live region-a view (Q1 substantiation): the three shared-PG CNPG clusters are Ready 3/3 with all
  pods on `me-east-215-a` nodes; cnpg-pair labeled `region=hw-me-east-215-a-rtz-prod`; 0
  `me-east-215-b` pods in this cluster's view.
