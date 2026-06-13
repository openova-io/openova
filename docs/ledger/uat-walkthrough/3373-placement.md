# #3373 PLACEMENT — user acceptance walk (web UI)

**What the user should be able to do:** when provisioning or viewing an app, choose / see **which vCluster** it lives in — by data, in the UI, with the default needing no thought. **Sign in:** `https://console.<sovereign>/`.

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| App install (default flow) | The default path never asks about a vCluster — the app just installs | ☐ | |
| App install → Advanced | ❌ **GAP** — there is **no** vCluster/region/cluster placement selector anywhere in the UI; the "Advanced" sections are backing-services + config only | ☐ | |
| `/app/<any>` detail | ❌ **GAP** — the app page does **not** show which vCluster the app lives in (no placement field) | ☐ | |
| `/app/<any>` (re-place) | ❌ **GAP** — no selector to change placement, no field to reflect a change | ☐ | |
| `https://<app>.<sov>/` | A placed app's public URL **loads and renders** in the browser (it reaches the user through the gateway even though it runs in a vCluster) | ☐ | |
| `https://<app>.<sov>/` | ❌ **LIMITATION** — on the open-source vCluster build, a route-bearing app may **not** serve from inside its vCluster (licensing limit); a failed load here is the known gap, not a regression | ☐ | |

**Verdict:** **largely not user-acceptance-testable today** — there is **no placement UI** (no selector, no display). A web-UI walk can only confirm a placed app still *serves*, not that the user can *choose or see* placement.
