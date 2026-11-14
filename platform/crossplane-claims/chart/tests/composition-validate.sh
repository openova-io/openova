#!/usr/bin/env bash
# bp-crossplane Day-2 CRUD Compositions validation gate (issue #240).
#
# This is the chart-level lint+template+kubectl-dry-run pass that runs
# against every render of bp-crossplane's templates/xrds + templates/compositions
# + templates/crds directory tree. The 7 XRDs back the catalyst-api Day-2
# CRUD endpoints (RegionClaim, ClusterClaim, NodePoolClaim,
# LoadBalancerClaim, PeeringClaim, NodeActionClaim) plus the cloud-agnostic
# XCloudAdoption adoption seam. The default render ships 7 Compositions
# (6 Hetzner CRUD kinds + opentofu-cloud-adoption).
#
# Sovereign IAM access plane (UserAccess — issue #322): as of Refs #4773
# UserAccess is a PLAIN CustomResourceDefinition (templates/crds/
# useraccess.yaml), NOT a Crossplane XRD/Claim. The old XRD's
# `claimNames: {kind: UserAccess}` spawned XUserAccess composites that
# could never bind (the legacy Composition was retired by default,
# PR #1309 / Fix #71) — an unbounded leak. The XRD + the legacy
# Composition are removed; the canonical realization is the in-cluster
# catalyst-useraccess-controller.
#
# Verifies, in order:
#   1. `helm template` renders without error (no Go-template breakage).
#   2. The render contains exactly 7 XRDs (6 Hetzner CRUD kinds +
#      XCloudAdoption) — UserAccess is NO LONGER an XRD.
#   3. The render contains the 7 expected default Compositions by name,
#      AND no useraccess Composition renders (removed Refs #4773).
#   3d. UserAccess ships as a plain namespaced CRD and NO XUserAccess
#      composite type is rendered anywhere.
#   4. Each expected claim/CR kind is present in the render.
#   5. Each XRC sample fixture under tests/fixtures/ refers to a kind that
#      matches one of the rendered XRDs / CRDs.
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

echo "[composition-validate] Case 2: render contains 7 XRDs"
# 6 day-2 Hetzner CRUD XRDs + 1 cloud-agnostic XCloudAdoption (the
# OpenTofu→Crossplane adoption seam, ADR-0011 / #4002). UserAccess is NO
# LONGER an XRD — it is a plain CRD (Refs #4773) so it never spawns an
# XUserAccess composite.
XRD_COUNT="$(grep -c '^kind: CompositeResourceDefinition$' "$TMP/render.yaml" || true)"
if [ "$XRD_COUNT" -ne 7 ]; then
  echo "FAIL: expected 7 XRDs, found $XRD_COUNT" >&2
  grep -E '^(kind|  name): ' "$TMP/render.yaml" | head -40 >&2
  exit 1
fi
echo "  PASS ($XRD_COUNT XRDs)"

