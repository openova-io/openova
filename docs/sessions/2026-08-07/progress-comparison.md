# UAT progress — measured comparison against previous checks

**Generated 2026-08-07 · env hw292 (`catalyst-api fad88bd`, built 2026-08-02) · 286 data rows throughout**

Every figure below is read from `docs/ledger/UAT.md` **at the commit named**, by
splitting each row and taking the 6th pipe-delimited cell. Nothing is recalled
or carried forward. Whole-line glyph matching over-counts, because passing rows
quote ❌/⛔ inside their evidence prose — that is how W3 (status ✅) was
mistakenly counted as a failure earlier in this session.

## The series

| date | commit | ✅ | ⚠️ | ❌ | ⛔ | ☐ | raw % |
|---|---|---:|---:|---:|---:|---:|---:|
| 2026-08-03 | `35ddf41ca` | 149 | 54 | 19 | 45 | 12 | 52.1% |
| 2026-08-04 | `2141b618b` | 154 | 49 | 19 | 45 | 12 | 53.8% |
| 2026-08-05 | `93b9a411b` | 156 | 47 | 19 | 45 | 12 | 54.5% |
| 2026-08-06 | `06836bec1` | **181** | 40 | 13 | 45 | 0 | **63.3%** |
| 2026-08-07 | `e0412b838` | **181** | 40 | 13 | 45 | 0 | **63.3%** |

## Improved

| movement | 08-03 → 08-07 | reading |
|---|---|---|
| ✅ green | **149 → 181** (+32) | the real gain, concentrated in the 08-05 → 08-06 step |
| ⚠️ partial | 54 → 40 (−14) | partials resolved upward, not reclassified sideways |
| ❌ fail | 19 → 13 (−6) | six failures genuinely cleared |
| ☐ unwalked | 12 → 0 (−12) | the backlog of never-attempted rows is gone |
| raw score | 52.1% → 63.3% (**+11.2 pts**) | |

The ☐ column reaching zero matters more than it looks: every row in the ledger
has now been attempted at least once, so the remaining figure reflects measured
outcomes rather than absence of effort.

## Regressed

**Nothing.** No status moved backwards in the series — no ✅→❌, no ⚠️→❌, and
the ⛔ count is unchanged at 45 throughout.

## Flat — and this is the finding

**08-06 → 08-07 shows zero movement: 181 → 181.** That covers this entire
session. It is not idleness and it is not a stall in the work; it is the
expected result once you know why the rows fail.

`scripts/classify-uat-delivery-state.py --image fad88bd` resolves each failing
row's cited fix against the artifact actually running:

| tier | deploy-gated | code-blocked |
|---|---|---|
| ❌ | **12** | 1 (row 20) |
| ⚠️ | **16** | 24 |

**28 rows are waiting on a roll.** Their fixes are merged; the running
catalyst-api was built 2026-08-02 and predates them. No amount of further fix
work moves those 28 — only delivering the train does. That is why the score is
flat while seven PRs merged today.

## Denominator honesty

The score depends on which rows are excluded, so state it explicitly:

| metric | figure | what it excludes |
|---|---|---|
| raw | **181/286 = 63.3%** | nothing |
| STONE as quoted | 181/239 = 75.7% | all 45 ⛔ + 2 N/A |
| **STONE honest** | **181/250 = 72.4%** | 34 ⛔ + 2 N/A |

The audit in `blocked-row-challenge.md` found that **11 of the 45 ⛔ rows are not
blocked by design** — 9 need a write the founder has already authorised, 1 is
filed against the wrong env, 1 needs re-wording. Returning them to the
denominator costs **3.3 points**. The 75.7% figure I had been quoting was
inflated by rows that were dismissed rather than walked.

## What moves the number next

1. **Roll catalyst-api + the chart pins onto hw292.** Up to 28 rows are eligible
   to re-walk the moment the artifact carries their fixes. This is the single
   highest-yield action and it is a live-cluster write.
2. **Walk the 9 write-gated ⛔ rows** with mutation. Mint the *second* voucher
   first — row 93 records that spending the only existing one destroys the
   fixture rows 75/78 depend on.
3. **The 24 code-blocked ⚠️ rows** are the genuine remaining engineering. They
   are the only group where writing a fix today changes an outcome.

Note the ordering: the largest gain needs no new code at all.
