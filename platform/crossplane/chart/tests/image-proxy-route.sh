#!/usr/bin/env bash
# bp-crossplane image-proxy-route render gate (issue #5281).
#
# Asserts the crossplane CORE controller image (+ rbac-manager, which reads
# the same `crossplane.image.repository`) renders through the Sovereign
# Harbor pull-through proxy — NEVER bare `xpkg.upbound.io`.
#
# Why this gate exists: `xpkg.upbound.io/crossplane/crossplane` was the only
# image on a fresh Sovereign that pulled DIRECT from upbound's registry. A
# Phase-1 upbound 503 (observed live on hw279 dep 059126bb, 2026-07-20) wedged
# crossplane-system at Init:ErrImagePull, stalling bp-crossplane ->
# bp-crossplane-claims -> bp-catalyst-platform. Routing the image through
# `harbor.openova.io/proxy-dockerhub/crossplane/crossplane` (docker.io publishes
# the identical release; proxy-dockerhub is live on the mothership AND mirrored
# at cutover) puts crossplane on the same registry path as every other platform
# image. This gate keeps it there — a future values edit that reintroduces a
# bare xpkg.upbound.io ref fails CI.
#
# Usage: bash tests/image-proxy-route.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

EXPECTED="harbor.openova.io/proxy-dockerhub/crossplane/crossplane"

cd "$CHART_DIR"
# Skip helm dep build when charts/ is already vendored (CI populates it before
# this step runs; re-running without `helm repo add` would fail). Mirrors
# tests/observability-toggle.sh.
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

echo "[image-proxy-route] Rendering bp-crossplane default values"
helm template smoke-cp . > "$TMP/default.yaml"

echo "[image-proxy-route] Case 1: no image ref pulls DIRECT from xpkg.upbound.io"
if grep -nE '^\s*-?\s*image:\s*"?xpkg\.upbound\.io/' "$TMP/default.yaml"; then
  echo "FAIL: bp-crossplane renders a container image pulling DIRECT from xpkg.upbound.io." >&2
  echo "      That host has no Harbor proxy-cache on the mothership and 503s during Phase-1 (#5281)." >&2
  echo "      Route the core image via ${EXPECTED} (crossplane.image.repository)." >&2
  exit 1
fi
echo "  PASS"

echo "[image-proxy-route] Case 2: crossplane image resolves to the Harbor proxy-dockerhub route"
# All four crossplane container images (core init+main, rbac-manager init+main)
# read crossplane.image.repository, so every rendered crossplane/crossplane ref
# must carry the harbor proxy prefix.
if ! grep -qE "image:\s*\"?${EXPECTED}:v1\.18\.0\"?" "$TMP/default.yaml"; then
  echo "FAIL: expected image '${EXPECTED}:v1.18.0' not found in rendered output." >&2
  echo "      Found these crossplane image refs instead:" >&2
  grep -nE 'image:.*crossplane/crossplane' "$TMP/default.yaml" | head -10 >&2
  exit 1
fi
# Belt-and-suspenders: EVERY crossplane/crossplane image ref must be the proxy route.
BAD="$(grep -nE 'image:.*crossplane/crossplane' "$TMP/default.yaml" | grep -v "${EXPECTED}:" || true)"
if [ -n "$BAD" ]; then
  echo "FAIL: some crossplane/crossplane image refs are NOT the Harbor proxy route:" >&2
  echo "$BAD" >&2
  exit 1
fi
COUNT="$(grep -cE "image:\s*\"?${EXPECTED}:v1\.18\.0\"?" "$TMP/default.yaml")"
echo "  PASS (${COUNT} crossplane image refs all on ${EXPECTED})"

echo "[image-proxy-route] All bp-crossplane image-proxy-route gates green."
