#!/usr/bin/env bash
# apicall-rbac-correspondence.sh - every cluster lookup of a policy, and every
# kind a policy matches, has the read it needs (#6832, generalises #5505;
# rewritten for the CEL lookups of the policies.kyverno.io kinds in CMP-064;
# the matched kinds since forbid-local-path-storage was never ready).
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
# A policy also needs read on every kind it MATCHES. Kyverno v1.19 marks a
# ValidatingPolicy (and the other kinds the reports controller reports on)
# ready only when the reports controller can get, list and watch each resource
# of spec.matchConstraints.resourceRules and of the autogen targets (the
# admission controller asks with a SubjectAccessReview for that ServiceAccount;
# pkg/controllers/policystatus/controller.go). Otherwise the policy stays
# RBACPermissionsGranted=False, never ready, and the function test fails:
# forbid-local-path-storage on persistentvolumes, measured 26 November. It
# further fails when
#   C5 a controller that acts on the kinds a policy matches (MATCHERS) lacks a
#      verb it needs on one of them, through its upstream roles and through a
#      ClusterRole of the render aggregated to it; or the policy's kind is
#      under policies.kyverno.io and not in MATCHERS; or an autogen controller
#      cannot be resolved without a cluster;
#   C6 the render holds no kind a policy matches (the check inspects nothing);
#   C7 (with a chart) the engine chart <chart>/../../kyverno/chart
#      (platform/kyverno/chart) no longer depends on the kyverno chart
#      UPSTREAM_CHART, whose roles UPSTREAM holds.
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

# The controllers that act on the kinds a policy MATCHES, and what they need:
# <kind> <controller> <verbs> <when>. <when> is `background` when the need
# holds only while spec.evaluation.background.enabled is not false (Kyverno's
# default is true), `always` otherwise. The reports controller's get, list and
# watch are the RBACPermissionsGranted check of
# pkg/controllers/policystatus/controller.go (which also covers the autogen
# targets of the kinds with autogen); the cleanup controller lists the matched
# objects and deletes them (pkg/controllers/deleting/controller.go).
MATCHERS=(
  "ValidatingPolicy reports-controller get,list,watch background"
  "NamespacedValidatingPolicy reports-controller get,list,watch background"
  "ImageValidatingPolicy reports-controller get,list,watch background"
  "NamespacedImageValidatingPolicy reports-controller get,list,watch background"
  "MutatingPolicy reports-controller get,list,watch background"
  "NamespacedMutatingPolicy reports-controller get,list,watch background"
  "GeneratingPolicy reports-controller get,list,watch always"
  "NamespacedGeneratingPolicy reports-controller get,list,watch always"
  "DeletingPolicy cleanup-controller list,delete always"
  "NamespacedDeletingPolicy cleanup-controller list,delete always"
)
# The kinds whose autogen Kyverno computes for that check (pkg/cel/policies/
# {vpol,ivpol,mpol}/autogen), and the pod controllers it targets when the
# policy names none (pkg/cel/autogen/data.go).
AUTOGEN_KINDS=" ValidatingPolicy NamespacedValidatingPolicy ImageValidatingPolicy NamespacedImageValidatingPolicy MutatingPolicy NamespacedMutatingPolicy "
AUTOGEN_DEFAULT="daemonsets deployments replicasets statefulsets jobs cronjobs"

