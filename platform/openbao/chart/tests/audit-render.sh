#!/usr/bin/env bash
# bp-openbao — the declared audit device (SEC-162).
#
# OpenBao 2.7 refuses an audit device enabled through the API; the chart
# declares it in the server configuration instead (audit.hcl of ConfigMap
# openbao-seal-config). This test asserts:
#
#   A1  audit.hcl declares exactly the file device stdout/ writing to
#       standard output, where the log pipeline collects it;
#   A2  audit.enabled=false renders no audit.hcl and keeps seal.hcl;
#   A3  nothing else in the chart touches sys/audit: no desired-state
#       operation, no script and no container command or args enables or
#       disables an audit device, by HTTP path or by CLI.
#
# Vacuity: A1 must fail on a device that writes to a file in the pod; A3 must
# fail on `bao audit enable` in a container's args and on sys/audit in a Job's
# command, and it fails when the scan read no StatefulSet, ConfigMap or Pod.
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cd "$CHART_DIR"
if ! ls charts/*.tgz >/dev/null 2>&1; then helm dependency build >/dev/null; fi

FAIL=0
pass() { echo "  PASS  $1"; }
fail() { echo "  FAIL  $1"; FAIL=1; }

printf 'seal "static" {\n  current_key_id = "k1"\n  current_key    = "file:///openbao/seal/openbao-seal-k1.key"\n}\n' > "$TMP/stanza.hcl"
helm template openbao . --namespace openbao --set-file seal.stanza="$TMP/stanza.hcl" > "$TMP/kit.yaml"
helm template openbao . --namespace openbao --set-file seal.stanza="$TMP/stanza.hcl" --set audit.enabled=false > "$TMP/off.yaml"
cat > "$TMP/want.hcl" <<'EOF'
audit "file" "stdout" {
  description = "Audit to standard output; collected by the log pipeline."
  options {
    file_path = "stdout"
  }
}
EOF

# check_audit <audit.hcl>: prints a fault line unless the file is the declared stdout device.
check_audit() {
  cmp -s "$1" "$TMP/want.hcl" || echo "audit.hcl is not the stdout file device: $(tr '\n' ' ' < "$1")"
}

# A1
yq 'select(.kind == "ConfigMap" and .metadata.name == "openbao-seal-config") | .data["audit.hcl"]' "$TMP/kit.yaml" > "$TMP/audit.raw"
grep -v -x '' "$TMP/audit.raw" > "$TMP/audit.hcl" || true
check_audit "$TMP/audit.hcl" > "$TMP/a1.txt"
if [ ! -s "$TMP/a1.txt" ]; then pass "A1 audit.hcl declares the file device stdout/ on standard output"
else fail "A1 $(cat "$TMP/a1.txt")"; fi

# A2
yq 'select(.kind == "ConfigMap" and .metadata.name == "openbao-seal-config") | .data | keys | join(",")' "$TMP/off.yaml" > "$TMP/keys.txt"
if [ "$(cat "$TMP/keys.txt")" = "seal.hcl,initialize.hcl" ]; then pass "A2 audit.enabled=false renders no audit.hcl"
else fail "A2 keys with audit off: $(cat "$TMP/keys.txt")"; fi

# A3: every rendered object except ConfigMap openbao-seal-config, comments dropped, so
# ConfigMap scripts and the commands and args of every Pod spec (server, reconcilers,
# test Pods) are read; the HTTP path and the CLI form both count.
scan_audit() { grep -E 'sys/audit|audit[[:space:]]+(enable|disable)' "$1" || true; }
yq 'select(.kind != null and (.kind != "ConfigMap" or .metadata.name != "openbao-seal-config")) | ... comments = ""' "$TMP/kit.yaml" > "$TMP/others.yaml"
yq 'select(.kind != null) | .kind' "$TMP/others.yaml" | sort -u > "$TMP/kinds.txt"
if ! grep -q -x -F StatefulSet "$TMP/kinds.txt" || ! grep -q -x -F ConfigMap "$TMP/kinds.txt" || ! grep -q -x -F Pod "$TMP/kinds.txt"; then
  fail "A3 the scan read no StatefulSet, ConfigMap or test Pod: kinds $(tr '\n' ' ' < "$TMP/kinds.txt")"
elif [ -z "$(scan_audit "$TMP/others.yaml")" ]; then pass "A3 no operation, script or container of the chart touches sys/audit or enables an audit device"
else fail "A3 an audit device is touched outside audit.hcl: $(scan_audit "$TMP/others.yaml" | tr '\n' ' ')"; fi

# Vacuity
sed 's|file_path = "stdout"|file_path = "/openbao/audit/audit.log"|' "$TMP/audit.hcl" > "$TMP/mutant.hcl"
check_audit "$TMP/mutant.hcl" > "$TMP/v1.txt"
if [ -s "$TMP/v1.txt" ]; then pass "A1 vacuity: a device writing to a file in the pod is caught"; else fail "A1 vacuity: a file device passed"; fi
printf -- '---\napiVersion: v1\nkind: Pod\nmetadata: {name: m}\nspec:\n  containers:\n  - name: m\n    args: ["bao audit enable file file_path=stdout"]\n' | cat "$TMP/others.yaml" - > "$TMP/m-cli.yaml"
if [ -n "$(scan_audit "$TMP/m-cli.yaml")" ]; then pass "A3 vacuity: bao audit enable in a container's args is caught"; else fail "A3 vacuity: bao audit enable in args passed"; fi
printf -- '---\napiVersion: batch/v1\nkind: Job\nmetadata: {name: m}\nspec:\n  template:\n    spec:\n      containers:\n      - name: m\n        command: ["sh", "-c", "curl -X PUT http://openbao:8200/v1/sys/audit/stdout"]\n' | cat "$TMP/others.yaml" - > "$TMP/m-http.yaml"
if [ -n "$(scan_audit "$TMP/m-http.yaml")" ]; then pass "A3 vacuity: sys/audit in a Job's command is caught"; else fail "A3 vacuity: sys/audit in a command passed"; fi

if [ "$FAIL" -ne 0 ]; then echo "FAIL: bp-openbao declared audit device"; exit 1; fi
echo "PASS: bp-openbao declared audit device — stdout/ in the server configuration only"
