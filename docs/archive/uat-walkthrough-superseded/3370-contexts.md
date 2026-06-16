# #3370 CONTEXTS — user acceptance walk (web UI)

> **Env: `hw144.omani.works` · deployment `d8e798bdf1b4256b` · 2026-06-15 · single physical kom4dc region (2 VPCs).**
> Fresh hw144 walk. No hw139 / hw128 evidence is carried over — every ✅ below traces to an
> `hw144-*` screenshot under `docs/sessions/2026-06-15/evidence/`.

**North Star #2 (founder, verbatim):** *"3 shared PG instances → 3 cards, 6-7 apps many-to-many."*
**Question Q3:** *where does the Contexts tab appear?* — on a **⛓ shareable** PostgreSQL instance's
app-detail page (one **"Contexts N"** tab per instance). On hw144 the shared-PG model renders in
full: **3 instance cards** (`shared-pg` / `shared-pg-b` / `shared-pg-c`) feeding **11 contexts
across 9 distinct consumer apps** — genuinely many-to-many.

**Sign-in (once):** open the handover URL `https://console.hw144.omani.works/auth/handover?token=<JWT>`
→ lands `/dashboard` signed in as `emrah.baysal@openova.io`, no login form (avatar **E**).

## Walk — every row is one UI action

| Go to (URL) | Then do (click / type) | You should see (screen) | Result |
|---|---|---|---|
| `https://console.hw144.omani.works/apps` | Click the **Catalog** tab, then locate the PostgreSQL card | A card reads **"PostgreSQL · Shareable PostgreSQL data-instance — many apps via isolated Contexts · ⛓ shareable"** | ✅ ([hw144-04](../../sessions/2026-06-15/evidence/hw144-04-postgres-3instances-active-hotstandby.png)) |
| `https://console.hw144.omani.works/catalog/bp-postgres` | Read the hero badges + the **Instances** table | Hero badges **`v0.2.1 · Data · multi-instance · ⛓ shareable · db`**; the Instances table lists **3 rows** — `shared-pg`, `shared-pg-b`, `shared-pg-c` (org `platform`), **all Topology = active-hotstandby, all Ready, Open → each**; **Supported topologies** = single-region (*default*) + active-active; **+ New instance** present | ✅ ([hw144-04](../../sessions/2026-06-15/evidence/hw144-04-postgres-3instances-active-hotstandby.png)) |
| `https://console.hw144.omani.works/app/shared-pg` | Click the **"Contexts 3"** tab | A table headed **`Context · Occupied by · Credential · Status`** with 3 rows: **`db/registry → harbor → harbor-database-secret → ready`**, **`db/gitea → gitea → gitea-database-secret → ready`**, **`db/keycloak → keycloak → keycloak-database-secret → ready`**. Consumer names are live links (blueprint `bp-postgres@0.2.1`) | ✅ ([hw144-06](../../sessions/2026-06-15/evidence/hw144-06-sharedpg-contexts-harbor-gitea-keycloak.png)) |
| `https://console.hw144.omani.works/app/shared-pg-b` | Click the **"Contexts 3"** tab | 3 rows, a fully **distinct** consumer set: **`db/grafana → grafana → grafana-database-env → ready`**, **`db/pdns → powerdns → pdns-database-secret → ready`**, **`db/pda → powerdns-admin → pda-shared-database-secret → ready`** | ✅ ([hw144-07](../../sessions/2026-06-15/evidence/hw144-07-sharedpg-b-contexts-grafana-powerdns.png)) |
| `https://console.hw144.omani.works/app/shared-pg-c` | Click the **"Contexts 5"** tab | 5 rows: **`db/sme_auth → catalyst-platform → sme-database-secret → ready`**, **`db/sme_billing → catalyst-platform → — → Declared`**, **`db/sme_documents → catalyst-platform → — → Declared`**, **`db/newapi → newapi → newapi-database-secret → ready`**, **`db/openova_flow → openova-flow → openova-flow-database-secret → ready`** | ✅ ([hw144-08](../../sessions/2026-06-15/evidence/hw144-08-sharedpg-c-contexts-catalyst-newapi-flow.png)) |

**Result: 5 / 5 ✅** — the 3 shared-PG instance cards + their per-instance Contexts tabs all render
on hw144.

## North Star #2 — the headline answers (hw144)

- **3 shared PG instances → 3 cards.** The `/catalog/bp-postgres` Instances table renders exactly
  three — `shared-pg`, `shared-pg-b`, `shared-pg-c` — each its own card, all `active-hotstandby` +
  Ready (hw144-04).
- **6–7 apps many-to-many.** Across the three instances there are **11 contexts** binding **9
  distinct consumer apps**: `shared-pg` ← harbor / gitea / keycloak; `shared-pg-b` ←
  grafana / powerdns / powerdns-admin; `shared-pg-c` ← catalyst-platform (×3 sme_*) / newapi /
  openova-flow. One engine, many consumers, isolated per-context credentials — the many-to-many
  model the North Star names.
- **Q3 answer:** the **Contexts tab** appears on a **⛓ shareable** instance's app-detail page
  (`/app/shared-pg{,-b,-c}`), one **"Contexts N"** tab per instance, with the
  `Context · Occupied by · Credential · Status` columns.

## Honest notes

- Two `shared-pg-c` rows (`db/sme_billing`, `db/sme_documents`) show **`Declared`** rather than
  `ready`. This is the genuine live reconcile state, **not a defect** — the catalyst-platform SME
  services declare those contexts but their consumer rollout had not finished provisioning at
  capture time. The other **9 contexts are `ready`**, proving the binding completes end-to-end.

## Automated cross-checks (NOT acceptance)

Demoted per the founder's UAT format law. **Acceptance is the operator walking the clickable rows
above.**

- The three instances are bootstrap-HR PostgreSQL `Cluster`s reflected as first-class App cards;
  context count per card: `shared-pg`=3, `shared-pg-b`=3, `shared-pg-c`=5 (11 total).