# What a controller reads (get, list and watch) without a grant of this chart,
# as <group>/<resource> (core has the group ""). Read from the render of the
# engine chart platform/kyverno/chart with its kyverno dependency
# UPSTREAM_CHART (C7 fails when that moves): `helm dependency build`, then
# `helm template kyverno . -n kyverno`, then for each controller the rules of
# the ClusterRoles labelled app.kubernetes.io/component=<controller> that hold
# get, list and watch. The chart binds the Kubernetes role `view` to the
# admission, background and reports controllers, not to the cleanup
# controller. VIEW is that role (Kubernetes bootstrap policy
# system:aggregate-to-view: no cluster-scoped kind, never Secrets) with the
# chart's kyverno:rbac:view:* roles aggregated into it.
UPSTREAM_CHART="3.9.2"
VIEW="/configmaps /endpoints /events /limitranges /namespaces /persistentvolumeclaims
  /pods /replicationcontrollers /resourcequotas /serviceaccounts /services
  apps/controllerrevisions apps/daemonsets apps/deployments apps/replicasets apps/statefulsets
  autoscaling/horizontalpodautoscalers batch/cronjobs batch/jobs discovery.k8s.io/endpointslices
  networking.k8s.io/ingresses networking.k8s.io/networkpolicies policy/poddisruptionbudgets
  kyverno.io/cleanuppolicies kyverno.io/clustercleanuppolicies kyverno.io/clusterpolicies
  kyverno.io/policies kyverno.io/updaterequests policies.kyverno.io/deletingpolicies
  policies.kyverno.io/generatingpolicies policies.kyverno.io/imagevalidatingpolicies
  policies.kyverno.io/mutatingpolicies policies.kyverno.io/namespaceddeletingpolicies
  policies.kyverno.io/namespacedgeneratingpolicies policies.kyverno.io/namespacedimagevalidatingpolicies
  policies.kyverno.io/namespacedmutatingpolicies policies.kyverno.io/namespacedvalidatingpolicies
  policies.kyverno.io/policyexceptions policies.kyverno.io/validatingpolicies
  reports.kyverno.io/clusterephemeralreports reports.kyverno.io/ephemeralreports
  wgpolicyk8s.io/clusterpolicyreports wgpolicyk8s.io/policyreports"
ADMISSION_CORE="/configmaps /namespaces coordination.k8s.io/leases
  admissionregistration.k8s.io/mutatingadmissionpolicies admissionregistration.k8s.io/mutatingadmissionpolicybindings
  admissionregistration.k8s.io/mutatingwebhookconfigurations admissionregistration.k8s.io/validatingadmissionpolicies
  admissionregistration.k8s.io/validatingadmissionpolicybindings admissionregistration.k8s.io/validatingwebhookconfigurations
  kyverno.io/clusterpolicies kyverno.io/clusterpolicies/status kyverno.io/globalcontextentries
  kyverno.io/globalcontextentries/status kyverno.io/policies kyverno.io/policies/status
  kyverno.io/policyexceptions kyverno.io/updaterequests kyverno.io/updaterequests/status
  policies.kyverno.io/generatingpolicies policies.kyverno.io/generatingpolicies/status
  policies.kyverno.io/imagevalidatingpolicies policies.kyverno.io/imagevalidatingpolicies/status
  policies.kyverno.io/mutatingpolicies policies.kyverno.io/mutatingpolicies/status
  policies.kyverno.io/namespacedgeneratingpolicies policies.kyverno.io/namespacedgeneratingpolicies/status
  policies.kyverno.io/namespacedimagevalidatingpolicies policies.kyverno.io/namespacedimagevalidatingpolicies/status
  policies.kyverno.io/namespacedmutatingpolicies policies.kyverno.io/namespacedmutatingpolicies/status
  policies.kyverno.io/namespacedvalidatingpolicies policies.kyverno.io/namespacedvalidatingpolicies/status
  policies.kyverno.io/policyexceptions policies.kyverno.io/validatingpolicies policies.kyverno.io/validatingpolicies/status
  rbac.authorization.k8s.io/clusterrolebindings rbac.authorization.k8s.io/clusterroles
  rbac.authorization.k8s.io/rolebindings rbac.authorization.k8s.io/roles
  reports.kyverno.io/clusterephemeralreports reports.kyverno.io/ephemeralreports
  wgpolicyk8s.io/clusterpolicyreports wgpolicyk8s.io/clusterpolicyreports/status
  wgpolicyk8s.io/policyreports wgpolicyk8s.io/policyreports/status"
BACKGROUND_CORE="/configmaps /events /namespaces events.k8s.io/events
  kyverno.io/clusterpolicies kyverno.io/clusterpolicies/status kyverno.io/globalcontextentries
  kyverno.io/globalcontextentries/status kyverno.io/policies kyverno.io/policies/status
  kyverno.io/policyexceptions kyverno.io/updaterequests kyverno.io/updaterequests/status
  policies.kyverno.io/generatingpolicies policies.kyverno.io/mutatingpolicies
  policies.kyverno.io/namespacedgeneratingpolicies policies.kyverno.io/namespacedmutatingpolicies
  policies.kyverno.io/policyexceptions reports.kyverno.io/clusterephemeralreports
  reports.kyverno.io/ephemeralreports"
