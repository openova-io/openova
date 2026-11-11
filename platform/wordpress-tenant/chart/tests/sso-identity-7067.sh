#!/usr/bin/env bash
# bp-wordpress-tenant — SSO from the platform identity contract (ADR-0015 D2,
# Refs #7067).
#
# With `openova.identity` rendered by the application-controller:
#   1. the AppRegistration registers in THAT realm (not the `default`
#      placeholder bp-sso-bridge waited on forever, hw307 walk-wp2);
#   2. the client id is per instance (<namespace>-<app>) — every instance used
#      to register the same `wordpress` client and overwrite the others;
#   3. bp-sso-bridge delivers the secret: the ConfigMap declares the
#      ExternalSecret, the chart renders NO generated secret, and the
#      oidc-config hook reads the delivered one;
#   4. the hook gets the realm's issuer and the realm's broker hint.
# Without it, the legacy rendering is unchanged.
set -euo pipefail
CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then helm dependency build >/dev/null; fi
fail() { echo "FAIL: $*"; exit 1; }

cat > "$TMP/id.yaml" <<'V'
openova:
  topology: {mode: active-hot-standby, self: ra, primary: ra, peers: [rb]}
  application: {name: blog, namespace: acme}
  endpoints: [{name: web, hostname: blog.acme.example.works, port: 443, tls: true, launchDefault: true}]
  identity: {realm: sovereign, issuerURL: "https://auth.example.works/realms/sovereign", idpHint: catalyst-pin}
database: {topology: {promotion: {mechanism: continuum}}}
adminUser: {email: owner@example.com}
V
helm template blog-rtz-a . -n acme -f "$TMP/id.yaml" > "$TMP/id.out"
grep -q '^  realm: "sovereign"' "$TMP/id.out" || fail "AppRegistration realm is not the contract realm"
grep -q '"clientId": "acme-blog"' "$TMP/id.out" || fail "client id is not per instance"
grep -q '"name": "blog-bp-wordpress-tenant-oidc", "namespace": "acme", "secretKey": "client-secret", "property": "client_secret"' "$TMP/id.out" \
  || fail "AppRegistration does not declare the delivered client secret"
grep -q 'catalyst.openova.io/sso-client-secret' "$TMP/id.out" && fail "chart still generates a client secret Keycloak never learns"
grep -A3 'name: KC_CLIENT_SECRET' "$TMP/id.out" | grep -q 'name: blog-bp-wordpress-tenant-oidc' || fail "oidc-config hook does not read the delivered secret"
grep -A1 'name: OIDC_ISSUER_URL\|name: KC_ISSUER\|name: OIDC_ISSUER' "$TMP/id.out" | grep -q 'https://auth.example.works/realms/sovereign' \
  || grep -q 'value: "https://auth.example.works/realms/sovereign"' "$TMP/id.out" || fail "hook issuer is not the contract issuer"
grep -q 'value: "catalyst-pin"' "$TMP/id.out" || fail "hook idp hint is not the realm broker"

# Legacy: no identity contract → unchanged (placeholder realm `default`,
# shared client id, chart-generated secret).
helm template wordpress . -n acme --set adminUser.email=owner@example.com > "$TMP/legacy.out"
grep -q '^  realm: "default"' "$TMP/legacy.out" || fail "legacy realm changed"
grep -q '"clientId": "wordpress"' "$TMP/legacy.out" || fail "legacy client id changed"
grep -q 'externalSecret.json' "$TMP/legacy.out" && fail "legacy render declares an ExternalSecret"
grep -q 'catalyst.openova.io/sso-client-secret' "$TMP/legacy.out" || fail "legacy chart-generated secret missing"
echo "PASS: sso-identity-7067"
