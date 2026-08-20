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
# The Job MUST still end up with the pg4wp db.php drop-in in its OWN
# wp-content (so wp-cli speaks Postgres without depending on the runtime
# PVC's copy) — the requirement #4220 established is unchanged.
cmd = "\n".join(spec['containers'][0].get('command') or []) \
    + "\n".join(spec['containers'][0].get('args') or [])
assert 'pg4wp' in cmd or 'db.php' in cmd, \
  "oidc-config Job must still depend on the pg4wp db.php drop-in"
# #6311 supersedes the 0.4.17/#4220 URL assertion. That assertion pinned the
# `v`-prefixed GitHub archive tag because the Job FETCHED pg4wp at install
# time; it no longer does — cutover step-08 holds a deny-egress NetworkPolicy
# against github.com, so an install-time fetch is a Pillar-5 violation and the
# Job was CrashLoopBackOff'ing on `curl: (28) Connection timed out`. The bytes
# now arrive from the runtime image's baked /usr/src/openova-wp-artifacts via
# the `wp-artifacts` initContainer; the version pin moved to the Dockerfile's
# PG4WP_VERSION ARG, where tests/6311-no-install-time-egress.sh asserts it.
seed = next((c for c in (spec.get('initContainers') or [])
             if c['name'] == 'wp-artifacts'), None)
assert seed, "oidc-config Job must carry the wp-artifacts seeding initContainer (#6311)"
seed_cmd = "\n".join(seed.get('command') or []) + "\n".join(seed.get('args') or [])
assert '/usr/src/openova-wp-artifacts' in seed_cmd, \
  "wp-artifacts initContainer must seed from the image's baked artifact path"
assert 'db.php' in seed_cmd, "wp-artifacts initContainer must seed the pg4wp db.php drop-in"
assert 'archive/refs/tags' not in cmd and 'archive/refs/tags' not in seed_cmd, \
  "the Job must NOT fetch a release archive at install time (#6311)"
print(f"  emptyDir wp-content -> {mount['mountPath']}; pg4wp db.php seeded from the image")
PYEOF
echo "  PASS"

echo "[oidc-config] Case 8: oidc plugin comes from the IMAGE — the hook fetches nothing at install time (#6311, supersedes #4322)"
# #4322 removed the `wp plugin install` wordpress.org fetch by VENDORING the
# plugin from a pinned GitHub release archive at install time. #6311 removes
# that too: cutover step-08 (`egress-block-test`) holds a 10-minute deny-egress
# NetworkPolicy against github.com / ghcr.io / harbor.openova.io and requires
# the cluster to reconcile green through it, so an install-time GitHub fetch is
# a Pillar-5 violation — and it was CrashLoopBackOff'ing live on
# `curl: (28) Connection timed out after 120001 milliseconds`.
#
# The plugin + pg4wp bytes now travel INSIDE the runtime image
# (/usr/src/openova-wp-artifacts, baked by platform/wordpress-tenant/image/
# Dockerfile) and are copied into this Job's ephemeral wp-content by the
# `wp-artifacts` initContainer. That reuses the ONLY artifact-mirroring seam
# this repo has — cutover step-03 `harbor-prewarm` mirrors OCI images and Helm
# charts, and `wordpress-tenant-pg` is already in that set.
#
# Assert (on the rendered oidc-config Job):
#   - NO `wp plugin install`, NO `wordpress.org`, NO `github.com` on ANY
#     executable line of ANY container in the Job.
#   - A `wp-artifacts` initContainer runs the RUNTIME image and copies from
#     /usr/src/openova-wp-artifacts into the shared wp-content volume.
#   - The main container still ACTIVATES the plugin, and still tolerates a
#     missing plugin (logs + continues) so the HR reaches Ready.
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

# Executable lines across EVERY container in the Job (init + main). Comments are
# excluded on purpose: the surrounding prose legitimately names the commands and
# hosts we are forbidding, so a naive substring match would false-positive on
# its own explanation. Scanning only the main container would also have missed
# a fetch reintroduced in an initContainer — the seam this case now guards.
def exec_lines(c):
    text = "\n".join(c.get('command') or []) + "\n" + "\n".join(c.get('args') or [])
    return [ln.strip() for ln in text.splitlines()
            if ln.strip() and not ln.strip().startswith('#')]

all_containers = (spec.get('initContainers') or []) + spec['containers']
invocation_lines = [ln for c in all_containers for ln in exec_lines(c)]
assert invocation_lines, "no executable lines found — the scan would pass vacuously"

# (1) No `wp plugin install` (the wordpress.org egress fetch that hangs).
assert not any(ln.startswith('wp plugin install') for ln in invocation_lines), \
  "oidc-config hook must NOT run `wp plugin install` (it fetches from " \
  "wordpress.org over egress and hangs the hook → HR Ready=False)"

# (2) No install-time fetch from ANY external artifact host — this is the
#     Pillar-5 contract cutover step-08 enforces at runtime (#6311).
for host in ('wordpress.org', 'github.com'):
    offenders = [ln for ln in invocation_lines if host in ln]
    assert not offenders, (
        f"oidc-config Job must not fetch from {host} at install time "
        f"(cutover step-08 deny-egress hold, Pillar 5) — offending line(s): "
        f"{offenders[:3]}")

# (3) The artifacts MUST arrive via a `wp-artifacts` initContainer running the
#     RUNTIME image (the wp-cli image is upstream and carries nothing baked),
#     copying from the baked path into the shared wp-content volume.
inits = {c['name']: c for c in (spec.get('initContainers') or [])}
art = inits.get('wp-artifacts')
assert art, "oidc-config Job must carry a `wp-artifacts` initContainer (#6311)"
assert 'wordpress-tenant-pg' in art['image'], \
  ("the wp-artifacts initContainer must run the RUNTIME image that carries the "
   f"baked artifacts, got {art['image']}")
art_cmd = "\n".join(art.get('command') or []) + "\n".join(art.get('args') or [])
assert '/usr/src/openova-wp-artifacts' in art_cmd, \
  "wp-artifacts initContainer must copy from the image's baked artifact path"
assert any(m['name'] == 'wp-content' for m in (art.get('volumeMounts') or [])), \
  "wp-artifacts initContainer must mount the shared wp-content volume"

# (4) The main container still activates, and still tolerates absence so a
#     partially-seeded Pod cannot fail the release.
assert 'wp plugin activate' in cmd, \
  "oidc-config hook must `wp plugin activate` the image-seeded plugin"
assert 'continuing without failing' in cmd or 'continuing so the release' in cmd, \
  "the OIDC plugin activate must LOG + CONTINUE on failure (never fail the " \
  "release) so the HR reaches Ready"

# (5) Provenance env stays threaded from oidc.plugin.{repo,version} — the
#     wp-artifacts initContainer compares it against the image's baked VERSIONS
#     file and warns on drift.
env = {e['name']: e.get('value') for e in (art.get('env') or [])}
assert env.get('OIDC_PLUGIN_REPO'), "OIDC_PLUGIN_REPO env not rendered from oidc.plugin.repo"
assert env.get('OIDC_PLUGIN_VERSION'), "OIDC_PLUGIN_VERSION env not rendered from oidc.plugin.version"
print(f"  OIDC plugin seeded from the image ({env['OIDC_PLUGIN_REPO']} "
      f"{env['OIDC_PLUGIN_VERSION']} declared); zero install-time egress")
PYEOF
echo "  PASS"

echo "[oidc-config] All bp-wordpress-tenant OIDC integration gates green."
