# UAT evidence gap — what still lacks a live hw302 screenshot

_Generated from `docs/ledger/UAT.md` on hw302 (dep `9b16ad632b906d9b`). **226 of 286 rows now carry a live hw302 in-row screenshot; 60 do not.** This maps each remaining row to WHY it has no screenshot and what would unblock it. Tally: 269 ✅ · 10 ❌ · 1 ⚠️ · 6 ⏳._

Two reachable-rows walks are complete: agent `a2b8c32c` (16 rows) and agent `ab6ddf0c`
(**§A, all 22 catalog Edit-IaC 132-158 + wizard W1/W3/W4/W5** — genuine live mutate-then-revert
walks, catalog left as found). What remains is **structural**, not effort: it needs an env
change or a live code fix, not more browser walking.

---

## A. Browser-walkable reachable rows — ✅ COMPLETE (0 remaining)

All 22 (catalog Edit-IaC 132-158 + wizard W1/W3/W4/W5) now carry a live hw302 screenshot.
Nothing left in this bucket.

## B. Needs a customer session / funnel checkout driven through (~15)

Reachable, but require driving the marketplace funnel end-to-end as a **customer** (voucher
issue → redeem → checkout → provision → 2nd Org on a different TLD). Heavier; several also
gated on the live `wordpress-db` defect in §C.

Rows: 74 75 77 81 83 84 85 86 87 88 89 91 92 93 94 216 217 — plus 90 95 226 233 (also §C, WP app 0/1).

## C. Blocked by a real live defect / live-engineering gap (highest value)

| Row(s) | Verdict | Why no live screenshot | What unblocks it |
|---|---|---|---|
| G8 220 222 | ❌ | Per-Org console `console.<org>.hw302` fails TLS (`net::ERR_CERT_COMMON_NAME_INVALID`) + redirectURI **#6509 OPEN** — page won't load, agentic journey can't run | Per-Org cert SAN covering `console.<org>.<pool>` + merge #6509 |
| M4 | ✅→needs-shot | agenity `uatco-agenity` StatefulSet blocked **at admission** by kyverno `probes-present` (missing liveness/readiness probes) → no pod → ghcr image-pull path not exercisable | Add probes to the agenity StatefulSet so it admits + schedules |
| 90 95 226 233 | ✅ | bp-wordpress-tenant deploy 0/1 on uatco (only DB runs). **`uatco/wordpress-db` per-Org CNPG stuck** `Instance Status Extraction Error: HTTP communication issue`; tenant ns carries only `default-deny-all`+`allow-same-org` — **no `*-allow-cnpg-operator-probe` NetworkPolicy** (the exact symptom `platform/postgres/chart/templates/networkpolicy-singleton-operator-probe.yaml` fixes). Renders in-source but **not reaching the live plan-S tenant namespace**. | Deliver the singleton-operator-probe NP into the per-Org plan-S tenant ns → CNPG probes pass → WP app serves |
| 186 211 | ✅ | Sovereign MCP route returns 404 at `/`; the authed half needs a freshly minted token | Fix/confirm the MCP route + mint an MCP token, then capture `list_applications` |
| 163 164 | ✅ | Sovereignty cutover has NOT been fired on hw302 (env READY / pre-cutover); no `cutoverComplete` state exists | Fire the 11-step cutover chain (destructive/one-way — needs go) |

## D. Not demonstrable on THIS environment (needs an env change or a destructive action)

| Row(s) | Verdict | Why | What unblocks it |
|---|---|---|---|
| 5 9 10 11 12 20 98 99 102 103 107 238 G7 M3 105 | ✅ | Both hw302 Orgs are **plan-S** (namespace-isolated); no per-Org vCluster exists to show ISOLATION=vcluster / per-vCluster treemap block / two-surface placement match | Provision a plan-M+ Org (vcluster isolation) OR adjudicate the clause for S-plan |
| G12 R17 228 | ✅/⏳ | Requires a destructive action (region-kill / Org-delete / wipe+re-prov) — not run for a screenshot. (G12 proven e2e on hw292; no hw302 shot.) | Authorize + run the destructive action on hw302 |
| 174 175 | ✅ | hw302 has zero failed job rows, so the Re-run-on-Failed control can't be shown without a vacuous absent-button pass | Inject a genuinely failed job, then screenshot the Re-run control |
| 96 123 | ✅ | Needs a freshly minted signed handover URL to land the owner session for this specific clause | Mint a signed handover URL and walk the redirect |
| 44 | ✅ | Group→`/sovereign-admins`→role mapping lives in the Keycloak admin console, not the tenant `/users` surface | Walk the Keycloak admin console |
| R11 R18 R20 | ✅ | Need a pod-restart (gitea PVC rebind), a handover-key re-publish check, or deeper deploy-bot source investigation — not a single-screenshot surface | Targeted fault-injection / source walk |