REPORTS_CORE="/configmaps /namespaces
  admissionregistration.k8s.io/validatingadmissionpolicies admissionregistration.k8s.io/validatingadmissionpolicybindings
  kyverno.io/clusterpolicies kyverno.io/globalcontextentries kyverno.io/globalcontextentries/status
  kyverno.io/policies kyverno.io/policyexceptions
  openreports.io/clusterreports openreports.io/clusterreports/status openreports.io/reports openreports.io/reports/status
  policies.kyverno.io/generatingpolicies policies.kyverno.io/imagevalidatingpolicies
  policies.kyverno.io/imagevalidatingpolicies/status policies.kyverno.io/mutatingpolicies
  policies.kyverno.io/namespacedgeneratingpolicies policies.kyverno.io/namespacedimagevalidatingpolicies
  policies.kyverno.io/namespacedimagevalidatingpolicies/status policies.kyverno.io/namespacedmutatingpolicies
  policies.kyverno.io/namespacedvalidatingpolicies policies.kyverno.io/namespacedvalidatingpolicies/status
  policies.kyverno.io/policyexceptions policies.kyverno.io/policyexceptions/status
  policies.kyverno.io/validatingpolicies policies.kyverno.io/validatingpolicies/status
  reports.kyverno.io/clusterephemeralreports reports.kyverno.io/ephemeralreports
  wgpolicyk8s.io/clusterpolicyreports wgpolicyk8s.io/clusterpolicyreports/status
  wgpolicyk8s.io/policyreports wgpolicyk8s.io/policyreports/status"
CLEANUP_CORE="/configmaps /namespaces admissionregistration.k8s.io/validatingwebhookconfigurations
  kyverno.io/globalcontextentries kyverno.io/globalcontextentries/status
  policies.kyverno.io/deletingpolicies policies.kyverno.io/namespaceddeletingpolicies
  policies.kyverno.io/policyexceptions"
UPSTREAM=(
  "admission-controller $VIEW $ADMISSION_CORE"
  "background-controller $VIEW $BACKGROUND_CORE"
  "reports-controller $VIEW $REPORTS_CORE"
  "cleanup-controller $CLEANUP_CORE"
)

# upstream_reads <controller> <group>/<resource>: 0 when UPSTREAM gives the
# controller get, list and watch on it.
upstream_reads() {
  local entry word
  for entry in "${UPSTREAM[@]}"; do
    [ "${entry%% *}" = "$1" ] || continue
    for word in ${entry#* }; do
      if [ "$word" = "$2" ]; then return 0; fi
    done
  done
  return 1
}

# granted <controller> <group> <resource> <verb>: 0 when a ClusterRole of the
# render aggregated to the controller grants the verb ($TMP/grants).
granted() {
  local grant
  for grant in "$1|$2|$3|$4" "$1|$2|$3|*" "$1|$2|*|$4" "$1|$2|*|*" \
    "$1|*|$3|$4" "$1|*|$3|*" "$1|*|*|$4" "$1|*|*|*"; do
    if grep -q -x -F "$grant" "$TMP/grants"; then return 0; fi
  done
  return 1
}

# autogen_target <controller name>: the <group>|<resource> autogen makes of a
# pod controller (pkg/cel/autogen: a name of data.go, or
# <resource>.<version>.<group>). Prints nothing for a bare name Kyverno
# resolves only against the cluster.
autogen_target() {
  case "$1" in
    daemonsets | deployments | replicasets | statefulsets) echo "apps|$1" ;;
    jobs | cronjobs) echo "batch|$1" ;;
    *.*.*) echo "${1#*.*.}|${1%%.*}" ;;
  esac
}

# check_render <render file>: C1 to C4. Prints one line per finding and a
# summary. Returns 1 when a check fails.
check_render() {
  local render="$1" rc=0 kind name text calls lookups=0 call fn api res group verb entry ctls ctl ok
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
        if upstream_reads "$ctl" "$group/$res" || granted "$ctl" "$group" "$res" "$verb"; then ok=1; fi
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
  check_matches "$render" || rc=1
  [ "$rc" -eq 0 ] && echo "$TAG PASS: $lookups lookups, each readable by every controller of its kind; every matched kind usable by the controller that acts on it"
  return "$rc"
}

