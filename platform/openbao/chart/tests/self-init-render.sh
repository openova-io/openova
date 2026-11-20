#!/usr/bin/env bash
# bp-openbao — the self-initialisation block (SEC-162).
#
# On the first start of an empty store OpenBao runs the initialize block of
# ConfigMap openbao-seal-config (initialize.hcl) with a root token it revokes
# at once. This test asserts:
#
#   I1  exactly four requests, in order, each an update: the Kubernetes auth
#       mount sys/auth/kubernetes (type kubernetes), its config (the
#       in-cluster API server), policy openbao-config-admin, and role
#       openbao-config-admin;
#   I2  the policy text it writes equals the payload of the first operation
#       of the reconciler's desired state (openbao.configAdminPolicy), so the
#       reconciler keeps that policy current on later chart versions;
#   I3  the role binds only service account openbao-config of the release
#       namespace, to policy openbao-config-admin only, with token TTL 15m
#       and max TTL 1h;
#   I4  kubernetesAuth.mountPath moves the mount, its config and the role,
#       and the reconciler logs in on that mount;
#   I5  selfInit.enabled=false renders no initialize.hcl and keeps seal.hcl.
#
# Vacuity: I1 must fail on a block with a fifth request.
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
render() {  # $1 out, $2 namespace, rest: more helm flags
  local out=$1 ns=$2; shift 2
  helm template openbao . --namespace "$ns" --set-file seal.stanza="$TMP/stanza.hcl" "$@" > "$out"
}
cmkey() {  # $1 render, $2 ConfigMap, $3 key -> the value, or nothing
  yq "select(.kind == \"ConfigMap\" and .metadata.name == \"$2\") | .data[\"$3\"] | select(. != null)" "$1"
}
render "$TMP/kit.yaml" openbao
cmkey "$TMP/kit.yaml" openbao-seal-config initialize.hcl > "$TMP/init.hcl"

# check_requests <initialize.hcl> <mount>: prints one line per fault of I1.
check_requests() {
  grep -E '^ *request "' "$1" | sed 's/^ *request "\([^"]*\)".*/\1/' > "$TMP/req.txt"
  printf '%s\n' auth-kubernetes auth-kubernetes-config policy-config-admin role-config-admin > "$TMP/req.want"
  cmp -s "$TMP/req.txt" "$TMP/req.want" || echo "requests $(tr '\n' ' ' < "$TMP/req.txt")"
  grep -E '^ *path *= ' "$1" | sed 's/^ *path *= *"\([^"]*\)".*/\1/' > "$TMP/path.txt"
  printf '%s\n' "sys/auth/$2" "auth/$2/config" sys/policies/acl/openbao-config-admin "auth/$2/role/openbao-config-admin" > "$TMP/path.want"
  cmp -s "$TMP/path.txt" "$TMP/path.want" || echo "paths $(tr '\n' ' ' < "$TMP/path.txt")"
  [ "$(grep -c -E '^ *operation *= *"update"$' "$1")" = 4 ] || echo "not four update operations"
  grep -q -x -F '    data      = { type = "kubernetes" }' "$1" || echo "the mount is not of type kubernetes"
  grep -q -x -F '    data      = { kubernetes_host = "https://kubernetes.default.svc" }' "$1" || echo "the mount does not review tokens at the in-cluster API server"
  grep -q -x -F 'initialize "platform" {' "$1" || echo "no initialize block named platform"
}

# I1
check_requests "$TMP/init.hcl" kubernetes > "$TMP/i1.txt"
if [ ! -s "$TMP/i1.txt" ]; then pass "I1 four update requests in order: auth mount, its config, policy, role"
else fail "I1 $(tr '\n' ';' < "$TMP/i1.txt")"; fi