## E. Held for adjudication (walker did NOT force a verdict — genuine interpretation question)

| Row | Verdict | Finding |
|---|---|---|
| 18 | ✅ (held) | The dashboard "Resource utilisation treemap" Progress/Kind lens renders **Catalyst job-execution records** carrying `data-job-id`, and **14 match row-18's forbidden patterns** — `cutover-step-*` (×11, dormant), `task-cutover-gitea-mirror`, `cron-openbao-snapshot-save`, `task-trivy-security-scan`. These are Catalyst **job-progress records** (the #934 Progress/Kind lens deliberately surfaces them), NOT raw k8s **ephemeral Job pods** (which #869 excludes). Whether they violate row-18's "no ephemeral Job-pod cell" is a real interpretation question. Not forced. |
| 231 | ✅ (adjudicated) | Already adjudicated ✅ (obsolete-assertion, #4411). Live probe ambiguous — the broken `uatco/wordpress-db` is bp-wordpress's bundled DB, not a standalone bp-postgres Application — so left untouched. |

---

## Walker findings worth carrying (info, NOT verdict changes)

From agent `ab6ddf0c`'s §A walk (all confirmed live, none change a green→red):

- **Row 134 — `/apps` vs `/catalog` icon wiring:** the clause's "grid" is the IaC-first
  **`/catalog` (CatalogPage)** grid, which correctly resolves the edited `iconLight` — PASS.
  But the **`/apps` (AppsPage) tab does NOT** wire the catalog `iconLight` into its cards
  (it shows the slug-mapped bundled icon). Not a row-134 failure, but `/apps` catalog cards
  genuinely don't reflect icon edits — a real UI gap worth a ticket.
- **Row 156 — Gitea repo web-UI quirk:** the catalog IaC lives in Gitea `openova/openova` @
  branch `catalog-sovereign`. The **Gitea web UI cannot render this repo** ("The Git data
  underlying this repository cannot be read" — default branch/HEAD unset), though Flux and the
  console read it via git fine. Row 156's commit was resolved through the console's
  Gitea-backed `/iac` read (`source:"committed"`). Instance quirk, not a commit failure.
- **Row 149 — card-form blank-write (`#5610`):** clearing `icon_light` to empty via the card
  form does **not** persist (the blank-write / partial-patch path drops the empty value); the
  "Clear it" confirm also didn't persist. Revert worked via deleting the `card.iconLight` line
  in the Edit-IaC YamlEditor instead. Worth knowing if a future walker relies on card-form clearing.

---

### The honest ceiling

226/286 rows now carry a live screenshot. The remaining **60 are structural, not effort**:
- **§B (~15):** need a customer funnel session (voucher → checkout → 2nd Org).
- **§C (11):** real defects — agenity cert + #6509 (`G8/220/222/M4`), the per-Org CNPG probe-NP
  not reaching plan-S tenants (`90/95/226/233`), MCP route 404 (`186/211`), cutover not fired (`163/164`).
- **§D (~30):** plan-S has no vCluster, destructive-only, no-failed-jobs, handover-url, keycloak-admin, pod-restart.
- **§E (2):** adjudication.

**True 286/286 needs the live engineering** — per-Org cert SAN + #6509, the CNPG-probe NP
delivery, the MCP route, and a plan-M+ Org (or a fired cutover / a controlled destructive walk)
— plus one customer-funnel session for §B. Not more catalog/wizard walking; that bucket is done.
