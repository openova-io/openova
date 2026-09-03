#!/usr/bin/env bash
# check-appversion-bumped-with-source.sh — #6841
#
# A product chart pins its image by `appVersion`. Change the product's SOURCE
# without bumping appVersion and the build re-pushes the SAME mutable tag; every
# node with that tag cached keeps running the OLD binary under `IfNotPresent`.
# The chart version bump is not enough — it delivers new VALUES to an old image.
#
# Measured on hw307 (#6841): chart went 0.1.3 -> 0.1.4 carrying a new env var,
# appVersion stayed 0.1.2, and the pods kept running chargeback:0.1.2 from a
# 20-hour-old layer. TRUSTED_FORWARD_AUTH_HEADER was set correctly and read by
# a binary that had never heard of it, so SSO silently did nothing.
#
# This gate fails when a product's Go/UI source changed in the diff but its
# chart appVersion did not.
set -euo pipefail

base="${1:-origin/main}"
head="${2:-HEAD}"
fail=0
checked=0

for chart in products/*/chart/Chart.yaml; do
  prod=$(dirname "$(dirname "$chart")")
  name=$(basename "$prod")
  # only products that actually build an image (have a Containerfile)
  [[ -f "$prod/Containerfile" ]] || continue
  grep -qE '^appVersion:' "$chart" || continue
  checked=$((checked+1))

  # did source (not chart/, not docs) change?
  src=$(git diff --name-only "$base" "$head" -- "$prod" 2>/dev/null \
        | grep -vE "^$prod/(chart|README|docs)/" | grep -E '\.(go|ts|tsx|js|jsx|mjs|svelte|sql|mod|sum)$|Containerfile' || true)
  [[ -z "$src" ]] && { echo "  ok: $name — no image-affecting source change"; continue; }

  before=$(git show "$base:$chart" 2>/dev/null | grep -E '^appVersion:' | head -1 | sed -E 's/appVersion:[[:space:]]*"?([^"]+)"?/\1/' || true)
  # read BOTH sides from their commits — reading `after` from the working tree
  # makes the gate pass on a commit that never bumped it (caught in review).
  after=$(git show "$head:$chart" 2>/dev/null | grep -E '^appVersion:' | head -1 | sed -E 's/appVersion:[[:space:]]*"?([^"]+)"?/\1/' || true)
  [[ -n "$after" ]] || after=$(grep -E '^appVersion:' "$chart" | head -1 | sed -E 's/appVersion:[[:space:]]*"?([^"]+)"?/\1/')

  if [[ -z "$before" ]]; then
    echo "  ok: $name — new chart (no baseline appVersion)"
  elif [[ "$before" == "$after" ]]; then
    echo "FAIL: $name changed image-affecting source but appVersion stayed $after."
    echo "      The build re-pushes the SAME tag, so nodes with it cached keep the OLD"
    echo "      binary under IfNotPresent — new chart values land on an old image (#6841)."
    echo "      changed: $(echo "$src" | head -3 | tr '\n' ' ')"
    fail=1
  else
    echo "  ok: $name — appVersion $before -> $after alongside source changes"
  fi
done

if [[ $checked -eq 0 ]]; then
  echo "FAIL: no image-building product charts were examined — the gate would pass vacuously"
  exit 1
fi
if [[ $fail -ne 0 ]]; then
  echo "FAILED — a source change would not reach a cluster."
  exit 1
fi
echo "PASS — every product whose source changed also bumped its appVersion ($checked chart(s) examined)."
