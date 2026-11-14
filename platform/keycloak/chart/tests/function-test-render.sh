#!/usr/bin/env bash
# Render check of the function test of bp-keycloak (templates/tests/function-test.yaml).
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
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

echo "[function-test] Case 1: the kit's shape (gateway.host set) asks for the pinned issuer"
KIT=(--set sovereignFQDN=t1.omani.works --set gateway.host=auth.t1.omani.works)
helm template keycloak . --namespace keycloak "${KIT[@]}" > "$TMP/kit.yaml" 2>/dev/null
python3 "$CHECK" --chart bp-keycloak --release keycloak < "$TMP/kit.yaml"
helm template keycloak . --namespace keycloak "${KIT[@]}" --show-only "$TEST" > "$TMP/kit-test.yaml" 2>/dev/null
grep -q -F 'value: "http://keycloak.keycloak.svc.cluster.local:80/realms/sovereign/.well-known/openid-configuration"' "$TMP/kit-test.yaml" \
  || fail "the test does not fetch the discovery document of the realm sovereign from the Service keycloak"
grep -q -F 'value: "https://auth.t1.omani.works/realms/sovereign"' "$TMP/kit-test.yaml" || fail "the test does not ask for the issuer of this Sovereign"
grep -q -F 'KC_HOSTNAME: "https://auth.t1.omani.works"' "$TMP/kit.yaml" || fail "the frontend URL the issuer comes from is not pinned"
grep -q -E '^  name: keycloak$' "$TMP/kit.yaml" || fail "the subchart renders no Service keycloak"

echo "[function-test] Case 2: another realm name and release name"
helm template idp . --namespace iam "${KIT[@]}" --set sovereignRealm.name=acme > "$TMP/other.yaml" 2>/dev/null
python3 "$CHECK" --chart bp-keycloak --release idp < "$TMP/other.yaml"
grep -q -F 'value: "http://idp-keycloak.iam.svc.cluster.local:80/realms/acme/.well-known/openid-configuration"' "$TMP/other.yaml" \
  || fail "the test does not follow the release name and the realm name"
grep -q -F 'value: "https://auth.t1.omani.works/realms/acme"' "$TMP/other.yaml" || fail "the issuer does not follow the realm name"

echo "[function-test] Case 3: no gateway.host: any issuer of the realm is accepted"
helm template keycloak . --namespace keycloak --show-only "$TEST" > "$TMP/nohost-test.yaml" 2>/dev/null
grep -A1 -F 'name: WANT_ISSUER' "$TMP/nohost-test.yaml" | grep -q -F 'value: ""' || fail "an issuer is demanded while gateway.host is empty"

echo "[function-test] Case 4: sovereignRealm.enabled=false renders no test"
helm template keycloak . --namespace keycloak "${KIT[@]}" --set sovereignRealm.enabled=false > "$TMP/norealm.yaml" 2>/dev/null
python3 "$CHECK" --chart bp-keycloak --release keycloak --absent < "$TMP/norealm.yaml"

echo "function-test-render: all cases passed"
