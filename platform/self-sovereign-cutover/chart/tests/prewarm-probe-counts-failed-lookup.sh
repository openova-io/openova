#!/usr/bin/env bash
# The readiness probe of the registry's public name (step 03, harbor-prewarm, and the same
# probe of the secondary-region prewarm) counts a curl that ends without an HTTP answer as
# ONE not-ready probe and goes on to its bound of 40.
#
# Measured 20 Nov 2026 on a fresh two-region install (rc/20261120T181443Z-b12cae1): step 03
# lost its first two pods (20:58:05-20:58:41 and 20:58:51-20:58:56) with exit code 6 and no
# FATAL line, right after it installed its CoreDNS pin of the registry's public name; the
# last line was "public dest not ready (HTTP pin-not-served, probe 2/40)". Exit 6 is curl's
# "could not resolve host": the probe assigned `_pr_code=$(curl ...)` under `set -eu`, so a
# failed name lookup while CoreDNS reloaded ended the pod instead of counting as one probe.
#
# Every case EXECUTES the probe the chart renders (sliced out of each rendered script) under
# /bin/sh with `set -eu` and stubbed curl, getent and sleep.
#   A  curl exits 6, 7 or 28 twice, then answers 200: the probe passes at the third try and
#      the script goes on. Fails on the tree before the repair (the script ends at probe 1).
#   B  curl exits 6, 7, 28 or 35 at every probe: the step fails at the bound (exit 1 after 40
#      probes), every not-ready line and the FATAL line name the reason. Fails before the
#      repair (exit 6 after one probe, no FATAL line).
#   C  HTTP 503 at every probe: not ready, FATAL at the bound, as before the repair; the
#      FATAL line now names the last reason (fails before the repair on that line only).
#   D  HTTP 401 while this pod does not resolve the pinned address: not ready (#7900), as
#      before the repair; HTTP 401 with the pin served: ready at probe 1.
#
# Usage: bash tests/prewarm-probe-counts-failed-lookup.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
FQDN="sov.example.test"
PIN_IP="10.43.7.7"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" \
  --set secondaryRegions.prewarmSecondaryHarbor=true >"${TMP}/render.yaml" 2>"${TMP}/render.err" || {
  echo "helm template failed:"; cat "${TMP}/render.err"; exit 1
}

# extract <ConfigMap name> <data key> <file>: the rendered script.
extract() {
  yq "select(.kind == \"ConfigMap\" and .metadata.name == \"$1\") | .data[\"$2\"]" \
    "${TMP}/render.yaml" >"$3"
}
# slice <script> <file>: the function curl_no_answer when the script defines it, then the
# probe from `_pr_ok=0` to the `fi` at column 0 that closes its FATAL branch.
slice() {
  awk '$0 == "curl_no_answer() {" {f = 1} f {print} f && $0 == "}" {f = 0}' "$1" >"$2"
  awk '$0 == "_pr_ok=0" {f = 1} f {print} f && $0 == "fi" {exit}' "$1" >>"$2"
}

# The stubs. curl answers the line of ${D}/plan whose number is its call count (the last
# line once the plan is shorter): "exitN" prints 000 and exits N, anything else is printed
# as the HTTP code. getent resolves to ${D}/resolves (the pinned address by default) and
# resolves nothing at a probe whose curl could not resolve the name.
cat >"${TMP}/stubs.sh" <<'STUBS'
set -eu
HARBOR_HOST="registry.${FQDN}"
_cip="${PIN_IP}"
REGISTRY_PIN_IP="${PIN_IP}"
sleep() { :; }
plan_line() {
  _n=$(grep -c '' "${D}/calls")
  _l=$(sed -n "${_n}p" "${D}/plan")
  [ -n "${_l}" ] || _l=$(tail -n 1 "${D}/plan")
  printf '%s' "${_l}"
}
curl() {
  echo x >>"${D}/calls"
  _o=$(plan_line)
  case "${_o}" in
    exit*) printf '000'; return "${_o#exit}" ;;
    *) printf '%s' "${_o}" ;;
  esac
}
getent() {
  [ "$(plan_line)" != exit6 ] || return 2
  printf '%s      STREAM %s\n' "$(cat "${D}/resolves")" "$2"
}
STUBS

# run <slice> <dir> <plan line>...: runs the probe; the log goes to <dir>/out, the exit code
# to <dir>/rc, one line per curl call to <dir>/calls.
run() {
  local s="$1" d="$2"; shift 2
  mkdir -p "${d}"; : >"${d}/calls"; printf '%s\n' "$@" >"${d}/plan"
  [ -s "${d}/resolves" ] || echo "${PIN_IP}" >"${d}/resolves"
  D="${d}" FQDN="${FQDN}" PIN_IP="${PIN_IP}" sh -c '. "$1"; . "$2"; echo PROBE-WENT-ON' sh \
    "${TMP}/stubs.sh" "${s}" >"${d}/out" 2>&1
  echo $? >"${d}/rc"
}
calls() { grep -c '' "$1/calls"; }
lines() { grep -c -F "$2" "$1/out"; }

