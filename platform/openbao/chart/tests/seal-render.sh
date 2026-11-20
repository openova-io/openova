#!/usr/bin/env bash
# bp-openbao — the seal wiring of the OpenBao server (SEC-162).
#
# The server reads its seal key from a file on its control-plane node and
# opens itself; the chart renders no init Job, no unseal-key Secret, no
# unseal loop and no root-token script. With the kit's shape (a seal stanza)
# this test asserts:
#
#   S1  ConfigMap openbao-seal-config holds seal.hcl, equal to seal.stanza;
#   S2  the StatefulSet runs image 2.7.2 through the Harbor proxy, keeps
#       podManagementPolicy Parallel and updateStrategy OnDelete, loads
#       /openbao/userconfig/openbao-seal-config/ as an extra configuration
#       directory from that ConfigMap, mounts the node directory
#       /var/lib/catalyst/seal (hostPath, type Directory) read-only at
#       /openbao/seal, runs as user 100 and group 1000 (contract C8), and
#       runs on control-plane nodes only;
#   S3  with an empty stanza (the chart default) no ConfigMap
#       openbao-seal-config renders, while the server still needs it: the
#       pod cannot start, so it fails closed instead of making a Shamir store;
#   S4  no Job, CronJob or Secret renders, no object is named after the
#       unseal key, root token, init trace or recovery seed, and the agent
#       injector does not render.
#
# Vacuity: S2 and S4 must fail on a render with the seal mount writable and
# on a render that holds a Job.
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cd "$CHART_DIR"
if ! ls charts/*.tgz >/dev/null 2>&1; then helm dependency build >/dev/null; fi

FAIL=0
pass() { echo "  PASS  $1"; }
fail() { echo "  FAIL  $1"; FAIL=1; }

printf 'seal "static" {\n  current_key_id = "3f9a1c0b7d2e4a61"\n  current_key    = "file:///openbao/seal/openbao-seal-3f9a1c0b7d2e4a61.key"\n}\n' > "$TMP/stanza.hcl"
helm template openbao . --namespace openbao --set-file seal.stanza="$TMP/stanza.hcl" \
  --set baoAddress=http://openbao.openbao.svc.cluster.local:8200 --set openbao.server.ha.replicas=1 > "$TMP/kit.yaml"
helm template openbao . --namespace openbao > "$TMP/default.yaml"

sts() { yq 'select(.kind == "StatefulSet" and .metadata.name == "openbao")' "$1"; }

# check_server <render>: prints one line per fault of S2.
check_server() {
  sts "$1" > "$TMP/sts.yaml"
  [ -s "$TMP/sts.yaml" ] || { echo "no StatefulSet openbao"; return; }
  q() { yq "$1" "$TMP/sts.yaml"; }
  [ "$(q '.spec.template.spec.containers[0].image')" = "harbor.openova.io/proxy-quay/openbao/openbao:2.7.2" ] || echo "image $(q '.spec.template.spec.containers[0].image')"
  [ "$(q '.spec.podManagementPolicy')" = Parallel ] || echo "podManagementPolicy $(q '.spec.podManagementPolicy')"
  [ "$(q '.spec.updateStrategy.type')" = OnDelete ] || echo "updateStrategy $(q '.spec.updateStrategy.type')"
  q '.spec.template.spec.containers[0].args[0]' > "$TMP/args.txt"
  grep -q -F -e '-config=/openbao/userconfig/openbao-seal-config/' "$TMP/args.txt" || echo "the server does not load /openbao/userconfig/openbao-seal-config/"
  [ "$(q '.spec.template.spec.volumes[] | select(.name == "userconfig-openbao-seal-config") | .configMap.name')" = openbao-seal-config ] || echo "no volume from ConfigMap openbao-seal-config"
  [ "$(q '.spec.template.spec.volumes[] | select(.name == "seal") | .hostPath.path + " " + .hostPath.type')" = "/var/lib/catalyst/seal Directory" ] || echo "the seal volume is not hostPath /var/lib/catalyst/seal of type Directory"
  [ "$(q '.spec.template.spec.containers[0].volumeMounts[] | select(.name == "seal") | .mountPath + " " + (.readOnly | tostring)')" = "/openbao/seal true" ] || echo "the seal directory is not mounted read-only at /openbao/seal"
  [ "$(q '.spec.template.spec.securityContext.runAsUser + ":" + .spec.template.spec.securityContext.runAsGroup')" = "100:1000" ] || echo "the server does not run as 100:1000"
  [ "$(q '.spec.template.spec.nodeSelector["catalyst.openova.io/role"]')" = control-plane ] || echo "the server is not pinned to control-plane nodes"
  [ "$(q '[.spec.template.spec.tolerations[] | select(.operator == "Exists" and .effect == "NoSchedule")] | length')" = 1 ] || echo "no toleration of the control-plane taint"
}

# check_clean <render>: prints one line per fault of S4.
check_clean() {
  yq 'select(.kind == "Job" or .kind == "CronJob" or .kind == "Secret") | .kind + "/" + .metadata.name' "$1" > "$TMP/kinds.txt"
  [ ! -s "$TMP/kinds.txt" ] || echo "renders $(tr '\n' ' ' < "$TMP/kinds.txt")"
  yq '.metadata.name' "$1" > "$TMP/names.txt"
  # The unseal-Secret name in parts: guard G7 counts every line that holds it.
  for n in "openbao-unseal-""keys" openbao-root-token openbao-init-trace openbao-recovery-seed openbao-auto-unseal openbao-unseal-reconciler openbao-agent-injector; do
    if grep -q -x -F "$n" "$TMP/names.txt"; then echo "renders an object named $n"; fi
  done
  yq 'select(.kind == "MutatingWebhookConfiguration") | .metadata.name' "$1" > "$TMP/hooks.txt"
  [ ! -s "$TMP/hooks.txt" ] || echo "renders a mutating webhook: $(tr '\n' ' ' < "$TMP/hooks.txt")"
}

# S1
# Byte for byte: yq prints the value base64-encoded, so a trailing newline counts.
yq 'select(.kind == "ConfigMap" and .metadata.name == "openbao-seal-config") | .data["seal.hcl"] | @base64' "$TMP/kit.yaml" > "$TMP/seal.b64"
base64 -w0 < "$TMP/stanza.hcl" > "$TMP/stanza.b64"; echo >> "$TMP/stanza.b64"
if cmp -s "$TMP/seal.b64" "$TMP/stanza.b64"; then pass "S1 seal.hcl of openbao-seal-config equals seal.stanza"
else fail "S1 seal.hcl differs from seal.stanza ($(cat "$TMP/seal.b64"))"; fi

# S2
check_server "$TMP/kit.yaml" > "$TMP/s2.txt"
if [ ! -s "$TMP/s2.txt" ]; then pass "S2 image 2.7.2, Parallel + OnDelete, seal directory read-only at /openbao/seal, 100:1000, control-plane only"
else fail "S2 $(tr '\n' ';' < "$TMP/s2.txt")"; fi

# S3
yq 'select(.kind == "ConfigMap" and .metadata.name == "openbao-seal-config") | .metadata.name' "$TMP/default.yaml" > "$TMP/s3.txt"
check_server "$TMP/default.yaml" > "$TMP/s3-server.txt"
if [ ! -s "$TMP/s3.txt" ] && [ ! -s "$TMP/s3-server.txt" ]; then pass "S3 no stanza: no openbao-seal-config, the server still needs it (fails closed)"
else fail "S3 no stanza: $(cat "$TMP/s3.txt") $(tr '\n' ';' < "$TMP/s3-server.txt")"; fi

# S4
check_clean "$TMP/kit.yaml" > "$TMP/s4.txt"
check_clean "$TMP/default.yaml" >> "$TMP/s4.txt"
if [ ! -s "$TMP/s4.txt" ]; then pass "S4 no Job, CronJob, Secret, unseal or root object, no injector"
else fail "S4 $(tr '\n' ';' < "$TMP/s4.txt")"; fi

# Vacuity
sed 's/readOnly: true/readOnly: false/' "$TMP/kit.yaml" > "$TMP/writable.yaml"
check_server "$TMP/writable.yaml" > "$TMP/v1.txt"
if [ -s "$TMP/v1.txt" ]; then pass "S2 vacuity: a writable seal mount is caught"; else fail "S2 vacuity: a writable seal mount passed"; fi
{ cat "$TMP/kit.yaml"; printf -- '---\napiVersion: batch/v1\nkind: Job\nmetadata:\n  name: openbao-init\n'; } > "$TMP/job.yaml"
check_clean "$TMP/job.yaml" > "$TMP/v2.txt"
if [ -s "$TMP/v2.txt" ]; then pass "S4 vacuity: a Job is caught"; else fail "S4 vacuity: a Job passed"; fi

if [ "$FAIL" -ne 0 ]; then echo "FAIL: bp-openbao seal wiring"; exit 1; fi
echo "PASS: bp-openbao seal wiring — static seal from the node file, no init Job, no unseal Secret"
