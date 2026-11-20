#!/usr/bin/env bash
# bp-kured: the cluster DNS keeps one copy through a drain (SOV-138).
#
# CoreDNS is the k3s add-on: Deployment kube-system/coredns, Pods labelled
# k8s-app: kube-dns (k3s manifests/coredns.yaml at the kit's k3s release), no
# replica count, so one copy. The registry's components and every caller of the
# registry resolve through it, so its only copy on a drained node is a single
# Pod on the pull path. This chart, which drains the nodes, renders next to it a
# HorizontalPodAutoscaler that holds it at two copies and a PodDisruptionBudget
# of minAvailable 1.
#
# Cases (bash, helm and yq; no cluster):
#   1. The kit's render (release kured, namespace kube-system): exactly one
#      HorizontalPodAutoscaler, autoscaling/v2, kube-system/coredns, on the
#      apps/v1 Deployment coredns, minReplicas 2 and maxReplicas 2.
#   2. Exactly one PodDisruptionBudget, policy/v1, kube-system/coredns,
#      minAvailable 1, unhealthyPodEvictionPolicy AlwaysAllow, selector exactly
#      k8s-app: kube-dns; the budget leaves a drain one eviction at two copies.
#   3. A release in another namespace still puts both into kube-system.
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  # A helm home of its own: the build reads no repository list of the host.
  HELM_CONFIG_HOME="$TMP/hc" HELM_CACHE_HOME="$TMP/hk" HELM_DATA_HOME="$TMP/hd" helm dependency update >/dev/null
fi

# one <render> <kind>: namespace/name of each object of that kind, one per line.
one() {
  yq ea -N "select(.kind == \"$2\") | .metadata.namespace + \"/\" + .metadata.name" "$TMP/$1.yaml"
}
field() { # field <render> <kind> <path>
  yq ea -N "select(.kind == \"$2\") | $3" "$TMP/$1.yaml"
}

check() { # check <render>
  local r="$1" v
  one "$r" HorizontalPodAutoscaler > "$TMP/hpa"
  [ "$(cat "$TMP/hpa")" = "kube-system/coredns" ] || fail "$r: the autoscalers are '$(tr '\n' ' ' < "$TMP/hpa")', expected kube-system/coredns"
  v="$(field "$r" HorizontalPodAutoscaler '.apiVersion + "," + .spec.scaleTargetRef.apiVersion + "," + .spec.scaleTargetRef.kind + "," + .spec.scaleTargetRef.name + "," + (.spec.minReplicas | tostring) + "," + (.spec.maxReplicas | tostring)')"
  [ "$v" = "autoscaling/v2,apps/v1,Deployment,coredns,2,2" ] || fail "$r: the autoscaler is '$v', expected two copies of the Deployment coredns"
  one "$r" PodDisruptionBudget > "$TMP/pdb"
  [ "$(cat "$TMP/pdb")" = "kube-system/coredns" ] || fail "$r: the budgets are '$(tr '\n' ' ' < "$TMP/pdb")', expected kube-system/coredns"
  v="$(field "$r" PodDisruptionBudget '.apiVersion + "," + (.spec.minAvailable | tostring) + "," + (.spec.maxUnavailable // "-" | tostring) + "," + (.spec.unhealthyPodEvictionPolicy // "-")')"
  [ "$v" = "policy/v1,1,-,AlwaysAllow" ] || fail "$r: the budget is '$v'"
  v="$(field "$r" PodDisruptionBudget '.spec.selector | to_entries | .[] | .key')"
  [ "$v" = "matchLabels" ] || fail "$r: the budget's selector has '$v', expected matchLabels only"
  v="$(field "$r" PodDisruptionBudget '.spec.selector.matchLabels | to_entries | .[] | .key + "=" + .value')"
  [ "$v" = "k8s-app=kube-dns" ] || fail "$r: the budget selects '$v', expected the CoreDNS Pods (k8s-app=kube-dns)"
  # The budget allows minReplicas - minAvailable evictions at the autoscaler's floor.
  v="$(field "$r" HorizontalPodAutoscaler '.spec.minReplicas')"
  [ "$v" -gt 1 ] || fail "$r: at $v copies a budget of minAvailable 1 refuses every eviction"
}

echo "[kured] coredns case 1 and 2: the kit's render"
helm template kured . --namespace kube-system > "$TMP/kit.yaml" || fail "the render errored"
check kit
echo "[kured] coredns case 3: a release in another namespace"
helm template bp-kured . --namespace flux-system > "$TMP/other.yaml" || fail "the render in flux-system errored"
check other
echo "coredns-availability: all cases passed"
