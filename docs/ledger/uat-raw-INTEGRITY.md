# `uat-raw.csv` — integrity statement

What in this dataset is **measured**, what is **absent**, and what is a **modelling
choice**. Read this before trusting any number derived from it.

## Provenance

Every cycle is reconstructed from a real commit: the last commit of each calendar
day that touched `docs/ledger/UAT.md`, read back with `git show <sha>:<path>` and
stamped with that commit's own author date. Days without a commit produce no cycle.

- **57 cycles**, 2026-06-04 → 2026-08-09
- **286 line items per cycle** (fixed), **16,302 rows**
- Regenerate: `python3 scripts/uat-backfill.py`

> _As-of 2026-08-09 (last cycle in this series). The cycle count (57), row total (16,302), the 71.7%-populated figure, and the 65.7% headline score below are point-in-time and drift as new cycles are added — regenerate before quoting them. Only the 286 denominator is fixed._

## Column integrity

| column | source | integrity |
|---|---|---|
| `cycle_ts`, `cycle_date` | commit author date | **measured** |
| `row_id` | UAT.md col 1 | **measured** |
| `epic` | UAT.md col 2 (canon) | **measured** — see caveat 2 |
| `ticket` | UAT.md col 3 (canon) | **measured** — see caveat 2 |
| `test_case` | UAT.md col 4 (canon) | **measured** — see caveat 2 |
| `walk_raw` | UAT.md col 5, verbatim | **measured** |
| `walk_env`, `walk_date` | parsed from `walk_raw` | **derived, deterministic** |
| `status` | UAT.md col 6 glyph | **measured** |
| `status_class` | glyph → PASS/FAIL/… | **derived, deterministic** |
| `dep_id` | — | **ABSENT — always empty for historical cycles** |
| `milestone` | — | **ABSENT — always empty for historical cycles** |

### A defect that was in this file and has been removed

An earlier version carried an `env` column inferred by regex over the **commit
message**. Audited against `walk_raw`, the row's own authoritative record of where
it was walked, it **disagreed 3,539 times against 1,569 agreements** — because a
commit like *"wipe hw225 + fire hw226"* matches the wrong environment and then
stamps it onto all 286 rows of that cycle.

That was a guess presented as measurement. It is deleted. `walk_env` is now parsed
from `walk_raw` and nowhere else; when a row has no walk stamp, `walk_env` is empty.
An unwalked row has no environment, and a blank is more honest than an inference.

## What is genuinely missing — do not fill these in

1. **`dep_id` and `milestone` are empty for all 57 historical cycles.** They were
   never recorded in UAT.md, so there is nothing to recover. Cycles captured from
   now on via `uat-snapshot.py --dep --milestone` will carry them; the past will
   not, and back-filling them from memory would be invention.

2. **`walk_raw` is populated on 71.7% of rows.** The remaining 28.3% are rows that
   carried no walk stamp at that point in history. Empty means *not recorded*, not
   *not walked* — those are different claims and the data cannot distinguish them.

3. **Intra-day changes are lost.** One cycle per day, the day's last commit. A row
   that went ❌ → ✅ → ❌ within a day appears only in its end state.

4. **Days with no commit produce no cycle.** The trend has real gaps
   (2026-06-14, 06-29/30, 07-12→14, 07-21/22, 07-28, 08-01). The work stopped or
   went unrecorded on those days; the series does not interpolate across them.

5. **No evidence text is captured.** UAT.md col 7 holds the walk evidence and is
   deliberately excluded — it is prose, often thousands of characters, and would
   make the sheet unusable while adding nothing a pivot can aggregate. The evidence
   lives in `UAT.md` and in git history.

## Modelling choices (defensible, but choices — not measurements)

1. **`status_class = ABSENT`** marks a test case that did not exist in the ledger at
   that cycle. The ledger genuinely grew from 86 rows to 286. ABSENT is kept
   distinct from NOTRUN on purpose: *"not yet defined"* and *"defined, never walked"*
   are different facts, and merging them would overstate early coverage.

2. **`epic`, `ticket` and `test_case` are taken from the CANON, not from each
   historical row.** If a test case was reworded or re-filed under a different epic
   after a cycle was recorded, this sheet shows its **current** wording for every
   cycle. That keeps a test case's trend on one line instead of fracturing it at
   each rename — but it means the `test_case` text is *today's*, not necessarily
   what the walker read at the time. The historical wording is recoverable from git.

3. **The denominator is fixed at 286** and `SUPERSEDED` counts against the score
   rather than being excluded. This is why the headline reads **65.7%** and not the
   **75.8%** the old floating-denominator STONE reported. The fixed version is the
   one where two cycles are comparable; the floating one moved when rows were
   reclassified, with no test passing.

## Verifying this yourself

```bash
python3 scripts/uat-backfill.py --dry-run   # recompute the trend, write nothing
python3 scripts/uat-pivot.py                # EPIC x STATUS, previous vs latest
python3 scripts/uat-pivot.py --trend        # score per cycle
python3 scripts/uat-pivot.py --moved        # rows that changed, named
```

Any cycle can be checked against its source directly:

```bash
git log --format='%H %ad' --date=short -- docs/ledger/UAT.md | grep <date>
git show <sha>:docs/ledger/UAT.md | grep '^| *84 *|'
```