# pol_yq <kind> <name> <output flag> <expression> <render file>: yq on the
# documents of that kind and name. One plain query a document, no comparison
# and no concatenation: yq v4.44.3 (CI and the release) and later versions
# print these alike, which they do not for the joined forms.
pol_yq() {
  K="$1" N="$2" yq "$3" "select(.kind == strenv(K) and .metadata.name == strenv(N)) | $4" "$5"
}

# prop_values <key pattern> <line>...: the value of every `<key> = <value>`
# line of yq -o=props whose key matches the extended regular expression.
prop_values() {
  local pattern="$1" line key
  shift
  for line in "$@"; do
    key="${line%% = *}"
    if [[ "$key" =~ ^${pattern}$ ]]; then printf '%s\n' "${line#"$key" = }"; fi
  done
}

# check_matches <render file>: C5 and C6. Prints one line per policy and per
# finding. Returns 1 when a check fails.
check_matches() {
  local render="$1" rc=0 matched=0 p kind name bg ctag entry ctl verbs when target group res verb missing c t r g line auto
  local -a policies mc rules groups versions resources names targets shown ctlnames lines
  # kind|name of every policy of policies.kyverno.io that matches.
  mapfile -t policies < <(yq -N 'select(.kind != null and .metadata.name != null and .spec.matchConstraints != null) |
    select(.apiVersion | test("^policies[.]kyverno[.]io/")) | .kind + "|" + .metadata.name' "$render" | sort -u)
  for p in "${policies[@]}"; do
    kind="${p%%|*}"
    name="${p#*|}"
    entry=""
    for c in "${MATCHERS[@]}"; do
      if [ "${c%% *}" = "$kind" ]; then entry="$c"; fi
    done
    if [ -z "$entry" ]; then
      echo "$TAG FAIL C5 $kind/$name: a policy kind that is not in MATCHERS of this file; add the controller that acts on what it matches"
      rc=1
      continue
    fi
    read -r _ ctl verbs when <<<"$entry"
    mapfile -t lines < <(pol_yq "$kind" "$name" -N '.spec.evaluation.background.enabled' "$render")
    bg="${lines[0]:-null}"
    if [ "$when" = background ] && [ "$bg" = false ]; then
      echo "$TAG match $kind/$name: background scan off, the $ctl needs no read of what it matches"
      continue
    fi
    # group|resource of every resource of every resource rule.
    mapfile -t mc < <(pol_yq "$kind" "$name" -o=props '.spec.matchConstraints' "$render")
    mapfile -t rules < <(for line in "${mc[@]}"; do
      if [[ "$line" =~ ^resourceRules[.]([0-9]+)[.] ]]; then echo "${BASH_REMATCH[1]}"; fi
    done | sort -u -n)
    targets=()
    for r in "${rules[@]}"; do
      mapfile -t groups < <(prop_values "resourceRules[.]${r}[.]apiGroups[.][0-9]+" "${mc[@]}")
      mapfile -t resources < <(prop_values "resourceRules[.]${r}[.]resources[.][0-9]+" "${mc[@]}")
      for g in "${groups[@]}"; do
        for res in "${resources[@]}"; do targets+=("$g|$res"); done
      done
    done
    # The autogen targets: CanAutoGen of pkg/cel/autogen/support.go (one rule,
    # of the core group, v1 and pods only, no resource names, no excluded
    # rule, no object selector), then the policy's pod controllers or the
    # default ones.
    auto=no
    if [[ "$AUTOGEN_KINDS" == *" $kind "* ]] && [ "${#rules[@]}" -eq 1 ]; then
      mapfile -t groups < <(prop_values 'resourceRules[.]0[.]apiGroups[.][0-9]+' "${mc[@]}")
      mapfile -t versions < <(prop_values 'resourceRules[.]0[.]apiVersions[.][0-9]+' "${mc[@]}")
      mapfile -t resources < <(prop_values 'resourceRules[.]0[.]resources[.][0-9]+' "${mc[@]}")
      mapfile -t names < <(prop_values '(resourceRules[.]0[.]resourceNames|excludeResourceRules|objectSelector[.]matchLabels|objectSelector[.]matchExpressions)[.].+' "${mc[@]}")
      if [ "${#groups[@]}" -eq 1 ] && [ -z "${groups[0]}" ] && [ "${#versions[@]}" -eq 1 ] && [ "${versions[0]}" = v1 ] &&
        [ "${#resources[@]}" -eq 1 ] && [ "${resources[0]}" = pods ] && [ "${#names[@]}" -eq 0 ]; then
        auto=yes
      fi
    fi
    if [ "$auto" = yes ]; then
      mapfile -t lines < <(pol_yq "$kind" "$name" -N '.spec.autogen.podControllers.controllers | tag' "$render")
      ctag="${lines[0]:-}"
      if [ "$ctag" = '!!seq' ]; then
        mapfile -t lines < <(pol_yq "$kind" "$name" -o=props '.spec.autogen.podControllers.controllers' "$render")
        mapfile -t ctlnames < <(prop_values '[0-9]+' "${lines[@]}")
      else
        read -r -a ctlnames <<<"$AUTOGEN_DEFAULT"
      fi
      for c in "${ctlnames[@]}"; do
        t="$(autogen_target "$c")"
        if [ -z "$t" ]; then
          echo "$TAG FAIL C5 $kind/$name: the autogen controller $c resolves only against a cluster; name it as <resource>.<version>.<group>"
          rc=1
          continue
        fi
        targets+=("$t")
      done
    fi
    shown=()
    for target in "${targets[@]}"; do
      group="${target%%|*}"
      res="${target#*|}"
      shown+=("$group/$res")
      missing=""
      for verb in ${verbs//,/ }; do
        if [ "$verb" != delete ] && upstream_reads "$ctl" "$group/$res"; then continue; fi
        if granted "$ctl" "$group" "$res" "$verb"; then continue; fi
        missing="$missing $verb"
      done
      matched=$((matched + 1))
      if [ -n "$missing" ]; then
        echo "$TAG FAIL C5 $kind/$name: it matches $group/$res, the $ctl needs$missing on it, and neither its upstream roles nor a ClusterRole of this chart aggregated to it grants that (the policy stays RBACPermissionsGranted=False, never ready)"
        rc=1
      fi
    done
    echo "$TAG match $kind/$name: ${shown[*]} ($verbs) for the $ctl"
  done
  if [ "$matched" -eq 0 ]; then
    echo "$TAG FAIL C6: the render holds no kind a policy matches; the check is inspecting nothing (did the render break?)"
    rc=1
  fi
  return "$rc"
}

# check_engine_pin <Chart.yaml of the engine chart>: C7.
check_engine_pin() {
  local file="$1" got
  if [ ! -f "$file" ]; then
    echo "$TAG FAIL C7: $file is missing, so UPSTREAM cannot be matched to the engine chart"
    return 1
  fi
  got="$(yq -N '.dependencies[] | select(.name == "kyverno") | .version' "$file")"
  if [ "$got" != "$UPSTREAM_CHART" ]; then
    echo "$TAG FAIL C7: the engine chart depends on the kyverno chart ${got:-none}, UPSTREAM of this file was read from $UPSTREAM_CHART; read the roles again from its render and set UPSTREAM_CHART"
    return 1
  fi
  echo "$TAG engine chart: kyverno $got, the chart UPSTREAM was read from"
}

run_test() {
  local chart="$1" rc=0
  check_engine_pin "$chart/../../kyverno/chart/Chart.yaml" || rc=1
  if ! "$HELM" template kp "$chart" > "$TMP/render.yaml" 2> "$TMP/err"; then
    cat "$TMP/err"
    echo "$TAG FAIL: the chart does not render"
    return 1
  fi
  check_render "$TMP/render.yaml" || rc=1
  return "$rc"
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

# st_vpol <kind> <expression>: a policy document holding one variable; it
# matches pods.
st_vpol() {
  printf 'apiVersion: policies.kyverno.io/v1\nkind: %s\nmetadata:\n  name: p\nspec:\n  matchConstraints:\n    resourceRules:\n      - apiGroups: [""]\n        apiVersions: ["v1"]\n        operations: ["CREATE"]\n        resources: ["pods"]\n  variables:\n    - name: v\n      expression: %s\n' "$1" "$2"
}

# st_match <kind> <name> <group> <resource> [<lines>]: a policy that matches
# one resource and looks nothing up. The lines (printf %b) follow the resource
# rule: indented by four they go under matchConstraints, by two under spec.
st_match() {
  printf 'apiVersion: policies.kyverno.io/v1\nkind: %s\nmetadata:\n  name: %s\nspec:\n  matchConstraints:\n    resourceRules:\n      - apiGroups: ["%s"]\n        apiVersions: ["v1"]\n        operations: ["CREATE"]\n        resources: ["%s"]\n%b' "$1" "$2" "$3" "$4" "${5:-}"
}

# st_pin <name> <wanted exit code> <Chart.yaml>: a case of C7.
st_pin() {
  local rc=0
  check_engine_pin "$3" > "$ST_TMP/out" 2>&1 || rc=$?
  if [ "$rc" != "$2" ] || { [ "$2" = 1 ] && ! grep -q -F "$TAG FAIL C7:" "$ST_TMP/out"; }; then
    cat "$ST_TMP/out"
    echo "self-test FAIL: $1: exit $rc, wanted $2"
    ST_FAILED=1
  fi
  ST_CASES=$((ST_CASES + 1))
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
# named after its check (C1 to C6) and passes only when that check, and no
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
  local get list off rollout labelled
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
  st_case "C2 a render without a lookup fails" 1 "$(st_vpol ValidatingPolicy "'true'")" \
    "$(st_role admission-controller storage.k8s.io storageclasses '"get"')"
  st_case "C3 a lookup without a grant fails" 1 "$(st_vpol ValidatingPolicy "$get")"
  st_case "C3 a grant of another verb fails" 1 "$(st_vpol ValidatingPolicy "$get")" \
    "$(st_role admission-controller,reports-controller storage.k8s.io storageclasses '"list"')"
  st_case "C3 a grant to one of the two controllers fails" 1 "$(st_vpol ValidatingPolicy "$get")" \
    "$(st_role admission-controller storage.k8s.io storageclasses '"get"')"
  st_case "C3 the view role does not cover the cleanup controller" 1 "$(st_vpol DeletingPolicy "$list")" \
    "$(st_role cleanup-controller "" pods '"list", "delete"')"
  st_case "C3 a kind not in CONTROLLERS fails" 1 "$(st_vpol MutatingPolicy "$get")" \
    "$(st_role admission-controller,reports-controller,background-controller storage.k8s.io storageclasses '"get"')"
  st_case "C4 a legacy apiCall fails" 1 "$(st_vpol ValidatingPolicy "$list")" \
    "$(printf 'apiVersion: kyverno.io/v1\nkind: ClusterPolicy\nmetadata:\n  name: legacy\nspec:\n  rules:\n    - context:\n        - name: x\n          apiCall:\n            urlPath: "/api/v1/namespaces"\n')"
  # C5 to C7: the kinds a policy matches. Each render also holds a granted
  # lookup ($list, a policy matching pods), so C2 cannot fire.
  off='  evaluation:\n    background:\n      enabled: false\n'
  rollout='  autogen:\n    podControllers:\n      controllers: ["rollouts.v1alpha1.argoproj.io"]\n'
  labelled='    objectSelector:\n      matchLabels:\n        app: x\n'
  st_case "C5 a matched kind no role reads fails (forbid-local-path-storage on persistentvolumes)" 1 \
    "$(st_vpol ValidatingPolicy "$list")" "$(st_match ValidatingPolicy pv "" persistentvolumes)"
  st_case "a matched kind a grant aggregated to the reports controller reads passes" 0 \
    "$(st_vpol ValidatingPolicy "$list")" "$(st_match ValidatingPolicy pv "" persistentvolumes)" \
    "$(st_role reports-controller "" persistentvolumes '"get", "list", "watch"')"
  st_case "C5 a grant without watch fails" 1 \
    "$(st_vpol ValidatingPolicy "$list")" "$(st_match ValidatingPolicy pv "" persistentvolumes)" \
    "$(st_role reports-controller "" persistentvolumes '"get", "list"')"
  st_case "C5 a grant to the other controllers fails" 1 \
    "$(st_vpol ValidatingPolicy "$list")" "$(st_match ValidatingPolicy pv "" persistentvolumes)" \
    "$(st_role admission-controller,background-controller "" persistentvolumes '"get", "list", "watch"')"
  st_case "a matched kind the reports controller's own upstream role reads passes" 0 \
    "$(st_vpol ValidatingPolicy "$list")" "$(st_match ValidatingPolicy rep openreports.io reports)"
  st_case "a policy with the background scan off needs no read of what it matches" 0 \
    "$(st_vpol ValidatingPolicy "$list")" "$(st_match ValidatingPolicy pv "" persistentvolumes "$off")"
  st_case "C5 a GeneratingPolicy needs the read whatever its background field says" 1 \
    "$(st_vpol ValidatingPolicy "$list")" "$(st_match GeneratingPolicy pv "" persistentvolumes "$off")"
  st_case "C5 an autogen target no role reads fails" 1 \
    "$(st_vpol ValidatingPolicy "$list")" "$(st_match ValidatingPolicy ag "" pods "$rollout")"
  st_case "an autogen target a grant reads passes" 0 \
    "$(st_vpol ValidatingPolicy "$list")" "$(st_match ValidatingPolicy ag "" pods "$rollout")" \
    "$(st_role reports-controller argoproj.io rollouts '"get", "list", "watch"')"
  st_case "a policy that cannot autogen (an object selector) has no autogen target" 0 \
    "$(st_vpol ValidatingPolicy "$list")" "$(st_match ValidatingPolicy ag "" pods "$labelled$rollout")"
  st_case "C5 an autogen controller that resolves only against a cluster fails" 1 \
    "$(st_vpol ValidatingPolicy "$list")" \
    "$(st_match ValidatingPolicy ag "" pods '  autogen:\n    podControllers:\n      controllers: ["rollouts"]\n')"
  st_case "C5 a DeletingPolicy whose kind the cleanup controller can list but not delete fails" 1 \
    "$(st_vpol ValidatingPolicy "$list")" "$(st_match DeletingPolicy d "" secrets)" \
    "$(st_role cleanup-controller "" secrets '"list"')"
  st_case "C5 a policy kind not in MATCHERS fails" 1 \
    "$(st_vpol ValidatingPolicy "$list")" "$(st_match FuturePolicy f "" pods)"
  st_case "C6 a render without a matched kind fails" 1 \
    "$(printf 'apiVersion: policies.kyverno.io/v1\nkind: ValidatingPolicy\nmetadata:\n  name: p\nspec:\n  variables:\n    - name: v\n      expression: %s\n' "$list")"
  # The default autogen targets are the six pod controllers of data.go.
  st_render "$ST_TMP/render.yaml" "$(st_vpol ValidatingPolicy "$list")"
  check_render "$ST_TMP/render.yaml" > "$ST_TMP/out" 2>&1 || true
  if ! grep -q -F "$TAG match ValidatingPolicy/p: /pods apps/daemonsets apps/deployments apps/replicasets apps/statefulsets batch/jobs batch/cronjobs (get,list,watch) for the reports-controller" "$ST_TMP/out"; then
    cat "$ST_TMP/out"
    echo "self-test FAIL: a policy on pods without an autogen list is not checked on the six default autogen targets"
    ST_FAILED=1
  fi
  ST_CASES=$((ST_CASES + 1))
  printf 'dependencies:\n  - name: kyverno\n    version: "%s"\n' "$UPSTREAM_CHART" > "$ST_TMP/engine-same.yaml"
  printf 'dependencies:\n  - name: kyverno\n    version: "9.9.9"\n' > "$ST_TMP/engine-moved.yaml"
  st_pin "the engine chart of UPSTREAM_CHART passes" 0 "$ST_TMP/engine-same.yaml"
  st_pin "C7 an engine chart on another kyverno chart fails" 1 "$ST_TMP/engine-moved.yaml"
  st_pin "C7 a missing engine chart fails" 1 "$ST_TMP/no-such-chart.yaml"
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
