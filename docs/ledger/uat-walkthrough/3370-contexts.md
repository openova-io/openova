# #3370 CONTEXTS — user acceptance walk (web UI)

**Sign in:** open [console.hw133.omani.works](https://console.hw133.omani.works/) → land on the **Dashboard** signed-in (no login form).

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [Apps · Catalog](https://console.hw133.omani.works/apps) | Click the **PostgreSQL** card → it shows a **⛓ shareable** badge | ☐ | |
| [Apps · Catalog](https://console.hw133.omani.works/apps) | The **Keycloak** card has **no** shareable badge | ☐ | |
| [Catalog · PostgreSQL](https://console.hw133.omani.works/catalog/bp-postgres) | Hero shows **⛓ shareable · db** | ☐ | |
| [Apps · Deployments](https://console.hw133.omani.works/apps) | Three cards: **shared-pg**, **shared-pg-b**, **shared-pg-c** | ☐ | |
| [Apps · Deployments](https://console.hw133.omani.works/apps) | Each of the three cards shows **⛓ 3 contexts** | ☐ | |
| [Apps · Deployments](https://console.hw133.omani.works/apps) | No extra generic "bp-postgres" card (3 instances = 3 cards) | ☐ | |
| [shared-pg · Contexts](https://console.hw133.omani.works/app/shared-pg) | Table rows: **db/gitea**, **db/registry**, **db/keycloak**, each **ready** | ☐ | |
| [shared-pg · Contexts](https://console.hw133.omani.works/app/shared-pg) | Click the **gitea** consumer link → opens the Gitea app page | ☐ | |
| [bp-gitea · Dependencies](https://console.hw133.omani.works/app/bp-gitea) | A line reads **Depends on: shared-pg / db:gitea** | ☐ | |
| [Catalog · PostgreSQL](https://console.hw133.omani.works/catalog/bp-postgres) | **Supported topologies**: singleton (*default*) + active-hot-standby | ☐ | |
| [Catalog · PostgreSQL](https://console.hw133.omani.works/catalog/bp-postgres) | **Instances** section = exactly 3 rows + a **+ New instance** button | ☐ | |
| [Catalog · PostgreSQL](https://console.hw133.omani.works/catalog/bp-postgres) | ❌ **GAP** — no "Data instances" panel here, but it was never removed from the tenant console (deletion DoD unmet) | ☐ | |
| [Catalog · Harbor](https://console.hw133.omani.works/catalog/bp-harbor) | **+ New instance** → Backing services shows a PostgreSQL selector, **Create new (default)** preselected | ☐ | |
| [Apps · Deployments](https://console.hw133.omani.works/apps) | After "Create new" → **two** new cards: the Harbor instance + its own postgres | ☐ | |
| [Catalog · Harbor](https://console.hw133.omani.works/catalog/bp-harbor) | **Reuse existing** → pick a self-made postgres → Create → only **one** new card | ☐ | |
| [shared-pg · Contexts](https://console.hw133.omani.works/app/shared-pg) | (on the reused instance) A **new row** appeared for the app that just reused it | ☐ | |
| [Catalog · Harbor](https://console.hw133.omani.works/catalog/bp-harbor) | ❌ **GAP** — reusing **shared-pg/-b/-c** is rejected; must use a self-made instance | ☐ | |
| [Catalog · Valkey](https://console.hw133.omani.works/catalog/bp-valkey) | Hero shows **⛓ shareable · keyspace** (not "db") | ☐ | |
| [Apps · Deployments](https://console.hw133.omani.works/apps) | Open a Valkey instance → **Contexts** tab → same table, rows named **keyspace/…** | ☐ | |

**Gaps:** Data-instances panel not deleted from the tenant console; the 3 bootstrap DBs can't be reused from the UI.
