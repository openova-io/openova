# hw291 status/uat queue — classification + next actions (2026-07-31)

Env: hw291 (dep 2c2d746b578c636b, 2-region me-east-215-a/-b-1), post-cutover cc=true 2026-07-30T11:03:43Z.
This consolidates ~25 live walks done 2026-07-30/31 into a decision map, so the queue is acted on by
category (one platform action clears many) rather than issue-by-issue.

## The four founder root-directives — status (committed, verifiable)

| Directive | State | Evidence |
|---|---|---|
| Model regression (Opus→Fable) | **CLOSED** | No local Opus pin exists; `settings.json:17 "model": "fable"` governs; 4/4 session sub-agents = 100% `claude-fable-5` at the wire (583 reqs). Only local opus refs are in 2 DISABLED plugins. RCA: `f60b6fed7`, `86df63179` |
| pct-matrix theater | **CLOSED** | Artifact-per-cell + no-mid-conversation-rederivation rules committed. RCA: `f60b6fed7` |
| EPIC convergence RCA | **CLOSED** | #3969/#4212 child-commit-active (23 EPIC commits/30d), live kubectl sweep 395 pods 0 non-Running. RCA: `182f589f5` |
| §854 NodePort | **ENFORCED** (3 layers) | static (337 svc 0 NodePort) + policy (Enforce, 674 policyreport PASS) + admission (webhook failurePolicy=Fail, fails closed). Row 240: `9ea3665d0`/`349f9e06d`/`610db0169`. Guard fail-open (#5517) reproduced, fix PR #5518 open on #5386 |

## Remaining status/uat — classified (the actionable part)

**Bucket 1 — DELIVERY-GATED (clear on a #5265 day-2 delivery run OR next fresh prov).** catalyst-api still image 8367b62 ~14h+ after these merged:
- #5524 gateway multi-region → #5511 (region-b listener decay, re-healed 5x)
- #5526 vcluster fabrication → #5489 / row 9 (uatcorp shows VCLUSTER:Ready)
- #5529 cutover-aware generator → #5527 (org-tenants emits ghcr)
- #5519 derivePattern exhaustive → #5515 / row 60 (2-region app renders singleton)
- #5507 postgres initdb owner → #5504
→ **ACTION: prioritize #5265 (day-2 chart-sync) or schedule a fresh prov on the current train. Clears all 5 at once.**

**Bucket 2 — ENV-LIMITED (need a vcluster-tier / bigger-plan Org; uatcorp is namespace-tier plan=s).**
- #5453 (backing-services matcher hardcodes 'apps' ns — no 'apps' ns exists, pods org-scoped)
- #5451 (per-Org dead apps badged INSTALLED — apps pruned, none present)
- #5400 (per-Org bp-keycloak clobbers pin-broker cred — bp-keycloak pruned, cred intact 39h)
- #5232/233/234, #89/90 (per-Org app serving — bundle pruned by #5393 quota)
→ **ACTION: a fresh prov with a vcluster-tier Org (or resolve #5393 plan-quota so s-plan fits its bundle).**

**Bucket 3 — FIX DELIVERED, needs fault-injection to close.**
- #4901 (Continuum standby-absent surfacing) — status now carries hotStandbyAbsent + StandbyAvailable condition
- #5477 (fabricated lag=0) — lag=0 is measured (StandbyReachable), distinguishing signal present
→ **ACTION: region-kill / standby-down fault-injection confirms the condition flips, then close. (Also the G12 DR re-proof.)**

**Bucket 4 — DEDUP.**
- #5341 (MCP ~40% envoy 404) == #5394 (openova-mcp single-region behind 2-region VIP; region-b 0 MCP pods). Same root. Close one as dup.

**Bucket 5 — GENUINELY-LIVE product defects (real, on the deployed image, not delivery-gated).**
- #5531 (gitea SSO callback 500) — CONFIRMED product defect via control method (2/2 repro; harbor/grafana/openbao/pdns/hubble all PASS same walk → gitea-specific). Root: gitea OAuth-state cookie SameSite/domain not surviving the keycloak round-trip.
- #5532 (keycloak/users API empty) — owner exists (row-43 JWT realm_access proves it); console proxy queries wrong realm or over-filters. Fix target: realm param + filter in the users proxy handler.

## The three moves that clear the most

1. **#5265 delivery run (or fresh prov)** → clears Bucket 1 (5 merged fixes + their live symptoms).
2. **Fresh prov with a vcluster-tier Org** → clears Bucket 2 + enables Bucket 3 fault-injection.
3. **Fix the two live defects** (#5531 gitea cookie, #5532 users-proxy realm) — the only genuinely-live product bugs.

Founder-gated: #5393 (plan-quota semantics), #4277 (Anthropic cred for Agentic pillar), #5518-vs-#5386 (guard-vacuity vs founder's NodePort-fixtures ordering — gates #5517).

Ledger: 148/281 green (from a post-flush 137 this session). Defects filed today: #5530 (cutover Job-pod GC), #5531, #5532.
