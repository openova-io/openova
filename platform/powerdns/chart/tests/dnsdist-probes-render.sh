#!/usr/bin/env bash
# bp-powerdns: the dnsdist workload declares a liveness and a readiness probe.
#
# The ClusterPolicy probes-present (platform/kyverno-policies, action Enforce
# once the bootstrap window closes) denies every create or change of a
# Deployment, StatefulSet or DaemonSet unless each container declares both
# probes. dnsdist had none: it was admitted at the fresh install, while the
# policy still audited, and every later upgrade of bp-powerdns was refused
# ("DaemonSet/dnsdist must declare both livenessProbe and readinessProbe"), so
# the release rolled back and the charts that wait on it waited for ever.
#
# Cases, on the default render (Deployment) and on the hostPort-53 render
# (DaemonSet):
#   1. the policy's own predicate: the number of containers that declare a
#      livenessProbe, and the number that declare a readinessProbe, each equal
#      the number of containers.
#   2. both probes of the dnsdist container are TCP connects to a port of the
#      container whose containerPort is 53 and whose protocol is TCP (dnsdist's
#      own DNS listener; a TCP probe on the UDP port could never succeed).
#   3. neither probe names a host: kubelet dials the pod's own address, where
#      dnsdist listens in both modes (the hostPort maps the node's port 53 to
#      that same socket; the pod has no hostNetwork).
#   4. the timings come from values (dnsdist.probes.*).
#   5. the predicate of case 1 fails on the same workload with its
#      livenessProbe removed, and with its readinessProbe removed (the check
#      can fail).
#
# CI runs every chart/tests/*.sh as `bash <script> <chart dir>` under the yq of
# the release (v4.44.3); only plain selections and index access are used.
set -euo pipefail

chart_dir="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
helm="${HELM_BIN:-helm}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL: $1" >&2; exit 1; }

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

# workload <out> [helm args...] writes the dnsdist Deployment or DaemonSet, one document.
workload() {
  local out="$1"
  shift
  "$helm" template smoke "$chart_dir" --show-only templates/dnsdist.yaml "$@" > "$tmp/render.yaml"
  yq 'select(.kind == "Deployment" or .kind == "DaemonSet")' "$tmp/render.yaml" > "$out"
  [ "$(yq '.metadata.name' "$out")" = "dnsdist" ] || fail "no dnsdist workload in the render ($*)"
}

# probes_declared <workload file> exits 0 when every container declares both probes.
probes_declared() {
  local total live ready
  total="$(yq '.spec.template.spec.containers | length' "$1")"
  live="$(yq '[.spec.template.spec.containers[] | select(has("livenessProbe"))] | length' "$1")"
  ready="$(yq '[.spec.template.spec.containers[] | select(has("readinessProbe"))] | length' "$1")"
  [ "$total" -gt 0 ] && [ "$live" = "$total" ] && [ "$ready" = "$total" ]
}

# check_probe <label> <workload file> <livenessProbe|readinessProbe>
check_probe() {
  local label="$1" wl="$2" probe="$3" port container_port protocol host
  port="$(yq ".spec.template.spec.containers[] | select(.name == \"dnsdist\") | .$probe.tcpSocket.port" "$wl")"
  [ -n "$port" ] && [ "$port" != "null" ] || fail "$label: the $probe of dnsdist is not a tcpSocket probe"
  if [ "$port" = "53" ]; then
    container_port=53
    protocol="$(yq '.spec.template.spec.containers[] | select(.name == "dnsdist") | .ports[] | select(.containerPort == 53 and .protocol == "TCP") | .protocol' "$wl")"
  else
    container_port="$(N="$port" yq '.spec.template.spec.containers[] | select(.name == "dnsdist") | .ports[] | select(.name == strenv(N)) | .containerPort' "$wl")"
    protocol="$(N="$port" yq '.spec.template.spec.containers[] | select(.name == "dnsdist") | .ports[] | select(.name == strenv(N)) | .protocol' "$wl")"
  fi
  [ "$container_port" = "53" ] || fail "$label: the $probe of dnsdist dials port '$port', which is not the container's port 53"
  [ "$protocol" = "TCP" ] || fail "$label: the $probe of dnsdist dials port '$port', whose protocol is '$protocol', not TCP"
  host="$(yq ".spec.template.spec.containers[] | select(.name == \"dnsdist\") | .$probe.tcpSocket.host" "$wl")"
  [ "$host" = "null" ] || fail "$label: the $probe of dnsdist names host '$host'; kubelet must dial the pod's own address"
}

check_render() {
  local label="$1" wl="$tmp/$1-workload.yaml"
  shift
  echo "[bp-powerdns] dnsdist probes, $label render"
  workload "$wl" "$@"

  # Case 1
  probes_declared "$wl" \
    || fail "$label: $(yq '.kind' "$wl")/dnsdist has a container without a livenessProbe or a readinessProbe (probes-present denies it)"

  # Case 2 and 3
  check_probe "$label" "$wl" livenessProbe
  check_probe "$label" "$wl" readinessProbe

  # Case 5
  yq 'del(.spec.template.spec.containers[0].livenessProbe)' "$wl" > "$tmp/$label-no-live.yaml"
  yq 'del(.spec.template.spec.containers[0].readinessProbe)' "$wl" > "$tmp/$label-no-ready.yaml"
  if probes_declared "$tmp/$label-no-live.yaml" || probes_declared "$tmp/$label-no-ready.yaml"; then
    fail "$label: the probe check passed on a workload with a probe removed (it cannot fail)"
  fi
  echo "  ok: $(yq '.kind' "$wl") dnsdist, both probes tcpSocket on the TCP port 53"
}

check_render default
check_render hostport --set dnsdist.hostPortMode=true

# Case 4
workload "$tmp/timings.yaml" --set dnsdist.probes.liveness.periodSeconds=37 --set dnsdist.probes.readiness.failureThreshold=7
[ "$(yq '.spec.template.spec.containers[0].livenessProbe.periodSeconds' "$tmp/timings.yaml")" = "37" ] \
  || fail "dnsdist.probes.liveness.periodSeconds does not reach the livenessProbe"
[ "$(yq '.spec.template.spec.containers[0].readinessProbe.failureThreshold' "$tmp/timings.yaml")" = "7" ] \
  || fail "dnsdist.probes.readiness.failureThreshold does not reach the readinessProbe"
echo "  ok: the probe timings come from dnsdist.probes"

echo "[bp-powerdns] dnsdist probes render: PASS"