# I2: the JSON string after `policy =` decodes to the policy of the first desired op.
sed -n 's/^ *data *= { policy = \(".*"\) }$/\1/p' "$TMP/init.hcl" | jq -r . > "$TMP/policy-init.hcl"
cmkey "$TMP/kit.yaml" openbao-config-desired sys.policies.acl.openbao-config-admin.json | jq -r .policy > "$TMP/policy-op.hcl"
cmkey "$TMP/kit.yaml" openbao-config-desired manifest > "$TMP/manifest.txt"
if [ -s "$TMP/policy-init.hcl" ] && cmp -s "$TMP/policy-init.hcl" "$TMP/policy-op.hcl" \
  && [ "$(grep -m1 . "$TMP/manifest.txt")" = "write sys/policies/acl/openbao-config-admin" ]; then
  pass "I2 the policy it writes equals the first op of the desired state"
else fail "I2 the policy of the self-initialisation differs from the first desired op"; diff "$TMP/policy-op.hcl" "$TMP/policy-init.hcl" || true; fi

# I3, in another namespace so that the binding follows the release namespace
render "$TMP/vault.yaml" vault
cmkey "$TMP/vault.yaml" openbao-seal-config initialize.hcl > "$TMP/init-vault.hcl"
if grep -q -x -F '      bound_service_account_names      = ["openbao-config"]' "$TMP/init-vault.hcl" \
  && grep -q -x -F '      bound_service_account_namespaces = ["vault"]' "$TMP/init-vault.hcl" \
  && grep -q -x -F '      token_policies                   = ["openbao-config-admin"]' "$TMP/init-vault.hcl" \
  && grep -q -x -F '      token_ttl                        = "15m"' "$TMP/init-vault.hcl" \
  && grep -q -x -F '      token_max_ttl                    = "1h"' "$TMP/init-vault.hcl"; then
  pass "I3 the role binds openbao-config of the release namespace only, policy openbao-config-admin, 15m / 1h"
else fail "I3 the role binding"; sed -n '/role-config-admin/,$p' "$TMP/init-vault.hcl"; fi

# I4
render "$TMP/k8s.yaml" openbao --set kubernetesAuth.mountPath=k8s
cmkey "$TMP/k8s.yaml" openbao-seal-config initialize.hcl > "$TMP/init-k8s.hcl"
check_requests "$TMP/init-k8s.hcl" k8s > "$TMP/i4.txt"
yq 'select(.kind == "Deployment" and .metadata.name == "openbao-config") | .spec.template.spec.containers[0].env[] | select(.name == "BAO_K8S_AUTH_PATH") | .value' "$TMP/k8s.yaml" > "$TMP/login.txt"
if [ ! -s "$TMP/i4.txt" ] && [ "$(cat "$TMP/login.txt")" = k8s ]; then pass "I4 kubernetesAuth.mountPath moves the mount, its config, the role and the reconciler's login"
else fail "I4 mountPath k8s: $(tr '\n' ';' < "$TMP/i4.txt") login on $(cat "$TMP/login.txt")"; fi

# I5
render "$TMP/off.yaml" openbao --set selfInit.enabled=false
yq 'select(.kind == "ConfigMap" and .metadata.name == "openbao-seal-config") | .data | keys | join(",")' "$TMP/off.yaml" > "$TMP/keys.txt"
if [ "$(cat "$TMP/keys.txt")" = "seal.hcl,audit.hcl" ]; then pass "I5 selfInit.enabled=false renders no initialize.hcl"
else fail "I5 keys with selfInit off: $(cat "$TMP/keys.txt")"; fi

# Vacuity
sed 's/^  request "role-config-admin" {$/  request "token" {\n    operation = "update"\n    path      = "auth\/token\/create"\n  }\n  request "role-config-admin" {/' "$TMP/init.hcl" > "$TMP/mutant.hcl"
check_requests "$TMP/mutant.hcl" kubernetes > "$TMP/v1.txt"
if [ -s "$TMP/v1.txt" ]; then pass "I1 vacuity: a fifth request is caught"; else fail "I1 vacuity: a fifth request passed"; fi

if [ "$FAIL" -ne 0 ]; then echo "FAIL: bp-openbao self-initialisation"; exit 1; fi
echo "PASS: bp-openbao self-initialisation — four requests, the reconciler's own grant only"
