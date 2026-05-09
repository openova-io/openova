#!/usr/bin/env bash
# bp-cnpg-pair render gate (slice C-DB-1, #1101).
#
# Asserts the chart's load-bearing rules:
#   1. Default render (cnpgPair.enabled=false) → ZERO resources.
#   2. Enabled render with full values → 8 resources (primary +
#      replica + service + probe Deployment + 3 NetworkPolicies +
#      audit-config ConfigMap).
#   3. Enabled with empty image.tag → fails with documented error.
#   4. Enabled with same primary/replica region → fails with
#      documented error.
#   5. ClusterMesh disabled → no replication Service rendered.
#
# Usage: bash tests/cnpg-pair-render.sh [CHART_DIR]
#
# CI consumes this via blueprint-release.yaml's `tests/*.sh` gate.

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

# ── Case 1: default render (disabled) ────────────────────────────
echo "[render] Case 1: default render (cnpgPair.enabled=false) emits ZERO resources"
helm template smoke-cnpg-pair . > "$TMP/disabled.yaml" 2> "$TMP/disabled.err" || {
  echo "FAIL: default-disabled render errored:" >&2
  cat "$TMP/disabled.err" >&2
  exit 1
}
# A YAML doc with only template comments + zero `kind:` lines is
# correct for a fully-disabled render.
KINDS=$(grep -cE "^kind: " "$TMP/disabled.yaml" || true)
if [ "$KINDS" -ne 0 ]; then
  echo "FAIL: default render produced $KINDS resource(s); expected 0." >&2
  grep -E "^kind: " "$TMP/disabled.yaml" >&2
  exit 1
fi
echo "  PASS"

# ── Case 2: full-on render ───────────────────────────────────────
echo "[render] Case 2: enabled render emits all expected resources"
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.primary.region=hz-fsn-rtz-prod \
  --set cnpgPair.replica.region=hz-hel-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  > "$TMP/enabled.yaml" 2> "$TMP/enabled.err" || {
  echo "FAIL: enabled render errored:" >&2
  cat "$TMP/enabled.err" >&2
  exit 1
}

# Expect 1×Cluster (primary) + 1×Cluster (replica) + 1×Service +
# 1×Deployment (probe) + 3×NetworkPolicy + 1×ConfigMap = 8 kinds.
EXPECTED=8
GOT=$(grep -cE "^kind: " "$TMP/enabled.yaml" || true)
if [ "$GOT" -ne "$EXPECTED" ]; then
  echo "FAIL: expected $EXPECTED resources, got $GOT." >&2
  grep -E "^kind: " "$TMP/enabled.yaml" >&2
  exit 1
fi

# Spot-check a few load-bearing assertions:
grep -q "kind: Cluster" "$TMP/enabled.yaml" || {
  echo "FAIL: no CNPG Cluster CR rendered." >&2
  exit 1
}
grep -q 'service.cilium.io/global: "true"' "$TMP/enabled.yaml" || {
  echo "FAIL: replication Service missing service.cilium.io/global=true annotation." >&2
  exit 1
}
grep -q "openova.io/cnpg-role: primary" "$TMP/enabled.yaml" || {
  echo "FAIL: primary Cluster CR missing openova.io/cnpg-role=primary label." >&2
  exit 1
}
grep -q "openova.io/cnpg-role: replica" "$TMP/enabled.yaml" || {
  echo "FAIL: replica Cluster CR missing openova.io/cnpg-role=replica label." >&2
  exit 1
}
grep -q "kind: ConfigMap" "$TMP/enabled.yaml" || {
  echo "FAIL: audit-config ConfigMap not rendered." >&2
  exit 1
}
grep -q "audit.subject:" "$TMP/enabled.yaml" || {
  echo "FAIL: audit-config ConfigMap missing audit.subject key." >&2
  exit 1
}
echo "  PASS ($GOT resources)"

# ── Case 3: missing image.tag fails fast ─────────────────────────
echo "[render] Case 3: empty image.tag triggers fail-fast"
if helm template smoke-cnpg-pair . \
     --set cnpgPair.enabled=true \
     --set cnpgPair.primary.region=hz-fsn-rtz-prod \
     --set cnpgPair.replica.region=hz-hel-rtz-prod \
     > "$TMP/notag.yaml" 2> "$TMP/notag.err"; then
  echo "FAIL: render succeeded with empty cnpgPair.image.tag — should have failed fast." >&2
  exit 1
fi
if ! grep -q "cnpgPair.image.tag is REQUIRED" "$TMP/notag.err"; then
  echo "FAIL: expected fail-fast error mentioning cnpgPair.image.tag is REQUIRED:" >&2
  cat "$TMP/notag.err" >&2
  exit 1
fi
echo "  PASS"

# ── Case 4: same primary/replica region fails fast ───────────────
echo "[render] Case 4: same primary/replica region triggers fail-fast"
if helm template smoke-cnpg-pair . \
     --set cnpgPair.enabled=true \
     --set cnpgPair.primary.region=hz-fsn-rtz-prod \
     --set cnpgPair.replica.region=hz-fsn-rtz-prod \
     --set cnpgPair.image.tag=16.3-23 \
     > "$TMP/sameregion.yaml" 2> "$TMP/sameregion.err"; then
  echo "FAIL: render succeeded with primary.region == replica.region — should have failed fast." >&2
  exit 1
fi
if ! grep -q "MUST NOT equal" "$TMP/sameregion.err"; then
  echo "FAIL: expected fail-fast error mentioning regions MUST NOT equal:" >&2
  cat "$TMP/sameregion.err" >&2
  exit 1
fi
echo "  PASS"

# ── Case 5: ClusterMesh disabled → no Service rendered ───────────
echo "[render] Case 5: clusterMesh.enabled=false skips replication Service"
helm template smoke-cnpg-pair . \
  --set cnpgPair.enabled=true \
  --set cnpgPair.primary.region=hz-fsn-rtz-prod \
  --set cnpgPair.replica.region=hz-hel-rtz-prod \
  --set cnpgPair.image.tag=16.3-23 \
  --set cnpgPair.clusterMesh.enabled=false \
  > "$TMP/nomesh.yaml" 2> "$TMP/nomesh.err" || {
  echo "FAIL: nomesh render errored:" >&2
  cat "$TMP/nomesh.err" >&2
  exit 1
}
SERVICES=$(awk '/^kind: Service$/{print}' "$TMP/nomesh.yaml" | wc -l)
if [ "$SERVICES" -ne 0 ]; then
  echo "FAIL: clusterMesh disabled but Service still rendered." >&2
  exit 1
fi
echo "  PASS"

echo "[render] All bp-cnpg-pair render gates green."
