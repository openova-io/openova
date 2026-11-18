#!/usr/bin/env bash
# bp-cilium: the chart names no API host and no native-routing range (NET-110).
#
# The address of the API server and the native-routing range are values of
# one Sovereign. The bootstrap-kit slot clusters/_template/bootstrap-kit/01-cilium.yaml
# sets them from the substitutes CILIUM_K8S_SERVICE_HOST and
# CILIUM_NATIVE_ROUTING_CIDR, which first boot writes per region; the range is
# the pod supernet of the provider module. The chart keeps native routing on
# and sets neither value.
#
# Cases:
#   1. The default render succeeds, runs native routing, holds no
#      ipv4-native-routing-cidr and sets no KUBERNETES_SERVICE_HOST.
#   2. A render that names an API host and no range, or an empty range, fails
#      (templates/native-routing-range.yaml), and the error names
#      cilium.ipv4NativeRoutingCIDR and the substitute.
#   3. A render that names an API host and a range, as the slot does, holds
#      that range, native routing, direct node routes, MTU 1420, WireGuard,
#      and the API host in the agent's environment.
#
# Usage:
#   bash native-routing.sh [CHART_DIR]   run the cases (CI runs every chart/tests/*.sh this way)
#   bash native-routing.sh --self-test   prove on three broken copies of the chart that
#                                        the cases fail: a range in the values (case 1),
#                                        an API host in the values without the guard
#                                        template (case 1), and no guard template
#                                        (case 2); then run the real chart
# Needs helm. Exit 0 pass, 1 fail.
set -euo pipefail

SELF="$(cd "$(dirname "$0")" && pwd)/$(basename "$0")"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

RANGE=10.40.0.0/13
HOST=192.0.2.10
MESSAGE='bp-cilium: cilium.ipv4NativeRoutingCIDR is not set.'

# cases <chart dir>: runs the three cases in a subshell; exit 0 when all pass.
cases() (
  set -euo pipefail
  fail() { echo "FAIL: $1" >&2; exit 1; }
  out="$(mktemp -d "$TMP/cases.XXXXXX")"
  cd "$1"
  if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
    helm dependency build >/dev/null
  fi

  echo "[native-routing] Case 1: the default render names no range and no API host"
  helm template cilium . --namespace kube-system > "$out/default.yaml"
  grep -q -E '^\s*routing-mode: "native"\s*$' "$out/default.yaml" || fail "the default render does not run native routing"
  if grep -q -E '^\s*ipv4-native-routing-cidr:' "$out/default.yaml"; then
    fail "the default render sets ipv4-native-routing-cidr; the range comes from the slot"
  fi
  if grep -q -F 'KUBERNETES_SERVICE_HOST' "$out/default.yaml"; then
    fail "the default render sets KUBERNETES_SERVICE_HOST; the API host comes from the slot"
  fi
  echo "  PASS"

  echo "[native-routing] Case 2: an API host without a range fails"
  if helm template cilium . --namespace kube-system --set cilium.k8sServiceHost="$HOST" \
    > "$out/norange.yaml" 2> "$out/norange.err"; then
    fail "a render with an API host and no ipv4NativeRoutingCIDR succeeded"
  fi
  grep -q -F "$MESSAGE" "$out/norange.err" || { cat "$out/norange.err" >&2; fail "the render failed without the message of the chart"; }
  grep -q -F 'CILIUM_NATIVE_ROUTING_CIDR' "$out/norange.err" || fail "the message does not name CILIUM_NATIVE_ROUTING_CIDR"
  if helm template cilium . --namespace kube-system --set cilium.k8sServiceHost="$HOST" \
    --set-string cilium.ipv4NativeRoutingCIDR= > "$out/empty.yaml" 2> "$out/empty.err"; then
    fail "a render with an API host and an empty ipv4NativeRoutingCIDR succeeded"
  fi
  grep -q -F "$MESSAGE" "$out/empty.err" || { cat "$out/empty.err" >&2; fail "the render with an empty range failed without the message of the chart"; }
  echo "  PASS"

  echo "[native-routing] Case 3: an API host and a range, as the slot sets them"
  helm template cilium . --namespace kube-system \
    --set cilium.k8sServiceHost="$HOST" --set cilium.ipv4NativeRoutingCIDR="$RANGE" > "$out/slot.yaml"
  grep -q -E "^\s*ipv4-native-routing-cidr: ${RANGE}\s*$" "$out/slot.yaml" || fail "ipv4-native-routing-cidr is not ${RANGE}"
  grep -q -E '^\s*routing-mode: "native"\s*$' "$out/slot.yaml" || fail "routing-mode is not native"
  grep -q -E '^\s*auto-direct-node-routes: "true"\s*$' "$out/slot.yaml" || fail "auto-direct-node-routes is not true"
  grep -q -E '^\s*mtu: "1420"\s*$' "$out/slot.yaml" || fail "mtu is not 1420"
  grep -q -E '^\s*enable-wireguard: "true"\s*$' "$out/slot.yaml" || fail "WireGuard is not on"
  grep -q -F 'name: KUBERNETES_SERVICE_HOST' "$out/slot.yaml" || fail "the agent has no KUBERNETES_SERVICE_HOST"
  if grep -A1 -F 'name: KUBERNETES_SERVICE_HOST' "$out/slot.yaml" | grep -E '^\s*value:' | grep -v -q -F "\"${HOST}\""; then
    fail "a KUBERNETES_SERVICE_HOST is not ${HOST}"
  fi
  echo "  PASS"
)

