#!/usr/bin/env bash
# bp-network-policies render test.
#
# What the chart must render, rule by rule. Every rule judges the render
# without the helm test Pod (helm template --skip-tests), except R2, which
# judges the whole render; the test Pod is judged by
# tests/function-test-render.sh.
#   R1  Default values render exactly ONE object, and it is exactly the
#       object `baseline-deny-metadata` written out below: a cluster-wide
#       egress deny to 169.254.169.254/32 that puts no pod under default
#       deny. No pod is under default deny.
#   R2  enabled=false renders nothing at all, also with enforced namespaces.
#   R3  enforcedNamespaces={alpha,beta} adds exactly the object
#       `baseline-default-deny` written out below: those two namespaces,
#       no peer in, DNS to the cluster resolver out.
#   R4  Allow files. Every file of templates/allow/ is a partial _*.tpl or
#       an allow file <ns>.yaml. Every allow file of the chart, and the file
#       net-dummy.yaml that this test adds to a copy of the chart, renders
#       nothing while <ns> is not enforced and exactly one object,
#       allow-<ns>, while it is. The dummy's object is exactly the one
#       written out below: the pods of <ns> that are not session pods,
#       same-namespace traffic both ways, then the dummy's own rules. A file
#       net-yml.yml that calls the helper stops the render.
#   R5  The allow helper stops the render, with its message, on each broken
#       rule of the list broken.txt below (no peer, no port, port 0 in any
#       spelling, a range of every port, an entity other than ingress,
#       kube-apiserver, host and remote-node, a range wider than /8 or /16,
#       an open DNS wildcard, an endpoint selector without a namespace, a
#       key outside the rule template), and renders each rule of the list
#       accepted.txt, an ordinary port and an ordinary port range among them.
#   R6  An entry of enforcedNamespaces that is not a namespace name, a
#       namespace named twice, or a value that is not a list (a string,
#       false, 0, an empty string, a map) stops the render.
#
# The test then proves that it can fail: it changes a copy of the chart in
# twenty-seven ways, one or more per rule, and each copy must fail its rule.
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

# The object baseline-default-deny for enforcedNamespaces={alpha,beta}.
cat > "$TMP/expected-deny.yaml" <<'EOF'
apiVersion: cilium.io/v2
kind: CiliumClusterwideNetworkPolicy
metadata:
  name: baseline-default-deny
spec:
  endpointSelector:
    matchExpressions:
      - key: io.kubernetes.pod.namespace
        operator: In
        values:
          - alpha
          - beta
  ingress:
    - {}
  egress:
    - toEndpoints:
        - matchLabels:
            io.kubernetes.pod.namespace: kube-system
            k8s-app: kube-dns
      toPorts:
        - ports:
            - port: "53"
              protocol: UDP
            - port: "53"
              protocol: TCP
          rules:
            dns:
              - matchPattern: "*"
EOF

# The allow file that the test adds to a copy of the chart, and its object.
cat > "$TMP/net-dummy.yaml" <<'EOF'
{{- define "allow.net-dummy.rules" }}
ingress:
  - fromEndpoints: [{matchLabels: {io.kubernetes.pod.namespace: flux-system}}]
    toPorts: [{ports: [{port: "3000", protocol: TCP}]}]
egress:
  - toFQDNs: [{matchName: "git.upstream.example"}]
    toPorts: [{ports: [{port: "443", protocol: TCP}]}]
{{- end }}
{{- include "network-policies.allow" . }}
EOF
cat > "$TMP/expected-allow.yaml" <<'EOF'
apiVersion: cilium.io/v2
kind: CiliumClusterwideNetworkPolicy
metadata:
  name: allow-net-dummy
spec:
  endpointSelector:
    matchLabels:
      io.kubernetes.pod.namespace: net-dummy
    matchExpressions:
      - key: access.openova.io/session
        operator: DoesNotExist
  ingress:
    - fromEndpoints:
        - matchLabels:
            io.kubernetes.pod.namespace: net-dummy
    - fromEndpoints:
      - matchLabels:
          io.kubernetes.pod.namespace: flux-system
      toPorts:
      - ports:
        - port: "3000"
          protocol: TCP
  egress:
    - toEndpoints:
        - matchLabels:
            io.kubernetes.pod.namespace: net-dummy
    - toFQDNs:
      - matchName: git.upstream.example
      toPorts:
      - ports:
        - port: "443"
          protocol: TCP
