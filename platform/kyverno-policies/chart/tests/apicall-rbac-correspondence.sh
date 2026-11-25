#!/usr/bin/env bash
# apicall-rbac-correspondence.sh - every cluster lookup of a policy has the
# read it needs (#6832, generalises #5505; rewritten for the CEL lookups of
# the policies.kyverno.io kinds in CMP-064).
#
# A policy that looks up cluster objects (resource.Get or resource.List in a
# CEL expression) needs read on them, granted to the Kyverno controller that
# evaluates its kind. Without it the lookup is refused ("permission denied")
# and the result is an error forever: pass and fail never happen. That is
# SILENT while the policy audits and a cluster-wide outage once it enforces
# behind the fail-closed webhook. #5505 was exactly that on cilium.io: harmless
# at Audit, then every Namespace create denied. #6832 was the same defect on
# storage.k8s.io, unnoticed for as long as it shipped.
#
# On the default render of the chart this test fails when
#   C1 a lookup does not name its apiVersion and resource as two string
#      literals, or is a function other than Get and List: it cannot be read;
#   C2 the render holds no lookup (the check would inspect nothing);
#   C3 a controller that evaluates the policy's kind (CONTROLLERS) can read
#      the looked-up resource neither through its upstream roles (UPSTREAM)
#      nor through a ClusterRole of the render aggregated to it by the label
#      rbac.kyverno.io/aggregate-to-<controller>: "true" that grants the verb
#      (get for resource.Get, list for resource.List) on that group and
#      resource; or the kind is not in CONTROLLERS;
#   C4 the render holds a legacy apiCall (urlPath), which this test does not
#      read.
#
# Usage:
#   bash apicall-rbac-correspondence.sh              the chart above this file
#   bash apicall-rbac-correspondence.sh <chart>      that chart directory
#   bash apicall-rbac-correspondence.sh --self-test  prove each check can fail
#
# Exit code: 0 pass. 1 a check failed. 2 wrong usage.
#
# HELM_BIN  The helm command. Default: helm. The self-test sets a fake one.
# Needs yq (mikefarah v4).
set -euo pipefail
export LC_ALL=C

HELM="${HELM_BIN:-helm}"
TAG="[apicall-rbac]"

# The controllers that evaluate a policy kind: <kind> <controller>... A
# ValidatingPolicy is evaluated at admission and by the background scan of the
# reports controller; a DeletingPolicy by the cleanup controller (Kyverno
# v1.19.2 cmd/reports-controller, cmd/cleanup-controller).
CONTROLLERS=(
  "ValidatingPolicy admission-controller reports-controller"
  "DeletingPolicy cleanup-controller"
)

# What a controller reads without a grant of this chart, as <group>/<resource>
# (core has the group ""), from the engine's upstream chart 3.9.2: it binds the
# Kubernetes role `view` (get, list and watch on these, never on Secrets) to
# the admission, background and reports controllers, not to the cleanup
# controller, whose own role reads Namespaces and ConfigMaps.
VIEW='^(/(configmaps|endpoints|events|limitranges|namespaces|persistentvolumeclaims|pods|replicationcontrollers|resourcequotas|serviceaccounts|services)|apps/(controllerrevisions|daemonsets|deployments|replicasets|statefulsets)|autoscaling/horizontalpodautoscalers|batch/(cronjobs|jobs)|discovery\.k8s\.io/endpointslices|networking\.k8s\.io/(ingresses|networkpolicies)|policy/poddisruptionbudgets)$'
UPSTREAM=(
  "admission-controller $VIEW"
  "background-controller $VIEW"
  "reports-controller $VIEW"
  "cleanup-controller ^/(configmaps|namespaces)$"
)

