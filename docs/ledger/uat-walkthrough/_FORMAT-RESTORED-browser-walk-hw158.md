# UAT browser walk — hw158 (FORMAT RESTORED, 2026-06-17)

**This file restores the agreed UAT contract** after the curl/kubectl violation:
**100% browser — no terminal, no kubectl, no git, no curl.** Every step = open a URL → click/type → **see a rendered screen** → screenshot.
A redirect that ends on a login screen is a **FAIL**; only a rendered, signed-in screen is ✅.

**Format (mandated):** 4-column table — **Tested page · Description · Status · Evidence**.
The *Tested page* cell is a **clickable link** to the live page on the current env (`hw158.omani.works`).
*Evidence* = a **screenshot link** under `docs/sessions/2026-06-17/evidence/`.

---

## §0 — Sign-in (zero-click owner-admin landing)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.hw158.omani.works](https://console.hw158.omani.works/auth/handover?token=…) | Open the signed handover URL in a fresh tab → must land **directly on the Dashboard, signed in as the owner-admin**, no login form. | ✅ | Rendered signed-in console: left nav (Dashboard…Settings), **E** avatar (emrah.baysal), env `hw158.omani.works`, "93 items", live resource treemap. [`hw158-uat-01-console-dashboard-signedin.png`](../../sessions/2026-06-17/evidence/hw158-uat-01-console-dashboard-signedin.png) |
| [console.hw158.omani.works/dashboard](https://console.hw158.omani.works/dashboard) | The Dashboard treemap renders live cluster resources (not a blank/error screen). | ✅ | Treemap shows seaweedfs/mimir/cnpg-pair/kyverno/falco/shared-pg with live utilisation %, tooltip "Open application →". Same screenshot above. |

_Method: handover JWT (RS256, mothership owner key) → `…/auth/handover?token=<JWT>` → 302 `/dashboard` → screenshot. This is the agreed "URL → signed-in screen → screenshot" path, witnessed in a real browser._
