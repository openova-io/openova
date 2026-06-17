# UAT Walkthrough — ORGANIZATIONS: the persona word "tenant"/"SME" is GONE from every user-facing screen, replaced by "Organization" (#3383)

## Status — last validated: hw158 (2026-06-17) — browser walk: **6 ✅ / 1 ❌ / 7 GAP**

> **hw158 browser-walk verdict (2026-06-17, real screenshots).** The Organizations directory (title, cards/columns, nav label), the org-detail view + breadcrumb, the BSS/billing screen, and the legacy `/bss/tenants` alias (→ `/organizations`) all read **"Organization"** with no "tenant"/"SME" persona text (6 ✅). **FAIL (1):** the **create-organization flow** (`/organizations/new`) still leaks persona words — field label **"SME tenant slug"**, submit button **"Onboard tenant"**, plus "the tenant picks…", "The SME owns an apex", "No sme-pool parents available". **Global finding (not a row):** the **bottom-left user widget reads "Tenant"** on every console screen — a residual persona-word leak the rename also needs to land. The 7 backend-only carriers (sme namespace, chart dir, secret, API route, Go symbols, CI guard, the retained `TenantKind="sme"` enum) remain GAP (no browser surface).


> **Prior curl/kubectl/grep format REPLACED.** The earlier version of this runbook walked `kubectl get ns sme`, `git grep -rwl "sme"`, `curl -si …/api/v1/sme/tenants`, `helm template …`, and Go-identifier `grep` counts — all of which are **banned** (curl / kubectl / git / command-output are not user-acceptance-testable). This revamp is **100% browser**: the operator opens each user-facing screen in a browser and READS it, confirming the displayed text says **"Organization"** and the persona terms **"tenant"** / **"SME"** are absent. Backend-only carriers (the `sme` namespace, the `sme-services/` chart dir, the `sme-secrets` Secret, the `/api/v1/sme/tenants` route, the Go handler/store identifiers, the CI naming guard) have **no UI surface** — a code-level rename cannot be acceptance-tested by clicking, so each is recorded as a **`GAP`** finding (not a browser row).

