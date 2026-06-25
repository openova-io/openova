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
# Refs #4322: the OIDC plugin is now VENDORED from a pinned GitHub release
# and ACTIVATED (never `wp plugin install`, which fetches from wordpress.org
# over egress and hangs the hook). So the needle is `wp plugin activate`,
# not `wp plugin install` — Case 8 below locks the no-wordpress.org contract.
for needle in \
  'wp core install' \
  'wp plugin activate' \
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

echo "[oidc-config] Case 7: wp-content is an emptyDir (NOT the runtime RWO PVC) + the Job self-seeds pg4wp"
# #4220 (Refs #4155): the Job MUST NOT mount the runtime Deployment's
# ReadWriteOnce wp-content PVC. The WordPress Pod holds that RWO PVC for
# its whole lifetime, and a Sovereign's only StorageClass is RWO (no RWX),
# so a concurrent Job mounting the same PVC deadlocks on the kubelet
# Multi-Attach error → the post-upgrade hook never runs → HR Ready=False.
# Instead the Job mounts its OWN emptyDir and self-seeds the pg4wp db.php
# drop-in so wp-cli still reaches the bp-cnpg Postgres backend.
#
# Extract just the oidc-config Job document and assert the emptyDir mount.
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
# MUST NOT carry any PVC volume (that would Multi-Attach-deadlock the RWO PVC).
pvc_vols = [v for v in volumes if v.get('persistentVolumeClaim')]
assert not pvc_vols, f"oidc-config Job must not mount a PVC (Multi-Attach deadlock); found: {pvc_vols}"
# MUST mount an emptyDir named wp-content at the WordPress wp-content path.
wp_vols = [v for v in volumes if v.get('name') == 'wp-content']
assert wp_vols, "oidc-config Job has no wp-content volume"
assert 'emptyDir' in wp_vols[0], f"wp-content volume must be emptyDir, got: {wp_vols[0]}"
mounts = spec['containers'][0].get('volumeMounts') or []
mount = next((m for m in mounts if m['name'] == 'wp-content'), None)
assert mount, "wp-content volume not mounted into container"
assert mount['mountPath'] == '/var/www/html/wp-content', \
  f"unexpected mountPath: {mount['mountPath']}"
# The Job's command MUST self-seed the pg4wp db.php drop-in (so wp-cli
# speaks Postgres without depending on the runtime PVC's seeded copy).
cmd = "\n".join(spec['containers'][0].get('command') or []) \
    + "\n".join(spec['containers'][0].get('args') or [])
assert 'pg4wp' in cmd and 'db.php' in cmd, \
  "oidc-config Job command must self-seed the pg4wp db.php drop-in"
# 0.4.17 (Refs #4220): the pg4wp GitHub archive tag is `v3.3.1` — the bare
# `tags/3.3.1.zip` (no `v`) 404s, which left a fresh Org's HR Ready=False
# (the hook errored on its first run). Assert the `v`-prefixed URL and reject
# the un-prefixed form so the regression cannot recur.
assert 'archive/refs/tags/v3.3.1.zip' in cmd, \
  "oidc-config Job must fetch pg4wp from the v-prefixed tag (v3.3.1.zip), not a 404 path"
assert 'archive/refs/tags/3.3.1.zip' not in cmd, \
  "oidc-config Job must NOT use the un-prefixed pg4wp tag (3.3.1.zip 404s)"
print(f"  emptyDir wp-content -> {mount['mountPath']}; pg4wp db.php self-seeded (v3.3.1)")
PYEOF
echo "  PASS"

echo "[oidc-config] Case 8: oidc plugin is VENDORED from a pinned GitHub release — the hook never depends on a wordpress.org fetch (#4322)"
# #4322: this Job mounts an EPHEMERAL emptyDir (Case 7), so the plugin the
# runtime Deployment seeds onto the persistent PVC is NOT visible here. The
# previous `wp plugin install openid-connect-generic --activate` therefore
# ALWAYS fetched the plugin zip from wordpress.org over egress and HUNG the
# hook until its deadline on a slow-egress / air-gapped Sovereign → the
# post-upgrade hook timed out → HelmRelease Ready=False. The fix VENDORS the
# plugin from a pinned GitHub release archive (same mechanism as the pg4wp
# db.php seed) and TOLERATES a fetch failure (logs + continues) so the release
# reaches Ready WITHOUT any external plugin download.
#
# Assert (on the rendered oidc-config Job command):
#   - NO `wp plugin install` (the wordpress.org egress fetch that hangs).
#   - NO `wordpress.org` / `downloads.wordpress.org` reference at all.
#   - The plugin is vendored from the pinned GitHub release archive whose
#     repo + version come from oidc.plugin.{repo,version}.
#   - The step is fetch-bounded (`--max-time`) and tolerant (a fetch failure
#     does not abort the script — it continues so the HR reaches Ready).
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
container = spec['containers'][0]
cmd = "\n".join(container.get('command') or []) \
    + "\n".join(container.get('args') or [])

# (1) The hook MUST NOT INVOKE `wp plugin install` (the wordpress.org egress
#     fetch that hangs the hook). We check command LINES, not prose — the
#     surrounding comments legitimately explain why we avoid that command, so a
#     naive substring match would false-positive on `# ... wp plugin install`.
invocation_lines = [
    ln.lstrip() for ln in cmd.splitlines()
    if ln.lstrip() and not ln.lstrip().startswith('#')
]
assert not any(ln.startswith('wp plugin install') for ln in invocation_lines), \
  "oidc-config hook must NOT run `wp plugin install` (it fetches from " \
  "wordpress.org over egress and hangs the hook → HR Ready=False)"

# (2) No command line may fetch from wordpress.org (air-gapped Sovereigns cannot
#     reach it — a runtime fetch from there is structurally wrong).
assert not any('wordpress.org' in ln for ln in invocation_lines), \
  "oidc-config hook must not depend on a wordpress.org plugin fetch"

# (3) The plugin MUST be vendored from a PINNED GitHub release archive, with
#     repo + version threaded from oidc.plugin.{repo,version} via env.
env = {e['name']: e.get('value') for e in (container.get('env') or [])}
assert env.get('OIDC_PLUGIN_REPO'), "OIDC_PLUGIN_REPO env not rendered from oidc.plugin.repo"
assert env.get('OIDC_PLUGIN_VERSION'), "OIDC_PLUGIN_VERSION env not rendered from oidc.plugin.version"
assert 'github.com/${OIDC_PLUGIN_REPO}/archive/refs/tags/${OIDC_PLUGIN_VERSION}.zip' in cmd, \
  "oidc-config hook must vendor the OIDC plugin from the pinned GitHub release archive"
assert 'wp plugin activate' in cmd, \
  "oidc-config hook must `wp plugin activate` the vendored plugin"

# (4) The fetch MUST be time-bounded AND the step MUST be tolerant (continue on
#     failure) so a black-holed egress route can never stall/fail the release.
assert '--max-time' in cmd, \
  "the OIDC plugin fetch must be --max-time bounded so a black-holed egress " \
  "route cannot stall the hook"
assert 'continuing without failing the release' in cmd or 'continuing so the release' in cmd, \
  "the OIDC plugin install must LOG + CONTINUE on failure (never fail the " \
  "release) so the HR reaches Ready without an external plugin download"
print(f"  OIDC plugin vendored from GitHub ({env['OIDC_PLUGIN_REPO']} "
      f"{env['OIDC_PLUGIN_VERSION']}); no wordpress.org fetch; tolerant + bounded")
PYEOF
echo "  PASS"

echo "[oidc-config] All bp-wordpress-tenant OIDC integration gates green."
