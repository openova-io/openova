#!/usr/bin/env bash
# #4347 — assert EVERY primary container in the rendered bp-nats-jetstream
# Deployments / StatefulSets / DaemonSets declares BOTH a livenessProbe AND a
# readinessProbe, so the cluster-wide Kyverno `probes-present` Enforce
# ClusterPolicy admits every workload.
#
# After #4325 the platform planes (incl. nats-jetstream) install into NATIVE
# host namespaces (`nats-system`, not a vCluster), where the host Kyverno
# Enforce policies apply. Four offenders the host-ns migration exposed:
#   - StatefulSet/<rel>-nats `reloader` sidecar — a config file-watcher with no
#     serving port, shipped with NO probes;
#   - Deployment/<rel>-nats-box `nats-box` — a `sleep infinity` debug toolbox
#     with no serving port, shipped with NO probes;
#   - Deployment/<rel> `jsc` (the nack jetstream-controller) — the nack 0.29.0
#     Deployment template had NO probe hook at all (fixed by the 0.34.0 bump +
#     `nack.jetstream.controlLoop: true`, which renders HTTP /healthz + /readyz
#     on :8081);
#   - (the nats SERVER container already ships HTTP probes from upstream when
#     `config.monitor.enabled: true`, the default — it was never an offender.)
# Each missing-probe container would be DENIED by `probes-present` (Enforce) →
# bp-nats-jetstream InstallFailed → wedged bp-catalyst-platform (NATS is the
# control-plane event spine). This test guards that regression: it FAILS if any
# rendered Deployment/StatefulSet/DaemonSet container lacks either probe.
set -euo pipefail
# CI invokes `bash <script> <chart_dir>`; fall back to the script's own dir.
chart_dir="${1:-$(dirname "$0")/..}"
cd "$chart_dir"
chart_dir="$(pwd)"

# The upstream nats + nack subcharts are vendored under charts/*.tgz (gitignored,
# rebuilt from Chart.lock). CI runs `helm dependency build` before tests, but
# build it here too so the test is runnable standalone.
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
# Default render (server StatefulSet + reloader sidecar + nats-box + jsc).
assert_probes "default" || fail=1
# Catalyst streams enabled (also renders the JetStream CRs; must not regress the
# workload probe set).
assert_probes "streams-enabled" --set catalystStreams.streamsEnabled=true || fail=1

if [ "$fail" -ne 0 ]; then
  echo "kyverno-probes-present: one or more workload containers lack liveness/readiness probes (would be DENIED by the host-ns Kyverno probes-present Enforce policy)."
  exit 1
fi
echo "kyverno-probes-present: all Deployment/StatefulSet/DaemonSet containers declare liveness+readiness probes."
