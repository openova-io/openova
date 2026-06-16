#!/usr/bin/env bash
# bp-sso-bridge — #3646 §5a render test: the sso-bridge-reconciler
# Deployment carries the `catalyst.openova.io/reconciler` marker label so
# the jobs canvas's generic reconciler ingestion surfaces it as a
# kind=reconciler leaf (with a HEALTH status), never a one-shot row.
#
# Pure render test (helm template) — no cluster needed.
set -euo pipefail

CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)"
RENDER="$(helm template sso-bridge "$CHART_DIR" --set enabled=true)"

fail() { echo "[3646-reconciler-marker] FAIL: $1" >&2; exit 1; }

# 1. The reconciler Deployment renders.
echo "$RENDER" | grep -q "name: sso-bridge-reconciler" \
  || fail "sso-bridge-reconciler Deployment did not render"

# 2. The metadata.labels carry the §5a marker (value "true").
echo "$RENDER" | grep -q 'catalyst.openova.io/reconciler: "true"' \
  || fail "marker label catalyst.openova.io/reconciler not present"

# 3. The marker is on the Deployment metadata (top-level labels block),
#    not only the pod template — the §5a reader lists Deployments and
#    reads metadata.labels. Assert the marker appears within ~12 lines
#    after the Deployment kind line.
DEPLOY_BLOCK="$(echo "$RENDER" | awk '/^kind: Deployment$/{c=1} c{print} c&&/^spec:/{c=0}')"
echo "$DEPLOY_BLOCK" | grep -q 'catalyst.openova.io/reconciler: "true"' \
  || fail "marker label not on the Deployment metadata block"

echo "[3646-reconciler-marker] PASS"
