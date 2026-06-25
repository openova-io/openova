#!/usr/bin/env bash
# #4343 — assert EVERY workload container in the rendered bp-harbor manifests
# declares resources.requests.cpu AND resources.requests.memory, so the
# cluster-wide Kyverno `resource-requests` Enforce ClusterPolicy admits every
# Pod/Deployment/StatefulSet/DaemonSet/Job/CronJob.
#
# After #4325 this chart installs into a NATIVE host namespace (not a vCluster)
# where the host Kyverno Enforce policies apply. The upstream goharbor subchart
# only emits `resources:` on the nginx Deployment when `nginx.resources` is set
# — left unset, harbor-nginx shipped with NO requests → DENIED → bp-harbor
# InstallFailed. This test guards that regression.
set -euo pipefail
chart_dir="${1:-$(dirname "$0")/..}"
cd "$chart_dir"
chart_dir="$(pwd)"

if ! ls charts/*.tgz >/dev/null 2>&1; then
  helm dependency build "$chart_dir" >/dev/null 2>&1 || true
fi

assert_py="$chart_dir/tests/_assert_resource_requests.py"

assert_requests() {
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
assert_requests "default" || fail=1

if [ "$fail" -ne 0 ]; then
  echo "kyverno-resource-requests: one or more workload containers lack resources.requests (would be DENIED by the host-ns Kyverno resource-requests Enforce policy)."
  exit 1
fi
echo "kyverno-resource-requests: all workload containers declare cpu+memory requests."
