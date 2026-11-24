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
#      "exemption <namespace> has no reason".
#
# Then it shows that each assertion can fail: it runs them again on copies of
# the chart with one defect each (a check removed, a second document, the
# model's name changed, a reason removed, the reason rule removed, the
# extraExcludeNamespaces join removed) and fails unless every copy fails.
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
if [ "$DEFECTS" -ne 0 ]; then
  echo "$TAG FAIL $DEFECTS defects pass: an assertion cannot fail"
  exit 1
fi
echo "$TAG ALL PASS"