if [ "${1:-}" = "--self-test" ]; then
  CHART="$(cd "$(dirname "$SELF")/.." && pwd)"
  bad=0
  # Fetch the upstream subchart once, so that every copy below carries it.
  if [ ! -d "$CHART/charts" ] || [ -z "$(ls -A "$CHART/charts" 2>/dev/null)" ]; then
    (cd "$CHART" && helm dependency build >/dev/null)
  fi
  # broken <name> <expected FAIL text>: the copy $TMP/<name> must fail the cases with that text.
  broken() {
    if cases "$TMP/$1" > "$TMP/$1.out" 2>&1; then
      echo "self-test FAILED: $1 passed the cases"; bad=1
    elif ! grep -q -F "FAIL: $2" "$TMP/$1.out"; then
      echo "self-test FAILED: $1 failed for another reason:"; cat "$TMP/$1.out"; bad=1
    else
      echo "self-test ok: $1 fails with \"$2\""
    fi
  }
  cp -R "$CHART" "$TMP/range-in-values"
  sed -i "s|^  autoDirectNodeRoutes: true\$|  autoDirectNodeRoutes: true\n  ipv4NativeRoutingCIDR: \"${RANGE}\"|" "$TMP/range-in-values/values.yaml"
  broken range-in-values "the default render sets ipv4-native-routing-cidr"
  cp -R "$CHART" "$TMP/host-in-values"
  sed -i "s|^  k8sServiceHost: \"\"\$|  k8sServiceHost: ${HOST}|" "$TMP/host-in-values/values.yaml"
  rm -f "$TMP/host-in-values/templates/native-routing-range.yaml"
  broken host-in-values "the default render sets KUBERNETES_SERVICE_HOST"
  cp -R "$CHART" "$TMP/no-guard"
  rm -f "$TMP/no-guard/templates/native-routing-range.yaml"
  broken no-guard "a render with an API host and no ipv4NativeRoutingCIDR succeeded"
  if cases "$CHART" > "$TMP/real.out" 2>&1; then
    echo "self-test ok: the chart passes the cases"
  else
    echo "self-test FAILED: the chart fails the cases:"; cat "$TMP/real.out"; bad=1
  fi
  [ "$bad" = 0 ] && echo "native-routing: self-test passed"
  exit "$bad"
fi

cases "$(cd "${1:-$(dirname "$SELF")/..}" && pwd)"
echo "native-routing: all cases passed"
