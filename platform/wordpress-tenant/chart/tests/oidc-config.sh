#!/usr/bin/env bash
# bp-wordpress-tenant OIDC config integration test (issue #915, D1).
#
# Verifies the post-install `oidc-config` Job:
#   1. Renders by default with sane wp-cli command structure.
#   2. Honours the canonical `oidc.*` block in values.yaml.
#   3. Falls back to the legacy `keycloak.*` block when modern keys
#      are at their values.yaml defaults (chart 0.1.x compat).
#   4. Short-circuits when `oidc.enabled=false`.
#   5. References the K8s Secret + key path that bp-keycloak's
#      tenant-realm ConfigMap (PR #918) materialises.
#   6. Uses the canonical `wordpress:cli` image (wp-cli, not direct
#      PHP/SQL writes — requirement of umbrella issue #915).
#
# Usage: bash tests/oidc-config.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

# Per-tenant inputs the orchestrator (#804) supplies. The canonical
# values match what sme_tenant_gitops.go emits for an SME with
# subdomain=acme on parentDomain=omantel.omani.works.
COMMON_SET=(
  --set "smeDomain=acme.omantel.omani.works"
  --set "oidc.issuerURL=https://keycloak.acme.omantel.omani.works/realms/sme-acme"
  --set "oidc.clientId=wordpress"
  --set "oidc.clientSecretName=wordpress-oidc-client-secret"
  --set "adminUser.email=admin@acme.test"
)

echo "[oidc-config] Case 1: canonical oidc.* render produces a working Job"
helm template smoke-wp . "${COMMON_SET[@]}" > "$TMP/canonical.yaml"

# Job present + post-install hook.
if ! grep -q "name: smoke-wp-bp-wordpress-tenant-oidc-config" "$TMP/canonical.yaml"; then
  echo "FAIL: oidc-config Job not rendered" >&2
  exit 1
fi
if ! grep -q '"helm.sh/hook": "post-install,post-upgrade"' "$TMP/canonical.yaml"; then
  echo "FAIL: hook annotation missing" >&2
  exit 1
fi
if ! grep -q '"helm.sh/hook-weight": "10"' "$TMP/canonical.yaml"; then
  echo "FAIL: hook-weight 10 missing" >&2
  exit 1
fi

# wp-cli image (canonical) — NOT the runtime wordpress:6-* image.
if ! grep -qE 'image: "wordpress:cli-2\.12\.0-php8\.3@sha256:[0-9a-f]{64}"' "$TMP/canonical.yaml"; then
  echo "FAIL: oidc-config container must use wordpress:cli-* image (umbrella #915 requirement)" >&2
  exit 1
fi

# wp-cli command flow — verifies the user-visible commands the Job runs.
for needle in \
  'wp core install' \
  'wp plugin install openid-connect-generic --activate' \
  'wp plugin is-installed openid-connect-generic' \
  'wp option update openid_connect_generic_settings' \
  'wp option update default_role' \
  'wp theme activate' \
; do
  if ! grep -qF "${needle}" "$TMP/canonical.yaml"; then
    echo "FAIL: wp-cli command missing: ${needle}" >&2
    exit 1
  fi
done

# Issuer URL + client ID flow into both env and the option JSON.
if ! grep -q 'value: "https://keycloak.acme.omantel.omani.works/realms/sme-acme"' "$TMP/canonical.yaml"; then
  echo "FAIL: KC_ISSUER_URL env not rendered from oidc.issuerURL" >&2
  exit 1
fi

# Client secret references the K8s Secret PR #918 emits.
if ! grep -A5 'name: KC_CLIENT_SECRET' "$TMP/canonical.yaml" | grep -q "name: wordpress-oidc-client-secret"; then
  echo "FAIL: KC_CLIENT_SECRET must come from Secret 'wordpress-oidc-client-secret' (PR #918)" >&2
  echo "--- KC_CLIENT_SECRET context ---" >&2
  grep -A5 'name: KC_CLIENT_SECRET' "$TMP/canonical.yaml" >&2
  exit 1
