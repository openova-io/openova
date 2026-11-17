#!/usr/bin/env bash
# bp-network-policies render test.
#
# What the chart must render, rule by rule. R1 and R3 judge the render
# without the helm test Pod (helm template --skip-tests); the test Pod is
# judged by tests/function-test-render.sh. R2 and R4 judge the whole
# render, test Pod included.
#   R1  Default values render exactly ONE object, and it is exactly the
#       object `baseline-deny-metadata` written out below: a cluster-wide
#       egress deny to 169.254.169.254/32 that puts no pod under default
#       deny.
#   R2  enabled=false renders nothing at all.
#   R3  legacyBaseline.enabled=true adds the three legacy objects, and the
#       deny object stays. allowEgressDNS=false still drops the DNS object.
#   R4  enabled=false wins over legacyBaseline.enabled=true: nothing at all.
#
# The test then proves that it can fail: it changes a copy of the chart in
# nine ways, one or more per rule, and each copy must fail its rule.
#
# Usage: bash tests/render.sh [CHART_DIR]
# Needs: bash, helm, and the usual text tools. No cluster.

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
CHART_DIR="$(cd "$CHART_DIR" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# The one object of the default render, without the `---` line and the
# `# Source:` comment that helm adds.
cat > "$TMP/expected.yaml" <<'EOF'
apiVersion: cilium.io/v2
kind: CiliumClusterwideNetworkPolicy
metadata:
  name: baseline-deny-metadata
spec:
  endpointSelector:
    matchExpressions:
      - key: network.openova.io/metadata-access
        operator: NotIn
        values:
          - "true"
  enableDefaultDeny:
    ingress: false
    egress: false
  egressDeny:
    - toCIDR:
        - 169.254.169.254/32
EOF

# render <chart> <out> [helm flags...]: helm template without the lines
# helm adds (`---`, comments) and without empty lines. An empty render is
# a valid result, so the exit code 1 of grep for no line is not an error.
render() {
  local chart="$1" out="$2"
  shift 2
  helm template t "$chart" "$@" > "$out.raw" 2> "$out.err" || return 1
  grep -v -e '^---$' -e '^#' -e '^[[:space:]]*$' "$out.raw" > "$out" || true
}

# names <file>: the object names of a render, sorted, on one line.
names() {
  grep -E '^  name: ' "$1" | sed 's/^  name: //' | sort | tr '\n' ' ' | sed 's/ $//'
}

# check_chart <chart> <workdir>: prints one line per broken rule and
# returns 1 when a rule is broken.
check_chart() {
  local chart="$1" w="$2" bad=0
  mkdir -p "$w"

  if ! render "$chart" "$w/default.yaml" --skip-tests; then
    echo "R1: the default render failed: $(cat "$w/default.yaml.err")"
    return 1
  fi
  if ! cmp -s "$w/default.yaml" "$TMP/expected.yaml"; then
    echo "R1: the default render is not exactly the object baseline-deny-metadata"
    bad=1
  fi

  if ! render "$chart" "$w/off.yaml" --set enabled=false; then
    echo "R2: the render with enabled=false failed: $(cat "$w/off.yaml.err")"
    return 1
  fi
  if [ -s "$w/off.yaml" ]; then
    echo "R2: enabled=false still renders: $(names "$w/off.yaml")"
    bad=1
  fi

  if ! render "$chart" "$w/legacy.yaml" --skip-tests --set legacyBaseline.enabled=true; then
    echo "R3: the render with legacyBaseline.enabled=true failed: $(cat "$w/legacy.yaml.err")"
    return 1
  fi
  if [ "$(names "$w/legacy.yaml")" != "00-default-deny 10-allow-system-namespaces 30-allow-egress-dns baseline-deny-metadata" ]; then
    echo "R3: legacyBaseline.enabled=true renders: $(names "$w/legacy.yaml")"
    bad=1
  fi
  if [ "$(grep -c '^kind: CiliumClusterwideNetworkPolicy$' "$w/legacy.yaml")" != "4" ]; then
    echo "R3: legacyBaseline.enabled=true does not render four CiliumClusterwideNetworkPolicy objects"
    bad=1
  fi
  if ! render "$chart" "$w/legacy-nodns.yaml" --skip-tests --set legacyBaseline.enabled=true --set allowEgressDNS=false; then
    echo "R3: the render with allowEgressDNS=false failed: $(cat "$w/legacy-nodns.yaml.err")"
    return 1
  fi
  if [ "$(names "$w/legacy-nodns.yaml")" != "00-default-deny 10-allow-system-namespaces baseline-deny-metadata" ]; then
    echo "R3: legacyBaseline.enabled=true with allowEgressDNS=false renders: $(names "$w/legacy-nodns.yaml")"
    bad=1
  fi

  if ! render "$chart" "$w/off-legacy.yaml" --set enabled=false --set legacyBaseline.enabled=true; then
    echo "R4: the render with enabled=false and legacyBaseline.enabled=true failed: $(cat "$w/off-legacy.yaml.err")"
    return 1
  fi
  if [ -s "$w/off-legacy.yaml" ]; then
    echo "R4: enabled=false with legacyBaseline.enabled=true still renders: $(names "$w/off-legacy.yaml")"
    bad=1
  fi

  return "$bad"
}

