#!/usr/bin/env bash
# #4347 — assert EVERY primary container in the rendered bp-loki
# Deployments / StatefulSets / DaemonSets declares BOTH a livenessProbe AND a
# readinessProbe, so the cluster-wide Kyverno `probes-present` Enforce
# ClusterPolicy admits every workload.
#
# After #4325 the platform planes (incl. loki) install into NATIVE host
# namespaces (not a vCluster), where the host Kyverno Enforce policies apply.
# Two offenders: the upstream loki chart shipped `loki.livenessProbe: {}`
# (empty) so the SingleBinary StatefulSet's loki container had readiness but NO
# liveness; the loki-sc-rules kiwigrid/k8s-sidecar shipped NO probes at all.
# Both were DENIED by `probes-present` (Enforce). This test guards the
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
out=$(helm template t . 2>/dev/null | python3 "$assert_py")
rc=$?
if [ "$rc" -ne 0 ]; then
  echo "FAIL: $out"
  echo "kyverno-probes-present: one or more workload containers lack liveness/readiness probes (would be DENIED by the host-ns Kyverno probes-present Enforce policy)."
  exit 1
fi
echo "PASS: $out"
echo "kyverno-probes-present: all Deployment/StatefulSet/DaemonSet containers declare liveness+readiness probes."
