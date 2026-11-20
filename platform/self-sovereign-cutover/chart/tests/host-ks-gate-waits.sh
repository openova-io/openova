#!/usr/bin/env bash
# The #3695 host GitOps loop gate of step egress-block-test waits a BOUNDED time for
# a Kustomization that is still progressing or whose health check timed out, fails at
# once on one that failed for any other reason, and names the HelmReleases that hold it.
#
# Why: on hw307 region a (19 Nov 2026) the gate read the kit's Kustomization once,
# found "Ready=Unknown: Reconciliation in progress" (a restarted helm-controller was
# re-running bp-sso-bridge's Helm test) and failed the step; the retried step rolled
# the Flux controllers again. A progressing Kustomization is not a corrupted Git loop;
# one still not Ready after the bound is. The kit runs with wait: true and timeout: 5m,
# so while a release still runs its test the kit does not stay Ready=Unknown: its
# health check times out, kustomize-controller marks it Ready=False/HealthCheckFailed
# and retries one interval (5m) later. The gate must wait through that too.
#
# Evidence, each part able to fail: the gate's three functions are cut out of the
# RENDERED step and run under sh against a stand-in kubectl and a stand-in clock; the
# stand-in Kustomization's Ready condition (status, reason, message) follows the clock:
#   (a) progressing, then Ready: passes, after waiting;
#   (e) the real sequence of the kit under wait: true, timeout: 5m, interval: 5m:
#       Unknown (health check running) for 5 min, then False/HealthCheckFailed until
#       the retry 5 min later, then Unknown, then True: passes within the default
#       bound, and fails with the former bound of 600 s, which ends before the retry;
#   (b) never Ready (that cycle for ever): fails once the bound has passed (not before,
#       and not more than one poll after), naming bp-sso-bridge (Reconciling) and
#       bp-powerdns-admin (not Ready) and no release outside the Kustomization's
#       inventory nor a settled one;
#   (c) Ready=False/BuildFailed: fails at once, without waiting, naming the holder;
#   (f) no Ready condition: fails at once, without waiting;
#   (d) the rendered step calls the wait, the bound carries the default of
#       values.yaml (900 s), and it reads every 15 s.
#
# Usage: bash tests/host-ks-gate-waits.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

helm template ssc "${CHART_DIR}" --set sovereign.fqdn=t01.omani.works >"${TMP}/render.yaml" 2>"${TMP}/render.err" || {
  echo "helm template failed:"; cat "${TMP}/render.err"; exit 1
}

# Cut a shell function out of the render: a one-line "<name>() { ...; }", or from
# "<name>() {" to the "}" at its indent.
extract() {
  awk -v fn="$1" '
    !on && $0 ~ ("^ *" fn "\\(\\) \\{ .*\\}$") { sub(/^ */, ""); print; exit }
    !on && $0 ~ ("^ *" fn "\\(\\) \\{$") { on = 1; match($0, /^ */); ind = RLENGTH; close_line = substr($0, 1, ind) "}" }
    on { print substr($0, ind + 1) }
    on && $0 == close_line { exit }
  ' "${TMP}/render.yaml"
}
: >"${TMP}/gate.sh"
for fn in host_ks_ready host_ks_holders host_ks_wait_ready; do
  extract "${fn}" >"${TMP}/${fn}.sh"
  if [ ! -s "${TMP}/${fn}.sh" ]; then
    echo "  FAIL — the rendered step holds no function ${fn}"; exit 1
  fi
  cat "${TMP}/${fn}.sh" >>"${TMP}/gate.sh"
done

echo "[host-ks-gate] (d) the rendered step calls the bounded wait with the defaults of values.yaml"
grep -q -F 'if ! host_ks_wait_ready; then' "${TMP}/render.yaml" && pass "the gate calls host_ks_wait_ready" || fail "the gate does not call host_ks_wait_ready"
grep -A1 -F -- '- name: HOST_KS_READY_WAIT_SECONDS' "${TMP}/render.yaml" | grep -q -F 'value: "900"' \
  && pass "HOST_KS_READY_WAIT_SECONDS is 900" || fail "HOST_KS_READY_WAIT_SECONDS is not rendered as 900"
grep -q -F 'sleep "${HOST_KS_POLL_SECONDS:-15}"' "${TMP}/render.yaml" \
  && pass "it reads every 15 s" || fail "the wait does not read every 15 s"

