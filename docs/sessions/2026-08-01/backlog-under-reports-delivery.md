# The backlog systematically under-reports delivered work (2026-08-01)

Three independent audits this session found the same thing, in the same direction: the
board implies work is unstarted when it is in fact merged to `main` and test-covered.
Recording it as a pattern, because it changes how the backlog should be read when
choosing what to work on.

## The three instances

### #5489 — board reads 0/6, five of six tasks shipped
Recorded in `docs/ledger/UAT.md` row 9. Task 6 (an Environments console surface) is the
only genuine gap, and it is **Sovereign-gated rather than unbuilt**: with `environments`
absent as a resource type on the mothership, there is no object for any console to list.

### #5485 — sits at `status/uat`, three of six defects merged with tests
| Defect | Fix | Commit | Test coverage |
|---|---|---|---|
| 1 — reconciler logs matched by prefix (`bp-velero` matched `bp-velero-hcs`) | `logLineMentionsToken` trailing-boundary guard | `a854cb7ae` (PR #5486) | 2 tests with real negative cases |
| 2 — showback listed each Job pod as its own Application row | one-shot Job collapse into `__platform__` | `6844b38f6` | 33 assertions in `org_consumption_test.go` |
| 3 — treemap emitted hash-suffixed ReplicaSet names | `applicationKey(pod, rsByKey)` → `replicaSetOwnerName` | `6844b38f6` | `dashboard_test.go:1085-1182`, table-driven + nil-index case |

Defects 4–6 have an agent PR in flight. Nothing about the issue's state communicates
that half of it is already on `main`.

### #5305 / #5274 — closed and delivered, repeatedly nominated as work to claim
Both `CLOSED` since 2026-07-23, labelled `status/completed`. Marking either
`status/in-progress` would have reopened finished work and misreported the board.

## Why this matters

The correction is always the *opposite* direction from what the board implies: not
"start this", but "this is further along than recorded". A backlog that under-reports
completion is a systematically pessimistic signal for deciding what to do next — it
directs effort at work that is already done, and the effort spent re-verifying is
invisible because it produces no diff.

It is also the same defect class this session has been filing all day, turned inward:
a **declared state that disagrees with the actual state**. #5542 (HTTP 200 declaring
400), #5545 (61 "Deleted" that never happened, confirmed live on the mothership),
#5515 (topology declaring "multi-region" over one live region), and now the board
itself declaring 0/6 over 5/6.

## What to do about it

1. **Check delivery by ancestry before working an issue.** `git log origin/main -- <path>`
   answers "is this already shipped?" in one command; the label does not.
2. **Verify by execution, not by reading.** Defect 1 looked unfixed until the code was
   read; it looked fixed until the tests were *run* — and the first run reported
   `ok ... [no tests to run]` because the filter matched nothing. A passing status line
   is not a passing test.
3. **Record the audit on the row, not just in the issue.** Rows 18/21/22 now carry the
   delivery state of #5485 defects 2/3 so the next walker does not re-derive it.

## Ledger impact — none, deliberately

Rows 18, 21 and 22 carry the #5485 delivery evidence but keep `☐`. Their assertions are
live console walks (treemap cell scan; Showback Application column; Showback
Platform-overhead roll-up) that need a Sovereign. Code being delivered is not the row
being walked, and greening them here would be exactly the declared-vs-actual error the
audit is about. Tally stays 3/281.