> **Ticket:** #3383 · **Slug:** `organizations-eradicate-sme-tenant-naming`
> **Intent (the only thing a User can SEE):** every user-facing screen reads **"Organization"** — never the persona word **"tenant"** or **"SME"**. The left-nav label, the Organizations directory page (title + cards), the organization-detail view, the BSS/billing menu and its screens, the create-organization flow, and every breadcrumb must all say **"Organization"**. The legacy `/bss/tenants` URL must still resolve and render the **Organizations** surface (the PR #3390 alias must not regress).
> **Env:** the CURRENT converged Sovereign (current = `console.hw158.omani.works`; substitute the live env FQDN when walking).
> **How to read a row:** open the **Tested page** link in a browser. A **rendered Organizations screen with no "tenant"/"SME" persona text = ✅**. Any **"tenant"/"SME"** persona word visible on the screen = **FAIL** (the rename did not land on that surface). A **login-screen redirect** (you are bounced to the Keycloak/SSO login form instead of landing in the app) = **FAIL** (per the agreed standard, a login redirect is never a pass). A surface that exists only in backend code with **no browser screen** = **`GAP`** (a finding — see the GAP table below).

---

## Browser walk — every user-facing screen that says (or must say) "Organization"

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.hw158/organizations](https://console.hw158.omani.works/organizations) | Open the Organizations directory. READ the **page title / heading** — it must say **"Organizations"**, never "Tenants" or "SME Tenants". | ✅ | Heading reads **"Organizations"**; intro "Every organization on this Sovereign … each department and customer is one Organization". No "Tenants"/"SME" in the title. ![3383-organizations-title](../../sessions/2026-06-17/evidence/3383-organizations-title.png) |
| [console.hw158/organizations](https://console.hw158.omani.works/organizations) | On the same directory, READ the **org cards / list rows** — each card's label, column headers, and any subtitle must read **"Organization"** (e.g. "Organization name", "Organization status"), never "tenant"/"SME". | ✅ | Table columns read **ORGANIZATION** / KIND / TIER / BILLING / ISOLATION / STATUS; the parent row reads "hw158.omani.works · PARENT · INTERNAL · Corporate · SHOWBACK · vcluster · ACTIVE" — no "tenant"/"SME" in the cards/columns. ![3383-organizations-cards](../../sessions/2026-06-17/evidence/3383-organizations-cards.png) |
| [console.hw158/organizations](https://console.hw158.omani.works/organizations) | READ the **left-nav sidebar label** for this section — the menu item must say **"Organizations"**, not "Tenants"/"SME". (PR #3390 moved this label; confirm it still reads "Organizations".) | ✅ | Left-nav item reads **"Organizations"**. ⚠️ Finding: the **bottom-left user widget still reads "Tenant"** (over `hw158.omani.works`) on every console screen — a residual persona-word leak (not the nav item itself). ![3383-nav-label](../../sessions/2026-06-17/evidence/3383-nav-label.png) |
| [console.hw158/organizations/new](https://console.hw158.omani.works/organizations/new) | Open the **create-organization flow**. READ the form title, field labels ("Organization name", "Organization slug/domain"), and the submit button — all must say **"Organization"**, never "Create Tenant" / "New SME Tenant". | ❌ | Title "Create organization" is fine, but the form **leaks persona words**: field label **"SME tenant slug"**, help text "the **tenant** picks free-subdomain mode", "The **SME** owns an apex", "No **sme**-pool parents available", and the submit button **"Onboard tenant"**. ![3383-create-org-flow](../../sessions/2026-06-17/evidence/3383-create-org-flow.png) |
| [console.hw158/organizations](https://console.hw158.omani.works/organizations) | Click into an org card to open the **organization-detail view**. READ the detail heading, tab labels, and the **breadcrumb trail** (e.g. `Organizations › <name>`) — all must say **"Organization"**, never "tenant"/"SME". | ✅ | Detail heading "hw158.omani.works · Parent", breadcrumb "← Organizations", fields Slug/Kind/Tier/Billing mode/Isolation/Status/Console — no "tenant"/"SME" in the detail. (⚠️ same global "Tenant" sidebar widget persists.) ![3383-org-detail](../../sessions/2026-06-17/evidence/3383-org-detail.png) |
| [console.hw158/organizations/billing/vouchers](https://console.hw158.omani.works/organizations/billing/vouchers) | Open the **BSS / billing menu** and its vouchers screen. READ the menu entry and the page heading — billing for an org must be framed as **"Organization"** billing, never "Tenant billing" / "SME billing". | ✅ | Billing screen reads "This **organization** is in showback mode … Real billing … when the marketplace sells to external **organizations**" + "Showback — per-app consumption". No "Tenant billing"/"SME billing". ![3383-bss-billing](../../sessions/2026-06-17/evidence/3383-bss-billing.png) |
| [console.hw158/bss/tenants](https://console.hw158.omani.works/bss/tenants) | Open the **legacy `/bss/tenants` URL** (PR #3390 alias). It must resolve and **render the Organizations surface** (client-side route lands on the Organizations directory) — NOT a 404, NOT a login redirect, and the rendered page heading must read **"Organizations"**. Confirms the alias is intact and the destination already uses the canonical term. | ✅ | `/bss/tenants` redirects to `/organizations` and renders the **Organizations** directory (heading "Organizations", org table) — alias intact, no 404, no login redirect. ![3383-bss-tenants-alias](../../sessions/2026-06-17/evidence/3383-bss-tenants-alias.png) |

---

## GAP findings — backend-only carriers with NO user-facing UI surface

These carriers live entirely in cluster/code/CI and **render nothing in the browser**, so they are **not** browser-walkable. A code-level rename here is a developer/CI concern, not a User-acceptance surface — each is recorded as a **`GAP`** (finding). They do not block the browser rows above, but they are the residual work the rename ticket must land in the repo + CI (verified by the existing banned-words CI guard mechanism, not by clicking).

| Carrier (backend-only) | Why it is a `GAP` (no browser surface) | Status |
|---|---|---|
| Kubernetes namespace `sme` → `org-services` | A namespace name is never displayed to a User; it appears only in cluster tooling. No browser screen renders it. | `GAP` |
| Chart template dir `products/catalyst/chart/templates/sme-services/` → `org-services/` | A chart directory path has no rendered UI; it is a build-time artifact only. | `GAP` |
| Secret `sme-secrets` → `org-services-secrets` (+ catalyst-api `CATALYST_SME_JWT_SECRET` env repoint) | A Secret name and an env-var key are never shown to a User; the billing flow that consumes them is what the User sees (covered by the BSS/billing row above). | `GAP` |
| API route `POST /api/v1/sme/tenants` → `POST /api/v1/organizations` (+ one-release deprecation alias) | A raw API path is not a user-facing screen — the User sees the create-organization FORM (browser row above), not the route string. The deprecation-header alias is a wire-level contract, not a clickable surface. | `GAP` |
| Go handler/store identifiers (`HandleCreateSMETenant`, `SMETenantProvisionStore`, …) → `Organization*` | Source-code symbols have no UI; they cannot be observed in a browser. | `GAP` |
| CI naming guard (`scripts/check-no-persona-machinery.sh` + `.github/workflows/naming-guard.yaml`) | A CI workflow/script is a developer-pipeline artifact; it surfaces on a GitHub PR check, not on the Sovereign console — out of scope for a User-acceptance browser walk. | `GAP` |
| Legitimate data-value survivor: `TenantKindSME TenantKind = "sme"` (a `Tier` enum value) | Intentionally retained — an internal enum value, never displayed as a persona label. Not a rename target, and has no UI. | `GAP` |

---

## PASS criteria

**PASS for the user-facing ticket** = every browser row above is ☐ ticked with a screenshot showing the rendered Organizations screen and **no "tenant"/"SME" persona word** on any of: the directory title + cards, the left-nav label, the create-organization flow, the organization-detail view + breadcrumb, and the BSS/billing menu — AND the legacy `/bss/tenants` URL renders the Organizations surface (no regression of PR #3390). Any persona term visible on a screen, a 404, or a login-screen redirect on any row = **FAIL** for that row.

The **`GAP`** rows are findings, not blockers — they record that the residual `sme`/`tenant` rename (namespace, chart dir, secret, route, Go identifiers, CI guard) is a code/CI rename with no browser surface, to be landed + verified through the repo's CI naming-guard mechanism rather than this UAT walk.

---

## Evidence

Capture each screenshot under `docs/sessions/2026-06-17/evidence/` using the `3383-<row-id>.png` names listed in the **Evidence** column above, and link each from `docs/ledger/UAT.md`.

- `3383-organizations-title.png` — directory heading reads "Organizations"
- `3383-organizations-cards.png` — org cards/columns read "Organization"
- `3383-nav-label.png` — left-nav label reads "Organizations"
- `3383-create-org-flow.png` — create flow reads "Organization"
- `3383-org-detail.png` — org-detail heading + breadcrumb read "Organization"
- `3383-bss-billing.png` — BSS/billing menu + screen read "Organization"
- `3383-bss-tenants-alias.png` — legacy `/bss/tenants` renders the Organizations surface
