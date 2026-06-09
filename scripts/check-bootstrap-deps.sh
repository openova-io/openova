#!/usr/bin/env bash
# check-bootstrap-deps.sh — bootstrap-kit dependency-graph audit (W2.K0).
#
# Authoritative spec: docs/ARCHITECTURE.md §2 + §3.
#
# What this does:
#   1. Parses every clusters/_template/bootstrap-kit/*.yaml and extracts
#      metadata.name + spec.dependsOn for the HelmRelease document(s).
#   2. Compares the actual deps graph against the expected DAG declared in
#      scripts/expected-bootstrap-deps.yaml.
#   3. Fails (non-zero exit) on any drift: missing or extra edges, unknown HRs.
#   4. Detects cycles: asserts no HR transitively depends on itself.
#   5. On success, prints the rendered DAG as ASCII (per-tier topological view).
#
# Exit codes:
#   0  — actual graph matches expected, no cycles, all present HRs validated
#   1  — drift (missing/extra deps, unknown HR present, etc.)
#   2  — cycle detected
#   3  — input/parse/usage error
#
# Behaviour against an in-flight expansion (W2.K1..K4 staggered merges):
#   HRs declared in expected-bootstrap-deps.yaml but not yet present on disk
#   are reported as "deferred" (informational, not an error). HRs present on
#   disk but not declared in expected-bootstrap-deps.yaml are an error — every
#   new bootstrap-kit slot must update the expected file in the same PR.
#
# Usage:
#   scripts/check-bootstrap-deps.sh
#   scripts/check-bootstrap-deps.sh --kit-dir clusters/_template/bootstrap-kit \
#                                   --expected scripts/expected-bootstrap-deps.yaml
#
# Dependencies: bash, yq (mikefarah, v4+), find, sort, awk.

set -euo pipefail

# ---------------------------------------------------------------------------
# Defaults + arg parsing
# ---------------------------------------------------------------------------

# Resolve repo root from this script's location so the tool works from any cwd.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

KIT_DIR="${REPO_ROOT}/clusters/_template/bootstrap-kit"
EXPECTED_FILE="${REPO_ROOT}/scripts/expected-bootstrap-deps.yaml"

usage() {
  cat <<EOF
Usage: $(basename "$0") [--kit-dir DIR] [--expected FILE]

  --kit-dir DIR    Directory containing bootstrap-kit HR yaml files
                   (default: ${KIT_DIR})
  --expected FILE  Path to expected-DAG yaml data file
                   (default: ${EXPECTED_FILE})
  -h, --help       Show this message

See docs/ARCHITECTURE.md §2 + §3 for the design contract.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --kit-dir)
      KIT_DIR="$2"
      shift 2
      ;;
    --expected)
      EXPECTED_FILE="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "ERROR: unknown argument: $1" >&2
      usage >&2
      exit 3
      ;;
  esac
done

if ! command -v yq >/dev/null 2>&1; then
  echo "ERROR: yq is required but not installed." >&2
  echo "Install: wget -qO /usr/local/bin/yq https://github.com/mikefarah/yq/releases/latest/download/yq_linux_amd64 && chmod +x /usr/local/bin/yq" >&2
  exit 3
fi

if [[ ! -d "${KIT_DIR}" ]]; then
  echo "ERROR: kit directory does not exist: ${KIT_DIR}" >&2
  exit 3
fi
if [[ ! -f "${EXPECTED_FILE}" ]]; then
  echo "ERROR: expected DAG file does not exist: ${EXPECTED_FILE}" >&2
  exit 3
fi

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

# Print a coloured banner if stdout is a TTY; otherwise plain text.
_banner() {
  local title="$1"
  if [[ -t 1 ]]; then
    printf '\n\033[1;36m== %s ==\033[0m\n' "${title}"
  else
    printf '\n== %s ==\n' "${title}"
  fi
}

_err() {
  if [[ -t 2 ]]; then
    printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2
  else
    printf 'ERROR: %s\n' "$*" >&2
  fi
}

