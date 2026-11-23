#!/usr/bin/env bash
# bp-keycloak — who may reach the Keycloak Pods (SVC-G06, 22 Nov 2026).
#
# Guards the fault measured on hw307 on 21 Nov 2026 in both regions:
# bp-plane-isolation's function test, a Pod in the namespace default, reached
# Keycloak, because the subchart's NetworkPolicy `keycloak` (allowExternal
# true) admitted every source on 8080 and on 7800, the Infinispan cluster
# transport. The values (keycloak.networkPolicy) say why.
#
# Cases:
#   1. the only ingress rule without `from` admits 8080 alone: Keycloak's
#      issuer stays open to every source, 7800 is not in it
#   2. with metrics on, that rule also admits the management port, as the
#      subchart's own rule did (an opt-in scraper is not cut off)
#   3. every rule on 7800 names its sources: Keycloak's own Pods of this
#      namespace (a podSelector, no namespaceSelector)
#   4. one rule on 7800 admits the Keycloak Pods of every cluster of the mesh
#      (io.cilium.k8s.policy.cluster Exists), so the peer region's member
#      still joins the one Infinispan cluster
#   5. egress is unchanged: one rule, `{}`
#
# On main this fails at case 1 (the rule without `from` lists 7800).
# yq expressions use plain index access only (chart tests run under yq
# v4.44.3, .github/workflows/chart-tests-pr.yaml lines 5-34).

set -euo pipefail

chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm="${HELM_BIN:-helm}"

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $1" >&2; exit 1; }

NP=charts/keycloak/templates/networkpolicy.yaml

render() {
  "$helm" template keycloak "$chart_dir" \
    --namespace keycloak \
    --set gateway.host=auth.smoke.omani.works \
    --set sovereignFQDN=smoke.omani.works \
    --show-only "$NP" "$@"
}

render > "$tmp/np.yaml"
[ "$(yq '.kind' "$tmp/np.yaml")" = "NetworkPolicy" ] || fail "the subchart renders no NetworkPolicy"
[ "$(yq '.metadata.name' "$tmp/np.yaml")" = "keycloak" ] || fail "the subchart NetworkPolicy is not named keycloak"

open_ports() { yq '.spec.ingress[] | select(has("from") | not) | .ports[] | .port' "$1" | sort -u | tr '\n' ' '; }

echo "[network-policy] Case 1: the only ingress rule without from admits 8080 alone"
[ "$(yq '[.spec.ingress[] | select(has("from") | not)] | length' "$tmp/np.yaml")" = "1" ] || fail "there is not exactly one ingress rule without from"
ports="$(open_ports "$tmp/np.yaml")"
[ "$ports" = "8080 " ] || fail "the ports admitted from every source are '${ports}', want '8080 ' (7800 must never be open to every source)"

echo "[network-policy] Case 2: with metrics on, the open rule also admits the management port"
render --set keycloak.metrics.enabled=true > "$tmp/np-metrics.yaml"
ports="$(open_ports "$tmp/np-metrics.yaml")"
[ "$ports" = "8080 9000 " ] || fail "with metrics on the ports admitted from every source are '${ports}', want '8080 9000 '"

echo "[network-policy] Case 3: every rule on 7800 admits only Keycloak's own Pods of this namespace"
rules_7800="$(yq '[.spec.ingress[] | select(.ports | any_c(.port == 7800))] | length' "$tmp/np.yaml")"
[ "$rules_7800" = "2" ] || fail "the policy has ${rules_7800} ingress rules on 7800, want 2 (this region's Pods, and every cluster's)"
[ "$(yq '[.spec.ingress[] | select(.ports | any_c(.port == 7800)) | select(has("from") | not)] | length' "$tmp/np.yaml")" = "0" ] || fail "a rule on 7800 names no source"
yq '.spec.ingress[] | select(.ports | any_c(.port == 7800)) | .from[]' -o json -I0 "$tmp/np.yaml" > "$tmp/from-7800.json"
if grep -q -E 'namespaceSelector|ipBlock' "$tmp/from-7800.json"; then
  fail "a rule on 7800 admits another namespace or an address range: $(tr '\n' ' ' < "$tmp/from-7800.json")"
fi
while IFS= read -r peer; do
  case "$peer" in '{"podSelector":'*) ;; *) fail "a rule on 7800 admits a source that is not a Pod of this namespace: ${peer}" ;; esac
done < "$tmp/from-7800.json"
grep -q -F '"app.kubernetes.io/name":"keycloak"' "$tmp/from-7800.json" || fail "no rule on 7800 admits Keycloak's own Pods"

echo "[network-policy] Case 4: the Keycloak Pods of every cluster of the mesh reach 7800"
yq '.spec.ingress[] | select(.ports | any_c(.port == 7800)) | .from[] | select(.podSelector.matchExpressions != null) | .podSelector' "$tmp/np.yaml" > "$tmp/mesh.yaml"
[ -s "$tmp/mesh.yaml" ] || fail "no rule on 7800 names the cluster key: Cilium (policy-default-local-cluster) then admits only this region's Keycloak and the regions' Infinispan cluster splits"
[ "$(yq '.matchExpressions | length' "$tmp/mesh.yaml")" = "1" ] || fail "the mesh rule has not exactly one expression"
[ "$(yq '.matchExpressions[0].key' "$tmp/mesh.yaml")" = "io.cilium.k8s.policy.cluster" ] || fail "the mesh rule does not name the key io.cilium.k8s.policy.cluster"
[ "$(yq '.matchExpressions[0].operator' "$tmp/mesh.yaml")" = "Exists" ] || fail "the cluster expression is not Exists"
[ "$(yq '.matchLabels["app.kubernetes.io/name"]' "$tmp/mesh.yaml")" = "keycloak" ] || fail "the mesh rule does not select the Keycloak Pods by name"
[ "$(yq '.matchLabels["app.kubernetes.io/instance"]' "$tmp/mesh.yaml")" = "keycloak" ] || fail "the mesh rule does not select the Keycloak Pods by instance"
[ "$(yq '.matchLabels["app.kubernetes.io/component"]' "$tmp/mesh.yaml")" = "keycloak" ] || fail "the mesh rule does not select the Keycloak Pods by component"
[ "$(yq '[.spec.ingress[] | select(.from[].podSelector.matchExpressions != null) | .ports[]] | length' "$tmp/np.yaml")" = "1" ] || fail "the mesh rule admits more than one port"

echo "[network-policy] Case 5: egress is unchanged"
[ "$(yq '.spec.egress | length' "$tmp/np.yaml")" = "1" ] || fail "egress has not exactly one rule"
[ "$(yq '.spec.egress[0] | length' "$tmp/np.yaml")" = "0" ] || fail "the egress rule is not {}"

echo "network-policy-render: all cases passed"
