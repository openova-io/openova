#!/usr/bin/env bash
# check-continuum-crd-lockstep.sh — ADR-0015 D8.
#
# The Continuum CRD ships in TWO charts: bp-catalyst-platform (region A, the
# historical owner) and bp-continuum (every region — a follower region runs no
# platform umbrella, yet its Continuum needs the CRD). Both apply crds/ with
# CreateReplace, so if the two copies ever differ, whichever chart upgrades
# last silently replaces the other's schema. They must be byte-identical.
#
# Usage:
#   scripts/check-continuum-crd-lockstep.sh              # compare the tree
#   scripts/check-continuum-crd-lockstep.sh --self-test  # prove both directions
set -euo pipefail

compare() {
  if cmp -s "$1" "$2"; then
    echo "check-continuum-crd-lockstep: OK ($1 == $2)"
    return 0
  fi
  echo "check-continuum-crd-lockstep: FAIL — $1 and $2 differ; edit both (copy one onto the other):"
  diff -u "$1" "$2" | head -40 || true
  return 1
}

if [[ "${1:-}" == "--self-test" ]]; then
  tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
  printf 'kind: CustomResourceDefinition\n' >"$tmp/a"; cp "$tmp/a" "$tmp/b"
  printf 'kind: CustomResourceDefinition\n# drift\n' >"$tmp/c"
  compare "$tmp/a" "$tmp/b" >/dev/null || { echo "self-test FAIL: identical copies rejected"; exit 1; }
  if compare "$tmp/a" "$tmp/c" >/dev/null; then echo "self-test FAIL: drift not caught"; exit 1; fi
  echo "check-continuum-crd-lockstep: self-test OK (identical passes, drift fails; no cluster contacted)"
  exit 0
fi

ROOT="${ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
compare "$ROOT/products/catalyst/chart/crds/continuum.yaml" "$ROOT/products/continuum/chart/crds/continuum.yaml"
