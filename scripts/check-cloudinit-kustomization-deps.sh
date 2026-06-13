#!/usr/bin/env bash
# check-cloudinit-kustomization-deps.sh — drift guard for the top-level
# Flux Kustomizations the control-plane cloud-init creates (#3380 row D).
#
# WHAT THIS DOES
#   1. Parses the THREE top-level Flux Kustomizations defined inline in
#      infra/providers/_shared/cloudinit-control-plane.tftpl —
#      `bootstrap-kit`, `sovereign-tls`, `infrastructure-config` — and
#      extracts each one's metadata.name + spec.dependsOn[].name.
#   2. Compares the actual dependsOn sets against the pinned expectation in
#      scripts/expected-cloudinit-kustomization-deps.yaml.
#   3. Fails (non-zero exit) on ANY drift: a missing edge, an extra edge,
#      an undeclared Kustomization on disk, or a declared-but-absent
#      Kustomization that is NOT marked `optional: true`.
#
# WHY (the hazard this guards)
#   `sovereign-tls` dependsOn the whole `bootstrap-kit` Kustomization, so a
#   stall ANYWHERE in the kit (hw130: shared-PG) serializes the wildcard-TLS
#   cert path behind it and delays ALL TLS (~75 min, proven). The
#   bootstrap-kit dependency-graph audit (check-bootstrap-deps.sh) only
#   covers HelmReleases INSIDE the kit — these three top-level Kustomizations
#   had no guard at all. Once the `sovereign-tls` edge is narrowed it must
#   not silently drift back; this guard locks it.
#
# Exit codes:
#   0  — actual == expected for every Kustomization
#   1  — drift (missing/extra dep, undeclared/missing Kustomization)
#   3  — input/parse/usage error
#
# Dependencies: bash, yq (mikefarah v4+), awk.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

TFTPL="${REPO_ROOT}/infra/providers/_shared/cloudinit-control-plane.tftpl"
EXPECTED_FILE="${REPO_ROOT}/scripts/expected-cloudinit-kustomization-deps.yaml"

usage() {
  cat <<EOF
Usage: $(basename "$0") [--tftpl FILE] [--expected FILE]

  --tftpl FILE     control-plane cloud-init template to scan
                   (default: ${TFTPL})
  --expected FILE  pinned expected-dependsOn data file
                   (default: ${EXPECTED_FILE})
  -h, --help       Show this message

See #3380 row D. To change a dependency intentionally, edit the .tftpl
Kustomization AND ${EXPECTED_FILE#"${REPO_ROOT}/"} in the same PR.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --tftpl)    TFTPL="$2"; shift 2 ;;
    --expected) EXPECTED_FILE="$2"; shift 2 ;;
    -h|--help)  usage; exit 0 ;;
    *) echo "ERROR: unknown argument: $1" >&2; usage >&2; exit 3 ;;
  esac
done

if ! command -v yq >/dev/null 2>&1; then
  echo "ERROR: yq (mikefarah v4+) is required but not installed." >&2
  exit 3
fi
[[ -f "${TFTPL}" ]]        || { echo "ERROR: tftpl not found: ${TFTPL}" >&2; exit 3; }
[[ -f "${EXPECTED_FILE}" ]] || { echo "ERROR: expected file not found: ${EXPECTED_FILE}" >&2; exit 3; }

_ok()   { printf 'OK: %s\n'    "$*"; }
_err()  { printf 'ERROR: %s\n' "$*" >&2; }

