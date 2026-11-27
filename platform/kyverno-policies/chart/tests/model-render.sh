#!/usr/bin/env bash
# model-render.sh - the ComplianceModel object of the policy chart (CMP-041).
#
# templates/compliance-model.yaml renders one object of kind ComplianceModel
# from model/model.yaml, model/checks.yaml and model/exemptions.yaml. This test
# renders it and asserts, on the chart:
#
#   1  the template renders exactly one document: apiVersion
#      compliance.openova.io/v1alpha1, kind ComplianceModel, name default;
#   2  spec.checks holds 22 checks, the list of model/checks.yaml verbatim and
#      in its order; every other field of spec but exemptions is
#      model/model.yaml verbatim;
#   3  spec.exemptions names the 20 exempt namespaces of values.yaml, each with
#      the reason and owner of model/exemptions.yaml; kube-system is exempt
#      from the 18 checks whose values block carries the shared exclude list,
#      cnpg from flux-managed alone and trivy-system from flux-managed and
#      harbor-proxy-pull;
#   4  a namespace with a reason added to an exclude list joins the exemption
#      of that check (cnpg with --set ...forbidNodePortService.excludeNamespaces);
#   5  a namespace with no reason in model/exemptions.yaml added to any exclude
#      list (excludeNamespaces or extraExcludeNamespaces, of a check or of a
#      policy that has none) fails the render with
#      "exemption <namespace> has no reason";
#   6  the model names exactly the checks whose policy the same render installs
#      (CMP-042, check CMP-G03): in one full render, the ids of spec.checks are
#      the names of the validating policies (every ValidatingPolicy, and every
#      ClusterPolicy with a validate rule); no exemption names a check
#      the model leaves out. It holds for the chart's values (with
#      forbid-local-path-storage in the model), for the values of the local
#      provider pack's kit (clusters/_template/providers/local/kit/
#      policy-values.yaml, which turns forbidLocalPath off: the check is not in
#      the model, the other 21 are those of model/checks.yaml verbatim and in
#      order), and with each check's values block turned off alone (--set
#      compliancePolicies.<valuesKey>.enabled=false: the check leaves both).
#
# Then it shows that each assertion can fail: it runs them again on copies of
# the chart with one defect each (a check removed, a second document, the
# model's name changed, a reason removed, the reason rule removed, the
# extraExcludeNamespaces join removed, a turned-off policy kept in the model,
# a policy that ignores its switch) and fails unless every copy fails.
#
# Usage:
#   bash model-render.sh            the chart above this file
#   bash model-render.sh <chart>    that chart directory (Chart tests (PR) passes it)
#
# Needs helm (the chart's dependencies built, as for every test of this chart)
# and yq (mikefarah v4). Exit code: 0 pass. 1 an assertion failed. 2 wrong usage.
set -euo pipefail
export LC_ALL=C

if [ "$#" -gt 1 ]; then
  echo "usage: bash model-render.sh [<chart directory>]" >&2
  exit 2
fi
CHART="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
HELM="${HELM_BIN:-helm}"
TAG="[model-render]"
TEMPLATE="templates/compliance-model.yaml"
# The policy values of the local provider pack (the document compliance-policy-values
# that the release reads there), in the repository that holds this file.
LOCAL_KIT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)/clusters/_template/providers/local/kit/policy-values.yaml"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# The 20 exempt namespaces of values.yaml today, sorted.
WANT_NAMESPACES="catalyst,cert-manager,cilium,cilium-gateway,cnpg,crossplane-system,dragonfly,falco,flux-system,ingress,kube-system,kyverno,monitoring,openova-system,org-services,powerdns,powerdns-admin,shared-data,sso-bridge,trivy-system"
# The 18 checks whose values block carries the shared list complianceDefaultExcludes.
WANT_SHARED="multi-replica-drainability,pdb-permits-eviction,topology-spread,probes-present,resource-requests,resource-limits,pvc-volume-expansion,hpa-effective,backup-configured,flux-managed,harbor-proxy-pull,image-tag-pinned,prometheus-scrape,otel-injected,runasnonroot-readonlyrootfs,cilium-l7-mtls,networkpolicy-present,secret-not-in-env"

