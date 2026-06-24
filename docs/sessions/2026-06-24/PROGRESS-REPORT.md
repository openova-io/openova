# Progress Report — omantel.biz Sovereign session (2026-06-23 → 06-24)

**Compiled:** 2026-06-24 ~05:45Z, from the live `docs/ledger/UAT.md` + verified live state on dep `4635277cae4ffed9`.

## UAT ledger — headline
**193 ✅ / 25 ❌ / 3 ⛔ / 1 ⚠️ (222 rows) ≈ 87%.** NOT 100%. The two *structural* blockers behind the ceiling are now fixed in code (see Goals) but pending a roll + a hold-verification walk.

## North Star pillars (founder DoD) — pass/fail/pending
| # | Pillar | Status | Evidence / gap |
|---|---|---|---|
| 1 | Marketplace + voucher onboarding | **PASS (with caveat)** | #4179 CLOSED (customer lands signed-in over HTTPS, verified); billing menu + voucher live-validated (#4196/#4198/#4235); agenity 0.9.6 live. Caveat: `.omani.rest` pool TLD has a separate DNS/cert gap (#4188) — `.omani.works` lands clean. |
| 2 | Multi-region BCP topology choice at signup | **FAIL** | UAT rows 50–71 (topology/placement UI) are ❌ — 15 rows. The topology/DR placement surface is the largest remaining ❌ cluster. |
| 3 | Two independent CNPG + region-kill failover | **PARTIAL** | cnpg-pair live + D31 placement fixed + the chronic cnpg-webhook P0 resolved (#4143). Region-kill walk rows fold into the cutover cluster (162–166 ❌). |
| 4 | Sandbox + auto-mounted openova-sandbox-mcp | **PENDING** | Agentic agenity + openova-MCP RBAC validated (demo-user org-scoped, no leak); the Sandbox-MCP-with-full-org-knowledge walk not driven this session. |
| 5 | Sovereign independence post-cutover | **PENDING** | cutover rows 162–166 ❌ (the 8-tether pivot + deny-egress hold not walked green this session). |

## Session goals (work tracker) — pass/fail/pending
| Goal | Status | Artifact |
|---|---|---|
| #1 customer bug (signup→signed-in) | **DONE — #4179 CLOSED** | verified live: discover 200, signed-in-HTTPS screenshot, 6 layers merged |
| agenity 0.9.6 dashboard | **DONE** | live on public host + demo-user RBAC airtight |
| Billing menu + voucher | **DONE** | #4196 (spec) + #4198 (impl, merged) + #4235 (live PASS) |
| Backlog refactor | **DONE (partial)** | 2 audits, 6 evidenced closes (#4079/4206/4224/4226/4228/4215); ~6 new genuine issues filed → net open ~flat |
| Env holds (the 100% gate) | **FIXED, pending roll** | #4250 root-caused + #4252 merged (transient parent-index read no longer prunes sibling orgs) |
| UAT-215 → 100% | **NOT DONE — 193/222** | gated on #4252 roll + a hold-verify, then walk the 25 ❌ on a stable Org |

## The 25 ❌ + 3 ⛔ — categorized honestly
- **topology — 15 ❌** (rows 50–71): the multi-region topology/DR placement UI. Largest cluster; genuine remaining build/walk.
- **cutover — 5 ❌** (rows 162–166): the `bp-self-sovereign-cutover` 8-tether pivot + deny-egress hold.
- **funnel — 4 ❌** (rows 89, 93–95): customer funnel; several likely flip ✅ now that #4179 closed + #4250 fixes the env churn — re-walk needed on a stable Org.
- **model — 1 ❌** (row 23).
- **adoption — 3 ⛔** (rows 206–208, #4002): Crossplane `CloudAdoption` — **ARCHITECTURAL, feature not built** (infra is 100% OpenTofu; 0 Crossplane providers/claims). Not a walk; requires the #4002 build (design-forked A-vs-B). These ⛔ are honest "not built," not test failures.

## What changed this session (≈22 PRs merged)
The #1 bug was ONE architectural divergence (funnel door skipped DNS/TLS/registration the BSS door does) — fully consolidated onto the org-controller/catalyst-api (#4236/#4242/#4251). agenity root-caused + fixed (was genuinely old, not cache). cnpg-webhook P0 + a ~17-min console outage recovered. Per-Org install-gauntlet (keycloak image-proxy, cnpg flux-managed, wordpress hooks, disableWait, cert-issuer) fixed. The chronic env teardown (#4250) root-caused + fixed.

## Path to a real 100%
1. **Roll #4252 + verify the env HOLDS** across concurrent org creation (the precondition — until this, walks don't stay green).
2. **Re-walk the 4 funnel + assorted env-churned ❌ rows** on a stable Org → flip what's genuinely fixed.
3. **Topology (15) + cutover (5)** — the genuine remaining build/walk work; these are real, not noise.
4. **#4002 adoption (3 ⛔)** — architectural; a build, not a walk. Decide build-vs-defer.
5. **#4188** `.omani.rest` pool DNS/cert so every pool TLD lands clean.

**Honest bottom line:** the two structural ceilings (the #1 journey, the env not holding) are fixed in code; the remaining gap is verification + walking a now-stable env, plus the genuine topology/cutover/adoption work — not unknown root causes.
