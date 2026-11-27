#!/usr/bin/env bash
# bp-plane-isolation — the birth Job of org-services may reach OpenBao (SEC-212).
#
# bp-catalyst-platform runs the birth Job of platform/secrets-lib in namespace
# org-services (templates/org-services/org-services-secrets.yaml): it writes the
# auth service's refresh-token signing secret into OpenBao, which the auth Pod
# then mounts. NetworkPolicy openbao-default-deny of this chart, rendered with
# the values of kit slot 26b (it sets no `components`, so the chart's own list
# is the one a Sovereign gets), admits only the namespaces it lists, and
# bp-openbao's peer trust admits a birth Job only on a Sovereign of more than
# one region. Without org-services in the list the value is never born on a
# Sovereign of one region.
#
#   B1  the birth Job of bp-catalyst-platform runs in namespace org-services,
#       labelled app.kubernetes.io/name secrets-birth, and dials the Service
#       openbao in namespace openbao on port 8200 as role secrets-birth.
#   B2  openbao-default-deny, in namespace openbao, has an ingress rule that
#       admits exactly that namespace on every port.
#   B3  the check of B2 can fail: the same render with that namespace taken
#       out of the openbao entry is refused.
#
# CI runs every chart/tests/*.sh as `bash <script> <chart dir>` under the yq of
# the release (v4.44.3); only plain selections and index access are used.
set -euo pipefail

chart_dir="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
repo="$(cd "$chart_dir/../../.." && pwd)"
umbrella="$repo/products/catalyst/chart"
kit="$repo/clusters/_template/bootstrap-kit"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
FAIL=0
fail() { echo "  FAIL  $1"; FAIL=1; }
pass() { echo "  ok    $1"; }
die() { echo "FAIL: $1" >&2; exit 1; }
# This chart needs its dependency built to render; a lock this build wrote is
# removed again, so the chart is left as found (as tests/openbao-ingress.sh).
if ! ls "$chart_dir"/charts/*.tgz >/dev/null 2>&1; then
  had_lock=0; [ ! -e "$chart_dir/Chart.lock" ] || had_lock=1
  helm dependency build "$chart_dir" >/dev/null
  [ "$had_lock" = 1 ] || rm -f "$chart_dir/Chart.lock"
fi
# The umbrella depends on platform/secrets-lib (SEC-216); its charts/ and Chart.lock are not tracked.
helm dependency build --skip-refresh "$umbrella" >/dev/null

echo "[bp-plane-isolation] B1: where the birth Job of org-services runs and what it dials"
helm template smoke "$umbrella" --namespace catalyst-system --set global.sovereignFQDN=t01.omani.works \
  --set ingress.marketplace.enabled=true --set ingress.hosts.marketplace.host=marketplace.t01.omani.works \
  --show-only templates/org-services/org-services-secrets.yaml > "$tmp/umbrella.yaml"
jobs="$(yq ea -N 'select(.kind == "Job" and (.metadata.name | test("^secrets-birth-"))) | .metadata.namespace + " " + .spec.template.metadata.labels["app.kubernetes.io/name"]' "$tmp/umbrella.yaml")"
[ "$jobs" = "org-services secrets-birth" ] && pass "one birth Job, in org-services, labelled secrets-birth" || fail "the birth Jobs are '$jobs'"
env="$(yq ea -N 'select(.kind == "Job" and (.metadata.name | test("^secrets-birth-"))) | .spec.template.spec.containers[0].env[] | select(.name == "BAO_ADDR" or .name == "BAO_ROLE") | .name + "=" + .value' "$tmp/umbrella.yaml" | tr '\n' ' ')"
[ "$env" = "BAO_ADDR=http://openbao.openbao.svc.cluster.local:8200 BAO_ROLE=secrets-birth " ] && pass "it dials openbao.openbao:8200 as role secrets-birth" || fail "its address and role are '$env'"
birth_ns="$(yq ea -N 'select(.kind == "Job" and (.metadata.name | test("^secrets-birth-"))) | .metadata.namespace' "$tmp/umbrella.yaml")"
[ "$birth_ns" = org-services ] || die "the birth Job's namespace is '$birth_ns', not org-services"

# admitted <render file>: the namespaces openbao-default-deny admits on every port, one per line.
admitted() {
  yq ea -N 'select(.kind == "NetworkPolicy" and .metadata.name == "openbao-default-deny" and .metadata.namespace == "openbao") | .spec.ingress[] | select(.ports == null) | .from[] | .namespaceSelector.matchLabels["kubernetes.io/metadata.name"]' "$1" | grep -v -x -F null || true
}

echo "[bp-plane-isolation] B2: openbao-default-deny admits $birth_ns"
yq ea -N 'select(.kind == "HelmRelease" and .metadata.name == "bp-plane-isolation") | .spec.values' "$kit/26b-bp-plane-isolation.yaml" > "$tmp/slot-values.yaml"
n="$(yq '.components | length' "$tmp/slot-values.yaml")"
[ "$n" = 0 ] || die "kit slot 26b sets $n components of its own; this test reads the chart's"
helm template plane-isolation "$chart_dir" --api-versions cilium.io/v2 --skip-tests -f "$tmp/slot-values.yaml" > "$tmp/render.yaml"
admitted "$tmp/render.yaml" > "$tmp/admitted"
[ -s "$tmp/admitted" ] || die "the render has no NetworkPolicy openbao-default-deny in namespace openbao"
grep -qxF "$birth_ns" "$tmp/admitted" && pass "openbao-default-deny admits $birth_ns on every port" || fail "openbao-default-deny does not admit '$birth_ns'; it admits: $(tr '\n' ' ' < "$tmp/admitted")"

echo "[bp-plane-isolation] B3: the check can fail"
NS="$birth_ns" yq '(.components[] | select(.namespace == "openbao") | .allowIngressFrom) |= map(select(. != strenv(NS)))' "$chart_dir/values.yaml" > "$tmp/without.yaml"
helm template plane-isolation "$chart_dir" --api-versions cilium.io/v2 --skip-tests -f "$tmp/without.yaml" > "$tmp/render-without.yaml"
admitted "$tmp/render-without.yaml" > "$tmp/admitted-without"
if [ -s "$tmp/admitted-without" ] && ! grep -qxF "$birth_ns" "$tmp/admitted-without"; then
  pass "without $birth_ns in the openbao entry the check refuses the render"
else
  fail "without $birth_ns in the openbao entry the check still sees: $(tr '\n' ' ' < "$tmp/admitted-without")"
fi

[ "$FAIL" = 0 ] || { echo "FAIL: bp-plane-isolation openbao birth ingress"; exit 1; }
echo "PASS: bp-plane-isolation openbao birth ingress"
