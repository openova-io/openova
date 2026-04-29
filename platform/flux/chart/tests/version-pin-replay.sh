#!/usr/bin/env bash
# bp-flux version-pin replay test — catastrophic-failure regression guard.
#
# Live incident replay (omantel.omani.works, 2026-04-29):
#   - Cloud-init pre-installed Flux core via
#       https://github.com/fluxcd/flux2/releases/download/v2.4.0/install.yaml
#   - bp-flux:1.1.1 declared `flux2` subchart 2.13.0 (= upstream
#     appVersion 2.3.0). MISMATCH against cloud-init's v2.4.0.
#   - helm-controller ran `helm install` for bp-flux on top of the
#     running v2.4.0 Flux. CRD `status.storedVersions` carried "v1"
#     from the v2.4.0 install; the chart's v2.3.0 CRDs only declare
#     "v1beta1". apiserver rejected the chart's CRD update with:
#       status.storedVersions[0]: Invalid value: "v1": must appear in
#       spec.versions
#   - Helm rolled back the install — and the rollback DELETED the
#     running Flux controller Deployments (helm-controller,
#     source-controller, kustomize-controller, image-automation,
#     image-reflector, notification-controller).
#   - Cluster lost its GitOps engine. No further HelmRelease could
#     progress. Catastrophic, in-place unrecoverable.
#
# This test replays the precondition for the catastrophic failure
# (version disagreement between cloud-init's flux2 install URL and the
# chart's `flux2` subchart pin) and FAILS LOUDLY if the disagreement is
# ever reintroduced.
#
# Usage: bash tests/version-pin-replay.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
# REPO_ROOT can be overridden via env (used by Case 6's self-test which
# runs against a /tmp fake chart but still needs to validate against the
# real repo's cloud-init template).
REPO_ROOT="${REPO_ROOT:-$(cd "$CHART_DIR/../../.." && pwd)}"
CLOUDINIT_TPL="$REPO_ROOT/infra/hetzner/cloudinit-control-plane.tftpl"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "[version-pin-replay] CHART_DIR=$CHART_DIR"
echo "[version-pin-replay] REPO_ROOT=$REPO_ROOT"