EOF

# The broken rules of R5: <name>|<what the helper must say>|<rules>. @P@ is
# a valid port list, so that each case breaks one rule only.
P='toPorts: [{ports: [{port: "443", protocol: TCP}]}]'
sed "s/@P@/$P/g" > "$TMP/broken.txt" <<'EOF'
no-port|names no port|egress: [{toEndpoints: [{matchLabels: {io.kubernetes.pod.namespace: gitea}}]}]
empty-ports|names no port|ingress: [{fromEntities: [ingress], toPorts: [{}]}]
port-0|names the port 0|egress: [{toEndpoints: [{matchLabels: {io.kubernetes.pod.namespace: gitea}}], toPorts: [{ports: [{port: "0", protocol: TCP}]}]}]
port-00|names the port 00;|egress: [{toEndpoints: [{matchLabels: {io.kubernetes.pod.namespace: gitea}}], toPorts: [{ports: [{port: "00", protocol: TCP}]}]}]
port-0x0|names the port 0x0;|ingress: [{fromEntities: [ingress], toPorts: [{ports: [{port: "0x0", protocol: TCP}]}]}]
every-port|names the port range 1 to 65535, every port|egress: [{toEndpoints: [{matchLabels: {io.kubernetes.pod.namespace: gitea}}], toPorts: [{ports: [{port: "1", endPort: 65535, protocol: TCP}]}]}]
no-peer-egress|egress rule 0 names no peer|egress: [{@P@}]
no-peer-ingress|ingress rule 0 names no peer|ingress: [{@P@}]
empty-peer|names no peer|egress: [{toEndpoints: [], @P@}]
world|names the entity world;|egress: [{toEntities: [world], @P@}]
world-ipv4|names the entity world-ipv4;|egress: [{toEntities: [world-ipv4], @P@}]
world-ipv6|names the entity world-ipv6;|egress: [{toEntities: [world-ipv6], @P@}]
all|names the entity all;|ingress: [{fromEntities: [all], @P@}]
cluster|names the entity cluster;|egress: [{toEntities: [cluster], @P@}]
any-range|names the range 0.0.0.0/0, wider than /8|egress: [{toCIDR: [0.0.0.0/0], @P@}]
half-range|names the range 0.0.0.0/1, wider than /8|egress: [{toCIDR: [0.0.0.0/1, 128.0.0.0/1], @P@}]
any-range-set|names the range 0.0.0.0/0, wider than /8|egress: [{toCIDRSet: [{cidr: 0.0.0.0/0}], @P@}]
from-range|names the range 0.0.0.0/0, wider than /8|ingress: [{fromCIDR: [0.0.0.0/0], @P@}]
from-range-set|names the range 10.0.0.0/7, wider than /8|ingress: [{fromCIDRSet: [{cidr: 10.0.0.0/7}], @P@}]
any-range-v6|names the range ::/0, wider than /16|egress: [{toCIDR: ["::/0"], @P@}]
fqdn-star|names the DNS name *;|egress: [{toFQDNs: [{matchPattern: "*"}], @P@}]
fqdn-tld|names the DNS name *.com;|egress: [{toFQDNs: [{matchPattern: "*.com"}], @P@}]
fqdn-inner|names the DNS name api.*.example;|egress: [{toFQDNs: [{matchPattern: "api.*.example"}], @P@}]
open-selector|without a namespace|egress: [{toEndpoints: [{}], @P@}]
label-selector|without a namespace|ingress: [{fromEndpoints: [{matchLabels: {app: web}}], @P@}]
notin-selector|without a namespace|ingress: [{fromEndpoints: [{matchExpressions: [{key: io.kubernetes.pod.namespace, operator: NotIn, values: [gitea]}]}], @P@}]
rule-key|holds the key toServices;|egress: [{toServices: [{k8sService: {serviceName: x, namespace: y}}], @P@}]
deny-key|only ingress and egress are allowed|egressDeny: [{toEntities: [host]}]
EOF
# The rules of R5 that the helper must render: <name>|<rules>.
sed "s/@P@/$P/g" > "$TMP/accepted.txt" <<'EOF'
webhook|ingress: [{fromEntities: [kube-apiserver, remote-node], @P@}]
gateway|ingress: [{fromEntities: [ingress, host], @P@}]
fqdn-pattern|egress: [{toFQDNs: [{matchPattern: "*.example.com"}, {matchPattern: "**.cdn.example.net"}, {matchName: "api.example.org"}], @P@}]
cloud-range|egress: [{toCIDR: [100.64.0.0/10, 192.0.2.10, "2001:db8::/32"], @P@}, {toCIDRSet: [{cidr: 203.0.113.0/24}], @P@}]
by-term|egress: [{toEndpoints: [{matchExpressions: [{key: io.kubernetes.pod.namespace, operator: In, values: [gitea]}]}], @P@}]
by-prefix|ingress: [{fromEndpoints: [{matchLabels: {"k8s:io.kubernetes.pod.namespace": flux-system, app: web}}], @P@}]
port-range|egress: [{toEndpoints: [{matchLabels: {io.kubernetes.pod.namespace: gitea}}], toPorts: [{ports: [{port: "3000", protocol: TCP}, {port: "8080", endPort: 8090, protocol: TCP}, {port: "1", endPort: 1024, protocol: UDP}]}]}]
EOF

