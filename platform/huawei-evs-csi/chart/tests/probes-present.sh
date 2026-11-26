#!/usr/bin/env bash
# probes-present.sh - every container of the EVS CSI driver declares a
# livenessProbe and a readinessProbe (CMP-067). The policy probes-present
# enforces after the bootstrap window and refuses a DaemonSet or a Deployment
# with a container that lacks either; tools/guards/cmd/kit-policies judges the
# whole kit, this test holds the chart by itself.
#
# It renders the chart twice, as kit slot 55b enables it (enabled=true, no
# snapshot class) and with the snapshot class on (the csi-snapshotter sidecar),
# and compares, for the DaemonSet csi-evs-node and the Deployment
# csi-evs-controller, the names of their containers with the expected list and
# the names of the containers without both probes with the empty list. A probe
# counts when it is a non-empty object, as the policy counts it.
#
# Usage:
#   bash probes-present.sh           the chart above this file
#   bash probes-present.sh <chart>   that chart directory
#
# Exit code: 0 pass. 1 a render failed or a list differs.
# Needs helm and yq (mikefarah v4).
set -euo pipefail
export LC_ALL=C

CHART="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
TAG="[probes-present]"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
rc=0

# names <render> <kind> <name> <filter>: the container names of that workload,
# one line, space separated; the filter selects containers.
names() {
  K="$2" N="$3" yq ea -N "[select(.kind == strenv(K) and .metadata.name == strenv(N))] | .[0] | .spec.template.spec.containers[] | $4 | .name" "$1" | tr '\n' ' ' | sed 's/ $//'
}

# check <label> <render> <kind> <name> <expected container names>
check() {
  local got lacking
  got="$(names "$2" "$3" "$4" '.')"
  # A missing probe is read as {} (yq gives a missing key no length to compare).
  lacking="$(names "$2" "$3" "$4" 'select(((.livenessProbe // {}) | length) == 0 or ((.readinessProbe // {}) | length) == 0)')"
  if [ "$got" != "$5" ]; then
    echo "$TAG FAIL $1: $3/$4 has the containers '$got', want '$5'"
    rc=1
  elif [ -n "$lacking" ]; then
    echo "$TAG FAIL $1: in $3/$4 these containers lack a livenessProbe or a readinessProbe: $lacking"
    rc=1
  else
    echo "$TAG PASS $1: every container of $3/$4 ($got) declares both probes"
  fi
}

render() {
  if ! helm template t "$CHART" --set enabled=true "$@" > "$TMP/render.yaml" 2> "$TMP/err"; then
    cat "$TMP/err"
    echo "$TAG FAIL: the chart does not render with $*"
    exit 1
  fi
}

render --set volumeSnapshotClass.enabled=false
check "slot 55b" "$TMP/render.yaml" DaemonSet csi-evs-node "evs-driver-registrar liveness-probe evs-csi-plugin"
check "slot 55b" "$TMP/render.yaml" Deployment csi-evs-controller "csi-provisioner csi-attacher csi-resizer liveness-probe evs-csi-plugin"
render --set volumeSnapshotClass.enabled=true
check "snapshot class on" "$TMP/render.yaml" Deployment csi-evs-controller "csi-provisioner csi-attacher csi-resizer csi-snapshotter liveness-probe evs-csi-plugin"
exit "$rc"
