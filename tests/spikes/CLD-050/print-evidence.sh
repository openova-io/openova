#!/usr/bin/env bash
# Spike CLD-050 — print the evidence directory into the job log, so the
# log alone carries every measured value (the artifact holds the same).
EV="${EVIDENCE_DIR:-$PWD/evidence}"
[ -d "$EV" ] || { echo "no evidence directory"; exit 0; }
echo "================ verdicts"
cat "$EV/verdicts.tsv" 2>/dev/null
echo "================ measures"
cat "$EV/measures.tsv" 2>/dev/null
find "$EV" -type f ! -name '*.tsv' | sort | while read -r f; do
  echo
  echo "================ ${f#"$EV"/} ($(wc -c < "$f") bytes)"
  head -c 6000 "$f"
  echo
done