# The stand-ins. The Ready condition of bootstrap-kit follows FIXTURE and the seconds
# since the gate started (E); sovereign-tls is always Ready.
cat >"${TMP}/harness.sh" <<'HARNESS'
. "${GATE}"
NOW=1000
SLEPT=0
date() { echo "${NOW}"; }
sleep() { NOW=$((NOW + $1)); SLEPT=$((SLEPT + 1)); }
RUNNING='Running health checks for revision main@sha1:0123 with a timeout of 5m0s'
TIMED_OUT="health check failed after 5m0s: timeout waiting for: [HelmRelease/flux-system/bp-sso-bridge status: 'InProgress']"
cond() { S="$1"; R="$2"; M="$3"; }
kit_ready() {
  E=$((NOW - 1000))
  case "${FIXTURE}" in
    then-ready) if [ "${E}" -lt 45 ]; then cond Unknown Progressing "${RUNNING}"; else cond True ReconciliationSucceeded 'Applied revision: main@sha1:0123'; fi ;;
    healthcheck)
      if [ "${E}" -lt 300 ]; then cond Unknown Progressing "${RUNNING}"
      elif [ "${E}" -lt 600 ]; then cond False HealthCheckFailed "${TIMED_OUT}"
      elif [ "${E}" -lt 630 ]; then cond Unknown Progressing 'Reconciliation in progress'
      else cond True ReconciliationSucceeded 'Applied revision: main@sha1:0123'; fi ;;
    forever) if [ $((E % 600)) -lt 300 ]; then cond Unknown Progressing "${RUNNING}"; else cond False HealthCheckFailed "${TIMED_OUT}"; fi ;;
    failed) cond False BuildFailed 'kustomize build failed: map[string]interface {}(nil): yaml: duplicate key values' ;;
    missing) cond '' '' '' ;;
  esac
}
kubectl() {
  for _a in "$@"; do _last="${_a}"; done
  _field="${_last##*].}"; _field="${_field%\}}"
  case "$*" in
    "get kustomization bootstrap-kit "*'(@.type=="Ready")].'*)
      kit_ready
      case "${_field}" in status) printf '%s' "${S}" ;; reason) printf '%s' "${R}" ;; message) printf '%s' "${M}" ;; esac ;;
    "get kustomization sovereign-tls "*'(@.type=="Ready")].'*)
      case "${_field}" in status) printf 'True' ;; reason) printf 'ReconciliationSucceeded' ;; message) printf 'Applied revision: main@sha1:0123' ;; esac ;;
    *lastAppliedRevision*) printf 'main@sha1:0123' ;;
    "get kustomization bootstrap-kit "*inventory*)
      printf '%s\n' flux-system_bp-sso-bridge_helm.toolkit.fluxcd.io_HelmRelease \
        flux-system_bp-powerdns-admin_helm.toolkit.fluxcd.io_HelmRelease \
        flux-system_bp-cilium_helm.toolkit.fluxcd.io_HelmRelease \
        flux-system_sovereign-object_batch_Job ;;
    "get helmreleases.helm.toolkit.fluxcd.io -A "*)
      printf '%s\n' "flux-system/bp-sso-bridge|True|True|Running 'test' action with timeout of 5m0s|Helm upgrade succeeded" \
        "flux-system/bp-powerdns-admin|False|||dependency 'flux-system/bp-sso-bridge' is not ready" \
        "flux-system/bp-cilium|True|||Helm install succeeded" \
        "other/bp-outside-the-inventory|False|||not of this Kustomization" ;;
    *) echo "unexpected kubectl $*" >&2; return 1 ;;
  esac
}
HOST_KS_NAMESPACE=flux-system
HOST_KS_NAMES="bootstrap-kit sovereign-tls"
host_ks_wait_ready
rc=$?
echo "RESULT rc=${rc} slept=${SLEPT} elapsed=$((NOW - 1000)) bad=[${host_ks_bad}]"
HARNESS

# run <name> <fixture> <bound> <poll> -> output in ${TMP}/<name>.out
run() {
  FIXTURE="$2" GATE="${TMP}/gate.sh" HOST_KS_READY_WAIT_SECONDS="$3" HOST_KS_POLL_SECONDS="$4" \
    sh "${TMP}/harness.sh" >"${TMP}/$1.out" 2>&1
}
has() { grep -q -F -- "$2" "${TMP}/$1.out"; }

echo "[host-ks-gate] (a) progressing, then Ready: passes after waiting"
run then-ready then-ready 900 15
has then-ready "RESULT rc=0 slept=3 elapsed=45" && pass "Ready=True on the fourth reading, after three waits of 15 s" || fail "progressing-then-ready did not pass after three waits: $(tail -1 "${TMP}/then-ready.out")"
has then-ready "WAIT — kustomization/bootstrap-kit Ready=Unknown (Progressing): Running health checks" && pass "each wait names the progressing Kustomization, its reason and its message" || fail "no WAIT line names the progressing Kustomization"
has then-ready "OK — kustomization/bootstrap-kit Ready=True (lastAppliedRevision=main@sha1:0123)" && has then-ready "OK — kustomization/sovereign-tls Ready=True" \
  && pass "every Kustomization is reported Ready" || fail "the Ready Kustomizations are not reported"

