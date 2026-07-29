#!/usr/bin/env bash
# bp-self-sovereign-cutover — offline-mirror HOST->PROJECT RENDER guard
# (#5442 Refs #4975 #5443).
#
# WHAT THIS PROTECTS. `offlineMirror.hostProjects` is the single coverage map
# step-03 (skopeo push dest), step-04 (containerd certs.d rewrite) and step-08
# (pre-hold completeness HEAD + self-heal warm) all derive local paths from. A
# registry host missing from it is not a cosmetic gap: step-03's Phase A3 guard
# FATALs with `unmapped=N` and the cutover stops before step-04 — which is
# exactly how live hw291 stalled on 2026-07-29 with `copy_fail=0 durable_miss=0
# unmapped=5` while nothing was actually broken.
#
# WHY A RENDER GUARD AND NOT A VALUES GUARD. tests/image-registry-coverage.sh
# already scans this repo's chart values for un-mapped hosts — but its surface
# is THIS REPO. The two hosts that stalled hw291 are declared by UPSTREAM
# sub-charts (litmuschaos/litmus 3.28.0 defaults its registry to the Scarf
# gateway `litmuschaos.docker.scarf.sh`; sigstore/policy-controller pins
# `cgr.dev/chainguard/kubectl:latest-dev`), so no in-repo values.yaml names
# them and the authoring-time scan is structurally blind to them. Their only
# in-repo trace is the coverage map itself — so the map entries must be
# defended directly, at the RENDER, where the runtime steps actually read them.
#
# WHY IT CHECKS THE RENDER AND NOT values.yaml. `_helpers.tpl`
# hostProjectMapInline is what the steps consume; a values key can be present
# and still not reach the env (renamed helper, dropped `range`, a consumer that
# stopped mounting the map). Asserting the values file proves nothing about the
# Job that runs.
#
# WHY THE VACUITY CHECK. `grep -q 'cgr.dev' render.yaml` passes trivially
# against an EMPTY render — a chart that fails to template, a `--show-only`
# typo, or a helper that yields "" all produce a green grep. So before any
# host assertion this guard proves the render is real: the HOST_PROJECT_MAP env
# must exist in >=3 consuming steps, its value must be non-empty, must parse
# into at least as many `host:project` tokens as the contract requires, and
# every occurrence must be BYTE-IDENTICAL (one source of truth, per #4975).
#
# LOCKSTEP. Every non-empty project named by the map must also be created by
# step-02 (`harbor.mirrorProjects`) — both read out of the SAME render. A host
# mapped to a project step-02 never creates makes step-03's push 404 on a
# non-existent Harbor project, i.e. it trades an `unmapped` failure for a
# `copy_fail` one instead of fixing anything.
#
# Usage:
#   tests/offline-mirror-host-projects.sh [CHART_DIR]
#
# The chart CI gate (.github/workflows/blueprint-release.yaml) invokes every
# chart/tests/*.sh as `bash <script> <chart_dir>`, so CHART_DIR is positional
# and defaults to the script's parent.
#
# Exit: 0 = contract holds (and both negative self-tests fired); 1 = contract
# violated; 2 = tool/input error.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CHART_DIR="$(cd "${1:-${SCRIPT_DIR}/..}" && pwd)"

command -v helm >/dev/null 2>&1 || { echo "ERROR: helm not installed" >&2; exit 2; }
command -v yq   >/dev/null 2>&1 || { echo "ERROR: yq not installed" >&2; exit 2; }

TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

# ── The CONTRACT ─────────────────────────────────────────────────────────────
# Every pair below MUST render into HOST_PROJECT_MAP. Removing any one of them
# from values.yaml re-opens a cutover stall, so removing one must fail the
# build. Empty project = host-swap (path already carries the project segment).
REQUIRED_PAIRS=(
  "ghcr.io:proxy-ghcr"
  "docker.io:proxy-dockerhub"
  "registry-1.docker.io:proxy-dockerhub"
  "index.docker.io:proxy-dockerhub"
  "quay.io:proxy-quay"
  "registry.k8s.io:proxy-k8s"
  "gcr.io:proxy-gcr"
  "k8s.gcr.io:proxy-gcr"
  # #4975 — Google's pull-through mirror of Docker Hub / GCR (bp-trivy).
  "mirror.gcr.io:proxy-gcr"
  # #5442 — Scarf download gateway in front of Docker Hub (bp-litmus, 4 refs:
  # litmusportal-frontend / -server / -auth-server / mongo:6).
  "litmuschaos.docker.scarf.sh:proxy-dockerhub"
  # #5442 — Chainguard (bp-sigstore's policy-controller leases-cleanup Job).
  "cgr.dev:proxy-chainguard"
  "public.ecr.aws:proxy-ecr"
  # #5026 — the mothership host-swap entry (empty project, path preserved).
  "harbor.openova.io:"
)

