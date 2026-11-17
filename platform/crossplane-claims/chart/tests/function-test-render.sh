#!/usr/bin/env bash
# Render check of the function test of bp-crossplane-claims (templates/tests/function-test.yaml).
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

# xrds <render>: the names of the CompositeResourceDefinitions of a render, sorted, one per line.
xrds() {
  python3 -c '
import sys, yaml
for doc in yaml.safe_load_all(open(sys.argv[1])):
    if isinstance(doc, dict) and doc.get("kind") == "CompositeResourceDefinition":
        print(doc["metadata"]["name"])
' "$1" | sort
}

echo "[function-test] Case 1: the file list of the test equals the files under templates/xrds"
( cd templates && ls xrds/*.yaml ) > "$TMP/files.txt"
sed -n 's/^  "\(xrds\/[0-9a-z-]*\.yaml\)"\( }}\)\{0,1\}$/\1/p' "$TEST" > "$TMP/listed.txt"
diff "$TMP/files.txt" "$TMP/listed.txt" > "$TMP/files.diff" || { cat "$TMP/files.diff" >&2; fail "the file list of the test differs from templates/xrds"; }

echo "[function-test] Case 2: default render reads every CompositeResourceDefinition of the release"
helm template crossplane-claims . --namespace crossplane-system > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-crossplane-claims --release crossplane-claims < "$TMP/default.yaml"
helm template crossplane-claims . --namespace crossplane-system --skip-tests > "$TMP/default-plain.yaml"
helm template crossplane-claims . --namespace crossplane-system --show-only "$TEST" > "$TMP/default-test.yaml"
xrds "$TMP/default-plain.yaml" > "$TMP/want.txt"
awk 'f { sub(/^ *value: "/, ""); sub(/"$/, ""); print; exit } /^ *- name: XRDS$/ { f = 1 }' "$TMP/default-test.yaml" \
  | tr ' ' '\n' | sed '/^$/d' | sort > "$TMP/got.txt"
[ -s "$TMP/want.txt" ] || fail "the default render holds no CompositeResourceDefinition"
diff "$TMP/want.txt" "$TMP/got.txt" > "$TMP/names.diff" || { cat "$TMP/names.diff" >&2; fail "the test does not read exactly the CompositeResourceDefinitions of the default render"; }
grep -q -F 'value: "xcloudadoptions.compose.openova.io"' "$TMP/default-test.yaml" || fail "the test does not read xcloudadoptions.compose.openova.io"
grep -q -F 'resources: ["compositeresourcedefinitions"]' "$TMP/default-test.yaml" || fail "the test ClusterRole does not name compositeresourcedefinitions"
grep -q -F 'verbs: ["get"]' "$TMP/default-test.yaml" || fail "the test ClusterRole grants more than get"
grep -q -F 'name: crossplane-system-crossplane-claims-function-test' "$TMP/default-test.yaml" || fail "the cluster-wide names do not carry the namespace"

echo "function-test-render: all cases passed"