echo "[host-ks-gate] (e) Unknown, then False/HealthCheckFailed until the retry, then Unknown, then True: passes within the bound"
run healthcheck healthcheck 900 15
has healthcheck "RESULT rc=0 slept=42 elapsed=630" && pass "Ready=True after the kit's retry (630 s of 900 s)" || fail "the health-check-timeout sequence did not pass within the bound: $(tail -1 "${TMP}/healthcheck.out")"
has healthcheck "WAIT — kustomization/bootstrap-kit Ready=False (HealthCheckFailed): health check failed after 5m0s" \
  && pass "Ready=False/HealthCheckFailed is waited for, and the wait says so" || fail "no WAIT line for Ready=False/HealthCheckFailed"
run healthcheck-600 healthcheck 600 15
has healthcheck-600 "RESULT rc=1" && pass "the former bound of 600 s fails the same sequence (it ends before the retry)" \
  || fail "a bound of 600 s did not fail the sequence, so this case does not test the bound: $(tail -1 "${TMP}/healthcheck-600.out")"

echo "[host-ks-gate] (b) never Ready: fails once the bound has passed, naming the holders"
run forever forever 900 15
if has forever "RESULT rc=1"; then pass "a Kustomization never Ready fails"; else fail "a Kustomization never Ready did not fail: $(tail -1 "${TMP}/forever.out")"; fi
elapsed="$(sed -n 's/^RESULT .*elapsed=\([0-9]*\).*/\1/p' "${TMP}/forever.out")"
if [ -n "${elapsed}" ] && [ "${elapsed}" -ge 900 ] && [ "${elapsed}" -le 915 ]; then
  pass "it waited the bound and at most one poll more (${elapsed} s of 900 s)"
else
  fail "it did not wait the bound of 900 s within one poll (waited ${elapsed:-?} s)"
fi
has forever "FAIL — still not Ready after 900s — kustomization/bootstrap-kit Ready=False (HealthCheckFailed): health check failed after 5m0s" \
  && pass "the failure names the Kustomization, its reason and the bound" || fail "the failure does not name the Kustomization, its reason and the bound"
has forever "held by HelmRelease/flux-system/bp-sso-bridge: Reconciling: Running 'test' action with timeout of 5m0s" \
  && pass "it names the release that holds it (bp-sso-bridge, reconciling its test)" || fail "bp-sso-bridge is not named as the holder"
has forever "held by HelmRelease/flux-system/bp-powerdns-admin: Ready=False: dependency 'flux-system/bp-sso-bridge' is not ready" \
  && pass "it names the release waiting on it (bp-powerdns-admin)" || fail "bp-powerdns-admin is not named"
if has forever "bp-cilium" || has forever "bp-outside-the-inventory"; then
  fail "a settled release or one outside the Kustomization's inventory is named"
else
  pass "no settled release and none outside the inventory is named"
fi
has forever "bad=[ bootstrap-kit]" && pass "the step's verdict line receives the Kustomization" || fail "host_ks_bad does not hold bootstrap-kit"

echo "[host-ks-gate] (c) Ready=False/BuildFailed: fails at once"
run failed failed 900 15
has failed "RESULT rc=1 slept=0 elapsed=0" && pass "a build failure fails at the first reading, without waiting" || fail "a build failure did not fail at once: $(tail -1 "${TMP}/failed.out")"
has failed "FAIL — kustomization/bootstrap-kit Ready=False (BuildFailed): kustomize build failed" && pass "the failure carries the Kustomization's reason and message" || fail "the failure does not carry the reason and message"
has failed "held by HelmRelease/flux-system/bp-sso-bridge" && pass "the failure names the holder" || fail "the failure does not name the holder"

echo "[host-ks-gate] (f) no Ready condition: fails at once"
run missing missing 900 15
has missing "RESULT rc=1 slept=0 elapsed=0" && pass "a Kustomization with no Ready condition fails at the first reading" || fail "a missing Ready condition did not fail at once: $(tail -1 "${TMP}/missing.out")"
has missing "FAIL — kustomization/bootstrap-kit Ready=<missing>" && pass "the failure says Ready is missing" || fail "the failure does not say Ready is missing"

if [ "${FAILURES}" -gt 0 ]; then
  echo "host-ks-gate-waits: ${FAILURES} check(s) failed"
  for f in then-ready healthcheck healthcheck-600 forever failed missing; do echo "--- ${f}"; cat "${TMP}/${f}.out" 2>/dev/null; done
  exit 1
fi
echo "host-ks-gate-waits: all checks passed"
