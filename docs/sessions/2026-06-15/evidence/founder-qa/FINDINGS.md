# Founder Q&A — live evidence on hw139 (2026-06-15)

Live captures (own headless Playwright, signed-in via `/auth/handover`) backing the
founder's Q1/Q2/Q6 about topology, placement, and the launch buttons. Env:
`hw139.omani.works` (`c89aa7059556b342`), the only live prov.

## Q1 — `shared-pg` shows **singleton** under DR-declaring consumers
Screenshot: [`hw139-Q1-shared-pg-topology-singleton-2026-06-15.png`](hw139-Q1-shared-pg-topology-singleton-2026-06-15.png)

`/app/shared-pg` → Topology tab renders verbatim:
> **Declared topology: singleton** — *shared-pg declares no topology contract in its
> Blueprint — it runs as a single instance with no cross-region failover.*

Topology is read **per-Blueprint** from the matrix and is never reconciled across the
consumer→data chain. `bp-postgres@0.1.10` declares no contract → singleton, while its
consumers (keycloak/gitea/harbor) declare `active-hot-standby`. **Real gap, not cosmetic:**
those apps' DR is hollow at the data layer — their shared backend has no replica/failover.

## Q2 — placement **is** editable (the editor is real)
Same page exposes a **"Change placement — Edit the placement…"** control offering both
regions (`me-east-215-a` + `me-east-215-b`). So the region picker is a genuine affordance,
not a mock. Caveat (not mutated here, per zero-touch): editing placement moves *compute*;
it does not by itself replicate the *data* — so selecting multi-region on a singleton
backend is a declaration the data layer can't honor until `bp-postgres` itself becomes a
`cnpg-pair`.

## Q6 — launch buttons: lost from the **grid**, present on the **detail page**
Screenshot: [`hw139-Q6-openbao-detail-HAS-open-button-2026-06-15.png`](hw139-Q6-openbao-detail-HAS-open-button-2026-06-15.png)

Corrected with live evidence (my first prose answer over-guessed):
- **`/apps` grid** — 52 cards, all plain `/app/<name>` navigation links. **Zero Open/Launch
  buttons, zero external-app-URL anchors.** You cannot launch an app from the list.
- **`/app/bp-openbao` detail** — **HAS** a prominent header button **"↗ Open OpenBao"** +
  anchor `https://bao.hw139.omani.works`. The launch affordance lives here.

So the buttons are **not deleted** — the regression is that **one-click launch was removed
from the apps *list*** and now requires drilling into each app's detail page first. Data
instances (shared-pg) correctly have **no** Open button (no UI). Fix in flight (restore the
per-card Open button on the grid) — `Refs #3374`; live-roll validation deferred (hw139 is
half-pivoted).
