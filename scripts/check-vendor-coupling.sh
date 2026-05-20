#!/usr/bin/env bash
# check-vendor-coupling.sh — CI guardrail enforcing the founder's
# 2026-05-01 vendor-agnostic rule (canonical-seam map at
# docs/archive/omantel-handover-wbs.md §3a, principle in
# docs/PRINCIPLES.md #4).
#
# What this does:
#   Scans platform/, clusters/, and products/catalyst/bootstrap/{api,ui}/
#   for vendor-name leakage in places where a capability name belongs.
#   The "vendor" set is hetzner|aws|gcp|azure|oci. The forbidden patterns:
#
#     1. "<vendor>-object-storage"
#        — sealed-secret / config-map / overlay-secret name leaking the
#          vendor. Capabilities should be named after their function
#          (e.g. "object-storage-credentials"), not their provider.
#
#     2. "<chart>Overlay\.<vendor>\."
#        — chart values block keyed to a specific vendor (e.g.
#          veleroOverlay.hetzner.s3.accessKey). The overlay must key on
#          the capability (veleroOverlay.objectStorage.s3.accessKey) and
#          the per-vendor selection happens at the values-rendering layer.
#
#     3. "<vendor>ObjectStorage" (camelCase payload field)
#        — Crossplane Composition forProvider key, Sovereign-API field,
#          or wizard payload field that hardcodes the vendor.
#
# Allowed (excluded) paths — these are LEGITIMATELY per-provider:
#
#     - infra/<provider>/                    Tofu modules
#     - internal/<provider>/                 Provider-specific Go impls
#     - internal/objectstorage/<provider>/   Post-#425 provider impls
#     - core/pkg/<provider>/                 Provider Go libs (if present)
#     - any line containing "crossplane-contrib/provider-"
#                                            Crossplane Provider CR refs
#     - any *.md file                        Docs may discuss the rule
#     - .git/                                version-control internals
#
# Warn vs hard-fail mode:
#
#   The vendor-rename work is happening across multiple PRs. While #425
#   (the rename) is in flight, this script runs in WARN-ONLY mode so
#   the work-in-progress drift is visible without blocking unrelated
#   merges. Once #425 lands, the script flips to HARD-FAIL mode and any
#   future re-introduction of vendor coupling fails CI.
#
#   The mode gate is the existence of the internal/objectstorage/
#   directory. Pre-#425 it does not exist → warnings only. Post-#425
#   it exists → hard-fail.
#
# Exit codes:
#   0  — clean tree (or warn-only mode with warnings emitted to stderr)
#   1  — hard-fail mode AND violations found
#   2  — usage / I/O error
#
# Usage:
#   scripts/check-vendor-coupling.sh
#   scripts/check-vendor-coupling.sh --root /path/to/repo
#   scripts/check-vendor-coupling.sh --force-hard-fail  (testing)

set -euo pipefail

# ---------------------------------------------------------------------------
# Args
# ---------------------------------------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
FORCE_HARD_FAIL=0

usage() {
  cat <<EOF
Usage: $(basename "$0") [--root DIR] [--force-hard-fail]

  --root DIR          Repo root to scan (default: ${REPO_ROOT})
  --force-hard-fail   Override mode gate, treat warnings as errors
                      (used by self-tests; CI never sets this)
  -h, --help          Show this message

See docs/archive/omantel-handover-wbs.md §3a (canonical-seam map) and
docs/PRINCIPLES.md #4 (no hardcoding) for the rule rationale.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --root)
      REPO_ROOT="$(cd "$2" && pwd)"
      shift 2
      ;;
    --force-hard-fail)
      FORCE_HARD_FAIL=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "ERROR: unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ ! -d "${REPO_ROOT}" ]]; then
  echo "ERROR: repo root does not exist: ${REPO_ROOT}" >&2
  exit 2
fi

# ---------------------------------------------------------------------------
# Mode gate
# ---------------------------------------------------------------------------

# The vendor-agnostic rename (#425) lands the canonical seam at
# internal/objectstorage/<provider>/. Once that directory exists, every
# remaining vendor-coupled reference is either drift or net-new — both
# must hard-fail. Until then, we can only warn (the drift IS the WIP).
HARD_FAIL=0
if [[ -d "${REPO_ROOT}/products/catalyst/bootstrap/api/internal/objectstorage" ]]; then
  HARD_FAIL=1
fi
if [[ "${FORCE_HARD_FAIL}" -eq 1 ]]; then
  HARD_FAIL=1
fi

# ---------------------------------------------------------------------------
# Patterns + scoped paths
# ---------------------------------------------------------------------------

# Vendors whose names should never leak into capability-named slots.
VENDORS=(hetzner aws gcp azure oci)

# Build an alternation regex for grep -E.
VENDOR_ALT="$(IFS='|'; echo "${VENDORS[*]}")"

# The forbidden patterns. grep -E syntax. Each is documented above.
PATTERNS=(
  "(${VENDOR_ALT})-object-storage"
  "[a-zA-Z0-9_]+Overlay\.(${VENDOR_ALT})\."
  "(${VENDOR_ALT})ObjectStorage"
)

# Paths to scan. Vendor coupling is forbidden anywhere in these trees;
# legitimately per-vendor code lives elsewhere (see SCAN_EXCLUDES below).
SCAN_PATHS=(
  "platform"
  "clusters"
  "products/catalyst/bootstrap/api"
  "products/catalyst/bootstrap/ui"
)

# Paths excluded from the scan because they ARE legitimately per-provider.
# Matched against the file path relative to REPO_ROOT.
SCAN_EXCLUDES_REGEX='^(infra/[^/]+/|internal/objectstorage/[^/]+/|internal/[^/]+/|core/pkg/[^/]+/|\.git/)'

