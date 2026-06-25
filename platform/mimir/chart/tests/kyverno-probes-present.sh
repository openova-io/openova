#!/usr/bin/env bash
# #4347 — assert EVERY primary container of a Deployment / StatefulSet /
# DaemonSet in the rendered bp-mimir manifests declares BOTH a livenessProbe
# AND a readinessProbe, so the cluster-wide Kyverno `probes-present` Enforce
# ClusterPolicy admits every workload.
#
# After #4325 this chart installs into a NATIVE host namespace (not a vCluster)
# where the host Kyverno Enforce policies apply. The pinned mimir-distributed
# 6.0.6 chart shipped the 9 microservice components with a readinessProbe but
# NO livenessProbe (overrides-exporter was the only one with both), and the
# gateway (nginx), rollout-operator, and bundled MinIO expose NO livenessProbe
# knob at all. #4347 (a) sets `<component>.livenessProbe` on the 9 components
# and (b) disables + re-renders the gateway/rollout-operator/MinIO as
# Catalyst-owned probe-compliant equivalents in
# templates/devcluster-workloads.yaml. This test guards that whole-render
# invariant — it PASSES after the fix and FAILS if any of it is reverted.
set -euo pipefail
chart_dir="${1:-$(dirname "$0")/..}"
cd "$chart_dir"
chart_dir="$(pwd)"

# The mimir-distributed subchart .tgz must be present for the workloads to
# render; build deps if it isn't already vendored.
if ! ls charts/*.tgz >/dev/null 2>&1; then
  helm dependency build "$chart_dir" >/dev/null 2>&1 || true
fi

assert_py="$chart_dir/tests/_assert_probes_present.py"

assert_probes() {
  local label="$1"; shift
  local out rc
  out=$(helm template t . "$@" 2>/dev/null | python3 "$assert_py")
  rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "FAIL [$label]:"
    echo "$out" | sed 's/^/  /'
    return 1
  fi
  echo "PASS [$label]: $out"
}

fail=0
assert_probes "default" || fail=1

if [ "$fail" -ne 0 ]; then
  echo "kyverno-probes-present: one or more workload containers lack a livenessProbe and/or readinessProbe (would be DENIED by the host-ns Kyverno probes-present Enforce policy)."
  exit 1
fi
echo "kyverno-probes-present: all workload containers declare livenessProbe + readinessProbe."
