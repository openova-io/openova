#!/usr/bin/env bash
# bp-sso-bridge — G117.5-followup #2914 dual-token KC mint shape test.
#
# Verifies the chart-side glue for the master-realm admin token path
# that closes the 403-on-POST-/admin/realms gap:
#
#   1. New stub Secret with reflects annotation pointing at
#      catalyst-kc-master-admin-credentials in keycloak ns.
#   2. Deployment env vars KC_MASTER_REALM / KC_MASTER_ADMIN_USER /
#      KC_MASTER_ADMIN_PASSWORD all carry optional:true so the Pod
#      doesn't crash on first install before reflector mirrors.
#   3. reconciler ConfigMap exports mint_kc_master_admin_token() that
#      uses grant_type=password against /realms/master/.../token via
#      admin-cli client.
#   4. provision_org_realm calls mint_kc_master_admin_token AND uses
#      KC_MASTER_ADMIN_TOKEN (not KC_ADMIN_TOKEN) for POST /admin/realms.
#
# Refs #2914 #2744.
set -euo pipefail
CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cd "$CHART_DIR"
helm="${HELM_BIN:-helm}"

"$helm" template . > "$TMP/all.yaml"

fail=0
assert_grep() {
  local pattern="$1" file="$2" msg="$3"
  if ! grep -q -- "$pattern" "$file"; then
    echo "FAIL: $msg"
    echo "      pattern: $pattern"
    fail=1
  fi
}

# (1) Stub Secret with the reflects annotation
"$helm" template . --show-only templates/secret-kc-credentials-mirror.yaml > "$TMP/secret.yaml"
assert_grep "name: catalyst-kc-master-admin-credentials" "$TMP/secret.yaml" \
  "(1) master-admin-credentials stub Secret not rendered"
assert_grep 'reflector.v1.k8s.emberstack.com/reflects: "keycloak/catalyst-kc-master-admin-credentials"' "$TMP/secret.yaml" \
  "(1) reflects annotation missing or wrong target"

# (2) Deployment env vars with optional:true
"$helm" template . --show-only templates/deployment.yaml > "$TMP/deploy.yaml"
for env in KC_MASTER_REALM KC_MASTER_ADMIN_USER KC_MASTER_ADMIN_PASSWORD; do
  assert_grep "name: $env" "$TMP/deploy.yaml" "(2) env var $env missing"
done
# optional:true must appear at least 3 times (one per env)
opt_count="$(grep -c "optional: true" "$TMP/deploy.yaml" || true)"
if [ "$opt_count" -lt 3 ]; then
  echo "FAIL: (2) expected >=3 'optional: true' entries (one per master env), got $opt_count"
  fail=1
fi

# (3) mint_kc_master_admin_token fn + password grant + admin-cli + master realm
"$helm" template . --show-only templates/configmap-reconciler.yaml > "$TMP/recon.yaml"
assert_grep "mint_kc_master_admin_token()" "$TMP/recon.yaml" \
  "(3) mint_kc_master_admin_token fn missing"
assert_grep "grant_type=password" "$TMP/recon.yaml" \
  "(3) password grant flow missing from reconciler"
assert_grep "client_id=admin-cli" "$TMP/recon.yaml" \
  "(3) admin-cli client_id missing"
assert_grep 'realms/${KC_MASTER_REALM}/protocol/openid-connect/token' "$TMP/recon.yaml" \
  "(3) /realms/master/.../token endpoint missing"

# (4) provision_org_realm uses KC_MASTER_ADMIN_TOKEN for POST /admin/realms.
# Extract the provision_org_realm body by line-range from its declaration
# down to the next 4-space-indented fn declaration.
provision_start="$(grep -n "provision_org_realm()" "$TMP/recon.yaml" | head -1 | cut -d: -f1)"
if [ -z "$provision_start" ]; then
  echo "FAIL: (4) provision_org_realm() declaration not found"
  fail=1
else
  provision_end="$(awk -v start="$provision_start" 'NR > start && /^    [a-z_]+\(\) \{$/ {print NR; exit}' "$TMP/recon.yaml")"
  [ -z "$provision_end" ] && provision_end="$(wc -l < "$TMP/recon.yaml")"
  sed -n "${provision_start},${provision_end}p" "$TMP/recon.yaml" > "$TMP/provision.yaml"
  assert_grep "mint_kc_master_admin_token" "$TMP/provision.yaml" \
    "(4) provision_org_realm doesn't call mint_kc_master_admin_token"
  # POST /admin/realms is on the curl line ending with `"${KC_ADDR%/}/admin/realms"`;
  # the Auth header is up to 5 lines above (followed by Content-Type + data-binary).
  # grep -F for the literal URL fragment to avoid regex-anchor escapes.
  if ! grep -B5 -F 'KC_ADDR%/}/admin/realms"' "$TMP/provision.yaml" | grep -q "Bearer .*KC_MASTER_ADMIN_TOKEN"; then
    echo "FAIL: (4) POST /admin/realms not using KC_MASTER_ADMIN_TOKEN"
    fail=1
  fi
fi

if [ "$fail" -ne 0 ]; then
  echo
  echo "=== g117-fu-2914-master-realm-dual-token: FAIL ==="
  exit 1
fi
echo "=== g117-fu-2914-master-realm-dual-token: PASS ==="
