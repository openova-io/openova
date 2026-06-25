#!/usr/bin/env bash
# #4347 — assert EVERY primary container in the rendered bp-grafana
# Deployments / StatefulSets / DaemonSets declares BOTH a livenessProbe AND a
# readinessProbe, so the cluster-wide Kyverno `probes-present` Enforce
# ClusterPolicy admits every workload.
#
# After #4325 bp-grafana installs into the native host namespace `grafana`
# (not a vCluster). The dashboard + datasource kiwigrid/k8s-sidecar containers
# shipped NO probes → `probes-present` (Enforce) would DENY the grafana Pod.
# This test guards the regression: it FAILS if any rendered workload container
# lacks either probe.
set -euo pipefail
chart_dir="${1:-$(dirname "$0")/..}"
cd "$chart_dir"
chart_dir="$(pwd)"
if ! ls charts/*.tgz >/dev/null 2>&1; then
  helm dependency build "$chart_dir" >/dev/null 2>&1 || true
fi
assert_py="$chart_dir/tests/_assert_probes_present.py"
out=$(helm template t . 2>/dev/null | python3 "$assert_py")
rc=$?
if [ "$rc" -ne 0 ]; then
  echo "FAIL: $out"
  echo "kyverno-probes-present: one or more workload containers lack liveness/readiness probes (would be DENIED by the host-ns Kyverno probes-present Enforce policy)."
  exit 1
fi
echo "PASS: $out"
echo "kyverno-probes-present: all Deployment/StatefulSet/DaemonSet containers declare liveness+readiness probes."
