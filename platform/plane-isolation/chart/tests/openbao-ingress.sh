#!/usr/bin/env bash
# bp-plane-isolation — cert-manager may reach OpenBao (SEC-250).
#
# The ClusterIssuers openbao-internal and sovereign-mesh-ca of
# bp-cert-manager-issuers (templates/clusterissuer-openbao-internal.yaml) sign
# at OpenBao from the cert-manager controller, which runs in the namespace kit
# slot 02 installs bp-cert-manager into. NetworkPolicy openbao-default-deny of
# this chart, rendered with the values of kit slot 26b (it sets no
# `components`, so the chart's own list is the one a Sovereign gets), admits
# only the namespaces it lists. Without that namespace the dial is dropped and
# neither issuer becomes Ready, on a Sovereign of one region too.
#
#   O1  both ClusterIssuers of the PKI dial the Service openbao in namespace
#       openbao on port 8200, from the service account cert-manager of the
#       namespace of kit slot 02.
#   O2  openbao-default-deny, in namespace openbao, has an ingress rule that
#       admits exactly that namespace on every port.
#   O3  the check of O2 can fail: the same render with that namespace taken
#       out of the openbao entry is refused.
#
# CI runs every chart/tests/*.sh as `bash <script> <chart dir>` under the yq of
# the release (v4.44.3); only plain selections and index access are used.
set -euo pipefail

chart_dir="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
repo="$(cd "$chart_dir/../../.." && pwd)"
issuers_chart="$repo/platform/cert-manager-issuers/chart"
kit="$repo/clusters/_template/bootstrap-kit"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
FAIL=0
fail() { echo "  FAIL  $1"; FAIL=1; }
pass() { echo "  ok    $1"; }
die() { echo "FAIL: $1" >&2; exit 1; }
# Both charts need their dependency built to render; a lock this build wrote is
# removed again, so each chart is left as found (as tests/pki-render.sh of bp-openbao).
for c in "$chart_dir" "$issuers_chart"; do
  if ! ls "$c"/charts/*.tgz >/dev/null 2>&1; then
    had_lock=0; [ ! -e "$c/Chart.lock" ] || had_lock=1
    helm dependency build "$c" >/dev/null
    [ "$had_lock" = 1 ] || rm -f "$c/Chart.lock"
  fi
done

echo "[bp-plane-isolation] O1: who dials OpenBao for the PKI"
cm_ns="$(yq ea -N 'select(.kind == "HelmRelease" and .metadata.name == "bp-cert-manager") | .spec.targetNamespace' "$kit/02-cert-manager.yaml")"
[ -n "$cm_ns" ] || die "kit slot 02 names no targetNamespace for bp-cert-manager"
helm template cert-manager-issuers "$issuers_chart" --namespace "$cm_ns" --show-only templates/clusterissuer-openbao-internal.yaml > "$tmp/issuers.yaml"
for i in openbao-internal sovereign-mesh-ca; do
  got="$(I="$i" yq ea -N 'select(.kind == "ClusterIssuer" and .metadata.name == strenv(I)) | .spec.vault.server + " " + .spec.vault.auth.kubernetes.serviceAccountRef.name' "$tmp/issuers.yaml")"
  [ "$got" = "http://openbao.openbao.svc.cluster.local:8200 cert-manager" ] && pass "$i dials openbao.openbao:8200 as $cm_ns/cert-manager" || fail "$i: server and service account are '$got'"
done

# admitted <render file>: the namespaces openbao-default-deny admits on every port, one per line.
admitted() {
  yq ea -N 'select(.kind == "NetworkPolicy" and .metadata.name == "openbao-default-deny" and .metadata.namespace == "openbao") | .spec.ingress[] | select(.ports == null) | .from[] | .namespaceSelector.matchLabels["kubernetes.io/metadata.name"]' "$1" | grep -v -x -F null || true
}

echo "[bp-plane-isolation] O2: openbao-default-deny admits $cm_ns"
yq ea -N 'select(.kind == "HelmRelease" and .metadata.name == "bp-plane-isolation") | .spec.values' "$kit/26b-bp-plane-isolation.yaml" > "$tmp/slot-values.yaml"
n="$(yq '.components | length' "$tmp/slot-values.yaml")"
[ "$n" = 0 ] || die "kit slot 26b sets $n components of its own; this test reads the chart's"
helm template plane-isolation "$chart_dir" --api-versions cilium.io/v2 --skip-tests -f "$tmp/slot-values.yaml" > "$tmp/render.yaml"
admitted "$tmp/render.yaml" > "$tmp/admitted"
[ -s "$tmp/admitted" ] || die "the render has no NetworkPolicy openbao-default-deny in namespace openbao"
grep -qxF "$cm_ns" "$tmp/admitted" && pass "openbao-default-deny admits $cm_ns on every port" || fail "openbao-default-deny does not admit $cm_ns; it admits: $(tr '\n' ' ' < "$tmp/admitted")"

echo "[bp-plane-isolation] O3: the check can fail"
NS="$cm_ns" yq '(.components[] | select(.namespace == "openbao") | .allowIngressFrom) |= map(select(. != strenv(NS)))' "$chart_dir/values.yaml" > "$tmp/without.yaml"
helm template plane-isolation "$chart_dir" --api-versions cilium.io/v2 --skip-tests -f "$tmp/without.yaml" > "$tmp/render-without.yaml"
admitted "$tmp/render-without.yaml" > "$tmp/admitted-without"
if [ -s "$tmp/admitted-without" ] && ! grep -qxF "$cm_ns" "$tmp/admitted-without"; then
  pass "without $cm_ns in the openbao entry the check refuses the render"
else
  fail "without $cm_ns in the openbao entry the check still sees: $(tr '\n' ' ' < "$tmp/admitted-without")"
fi

[ "$FAIL" = 0 ] || { echo "FAIL: bp-plane-isolation openbao ingress"; exit 1; }
echo "PASS: bp-plane-isolation openbao ingress"
