#!/usr/bin/env bash
# #4347 — assert EVERY primary container in the rendered bp-openbao
# Deployments / StatefulSets / DaemonSets declares BOTH a livenessProbe AND a
# readinessProbe, so the cluster-wide Kyverno `probes-present` Enforce
# ClusterPolicy admits every workload.
#
# After #4325 the platform planes (incl. openbao) install into NATIVE host
# namespaces (not a vCluster), where the host Kyverno Enforce policies apply.
# Two offenders the host-ns migration exposed:
#   - the openbao-sso-configure Deployment (a continuous reconcile loop with NO
#     serving port) shipped with NO probes;
#   - the upstream openbao subchart ships `server.readinessProbe.enabled: true`
#     but `server.livenessProbe.enabled: false`, so the server StatefulSet had
#     a readinessProbe but NO livenessProbe.
# Both were DENIED by `probes-present` (Enforce) → bp-openbao InstallFailed →
# wedged bp-external-secrets-stores → bp-sso-bridge. This test guards that
# regression: it FAILS if any rendered Deployment/StatefulSet/DaemonSet
# container lacks either probe.
set -euo pipefail
chart_dir="${1:-$(dirname "$0")/..}"
cd "$chart_dir"
chart_dir="$(pwd)"

if ! ls charts/*.tgz >/dev/null 2>&1; then
  helm dependency build "$chart_dir" >/dev/null 2>&1 || true
fi

assert_py="$chart_dir/tests/_assert_probes_present.py"

assert_probes() {
  local label="$1"; shift
  local out rc
  out=$(helm template t . --set gateway.host=bao.example.test "$@" 2>/dev/null | python3 "$assert_py")
  rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "FAIL [$label]:"
    echo "$out" | sed 's/^/  /'
    return 1
  fi
  echo "PASS [$label]: $out"
}

fail=0
# Default render (server StatefulSet + agent-injector + sso-landing).
assert_probes "default" || fail=1
# SSO enabled (renders the openbao-sso-configure Deployment — a #4347 offender).
assert_probes "sso-enabled" --set sso.enabled=true --set sso.sovereignFqdn=example.test || fail=1

if [ "$fail" -ne 0 ]; then
  echo "kyverno-probes-present: one or more workload containers lack liveness/readiness probes (would be DENIED by the host-ns Kyverno probes-present Enforce policy)."
  exit 1
fi
echo "kyverno-probes-present: all Deployment/StatefulSet/DaemonSet containers declare liveness+readiness probes."
