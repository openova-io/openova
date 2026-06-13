# #3373 PLACEMENT — user acceptance walk (web UI)

**What the user should be able to do:** when provisioning or viewing an app, choose / see **which vCluster** it lives in — by data, in the UI. **Sign in:** [console.hw133.omani.works](https://console.hw133.omani.works/).

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [Apps](https://console.hw133.omani.works/apps) | App install (default flow) → never asks about a vCluster; the app just installs | ☐ | |
| [Apps](https://console.hw133.omani.works/apps) | App install → **Advanced** → ❌ **GAP** — no vCluster/region/cluster placement selector anywhere (Advanced is backing-services + config only) | ☐ | |
| [bp-keycloak](https://console.hw133.omani.works/app/bp-keycloak) | ❌ **GAP** — the app page does **not** show which vCluster the app lives in | ☐ | |
| [bp-keycloak](https://console.hw133.omani.works/app/bp-keycloak) | ❌ **GAP** — no selector to change placement, no field to reflect a change | ☐ | |
| [grafana.hw133.omani.works](https://grafana.hw133.omani.works/) | A placed app's public URL **loads and renders** (it reaches the user through the gateway even though it runs in a vCluster) | ☐ | |
| [grafana.hw133.omani.works](https://grafana.hw133.omani.works/) | ❌ **LIMITATION** — on the open-source vCluster build a route-bearing app may **not** serve from inside its vCluster (licensing); a failed load here is the known gap, not a regression | ☐ | |

**Verdict:** **largely not user-acceptance-testable today** — there is **no placement UI** (no selector, no display). A walk can only confirm a placed app still *serves*, not that the user can *choose or see* placement.
