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
# A probe that is present can still never pass. csi-node-driver-registrar
# v2.4.0 serves /healthz on no port (it registers the handler on a private
# ServeMux and serves the default one), so its probe is the exec mode
# kubelet-registration-probe, which passes while the file
# <dir of --kubelet-registration-path>/registration exists; the registrar
# writes that file, and under a read-only root file system it can only write it
# into a mounted directory. The registrar case compares path values: the
# registration path the registrar runs with (its argument, resolved through its
# env), the path each exec probe names, and the mount at that directory, which
# must be writable and the node's own directory (a hostPath of the same path).
# It runs with the default kubelet directory and with another one. It cannot
# prove that the kubelet calls GetInfo, that the file is written or that the
# probe exits 0: that needs the container running on a node.
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

# registrar <render> <expression>: the expression on the container
# node-driver-registrar of the DaemonSet csi-evs-node.
registrar() {
  yq ea -N "[select(.kind == \"DaemonSet\" and .metadata.name == \"csi-evs-node\")] | .[0] | .spec.template.spec.containers[] | select(.name == \"node-driver-registrar\") | $2" "$1"
}

# registrar_probe_file <label> <render> <kubelet dir>
registrar_probe_file() {
  local r="$2" want="$3/plugins/evs.csi.huaweicloud.com/csi.sock" arg reg dir n bad=0 vol host ro kind f
  arg="$(registrar "$r" '.args[] | select(test("^--kubelet-registration-path="))')"
  reg="${arg#--kubelet-registration-path=}"
  case "$reg" in
    '$('*')') n=${reg#\$\(}; n=${n%\)}
      reg="$(registrar "$r" ".env[] | select(.name == \"$n\") | .value")" ;;
  esac
  if [ "$reg" != "$want" ]; then
    echo "$TAG FAIL $1: the registrar runs with the registration path '$reg', want '$want'"
    rc=1
    return
  fi
  dir="${reg%/*}"
  for kind in livenessProbe readinessProbe; do
    f="$TMP/$kind.cmd"
    : > "$f"
    if [ "$(registrar "$r" ".$kind.exec.command | tag")" = "!!seq" ]; then
      registrar "$r" ".$kind.exec.command[]" > "$f"
    fi
    if [ "$(registrar "$r" ".$kind.exec.command[0]")" != "/csi-node-driver-registrar" ] ||
       ! grep -Fxq -- "--mode=kubelet-registration-probe" "$f" ||
       ! grep -Fxq -- "--kubelet-registration-path=$reg" "$f"; then
      echo "$TAG FAIL $1: the registrar's $kind is not the exec probe kubelet-registration-probe with --kubelet-registration-path=$reg (it runs: $(tr '\n' ' ' < "$f"))"
      bad=1
    fi
  done
  vol="$(registrar "$r" ".volumeMounts[] | select(.mountPath == \"$dir\" or .mountPath == \"$dir/\") | .name")"
  ro="$(registrar "$r" ".volumeMounts[] | select(.mountPath == \"$dir\" or .mountPath == \"$dir/\") | .readOnly")"
  host="$(V="$vol" yq ea -N '[select(.kind == "DaemonSet" and .metadata.name == "csi-evs-node")] | .[0] | .spec.template.spec.volumes[] | select(.name == strenv(V)) | .hostPath.path' "$r")"
  if [ -z "$vol" ]; then
    echo "$TAG FAIL $1: no volume of the registrar is mounted at $dir, where it writes its registration probe file $dir/registration"
    bad=1
  elif [ "$ro" = "true" ] || [ "${host%/}" != "$dir" ]; then
    echo "$TAG FAIL $1: the mount at $dir is '$vol' (readOnly '$ro', hostPath '$host'), want a writable mount of the hostPath $dir"
    bad=1
  fi
  if [ "$(registrar "$r" '.securityContext.readOnlyRootFilesystem')" != "true" ]; then
    echo "$TAG FAIL $1: the registrar's root file system is not read-only"
    bad=1
  fi
  if [ "$bad" -eq 0 ]; then
    echo "$TAG PASS $1: the registrar's exec probes read $dir/registration and $dir is the node's directory, mounted writable"
  else
    rc=1
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
check "slot 55b" "$TMP/render.yaml" DaemonSet csi-evs-node "node-driver-registrar liveness-probe evs-csi-plugin"
registrar_probe_file "slot 55b" "$TMP/render.yaml" /var/lib/kubelet
render --set volumeSnapshotClass.enabled=false --set kubeletDir=/var/lib/k0s/kubelet
registrar_probe_file "kubeletDir /var/lib/k0s/kubelet" "$TMP/render.yaml" /var/lib/k0s/kubelet
check "slot 55b" "$TMP/render.yaml" Deployment csi-evs-controller "csi-provisioner csi-attacher csi-resizer liveness-probe evs-csi-plugin"
render --set volumeSnapshotClass.enabled=true
check "snapshot class on" "$TMP/render.yaml" Deployment csi-evs-controller "csi-provisioner csi-attacher csi-resizer csi-snapshotter liveness-probe evs-csi-plugin"
exit "$rc"