echo "[composition-validate] Case 3: render contains the 7 expected default Compositions"
# Explicit-name check (qa-loop iter-16 Fix #89, post PR #1309).
#
# The legacy useraccess.compose.openova.io Composition was retired by
# default in PR #1309 (Fix #71) and is now DELETED outright (Refs #4773) —
# along with its XUserAccess XRD — because it fed an unbounded composite
# leak. The canonical day-2 IaC for IAM grants is the in-cluster
# catalyst-useraccess-controller.
#
# We assert the EXACT default-rendered Composition names rather than a
# magic-number count so this gate is robust against future
# additions/retirements (Principle 4: target-state, never hardcode the wrong
# magic number; the names ARE the contract).
EXPECTED_COMPOSITIONS=(
  hetzner-cluster.compose.openova.io
  hetzner-load-balancer-claim.compose.openova.io
  hetzner-node-action.compose.openova.io
  hetzner-node-pool.compose.openova.io
  hetzner-peering.compose.openova.io
  hetzner-region.compose.openova.io
  opentofu-cloud-adoption.compose.openova.io
)
COMPOSITION_COUNT="$(grep -c '^kind: Composition$' "$TMP/render.yaml" || true)"
if [ "$COMPOSITION_COUNT" -ne ${#EXPECTED_COMPOSITIONS[@]} ]; then
  echo "FAIL: expected exactly ${#EXPECTED_COMPOSITIONS[@]} Compositions (default render), found $COMPOSITION_COUNT" >&2
  echo "      hint: did you add/remove a Composition in templates/compositions/? update EXPECTED_COMPOSITIONS in tests/composition-validate.sh" >&2
  grep -E '^(kind|  name): ' "$TMP/render.yaml" | grep -B1 'compose.openova.io' >&2 || true
  exit 1
fi
for name in "${EXPECTED_COMPOSITIONS[@]}"; do
  if ! grep -q "^  name: $name$" "$TMP/render.yaml"; then
    echo "FAIL: expected Composition $name not found in render" >&2
    exit 1
  fi
done
echo "  PASS ($COMPOSITION_COUNT Compositions, all expected names present)"

echo "[composition-validate] Case 3a: legacy useraccess Composition is fully removed"
# Refs #4773: useraccess.compose.openova.io is DELETED (not merely gated
# off). It must never render, at any values combination — the leak it
# fed was the XUserAccess composite it backed.
if grep -q '^  name: useraccess.compose.openova.io$' "$TMP/render.yaml"; then
  echo "FAIL: useraccess.compose.openova.io still renders — it should be removed (Refs #4773)" >&2
  exit 1
fi
echo "  PASS (useraccess Composition removed)"

echo "[composition-validate] Case 3d: UserAccess ships as a plain CRD, no XUserAccess composite"
# Refs #4773: the plain useraccesses.access.openova.io CustomResourceDefinition
# MUST render (it registers the CRD the catalyst-useraccess-controller
# watches), and the composite type XUserAccess / xuseraccesses MUST NOT
# appear anywhere (that is the leaked type — removing the XRD removes it).
if ! grep -q '^  name: useraccesses.access.openova.io$' "$TMP/render.yaml"; then
  echo "FAIL: plain CRD useraccesses.access.openova.io did not render — controller would lose its CRD" >&2
  exit 1
fi
# Strip comment lines (the plain-CRD template's own history note names the
# retired type in prose) before asserting the composite type is gone —
# match only real manifest declarations of the XUserAccess composite.
# `grep -Ec … >/dev/null` — NOT `grep -Eq`. Refs #6235: `-q` exits on the FIRST
# match, so the producer's next write hits a pipe with no reader (SIGPIPE 141
# locally; EPIPE -> rc 1 on the GitHub runner, where SIGPIPE is SIG_IGN). Under
# `set -o pipefail` that poisons the pipeline status, and a PRESENT XUserAccess
# reads as absent — the `if` goes false and this fail-open assertion reports a
# pass. `-c` counts, so it reads to EOF and the producer always finishes. This
# producer emits 492,746 bytes; the earliest offending line is near the top.
if grep -vE '^\s*#' "$TMP/render.yaml" | grep -Ec 'kind: XUserAccess|xuseraccesses' >/dev/null; then
  echo "FAIL: XUserAccess / xuseraccesses composite type still present — the XR-spawning layer must be gone (Refs #4773)" >&2
  grep -vE '^\s*#' "$TMP/render.yaml" | grep -nE 'kind: XUserAccess|xuseraccesses' >&2
  exit 1
fi
echo "  PASS (plain UserAccess CRD present, no XUserAccess composite)"

echo "[composition-validate] Case 3b: render contains 8 ClusterRoles (Sovereign IAM + 5 catalog tiers)"
# 3 legacy openova:application-{admin,editor,viewer} + 5 EPIC-3 tier-*
# ClusterRoles (viewer/developer/operator/admin/owner). When the
# tier-clusterroles toggle is off (.Values.tiers.enabled=false) only
# the 3 legacy roles render; the count check below tracks the default
# values.yaml shape (tiers.enabled=true).
CLUSTERROLE_COUNT="$(grep -c '^kind: ClusterRole$' "$TMP/render.yaml" || true)"
if [ "$CLUSTERROLE_COUNT" -ne 8 ]; then
  echo "FAIL: expected 8 ClusterRoles (3 application-* + 5 tier-*), found $CLUSTERROLE_COUNT" >&2
  exit 1
fi
echo "  PASS ($CLUSTERROLE_COUNT ClusterRoles)"

echo "[composition-validate] Case 3c: render contains 1 ClusterPolicy (useraccess-boundary)"
# EPIC-3 (#1098) slice A3 ships a single Kyverno ClusterPolicy. The
# default (.Values.userAccessBoundary.enabled=true) renders it; per-
# Sovereign overlays may flip it off in which case the count is 0.
POLICY_COUNT="$(grep -c '^kind: ClusterPolicy$' "$TMP/render.yaml" || true)"
if [ "$POLICY_COUNT" -ne 1 ]; then
  echo "FAIL: expected 1 ClusterPolicy (useraccess-boundary), found $POLICY_COUNT" >&2
  exit 1
fi
echo "  PASS ($POLICY_COUNT ClusterPolicy)"

echo "[composition-validate] Case 4: every expected CR kind is present"
# RegionClaim..CloudAdoption are XRD claim kinds; UserAccess is a plain
# CRD kind (Refs #4773). The grep asserts the kind string is present in
# the render regardless of which resource declares it.
EXPECTED_KINDS=(
  RegionClaim
  ClusterClaim
  NodePoolClaim
  LoadBalancerClaim
  PeeringClaim
  NodeActionClaim
  UserAccess
  CloudAdoption
)
for kind in "${EXPECTED_KINDS[@]}"; do
  if ! grep -q "kind: $kind$" "$TMP/render.yaml"; then
    echo "FAIL: expected CR kind $kind not found in render" >&2
    exit 1
  fi
done
echo "  PASS (all ${#EXPECTED_KINDS[@]} CR kinds present)"

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
