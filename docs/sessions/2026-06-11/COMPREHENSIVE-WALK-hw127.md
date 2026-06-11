# Comprehensive acceptance walk — hw127.omani.works (2-region, SHARED_PG=true)

> Acceptance = the founder (or the agent via the real Stalwart mailbox) clicking these
> steps and seeing the **You should see** screen. Each row is ONE UI action. Routes/labels
> cite real UI code. Automated checks are an appendix, NOT acceptance.
> Fires after hw126 wipe + VPC-free. Validates the fixes merged 2026-06-11: SSO-landing
> (#3272), data-instances panel (#3274), jobs-both-regions (#3278), shared-PG gate (#3275).

## Pre-walk: env is converged + green
| # | Go to | Do | You should see | ☑ |
|---|-------|----|----------------|---|
| 0 | `https://console.hw127.omani.works/sovereign` | load | console 200, treemap renders | ☐ |
| 0b | (kubectl) | `kubectl get hr -A` | bp-* HRs Ready (no KC-wedge chain); `bp-postgres-shared` Ready, `shared-data` ns has pods | ☐ |

## Row 1 — SSO LANDS IN THE APP, not the console (#3272 — the headline fix)
| # | Go to | Do | You should see | ☑ |
|---|-------|----|----------------|---|
| 1 | `https://grafana.hw127.omani.works` | load | Grafana login w/ "Sign in with OpenOva SSO" button | ☐ |
| 2 | same | click the SSO button | redirect through `auth.hw127.omani.works` (KC) → catalyst-pin email screen on console host | ☐ |
| 3 | email screen | type `emrah.baysal@openova.io`, click Send code | "enter code" screen w/ requestId | ☐ |
| 4 | (Stalwart IMAP `mail.openova.io`, secret `stalwart-emrah-real`) | read newest "Your OpenOva sign-in code" | a 6-digit PIN | ☐ |
| 5 | code screen | type the PIN | **lands INSIDE Grafana** (dashboards UI + user avatar) — NOT `console/dashboard` | ☐ |
> Regression guard (#3271): pre-#3272 this landed on `console/dashboard`. The pass condition is the Grafana UI itself.

## Row 2 — Shared-Postgres as a reusable multi-instance card (#3188 / #3274 / ADR-0010)
| # | Go to | Do | You should see | ☑ |
|---|-------|----|----------------|---|
| 6 | `https://console.hw127.omani.works/sovereign` → Catalog → `bp-postgres` (or `bp-cnpg`) | open the blueprint detail | a **Data instances** panel (CatalogDetail.tsx, #3274) — NOT just "depends on bp-cnpg" | ☐ |
| 7 | same panel | read the engine card | the Postgres engine rendered as a first-class instance w/ a live instance count | ☐ |
| 8 | same | read consumers | the consumer apps bound to that one Postgres engine (or honest "bindings not yet surfaced" notice — not faked) | ☐ |
> Pass condition: the operator can SEE "this Postgres has N consumers", the exact thing ADR-0010 says was missing.

## Row 3 — Jobs page shows BOTH regions (#3278 / defect-A)
| # | Go to | Do | You should see | ☑ |
|---|-------|----|----------------|---|
| 9 | console → Jobs (JobsTable.tsx) | load | the jobs table with a **Region** column | ☐ |
| 10 | Jobs table | scan the Region column | rows from BOTH `me-east-215-a` AND `me-east-215-b` (not one region) | ☐ |
> Pass condition: install-* HRs from the secondary region appear, region-labeled.

## Row 4 — Region-kill failover (T2/T3 — north-star row 1)
| # | Go to | Do | You should see | ☑ |
|---|-------|----|----------------|---|
| 11 | (kubectl, region-b) | confirm cnpg replica streaming from primary | replica `Streaming` from the primary cluster | ☐ |
| 12 | (Huawei API / kubectl) | kill region-a (primary) | region-b cnpg promotes; app data survives; recovery ≤ 60s | ☐ |
> Pass condition: a write before the kill is readable after, served from region-b.

## Appendix — automated (NOT acceptance)
- `scripts/verify-sovereign-convergence.sh <id>` — HR convergence + mesh ids + cnpg materialization
- `go test ./internal/jobs ./internal/handler` (defect-A), `DataInstances.test.tsx` (#3274), `auth-gate.test.ts` (#3272)
