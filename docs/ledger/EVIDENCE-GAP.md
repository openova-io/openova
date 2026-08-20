# UAT evidence gap — what still lacks a live hw302 screenshot

_Generated from `docs/ledger/UAT.md` on hw302 (dep 9b16ad632b906d9b). 98 of 286 rows have no in-row screenshot; this maps each to WHY and what would unblock it. The other 189 rows carry a live hw302 screenshot._

## Blocked by a real live defect or a live-engineering gap (highest value)

| Row | Verdict | Why no live screenshot | What would unblock it |
|---|---|---|---|
| 90 | ✅ | LIVE DEFECT: bp-wordpress-tenant deploy is 0/1 on uatco (only the DB runs; the WP app was never scheduled) | Fix the bp-wordpress-tenant deployment so the purchased app serves |
| 95 | ✅ | LIVE DEFECT: bp-wordpress-tenant deploy is 0/1 on uatco (only the DB runs; the WP app was never scheduled) | Fix the bp-wordpress-tenant deployment so the purchased app serves |
| 226 | ✅ | LIVE DEFECT: bp-wordpress-tenant deploy is 0/1 on uatco (only the DB runs; the WP app was never scheduled) | Fix the bp-wordpress-tenant deployment so the purchased app serves |
| 233 | ✅ | LIVE DEFECT: bp-wordpress-tenant deploy is 0/1 on uatco (only the DB runs; the WP app was never scheduled) | Fix the bp-wordpress-tenant deployment so the purchased app serves |
| G8 | ❌ | Per-Org console `console.<org>.hw302` fails TLS (net::ERR_CERT_COMMON_NAME_INVALID) + redirectURI #6509 OPEN — page won't load, agentic journey can't run | Land a per-Org cert SAN covering `console.<org>.<pool>` + merge #6509 |
| 220 | ❌ | Per-Org console `console.<org>.hw302` fails TLS (net::ERR_CERT_COMMON_NAME_INVALID) + redirectURI #6509 OPEN — page won't load, agentic journey can't run | Land a per-Org cert SAN covering `console.<org>.<pool>` + merge #6509 |
| 222 | ❌ | Per-Org console `console.<org>.hw302` fails TLS (net::ERR_CERT_COMMON_NAME_INVALID) + redirectURI #6509 OPEN — page won't load, agentic journey can't run | Land a per-Org cert SAN covering `console.<org>.<pool>` + merge #6509 |
| 163 | ✅ | Sovereignty cutover has NOT been fired on hw302 (env READY/pre-cutover); no cutoverComplete state exists | Fire the 11-step cutover chain on hw302 (destructive/one-way — needs go) |
| 186 | ✅ | Sovereign MCP route returns 404 at `/`; the authed half needs a freshly minted token | Mint an MCP token + fix/confirm the MCP route, then capture the list_applications call |
| 211 | ✅ | Sovereign MCP route returns 404 at `/`; the authed half needs a freshly minted token | Mint an MCP token + fix/confirm the MCP route, then capture the list_applications call |

## Not demonstrable on THIS environment (needs an env change or a destructive action)

| Row | Verdict | Why no live screenshot | What would unblock it |
|---|---|---|---|
| 5 | ✅ | Both hw302 Orgs are plan-S (namespace-isolated); no per-Org vCluster exists to show ISOLATION=vcluster | Provision a plan-M+ Org (vcluster isolation) OR adjudicate the clause for S-plan |
| 9 | ✅ | Both hw302 Orgs are plan-S (namespace-isolated); no per-Org vCluster exists to show ISOLATION=vcluster | Provision a plan-M+ Org (vcluster isolation) OR adjudicate the clause for S-plan |
| 10 | ✅ | Both hw302 Orgs are plan-S (namespace-isolated); no per-Org vCluster exists to show ISOLATION=vcluster | Provision a plan-M+ Org (vcluster isolation) OR adjudicate the clause for S-plan |
| 11 | ✅ | Both hw302 Orgs are plan-S (namespace-isolated); no per-Org vCluster exists to show ISOLATION=vcluster | Provision a plan-M+ Org (vcluster isolation) OR adjudicate the clause for S-plan |
| 12 | ✅ | Both hw302 Orgs are plan-S (namespace-isolated); no per-Org vCluster exists to show ISOLATION=vcluster | Provision a plan-M+ Org (vcluster isolation) OR adjudicate the clause for S-plan |
| 20 | ✅ | Both hw302 Orgs are plan-S (namespace-isolated); no per-Org vCluster exists to show ISOLATION=vcluster | Provision a plan-M+ Org (vcluster isolation) OR adjudicate the clause for S-plan |
| 98 | ✅ | Both hw302 Orgs are plan-S (namespace-isolated); no per-Org vCluster exists to show ISOLATION=vcluster | Provision a plan-M+ Org (vcluster isolation) OR adjudicate the clause for S-plan |
| 99 | ✅ | Both hw302 Orgs are plan-S (namespace-isolated); no per-Org vCluster exists to show ISOLATION=vcluster | Provision a plan-M+ Org (vcluster isolation) OR adjudicate the clause for S-plan |
| 102 | ✅ | Both hw302 Orgs are plan-S (namespace-isolated); no per-Org vCluster exists to show ISOLATION=vcluster | Provision a plan-M+ Org (vcluster isolation) OR adjudicate the clause for S-plan |
| 103 | ✅ | Both hw302 Orgs are plan-S (namespace-isolated); no per-Org vCluster exists to show ISOLATION=vcluster | Provision a plan-M+ Org (vcluster isolation) OR adjudicate the clause for S-plan |
| 107 | ✅ | Requires a destructive action (region-kill / Org-delete / wipe+re-prov) — not run for a screenshot | Authorize + run the destructive action on hw302 |
| 228 | ⏳ | Requires a destructive action (region-kill / Org-delete / wipe+re-prov) — not run for a screenshot | Authorize + run the destructive action on hw302 |
| G12 | ✅ | Requires a destructive action (region-kill / Org-delete / wipe+re-prov) — not run for a screenshot | Authorize + run the destructive action on hw302 |
| R17 | ✅ | Requires a destructive action (region-kill / Org-delete / wipe+re-prov) — not run for a screenshot | Authorize + run the destructive action on hw302 |
| 174 | ✅ | hw302 has zero failed job rows, so the Re-run-on-Failed control can't be shown without a vacuous absent-button pass | Inject a genuinely failed job, then screenshot the Re-run control |
| 175 | ✅ | hw302 has zero failed job rows, so the Re-run-on-Failed control can't be shown without a vacuous absent-button pass | Inject a genuinely failed job, then screenshot the Re-run control |
| 96 | ✅ | Needs a freshly minted signed handover URL to land the owner session for this specific clause | Mint a signed handover URL and walk the redirect |
| 123 | ✅ | Needs a freshly minted signed handover URL to land the owner session for this specific clause | Mint a signed handover URL and walk the redirect |

## Carried-forward ✅ (reachable UI, live re-walk not yet performed — safe to walk)

These are ✅ rows carried from a prior env; a genuine hw302 re-walk (like the 79 already done) can give each a screenshot. Not blocked — just not yet walked.

Rows: 3 18 44 50 53 59 61 74 75 77 81 83 84 85 86 87 88 89 91 92 93 94 G5 G7 M1 M3 M4 R1 R8 W1 W3 W4 W5 104 105 108 115 132 133 134 135 136 138 139 140 141 143 145 147 148 149 150 151 153 154 155 156 158 164 184 196 197 216 217 231 238 R11 R15 R18 R20
