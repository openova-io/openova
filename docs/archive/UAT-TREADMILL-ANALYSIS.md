Measured from `docs/ledger/uat-raw.csv` (57 day-cycles reconstructed from real
commits). Not an interpretation — the reset events are visible as NOTRUN jumps.

## Nine resets in eight weeks destroyed 886 banked green rows

| date | PASS before → after | green wiped |
|---|---|---|
| 2026-06-21 | 124 → 0 | −124 |
| 2026-06-26 | 195 → 87 | −108 |
| 2026-06-28 | 205 → 17 | −188 |
| 2026-07-10 | 154 → 69 | −85 |
| 2026-07-15 | 168 → 1 | −167 |
| 2026-07-20 | 21 → 17 | −4 |
| 2026-07-29 | 181 → 104 | −77 |
| 2026-07-31 | 133 → 0 | −133 |
| **total** | | **−886** |

## The ceiling has not moved since 2026-06-24

Peak PASS reached in each era between resets:

```
124 → 195 → 205 → 205 → 168 → 48 → 184 → 133 → 189
```

Every era spends itself re-earning the same ~190 rows. No era has ever run long
enough to attack the ~86 that have never been green. The reset always lands first.

**2026-08-09 looks like 2026-06-27 because it is 2026-06-27, walked for the ninth
time.** The platform is not regressing and it is not advancing on this metric — it
is being re-measured.

## Cause

`scripts/reset-uat.py` fires on every fresh prov, because UAT evidence is per-env
by law. Each fresh prov therefore costs ~150 rows of re-walking before any new
ground is covered. Nine provs were fired in eight weeks.

The per-env rule is correct — evidence from a wiped environment is not evidence.
The defect is the **cadence**: at ~150 rows of re-walk per prov and a walk rate of
roughly 60 rows/session, a prov every ~5 days consumes the entire walking budget
and leaves nothing for the untouched rows.

## What follows from this

1. **Prov cadence is the lever, not walk throughput.** Walking faster does not
   help while the reset interval is shorter than the time to re-walk 190 rows plus
   attack new ones.
2. **The ~86 never-green rows are the real backlog** and have never been the
   focus of any era. They include the whole `e2e-journey` epic (0%), `mcp` (33%)
   and `placement` (29%).
3. **The next prov must be the last for a while.** Fire once, on a complete train,
   then hold the environment long enough to re-walk 190 AND close the 86 — not
   re-fire at the first defect found.

Verify:
```
python3 scripts/uat-pivot.py --trend
```