# ---------------------------------------------------------------------------
# Phase 1 — extract actual Kustomization name + dependsOn from the .tftpl
# ---------------------------------------------------------------------------
#
# The Kustomizations are embedded as YAML inside a cloud-init `write_files`
# `content: |` block (indented), interleaved with HCL `%{ if }` directives
# and `${...}` substitutions — so the region is NOT parseable as pure YAML.
# We therefore do an indentation-aware scan with awk that is scoped to
# `kind: Kustomization` documents only:
#
#   - A document starts at a line whose trimmed content is "kind: Kustomization".
#   - We record the line's indent as the document's base column.
#   - Within the document we capture `name:` at metadata level (the FIRST
#     `name:` after the kind line, before `spec:`) and every `- name: X`
#     under a `dependsOn:` key (until a sibling key at dependsOn's indent or
#     a new document `---`/kind line).
#
# This is robust to the HCL directives because we only read the dependsOn
# list lines, which are plain `- name: <literal>` entries.
ACTUAL="$(awk '
  function trim(s){ sub(/^[ \t]+/,"",s); sub(/[ \t]+$/,"",s); return s }
  function indent(line,   n){ n=match(line,/[^ ]/); return (n==0)?0:n-1 }
  BEGIN{ in_doc=0; in_deps=0; name=""; deps=""; doc_indent=-1; deps_indent=-1; meta_seen=0 }
  {
    line=$0
    t=trim(line)
    ind=indent(line)

    # New Kustomization document begins.
    if (t=="kind: Kustomization") {
      # flush a previous doc if we were in one
      if (in_doc && name!="") { print name "|" deps }
      in_doc=1; in_deps=0; name=""; deps=""; meta_seen=0
      doc_indent=ind
      next
    }

    if (!in_doc) next

    # A document boundary: a `---` at or below the doc indent, OR another
    # top-level resource key (apiVersion:) starting a new doc, ends this one.
    if (t=="---" || t ~ /^apiVersion:/) {
      # apiVersion may be the NEXT doc header; the kind line that follows
      # will (re)open a doc. Close the current one here.
      if (name!="") { print name "|" deps }
      in_doc=0; in_deps=0; name=""; deps=""; meta_seen=0
      next
    }

    # metadata.name — the first `name:` we see in the doc (under metadata,
    # before spec). Guard with meta_seen so a later `name:` (e.g. sourceRef
    # name, substituteFrom name) is NOT mistaken for the Kustomization name.
    if (!meta_seen && t ~ /^name:[ ]*/) {
      v=t; sub(/^name:[ ]*/,"",v); gsub(/["'\'']/,"",v); v=trim(v)
      name=v; meta_seen=1
      next
    }

    # Enter dependsOn block.
    if (t ~ /^dependsOn:[ ]*$/) {
      in_deps=1; deps_indent=ind; next
    }

    if (in_deps) {
      # A list item under dependsOn: `- name: X`.
      if (t ~ /^-[ ]*name:[ ]*/) {
        v=t; sub(/^-[ ]*name:[ ]*/,"",v); gsub(/["'\'']/,"",v); v=trim(v)
        deps=(deps==""? v : deps "," v)
        next
      }
      # A bare `-` list item is unexpected for dependsOn (always has name:)
      # — ignore. Leave the block when we hit a key at deps_indent or less
      # that is not a list item (a sibling spec key like wait:/timeout:).
      if (ind<=deps_indent && t !~ /^-/) { in_deps=0 }
    }
  }
  END{ if (in_doc && name!="") print name "|" deps }
' "${TFTPL}")"

if [[ -z "${ACTUAL}" ]]; then
  _err "no Kustomization documents parsed from ${TFTPL#"${REPO_ROOT}/"} — extractor returned nothing."
  exit 3
fi

declare -A ACTUAL_DEPS=()
ACTUAL_NAMES=()
while IFS='|' read -r name deps_csv; do
  [[ -z "${name}" ]] && continue
  if [[ -n "${ACTUAL_DEPS[${name}]+x}" ]]; then
    _err "duplicate Kustomization name '${name}' parsed from the .tftpl"
    exit 3
  fi
  ACTUAL_NAMES+=("${name}")
  if [[ -z "${deps_csv}" ]]; then
    ACTUAL_DEPS["${name}"]=""
  else
    ACTUAL_DEPS["${name}"]="$(echo "${deps_csv}" | tr ',' '\n' | sort -u | tr '\n' ' ' | sed 's/ $//')"
  fi
done <<< "${ACTUAL}"

# ---------------------------------------------------------------------------
# Phase 2 — load expected
# ---------------------------------------------------------------------------
declare -A EXPECTED_DEPS=()
declare -A EXPECTED_OPTIONAL=()
EXPECTED_NAMES=()
while IFS='|' read -r name optional deps_csv; do
  [[ -z "${name}" ]] && continue
  EXPECTED_NAMES+=("${name}")
  EXPECTED_OPTIONAL["${name}"]="${optional}"
  if [[ -z "${deps_csv}" ]]; then
    EXPECTED_DEPS["${name}"]=""
  else
    EXPECTED_DEPS["${name}"]="$(echo "${deps_csv}" | tr ',' '\n' | sort -u | tr '\n' ' ' | sed 's/ $//')"
  fi
done < <(
  yq -r '
    .kustomizations[] |
    [ .name, ((.optional // false) | tostring), ((.depends_on // []) | join(",")) ] | join("|")
  ' "${EXPECTED_FILE}"
)

if [[ ${#EXPECTED_NAMES[@]} -eq 0 ]]; then
  _err "expected file declared no kustomizations (empty .kustomizations[])"
  exit 3
fi

# ---------------------------------------------------------------------------
# Phase 3 — compare
# ---------------------------------------------------------------------------
DRIFT=0

# 3a — every ACTUAL Kustomization must be declared, deps must match.
for name in "${ACTUAL_NAMES[@]}"; do
  if [[ -z "${EXPECTED_DEPS[${name}]+x}" ]]; then
    _err "Kustomization '${name}' is present in ${TFTPL#"${REPO_ROOT}/"} but NOT declared in ${EXPECTED_FILE#"${REPO_ROOT}/"}. Add it (with its dependsOn) to the expected file."
    DRIFT=$((DRIFT+1)); continue
  fi
  exp="${EXPECTED_DEPS[${name}]}"
  got="${ACTUAL_DEPS[${name}]}"
  if [[ "${exp}" != "${got}" ]]; then
    missing=$(comm -23 <(echo "${exp}" | tr ' ' '\n' | sort -u) <(echo "${got}" | tr ' ' '\n' | sort -u) | tr '\n' ' ' | sed 's/ $//')
    extra=$(  comm -13 <(echo "${exp}" | tr ' ' '\n' | sort -u) <(echo "${got}" | tr ' ' '\n' | sort -u) | tr '\n' ' ' | sed 's/ $//')
    _err "Kustomization '${name}': dependsOn drift (expected '[${exp}]' got '[${got}]')."
    [[ -n "${missing// /}" ]] && echo "         missing (declared expected, NOT in .tftpl): ${missing}" >&2
    [[ -n "${extra// /}"   ]] && echo "         extra   (in .tftpl, NOT declared expected): ${extra}"   >&2
    DRIFT=$((DRIFT+1))
  fi
done

# 3b — every EXPECTED Kustomization must be present unless optional.
for name in "${EXPECTED_NAMES[@]}"; do
  if [[ -z "${ACTUAL_DEPS[${name}]+x}" ]]; then
    if [[ "${EXPECTED_OPTIONAL[${name}]}" == "true" ]]; then
      echo "OK: optional Kustomization '${name}' absent from this template (provider-gated) — not drift."
    else
      _err "Kustomization '${name}' is declared expected but NOT present in ${TFTPL#"${REPO_ROOT}/"}. If it was intentionally removed, drop it from the expected file; otherwise it drifted out."
      DRIFT=$((DRIFT+1))
    fi
  fi
done

if [[ ${DRIFT} -gt 0 ]]; then
  echo "" >&2
  _err "${DRIFT} cloud-init Kustomization dependsOn drift(s). Reconcile the .tftpl with the expected file (or vice versa) in the SAME PR."
  exit 1
fi

echo ""
for name in "${ACTUAL_NAMES[@]}"; do
  deps="${ACTUAL_DEPS[${name}]}"
  if [[ -z "${deps}" ]]; then
    printf '  %-22s (root, no dependsOn)\n' "${name}"
  else
    printf '  %-22s <-- %s\n' "${name}" "${deps}"
  fi
done
echo ""
_ok "cloud-init Kustomization dependsOn matches the pinned expectation (${#ACTUAL_NAMES[@]} checked)."
