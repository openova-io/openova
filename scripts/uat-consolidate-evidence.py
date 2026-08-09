#!/usr/bin/env python3
"""Merge the per-batch evidence audits into two ledger files.

Six read-only auditors each took a slice of the still-green test cases and
answered two questions per row: is the verdict backed by an artifact that shows
the surface the clause names, and WHICH surface would have to change for the
pass to stop being valid.

    docs/ledger/uat-surfaces.csv        row_id -> surface_path
        Feeds trigger 2 of uat-retest-policy.py. Without this file no pass can
        ever expire on a code change, which is why the policy prints a warning
        when it is missing rather than presenting its number as a ceiling.

    docs/ledger/uat-evidence-audit.csv  the full per-row verdict
        BACKED / UNBACKED / ARTIFACT-MISSING / LAYER-MISMATCH, with the reason.

Run:  python3 scripts/uat-consolidate-evidence.py <scratchpad-dir>
"""
import csv
import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
OUT_SURF = ROOT / "docs" / "ledger" / "uat-surfaces.csv"
OUT_AUD = ROOT / "docs" / "ledger" / "uat-evidence-audit.csv"
FIELDS = ["row_id", "verdict", "artifact_path", "artifact_exists",
          "layer_match", "surface_path", "reason"]


def main():
    src = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else ".")
    batches = sorted(src.glob("batch-*.csv"))
    if not batches:
        sys.exit(f"no batch-*.csv under {src}")

    rows, seen = [], {}
    for b in batches:
        n = 0
        for r in csv.DictReader(b.open(newline="", encoding="utf-8")):
            rid = (r.get("row_id") or "").strip()
            if not rid:
                continue
            # A test case belongs to exactly one batch. A duplicate means two
            # auditors graded the same row, so keep the first and say so rather
            # than letting one silently overwrite the other.
            if rid in seen:
                print(f"  DUPLICATE {rid}: in {seen[rid]} and {b.name} — keeping the first")
                continue
            seen[rid] = b.name
            rows.append({k: (r.get(k) or "").strip() for k in FIELDS})
            n += 1
        print(f"  {b.name:<24} {n:>3} rows")

    rows.sort(key=lambda r: (len(r["row_id"]), r["row_id"]))

    with OUT_AUD.open("w", newline="", encoding="utf-8") as fh:
        w = csv.DictWriter(fh, fieldnames=FIELDS)
        w.writeheader()
        w.writerows(rows)

    surf = [r for r in rows if r["surface_path"] and r["surface_path"].upper() != "UNKNOWN"]
    with OUT_SURF.open("w", newline="", encoding="utf-8") as fh:
        w = csv.writer(fh)
        w.writerow(["row_id", "surface_path"])
        w.writerows([[r["row_id"], r["surface_path"]] for r in surf])

    tally = {}
    for r in rows:
        tally[r["verdict"]] = tally.get(r["verdict"], 0) + 1
    print(f"\n{len(rows)} test cases audited")
    for k, v in sorted(tally.items(), key=lambda kv: -kv[1]):
        print(f"  {k:<18} {v:>3}")
    print(f"\nsurface attributed: {len(surf)}/{len(rows)}")
    print(f"-> {OUT_AUD.relative_to(ROOT)}\n-> {OUT_SURF.relative_to(ROOT)}")


if __name__ == "__main__":
    main()
