#!/usr/bin/env bash
# #4347 — assert EVERY primary container in the rendered bp-seaweedfs
# Deployments / StatefulSets / DaemonSets declares BOTH a livenessProbe AND a
# readinessProbe (the upstream seaweedfs chart already provides both; this is
# the regression guard) so the cluster-wide Kyverno `probes-present` Enforce
# ClusterPolicy admits every workload after #4325 re-homed bp-seaweedfs into
# the native host namespace `seaweedfs`.
set -euo pipefail
chart_dir="${1:-$(dirname "$0")/..}"
cd "$chart_dir"
chart_dir="$(pwd)"
if ls Chart.lock >/dev/null 2>&1 && ! ls charts/*.tgz >/dev/null 2>&1; then
  helm dependency build "$chart_dir" >/dev/null 2>&1 || true
fi
assert_py="$chart_dir/tests/_assert_probes_present.py"
out=$(helm template t . 2>/dev/null | python3 "$assert_py")
rc=$?
if [ "$rc" -ne 0 ]; then
  echo "FAIL: $out"
  echo "kyverno-probes-present: one or more workload containers lack liveness/readiness probes."
  exit 1
fi
echo "PASS: $out"
echo "kyverno-probes-present: all Deployment/StatefulSet/DaemonSet containers declare liveness+readiness probes."
