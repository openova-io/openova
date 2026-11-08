#!/usr/bin/env bash
# check-region-vocabulary.sh — ADR-0015 D3: one region vocabulary.
#
# Region identity is the cluster region id (`hw-me-east-215-a-rtz-prod`), read
# from ONE registry: core/controllers/pkg/regions (the Sovereign's region list,
# SOVEREIGN_REGIONS_JSON). Every legacy spelling — region letters A/B, bare
# regions (`me-east-215-a`), `meea`, `region-a`, role-derived region suffixes,
# the remote-region kubeconfig pivot, the `local-undelivered` label class —
# may appear ONLY inside the registry.
#
# This lint fails when any of those spellings appears in the controller / API
# code paths ADR-0015 D1/D3 moved onto the registry (CHECKED_FILES below).
# Extend CHECKED_FILES as further paths are migrated; never shrink it.
#
# Usage: scripts/check-region-vocabulary.sh [--self-test]
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

CHECKED_FILES=(
  core/controllers/application/cmd/main.go
  core/controllers/application/internal/controller/application_controller.go
  core/controllers/application/internal/controller/region_delivery.go
  core/controllers/application/internal/render/fanout.go
  core/controllers/application/internal/render/region_files.go
  core/controllers/application/internal/render/topology.go
  core/controllers/organization/cmd/main.go
  core/controllers/organization/internal/controller/per_org_flux.go
  products/catalyst/bootstrap/api/internal/handler/org_console_tls_reconcile.go
)

# Extended regexes, one per forbidden spelling class.
PATTERNS=(
  '"me-east-[0-9]+-[a-z]"'                   # bare region literal
  '"meea"'                                   # abbreviated region
  '"region-[ab]"'                            # letter regions
  'clusterregistry\.Region[AB]\b'            # region-letter constants
  'regionSuffixFromRole'                     # role → region-letter mapping
  'RemoteRegionSecrets?'                     # cross-region kubeconfig pivot
  'local-undelivered'                        # standby applied in the wrong region
  'normalizeRegion\(|normalizePlacementRegion\(' # ad-hoc spelling folders
  'LocalRegion:'                             # A/B local-region config
)

scan() {
  local base="$1"; shift
  local fail=0 f p
  for f in "$@"; do
    [[ -f "${base}/${f}" ]] || { echo "check-region-vocabulary: missing checked file ${f}" >&2; fail=1; continue; }
    for p in "${PATTERNS[@]}"; do
      # Comment lines may describe the retired vocabulary; code may not.
      if grep -nE "${p}" "${base}/${f}" | grep -vE '^[0-9]+:[[:space:]]*//' >/dev/null; then
        grep -nE "${p}" "${base}/${f}" | grep -vE '^[0-9]+:[[:space:]]*//' | sed "s#^#${f}:#" >&2
        fail=1
      fi
    done
  done
  return "${fail}"
}

if [[ "${1:-}" == "--self-test" ]]; then
  tmp="$(mktemp -d)"; trap 'rm -rf "${tmp}"' EXIT
  mkdir -p "${tmp}/x"
  printf 'package x\nvar r = "me-east-215-a"\n' >"${tmp}/x/bad.go"
  printf 'package x\n// retired: "me-east-215-a"\nvar r = "hw-me-east-215-a-rtz-prod"\n' >"${tmp}/x/good.go"
  if scan "${tmp}" x/bad.go 2>/dev/null; then echo "self-test FAIL: bad spelling not caught" >&2; exit 1; fi
  if ! scan "${tmp}" x/good.go; then echo "self-test FAIL: canonical id / comment flagged" >&2; exit 1; fi
  echo "check-region-vocabulary: self-test OK"
  exit 0
fi

if scan "${ROOT}" "${CHECKED_FILES[@]}"; then
  echo "check-region-vocabulary: OK (${#CHECKED_FILES[@]} files, region ids come only from core/controllers/pkg/regions)"
else
  echo "check-region-vocabulary: FAIL — non-canonical region spelling outside the registry (ADR-0015 D3)" >&2
  exit 1
fi