# Per-line content excludes — lines matching these are NEVER violations
# even inside scanned paths. (e.g. Crossplane Provider CR references that
# legitimately name `provider-hcloud`.)
LINE_EXCLUDES_REGEX='crossplane-contrib/provider-'

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

_banner() {
  if [[ -t 1 ]]; then
    printf '\033[1;36m== %s ==\033[0m\n' "$*"
  else
    printf '== %s ==\n' "$*"
  fi
}

_warn() {
  if [[ -t 2 ]]; then
    printf '\033[1;33mWARN:\033[0m %s\n' "$*" >&2
  else
    printf 'WARN: %s\n' "$*" >&2
  fi
}

_err() {
  if [[ -t 2 ]]; then
    printf '\033[1;31mFAIL:\033[0m %s\n' "$*" >&2
  else
    printf 'FAIL: %s\n' "$*" >&2
  fi
}

_ok() {
  if [[ -t 1 ]]; then
    printf '\033[1;32mOK:\033[0m %s\n' "$*"
  else
    printf 'OK: %s\n' "$*"
  fi
}

# ---------------------------------------------------------------------------
# Scan
# ---------------------------------------------------------------------------

cd "${REPO_ROOT}"

if [[ "${HARD_FAIL}" -eq 1 ]]; then
  _banner "vendor-coupling check (HARD-FAIL mode — internal/objectstorage/ present)"
else
  _banner "vendor-coupling check (WARN-ONLY mode — pre-#425, internal/objectstorage/ absent)"
fi

# Build a single grep -E regex that ORs all patterns together. We pipe
# through filter passes (path exclude, content exclude, doc exclude).
JOINED_PATTERN="$(IFS='|'; echo "${PATTERNS[*]}")"

# Collect existing scan paths (some may not exist in a given checkout).
EXISTING_PATHS=()
for p in "${SCAN_PATHS[@]}"; do
  if [[ -d "$p" ]]; then
    EXISTING_PATHS+=("$p")
  fi
done

if [[ ${#EXISTING_PATHS[@]} -eq 0 ]]; then
  _warn "none of the scan paths exist in this tree (${SCAN_PATHS[*]}) — nothing to check"
  exit 0
fi

# grep flags:
#   -r  recursive
#   -n  line numbers
#   -E  extended regex (alternation)
#   -I  skip binary files
#   --include / --exclude-dir for performance + scope
#
# We exclude *.md at the grep layer (docs may discuss the rule itself).
# We can't easily exclude infra/ or internal/ at the grep layer because
# they're not under the scan paths anyway — keeping the path-exclude
# regex below is defensive (if someone broadens SCAN_PATHS later the
# exclusions still apply).
RAW_HITS="$(
  grep -rnEI \
    --exclude='*.md' \
    --exclude-dir='node_modules' \
    --exclude-dir='.git' \
    --exclude-dir='dist' \
    --exclude-dir='build' \
    --exclude-dir='.svelte-kit' \
    "${JOINED_PATTERN}" \
    "${EXISTING_PATHS[@]}" \
    2>/dev/null \
    || true
)"

# Filter pass — drop excluded paths and excluded lines.
FILTERED_HITS=""
if [[ -n "${RAW_HITS}" ]]; then
  while IFS= read -r line; do
    [[ -z "$line" ]] && continue
    # path is everything before the first ':<digits>:' line-number sep.
    path="${line%%:*}"
    if [[ "$path" =~ ${SCAN_EXCLUDES_REGEX} ]]; then
      continue
    fi
    if [[ "$line" =~ ${LINE_EXCLUDES_REGEX} ]]; then
      continue
    fi
    FILTERED_HITS+="${line}"$'\n'
  done <<< "${RAW_HITS}"
fi

# Strip trailing newline introduced by the loop.
FILTERED_HITS="${FILTERED_HITS%$'\n'}"

# ---------------------------------------------------------------------------
# Report
# ---------------------------------------------------------------------------

if [[ -z "${FILTERED_HITS}" ]]; then
  _ok "no vendor-coupling violations found across ${#EXISTING_PATHS[@]} scan path(s)"
  exit 0
fi

# We have violations. In hard-fail mode, fail; otherwise warn-only.
hit_count="$(echo "${FILTERED_HITS}" | wc -l | tr -d ' ')"

if [[ "${HARD_FAIL}" -eq 1 ]]; then
  _err "${hit_count} vendor-coupling violation(s) found"
  echo "" >&2
  echo "${FILTERED_HITS}" >&2
  echo "" >&2
  echo "Vendor names must not appear in capability-named slots. The canonical-seam map" >&2
  echo "is documented at docs/archive/omantel-handover-wbs.md §3a; principle #4 in" >&2
  echo "docs/PRINCIPLES.md (\"never hardcode\") covers the rationale." >&2
  echo "" >&2
  echo "Common fixes:" >&2
  echo "  - rename '<vendor>-object-storage' Secret/Overlay to 'object-storage-credentials'" >&2
  echo "  - re-key chart values: 'veleroOverlay.hetzner.s3.x' → 'veleroOverlay.objectStorage.s3.x'" >&2
  echo "  - rename payload field '<vendor>ObjectStorage' → 'objectStorage' with provider tag" >&2
  exit 1
fi

# Warn-only mode: emit and exit 0.
_warn "${hit_count} vendor-coupling reference(s) — WARN-ONLY (pre-#425 work-in-progress)"
echo "" >&2
echo "${FILTERED_HITS}" >&2
echo "" >&2
echo "These will become hard-fails once internal/objectstorage/ lands (PR #425)." >&2
echo "See docs/archive/omantel-handover-wbs.md §3a + docs/PRINCIPLES.md #4." >&2
exit 0