# Vacuity floor: the smallest token count that still proves the rendered map is
# a real list rather than a stub/empty helper output. Deliberately far below
# the contract size — see the note in check_render.
VACUITY_MIN_TOKENS=3

# Projects whose canonical DIRECT upstream host must be the FIRST map entry
# carrying that project: step-03 copy_one and step-08's self-heal both derive
# the #5095 mothership-proxy direct-source fallback by walking the map and
# taking the FIRST host whose project matches. A redirector host ordered ahead
# of the real registry would silently retarget every fallback at the redirector.
declare -A FIRST_HOST_FOR_PROJECT=(
  ["proxy-dockerhub"]="docker.io"
  ["proxy-gcr"]="gcr.io"
  ["proxy-ghcr"]="ghcr.io"
)

# ── render helper ────────────────────────────────────────────────────────────
render_chart() {
  # $1 = chart dir, $2 = output file. Non-zero on template failure.
  helm template smoke "$1" >"$2" 2>"$2.err"
}

# Extract every rendered HOST_PROJECT_MAP value, one per line.
extract_maps() {
  awk '
    /name: HOST_PROJECT_MAP[[:space:]]*$/ {
      if ((getline line) > 0) {
        sub(/^[[:space:]]*value:[[:space:]]*/, "", line)
        sub(/^"/, "", line); sub(/"[[:space:]]*$/, "", line)
        print line
      }
    }' "$1"
}

# Extract step-02's rendered mirror-project list (the `for mirror_proj in …` loop).
extract_mirror_projects() {
  sed -n 's/^[[:space:]]*for mirror_proj in \(.*\); do[[:space:]]*$/\1/p' "$1" | head -1
}