_warn() {
  if [[ -t 1 ]]; then
    printf '\033[1;33mWARN:\033[0m %s\n' "$*"
  else
    printf 'WARN: %s\n' "$*"
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
# Phase 1 — Parse expected DAG
# ---------------------------------------------------------------------------

_banner "Phase 1: parse expected DAG (${EXPECTED_FILE#"${REPO_ROOT}/"})"

# Build two associative arrays:
#   EXPECTED_DEPS[name]="dep1 dep2 ..."  (space-separated, sorted)
#   EXPECTED_SLOT[name]="<int>"
#   EXPECTED_WAVE[name]="<wave-tag>"
declare -A EXPECTED_DEPS=()
declare -A EXPECTED_SLOT=()
declare -A EXPECTED_WAVE=()
EXPECTED_NAMES=()

# yq emits one record per line: "<slot>|<name>|<wave>|<dep1,dep2,...>"
while IFS='|' read -r slot name wave deps_csv; do
  [[ -z "${name}" ]] && continue
  EXPECTED_NAMES+=("${name}")
  EXPECTED_SLOT["${name}"]="${slot}"
  EXPECTED_WAVE["${name}"]="${wave}"
  if [[ -z "${deps_csv}" ]]; then
    EXPECTED_DEPS["${name}"]=""
  else
    # Sort deps so set comparison is order-insensitive.
    EXPECTED_DEPS["${name}"]="$(echo "${deps_csv}" | tr ',' '\n' | sort -u | tr '\n' ' ' | sed 's/ $//')"
  fi
done < <(
  yq -r '
    .slots[] |
    [
      (.slot | tostring),
      .name,
      .wave,
      ((.depends_on // []) | join(","))
    ] | join("|")
  ' "${EXPECTED_FILE}"
)

if [[ ${#EXPECTED_NAMES[@]} -eq 0 ]]; then
  _err "expected DAG file declared no slots (empty .slots[])"
  exit 3
fi

echo "  Loaded ${#EXPECTED_NAMES[@]} expected HRs from ${EXPECTED_FILE#"${REPO_ROOT}/"}"

# ---------------------------------------------------------------------------
# Phase 2 — Parse actual HR files
# ---------------------------------------------------------------------------

_banner "Phase 2: parse actual HRs in ${KIT_DIR#"${REPO_ROOT}/"}"

declare -A ACTUAL_DEPS=()
declare -A ACTUAL_FILE=()
ACTUAL_NAMES=()

# Iterate in slot order to make the output deterministic.
shopt -s nullglob
HR_FILES=()
while IFS= read -r f; do
  HR_FILES+=("$f")
done < <(find "${KIT_DIR}" -maxdepth 1 -type f -name '*.yaml' \
           ! -name 'kustomization.yaml' | sort)
shopt -u nullglob

if [[ ${#HR_FILES[@]} -eq 0 ]]; then
  _err "no HR yaml files found in ${KIT_DIR}"
  exit 3
fi

for f in "${HR_FILES[@]}"; do
  # Each file may contain multiple yaml documents (Namespace, HelmRepository,
  # HelmRelease). Extract the HelmRelease document(s).
  while IFS='|' read -r name deps_csv; do
    [[ -z "${name}" ]] && continue
    if [[ -n "${ACTUAL_DEPS[${name}]+x}" ]]; then
      _err "duplicate HelmRelease name '${name}' (in ${f#"${REPO_ROOT}/"} and ${ACTUAL_FILE[${name}]})"
      exit 3
    fi
    ACTUAL_NAMES+=("${name}")
    ACTUAL_FILE["${name}"]="${f#"${REPO_ROOT}/"}"
    if [[ -z "${deps_csv}" ]]; then
      ACTUAL_DEPS["${name}"]=""
    else
      ACTUAL_DEPS["${name}"]="$(echo "${deps_csv}" | tr ',' '\n' | sort -u | tr '\n' ' ' | sed 's/ $//')"
    fi
  done < <(
    yq -r '
      select(.kind == "HelmRelease") |
      [
        .metadata.name,
        ((.spec.dependsOn // []) | map(.name) | join(","))
      ] | join("|")
    ' "$f" 2>/dev/null || true
  )
done

if [[ ${#ACTUAL_NAMES[@]} -eq 0 ]]; then
  _err "no HelmRelease resources parsed from any file in ${KIT_DIR}"
  exit 3
fi

echo "  Parsed ${#ACTUAL_NAMES[@]} HelmRelease(s) across ${#HR_FILES[@]} file(s)"

# ---------------------------------------------------------------------------
# Phase 2.5 — Slot-registration audit (kustomization.yaml completeness)
# ---------------------------------------------------------------------------
#
# Every NN-*.yaml slot file that carries a HelmRelease MUST be listed in
# kustomization.yaml's resources[]. An unregistered slot file is INERT:
# `kubectl apply -k` (the production reconcile path) only applies resources
# named in resources[], so the slot's HR is NEVER created on the cluster —
# any consumer that dependsOn that HR dangles forever. This is the #3191
# regression class (slot 16a-bp-postgres-shared.yaml authored but never
# registered → bp-gitea's dependsOn dangled → bp-catalyst-platform wedged
# on hw124). The same shape previously bit bp-sso-bridge (hw86) and
# bp-powerdns-admin (#3150) — a forgotten-slot bug CI never caught.
#
# This guard fails the audit when a slot file with an HR is missing from
# resources[]. (Files with no HelmRelease — e.g. 00-vcluster-host-
# namespaces.yaml — are exempt: they still SHOULD be listed, but their
# absence does not dangle a dependsOn edge, so we only hard-fail on HR-
# bearing files. The drift workflow surfaces the rest.)

_banner "Phase 2.5: slot-registration audit (kustomization.yaml)"

KUSTOMIZATION_FILE="${KIT_DIR}/kustomization.yaml"
if [[ ! -f "${KUSTOMIZATION_FILE}" ]]; then
  _err "kustomization.yaml not found in ${KIT_DIR#"${REPO_ROOT}/"} — cannot audit slot registration."
  exit 3
fi

# The set of basenames listed in resources[]. yq extracts them directly so
# commented-out lines (the documented-but-disabled case) are NOT counted as
# registered — exactly the bug we are guarding against.
declare -A REGISTERED=()
while IFS= read -r res; do
  [[ -z "${res}" ]] && continue
  REGISTERED["${res}"]=1
done < <(yq -r '.resources[]' "${KUSTOMIZATION_FILE}" 2>/dev/null || true)

UNREGISTERED_COUNT=0
for f in "${HR_FILES[@]}"; do
  base="$(basename "$f")"
  # Only HR-bearing files dangle a dependsOn edge when unregistered.
  has_hr="$(yq -r 'select(.kind == "HelmRelease") | .metadata.name' "$f" 2>/dev/null | head -n1 || true)"
  [[ -z "${has_hr}" ]] && continue
  if [[ -z "${REGISTERED[${base}]+x}" ]]; then
    _err "slot file '${base}' carries a HelmRelease ('${has_hr}') but is NOT listed in ${KUSTOMIZATION_FILE#"${REPO_ROOT}/"} resources[]. An unregistered slot is inert (kubectl apply -k skips it) → any consumer dependsOn '${has_hr}' dangles forever (the #3191 / hw124 wedge). Add '- ${base}' to resources[]."
    UNREGISTERED_COUNT=$((UNREGISTERED_COUNT + 1))
  fi
done

if [[ ${UNREGISTERED_COUNT} -gt 0 ]]; then
  echo "" >&2
  _err "${UNREGISTERED_COUNT} HR-bearing slot file(s) missing from kustomization.yaml resources[]. Register each and re-run."
  exit 1
fi

_ok "every HR-bearing slot file is registered in kustomization.yaml resources[]"

# Phase 2.5b — kustomize build sanity. Registration alone is necessary but
# not sufficient: a newly-registered slot can still break the merged set
# (e.g. a duplicate resource id — two slots declaring the same Namespace,
# which is how 26-network-policies.yaml + 13-bp-catalyst-platform.yaml
# collided on catalyst-system when 26 was first registered in #3188). A
# `kustomize build` of the kit catches that whole class. Best-effort: if no
# kustomize binary is available we warn-skip rather than fail the audit, so
# the guard never blocks an environment lacking kustomize/kubectl.
_KUSTOMIZE=""
if command -v kustomize >/dev/null 2>&1; then
  _KUSTOMIZE="kustomize build"
elif command -v kubectl >/dev/null 2>&1; then
  _KUSTOMIZE="kubectl kustomize"
fi
if [[ -n "${_KUSTOMIZE}" ]]; then
  if _kustomize_err="$(${_KUSTOMIZE} "${KIT_DIR}" 2>&1 >/dev/null)"; then
    _ok "kustomize build of ${KIT_DIR#"${REPO_ROOT}/"} succeeds (no duplicate/merge errors)"
  else
    _err "kustomize build of ${KIT_DIR#"${REPO_ROOT}/"} FAILED — the registered resource set does not merge cleanly:"
    echo "${_kustomize_err}" | sed 's/^/         /' >&2
    exit 1
  fi
else
  _warn "kustomize/kubectl not found — skipping the kustomize-build sanity check (registration check still ran)"
fi

# ---------------------------------------------------------------------------
# Phase 3 — Compare actual vs expected (drift detection)
# ---------------------------------------------------------------------------

_banner "Phase 3: drift detection"

DRIFT_COUNT=0
DEFERRED_COUNT=0

# 3a — every actual HR must be declared in expected, with matching deps.
for name in "${ACTUAL_NAMES[@]}"; do
  if [[ -z "${EXPECTED_DEPS[${name}]+x}" ]]; then
    _err "HR '${name}' (file ${ACTUAL_FILE[${name}]}) is present on disk but NOT declared in ${EXPECTED_FILE#"${REPO_ROOT}/"}. Add it to the expected DAG."
    DRIFT_COUNT=$((DRIFT_COUNT + 1))
    continue
  fi

  exp="${EXPECTED_DEPS[${name}]}"
  got="${ACTUAL_DEPS[${name}]}"

  if [[ "${exp}" != "${got}" ]]; then
    # Compute set differences for an actionable message.
    missing=$(comm -23 <(echo "${exp}" | tr ' ' '\n' | sort -u) <(echo "${got}" | tr ' ' '\n' | sort -u) | tr '\n' ' ' | sed 's/ $//')
    extra=$(  comm -13 <(echo "${exp}" | tr ' ' '\n' | sort -u) <(echo "${got}" | tr ' ' '\n' | sort -u) | tr '\n' ' ' | sed 's/ $//')
    _err "HR '${name}' (file ${ACTUAL_FILE[${name}]}): dependsOn drift"
    [[ -n "${missing// /}" ]] && echo "         missing edges (declared expected, NOT in HR): ${missing}" >&2
    [[ -n "${extra// /}"   ]] && echo "         extra edges   (in HR, NOT declared expected):   ${extra}"   >&2
    DRIFT_COUNT=$((DRIFT_COUNT + 1))
  fi
done

# 3b — expected HRs not yet on disk are reported as deferred (info, not error).
for name in "${EXPECTED_NAMES[@]}"; do
  if [[ -z "${ACTUAL_DEPS[${name}]+x}" ]]; then
    DEFERRED_COUNT=$((DEFERRED_COUNT + 1))
    _slot_raw="${EXPECTED_SLOT[${name}]}"
    if [[ "${_slot_raw}" =~ ^[0-9]+$ ]]; then
      _slot_fmt="$(printf '%02d' "${_slot_raw}")"
    else
      _slot_fmt="${_slot_raw}"
    fi
    _warn "HR '${name}' (slot ${_slot_fmt}, wave ${EXPECTED_WAVE[${name}]}) declared expected but not yet on disk — will be added by ${EXPECTED_WAVE[${name}]}"
  fi
done

if [[ ${DRIFT_COUNT} -gt 0 ]]; then
  echo "" >&2
  _err "${DRIFT_COUNT} drift(s) detected. Reconcile HR files with ${EXPECTED_FILE#"${REPO_ROOT}/"} (or vice versa) and re-run."
  exit 1
fi

_ok "no drift between actual HRs and expected DAG (${DEFERRED_COUNT} deferred)"

# ---------------------------------------------------------------------------
# Phase 4 — Cycle detection (Kahn's algorithm)
# ---------------------------------------------------------------------------

_banner "Phase 4: cycle detection"

# We check the *expected* graph (since it's the authoritative DAG and is the
# superset of what's currently on disk). A cycle in the expected graph is the
# bug; any subset on disk inherits the property.
declare -A INDEGREE=()
for name in "${EXPECTED_NAMES[@]}"; do
  INDEGREE["${name}"]=0
done
for name in "${EXPECTED_NAMES[@]}"; do
  for dep in ${EXPECTED_DEPS[${name}]}; do
    [[ -z "${dep}" ]] && continue
    if [[ -z "${INDEGREE[${dep}]+x}" ]]; then
      _err "HR '${name}' depends on unknown HR '${dep}' (not declared in expected DAG)"
      exit 1
    fi
    INDEGREE["${name}"]=$((INDEGREE["${name}"] + 1))
  done
done

# Kahn's algorithm: repeatedly drain zero-in-degree nodes.
declare -a QUEUE=()
declare -a TOPO_ORDER=()
for name in "${EXPECTED_NAMES[@]}"; do
  if [[ "${INDEGREE[${name}]}" -eq 0 ]]; then
    QUEUE+=("${name}")
  fi
done

while [[ ${#QUEUE[@]} -gt 0 ]]; do
  current="${QUEUE[0]}"
  QUEUE=("${QUEUE[@]:1}")
  TOPO_ORDER+=("${current}")
  # For each n that depends on current, decrement its indegree.
  for name in "${EXPECTED_NAMES[@]}"; do
    for dep in ${EXPECTED_DEPS[${name}]}; do
      if [[ "${dep}" == "${current}" ]]; then
        INDEGREE["${name}"]=$((INDEGREE["${name}"] - 1))
        if [[ "${INDEGREE[${name}]}" -eq 0 ]]; then
          QUEUE+=("${name}")
        fi
      fi
    done
  done
done

if [[ "${#TOPO_ORDER[@]}" -ne "${#EXPECTED_NAMES[@]}" ]]; then
  _err "cycle detected in expected DAG"
  echo "  Topo-ordered ${#TOPO_ORDER[@]} of ${#EXPECTED_NAMES[@]} HRs before stalling." >&2
  echo "  Stalled HRs (transitively depend on themselves):" >&2
  for name in "${EXPECTED_NAMES[@]}"; do
    if [[ "${INDEGREE[${name}]}" -gt 0 ]]; then
      echo "    - ${name} (remaining in-degree=${INDEGREE[${name}]})" >&2
    fi
  done
  exit 2
fi

_ok "no cycles (${#EXPECTED_NAMES[@]} HRs topologically ordered)"

# ---------------------------------------------------------------------------
# Phase 5 — Render ASCII DAG (per-wave grouping, topological order within wave)
# ---------------------------------------------------------------------------

_banner "Phase 5: rendered DAG"

cat <<EOF
Bootstrap-kit dependency graph
(authoritative spec: docs/ARCHITECTURE.md §2)

Legend:
  [P]  present on disk and validated
  [.]  declared in expected DAG, deferred (file not yet added by W2.Kn)

EOF

# Group nodes by wave for the printout, in slot order.
declare -A WAVE_HEADERS=(
  ["present"]="Tier 0-4 — Foundation through Catalyst umbrella (post-PR-247 baseline)"
  ["W2.K1"]="Tier 5    — Storage + DB (Wave 2 batch K1, slots 15-19)"
  ["W2.K2"]="Tier 6    — Observability (Wave 2 batch K2, slots 20-26)"
  ["W2.K3"]="Tier 7    — Security + policy (Wave 2 batch K3, slots 27-34)"
  ["W2.K4"]="Tier 8+9  — Edge + apps + AI runtime (Wave 2 batch K4, slots 35-48)"
)

for wave in present W2.K1 W2.K2 W2.K3 W2.K4; do
  echo "${WAVE_HEADERS[${wave}]}"
  printf '%.0s-' {1..78}; echo
  any=0
  for name in "${EXPECTED_NAMES[@]}"; do
    if [[ "${EXPECTED_WAVE[${name}]}" != "${wave}" ]]; then
      continue
    fi
    any=1
    if [[ -n "${ACTUAL_DEPS[${name}]+x}" ]]; then
      marker="[P]"
    else
      marker="[.]"
    fi
    raw_slot="${EXPECTED_SLOT[${name}]}"
    # Slots are mostly numeric (01..35, 49) but may carry an alphabetic
    # sub-slot suffix (e.g. 15a) when a chart is wedged between two
    # already-numbered slots without renumbering downstream consumers
    # — see PR #334 (issue #331) for slot 15a-external-secrets-stores.
    if [[ "${raw_slot}" =~ ^[0-9]+$ ]]; then
      slot="$(printf '%02d' "${raw_slot}")"
    else
      slot="${raw_slot}"
    fi
    deps="${EXPECTED_DEPS[${name}]}"
    if [[ -z "${deps}" ]]; then
      printf '  %s slot %s  %-26s (root, no deps)\n' "${marker}" "${slot}" "${name}"
    else
      printf '  %s slot %s  %-26s <-- %s\n' "${marker}" "${slot}" "${name}" "${deps}"
    fi
  done
  if [[ "${any}" -eq 0 ]]; then
    echo "  (none)"
  fi
  echo ""
done

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

_banner "Summary"
present_count=${#ACTUAL_NAMES[@]}
expected_count=${#EXPECTED_NAMES[@]}
deferred_count=$((expected_count - present_count))
echo "  Present on disk:       ${present_count}"
echo "  Declared expected:     ${expected_count}"
echo "  Deferred (W2.K1-K4):   ${deferred_count}"
echo "  Drift:                 0"
echo "  Cycles:                0"
echo ""
_ok "bootstrap-kit dependency graph audit PASSED"
