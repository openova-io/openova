#!/usr/bin/env bash
# bp-openbao cross-region stretched-raft render test (#3492).
#
# Verifies the REAL OpenBao OSS cross-region mechanism (openbao discussion
# #1842): region-B joins region-A's raft cluster as a NON-VOTER via retry_join
# so it holds region-A's live KV (NOT a snapshot copy — that can't share the
# barrier key). Rendered by templates/cross-region-raft-config.yaml +
# templates/cross-region-mesh-service.yaml + the _helpers.tpl
# openbao.crossRegionRaftConfig.
#
#   Case 1 — default render (crossRegion.enabled=false): NO retry_join
#            ConfigMap, NO ClusterMesh Service. Single-region byte-identical
#            (skip-render, never `{{ fail }}` per #402).
#   Case 2 — enabled + role=primary (region-A): the ClusterMesh-global Service
#            renders (so region-B's retry_join resolves it) but NO retry_join
#            ConfigMap (region-A is the unmodified seed).
#   Case 3 — enabled + role=secondary (region-B): the retry_join ConfigMap
#            renders with retry_join + retry_join_as_non_voter=true +
#            leader_api_addr, AND the ClusterMesh-global Service (the mesh-merge
#            contract requires it on both sides).
#
# Usage: bash tests/cross-region-render.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

MESH_SVC='name: "openbao-active-mesh"'
CFG_MAP='name: openbao-cross-region-raft-config'

echo "[cross-region-render] Case 1: default render produces NO cross-region artefacts"
helm template smoke-bao . > "$TMP/default.yaml"
if grep -qF "$CFG_MAP" "$TMP/default.yaml"; then
  echo "FAIL: default render contains the cross-region raft ConfigMap — skip-render is broken." >&2
  exit 1
fi
if grep -qF "$MESH_SVC" "$TMP/default.yaml"; then
  echo "FAIL: default render contains the ClusterMesh Service — skip-render is broken." >&2
  exit 1
fi
if grep -q "retry_join" "$TMP/default.yaml"; then
  echo "FAIL: default render contains a retry_join stanza — single-region must be byte-identical." >&2
  exit 1
fi
echo "  PASS"

echo "[cross-region-render] Case 2: role=primary renders the mesh Service but NO retry_join ConfigMap"
helm template smoke-bao . \
  --set crossRegion.enabled=true \
  --set crossRegion.role=primary > "$TMP/primary.yaml"
if ! grep -qF "$MESH_SVC" "$TMP/primary.yaml"; then
  echo "FAIL: role=primary did not render the ClusterMesh Service (region-B's retry_join can't resolve it)." >&2
  exit 1
fi
if grep -qF "$CFG_MAP" "$TMP/primary.yaml"; then
  echo "FAIL: role=primary rendered the retry_join ConfigMap — region-A is the unmodified seed, it must NOT join itself." >&2
  exit 1
fi
# The mesh Service MUST carry the Cilium global annotation or it won't cross the mesh.
if ! grep -q 'service.cilium.io/global: "true"' "$TMP/primary.yaml"; then
  echo "FAIL: ClusterMesh Service is missing service.cilium.io/global annotation." >&2
  exit 1
fi
echo "  PASS"

echo "[cross-region-render] Case 3: role=secondary renders retry_join non-voter config + the mesh Service"
helm template smoke-bao . \
  --set crossRegion.enabled=true \
  --set crossRegion.role=secondary \
  --set crossRegion.leaderApiAddr="http://openbao-active-mesh.openbao.svc.cluster.local:8200" > "$TMP/secondary.yaml"
if ! grep -qF "$CFG_MAP" "$TMP/secondary.yaml"; then
  echo "FAIL: role=secondary did not render the retry_join ConfigMap." >&2
  exit 1
fi
# The retry_join stanza + non-voter flag are the whole point — region-B joins
# region-A as a non-voter and receives the live replication stream.
if ! grep -q "retry_join {" "$TMP/secondary.yaml"; then
  echo "FAIL: secondary raft config is missing the retry_join stanza." >&2
  exit 1
fi
if ! grep -q "retry_join_as_non_voter = true" "$TMP/secondary.yaml"; then
  echo "FAIL: secondary raft config is missing retry_join_as_non_voter = true (region-B MUST join as a non-voter)." >&2
  exit 1
fi
if ! grep -q 'leader_api_addr = "http://openbao-active-mesh.openbao.svc.cluster.local:8200"' "$TMP/secondary.yaml"; then
  echo "FAIL: secondary retry_join leader_api_addr was not wired from crossRegion.leaderApiAddr." >&2
  exit 1
fi
# The mesh Service MUST also exist on the secondary (Cilium merges by name on
# BOTH sides).
if ! grep -qF "$MESH_SVC" "$TMP/secondary.yaml"; then
  echo "FAIL: role=secondary did not render the ClusterMesh Service (the merge contract needs it on both sides)." >&2
  exit 1
fi
echo "  PASS"

echo "[cross-region-render] All bp-openbao cross-region render gates green."
