# UAT evidence gap — what still lacks a live hw302 screenshot

_Generated from `docs/ledger/UAT.md` on hw302 (dep `9b16ad632b906d9b`). **204 of 286 rows now carry a live hw302 in-row screenshot; 82 do not.** This maps each remaining row to WHY it has no screenshot and what would unblock it. Tally: 269 ✅ · 10 ❌ · 1 ⚠️ · 6 ⏳._

Last refresh reflects the reachable-rows walk (agent a2b8c32c79e528da9) that added 16 rows —
`3 53 104 108 115 136 150 151 184 196 197` plus wire rows `R1 M1 G5 R8 R15` — and its two live findings (row 18 adjudication + the `wordpress-db` NetworkPolicy defect, both below).

---

## A. Browser-walkable on hw302 RIGHT NOW — reachable, just not yet walked (22)

These are ✅ rows whose surface is live and reachable in the authed console with only a
**reversible** IaC mutation (Edit → Save → reload → revert) or a read-only wizard walk — the
same method that screenshotted 136/150/151. Not blocked; next walk target.

| Rows | Epic | Walk |
|---|---|---|
| 132 133 134 135 138 139 140 141 143 145 147 148 149 153 154 155 156 158 | catalog Edit-IaC · #3668 | Open a blueprint detail → Edit → change icon/summary/manifest → Save (durable-commit verdict) → reload (persists) → revert. Screenshot each clause's surface. |
| W1 W3 W4 W5 | deployment wizard · #3376/#3969/#5401/#5555 | Walk the deployment wizard read-only: Back control on every step, no fabricated company pre-fill, storefront branding blank, component counts self-consistent. |

## B. Needs a customer session / funnel checkout driven through (20)

Reachable, but require driving the marketplace funnel end-to-end as a **customer** (voucher
issue → redeem → checkout → provision → 2nd Org on a different TLD). Heavier; several also
gated on the live `wordpress-db` defect in §C.

Rows: 74 75 77 81 83 84 85 86 87 88 89 91 92 93 94 216 217 — plus 90 95 226 233 (also §C, WP app 0/1).

## C. Blocked by a real live defect / live-engineering gap (highest value)

| Row(s) | Verdict | Why no live screenshot | What unblocks it |
|---|---|---|---|
| G8 220 222 | ❌ | Per-Org console `console.<org>.hw302` fails TLS (`net::ERR_CERT_COMMON_NAME_INVALID`) + redirectURI **#6509 OPEN** — page won't load, agentic journey can't run | Per-Org cert SAN covering `console.<org>.<pool>` + merge #6509 |
| M4 | ✅→needs-shot | agenity `uatco-agenity` StatefulSet is blocked **at admission** by kyverno `probes-present` (missing liveness/readiness probes) → no pod schedules → the ghcr image-pull path is not exercisable | Add probes to the agenity StatefulSet so it admits + schedules |
| 90 95 226 233 | ✅ | bp-wordpress-tenant deploy is 0/1 on uatco (only the DB runs; WP app never scheduled). **NEW (walker):** `uatco/wordpress-db` per-Org CNPG is stuck `Instance Status Extraction Error: HTTP communication issue`; the tenant ns carries only `default-deny-all`+`allow-same-org` — **no `*-allow-cnpg-operator-probe` NetworkPolicy**, the exact symptom `platform/postgres/chart/templates/networkpolicy-singleton-operator-probe.yaml` fixes. Fix renders in-source (`helm template` confirmed) but is **not reaching the live plan-S tenant namespace**. | Land the singleton-operator-probe NP into the per-Org plan-S tenant ns so CNPG probes pass → WP app serves |
| 186 211 | ✅ | Sovereign MCP route returns 404 at `/`; the authed half needs a freshly minted token | Fix/confirm the MCP route + mint an MCP token, then capture `list_applications` |
| 163 164 | ✅ | Sovereignty cutover has NOT been fired on hw302 (env READY / pre-cutover); no `cutoverComplete` state exists | Fire the 11-step cutover chain (destructive/one-way — needs go) |

## D. Not demonstrable on THIS environment (needs an env change or a destructive action)

| Row(s) | Verdict | Why | What unblocks it |
|---|---|---|---|
| 5 9 10 11 12 20 98 99 102 103 107 238 G7 M3 105 | ✅ | Both hw302 Orgs are **plan-S** (namespace-isolated); no per-Org vCluster exists to show ISOLATION=vcluster / per-vCluster treemap block / two-surface placement match | Provision a plan-M+ Org (vcluster isolation) OR adjudicate the clause for S-plan |
| G12 R17 228 | ✅/⏳ | Requires a destructive action (region-kill / Org-delete / wipe+re-prov) — not run for a screenshot. (G12 was proven e2e on hw292; no hw302 shot.) | Authorize + run the destructive action on hw302 |
| 174 175 | ✅ | hw302 has zero failed job rows, so the Re-run-on-Failed control can't be shown without a vacuous absent-button pass | Inject a genuinely failed job, then screenshot the Re-run control |
| 96 123 | ✅ | Needs a freshly minted signed handover URL to land the owner session for this specific clause | Mint a signed handover URL and walk the redirect |
| 44 | ✅ | Group→`/sovereign-admins`→role mapping lives in the Keycloak admin console, not the tenant `/users` surface | Walk the Keycloak admin console |
| R11 R18 R20 | ✅ | Need a pod-restart (gitea PVC rebind), a handover-key re-publish check, or deeper deploy-bot source investigation — not a single-screenshot surface | Targeted fault-injection / source walk |

## E. Held for adjudication (walker did NOT force a verdict — genuine interpretation question)

| Row | Verdict | Finding |
|---|---|---|
| 18 | ✅ (held) | The dashboard "Resource utilisation treemap" Progress/Kind lens renders **Catalyst job-execution records** carrying `data-job-id`, and **14 match row-18's forbidden patterns** — `cutover-step-*` (×11, dormant since cutover hasn't fired), `task-cutover-gitea-mirror`, `cron-openbao-snapshot-save`, `task-trivy-security-scan`. These are Catalyst **job-progress records** (the #934 Progress/Kind lens deliberately surfaces them), NOT raw k8s **ephemeral Job pods** (which #869 excludes). Whether they violate row-18's "no ephemeral Job-pod cell" is a real interpretation question. Not forced ✅, not flipped ❌. |
| 231 | ✅ (adjudicated) | Already adjudicated ✅ (obsolete-assertion, #4411). Live probe ambiguous — the broken `uatco/wordpress-db` is bp-wordpress's bundled DB, not a standalone bp-postgres Application — so left untouched rather than flipped on shaky grounds. |

---

### The honest ceiling

Browser-walking alone (no env change, no engineering) can still add **§A (22 rows)** and most
of **§B (customer funnel, ~15 rows)** → roughly **240/286** screenshotted. The remaining gap is
**structural**, not effort: plan-S has no vCluster (§D 15 rows), cutover isn't fired (163/164),
the agenity/console cert + #6509 is open (G8/220/222/M4), the MCP route 404s (186/211), and the
per-Org CNPG probe NP isn't reaching plan-S tenants (90/95/226/233). True **286/286 needs the
live engineering** — per-Org cert SAN, #6509, the CNPG-probe NP delivery, the MCP route, and a
plan-M+ Org (or a fired cutover / a controlled destructive walk) — not more walking.
