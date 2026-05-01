#!/usr/bin/env bash
# bp-external-secrets-stores ClusterSecretStore toggle integration test.
#
# Sister test to platform/external-secrets/chart/tests/observability-toggle.sh.
# Verifies the chart split shipped in PR #334 (issue #331):
#   - default render produces the vault-region1 ClusterSecretStore CR
#   - clusterSecretStore.enabled=false suppresses it
#
# Mirrors the bp-crossplane-claims pattern (PR #247).
#
# Usage: bash tests/clustersecretstore-toggle.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

echo "[stores-toggle] Case 1: default render includes vault-region1 ClusterSecretStore"
helm template smoke-eso-stores . > "$TMP/default.yaml"
if ! grep -qE "^kind: ClusterSecretStore$" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-external-secrets-stores is missing the vault-region1 ClusterSecretStore CR." >&2
  echo "      The chart's whole purpose is to ship this default CR — see Chart.yaml." >&2
  exit 1
fi
if ! grep -q "name: \"vault-region1\"" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-external-secrets-stores is missing the vault-region1 name." >&2
  exit 1
fi
echo "  PASS"

echo "[stores-toggle] Case 2: clusterSecretStore.enabled=false omits the default ClusterSecretStore"
if ! helm template smoke-eso-stores . \
    --set "clusterSecretStore.enabled=false" \
    > "$TMP/css-off.yaml" 2> "$TMP/css-off.err"; then
  echo "FAIL: clusterSecretStore.enabled=false render failed:" >&2
  cat "$TMP/css-off.err" >&2
  exit 1
fi
if grep -qE "^kind: ClusterSecretStore$" "$TMP/css-off.yaml"; then
  echo "FAIL: clusterSecretStore.enabled=false still renders a ClusterSecretStore CR — the toggle is broken." >&2
  exit 1
fi
echo "  PASS"

echo "[stores-toggle] Case 3: clusterSecretStore.server override propagates"
if ! helm template smoke-eso-stores . \
    --set "clusterSecretStore.server=https://openbao.example.com:8200" \
    > "$TMP/srv.yaml" 2> "$TMP/srv.err"; then
  echo "FAIL: server override render failed:" >&2
  cat "$TMP/srv.err" >&2
  exit 1
fi
if ! grep -q 'server: "https://openbao.example.com:8200"' "$TMP/srv.yaml"; then
  echo "FAIL: server override did not propagate into rendered ClusterSecretStore." >&2
  exit 1
fi
echo "  PASS"

echo "[stores-toggle] All bp-external-secrets-stores toggle gates green."
