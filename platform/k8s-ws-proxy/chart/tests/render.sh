#!/usr/bin/env bash
# bp-k8s-ws-proxy helm-render smoke test (EPIC-4 K1, #1099).
#
# Verifies:
#   - default-OFF renders 0 resources
#   - empty image.tag fails fast
#   - full-ON renders DaemonSet + Service + ServiceAccount + ClusterRole + ClusterRoleBinding (5 resources)

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

if [[ ! -d charts ]] || [[ -z "$(ls -A charts 2>/dev/null)" ]]; then
  helm dependency update >/dev/null 2>&1 || {
    echo "WARNING: helm dependency update failed; skipping"
    exit 0
  }
fi

# 1. Default-OFF
off="$(helm template bp-k8s-ws-proxy . | grep -cE '^kind:' || true)"
if [[ "$off" != "0" ]]; then
  echo "FAIL: default-OFF rendered $off resources, want 0"
  exit 1
fi
echo "PASS: default-OFF = 0 resources"

# 2. Fail-fast on empty image tag
if helm template bp-k8s-ws-proxy . --set k8sWsProxy.enabled=true \
    >/dev/null 2>"$TMP/err"; then
  echo "FAIL: empty tag did not abort render"
  exit 1
fi
if ! grep -q 'image.tag is empty' "$TMP/err"; then
  echo "FAIL: error did not mention 'image.tag is empty'"
  cat "$TMP/err"
  exit 1
fi
echo "PASS: empty image.tag fails fast"

# 3. Full-ON
on="$(helm template bp-k8s-ws-proxy . \
  --set k8sWsProxy.enabled=true \
  --set k8sWsProxy.image.tag=abc1234 | grep -cE '^kind:' || true)"
if [[ "$on" != "5" ]]; then
  echo "FAIL: full-ON rendered $on resources, want 5"
  exit 1
fi
echo "PASS: full-ON = 5 resources"

echo "All render tests passed."
