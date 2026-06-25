#!/usr/bin/env bash
# #4347 — assert EVERY primary container in the rendered bp-harbor
# Deployments / StatefulSets / DaemonSets declares BOTH a livenessProbe AND a
# readinessProbe, so the cluster-wide Kyverno `probes-present` Enforce
# ClusterPolicy admits every workload.
#
# After #4325 the platform planes (incl. harbor) install into NATIVE host
# namespaces (not a vCluster), where the host Kyverno Enforce policies apply.
# The chart's `harbor-sso-configure` Deployment is a continuous reconcile loop
# (a `while true` shell loop) with NO serving port — it shipped with NO
# liveness/readiness probes → kyverno `probes-present` (Enforce) DENIED it →
# the bp-harbor Helm install failed → bp-harbor HR False. This test guards that
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
  out=$(helm template t . --set gateway.host=registry.example.test "$@" 2>/dev/null | python3 "$assert_py")
  rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "FAIL [$label]:"
    echo "$out" | sed 's/^/  /'
    return 1
  fi
  echo "PASS [$label]: $out"
}

fail=0
# Default render (core / jobservice / registry / portal / nginx / redis).
assert_probes "default" || fail=1
# SSO enabled (renders the harbor-sso-configure Deployment — the #4347 offender).
assert_probes "sso-enabled" --set sso.enabled=true --set sso.sovereignFqdn=example.test || fail=1

if [ "$fail" -ne 0 ]; then
  echo "kyverno-probes-present: one or more workload containers lack liveness/readiness probes (would be DENIED by the host-ns Kyverno probes-present Enforce policy)."
  exit 1
fi
echo "kyverno-probes-present: all Deployment/StatefulSet/DaemonSet containers declare liveness+readiness probes."
