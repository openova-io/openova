#!/usr/bin/env bash
# Render check of the function test of bp-kyverno-policies (templates/tests/function-test.yaml).
# The contract is docs/RUNBOOKS.md section 4.5; scripts/check-function-test.py checks it.
# CI runs every chart/tests/*.sh as `bash <script> <chart dir>`.
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
CHECK="$(cd "$CHART_DIR/../../.." && pwd)/scripts/check-function-test.py"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $1" >&2; exit 1; }
TEST=templates/tests/function-test.yaml

python3 "$CHECK" --self-test > "$TMP/self-test.txt" || { cat "$TMP/self-test.txt" >&2; fail "check-function-test.py --self-test failed"; }

cd "$CHART_DIR"

# policies <render> <kind>: the names of the policies of that kind (ClusterPolicy or
# ValidatingPolicy) of a render, sorted, one per line.
policies() {
  python3 -c '
import sys, yaml
for doc in yaml.safe_load_all(open(sys.argv[1])):
    if isinstance(doc, dict) and doc.get("kind") == sys.argv[2]:
        print(doc["metadata"]["name"])
' "$1" "$2" | sort
}
# asked <test render> <variable>: the names the test asks for in that variable
# (POLICIES: the ClusterPolicies; VALIDATING_POLICIES: the ValidatingPolicies), sorted.
asked() {
  awk -v v="$2" 'f { sub(/^ *value: "/, ""); sub(/"$/, ""); print; exit } $0 ~ "^ *- name: " v "$" { f = 1 }' "$1" \
    | tr ' ' '\n' | sed '/^$/d' | sort
}
# both <render> <test render> <out prefix>: writes <prefix>-want.txt and <prefix>-got.txt,
# the policies of both kinds, each line "<kind> <name>".
both() {
  { policies "$1" ClusterPolicy | sed 's/^/ClusterPolicy /'; policies "$1" ValidatingPolicy | sed 's/^/ValidatingPolicy /'; } > "$3-want.txt"
  { asked "$2" POLICIES | sed 's/^/ClusterPolicy /'; asked "$2" VALIDATING_POLICIES | sed 's/^/ValidatingPolicy /'; } > "$3-got.txt"
}

echo "[function-test] Case 1: the file list of the test equals the policy templates of the chart"
( cd templates && ls baseline/*.yaml mutate/*.yaml ) > "$TMP/files.txt"
sed -n 's/^  "\([a-z]*\/[0-9a-z-]*\.yaml\)"\( }}\)\{0,1\}$/\1/p' "$TEST" > "$TMP/listed.txt"
diff "$TMP/files.txt" "$TMP/listed.txt" > "$TMP/files.diff" || { cat "$TMP/files.diff" >&2; fail "the file list of the test differs from templates/baseline and templates/mutate"; }

echo "[function-test] Case 2: default render asks for every ClusterPolicy and ValidatingPolicy of the release"
helm template kyverno-policies . --namespace kyverno > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-kyverno-policies --release kyverno-policies < "$TMP/default.yaml"
helm template kyverno-policies . --namespace kyverno --skip-tests > "$TMP/default-plain.yaml"
helm template kyverno-policies . --namespace kyverno --show-only "$TEST" > "$TMP/default-test.yaml"
sh "$CHART_DIR/../../../scripts/check-kube-access-rendered.sh" "$TMP/default-test.yaml" || fail "the test hook runs kubectl before it sets up its API access through kube-access-lib"
both "$TMP/default-plain.yaml" "$TMP/default-test.yaml" "$TMP/default"
[ "$(wc -l < "$TMP/default-want.txt")" -ge 20 ] || fail "the default render holds fewer than 20 ClusterPolicies and ValidatingPolicies"
grep -q '^ValidatingPolicy ' "$TMP/default-want.txt" || fail "the default render holds no ValidatingPolicy, so the second list is not under test"
diff "$TMP/default-want.txt" "$TMP/default-got.txt" > "$TMP/names.diff" || { cat "$TMP/names.diff" >&2; fail "the test does not ask for exactly the ClusterPolicies and ValidatingPolicies of the default render"; }
grep -q -F 'resources: ["clusterpolicies"]' "$TMP/default-test.yaml" || fail "the test ClusterRole does not name clusterpolicies"
grep -q -F 'resources: ["validatingpolicies"]' "$TMP/default-test.yaml" || fail "the test ClusterRole does not name validatingpolicies"
grep -q -F 'verbs: ["get"]' "$TMP/default-test.yaml" || fail "the test ClusterRole grants more than get"
grep -q -F 'name: kyverno-kyverno-policies-function-test' "$TMP/default-test.yaml" || fail "the cluster-wide names do not carry the namespace"

echo "[function-test] Case 3: the two policies that are off by default are asked for when they are on"
helm template kyverno-policies . --namespace kyverno \
  --set compliancePolicies.bootstrapMode=false \
  --set compliancePolicies.hubbleFlowsSeen.enabled=true \
  --set compliancePolicies.cosignVerified.enabled=true > "$TMP/all.yaml"
python3 "$CHECK" --chart bp-kyverno-policies --release kyverno-policies < "$TMP/all.yaml"
helm template kyverno-policies . --namespace kyverno --skip-tests \
  --set compliancePolicies.bootstrapMode=false \
  --set compliancePolicies.hubbleFlowsSeen.enabled=true \
  --set compliancePolicies.cosignVerified.enabled=true > "$TMP/all-plain.yaml"
helm template kyverno-policies . --namespace kyverno --show-only "$TEST" \
  --set compliancePolicies.bootstrapMode=false \
  --set compliancePolicies.hubbleFlowsSeen.enabled=true \
  --set compliancePolicies.cosignVerified.enabled=true > "$TMP/all-test.yaml"
both "$TMP/all-plain.yaml" "$TMP/all-test.yaml" "$TMP/all"
diff "$TMP/all-want.txt" "$TMP/all-got.txt" > "$TMP/names-all.diff" || { cat "$TMP/names-all.diff" >&2; fail "the test does not ask for exactly the ClusterPolicies and ValidatingPolicies of the render with every policy on"; }
[ "$(wc -l < "$TMP/all-got.txt")" -gt "$(wc -l < "$TMP/default-got.txt")" ] || fail "turning two policies on did not add them to the test"

echo "[function-test] Case 4: no ClusterPolicy and no ValidatingPolicy renders no test"
: > "$TMP/off.yaml"
for key in multiReplica pdb topologySpread probesPresent resourceRequests resourceLimits pvcExpansion \
           hpaEffective ciliumL7Mtls fluxManaged harborProxyPull imageTagPinned prometheusScrape \
           networkpolicyPresent otelInjected hubbleFlowsSeen runAsNonRootReadonlyRootFs cosignVerified \
           secretNotInEnv backupConfigured appLabelMutate substrateOnHost forbidLocalPath \
           forbidNodePortService ciliumEnforcedLabelMutate injectGiteaSourceSecretRef; do
  printf -- '--set\ncompliancePolicies.%s.enabled=false\n' "$key" >> "$TMP/off.yaml"
done
mapfile -t off < "$TMP/off.yaml"
helm template kyverno-policies . --namespace kyverno "${off[@]}" > "$TMP/none.yaml"
if grep -q -E '^kind: (ClusterPolicy|ValidatingPolicy)$' "$TMP/none.yaml"; then
  fail "the render with every policy off still holds a ClusterPolicy or a ValidatingPolicy"
fi
python3 "$CHECK" --chart bp-kyverno-policies --release kyverno-policies --absent < "$TMP/none.yaml"

echo "function-test-render: all cases passed"
