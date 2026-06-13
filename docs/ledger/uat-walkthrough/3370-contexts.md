# #3370 CONTEXTS — user acceptance walk (100% web UI)

**Sign in:** open `https://console.<sovereign>/` → you land on the **Dashboard** signed-in (no login form).
_Each line = one browser step. `☐`/`✅`/`❌`. No terminal — everything is click/type/see._

### Shareable apps are marked in the catalog
- [ ] Sidebar **Apps** → **Catalog** tab → the **PostgreSQL** card shows a **⛓ shareable** badge.
- [ ] Same grid → the **Keycloak** (or **Gitea**) card has **no** shareable badge.
- [ ] Click the **PostgreSQL** card → its detail page hero shows **⛓ shareable · db**.

### The three shared databases each render as one card with their Contexts
- [ ] **Apps** → **Deployments** tab → you see three cards: **shared-pg**, **shared-pg-b**, **shared-pg-c**.
- [ ] Each of those three cards shows a **⛓ 3 contexts** badge.
- [ ] There is **no** separate generic "bp-postgres" card (three instances = three cards, nothing extra).

### Opening a shared database shows who occupies it
- [ ] Click the **shared-pg** card → click the **Contexts** tab.
- [ ] The Contexts table shows three rows — **db/gitea**, **db/registry**, **db/keycloak** — each with a consuming app and a **ready** status.
- [ ] Click the **gitea** consumer link in that table → it opens the **Gitea** app page.

### The consumer shows what it depends on
- [ ] Open the **Gitea** app page → **Dependencies** tab → a line reads **Depends on: shared-pg / db:gitea**.

### Catalog detail: topologies, instances, and the old panel is gone
- [ ] Open **Catalog → PostgreSQL** → a **Supported topologies** section lists **singleton** (marked *default*) + **active-hot-standby**.
- [ ] Same page → an **Instances** section lists **exactly three** rows (shared-pg, -b, -c) with a **+ New instance** button.
- [ ] ❌ **GAP** — the page should have **no** old "Data instances" panel. It is gone here, but the same panel was **never removed** from the separate tenant console (still visible there) — strictly, the deletion DoD is unmet.

### Creating an app: default makes its own DB; reuse adds a Context
- [ ] **Catalog → Harbor → + New instance** → in the dialog, the **Backing services** section shows a PostgreSQL selector with **Create new (default)** preselected.
- [ ] Leave **Create new** → **Create instance** → on **Deployments** you now see **two** new cards: the Harbor instance **and** its own auto-created postgres card.
- [ ] Open **+ New instance** again → choose **Reuse existing** → pick a postgres instance you created earlier → **Create instance** → only **one** new card appears (no second DB).
- [ ] Open that reused postgres instance → **Contexts** tab → a **new row** has appeared for the app that just reused it.
- [ ] ❌ **GAP** — try **Reuse existing** and pick **shared-pg/-b/-c** → it is rejected (the three bootstrap databases can't be reused from the UI); the reuse demo must use a self-created instance.

### Generality: the same Contexts UI works for a non-database (valkey)
- [ ] **Catalog → Valkey** → hero shows **⛓ shareable · keyspace** (not "db").
- [ ] Open a Valkey instance → **Contexts** tab → the **same** table, with rows named **keyspace/…** instead of db.

**Gaps surfaced:** old Data-instances panel not deleted from the tenant console; the 3 bootstrap DBs can't be reused from the UI; (and behind the scenes shared-pg-c only carries the `sme` apps, not newapi/openova-flow — not visible as a UI difference but noted for completeness).
