#!/usr/bin/env bash
# bp-harbor: no single Pod on the registry's pull path (SOV-138).
#
# After the sovereignty cutover every node pulls only through this registry's
# front (nginx), core and registry. A drain of the one node that ran the single
# front left its replacement with nothing to pull from, and 14 of the 21 Pods
# moved off that node waited on it. Each of the three now runs two copies
# spread over two nodes, with a PodDisruptionBudget of minAvailable 1.
#
# Cases (bash, helm and yq; no cluster):
#   1. The kit's storage (type s3): nginx, core and registry each render two
#      copies, spread by kubernetes.io/hostname (maxSkew 1, DoNotSchedule,
#      nodeTaintsPolicy Honor), and one budget of the Deployment's name,
#      minAvailable 1, unhealthyPodEvictionPolicy AlwaysAllow, that selects
#      that Deployment's Pods and the Pods of no other Deployment.
#   2. With type s3 the registry mounts no volume claim: the two copies share
#      only the object store.
#   3. A component at one copy renders no budget (a budget of minAvailable 1
#      at one copy refuses every eviction); the others keep theirs. With
#      type filesystem and registry.replicas 1 the registry has no budget.
#   4. With expose.type ingress the front renders no Deployment and no budget.
#   5. Another release name: each budget still carries its Deployment's name
#      and selects its Pods.
#   6. The chart's defaults carry the two copies and the three budgets.
set -euo pipefail

chart_dir="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
helm="${HELM_BIN:-helm}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

# render <name> <release> [helm flags...]: the render goes to $TMP/<name>.yaml.
render() {
  local name="$1" release="$2"
  shift 2
  "$helm" template "$release" "$chart_dir" --namespace harbor "$@" > "$TMP/$name.yaml" \
    || fail "the render $name errored"
}
# names <render> <kind>: the names of the objects of that kind, one per line.
names() {
  yq ea -N "select(.kind == \"$2\") | .metadata.name" "$TMP/$1.yaml"
}
# field <render> <kind> <name> <path>: one field of one object.
field() {
  yq ea -N "select(.kind == \"$2\" and .metadata.name == \"$3\") | $4" "$TMP/$1.yaml"
}
# pairs <render> <kind> <name> <path>: the map at <path> as key=value lines.
pairs() {
  yq ea -N "select(.kind == \"$2\" and .metadata.name == \"$3\") | $4 | to_entries | .[] | .key + \"=\" + (.value | tostring)" "$TMP/$1.yaml"
}
# selects <render> <budget> <deployment>: exit 0 when every pair of the
# budget's selector is a label of the Deployment's Pod template.
selects() {
  pairs "$1" PodDisruptionBudget "$2" .spec.selector.matchLabels > "$TMP/want"
  pairs "$1" Deployment "$3" .spec.template.metadata.labels > "$TMP/have"
  [ -s "$TMP/want" ] || return 1
  local p
  while IFS= read -r p; do
    grep -q -x -F -e "$p" "$TMP/have" || return 1
  done < "$TMP/want"
}
has() { # has <render> <kind> <name>
  names "$1" "$2" > "$TMP/names"
  grep -q -x -F -e "$3" "$TMP/names"
}

# check_component <render> <deployment>: the two copies, the spread and the budget.
check_component() {
  local r="$1" d="$2" v other
  has "$r" Deployment "$d" || fail "$r: no Deployment $d"
  v="$(field "$r" Deployment "$d" .spec.replicas)"
  [ "$v" = 2 ] || fail "$r: $d runs $v copies, expected 2"
  v="$(field "$r" Deployment "$d" '.spec.template.spec.topologySpreadConstraints | length')"
  [ "$v" = 1 ] || fail "$r: $d has $v spread constraints, expected 1"
  v="$(field "$r" Deployment "$d" '.spec.template.spec.topologySpreadConstraints[0] | (.maxSkew // "-" | tostring) + "," + (.topologyKey // "-") + "," + (.whenUnsatisfiable // "-") + "," + (.nodeTaintsPolicy // "-")')"
  [ "$v" = "1,kubernetes.io/hostname,DoNotSchedule,Honor" ] || fail "$r: $d spreads as '$v'"
  pairs "$r" Deployment "$d" '.spec.template.spec.topologySpreadConstraints[0].labelSelector.matchLabels' > "$TMP/spread"
  pairs "$r" Deployment "$d" .spec.selector.matchLabels > "$TMP/selector"
  cmp -s "$TMP/spread" "$TMP/selector" || fail "$r: the spread of $d does not count the Deployment's own Pods"
  has "$r" PodDisruptionBudget "$d" || fail "$r: no PodDisruptionBudget $d, so a drain can remove the last copy"
  v="$(field "$r" PodDisruptionBudget "$d" '.apiVersion + "," + .metadata.namespace + "," + (.spec.minAvailable | tostring) + "," + (.spec.unhealthyPodEvictionPolicy // "-") + "," + (.spec.maxUnavailable // "-" | tostring)')"
  [ "$v" = "policy/v1,harbor,1,AlwaysAllow,-" ] || fail "$r: the budget $d is '$v'"
  selects "$r" "$d" "$d" || fail "$r: the budget $d does not select the Pods of the Deployment $d"
  names "$r" Deployment > "$TMP/deployments"
  while IFS= read -r other; do
    [ "$other" = "$d" ] && continue
    if selects "$r" "$d" "$other"; then fail "$r: the budget $d also selects the Pods of $other"; fi
  done < "$TMP/deployments"
}

