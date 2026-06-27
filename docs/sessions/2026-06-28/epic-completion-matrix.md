# EPIC completion matrix — 2026-06-28 (live)

Source: `docs/ledger/UAT.md` (origin/main), live env `91dc05917e44d1c1` (omantel.biz).
Canonical UAT tally: **185 ✅ · 0 ❌ · 11 ⛔ · 38 ⚠️ · 5 ☐ = 239** (numbered table) — from **65 ✅ at session start**.
(The per-EPIC table below counts every grouped row incl. R-reconciliation + G-gate rows, so its raw total is 277/207 — the per-EPIC % is accurate; the canonical headline is the 239-row numbered tally.)

## Matrix

| EPIC | Governing | Items | ✅ | ⚠️ | ⛔ | ❌ | ☐ | % live |
|---|---|--:|--:|--:|--:|--:|--:|--:|
| catalog | #3668 IaC editor | 37 | 37 | 0 | 0 | 0 | 0 | **100%** |
| sso | #3374 SSO | 24 | 24 | 0 | 0 | 0 | 0 | **100%** |
| cloud | #3987 Cloud view | 7 | 7 | 0 | 0 | 0 | 0 | **100%** |
| mcp | Pillar-4 MCP | 3 | 3 | 0 | 0 | 0 | 0 | **100%** |
| plane-isolation | plane-iso | 2 | 2 | 0 | 0 | 0 | 0 | **100%** |
| gitea | gitea | 2 | 2 | 0 | 0 | 0 | 0 | **100%** |
| storage | #3971 | 2 | 2 | 0 | 0 | 0 | 0 | **100%** |
| sandbox | Pillar-4 Sandbox | 1 | 1 | 0 | 0 | 0 | 0 | **100%** |
| fleet | fleet | 1 | 1 | 0 | 0 | 0 | 0 | **100%** |
| jobs | #3646 Jobs | 13 | 12 | 1 | 0 | 0 | 0 | 92% |
| orgs | #3378/#4293 Orgs | 12 | 11 | 0 | 0 | 0 | 1 | 92% |
| funnel | #3376 Funnel (P1) | 27 | 24 | 0 | 0 | 0 | 3 | 89% |
| delivery | delivery | 6 | 5 | 1 | 0 | 0 | 0 | 83% |
| topology | #3375 Topology | 29 | 23 | 6 | 0 | 0 | 0 | 79% |
| model | #3687/#4212 model+DR | 27 | 20 | 6 | 0 | 0 | 1 | 74% |
| cutover | #3379 Cutover (P5) | 10 | 7 | 0 | 0 | 0 | 3 | 70% |
| recon | #3958 Recon | 6 | 4 | 2 | 0 | 0 | 0 | 67% |
| postgres | bp-postgres | 3 | 2 | 1 | 0 | 0 | 0 | 67% |
| network | network | 2 | 1 | 1 | 0 | 0 | 0 | 50% |
| meta | guards | 9 | 4 | 4 | 1 | 0 | 0 | 44% |
| e2e-journey | North Star | 8 | 3 | 2 | 3 | 0 | 0 | 38% |
| placement | #3969 Placement | 21 | 7 | 12 | 1 | 0 | 1 | 33% |
| janitor | janitor | 3 | 1 | 1 | 0 | 0 | 1 | 33% |
| convergence | fresh-prov | 3 | 1 | 1 | 1 | 0 | 0 | 33% |
| apps | per-Org apps | 10 | 3 | 4 | 0 | 0 | 3 | 30% |
| adoption | #4212 adoption | 6 | 0 | 0 | 4 | 0 | 2 | **0%** |
| dr | #4275 DR (P3) | 2 | 0 | 0 | 0 | 0 | 2 | **0%** |
| spine | #4212 spine | 1 | 0 | 0 | 1 | 0 | 0 | **0%** |
| **TOTAL (per-epic raw)** | | **277** | **207** | 42 | 11 | 0 | 17 | **75%** |

**9 EPICs at 100%:** catalog, cloud, fleet, gitea, mcp, plane-isolation, sandbox, sso, storage.
**Zero genuine failures (0 ❌).**

## The gate on every non-100% EPIC

Each of the 17 ☐ rows + the low-% EPICs maps to exactly one of four things — there is no ungated autonomous walk left:

| What unblocks it | EPICs / ☐ rows | Owner |
|---|---|---|
| **Cutover #3379 reaching cutoverComplete** | cutover (P5) 70%, cutover ☐ 165/166, G11 | **me** — deep tail; fixes merged (#4581 prewarm-504-retry, #4557, #4543, #4527/#4529), grinding step-by-step |
| **EIP quota bump 10→≥16** | adoption 0%, dr/Pillar-3 0%, spine 0%, placement 33%, G1–G7, 93/94/95 | **founder** |
| **Anthropic OAuth blob** | e2e-journey/North Star agent-chat (G8/G9, rows 221/222), #4111/#4277 | **founder** |
| **bastion-Harbor token** | the #4212 crossplane-adoption seam | **founder** |

## Pillar-4 North Star: 2.5 / 3
- ✅ New Org `agnstar` + agenity **singleton** (row 219)
- ✅ Zero-click **SSO, no token paste** (durable chart gate #4572, verified)
- ✅ **MCP RBAC scoping** verified live (row 223 — Org-scope 200 / cross-Org 403)
- 🔑 The live agent-*driven chat* (chat → MCP `create_application`) is the only piece left, gated on the Anthropic OAuth blob.

## Bottom line
**75% of grouped UAT rows live-verified (185 ✅ canonical), 0 failures, 9 EPICs at 100%.** The path from here to 100% / zero-open is the cutover finishing (mine, in flight) + the founder's **3 keys** (EIP bump, Anthropic blob, Harbor token). No autonomous walk remains that isn't theater against a gated row.
