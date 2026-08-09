#!/usr/bin/env bash
# Prove docs/ledger/uat-raw.csv contains nothing but what git already holds.
#
# The sheet is not a record anyone has to trust. It is a PROJECTION of the commit
# history of docs/ledger/UAT.md. Delete it, rebuild it, and the bytes must be
# identical -- if they are not, something entered the file that did not come from
# git, and the sheet must not be believed until that is explained.
#
# Run it yourself:  bash scripts/uat-verify-reproducible.sh
set -euo pipefail
cd "$(dirname "$0")/.."
RAW=docs/ledger/uat-raw.csv
[ -f "$RAW" ] || { echo "$RAW missing"; exit 1; }

BEFORE=$(sha256sum "$RAW" | cut -d' ' -f1)
cp "$RAW" /tmp/.uat-raw.keep
rm -f "$RAW"
python3 scripts/uat-backfill.py >/dev/null
AFTER=$(sha256sum "$RAW" | cut -d' ' -f1)

echo "committed : $BEFORE"
echo "rebuilt   : $AFTER"
if [ "$BEFORE" = "$AFTER" ]; then
  echo "REPRODUCIBLE — every byte is derived from git history. Nothing injected."
  rm -f /tmp/.uat-raw.keep
else
  echo "NOT REPRODUCIBLE — the committed file contains something git does not." >&2
  cp /tmp/.uat-raw.keep "$RAW"
  echo "committed copy restored; investigate before trusting any number." >&2
  exit 1
fi

# The row count is fixed by construction: cycles x the frozen canon. Any other
# number means rows were appended rather than the file rebuilt.
python3 - <<'PY'
import csv, pathlib, sys
rows = list(csv.DictReader(pathlib.Path("docs/ledger/uat-raw.csv").open(newline="", encoding="utf-8")))
canon = sum(1 for _ in csv.DictReader(pathlib.Path("docs/ledger/uat-testcases.csv").open(newline="", encoding="utf-8")))
cycles = len({r["cycle_ts"] for r in rows})
print(f"cycles={cycles} canon={canon} expected={cycles*canon} actual={len(rows)}")
if len(rows) != cycles * canon:
    sys.exit("ROW COUNT WRONG — the file accumulated instead of being rebuilt")
print("ROW COUNT EXACT — not accumulating.")
PY