fi
if ! grep -A5 'name: KC_CLIENT_SECRET' "$TMP/canonical.yaml" | grep -q "key: client-secret"; then
  echo "FAIL: KC_CLIENT_SECRET must read 'client-secret' key from the Secret" >&2
  exit 1
fi

echo "  PASS"

echo "[oidc-config] Case 2: legacy keycloak.* fold path (chart 0.1.x compat)"
helm template smoke-wp . \
  --set "smeDomain=acme.example.local" \
  --set "keycloak.realmURL=https://auth.acme.example.local/realms/sme" \
  --set "keycloak.clientID=wordpress" \
  --set "keycloak.clientSecretName=wordpress-oidc" \
  --set "adminUser.email=admin@acme.example.local" \
  > "$TMP/legacy.yaml"

# Legacy realmURL must propagate when oidc.* is at its values.yaml defaults.
if ! grep -q 'value: "https://auth.acme.example.local/realms/sme"' "$TMP/legacy.yaml"; then
  echo "FAIL: legacy keycloak.realmURL not folded into KC_ISSUER_URL" >&2
  exit 1
fi
# Legacy clientSecretName must propagate.
if ! grep -A5 'name: KC_CLIENT_SECRET' "$TMP/legacy.yaml" | grep -q "name: wordpress-oidc$"; then
  echo "FAIL: legacy keycloak.clientSecretName not folded" >&2
  echo "--- KC_CLIENT_SECRET context (legacy) ---" >&2
  grep -A5 'name: KC_CLIENT_SECRET' "$TMP/legacy.yaml" >&2
  exit 1
fi
echo "  PASS"

echo "[oidc-config] Case 3: oidc.enabled=false short-circuits the Job"
helm template smoke-wp . "${COMMON_SET[@]}" --set "oidc.enabled=false" > "$TMP/disabled.yaml"
if grep -q "name: smoke-wp-bp-wordpress-tenant-oidc-config" "$TMP/disabled.yaml"; then
  echo "FAIL: oidc-config Job rendered despite oidc.enabled=false" >&2
  exit 1
fi
echo "  PASS"

echo "[oidc-config] Case 4: redirect URIs match what bp-keycloak's PR #918 registers"
# The Job writes openid_connect_generic_settings — the plugin builds
# its callback URL as `<siteurl>/wp-admin/admin-ajax.php?action=
# openid-connect-authorize` when alternate_redirect_uri=0 (our default).
# bp-keycloak (PR #918) registers the SAME URL for the wordpress
# client. We don't directly assert the URL string here (the plugin
# composes it at runtime) but we DO assert alternate_redirect_uri=0
# is written so the redirect URL matches what KC will accept.
if ! grep -q '"alternate_redirect_uri":   0' "$TMP/canonical.yaml"; then
  echo "FAIL: alternate_redirect_uri must be 0 so the plugin uses the path" >&2
  echo "      bp-keycloak (PR #918) registered: " >&2
  echo "        /wp-admin/admin-ajax.php?action=openid-connect-authorize" >&2
  exit 1
fi
echo "  PASS"

echo "[oidc-config] Case 5: defaultRole written to wp_options"
if ! grep -A3 'name: OIDC_DEFAULT_ROLE' "$TMP/canonical.yaml" | grep -q 'value: "subscriber"'; then
  echo "FAIL: OIDC_DEFAULT_ROLE env not rendered (default 'subscriber')" >&2
  exit 1
fi
helm template smoke-wp . "${COMMON_SET[@]}" --set "oidc.defaultRole=editor" > "$TMP/role-editor.yaml"
if ! grep -A3 'name: OIDC_DEFAULT_ROLE' "$TMP/role-editor.yaml" | grep -q 'value: "editor"'; then
  echo "FAIL: oidc.defaultRole=editor not propagated" >&2
  exit 1
fi
echo "  PASS"

