#!/usr/bin/env bash
# #4343 — assert EVERY workload container in the rendered bp-openbao manifests
# declares resources.requests.cpu AND resources.requests.memory, so the
# cluster-wide Kyverno `resource-requests` Enforce ClusterPolicy admits every
# Pod/Deployment/StatefulSet/DaemonSet/Job/CronJob.
#
# After #4325 the platform planes install into NATIVE host namespaces (not a
# vCluster), where the host Kyverno Enforce policies apply. The upstream
# openbao subchart defaults `injector.resources: {}` (empty) → the
# agent-injector Deployment shipped with NO requests → DENIED → bp-openbao
# InstallFailed → wedged bp-external-secrets-stores → bp-sso-bridge. This test
# guards that regression: it FAILS if any rendered workload container lacks
# cpu+memory requests.
set -euo pipefail
# CI invokes `bash <script> <chart_dir>`; fall back to the script's own dir.
chart_dir="${1:-$(dirname "$0")/..}"
cd "$chart_dir"
chart_dir="$(pwd)"

# The upstream subchart is vendored under charts/*.tgz (gitignored, rebuilt
# from Chart.lock). CI runs `helm dependency build` before tests, but build it
# here too so the test is runnable standalone.
if ! ls charts/*.tgz >/dev/null 2>&1; then
  helm dependency build "$chart_dir" >/dev/null 2>&1 || true
fi

assert_py="$chart_dir/tests/_assert_resource_requests.py"

assert_requests() {
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
assert_requests "default" || fail=1

if [ "$fail" -ne 0 ]; then
  echo "kyverno-resource-requests: one or more workload containers lack resources.requests (would be DENIED by the host-ns Kyverno resource-requests Enforce policy)."
  exit 1
fi
echo "kyverno-resource-requests: all workload containers declare cpu+memory requests."