FAILED=""

# fail <message>: records one failed assertion.
fail() {
  echo "$TAG FAIL $*"
  FAILED=1
}

# expect <label> <got> <want>
expect() {
  if [ "$2" = "$3" ]; then
    echo "$TAG PASS $1"
  else
    fail "$1: got '$2', want '$3'"
  fi
}

# render <chart> <out> [helm arguments...]: renders the model template.
render() {
  local chart="$1" out="$2"
  shift 2
  "$HELM" template kyverno-policies "$chart" --show-only "$TEMPLATE" "$@" >"$out" 2>"$out.err"
}

# exemption <render> <namespace>: the checks of the namespace's exemption, comma-separated.
exemption() {
  yq ".spec.exemptions[] | select(.namespace == \"$2\") | .checks | join(\",\")" "$1"
}

# assert_model <chart>: assertions 1 to 4. Returns 1 when one failed.
assert_model() {
  local chart="$1" out="$WORK/model.yaml" got want
  FAILED=""
  if ! render "$chart" "$out"; then
    cat "$out.err"
    fail "1 the chart does not render"
    return 1
  fi
  expect "1 one document" "$(grep -c '^kind:' "$out" || true)" "1"
  expect "1 apiVersion, kind and name" "$(yq '.apiVersion + " " + .kind + " " + .metadata.name' "$out")" \
    "compliance.openova.io/v1alpha1 ComplianceModel default"
  expect "2 22 checks" "$(yq '.spec.checks | length' "$out")" "22"
  got="$(yq -o=json -I=0 '.spec.checks | sort_keys(..)' "$out")"
  want="$(yq -o=json -I=0 '.checks | sort_keys(..)' "$chart/model/checks.yaml")"
  [ "$got" = "$want" ] && echo "$TAG PASS 2 the checks are model/checks.yaml verbatim" ||
    fail "2 the checks differ from model/checks.yaml"
  got="$(yq -o=json -I=0 '.spec | del(.checks) | del(.exemptions) | sort_keys(..)' "$out")"
  want="$(yq -o=json -I=0 'sort_keys(..)' "$chart/model/model.yaml")"
  [ "$got" = "$want" ] && echo "$TAG PASS 2 the other fields are model/model.yaml verbatim" ||
    fail "2 spec differs from model/model.yaml: $got"
  expect "3 the exempt namespaces" "$(yq '[.spec.exemptions[].namespace] | join(",")' "$out")" "$WANT_NAMESPACES"
  got="$(yq -o=json -I=0 '[.spec.exemptions[] | {"namespace": .namespace, "owner": .owner, "reason": .reason}] | sort_by(.namespace)' "$out")"
  want="$(yq -o=json -I=0 '[.namespaces | to_entries | .[] | {"namespace": .key, "owner": .value.owner, "reason": .value.reason}] | sort_by(.namespace)' "$chart/model/exemptions.yaml")"
  [ "$got" = "$want" ] && echo "$TAG PASS 3 every reason and owner is that of model/exemptions.yaml" ||
    fail "3 the reasons differ from model/exemptions.yaml"
  expect "3 kube-system" "$(exemption "$out" kube-system)" "$WANT_SHARED"
  expect "3 cnpg" "$(exemption "$out" cnpg)" "flux-managed"
  expect "3 trivy-system" "$(exemption "$out" trivy-system)" "flux-managed,harbor-proxy-pull"
  if render "$chart" "$out" --set 'compliancePolicies.forbidNodePortService.excludeNamespaces={cnpg}'; then
    expect "4 a listed namespace with a reason joins the check" "$(exemption "$out" cnpg)" "flux-managed,forbid-nodeport-service"
  else
    cat "$out.err"
    fail "4 the chart does not render with cnpg exempt from forbid-nodeport-service"
  fi
  [ -z "$FAILED" ]
}