# check_render <render file>: C1 to C4. Prints one line per finding and a
# summary. Returns 1 when a check fails.
check_render() {
  local render="$1" rc=0 kind name text calls lookups=0 call fn api res group verb entry ctls ctl ok pattern grant
  local -a list
  if grep -q -E '^ *urlPath:' "$render"; then
    echo "$TAG FAIL C4: the render holds a legacy apiCall (urlPath); rewrite it as a CEL lookup, which this test reads"
    rc=1
  fi
  # ctl|group|resource|verb for every rule of every ClusterRole aggregated
  # to a Kyverno controller.
  yq -N 'select(.kind == "ClusterRole" and .metadata.labels != null) |
    [.metadata.labels | to_entries | .[] |
      select((.key | test("^rbac[.]kyverno[.]io/aggregate-to-")) and .value == "true") |
      .key | sub("^rbac[.]kyverno[.]io/aggregate-to-"; "")] as $ctls |
    .rules[] | select(.apiGroups != null and .resources != null and .verbs != null) |
    . as $r | $ctls[] as $c | $r.apiGroups[] as $g | $r.resources[] as $res | $r.verbs[] as $v |
    $c + "|" + $g + "|" + $res + "|" + $v' "$render" > "$TMP/grants"
  # kind|name|text for every string of a document that holds a lookup.
  yq -N 'select(.kind != null and .metadata.name != null) | .kind as $k | .metadata.name as $n |
    .. | select(tag == "!!str") | select(test("resource[.][A-Za-z]+[(]")) |
    $k + "|" + $n + "|" + sub("\n"; " ")' "$render" > "$TMP/lookups"
  while IFS='|' read -r kind name text; do
    calls="$({ grep -o -E 'resource[.][A-Za-z]+[(]' <<<"$text" || true; } | wc -l)"
    mapfile -t list < <(grep -o -E 'resource[.](Get|List)[(] *"[^"]*" *, *"[^"]*"' <<<"$text" || true)
    if [ "${#list[@]}" -ne "$calls" ]; then
      echo "$TAG FAIL C1 $kind/$name: $calls lookups, ${#list[@]} of them resource.Get or resource.List with a literal apiVersion and resource: $text"
      rc=1
    fi
    ctls=""
    for entry in "${CONTROLLERS[@]}"; do
      if [ "${entry%% *}" = "$kind" ]; then ctls="${entry#* }"; fi
    done
    for call in "${list[@]}"; do
      lookups=$((lookups + 1))
      fn="$(sed -E 's/^resource[.]([A-Za-z]+).*/\1/' <<<"$call")"
      api="$(sed -E 's/^[^"]*"([^"]*)".*/\1/' <<<"$call")"
      res="$(sed -E 's/.*"([^"]*)"$/\1/' <<<"$call")"
      group=""
      case "$api" in */*) group="${api%/*}" ;; esac
      verb="get"
      [ "$fn" = List ] && verb="list"
      if [ -z "$ctls" ]; then
        echo "$TAG FAIL C3 $kind/$name: resource.$fn of $group/$res, and $kind is not in CONTROLLERS of this file"
        rc=1
        continue
      fi
      for ctl in $ctls; do
        ok=0
        for entry in "${UPSTREAM[@]}"; do
          pattern="${entry#* }"
          if [ "${entry%% *}" = "$ctl" ] && [[ "$group/$res" =~ $pattern ]]; then ok=1; fi
        done
        for grant in "$ctl|$group|$res|$verb" "$ctl|$group|$res|*" "$ctl|$group|*|$verb" "$ctl|$group|*|*" \
          "$ctl|*|$res|$verb" "$ctl|*|$res|*" "$ctl|*|*|$verb" "$ctl|*|*|*"; do
          if grep -q -x -F "$grant" "$TMP/grants"; then ok=1; fi
        done
        if [ "$ok" -eq 0 ]; then
          echo "$TAG FAIL C3 $kind/$name: resource.$fn of $group/$res needs $verb for the $ctl, and no ClusterRole of this chart aggregated to it grants that (#6832: the lookup is refused and the result is an error forever)"
          rc=1
        fi
      done
      echo "$TAG lookup $kind/$name: resource.$fn $group/$res ($verb) for $ctls"
    done
  done < "$TMP/lookups"
  if [ "$lookups" -eq 0 ]; then
    echo "$TAG FAIL C2: the render holds no lookup; the check is inspecting nothing (did the render break?)"
    rc=1
  fi
  [ "$rc" -eq 0 ] && echo "$TAG PASS: $lookups lookups, each readable by every controller of its kind"
  return "$rc"
}

run_test() {
  local chart="$1"
  if ! "$HELM" template kp "$chart" > "$TMP/render.yaml" 2> "$TMP/err"; then
    cat "$TMP/err"
    echo "$TAG FAIL: the chart does not render"
    return 1
  fi
  check_render "$TMP/render.yaml"
}

# st_render <file> <document>...: a fake render, the documents joined by ---.
st_render() {
  local out="$1" doc
  shift
  : > "$out"
  for doc in "$@"; do
    printf -- '---\n%s\n' "$doc" >> "$out"
  done
}

# st_vpol <kind> <expression>: a policy document holding one variable.
st_vpol() {
  printf 'apiVersion: policies.kyverno.io/v1\nkind: %s\nmetadata:\n  name: p\nspec:\n  variables:\n    - name: v\n      expression: %s\n' "$1" "$2"
}

# st_role <controllers> <group> <resource> <verbs>: a ClusterRole aggregated
# to the controllers (comma-separated).
st_role() {
  local labels="" c
  for c in ${1//,/ }; do
    labels="$labels    rbac.kyverno.io/aggregate-to-$c: \"true\"\n"
  done
  printf 'apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: r\n  labels:\n%brules:\n  - apiGroups: ["%s"]\n    resources: ["%s"]\n    verbs: [%s]\n' "$labels" "$2" "$3" "$4"
}

# st_case <name> <wanted exit code> <document>...: a case that wants 1 is
# named after its check (C1 to C4) and passes only when that check, and no
# other, prints a FAIL line, so a case cannot pass for another check's reason.
st_case() {
  local label="$1" want="$2" rc=0 check all own
  shift 2
  check="${label%% *}"
  st_render "$ST_TMP/render.yaml" "$@"
  check_render "$ST_TMP/render.yaml" > "$ST_TMP/out" 2>&1 || rc=$?
  all="$(grep -c -F "$TAG FAIL " "$ST_TMP/out" || true)"
  own="$(grep -c -F -e "$TAG FAIL $check " -e "$TAG FAIL $check:" "$ST_TMP/out" || true)"
  if [ "$rc" != "$want" ]; then
    cat "$ST_TMP/out"
    echo "self-test FAIL: $label: exit $rc, wanted $want"
    ST_FAILED=1
  elif [ "$want" = 1 ] && { [ "$own" -eq 0 ] || [ "$own" -ne "$all" ]; }; then
    cat "$ST_TMP/out"
    echo "self-test FAIL: $label: $own FAIL lines of $check, $all FAIL lines in all"
    ST_FAILED=1
  fi
  ST_CASES=$((ST_CASES + 1))
}

self_test() {
  local get list
  ST_TMP="$TMP/self-test"
  mkdir -p "$ST_TMP"
  ST_FAILED=0
  ST_CASES=0
  get="'resource.Get(\"storage.k8s.io/v1\", \"storageclasses\", \"\", object.spec.storageClassName)'"
  list="'resource.List(\"networking.k8s.io/v1\", \"networkpolicies\", object.metadata.name)'"
  st_case "a granted lookup passes" 0 "$(st_vpol ValidatingPolicy "$get")" \
    "$(st_role admission-controller,reports-controller storage.k8s.io storageclasses '"get", "list"')"
  st_case "a lookup the upstream view role covers passes without a grant" 0 "$(st_vpol ValidatingPolicy "$list")"
  st_case "C1 a lookup with a variable apiVersion fails" 1 \
    "$(st_vpol ValidatingPolicy "'resource.Get(variables.api, \"storageclasses\", \"\", \"x\")'")" \
    "$(st_vpol ValidatingPolicy "$get")" \
    "$(st_role admission-controller,reports-controller storage.k8s.io storageclasses '"get"')"
  st_case "C1 a lookup of a function other than Get and List fails" 1 \
    "$(st_vpol ValidatingPolicy "'resource.Post(\"authorization.k8s.io/v1\", \"subjectaccessreviews\", {})'")" \
    "$(st_vpol ValidatingPolicy "$get")" \
    "$(st_role admission-controller,reports-controller storage.k8s.io storageclasses '"get"')"
  st_case "C2 a render without a lookup fails" 1 "$(st_role admission-controller storage.k8s.io storageclasses '"get"')"
  st_case "C3 a lookup without a grant fails" 1 "$(st_vpol ValidatingPolicy "$get")"
  st_case "C3 a grant of another verb fails" 1 "$(st_vpol ValidatingPolicy "$get")" \
    "$(st_role admission-controller,reports-controller storage.k8s.io storageclasses '"list"')"
  st_case "C3 a grant to one of the two controllers fails" 1 "$(st_vpol ValidatingPolicy "$get")" \
    "$(st_role admission-controller storage.k8s.io storageclasses '"get"')"
  st_case "C3 the view role does not cover the cleanup controller" 1 "$(st_vpol DeletingPolicy "$list")"
  st_case "C3 a kind not in CONTROLLERS fails" 1 "$(st_vpol MutatingPolicy "$get")" \
    "$(st_role admission-controller,reports-controller,background-controller storage.k8s.io storageclasses '"get"')"
  st_case "C4 a legacy apiCall fails" 1 "$(st_vpol ValidatingPolicy "$list")" \
    "$(printf 'apiVersion: kyverno.io/v1\nkind: ClusterPolicy\nmetadata:\n  name: legacy\nspec:\n  rules:\n    - context:\n        - name: x\n          apiCall:\n            urlPath: "/api/v1/namespaces"\n')"
  if [ "$ST_FAILED" -ne 0 ]; then
    return 1
  fi
  echo "self-test: $ST_CASES cases passed"
}

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

case "${1:-}" in
  --self-test) self_test ;;
  -*) echo "usage: $0 [<chart> | --self-test]" >&2; exit 2 ;;
  "") run_test "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)" ;;
  *) run_test "$1" ;;
esac