# The values of R6 that must stop the render: <name>|<helm flag>|<value>.
cat > "$TMP/bad-values.txt" <<'EOF'
upper-case|--set|enforcedNamespaces={Bad_NS}
dot|--set|enforcedNamespaces={a.b}
twice|--set|enforcedNamespaces={alpha,alpha}
a-string|--set|enforcedNamespaces=alpha
false|--set|enforcedNamespaces=false
zero|--set|enforcedNamespaces=0
empty-string|--set-string|enforcedNamespaces=
a-map|--set-json|enforcedNamespaces={}
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
  { grep -E '^  name: ' "$1" || true; } | sed 's/^  name: //' | sort | tr '\n' ' ' | sed 's/ $//'
}

# object <render> <name> <out>: the one object of that name in a render.
object() {
  awk -v want="$2" '
    /^apiVersion: / { if (keep) exit; buf = $0 "\n"; inobj = 1; next }
    inobj && /^  name: / { keep = ($0 == "  name: " want) }
    inobj { buf = buf $0 "\n" }
    END { if (keep) printf "%s", buf }
  ' "$1" > "$3"
}

# check_chart <chart> <workdir>: prints one line per broken rule and
# returns 1 when a rule is broken.
check_chart() {
  local chart="$1" w="$2" bad=0 f ns line name want rules flag
  mkdir -p "$w"

  # R1
  if ! render "$chart" "$w/default.yaml" --skip-tests; then
    echo "R1: the default render failed: $(cat "$w/default.yaml.err")"
    return 1
  fi
  if ! cmp -s "$w/default.yaml" "$TMP/expected.yaml"; then
    echo "R1: the default render is not exactly the object baseline-deny-metadata"
    bad=1
  fi

  # R2
  if ! render "$chart" "$w/off.yaml" --set enabled=false; then
    echo "R2: the render with enabled=false failed: $(cat "$w/off.yaml.err")"
    return 1
  fi
  if [ -s "$w/off.yaml" ]; then
    echo "R2: enabled=false still renders: $(names "$w/off.yaml")"
    bad=1
  fi
  if ! render "$chart" "$w/off-enforced.yaml" --set enabled=false --set 'enforcedNamespaces={alpha,beta}'; then
    echo "R2: the render with enabled=false and enforced namespaces failed: $(cat "$w/off-enforced.yaml.err")"
    return 1
  fi
  if [ -s "$w/off-enforced.yaml" ]; then
    echo "R2: enabled=false with enforced namespaces still renders: $(names "$w/off-enforced.yaml")"
    bad=1
  fi

  # R3
  if ! render "$chart" "$w/enforced.yaml" --skip-tests --set 'enforcedNamespaces={alpha,beta}'; then
    echo "R3: the render with enforcedNamespaces={alpha,beta} failed: $(cat "$w/enforced.yaml.err")"
    return 1
  fi
  if [ "$(names "$w/enforced.yaml")" != "baseline-default-deny baseline-deny-metadata" ]; then
    echo "R3: enforcedNamespaces={alpha,beta} renders: $(names "$w/enforced.yaml")"
    bad=1
  fi
  object "$w/enforced.yaml" baseline-default-deny "$w/deny.yaml"
  if ! cmp -s "$w/deny.yaml" "$TMP/expected-deny.yaml"; then
    echo "R3: the object baseline-default-deny is not exactly the one written out in this test"
    bad=1
  fi

  # R4: the files of the folder, the allow files of the chart, then the dummy in a copy.
  for f in "$chart"/templates/allow/*; do
    [ -e "$f" ] || continue
    case "$(basename "$f")" in
      _*.tpl | *.yaml) ;;
      *) echo "R4: templates/allow/$(basename "$f") is neither a partial _*.tpl nor an allow file <namespace>.yaml"; bad=1 ;;
    esac
  done
  for f in "$chart"/templates/allow/*.yaml; do
    [ -e "$f" ] || continue
    ns="$(basename "$f" .yaml)"
    if ! render "$chart" "$w/other-$ns.yaml" --skip-tests --set 'enforcedNamespaces={net-other}'; then
      echo "R4: the render with enforcedNamespaces={net-other} failed: $(cat "$w/other-$ns.yaml.err")"
      return 1
    fi
    if [ "$(names "$w/other-$ns.yaml")" != "baseline-default-deny baseline-deny-metadata" ]; then
      echo "R4: templates/allow/$ns.yaml renders while $ns is not enforced: $(names "$w/other-$ns.yaml")"
      bad=1
    fi
    if ! render "$chart" "$w/own-$ns.yaml" --skip-tests --set "enforcedNamespaces={$ns}"; then
      echo "R4: the render with enforcedNamespaces={$ns} failed: $(cat "$w/own-$ns.yaml.err")"
      return 1
    fi
    if [ "$(names "$w/own-$ns.yaml")" != "allow-$ns baseline-default-deny baseline-deny-metadata" ]; then
      echo "R4: templates/allow/$ns.yaml does not render exactly allow-$ns while $ns is enforced: $(names "$w/own-$ns.yaml")"
      bad=1
    fi
  done
  rm -rf "$w/dummy"
  cp -R "$chart" "$w/dummy"
  mkdir -p "$w/dummy/templates/allow"
  cp "$TMP/net-dummy.yaml" "$w/dummy/templates/allow/net-dummy.yaml"
  if ! render "$w/dummy" "$w/dummy-default.yaml" --skip-tests || ! cmp -s "$w/dummy-default.yaml" "$TMP/expected.yaml"; then
    echo "R4: with an allow file, the default render is not exactly the object baseline-deny-metadata"
    bad=1
  fi
  if ! render "$w/dummy" "$w/dummy-other.yaml" --skip-tests --set 'enforcedNamespaces={net-other}'; then
    echo "R4: the render of the dummy with enforcedNamespaces={net-other} failed: $(cat "$w/dummy-other.yaml.err")"
    return 1
  fi
  if [ "$(names "$w/dummy-other.yaml")" != "baseline-default-deny baseline-deny-metadata" ]; then
    echo "R4: the dummy allow file renders while net-dummy is not enforced: $(names "$w/dummy-other.yaml")"
    bad=1
  fi
  if ! render "$w/dummy" "$w/dummy-own.yaml" --skip-tests --set 'enforcedNamespaces={net-other,net-dummy}'; then
    echo "R4: the render of the dummy with net-dummy enforced failed: $(cat "$w/dummy-own.yaml.err")"
    return 1
  fi
  if [ "$(names "$w/dummy-own.yaml")" != "allow-net-dummy baseline-default-deny baseline-deny-metadata" ]; then
    echo "R4: the dummy allow file does not render exactly allow-net-dummy while net-dummy is enforced: $(names "$w/dummy-own.yaml")"
    bad=1
  fi
  object "$w/dummy-own.yaml" allow-net-dummy "$w/allow.yaml"
  if ! cmp -s "$w/allow.yaml" "$TMP/expected-allow.yaml"; then
    echo "R4: the object allow-net-dummy is not exactly the one written out in this test"
    bad=1
  fi

  printf '{{- define "allow.net-yml.rules" }}\ningress: []\n{{- end }}\n{{- include "network-policies.allow" . }}\n' \
    > "$w/dummy/templates/allow/net-yml.yml"
  if render "$w/dummy" "$w/yml.yaml" --skip-tests --set 'enforcedNamespaces={net-yml}'; then
    echo "R4: an allow file net-yml.yml renders; the helper must refuse a file that is not <namespace>.yaml"
    bad=1
  elif ! grep -q -F "templates/allow/net-yml.yml: an allow file is named <namespace>.yaml" "$w/yml.yaml.err"; then
    echo "R4: the file net-yml.yml stops the render without the message of the helper: $(tr '\n' ' ' < "$w/yml.yaml.err" | cut -c1-200)"
    bad=1
  fi
  rm -f "$w/dummy/templates/allow/net-yml.yml"

  # R5: each accepted rule in the dummy must render, and each broken rule must
  # stop the render with its message.
  while IFS='|' read -r name rules; do
    printf '{{- define "allow.net-dummy.rules" }}\n%s\n{{- end }}\n{{- include "network-policies.allow" . }}\n' "$rules" \
      > "$w/dummy/templates/allow/net-dummy.yaml"
    if ! render "$w/dummy" "$w/accepted-$name.yaml" --skip-tests --set 'enforcedNamespaces={net-dummy}'; then
      echo "R5: the accepted rule of the case $name does not render: $(tr '\n' ' ' < "$w/accepted-$name.yaml.err" | cut -c1-200)"
      bad=1
    elif [ "$(names "$w/accepted-$name.yaml")" != "allow-net-dummy baseline-default-deny baseline-deny-metadata" ]; then
      echo "R5: the accepted rule of the case $name renders: $(names "$w/accepted-$name.yaml")"
      bad=1
    fi
  done < "$TMP/accepted.txt"
  while IFS='|' read -r name want rules; do
    printf '{{- define "allow.net-dummy.rules" }}\n%s\n{{- end }}\n{{- include "network-policies.allow" . }}\n' "$rules" \
      > "$w/dummy/templates/allow/net-dummy.yaml"
    if render "$w/dummy" "$w/broken-$name.yaml" --skip-tests --set 'enforcedNamespaces={net-dummy}'; then
      echo "R5: an allow rule of the case $name renders; the helper must refuse it"
      bad=1
    elif ! grep -q -F "templates/allow/net-dummy.yaml: " "$w/broken-$name.yaml.err" || ! grep -q -F "$want" "$w/broken-$name.yaml.err"; then
      echo "R5: the case $name stops the render without the message '$want': $(tr '\n' ' ' < "$w/broken-$name.yaml.err" | cut -c1-200)"
      bad=1
    fi
  done < "$TMP/broken.txt"

  # R6
  while IFS='|' read -r name how flag; do
    if render "$chart" "$w/bad-$name.yaml" --skip-tests "$how" "$flag"; then
      echo "R6: the value $how $flag (case $name) renders; it must stop the render"
      bad=1
    fi
  done < "$TMP/bad-values.txt"

  return "$bad"
}

echo "[render] the chart at $CHART_DIR"
if ! check_chart "$CHART_DIR" "$TMP/real"; then
  echo "FAIL: the chart breaks a rule (lines above)." >&2
  exit 1
fi
echo "  PASS: R1 to R6"

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
B=templates/baseline-default-deny.yaml
H=templates/allow/_helpers.tpl
mutant address        R1 "$D" 's|169\.254\.169\.254/32|169.254.0.0/16|'
mutant default-deny   R1 "$D" 's|^    egress: false$|    egress: true|'
mutant selector       R1 "$D" 's|operator: NotIn|operator: In|'
mutant enforced-value R1 values.yaml 's|^enforcedNamespaces: \[\]$|enforcedNamespaces: [default]|'
mutant deny-ungated   R2 "$D" 's|^{{- if \.Values\.enabled }}$|{{- if true }}|'
mutant test-ungated   R2 templates/tests/function-test.yaml 's|^{{- if \.Values\.enabled }}$|{{- if true }}|'
mutant base-ungated   R2 "$B" 's|^{{- if \.Values\.enabled }}$|{{- if true }}|'
mutant ingress-open   R3 "$B" 's|^    - {}$|    - fromEntities: [cluster]|'
mutant dns-port       R3 "$B" '0,/port: "53"/s|port: "53"|port: "5353"|'
mutant no-dns-proxy   R3 "$B" 's|- matchPattern: "\*"|- matchPattern: "*.cluster.local"|'
mutant allow-ungated  R4 "$H" 's#{{- if and \.Values\.enabled (has \$ns (\.Values\.enforcedNamespaces | default list)) -}}#{{- if true -}}#'
mutant no-session     R4 "$H" 's|operator: DoesNotExist|operator: Exists|'
mutant no-same-ns     R4 "$H" '0,/fromEndpoints:$/s|fromEndpoints:$|fromEntities: [cluster]\n      fromEndpoints:|'
mutant any-port       R5 "$H" 's|{{- if not \$rule\.toPorts -}}|{{- if false -}}|'
mutant any-name       R6 "$B" 's|regexMatch "[^"]*"|regexMatch ".*"|'
mutant twice-ok       R6 "$B" 's|{{- if ne (len \$enforced) (len (uniq \$enforced)) }}|{{- if false }}|'
mutant type-ok        R6 "$B" 's|{{- if not (or (kindIs "invalid" \$raw) (kindIs "slice" \$raw)) }}|{{- if false }}|'
mutant name-ok        R4 "$H" 's|{{- if or (not (hasSuffix ".yaml" \$file)) (not |{{- if and false (not |'
mutant peer-ok        R5 "$H" 's|{{- if not \$named -}}|{{- if false -}}|'
mutant key-ok         R5 "$H" 's|{{- else if ne \$key "toPorts" -}}|{{- else if false -}}|'
mutant entity-ok      R5 "$H" 's|{{- if not (has \. (list "ingress" "kube-apiserver" "host" "remote-node")) -}}|{{- if has . (list "world" "all") -}}|'
mutant entity-narrow  R5 "$H" 's|(list "ingress" "kube-apiserver" "host" "remote-node")|(list "ingress" "host" "remote-node")|'
mutant selector-ok    R5 "$H" 's|{{- if not (or \$byLabel \$byTerm) -}}|{{- if false -}}|'
mutant range-width    R5 "$H" 's|{{- if or (eq \$cidr "") (lt \$len \$min) -}}|{{- if has $cidr (list "0.0.0.0/0" "::/0") -}}|'
mutant fqdn-ok        R5 "$H" 's|{{- if or (contains "\*" \$name) (not (contains "\." \$name)) -}}|{{- if eq $name "*" -}}|'
mutant port-spelling  R5 "$H" 's|(not (regexMatch "^\[1-9\]|(not (regexMatch "^[0-9]|'
mutant every-port-ok  R5 "$H" 's|{{- if and \.endPort (eq (atoi \$port) 1)|{{- if and false (eq (atoi $port) 1)|'

if [ "$mutants" != "27" ]; then
  echo "FAIL: self-test ran $mutants changed copies, expected 27." >&2
  exit 1
fi
echo "  PASS: self-test, 27 changed copies of the chart each fail their rule"
echo "[render] all green."