# assert_no_reason_fails <chart>: assertion 5. Returns 1 when one failed.
assert_no_reason_fails() {
  local chart="$1" out="$WORK/noreason.yaml" set
  FAILED=""
  for set in 'compliancePolicies.pdb.excludeNamespaces={kube-system,cmp-unreasoned}' \
    'compliancePolicies.fluxManaged.extraExcludeNamespaces={cnpg,cmp-unreasoned}' \
    'compliancePolicies.appLabelMutate.excludeNamespaces={cmp-unreasoned}'; do
    if render "$chart" "$out" --set "$set"; then
      fail "5 the chart renders with --set $set"
    elif grep -q 'exemption cmp-unreasoned has no reason' "$out.err"; then
      echo "$TAG PASS 5 --set $set fails the render: exemption cmp-unreasoned has no reason"
    else
      cat "$out.err"
      fail "5 --set $set fails the render without the message"
    fi
  done
  [ -z "$FAILED" ]
}

# model_ids <render>: the ids of the checks of the model of a full render, sorted, comma-separated.
model_ids() {
  yq ea '[select(.kind == "ComplianceModel") | .spec.checks[].id] | .[]' "$1" | sort | paste -sd, -
}

# installed <render>: the names of the validating policies of a full render, sorted,
# comma-separated.
installed() {
  yq ea '[select(.kind == "ValidatingPolicy" or (.kind == "ClusterPolicy" and ([(.spec.rules // [])[] | select(has("validate"))] | length) > 0)) | .metadata.name] | .[]' "$1" | sort | paste -sd, -
}

# stray <render>: the checks that an exemption names and the model does not, comma-separated.
stray() {
  local id ids model
  model=",$(model_ids "$1"),"
  ids="$(yq ea '[select(.kind == "ComplianceModel") | .spec.exemptions[].checks[]] | unique | join(" ")' "$1")"
  for id in $ids; do
    case "$model" in *",$id,"*) ;; *) printf '%s,' "$id" ;; esac
  done
}

# agree <label> <render>: assertion 6 on one full render.
agree() {
  expect "6 $1: the model's checks are the installed policies" "$(model_ids "$2")" "$(installed "$2")"
  expect "6 $1: every exempted check is in the model" "$(stray "$2")" ""
}

# has_check <render> <id>: yes when the model of the render names the check, else no.
has_check() {
  case ",$(model_ids "$1")," in *",$2,"*) echo yes ;; *) echo no ;; esac
}

# assert_follows <chart>: assertion 6. Returns 1 when one failed.
assert_follows() {
  local chart="$1" out="$WORK/full.yaml" values="$WORK/local-kit-values.yaml" id key got want off=0 bad=""
  FAILED=""
  if "$HELM" template kyverno-policies "$chart" >"$out" 2>"$out.err"; then
    agree "the chart's values" "$out"
    expect "6 the chart's values: forbid-local-path-storage is in the model" "$(has_check "$out" forbid-local-path-storage)" "yes"
  else
    cat "$out.err"
    fail "6 the chart does not render"
  fi
  if [ ! -s "$LOCAL_KIT" ]; then
    fail "6 the local kit's values are missing: $LOCAL_KIT"
  else
    yq '.data["values.yaml"]' "$LOCAL_KIT" >"$values"
    expect "6 the local kit turns forbidLocalPath off" "$(yq '.compliancePolicies.forbidLocalPath.enabled' "$values")" "false"
    if "$HELM" template kyverno-policies "$chart" -f "$values" >"$out" 2>"$out.err"; then
      agree "the local kit's values" "$out"
      expect "6 the local kit's values: forbid-local-path-storage is not in the model" "$(has_check "$out" forbid-local-path-storage)" "no"
      got="$(yq ea -o=json -I=0 '[select(.kind == "ComplianceModel") | .spec.checks[]] | sort_keys(..)' "$out")"
      want="$(yq -o=json -I=0 '[.checks[] | select(.id != "forbid-local-path-storage")] | sort_keys(..)' "$chart/model/checks.yaml")"
      [ "$got" = "$want" ] && echo "$TAG PASS 6 the local kit's values: the other checks are model/checks.yaml verbatim and in order" ||
        fail "6 the local kit's values: the checks differ from model/checks.yaml less forbid-local-path-storage"
    else
      cat "$out.err"
      fail "6 the chart does not render with the local kit's values"
    fi
  fi
  while read -r id key; do
    [ "$(yq ".compliancePolicies.$key | tag" "$chart/values.yaml")" = "!!map" ] || continue
    off=$((off + 1))
    if ! "$HELM" template kyverno-policies "$chart" --set "compliancePolicies.$key.enabled=false" >"$out" 2>"$out.err"; then
      cat "$out.err"
      bad="$bad $id(no render)"
    elif [ "$(model_ids "$out")" != "$(installed "$out")" ] || [ "$(has_check "$out" "$id")" != "no" ] || [ -n "$(stray "$out")" ]; then
      bad="$bad $id"
    fi
  done < <(yq '.checks[] | .id + " " + .valuesKey' "$chart/model/checks.yaml")
  expect "6 each of the $off checks with a values block turned off alone leaves the model and the render" "${bad:-none}" "none"
  expect "6 the checks with a values block" "$off" "22"
  [ -z "$FAILED" ]
}

