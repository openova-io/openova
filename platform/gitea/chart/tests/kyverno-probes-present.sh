#!/usr/bin/env bash
# #4347 — assert EVERY primary container in the rendered bp-gitea
# Deployments / StatefulSets / DaemonSets declares BOTH a livenessProbe AND a
# readinessProbe, so the cluster-wide Kyverno `probes-present` Enforce
# ClusterPolicy admits every workload.
#
# After #4325 the platform planes (incl. gitea) install into NATIVE host
# namespaces (not a vCluster), where the host Kyverno Enforce policies apply.
# The chart's `gitea-sso-configure` Deployment is a continuous reconcile loop
# (a `while true` bash loop) with NO serving port — it shipped with NO
# liveness/readiness probes → kyverno `probes-present` (Enforce) DENIED the
# Deployment at admission → the whole bp-gitea Helm install FAILED → bp-gitea
# HR False → bp-catalyst-platform `dependency bp-gitea not ready`. This test
# guards that regression: it FAILS if any rendered Deployment/StatefulSet/
# DaemonSet container lacks either probe.
set -euo pipefail
# CI invokes `bash <script> <chart_dir>`; fall back to the script's own dir.
chart_dir="${1:-$(dirname "$0")/..}"
cd "$chart_dir"
chart_dir="$(pwd)"

# The upstream gitea subchart is vendored under charts/*.tgz (gitignored,
# rebuilt from Chart.lock). CI runs `helm dependency build` before tests, but
# build it here too so the test is runnable standalone.
if ! ls charts/*.tgz >/dev/null 2>&1; then
  helm dependency build "$chart_dir" >/dev/null 2>&1 || true
fi

assert_py="$chart_dir/tests/_assert_probes_present.py"

assert_probes() {
  local label="$1"; shift
  local out rc
  out=$(helm template t . --set gateway.host=git.example.test "$@" 2>/dev/null | python3 "$assert_py")
  rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "FAIL [$label]:"
    echo "$out" | sed 's/^/  /'
    return 1
  fi
  echo "PASS [$label]: $out"
}

fail=0
# Default render.
assert_probes "default" || fail=1
# SSO enabled (renders the gitea-sso-configure Deployment — the #4347 offender).
assert_probes "sso-enabled" --set sso.enabled=true --set sso.sovereignFqdn=example.test || fail=1

if [ "$fail" -ne 0 ]; then
  echo "kyverno-probes-present: one or more workload containers lack liveness/readiness probes (would be DENIED by the host-ns Kyverno probes-present Enforce policy)."
  exit 1
fi
echo "kyverno-probes-present: all Deployment/StatefulSet/DaemonSet containers declare liveness+readiness probes."