echo "[render] the chart at $CHART_DIR"
if ! check_chart "$CHART_DIR" "$TMP/real"; then
  echo "FAIL: the chart breaks a rule (lines above)." >&2
  exit 1
fi
echo "  PASS: R1 to R4"

# ---- The test must be able to fail.
# mutant <name> <rule> <file> <sed expression>: copy the chart, change one
# file of the copy, and require that check_chart reports <rule> for it.
mutants=0
mutant() {
  local name="$1" rule="$2" file="$3" expr="$4" dir="$TMP/mutant-$1"
  cp -R "$CHART_DIR" "$dir"
  sed -i "$expr" "$dir/$file"
  if cmp -s "$dir/$file" "$CHART_DIR/$file"; then
    echo "FAIL: self-test $name: the change did not apply to $file; the test is out of date." >&2
    exit 1
  fi
  if check_chart "$dir" "$TMP/out-$name" > "$TMP/out-$name.txt" 2>&1; then
    echo "FAIL: self-test $name: a chart that breaks $rule passes." >&2
    exit 1
  fi
  if ! grep -q "^$rule: " "$TMP/out-$name.txt"; then
    echo "FAIL: self-test $name: expected a line for $rule, got:" >&2
    cat "$TMP/out-$name.txt" >&2
    exit 1
  fi
  mutants=$((mutants + 1))
}

D=templates/baseline-deny-metadata.yaml
mutant address       R1 "$D" 's|169\.254\.169\.254/32|169.254.0.0/16|'
mutant default-deny  R1 "$D" 's|^    egress: false$|    egress: true|'
mutant selector      R1 "$D" 's|operator: NotIn|operator: In|'
mutant deny-ungated  R2 "$D" 's|^{{- if \.Values\.enabled }}$|{{- if true }}|'
mutant test-ungated  R2 templates/tests/function-test.yaml 's|^{{- if \.Values\.enabled }}$|{{- if true }}|'
mutant legacy-on     R1 values.yaml '/^legacyBaseline:$/,/^  enabled: false$/s|^  enabled: false$|  enabled: true|'
mutant deny-all-back R1 templates/default-deny.yaml 's|{{- if and \.Values\.enabled \.Values\.legacyBaseline\.enabled -}}|{{- if .Values.enabled -}}|'
mutant legacy-gone   R3 templates/allow-system-namespaces.yaml 's|{{- if and \.Values\.enabled \.Values\.legacyBaseline\.enabled \.Values\.allowSystemNamespaces -}}|{{- if false -}}|'
mutant legacy-alone  R4 templates/allow-egress-dns.yaml 's|{{- if and \.Values\.enabled \.Values\.legacyBaseline\.enabled \.Values\.allowEgressDNS -}}|{{- if and .Values.legacyBaseline.enabled .Values.allowEgressDNS -}}|'

if [ "$mutants" != "9" ]; then
  echo "FAIL: self-test ran $mutants changed copies, expected 9." >&2
  exit 1
fi
echo "  PASS: self-test, 9 changed copies of the chart each fail their rule"
echo "[render] all green."