# defect <name> <check> <shell commands run in the copy>: a copy of the chart with
# one defect, on which the assertion function <check> must fail.
DEFECTS=0
defect() {
  local name="$1" check="$2" copy="$WORK/defect"
  rm -rf "$copy"
  cp -R "$chart_dir" "$copy"
  (cd "$copy" && eval "$3")
  if "$check" "$copy" >"$WORK/defect.out" 2>&1; then
    cat "$WORK/defect.out"
    echo "$TAG FAIL the defect '$name' passes $check"
    DEFECTS=$((DEFECTS + 1))
  else
    echo "$TAG PASS the defect '$name' fails $check"
  fi
}

chart_dir="$CHART"
ok=1
assert_model "$CHART" || ok=""
assert_no_reason_fails "$CHART" || ok=""
assert_follows "$CHART" || ok=""
if [ -z "$ok" ]; then
  echo "$TAG FAIL the chart"
  exit 1
fi

defect "a check removed" assert_model "yq -i 'del(.checks[21])' model/checks.yaml"
defect "a second document" assert_model "printf -- '---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n' >> $TEMPLATE"
defect "the name is not default" assert_model "sed -i 's/^  name: default$/  name: other/' $TEMPLATE"
defect "a reason removed" assert_model "yq -i 'del(.namespaces.cnpg)' model/exemptions.yaml"
defect "a model field changed in the render" assert_model "sed -i 's/^{{- \$spec := deepCopy \$model -}}$/{{- \$spec := deepCopy \$model -}}{{- \$_ := set \$spec \"version\" \"9.9.9\" -}}/' $TEMPLATE"
defect "extraExcludeNamespaces left out of the exemptions" assert_model "sed -i '/^{{- range \$c := \$checks -}}$/,\$ s/(default (list) \$block.extraExcludeNamespaces)/(list)/' $TEMPLATE"
defect "the reason rule removed" assert_no_reason_fails "sed -i 's/^{{- fail (printf \"exemption %s has no reason\" \$ns) -}}$//' $TEMPLATE"
defect "a turned-off policy kept in the model" assert_follows "sed -i 's/^{{- if or (not (kindIs \"map\" \$switch)) (get \$switch \"enabled\") -}}$/{{- if true -}}/' $TEMPLATE"
defect "a policy that ignores its switch" assert_follows "sed -i 's/^{{- if .Values.compliancePolicies.forbidLocalPath.enabled -}}$/{{- if true -}}/' templates/baseline/23-forbid-local-path.yaml"
if [ "$DEFECTS" -ne 0 ]; then
  echo "$TAG FAIL $DEFECTS defects pass: an assertion cannot fail"
  exit 1
fi
echo "$TAG ALL PASS"
