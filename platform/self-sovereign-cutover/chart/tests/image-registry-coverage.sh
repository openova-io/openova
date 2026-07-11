#!/usr/bin/env bash
# bp-self-sovereign-cutover — offline-mirror IMAGE-REGISTRY COVERAGE guardrail
# (#4975 Refs #3379 #3695).
#
# THE WHACK-A-MOLE TRAP, CLOSED AT AUTHORING TIME. The step-08 deny-egress proof
# converges only if the local Harbor is a COMPLETE offline mirror of every
# workload image. step-03 mirrors + step-04 rewrites strictly what the coverage
# map (chart values.yaml `offlineMirror.hostProjects` + `.excludedHosts`) covers.
# So the ONE thing that can silently re-open the whack-a-mole is a chart adding
# an image from a registry host the map does not cover. This guardrail scans
# every bootstrap-kit slot's container-image refs and FAILS the build the moment
# a ref's registry host is neither mapped nor explicitly excluded — BEFORE it can
# wedge a live cutover mid deny-egress hold.
#
# It reads the coverage map from the SAME chart values.yaml the runtime steps
# render from (single source of truth) — nothing to keep in sync.
#
# SUB-CHART DESCENT (#4975 Refs #4977). The scan surface includes every
# platform/<x>/chart/values.yaml + products/<x>/chart/values.yaml (where the
# bootstrap-kit slots' referenced charts pin their image hosts), and it parses
# the SPLIT `registry:` + `repository:` shape — so a sub-chart image from an
# un-mapped host (mirror.gcr.io in platform/trivy) FAILS the build at PR time
# instead of wedging a live cutover mid deny-egress hold (the hw239 finding).
#
# Usage:
#   tests/image-registry-coverage.sh            # scan the repo's bootstrap-kit slots
#   tests/image-registry-coverage.sh --self-test # negative + positive coverage self-tests
#
# Exit: 0 = every scanned host is covered (or excluded); 1 = at least one
# un-mapped host (named); 2 = tool/input error.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CHART_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
VALUES="${CHART_DIR}/values.yaml"

# Repo root — the chart lives at platform/self-sovereign-cutover/chart, so the
# repo root is four levels up. Allow override for CI layouts.
REPO_ROOT="${REPO_ROOT:-$(cd "${CHART_DIR}/../../.." && pwd)}"
SLOT_DIR="${SLOT_DIR:-${REPO_ROOT}/clusters/_template/bootstrap-kit}"

if ! command -v yq >/dev/null 2>&1; then
  echo "ERROR: yq not installed" >&2; exit 2
fi
if [ ! -f "${VALUES}" ]; then
  echo "ERROR: chart values.yaml not found at ${VALUES}" >&2; exit 2
fi

# ── Coverage sets from the chart values (single source of truth) ─────────────
MAPPED_HOSTS="$(yq -r '.offlineMirror.hostProjects[].host' "${VALUES}" | sort -u)"
EXCLUDED_HOSTS="$(yq -r '.offlineMirror.excludedHosts[]' "${VALUES}" 2>/dev/null | sort -u || true)"
EXCLUDED_SUBSTR="$(yq -r '.offlineMirror.excludedImageSubstrings[]' "${VALUES}" 2>/dev/null || true)"

host_covered() {
  # $1 = registry host. Covered if mapped OR excluded OR docker.io (bare images).
  _h="$1"
  [ "${_h}" = "docker.io" ] && return 0
  for _m in ${MAPPED_HOSTS}; do [ "${_h}" = "${_m}" ] && return 0; done
  for _e in ${EXCLUDED_HOSTS}; do [ "${_h}" = "${_e}" ] && return 0; done
  return 1
}

is_excluded_substr() {
  # $1 = full image ref. True if it contains an excluded substring.
  _r="$1"
  for _s in ${EXCLUDED_SUBSTR}; do
    case "${_r}" in *"${_s}"*) return 0 ;; esac
  done
  return 1
}

