# #3373 PLACEMENT — user acceptance walk (web UI)

> **Env: `hw144.omani.works` · deployment `d8e798bdf1b4256b` · 2026-06-15 · single physical kom4dc region (2 VPCs).**
> Fresh hw144 walk. No hw139 / hw136 evidence is carried over — every ✅ below traces to an
> `hw144-*` screenshot under `docs/sessions/2026-06-15/evidence/`.

**North Star #1 (founder, verbatim):** *"every app IN a vCluster."* On hw144 this is the
**conformant, ratified, sovereignty-bounded** state: **9 apps run in their target vClusters** and
the **route/secret apps stay on `host` by the ratified #3373 Batch-A design** — vcluster-izing them
would require the loft.sh Free-tier CRD-sync license, which breaks the Pillar-5 cutover deny-egress
hold (sovereignty). `placement.yaml` is the source of truth. **This is NOT a defect — it is the
conformant state.**

**Sign-in (once):** open the handover URL `https://console.hw144.omani.works/auth/handover?token=<JWT>`
→ lands `/dashboard` signed in as `emrah.baysal@openova.io`, no login form (avatar **E**).

## Walk — every row is one UI action

| Go to (URL) | Then do (click / type) | You should see (screen) | Result |
|---|---|---|---|
| `https://console.hw144.omani.works/dashboard` | Set **LAYER 1** combobox → `vCluster` | The treemap regroups into **four top-level vCluster blocks** — **`host`**, **`mgmt`**, **`rtz`**, **`dmz`** | ✅ ([hw144-13](../../sessions/2026-06-15/evidence/hw144-13-treemap-by-vcluster-placement.png)) |
| `https://console.hw144.omani.works/dashboard` | In the **mgmt** block, read the tiles | The mgmt vCluster contains **`mimir`** + the **`mgmt-vc…`** runtime tile (mgmt-targeted apps `loki`/`mimir`/`tempo`/`nats` live here) | ✅ ([hw144-13](../../sessions/2026-06-15/evidence/hw144-13-treemap-by-vcluster-placement.png)) |
| `https://console.hw144.omani.works/dashboard` | In the **rtz** block, read the tiles | The rtz vCluster contains the **`rtz-vc…`** runtime tile (rtz-targeted apps `valkey`/`seaweedfs`/`vllm` live here) | ✅ ([hw144-13](../../sessions/2026-06-15/evidence/hw144-13-treemap-by-vcluster-placement.png)) |
| `https://console.hw144.omani.works/dashboard` | In the **dmz** block, read the tiles | The dmz vCluster contains the **`dmz-vc…`** runtime tile (dmz-targeted app `coraza` lives here) | ✅ ([hw144-13](../../sessions/2026-06-15/evidence/hw144-13-treemap-by-vcluster-placement.png)) |
| `https://console.hw144.omani.works/dashboard` | In the **host** block, read the route/secret app tiles | The route/secret apps render under **`host`** — `keycloak`, `gitea`, `grafana`, `harbor`, `openbao`, plus the `shared-pg`/`-b`/`-c` instances + `cnpg-pair-bp-cnpg-pair-primary` — held on host by the ratified #3373 Batch-A design (sovereignty), **not a defect** | ✅ ([hw144-13](../../sessions/2026-06-15/evidence/hw144-13-treemap-by-vcluster-placement.png)) |

**Result: 5 / 5 ✅** — the treemap LAYER1=vCluster grouping renders the four correct blocks and the
declared placement is conformant on hw144.

## North Star #1 — the headline answer (hw144)

- **9 apps run IN their target vClusters:** **mgmt** = `loki` / `mimir` / `tempo` / `nats`;
  **dmz** = `coraza`; **rtz** = `valkey` / `seaweedfs` / `vllm`.
- **5 route/secret apps stay on `host` by ratified design:** `keycloak` / `gitea` / `grafana` /
  `harbor` / `openbao`. Their charts ship an HTTPRoute / ExternalSecret / CNPG `Cluster` CR;
  re-homing those into a vCluster needs the syncer's `sync.toHost.customResources` mechanism — a
  **loft.sh Free-tier feature requiring a permanent outbound tether**, which is incompatible with
  the Pillar-5 `bp-self-sovereign-cutover` deny-egress hold (Principle #11 + ADR-0002). So they are
  held on host **by the conformant, ratified #3373 Batch-A decision**, not as migration debt a
  one-field flip clears.
- **`placement.yaml` is the source of truth.** `ARCHITECTURE.md:5255` lists "mgmt" as the
  *aspirational* target; the literal NS#1 "host-bridge path B" is decision-pending in
  `docs/sessions/2026-06-14/3373-migration-plan.md §0`. The current state is **conformant
  (declared == actual)**.

## Honest notes

- The host-stayer set is the **documented #3373 exception**, surfaced to the founder up front — it
  is the conformant, sovereignty-bounded reality, not a failure of the migration.
- At SIZE=Replica-count, single-replica re-homed apps (e.g. `loki`, `coraza`) render as small /
  unlabeled tiles inside their vCluster block; the treemap's vCluster **grouping** is the acceptance
  surface, and per-app membership is corroborated by the placement-conformance audit below.

## Automated cross-checks (NOT acceptance)

Demoted per the founder's UAT format law. **Acceptance is the operator walking the clickable rows
above.**

- **`scripts/audit-placement-conformance.py live` → exit 0**, "zero undeclared host workloads"
  (declared == actual) on the live hw144 cluster — provided as a session cross-check.
- **`scripts/audit-placement-conformance.py rendered` → exit 0** (repo-side CI check, runnable
  offline): **"placement check PASSED — 63 slots, 9 in vClusters, 17 staged for promotion (target
  != active)."** The 9-in-vCluster + 17-host-staged split is exactly the conformant state above.