# ── the assertion body, reusable so the self-tests can re-run it verbatim ─────
# $1 = render file. Prints diagnostics; returns 0 pass / 1 fail.
check_render() {
  local render="$1" rc=0
  local maps first_map n_tokens hpm_count

  # ---- VACUITY GATE -------------------------------------------------------
  if [ ! -s "${render}" ]; then
    echo "FAIL[vacuity]: render is empty — every host assertion below would pass trivially (#5442)" >&2
    return 1
  fi
  hpm_count=$(grep -c 'name: HOST_PROJECT_MAP' "${render}" || true)
  if [ "${hpm_count}" -lt 3 ]; then
    echo "FAIL[vacuity]: HOST_PROJECT_MAP env rendered ${hpm_count}x, expected >=3 (steps 03/04/08 must all consume the map) (#4975)" >&2
    return 1
  fi
  maps="$(extract_maps "${render}")"
  if [ -z "${maps}" ]; then
    echo "FAIL[vacuity]: HOST_PROJECT_MAP is declared but its rendered value could not be extracted — the helper produced nothing (#5442)" >&2
    return 1
  fi
  if [ "$(printf '%s\n' "${maps}" | sort -u | grep -c .)" -ne 1 ]; then
    echo "FAIL: HOST_PROJECT_MAP renders with DIFFERENT values across consuming steps — the map must be one source of truth (#4975):" >&2
    printf '%s\n' "${maps}" | sort -u | sed 's/^/  /' >&2
    return 1
  fi
  first_map="$(printf '%s\n' "${maps}" | head -1)"
  if [ -z "${first_map}" ]; then
    echo "FAIL[vacuity]: HOST_PROJECT_MAP rendered EMPTY — a grep for any host would pass trivially (#5442)" >&2
    return 1
  fi
  # Vacuity floor only — it proves the helper produced a REAL list rather than
  # a stub. Completeness is the named-pair contract's job below, which reports
  # WHICH entry went missing; a floor equal to the contract size would steal
  # that diagnosis and report a count instead of a host.
  n_tokens=$(printf '%s\n' ${first_map} | grep -c . || true)
  if [ "${n_tokens}" -lt "${VACUITY_MIN_TOKENS}" ]; then
    echo "FAIL[vacuity]: HOST_PROJECT_MAP parsed into ${n_tokens} token(s), below the ${VACUITY_MIN_TOKENS}-token floor that proves the map is a real rendered list (#5442)" >&2
    echo "  rendered map: ${first_map}" >&2
    return 1
  fi
  echo "  vacuity OK — map rendered in ${hpm_count} step(s), identical everywhere, ${n_tokens} host:project token(s)"

  # ---- CONTRACT: every required pair is a WHOLE token ----------------------
  # Whole-token comparison, never a substring grep on the render: `cgr.dev`
  # appears in prose comments too, and a substring match would accept a
  # mis-projected `cgr.dev:proxy-dockerhub`.
  local want found tok
  for want in "${REQUIRED_PAIRS[@]}"; do
    found=0
    for tok in ${first_map}; do
      [ "${tok}" = "${want}" ] && { found=1; break; }
    done
    if [ "${found}" -ne 1 ]; then
      echo "FAIL: HOST_PROJECT_MAP is missing the required entry '${want}' (#5442)" >&2
      echo "  rendered map: ${first_map}" >&2
      echo "  FIX: restore it under offlineMirror.hostProjects in values.yaml. Dropping a mapped host makes step-03 Phase A3 FATAL with 'unmapped=N' and the cutover stops before step-04." >&2
      rc=1
    fi
  done
  [ "${rc}" -eq 0 ] && echo "  all ${#REQUIRED_PAIRS[@]} required host:project entries present"

  # ---- CONTRACT: inverse-lookup ordering (#5095) --------------------------
  local proj want_first seen_first
  for proj in "${!FIRST_HOST_FOR_PROJECT[@]}"; do
    want_first="${FIRST_HOST_FOR_PROJECT[$proj]}"
    seen_first=""
    for tok in ${first_map}; do
      case "${tok}" in
        *:"${proj}") seen_first="${tok%%:*}"; break ;;
      esac
    done
    if [ -n "${seen_first}" ] && [ "${seen_first}" != "${want_first}" ]; then
      echo "FAIL: project '${proj}' is first claimed by host '${seen_first}', expected '${want_first}' (#5095)" >&2
      echo "  The mothership-proxy direct-source fallback walks the map and takes the FIRST host whose project matches; a redirector ordered ahead of the real registry retargets every fallback at the redirector." >&2
      rc=1
    fi
  done

  # ---- LOCKSTEP: step-02 creates every project the map names --------------
  local mirror_projects mp mp_found
  mirror_projects="$(extract_mirror_projects "${render}")"
  if [ -z "${mirror_projects}" ]; then
    echo "FAIL[vacuity]: could not extract step-02's mirror-project loop from the render — the lockstep check would be vacuous (#5442)" >&2
    return 1
  fi
  for tok in ${first_map}; do
    mp="${tok#*:}"
    [ -z "${mp}" ] && continue          # host-swap entry: no project to create
    mp_found=0
    for want in ${mirror_projects}; do
      [ "${mp}" = "${want}" ] && { mp_found=1; break; }
    done
    if [ "${mp_found}" -ne 1 ]; then
      echo "FAIL: HOST_PROJECT_MAP maps '${tok%%:*}' to Harbor project '${mp}', which step-02 does NOT create (#5442)" >&2
      echo "  step-02 creates: ${mirror_projects}" >&2
      echo "  FIX: add '${mp}' to harbor.mirrorProjects in values.yaml — otherwise step-03's skopeo push 404s on a non-existent project and one cutover failure is merely traded for a later one." >&2
      rc=1
    fi
  done
  [ "${rc}" -eq 0 ] && echo "  every mapped project is created by step-02 (${mirror_projects})"

  return "${rc}"
}

# ═════════════════════════════════════════════════════════════════════════════
echo "[offline-mirror-host-projects] rendering ${CHART_DIR}"
if ! render_chart "${CHART_DIR}" "${TMP}/render.yaml"; then
  echo "ERROR: helm template failed for ${CHART_DIR}" >&2
  sed 's/^/  /' "${TMP}/render.yaml.err" >&2 || true
  exit 2
fi

echo "[offline-mirror-host-projects] POSITIVE: the shipped chart satisfies the coverage contract"
if ! check_render "${TMP}/render.yaml"; then
  exit 1
fi
echo "  PASS"