ref_host() {
  # $1 = image ref (or a bare `registry:` field value). Prints its registry
  # host, or "" to SKIP (templated/local).
  _r="$1"
  # Strip an oci:// scheme (chart repos); ghcr.io is mapped anyway.
  _r="${_r#oci://}"
  # Templated (registry.${SOVEREIGN_FQDN}, ${VAR}/...) → local/dynamic → skip.
  case "${_r}" in *'${'*|*'}'*) printf ''; return 0 ;; esac
  case "${_r}" in
    */*)
      _h="${_r%%/*}"
      case "${_h}" in
        *.*|*:*|localhost) printf '%s' "${_h}" ;;
        *) printf 'docker.io' ;;    # docker.io namespaced (e.g. grafana/loki)
      esac
      ;;
    *)
      # No slash. A bare `registry:` field value that LOOKS like a host
      # (contains a `.`, e.g. mirror.gcr.io / harbor.openova.io) is a REGISTRY
      # HOST — this is the split `registry:` + `repository:` shape (upstream
      # Bitnami/trivy-operator idiom) the old scanner was blind to, so a
      # sub-chart image on an un-mapped host (mirror.gcr.io in platform/trivy)
      # slipped through as covered `docker.io`. A bare OFFICIAL image
      # (busybox, nginx:1.25 — the colon is a TAG, not a host:port) has NO dot
      # → docker.io. A non-host `registry:` value (external-dns `registry: txt`)
      # has no dot → docker.io (covered, harmless).
      case "${_r}" in
        *.*) printf '%s' "${_r}" ;;   # bare registry host (mirror.gcr.io)
        *)   printf 'docker.io' ;;    # bare official image / non-host token
      esac
      ;;
  esac
}

scan_refs() {
  # Emit candidate image refs (host-bearing) from the given files. Matches
  # `image:`, `repository:`, `registry:`, `package:`, `spec.package:` values;
  # strips quotes and inline comments; ignores comment-only lines.
  #
  # `registry:` is scanned so the SPLIT `registry:` + `repository:` sub-chart
  # image shape (platform/trivy pins `registry: mirror.gcr.io` +
  # `repository: aquasec/trivy` in the umbrella values) is caught — the old
  # scanner only saw `repository: aquasec/trivy` and mis-classified the host as
  # the covered `docker.io`, so an image from an un-mapped registry host slipped
  # through (the live hw239 whack-a-mole this guardrail exists to close).
  grep -rhoE '^[[:space:]]*(image|repository|registry|package):[[:space:]]*["'\'']?[A-Za-z0-9_./:$?{}%-]+' "$@" 2>/dev/null \
    | sed -E 's/^[[:space:]]*(image|repository|registry|package):[[:space:]]*["'\'']?//' \
    | sed -E 's/["'\''].*$//' \
    | grep -vE '^[[:space:]]*$' \
    | sort -u
}

run_scan() {
  echo "[image-registry-coverage] coverage map (hosts): $(echo "${MAPPED_HOSTS}" | tr '\n' ' ')"
  echo "[image-registry-coverage] excluded hosts: $(echo "${EXCLUDED_HOSTS}" | tr '\n' ' ')"
  echo "[image-registry-coverage] scanning $# file(s)"
  local refs uncovered n_refs n_hosts
  refs="$(scan_refs "$@")"
  uncovered=""
  n_refs=0
  seen_hosts=""
  while IFS= read -r ref; do
    [ -z "${ref}" ] && continue
    is_excluded_substr "${ref}" && continue
    h="$(ref_host "${ref}")"
    [ -z "${h}" ] && continue     # templated/local → skip
    n_refs=$((n_refs+1))
    case " ${seen_hosts} " in *" ${h} "*) : ;; *) seen_hosts="${seen_hosts} ${h}" ;; esac
    if ! host_covered "${h}"; then
      uncovered="${uncovered}\n  UNMAPPED host '${h}'  (ref: ${ref})"
    fi
  done <<EOF
${refs}
EOF
  n_hosts="$(echo "${seen_hosts}" | wc -w | tr -d ' ')"
  echo "[image-registry-coverage] checked ${n_refs} host-bearing image ref(s) across ${n_hosts} distinct host(s)"
  if [ -n "${uncovered}" ]; then
    echo "FAIL: image ref(s) from registry host(s) NOT in the offline-mirror coverage map (offlineMirror.hostProjects) and NOT excluded:" >&2
    printf '%b\n' "${uncovered}" >&2
    echo "" >&2
    echo "FIX: add the host→project to platform/self-sovereign-cutover/chart/values.yaml offlineMirror.hostProjects (and harbor.mirrorProjects), or add it to offlineMirror.excludedHosts if it is handled outside the containerd mirror." >&2
    return 1
  fi
  echo "  PASS — every scanned image host is covered by the offline-mirror map (or excluded)."
  return 0
}

if [ "${1:-}" = "--self-test" ]; then
  # NEGATIVE TEST: prove an un-mapped registry host FAILS the guardrail.
  TMP="$(mktemp -d)"; trap 'rm -rf "${TMP}"' EXIT
  cat > "${TMP}/slot.yaml" <<'YAML'
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
spec:
  values:
    image:
      repository: totally-unmapped.example.net/foo/bar
      tag: "1.0"
YAML
  echo "[image-registry-coverage] --self-test: an image from 'totally-unmapped.example.net' MUST fail the guardrail"
  if run_scan "${TMP}" >/dev/null 2>&1; then
    echo "SELF-TEST FAIL: an un-mapped registry host was NOT caught — the guardrail is inert." >&2
    exit 1
  fi
  echo "  PASS — un-mapped host correctly FAILED the guardrail (negative test green)."
  # NEGATIVE TEST 2 (#4975): the SPLIT `registry:` + `repository:` sub-chart
  # shape (platform/trivy idiom) on an UN-MAPPED host MUST fail — proving the
  # scanner parses `registry:` and no longer mis-reads the host as docker.io.
  cat > "${TMP}/slot-split.yaml" <<'YAML'
spec:
  values:
    image:
      registry: unmapped-split.example.net
      repository: foo/bar
      tag: "1.0"
YAML
  echo "[image-registry-coverage] --self-test: a SPLIT registry:/repository: image from 'unmapped-split.example.net' MUST fail the guardrail"
  if run_scan "${TMP}" >/dev/null 2>&1; then
    echo "SELF-TEST FAIL: a split registry:/repository: image on an un-mapped host was NOT caught — the scanner still ignores registry:." >&2
    exit 1
  fi
  echo "  PASS — split-form un-mapped host correctly FAILED the guardrail (negative test 2 green)."
  rm -f "${TMP}/slot.yaml" "${TMP}/slot-split.yaml"
  # And prove the covered case passes.
  cat > "${TMP}/slot-ok.yaml" <<'YAML'
spec:
  values:
    image:
      repository: harbor.openova.io/proxy-ghcr/cloudnative-pg/postgresql
YAML
  if ! run_scan "${TMP}" >/dev/null 2>&1; then
    echo "SELF-TEST FAIL: a mapped host (harbor.openova.io) was wrongly rejected." >&2
    exit 1
  fi
  echo "  PASS — mapped host correctly ACCEPTED (positive control green)."
  # POSITIVE TEST 2 (#4975): the platform/trivy SPLIT-form image
  # `registry: mirror.gcr.io` + `repository: aquasec/trivy*` MUST now be COVERED
  # (proves Fix 1's coverage-map entry + Fix 2's registry: parsing land together
  # — the exact hw239 offender is caught AND resolved).
  cat > "${TMP}/slot-trivy.yaml" <<'YAML'
spec:
  values:
    trivy-operator:
      image:
        registry: mirror.gcr.io
        repository: aquasec/trivy-operator
        tag: "0.30.1"
      trivy:
        image:
          registry: mirror.gcr.io
          repository: aquasec/trivy
          tag: "0.70.0"
YAML
  echo "[image-registry-coverage] --self-test: mirror.gcr.io/aquasec/trivy* (platform/trivy split form) MUST be covered"
  if ! run_scan "${TMP}" >/dev/null 2>&1; then
    echo "SELF-TEST FAIL: mirror.gcr.io/aquasec/trivy* is NOT covered — add host: mirror.gcr.io -> project: proxy-gcr to offlineMirror.hostProjects." >&2
    exit 1
  fi
  echo "  PASS — mirror.gcr.io/aquasec/trivy* correctly ACCEPTED (positive test 2 green)."
  exit 0
fi

if [ ! -d "${SLOT_DIR}" ]; then
  echo "WARN: bootstrap-kit slot dir not found at ${SLOT_DIR} — set SLOT_DIR or REPO_ROOT. Skipping repo scan." >&2
  exit 0
fi
# Scan surface = every place a bootstrap-kit workload image host is statically
# pinned in this repo: the slot overrides AND every platform/products chart
# values.yaml (where bp-* charts — including the bootstrap-kit slots' referenced
# SUB-CHARTS, e.g. platform/trivy — pin image.repository / image.registry
# hosts). The actual image TAGS live in the OCI charts, but the HOST — the only
# thing this guardrail checks — is pinned here, whether as a single
# `repository: host/path` or the split `registry: host` + `repository: path`
# shape. Templated refs (registry.${SOVEREIGN_FQDN}, ${REGISTRY_MIRROR:=…}) are
# skipped (dynamic/local).
SCAN_FILES="$(
  find "${SLOT_DIR}" -maxdepth 1 -name '*.yaml' 2>/dev/null
  find "${REPO_ROOT}/platform" -path '*/chart/values.yaml' 2>/dev/null
  find "${REPO_ROOT}/products" -path '*/chart/values.yaml' 2>/dev/null
)"
# shellcheck disable=SC2086
run_scan ${SCAN_FILES}
