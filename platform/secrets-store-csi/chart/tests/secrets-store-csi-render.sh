#!/usr/bin/env bash
# bp-secrets-store-csi: render test of the CSI driver wrapper. Needs bash, helm
# and yq v4; no cluster. Each case reads the default render and fails when
# the setting it names is gone; case 1 also renders with Secret sync on and
# fails unless that render grants a right on secrets, so the case can fail.
#   1. no Role or ClusterRole of the render names the resource secrets;
#   2. the driver container rotates every 3600s;
#   3. the CSIDriver asks the kubelet for a token with audience openbao;
#   4. every container image comes through harbor.openova.io/proxy-*;
#   5. every container declares cpu and memory requests;
#   6. no Windows DaemonSet is rendered.
# Usage: bash tests/secrets-store-csi-render.sh [CHART_DIR]
set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
if [ ! -d "$CHART_DIR/charts" ] || [ -z "$(ls -A "$CHART_DIR/charts" 2>/dev/null)" ]; then
  # A helm home of its own: the build reads no repository list of the host.
  HELM_CONFIG_HOME="$TMP/hc" HELM_CACHE_HOME="$TMP/hk" HELM_DATA_HOME="$TMP/hd" helm dependency update "$CHART_DIR" >/dev/null
fi
helm template secrets-store-csi "$CHART_DIR" --namespace kube-system > "$TMP/default.yaml" \
  || fail "the default render errored"
R="$TMP/default.yaml"

# secret_rules FILE: how many rules of a Role or ClusterRole name the resource secrets.
secret_rules() {
  yq eval-all -N '[select(.kind == "ClusterRole" or .kind == "Role") | .rules[] | select(.resources[] == "secrets")] | length' "$1"
}

echo "[bp-secrets-store-csi] case 1: the driver has no right on Secret objects"
helm template secrets-store-csi "$CHART_DIR" --namespace kube-system \
  --set secrets-store-csi-driver.syncSecret.enabled=true > "$TMP/sync.yaml" || fail "the render with Secret sync errored"
[ "$(secret_rules "$TMP/sync.yaml")" -gt 0 ] || fail "case 1 cannot fail: the render with Secret sync on grants no right on secrets"
[ "$(secret_rules "$R")" = 0 ] || fail "case 1: a Role of the default render names secrets: $(secret_rules "$R") rules"
echo "  PASS"

echo "[bp-secrets-store-csi] case 2: rotation every 3600s"
args="$(yq eval-all -N 'select(.kind == "DaemonSet") | .spec.template.spec.containers[] | select(.name == "secrets-store") | .args[]' "$R")"
grep -qx -- '--enable-secret-rotation=true' <<<"$args" || fail "case 2: the driver does not rotate"
grep -qx -- '--rotation-poll-interval=3600s' <<<"$args" || fail "case 2: the rotation interval is not 3600s"
echo "  PASS"

echo "[bp-secrets-store-csi] case 3: the kubelet token has audience openbao"
aud="$(yq eval-all -N 'select(.kind == "CSIDriver") | .spec.tokenRequests[] | .audience' "$R")"
[ "$aud" = openbao ] || fail "case 3: the CSIDriver token audiences are [$aud], not [openbao]"
echo "  PASS"

echo "[bp-secrets-store-csi] case 4: every image comes through the Harbor proxy"
images="$(yq eval-all -N 'select(.kind == "DaemonSet" or .kind == "Job") | .spec.template.spec | (.initContainers // []) + .containers | .[] | .image' "$R")"
[ "$(grep -c . <<<"$images")" -ge 5 ] || fail "case 4: fewer than five containers in the render: $images"
bad="$(grep -v '^harbor\.openova\.io/proxy-[a-z0-9-]*/' <<<"$images" || true)"
[ -z "$bad" ] || fail "case 4: images outside the Harbor proxy: $bad"
echo "  PASS"

echo "[bp-secrets-store-csi] case 5: every container declares cpu and memory requests"
missing="$(yq eval-all -N 'select(.kind == "DaemonSet" or .kind == "Job") | .spec.template.spec | (.initContainers // []) + .containers | .[] | select(.resources.requests.cpu == null or .resources.requests.memory == null) | .name' "$R")"
[ -z "$missing" ] || fail "case 5: containers without requests: $missing"
with="$(yq eval-all -N 'select(.kind == "DaemonSet" or .kind == "Job") | .spec.template.spec | (.initContainers // []) + .containers | .[] | select(.resources.requests.cpu != null and .resources.requests.memory != null) | .name' "$R" | grep -c . || true)"
[ "$with" -ge 5 ] || fail "case 5: fewer than five containers declare requests: $with"
echo "  PASS"

echo "[bp-secrets-store-csi] case 6: no Windows DaemonSet"
[ "$(yq eval-all -N '[select(.kind == "DaemonSet")] | length' "$R")" = 1 ] || fail "case 6: the render holds more than the one Linux DaemonSet"
echo "  PASS"

echo "[bp-secrets-store-csi SEC-211] All cases PASS"
