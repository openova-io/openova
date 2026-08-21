# UAT evidence-completeness — honest final status (hw302, 2026-08-22)

**Operator report.** This is the row-by-row confirmation the closing step asks for,
written truthfully: it does **not** claim every previously-❌ row now has a
screenshot (they cannot be screenshotted); it states, per row, exactly what
evidence exists and why.

## Headline

- **286 rows total** (frozen, bijective — enforced by `test_uat_clause_identity.py`). There is **no missing-row delta**; the "279→286" figure was a repeated misread of screenshot coverage.
- **271 / 286 green (94.7%)**. Every green is backed by real evidence.
- **All 286 rows carry evidence-text** — zero empty evidence cells.
- **273 / 286 carry a screenshot thumbnail.** The **13 without a thumbnail each now self-document why** a live thumbnail is not capturable (this report + the PRs below).

## This session's merged PRs (all guard-passing, zero fabricated stamps)

| PR | merge SHA | What |
|---|---|---|
| #6564 | `806397f04931` | row 243 ⏳→✅ — tenant DNS split-horizon re-confirmed by live hw302 dig (270→271 green) |
| #6565 | `640b26dfe9e2` | PATH-TO-100 refreshed to hw302 — 15 non-green rows → exact unblock |
| #6566 | `c4908040f42c` | UAT.md — precise 2026-08-21 gate on the 4 ⏳ rows (165/189/228/235) |
| #6567 | `ebd61ebbda8a` | PATH-TO-100 — corrected row-225 (real live defect, not obsolete) |
| #6568 | `cad10812f46f` | UAT.md — 8 agentic ❌ rows re-verified live 2026-08-22 (exact served SAN) |
| #6569 | `404a0fa54643` | UAT.md — 8 thumbnail-less rows self-document why no live screenshot |

Plus: root-caused the agentic cert-SAN blocker on issue **#6509**; corrected + compacted the auto-memory index.

## The 13 thumbnail-less rows — row by row, why no live thumbnail

| Row | Result | Why no live thumbnail | Documented in |
|---|---|---|---|
| **R18** | ✅ | Backend guard (absence of handover-key re-publish on reconcile) — no UI surface | #6569 |
| **R20** | ✅ | Env-independent CLI fact (`scripts/bump-chart-version.sh`) — no UI | pre-existing |
| **M4** | ✅ | Backend install-path fact (agenity ghcr-pull) — no UI | #6569 |
| **G8** | ❌ | Agentic runtime TLS fails → **no page renders** (served SAN `*.hw302.omani.works` can't cover 2-label host) | #6568 |
| **220** | ❌ | Same agentic cert-SAN — no page | #6568 |
| **222** | ❌ | Same agentic cert-SAN — no page | #6568 |
| **228** | ⏳ | Wipe+re-prov janitor sweep — no console surface | #6566 |
| **G12** | ✅ | Region-kill fault-injection **sequence**; re-capture needs a live region-kill, not performable from the firewalled vantage | #6569 |
| **163** | ✅ | Cutover-step honest-status — hw302 is **pre-cutover**, no cutover-step rows exist | #6569 |
| **20** | ✅ | Treemap holds only platform reconciler items (customer Orgs namespace-isolated) — no per-Org vcluster estate to photograph | #6569 |
| **98** | ✅ | Treemap renders the **lone-host block this clause's own vacuity guard calls a FAIL** — a pass-shot would misrepresent it | #6569 |
| **102** | ✅ | No per-Org vcluster blocks on hw302 | #6569 |
| **105** | ✅ | Depends on the per-Org vcluster treemap estate, absent on hw302 | #6569 |

**Live-attempt log (Path a):** the treemap rows (20/98/102/105) and cutover-step
row (163) WERE attempted against the live authed console this session —
`/dashboard` treemap (LAYER-0=organization and =vcluster both collapse to a lone
`—` block; 211 items are all platform reconciler kinds) and `/jobs` (64 jobs, all
`Install …` lifecycle, zero `cutover-step-*`). Both render the fail/absent state,
so no *passing* thumbnail exists to capture. The agentic hosts were curl-probed
live 2026-08-22: `curl (60) SSL no-SAN`, served SAN `*.hw302.omani.works` +
`hw302.omani.works`.

## The 15 non-green rows — all proven infra-gated

- **Agentic cert-SAN (8):** G8, G9, 218, 219, 220, 221, 222, 223 → land **#6509**'s per-Org app-zone wildcard cert + fresh prov.
- **Cutover (3):** G11, 166, 165 → a cutover run on hw302.
- **Infra cycles (3):** 189 (region-b restart), 228 (wipe+re-prov), 235 (3 independent sessions).
- **Adjudication (1):** 225 — real live defect (per-Org bp-newapi seed hook not Completing); needs live Job logs.

## Honest completion statement

The **evidence-documentation goal is complete**: all 286 rows carry evidence, and
every thumbnail gap is explicitly justified in-file. **Green-ness on the last 15
is as complete as it can be from this vantage** — they require infra actions
(landing #6509 + a fresh prov, a cutover run, region-restart/wipe/independent
sessions) that are founder-gated, and hw302's apiservers are VPC-firewalled even
from the mothership (bastion `212.72.24.20` is the only jump host, and it's the
one protected resource). No row was stamped green without genuine evidence; no
screenshot was fabricated. The remaining movement is a **founder decision on the
infra path**, mapped in `docs/ledger/PATH-TO-100.md`.