echo "[bp-harbor] case 1: the kit's storage (s3): two copies, spread, one budget each"
render kit harbor --set harbor.persistence.imageChartStorage.type=s3
for c in nginx core registry; do check_component kit "harbor-$c"; done
names kit PodDisruptionBudget > "$TMP/budgets"
[ "$(grep -c . "$TMP/budgets")" = 3 ] || fail "kit: expected exactly the three budgets, found: $(tr '\n' ' ' < "$TMP/budgets")"
echo "[bp-harbor] case 1: PASS"

echo "[bp-harbor] case 2: with s3 the registry mounts no volume claim"
v="$(field kit Deployment harbor-registry '[.spec.template.spec.volumes[] | select(has("persistentVolumeClaim"))] | length')"
[ "$v" = 0 ] || fail "the registry with type s3 mounts $v volume claims; two copies on two nodes cannot share a ReadWriteOnce claim"
v="$(field kit Deployment harbor-registry '.spec.template.spec.volumes[] | select(.name == "registry-data") | has("emptyDir")')"
[ "$v" = true ] || fail "the registry's data volume is not an emptyDir with type s3"
! has kit PersistentVolumeClaim harbor-registry || fail "type s3 still renders the claim harbor-registry"
echo "[bp-harbor] case 2: PASS"

echo "[bp-harbor] case 3: one copy renders no budget"
render onecore harbor --set harbor.persistence.imageChartStorage.type=s3 --set harbor.core.replicas=1
[ "$(field onecore Deployment harbor-core .spec.replicas)" = 1 ] || fail "onecore: harbor-core does not run one copy"
! has onecore PodDisruptionBudget harbor-core || fail "onecore: a budget of minAvailable 1 on one copy blocks every drain of its node"
check_component onecore harbor-nginx
check_component onecore harbor-registry
render filesystem harbor --set harbor.registry.replicas=1
[ "$(field filesystem Deployment harbor-registry .spec.replicas)" = 1 ] || fail "filesystem: harbor-registry does not run one copy"
! has filesystem PodDisruptionBudget harbor-registry || fail "filesystem: the registry on its ReadWriteOnce claim gets a budget at one copy"
echo "[bp-harbor] case 3: PASS"

echo "[bp-harbor] case 4: no front Deployment, no front budget"
render ingress harbor --set harbor.persistence.imageChartStorage.type=s3 --set harbor.expose.type=ingress
! has ingress Deployment harbor-nginx || fail "ingress: the upstream chart still renders harbor-nginx; the condition of the budget is stale"
! has ingress PodDisruptionBudget harbor-nginx || fail "ingress: a budget for a front that is not rendered"
check_component ingress harbor-core
check_component ingress harbor-registry
echo "[bp-harbor] case 4: PASS"

echo "[bp-harbor] case 5: another release name"
render other registry1 --set harbor.persistence.imageChartStorage.type=s3
for c in nginx core registry; do check_component other "registry1-harbor-$c"; done
echo "[bp-harbor] case 5: PASS"

echo "[bp-harbor] case 6: the chart's defaults"
render defaults harbor
for c in nginx core registry; do check_component defaults "harbor-$c"; done
echo "[bp-harbor] case 6: PASS"

echo "[bp-harbor] pull-path availability: all cases PASS"