reason() {
  case "$1" in
    6) echo "could not resolve host, curl exit 6" ;;
    7) echo "could not connect, curl exit 7" ;;
    28) echo "timed out, curl exit 28" ;;
    35) echo "TLS not ready, curl exit 35" ;;
  esac
}

extract cutover-step-03-harbor-prewarm run.sh "${TMP}/primary.sh"
extract cutover-secondary-harbor-prewarm-script secondary-harbor-prewarm.sh "${TMP}/secondary.sh"

for which in primary secondary; do
  S="${TMP}/${which}-probe.sh"
  slice "${TMP}/${which}.sh" "${S}"
  echo "[prewarm-probe] ${which}: the readiness probe of https://registry.${FQDN}/v2/"
  if ! grep -q 'for _pr_i in $(seq 1 40); do' "${S}" || ! grep -q 'never became ready after 40 probes' "${S}"; then
    fail "${which}: could not slice the bounded readiness probe out of the rendered script"
    continue
  fi

  for code in 6 7 28; do
    why="$(reason "${code}")"
    D="${TMP}/${which}-a${code}"
    run "${S}" "${D}" "exit${code}" "exit${code}" 200
    if [ "$(cat "${D}/rc")" = 0 ] && grep -qx PROBE-WENT-ON "${D}/out"; then
      pass "A ${code}: two probes without an HTTP answer did not end the script"
    else
      fail "A ${code}: the script ended with exit code $(cat "${D}/rc") after $(calls "${D}") probe(s): $(tail -n 1 "${D}/out")"
    fi
    [ "$(lines "${D}" 'public dest ready (HTTP 200, probe 3)')" = 1 ] && [ "$(calls "${D}")" = 3 ] \
      && pass "A ${code}: ready at the third probe" \
      || fail "A ${code}: not ready at the third probe ($(calls "${D}") curl calls)"
    [ "$(lines "${D}" "public dest not ready (${why}, probe ")" = 2 ] \
      && pass "A ${code}: the two not-ready lines name '${why}'" \
      || fail "A ${code}: $(lines "${D}" "(${why}, probe ") not-ready line(s) name '${why}', want 2"
  done

  for code in 6 7 28 35; do
    why="$(reason "${code}")"
    D="${TMP}/${which}-b${code}"
    run "${S}" "${D}" "exit${code}"
    [ "$(cat "${D}/rc")" = 1 ] && [ "$(calls "${D}")" = 40 ] \
      && pass "B ${code}: the step fails at the bound (exit 1 after 40 probes)" \
      || fail "B ${code}: exit code $(cat "${D}/rc") after $(calls "${D}") probe(s), want 1 after 40"
    [ "$(lines "${D}" "public dest not ready (${why}, probe ")" = 40 ] \
      && pass "B ${code}: every not-ready line names '${why}'" \
      || fail "B ${code}: $(lines "${D}" "(${why}, probe ") not-ready line(s) name '${why}', want 40"
    [ "$(lines "${D}" "never became ready after 40 probes (10 min; last probe: ${why})")" = 1 ] \
      && pass "B ${code}: the FATAL line names '${why}'" \
      || fail "B ${code}: no FATAL line that names '${why}': $(tail -n 1 "${D}/out")"
  done

  D="${TMP}/${which}-c"
  run "${S}" "${D}" 503
  [ "$(cat "${D}/rc")" = 1 ] && [ "$(calls "${D}")" = 40 ] && [ "$(lines "${D}" "(HTTP 503, probe ")" = 40 ] \
    && pass "C: HTTP 503 counts as not ready to the bound, as before" \
    || fail "C: HTTP 503: exit code $(cat "${D}/rc") after $(calls "${D}") probe(s)"
  [ "$(lines "${D}" "never became ready after 40 probes (10 min; last probe: HTTP 503)")" = 1 ] \
    && pass "C: the FATAL line names 'HTTP 503'" \
    || fail "C: no FATAL line that names 'HTTP 503': $(tail -n 1 "${D}/out")"

  D="${TMP}/${which}-d"
  mkdir -p "${D}"; echo 198.51.100.9 >"${D}/resolves"
  run "${S}" "${D}" 401
  [ "$(cat "${D}/rc")" = 1 ] && [ "$(lines "${D}" "(HTTP pin-not-served, probe ")" = 40 ] \
    && pass "D: HTTP 401 without the pinned address is not ready (#7900)" \
    || fail "D: HTTP 401 without the pinned address: exit code $(cat "${D}/rc"), $(calls "${D}") probe(s)"
  D="${TMP}/${which}-d2"
  run "${S}" "${D}" 401
  [ "$(cat "${D}/rc")" = 0 ] && [ "$(lines "${D}" 'public dest ready (HTTP 401, probe 1)')" = 1 ] \
    && pass "D: HTTP 401 with the pinned address is ready at probe 1" \
    || fail "D: HTTP 401 with the pinned address: exit code $(cat "${D}/rc"), $(calls "${D}") probe(s)"
done

if [ "${FAILURES}" -gt 0 ]; then
  echo "[prewarm-probe] ${FAILURES} check(s) FAILED"
  exit 1
fi
echo "[prewarm-probe] all checks passed"