# ═════════════════════════════════════════════════════════════════════════════
# NEGATIVE self-tests — a guard that has never been observed to fail is not a
# guard. Each mutates ONE value in a throwaway copy of the chart, renders THAT
# copy, and asserts check_render rejects it.
mutate_and_expect_fail() {
  # $1 = human label, $2 = yq expression applied to the copy's values.yaml,
  # $3 = substring the failure message must contain.
  local label="$1" expr="$2" want_msg="$3"
  local dir="${TMP}/mut-$(printf '%s' "${label}" | tr -c 'A-Za-z0-9' '_')"
  rm -rf "${dir}"; cp -r "${CHART_DIR}" "${dir}"
  yq -i "${expr}" "${dir}/values.yaml"
  if ! render_chart "${dir}" "${dir}/render.yaml"; then
    echo "SELF-TEST FAIL[${label}]: the mutated chart did not render at all — the negative test proves nothing" >&2
    sed 's/^/  /' "${dir}/render.yaml.err" >&2 || true
    return 1
  fi
  local out rc
  set +e
  out="$(check_render "${dir}/render.yaml" 2>&1)"
  rc=$?
  set -e
  if [ "${rc}" -eq 0 ]; then
    echo "SELF-TEST FAIL[${label}]: the guard PASSED a chart it must reject — it does not actually defend the map (#5442)" >&2
    return 1
  fi
  if ! printf '%s' "${out}" | grep -qF "${want_msg}"; then
    echo "SELF-TEST FAIL[${label}]: the guard failed, but not for the expected reason. Wanted a message containing:" >&2
    echo "    ${want_msg}" >&2
    echo "  got:" >&2
    printf '%s\n' "${out}" | sed 's/^/    /' >&2
    return 1
  fi
  echo "  PASS[${label}] — rejected, and for the right reason:"
  printf '%s\n' "${out}" | grep -F "${want_msg}" | sed 's/^/    /'
  return 0
}

echo "[offline-mirror-host-projects] NEGATIVE 1: removing the cgr.dev mapping must FAIL"
mutate_and_expect_fail "drop-cgr" \
  'del(.offlineMirror.hostProjects[] | select(.host == "cgr.dev"))' \
  "missing the required entry 'cgr.dev:proxy-chainguard'"

echo "[offline-mirror-host-projects] NEGATIVE 2: removing the Scarf-gateway mapping must FAIL"
mutate_and_expect_fail "drop-scarf" \
  'del(.offlineMirror.hostProjects[] | select(.host == "litmuschaos.docker.scarf.sh"))' \
  "missing the required entry 'litmuschaos.docker.scarf.sh:proxy-dockerhub'"

echo "[offline-mirror-host-projects] NEGATIVE 3: breaking the step-02 lockstep must FAIL"
mutate_and_expect_fail "drop-project" \
  'del(.harbor.mirrorProjects[] | select(. == "proxy-chainguard"))' \
  "which step-02 does NOT create"

echo "[offline-mirror-host-projects] NEGATIVE 4: ordering the Scarf gateway AHEAD of docker.io must FAIL"
mutate_and_expect_fail "scarf-first" \
  '.offlineMirror.hostProjects = [{"host":"litmuschaos.docker.scarf.sh","project":"proxy-dockerhub"}] + (.offlineMirror.hostProjects | map(select(.host != "litmuschaos.docker.scarf.sh")))' \
  "expected 'docker.io'"

echo "[offline-mirror-host-projects] NEGATIVE 5: an EMPTY render must be rejected as vacuous"
: > "${TMP}/empty.yaml"
set +e
empty_out="$(check_render "${TMP}/empty.yaml" 2>&1)"
empty_rc=$?
set -e
if [ "${empty_rc}" -eq 0 ]; then
  echo "SELF-TEST FAIL[empty-render]: the guard PASSED an EMPTY render — it is a trivially-green grep, not a guard (#5442)" >&2
  exit 1
fi
if ! printf '%s' "${empty_out}" | grep -qF "FAIL[vacuity]"; then
  echo "SELF-TEST FAIL[empty-render]: an empty render was rejected, but not by the vacuity gate:" >&2
  printf '%s\n' "${empty_out}" | sed 's/^/    /' >&2
  exit 1
fi
echo "  PASS[empty-render] — rejected by the vacuity gate:"
printf '%s\n' "${empty_out}" | sed 's/^/    /'

echo "[offline-mirror-host-projects] ALL CHECKS PASSED (positive + 5 negative)"