# ── Case 1 — Chart.yaml's flux2 subchart pin is set ──────────────────
echo "[version-pin-replay] Case 1: Chart.yaml declares the flux2 subchart with an explicit version"
chart_dep_version=$(awk '
  /^dependencies:/ {in_deps=1; next}
  in_deps && /name: *flux2/ {found_name=1; next}
  in_deps && found_name && /version:/ {gsub(/"/, "", $2); print $2; exit}
' "$CHART_DIR/Chart.yaml")
if [ -z "$chart_dep_version" ]; then
  echo "FAIL: Chart.yaml does not declare a flux2 subchart with `version:`. Replay precondition met (catastrophic regression)." >&2
  exit 1
fi
echo "  chart subchart pin: flux2 $chart_dep_version"

# ── Case 2 — cloud-init's install.yaml URL contains an explicit version tag ──
echo "[version-pin-replay] Case 2: cloud-init pins flux2 install.yaml to an explicit v-tag"
if [ ! -f "$CLOUDINIT_TPL" ]; then
  echo "FAIL: cloud-init template missing at $CLOUDINIT_TPL — cannot validate version pin." >&2
  exit 1
fi
cloudinit_url=$(grep -oE 'https://github.com/fluxcd/flux2/releases/download/v[0-9]+\.[0-9]+\.[0-9]+/install.yaml' "$CLOUDINIT_TPL" | head -1)
if [ -z "$cloudinit_url" ]; then
  echo "FAIL: cloud-init template at $CLOUDINIT_TPL does not pin a flux2 install.yaml URL with explicit v-tag (e.g. v2.4.0)." >&2
  exit 1
fi
cloudinit_version=$(echo "$cloudinit_url" | sed -E 's|.*/v([0-9]+\.[0-9]+\.[0-9]+)/install.yaml|\1|')
echo "  cloud-init flux2 install.yaml pin: v$cloudinit_version"

# ── Case 3 — chart subchart appVersion equals cloud-init install.yaml version ──
# The fluxcd-community `flux2` chart's `appVersion` field is the upstream
# Flux release tag (e.g. 2.4.0). It MUST match cloud-init's URL pin.
echo "[version-pin-replay] Case 3: chart's flux2 subchart appVersion equals cloud-init's pinned upstream version"
subchart_tgz="$CHART_DIR/charts/flux2-${chart_dep_version}.tgz"
subchart_dir="$CHART_DIR/charts/flux2"
if [ ! -f "$subchart_tgz" ] && [ ! -d "$subchart_dir" ]; then
  echo "  charts/ empty — running 'helm dependency build' to fetch flux2 ${chart_dep_version}"
  ( cd "$CHART_DIR" && helm dependency build >"$TMP/dep-build.log" 2>&1 ) || {
    echo "FAIL: helm dependency build failed:" >&2
    cat "$TMP/dep-build.log" >&2
    exit 1
  }
fi
if [ -f "$subchart_tgz" ]; then
  app_version=$(tar -xzOf "$subchart_tgz" flux2/Chart.yaml | awk '/^appVersion:/ {gsub(/"/, "", $2); print $2; exit}')
elif [ -d "$subchart_dir" ]; then
  app_version=$(awk '/^appVersion:/ {gsub(/"/, "", $2); print $2; exit}' "$subchart_dir/Chart.yaml")
else
  echo "FAIL: helm dependency build did not produce flux2 subchart at $subchart_tgz nor $subchart_dir" >&2
  exit 1
fi
echo "  subchart flux2 ${chart_dep_version}.appVersion = ${app_version}"

if [ "$app_version" != "$cloudinit_version" ]; then
  cat >&2 <<EOF
FAIL: VERSION-PIN MISMATCH (catastrophic regression).
  cloud-init's install.yaml URL pins upstream Flux: v${cloudinit_version}
  bp-flux Chart.yaml's flux2 subchart pin (${chart_dep_version}) carries
    appVersion: ${app_version}

  These MUST match — bp-flux's HelmRelease will run \`helm install\` on
  top of the cloud-init-installed Flux. A version mismatch makes the
  CRD storedVersions update fail, Helm rolls back, and the rollback
  DELETES the running Flux controllers.

  Live verified on omantel.omani.works (2026-04-29). Either:
    (a) bump $CLOUDINIT_TPL to install v${app_version}, or
    (b) bump $CHART_DIR/Chart.yaml's flux2 subchart to a version whose
        appVersion equals v${cloudinit_version}.
EOF
  exit 1
fi
echo "  PASS: cloud-init v${cloudinit_version} == subchart appVersion ${app_version}"

# ── Case 4 — values.yaml catalystBlueprint metadata mirrors Chart.yaml dep ──
echo "[version-pin-replay] Case 4: values.yaml catalystBlueprint.upstream.version mirrors the Chart.yaml dep pin"
values_meta_version=$(awk '
  /catalystBlueprint:/ {in_meta=1; next}
  in_meta && /upstream:/ {
    line=$0
    sub(/.*version:[[:space:]]*"?/, "", line)
    sub(/".*/, "", line)
    sub(/[,}].*/, "", line)
    gsub(/[[:space:]]/, "", line)
    print line
    exit
  }
' "$CHART_DIR/values.yaml")
if [ -z "$values_meta_version" ]; then
  echo "FAIL: values.yaml does not declare catalystBlueprint.upstream.version (provenance metadata missing)." >&2
  exit 1
fi
if [ "$values_meta_version" != "$chart_dep_version" ]; then
  echo "FAIL: values.yaml catalystBlueprint.upstream.version (${values_meta_version}) != Chart.yaml flux2 subchart version (${chart_dep_version}). Provenance metadata is out of sync." >&2
  exit 1
fi
echo "  PASS: values.yaml metadata = Chart.yaml dep = ${chart_dep_version}"

# ── Case 5 — `helm template` renders cleanly with default values ─────
echo "[version-pin-replay] Case 5: helm template renders cleanly and contains the version-aligned Flux controller payload"
helm template smoke-flux "$CHART_DIR" > "$TMP/render.yaml" 2> "$TMP/render.err" || {
  echo "FAIL: helm template render failed:" >&2
  cat "$TMP/render.err" >&2
  exit 1
}
for ctl in source-controller kustomize-controller helm-controller notification-controller; do
  if ! grep -q "name: ${ctl}$" "$TMP/render.yaml"; then
    echo "FAIL: rendered chart missing Flux controller Deployment: ${ctl}" >&2
    exit 1
  fi
done
echo "  PASS: rendered chart contains all four core Flux controllers"

# ── Case 6 — rollback-destruction precondition replay ────────────────
# Simulate the disagreement that caused the omantel destruction by
# planting a fake `Chart.yaml` with a mismatched flux2 dep, run this
# very test in dry-mode against it, and assert it FAILS. This is the
# regression-guard's regression-guard: prove the test itself rejects
# the catastrophic precondition.
echo "[version-pin-replay] Case 6: replay test rejects a fake mismatched Chart.yaml (self-test of the gate)"
fake_chart="$TMP/fake-chart"
mkdir -p "$fake_chart/charts"
cp "$CHART_DIR/values.yaml" "$fake_chart/values.yaml"
cat > "$fake_chart/Chart.yaml" <<YAML
apiVersion: v2
name: bp-flux
version: 9.9.9
type: application
dependencies:
  - name: flux2
    version: "2.13.0"
    repository: "https://fluxcd-community.github.io/helm-charts"
YAML
# Re-use the already-fetched 2.13.0 subchart if present in the working
# tree; otherwise download it via helm dependency build.
if [ -f "$REPO_ROOT/.test-cache/flux2-2.13.0.tgz" ]; then
  cp "$REPO_ROOT/.test-cache/flux2-2.13.0.tgz" "$fake_chart/charts/flux2-2.13.0.tgz"
else
  ( cd "$fake_chart" && helm dependency build >"$TMP/fake-dep-build.log" 2>&1 ) || {
    echo "  (skip Case 6: could not fetch flux2 2.13.0 for the self-test)" >&2
    echo "[version-pin-replay] All upstream gates green; self-test skipped (offline)."
    exit 0
  }
fi
# Run THIS test against the fake chart and assert non-zero exit.
# Pass REPO_ROOT through so Case 2 (cloud-init lookup) still resolves.
if REPO_ROOT="$REPO_ROOT" bash "$0" "$fake_chart" >"$TMP/fake.out" 2>&1; then
  echo "FAIL: self-test did NOT reject the mismatched fake chart — the gate is broken." >&2
  cat "$TMP/fake.out" >&2
  exit 1
fi
if ! grep -q "VERSION-PIN MISMATCH" "$TMP/fake.out"; then
  echo "FAIL: self-test rejected the fake chart but not for the expected reason. Output:" >&2
  cat "$TMP/fake.out" >&2
  exit 1
fi
echo "  PASS: self-test correctly rejected the catastrophic fake (mismatch detected)"

echo "[version-pin-replay] All bp-flux version-pin gates green."
