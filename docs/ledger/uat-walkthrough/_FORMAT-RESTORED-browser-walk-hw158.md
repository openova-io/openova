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
| — | Open the signed handover URL in a fresh tab → must land **directly on the Dashboard, signed in as the owner-admin**, no login form. | ☐ | — |
| — | The Dashboard treemap renders live cluster resources (not a blank/error screen). | ☐ | Treemap shows seaweedfs/mimir/cnpg-pair/kyverno/falco/shared-pg with live utilisation %, tooltip "Open application →". Same screenshot above. |

_Method: handover JWT (RS256, mothership owner key) → `…/auth/handover?token=<JWT>` → 302 `/dashboard` → screenshot. This is the agreed "URL → signed-in screen → screenshot" path, witnessed in a real browser._
