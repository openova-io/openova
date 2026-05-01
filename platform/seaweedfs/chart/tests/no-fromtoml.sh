#!/usr/bin/env bash
# bp-seaweedfs fromToml-removal integration test (issue #340).
#
# Verifies the vendored upstream subchart at charts/seaweedfs/ does NOT
# contain templates/shared/security-configmap.yaml — that file uses the
# `fromToml` Sprig function which is only available in Helm 3.13+. Flux
# helm-controller bundles an older Helm SDK and PARSES every template
# before any `{{- if .Values.global.seaweedfs.enableSecurity }}` gate
# fires, so the file's mere presence breaks install on every Sovereign
# even with enableSecurity=false (the chart's own default).
#
# This test is the regression guard: re-vendoring SeaweedFS at a future
# version MUST re-delete the file (or, if upstream has stopped using
# fromToml, this test can be removed in the same PR that re-vendors).
#
# Usage: bash tests/no-fromtoml.sh [CHART_DIR]
#   CHART_DIR defaults to the parent directory of this script.

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
cd "$CHART_DIR"

# ── Case 1: vendored subchart present (we are not a hollow chart) ───────
echo "[no-fromtoml] Case 1: vendored subchart present at charts/seaweedfs/"
if [ ! -f charts/seaweedfs/Chart.yaml ]; then
  echo "FAIL: vendored upstream subchart missing at charts/seaweedfs/Chart.yaml." >&2
  echo "      bp-seaweedfs vendors the upstream chart per issue #340; re-vendor before publishing." >&2
  exit 1
fi
echo "  PASS"

# ── Case 2: offending fromToml-using template MUST be deleted ───────────
echo "[no-fromtoml] Case 2: charts/seaweedfs/templates/shared/security-configmap.yaml is absent"
if [ -f charts/seaweedfs/templates/shared/security-configmap.yaml ]; then
  echo "FAIL: charts/seaweedfs/templates/shared/security-configmap.yaml still present." >&2
  echo "      That file uses fromToml (Helm 3.13+) and breaks Flux install — see issue #340." >&2
  echo "      Delete it after every re-vendor of the upstream chart." >&2
  exit 1
fi
echo "  PASS"

# ── Case 3: NO fromToml call site survives anywhere in the vendored sub ─
echo "[no-fromtoml] Case 3: no fromToml usage anywhere under charts/seaweedfs/templates/"
if grep -rn 'fromToml' charts/seaweedfs/templates/ 2>/dev/null; then
  echo "FAIL: at least one fromToml call site remains in the vendored subchart." >&2
  echo "      Helm 3.12 / Flux helm-controller cannot parse it. Remove the call site." >&2
  exit 1
fi
echo "  PASS"

# ── Case 4: default render with helm 3.12-compatible binary equivalent ──
# We cannot run helm 3.12 on the runner, but a default `helm template`
# render that succeeds without --include-crds with default values is the
# closest proxy. The render fails on parse errors regardless of helm
# version, so a clean render here is necessary (not sufficient).
echo "[no-fromtoml] Case 4: default helm template render succeeds"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
if ! helm template smoke-seaweedfs . > "$TMP/default.yaml" 2> "$TMP/err"; then
  echo "FAIL: default helm template render of bp-seaweedfs failed:" >&2
  cat "$TMP/err" >&2
  exit 1
fi
echo "  PASS ($(wc -l < "$TMP/default.yaml") lines rendered)"

echo "[no-fromtoml] All bp-seaweedfs fromToml-removal gates green."
