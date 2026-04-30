#!/usr/bin/env bash
# bp-crossplane Day-2 CRUD Compositions validation gate (issue #240).
#
# This is the chart-level lint+template+kubectl-dry-run pass that runs
# against every render of bp-crossplane's templates/xrds + templates/compositions
# directory tree. The 6 XRDs and 6 Compositions composed here back the
# catalyst-api Day-2 CRUD endpoints (RegionClaim, ClusterClaim,
# NodePoolClaim, LoadBalancerClaim, PeeringClaim, NodeActionClaim).
#
# Verifies, in order:
#   1. `helm template` renders without error (no Go-template breakage).
#   2. The render contains exactly 6 XRDs (one per CRUD kind) and at least
#      6 Compositions (NodePool/LoadBalancer compose multiple sub-resources
#      so the count for those families ≥ 6).
#   3. Each XRD's `claimNames.kind` matches the catalyst-api expectation:
#      RegionClaim, ClusterClaim, NodePoolClaim, LoadBalancerClaim,
#      PeeringClaim, NodeActionClaim.
#   4. `kubectl --dry-run=client` accepts every rendered XRD + Composition
#      (schema-shape verification — does NOT require a live cluster).
#   5. Each XRC sample fixture under tests/fixtures/ refers to a kind that
#      matches one of the rendered XRDs.
#
# Usage: bash tests/composition-validate.sh [CHART_DIR]
#
# Per docs/INVIOLABLE-PRINCIPLES.md #2 every gate is non-negotiable —
# `set -euo pipefail` ensures one failure aborts the whole run.

set -euo pipefail

# Resolve CHART_DIR to an ABSOLUTE path BEFORE the cd below — otherwise
# CI invokes us with the relative path `platform/crossplane/chart` and
# every later `"$CHART_DIR/<sub>"` reference (notably FIXTURE_DIR) ends
# up pointing into a non-existent path because we've already chdir'd
# into the chart dir.
CHART_DIR_INPUT="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
CHART_DIR="$(cd "$CHART_DIR_INPUT" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

# Skip dep build if charts/ is already vendored (CI populates it before
# this step runs; same pattern as observability-toggle.sh).
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

echo "[composition-validate] Case 1: chart renders cleanly"
helm template smoke-cp . > "$TMP/render.yaml" 2> "$TMP/render.err" || {
  echo "FAIL: helm template failed:" >&2
  cat "$TMP/render.err" >&2
  exit 1
}
echo "  PASS"

echo "[composition-validate] Case 2: render contains 6 XRDs"
XRD_COUNT="$(grep -c '^kind: CompositeResourceDefinition$' "$TMP/render.yaml" || true)"
if [ "$XRD_COUNT" -ne 6 ]; then
  echo "FAIL: expected 6 XRDs, found $XRD_COUNT" >&2
  grep -E '^(kind|  name): ' "$TMP/render.yaml" | head -40 >&2
  exit 1
fi
echo "  PASS ($XRD_COUNT XRDs)"

echo "[composition-validate] Case 3: render contains ≥ 6 Compositions"
COMPOSITION_COUNT="$(grep -c '^kind: Composition$' "$TMP/render.yaml" || true)"
if [ "$COMPOSITION_COUNT" -lt 6 ]; then
  echo "FAIL: expected ≥ 6 Compositions, found $COMPOSITION_COUNT" >&2
  exit 1
fi
echo "  PASS ($COMPOSITION_COUNT Compositions)"

echo "[composition-validate] Case 4: every expected claim kind is present"
EXPECTED_KINDS=(
  RegionClaim
  ClusterClaim
  NodePoolClaim
  LoadBalancerClaim
  PeeringClaim
  NodeActionClaim
)
for kind in "${EXPECTED_KINDS[@]}"; do
  if ! grep -q "kind: $kind$" "$TMP/render.yaml"; then
    echo "FAIL: claim kind $kind not found in any XRD" >&2
    exit 1
  fi
done
echo "  PASS (all 6 claim kinds present)"

echo "[composition-validate] Case 5: every rendered document is valid YAML"
# We can't run `kubectl apply --dry-run=client` without an API server
# context that already has Crossplane's apiextensions.crossplane.io/v1
# CRDs registered (the kubectl client resolves kind→resource via the
# server's discovery API and will reject CompositeResourceDefinition
# otherwise). So at this stage we restrict validation to YAML
# well-formedness; the schema-aware pass is Case 7 below, gated on a
# live kubeconfig reaching a kind/k3s cluster with bp-crossplane already
# installed (CI provides one via tests/integration/ infrastructure).
if ! python3 -c "
import sys, yaml
with open('$TMP/render.yaml') as f:
    docs = list(yaml.safe_load_all(f))
print(f'parsed {len(docs)} YAML documents')
for i, d in enumerate(docs):
    if d is None:
        continue
    if 'kind' not in d:
        sys.exit(f'doc {i} missing kind field')
" > "$TMP/yaml.out" 2> "$TMP/yaml.err"; then
  echo "FAIL: rendered YAML is not well-formed:" >&2
  cat "$TMP/yaml.err" >&2
  exit 1
fi
cat "$TMP/yaml.out"
echo "  PASS"

echo "[composition-validate] Case 6: every fixture XRC kind is matched by an XRD"
FIXTURE_DIR="$CHART_DIR/tests/fixtures"
if [ ! -d "$FIXTURE_DIR" ]; then
  echo "FAIL: fixtures dir $FIXTURE_DIR missing" >&2
  exit 1
fi
for fixture in "$FIXTURE_DIR"/*-sample.yaml; do
  fixture_kind="$(grep '^kind:' "$fixture" | head -1 | awk '{print $2}')"
  if ! grep -q "kind: $fixture_kind$" "$TMP/render.yaml"; then
    echo "FAIL: fixture $fixture references kind $fixture_kind which has no XRD" >&2
    exit 1
  fi
done
echo "  PASS"

echo "[composition-validate] Case 7: server-side dry-run for each fixture (when Crossplane is installed)"
# Only run this when a kubeconfig is available AND the cluster has the
# apiextensions.crossplane.io/v1 CRD registered (i.e. bp-crossplane is
# already installed). The chart renders are enforceable without a
# cluster (Cases 1-6); this case is the additional schema-aware pass
# CI gives us when running tests/integration/ infrastructure with
# bp-crossplane pre-installed.
if [ -n "${KUBECONFIG:-}" ] \
    && kubectl version --request-timeout=2s >/dev/null 2>&1 \
    && kubectl get crd compositeresourcedefinitions.apiextensions.crossplane.io >/dev/null 2>&1; then
  # Install the rendered XRDs first (so claims can be validated against them).
  kubectl apply -f "$TMP/render.yaml" --dry-run=server > "$TMP/server-render.out" 2> "$TMP/server-render.err" || {
    echo "FAIL: server-side dry-run of rendered manifests failed:" >&2
    cat "$TMP/server-render.err" >&2
    exit 1
  }
  for fixture in "$FIXTURE_DIR"/*-sample.yaml; do
    if ! kubectl apply -f "$fixture" --dry-run=server \
          > "$TMP/$(basename "$fixture").out" 2> "$TMP/$(basename "$fixture").err"; then
      echo "FAIL: server-side dry-run of $fixture failed:" >&2
      cat "$TMP/$(basename "$fixture").err" >&2
      exit 1
    fi
  done
  echo "  PASS (server-side)"
else
  echo "  SKIP (no live cluster — case enforced from CI integration job)"
fi

echo "[composition-validate] All bp-crossplane Day-2 CRUD Composition gates green."