echo "[oidc-config] Case 6: smoke render emits the Cilium Gateway HTTPRoute (#3785)"
# #3785: a Catalyst Sovereign exposes WordPress via the Cilium Gateway API
# (HTTPRoute → cilium-gateway-console), NOT traefik. The chart's default
# exposure is now an HTTPRoute (gateway.enabled=true) and the legacy traefik
# Ingress defaults OFF — so the canonical render MUST carry an HTTPRoute (with
# gateway.host set, as the org-tenant overlay does) and MUST NOT carry an
# Ingress. The bare CrashLoop root cause of the funnel terminal was a healthy
# Pod with no route → 404; this gate locks the route in.
helm template smoke-wp . "${COMMON_SET[@]}" --set "gateway.host=wordpress.acme.omantel.omani.works" > "$TMP/route.yaml"
if ! python3 -c "
import yaml, sys
docs = list(yaml.safe_load_all(open('$TMP/route.yaml')))
got = [d['kind'] for d in docs if d]
required = ['Deployment', 'Service', 'Job', 'HTTPRoute']
for r in required:
    if r not in got:
        print(f'FAIL: {r} not in render', file=sys.stderr)
        sys.exit(1)
if 'Ingress' in got:
    print('FAIL: traefik Ingress must NOT render on a Sovereign (gateway path is canonical)', file=sys.stderr)
    sys.exit(1)
# The HTTPRoute MUST parent the dedicated console Gateway + carry the host.
route = next(d for d in docs if d and d['kind'] == 'HTTPRoute')
prs = route['spec']['parentRefs'][0]
if prs.get('name') != 'cilium-gateway-console' or prs.get('namespace') != 'kube-system':
    print(f'FAIL: HTTPRoute must parent cilium-gateway-console/kube-system, got {prs}', file=sys.stderr)
    sys.exit(1)
if route['spec']['hostnames'] != ['wordpress.acme.omantel.omani.works']:
    print(f\"FAIL: HTTPRoute host mismatch: {route['spec']['hostnames']}\", file=sys.stderr)
    sys.exit(1)
print(f'render kinds: {got}')
" 2>&1 | tail -5; then
  echo "FAIL: HTTPRoute render invalid" >&2
  exit 1
fi
# Default render (no gateway.host) must fail closed — NO HTTPRoute, NO Ingress.
if grep -qE '^kind: (HTTPRoute|Ingress)$' "$TMP/canonical.yaml"; then
  echo "FAIL: no exposure resource may render when gateway.host is unset (fails closed)" >&2
  exit 1
fi
echo "  PASS"

echo "[oidc-config] Case 7: wp-content PVC mounted in Job (so seeded plugin/db.php drop-in are visible)"
# The Job MUST mount the same wp-content PVC the runtime container
# uses, otherwise wp-cli won't find pg4wp's db.php drop-in and `wp db
# check` will fall through to the default mysqli adapter (which fails
# against bp-cnpg).
#
# Extract just the oidc-config Job document and assert PVC mount.
python3 - "$TMP/canonical.yaml" <<'PYEOF'
import sys, yaml
docs = list(yaml.safe_load_all(open(sys.argv[1])))
job = next(
  (d for d in docs if d and d.get('kind') == 'Job'
   and d['metadata']['name'].endswith('-oidc-config')),
  None,
)
assert job, "oidc-config Job missing"
spec = job['spec']['template']['spec']
volumes = spec.get('volumes') or []
pvc_vols = [v for v in volumes if v.get('persistentVolumeClaim')]
assert pvc_vols, "oidc-config Job has no PVC volumes"
claim = pvc_vols[0]['persistentVolumeClaim']['claimName']
assert claim.endswith('-wp-content'), f"unexpected PVC claim: {claim}"
mounts = spec['containers'][0].get('volumeMounts') or []
mount_names = [m['name'] for m in mounts]
assert pvc_vols[0]['name'] in mount_names, "PVC volume not mounted into container"
mount = next(m for m in mounts if m['name'] == pvc_vols[0]['name'])
assert mount['mountPath'] == '/var/www/html/wp-content', \
  f"unexpected mountPath: {mount['mountPath']}"
print(f"  PVC {claim} -> {mount['mountPath']}")
PYEOF
echo "  PASS"

echo "[oidc-config] All bp-wordpress-tenant OIDC integration gates green."
