# #3370 CONTEXTS — user acceptance walk (web UI)

**Sign in:** open `https://console.<sovereign>/` → land on the **Dashboard** signed-in (no login form).

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| `/apps` → Catalog | Click the **PostgreSQL** card → it shows a **⛓ shareable** badge | ☐ | |
| `/apps` → Catalog | The **Keycloak** card has **no** shareable badge | ☐ | |
| `/catalog/bp-postgres` | Hero shows **⛓ shareable · db** | ☐ | |
| `/apps` → Deployments | Three cards: **shared-pg**, **shared-pg-b**, **shared-pg-c** | ☐ | |
| `/apps` → Deployments | Each of the three cards shows **⛓ 3 contexts** | ☐ | |
| `/apps` → Deployments | No extra generic "bp-postgres" card (3 instances = 3 cards) | ☐ | |
| `/app/shared-pg` → Contexts | Table rows: **db/gitea**, **db/registry**, **db/keycloak**, each **ready** | ☐ | |
| `/app/shared-pg` → Contexts | Click the **gitea** consumer link → opens the Gitea app page | ☐ | |
| `/app/bp-gitea` → Dependencies | A line reads **Depends on: shared-pg / db:gitea** | ☐ | |
| `/catalog/bp-postgres` | **Supported topologies**: singleton (*default*) + active-hot-standby | ☐ | |
| `/catalog/bp-postgres` | **Instances** section = exactly 3 rows + a **+ New instance** button | ☐ | |
| `/catalog/bp-postgres` | ❌ **GAP** — no "Data instances" panel here, but it was never removed from the tenant console (deletion DoD unmet) | ☐ | |
| `/catalog/bp-harbor` → New instance | Backing services shows a PostgreSQL selector, **Create new (default)** preselected | ☐ | |
| `/apps` → Deployments | After "Create new" → **two** new cards: the Harbor instance + its own postgres | ☐ | |
| `/catalog/bp-harbor` → New instance | Choose **Reuse existing** → pick a self-made postgres → Create → only **one** new card | ☐ | |
| `/app/<reused-pg>` → Contexts | A **new row** appeared for the app that just reused it | ☐ | |
| `/catalog/bp-harbor` → New instance | ❌ **GAP** — reusing **shared-pg/-b/-c** is rejected; must use a self-made instance | ☐ | |
| `/catalog/bp-valkey` | Hero shows **⛓ shareable · keyspace** (not "db") | ☐ | |
| `/app/<valkey>` → Contexts | Same table, rows named **keyspace/…** | ☐ | |

**Gaps:** Data-instances panel not deleted from the tenant console; the 3 bootstrap DBs can't be reused from the UI.
