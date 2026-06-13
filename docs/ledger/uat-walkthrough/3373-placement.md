# #3373 PLACEMENT — user acceptance walk (100% web UI)

**What the user should be able to do:** when provisioning or viewing an app, choose / see **which vCluster** it lives in — by data, in the UI, with the default needing no thought. **Sign in:** open `https://console.<sovereign>/`.

_Each line = one browser step. `☐`/`✅`/`❌`. No terminal._

### Default provisioning hides the vCluster detail
- [ ] Start provisioning an app (the install/new-instance flow). In the **default** path you are **not** asked about a vCluster — the app just installs. ✅ if the default flow never shows a placement choice.

### Advanced provisioning lets the user choose the vCluster
- [ ] In the install flow, open **Advanced** → look for a **vCluster / region / cluster** placement selector.
- [ ] ❌ **GAP** — there is **no** placement selector anywhere in the web UI. The "Advanced" sections that exist are for **backing services** and **config fields**, not placement. The default-vs-advanced placement screenshots the DoD asks for **cannot be produced** — the feature has no UI.

### An app's page shows which vCluster it lives in
- [ ] Open any app's detail page → look for a **vCluster: <mgmt/dmz/rtz/host>** field.
- [ ] ❌ **GAP** — the app detail page does **not** display the instance's vCluster placement. (Its only "vCluster" text is generic tenant wording.) The user cannot see where an app is placed.

### Re-placing an app and seeing it reflected
- [ ] (If a placement selector existed) change an app's vCluster in the UI → the app page should show the new vCluster.
- [ ] ❌ **GAP** — not walkable: no selector to change it, no field to show it.

### What the user CAN observe (the user-visible outcome of placement)
- [ ] Open an app that is supposed to live inside a vCluster (e.g. a re-homed app) and **open its public URL in the browser** → the app **serves and renders** (proving it reaches the user through the gateway even though it runs inside a vCluster). ✅ if the page loads.
- [ ] ❌ **LIMITATION** — on the open-source vCluster build, route-bearing apps may **not** serve from inside their vCluster (a licensing limitation). If the app's URL fails to load for that reason, record it as the known Pro-gating gap, not a regression.

**Verdict:** #3373 is **largely not user-acceptance-testable today** — the model exists underneath, but there is **no placement UI** (no selector, no display), so a web-UI walk can only confirm that placed apps still *serve*, not that the user can *choose or see* placement. This is the honest gap.
