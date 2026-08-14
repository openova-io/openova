#!/usr/bin/env bash
# bp-wordpress-tenant — zero install-time egress to third-party artifact hosts
# (#6311, Refs #3988 #3379).
#
# WHAT THIS LOCKS
# ───────────────
# Cutover step-08 (`egress-block-test`) holds a 10-minute deny-egress
# NetworkPolicy against `github.com`, `ghcr.io` and `harbor.openova.io` and
# requires the cluster to reconcile GREEN through it — that hold IS the
# sovereignty proof (ADR-0002, Pillar 5). A customer Application whose install
# path reaches GitHub therefore cannot exist on a sovereign Sovereign.
#
# Before this fix bp-wordpress-tenant fetched at install time from THREE sites:
#   - deployment.yaml `wp-plugin-install` init  → downloads.wordpress.org (OIDC
#     plugin) and github.com (pg4wp)
#   - oidc-config-job.yaml step 0               → github.com (pg4wp)
#   - oidc-config-job.yaml step 4               → github.com (OIDC plugin)
# and a fourth latent one — step 7's `wp theme install` fallback, reachable
# because the Job's emptyDir masks the image's bundled themes.
# The live symptom was the oidc-config Job CrashLoopBackOff on
#   curl: (28) Connection timed out after 120001 milliseconds
#
# THE SEAM REUSED (not invented): this repo mirrors exactly two artifact
# classes into a post-cutover Sovereign's Harbor — OCI **container images** and
# **Helm charts** (cutover step-03 `harbor-prewarm`). There is no mirroring
# path for plain file artifacts. So the bytes now travel as layers of an image
# that is ALREADY in the prewarm set: `wordpress-tenant-pg`, baked by
# platform/wordpress-tenant/image/Dockerfile into
# /usr/src/openova-wp-artifacts.
#
# Every assertion is VACUITY-PROVED below: each one is re-run against a render
# with exactly one behaviour mutated back to the pre-fix shape and must FAIL.
#
# Usage: bash tests/6311-no-install-time-egress.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

# ──────────────────────────────────────────────────────────────────────────
# Assertion body — shared by the real render and every mutation.
# ──────────────────────────────────────────────────────────────────────────
assert_no_egress() {  # $1 = rendered manifest path
  python3 - "$1" <<'PYEOF'
import sys, yaml

docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]

def pod_specs(d):
    k = d.get('kind')
    if k in ('Deployment', 'StatefulSet', 'Job'):
        return [d['spec']['template']['spec']]
    if k == 'CronJob':
        return [d['spec']['jobTemplate']['spec']['template']['spec']]
    return []

specs = [(d, s) for d in docs for s in pod_specs(d)]
assert specs, "no workloads rendered — this scan would pass vacuously"

def containers(spec):
    return (spec.get('initContainers') or []) + (spec.get('containers') or [])

def exec_lines(c):
    """Executable shell lines only. Comments are stripped deliberately: the
    prose in these templates names the very hosts and commands being
    forbidden (that is how the change documents itself), so a raw substring
    scan would false-positive on its own rationale."""
    body = "\n".join(c.get('command') or []) + "\n" + "\n".join(c.get('args') or [])
    out = []
    for ln in body.splitlines():
        s = ln.strip()
        if not s or s.startswith('#'):
            continue
        out.append(s)
    return out

scanned = 0
offenders = []
FORBIDDEN_HOSTS = ('github.com', 'wordpress.org', 'raw.githubusercontent.com',
                   'ghcr.io', 'deb.debian.org')
for doc, spec in specs:
    for c in containers(spec):
        for ln in exec_lines(c):
            scanned += 1
            for host in FORBIDDEN_HOSTS:
                # `wordpress.org.local` is the DERIVED ingress host of the
                # default test render (orgDomain=org.local) — a hostname the
                # chart serves, not one it fetches from. Only flag a forbidden
                # host when the line is actually a fetch.
                if host in ln and any(
                        tok in ln for tok in ('curl', 'wget', 'http://', 'https://')):
                    if host == 'wordpress.org' and 'wordpress.org.local' in ln \
                            and 'wordpress.org/' not in ln:
                        continue
                    offenders.append((doc['kind'], doc['metadata']['name'],
                                      c['name'], host, ln[:120]))

assert scanned > 50, (
    f"B0 only {scanned} executable lines scanned — the scan is too shallow to "
    "be believed; the chart's install scripts are hundreds of lines")

# B1 — no install-time fetch from any third-party artifact host, in ANY
#      container of ANY workload. Scanning only the main container is how a
#      reintroduced fetch would hide in an initContainer.
assert not offenders, (
    "B1 install-time egress to a third-party artifact host — cutover step-08's "
    "deny-egress hold would fail here (#6311):\n" +
    "\n".join(f"  {k}/{n} [{c}] -> {h}: {ln}" for k, n, c, h, ln in offenders))

# B2 — no `wp plugin install` / `wp theme install` (wp-cli's own network
#      installers; they fetch from wordpress.org without naming the host, so
#      the host scan above cannot see them).
wp_net = [(d['metadata']['name'], c['name'], ln)
          for d, s in specs for c in containers(s) for ln in exec_lines(c)
          if ln.startswith('wp plugin install') or ln.startswith('wp theme install')]
assert not wp_net, (
    "B2 wp-cli network installer invoked at install time (fetches from "
    f"wordpress.org): {wp_net}")

# B3 — the artifacts must be SOURCED from the image's baked path, in both
#      consumers. An absent fetch is not enough: something has to supply the
#      bytes, or WordPress simply has no Postgres driver.
BAKED = '/usr/src/openova-wp-artifacts'
consumers = {}
for d, s in specs:
    for c in containers(s):
        if any(BAKED in ln for ln in exec_lines(c)):
            consumers.setdefault(d['kind'], set()).add(c['name'])
assert 'wp-plugin-install' in consumers.get('Deployment', set()), (
    "B3 the runtime Deployment's wp-plugin-install initContainer must seed "
    f"the PVC from {BAKED}")
assert 'wp-artifacts' in consumers.get('Job', set()), (
    "B3 the oidc-config Job must carry a wp-artifacts initContainer seeding "
    f"its ephemeral wp-content from {BAKED}")

# B4 — the wp-artifacts initContainer must run the RUNTIME image. The wp-cli
#      image is upstream and carries nothing baked, so sourcing from it would
#      be a copy from an empty path that only fails at runtime.
job = next(d for d, _ in specs if d['kind'] == 'Job'
           and d['metadata']['name'].endswith('-oidc-config'))
art = next(c for c in (job['spec']['template']['spec'].get('initContainers') or [])
           if c['name'] == 'wp-artifacts')
assert 'wordpress-tenant-pg' in art['image'], (
    f"B4 wp-artifacts must run the runtime image, got {art['image']}")
assert any(m['name'] == 'wp-content' for m in (art.get('volumeMounts') or [])), \
    "B4 wp-artifacts must mount the shared wp-content volume or it seeds nothing"

# B5 — FAIL CLOSED, never fall back to the network. Both consumers must abort
#      when the baked path is absent (a hollow image pin), because a silent
#      fallback is exactly the sovereignty violation being removed.
for kind, cname in (('Deployment', 'wp-plugin-install'), ('Job', 'wp-artifacts')):
    c = next(c for d, s in specs if d['kind'] == kind
             for c in containers(s) if c['name'] == cname)
    lines = exec_lines(c)
    joined = "\n".join(lines)
    assert f'if [ ! -d "$' in joined and 'exit 1' in joined, (
        f"B5 {kind}/{cname} must fail closed when {BAKED} is absent, not fall "
        "back to a network fetch")

print(f"  scanned {scanned} executable lines across "
      f"{sum(len(containers(s)) for _, s in specs)} containers; "
      "zero third-party fetches; artifacts sourced from the image")
PYEOF
}

echo "[6311-egress] Case 1: default render performs ZERO install-time third-party fetches"
helm template smoke-wp . --api-versions "postgresql.cnpg.io/v1" > "$TMP/canonical.yaml"
assert_no_egress "$TMP/canonical.yaml"
echo "  PASS"

echo "[6311-egress] Case 2: the same holds with an air-gapped-style overlay (harbor registry prefix)"
helm template smoke-wp . --api-versions "postgresql.cnpg.io/v1" \
  --set global.imageRegistry=harbor.t99.omani.works/proxy-ghcr \
  --set orgDomain=acme.omani.homes > "$TMP/overlay.yaml"
assert_no_egress "$TMP/overlay.yaml"
echo "  PASS"

echo "[6311-egress] Case 3: the Dockerfile actually bakes what the chart copies"
assert_dockerfile() {  # $1 = Dockerfile path
  python3 - "$1" <<'PYEOF'
import sys, re
df = open(sys.argv[1]).read()
# C1 — the baked path the chart copies from must be produced here.
assert '/usr/src/openova-wp-artifacts' in df, \
  "C1 Dockerfile does not create /usr/src/openova-wp-artifacts"
# C2 — both artifacts, pinned by an explicit version ARG (no floating ref,
#      docs/PRINCIPLES.md #4).
for arg in ('PG4WP_VERSION', 'OIDC_PLUGIN_VERSION', 'OIDC_PLUGIN_REPO'):
    m = re.search(rf'^ARG {arg}=(\S+)', df, re.M)
    assert m, f"C2 Dockerfile must pin {arg} as an ARG"
    assert 'main' not in m.group(1) and 'latest' not in m.group(1), \
      f"C2 {arg} must be a pinned release, got {m.group(1)}"
# C3 — a build-time assertion on the exact three paths the chart copies, so an
#      image that silently ships without them can never be published (the chart
#      has no network fallback any more).
for path in ('/usr/src/openova-wp-artifacts/db.php',
             '/usr/src/openova-wp-artifacts/pg4wp/driver_pgsql.php',
             '/usr/src/openova-wp-artifacts/plugins/openid-connect-generic/openid-connect-generic.php'):
    assert f'test -s {path}' in df, \
      f"C3 Dockerfile must assert {path} exists at build time"
# C4 — the plugin must be baked under the BARE slug, which is what the Job's
#      `wp plugin activate` writes into WordPress's active_plugins option row.
assert re.search(r'/plugins/openid-connect-generic(?![-\w])', df), \
  "C4 the plugin must be baked under the bare `openid-connect-generic` slug"
print("  Dockerfile bakes both artifacts, version-pinned, with build-time assertions")
PYEOF
}
assert_dockerfile "$CHART_DIR/../image/Dockerfile"
echo "  PASS"

echo "[6311-egress] Case 3b: vacuity proof — each Dockerfile assertion goes RED on its own mutation"
df_mutate_must_fail() {  # $1 = label  $2 = python: mutates `df`, then write()
  local label="$1" mutation="$2"
  python3 - "$CHART_DIR/../image/Dockerfile" "$TMP/Dockerfile.mutated" <<PYEOF
import sys
df = open(sys.argv[1]).read()
def write():
    open(sys.argv[2], 'w').write(df)
${mutation}
PYEOF
  if assert_dockerfile "$TMP/Dockerfile.mutated" >/dev/null 2>&1; then
    echo "FAIL: Dockerfile assertions PASSED against the mutated file — ${label}" >&2
    exit 1
  fi
  echo "  RED as required: ${label}"
}
# C1 — the baked directory is not produced at all (the pre-fix Dockerfile).
df_mutate_must_fail "C1 baked artifact path absent (pre-fix Dockerfile)" \
  "df = df.replace('/usr/src/openova-wp-artifacts', '/usr/src/somewhere-else'); write()"
# C2 — a floating ref instead of a pinned release.
df_mutate_must_fail "C2 PG4WP_VERSION floated to main" \
  "df = df.replace('ARG PG4WP_VERSION=3.3.1', 'ARG PG4WP_VERSION=main'); write()"
df_mutate_must_fail "C2 OIDC_PLUGIN_VERSION floated to latest" \
  "import re; df = re.sub(r'ARG OIDC_PLUGIN_VERSION=\S+', 'ARG OIDC_PLUGIN_VERSION=latest', df); write()"
# C3 — the build-time assertion is dropped, so an image missing the artifacts
#      could publish and every per-Org install would fail on a hollow pin.
df_mutate_must_fail "C3 build-time assertion on db.php removed" \
  "df = df.replace('test -s /usr/src/openova-wp-artifacts/db.php;', 'true;'); write()"
df_mutate_must_fail "C3 build-time assertion on the plugin removed" \
  "df = df.replace('test -s /usr/src/openova-wp-artifacts/plugins/openid-connect-generic/openid-connect-generic.php', 'true'); write()"
# C4 — baked under the old wordpress.org slug, which does not match the slug
#      the Job writes into WordPress's active_plugins option row.
df_mutate_must_fail "C4 plugin baked under the wordpress.org slug" \
  "df = df.replace('plugins/openid-connect-generic', 'plugins/daggerhart-openid-connect-generic'); write()"
echo "  PASS"

echo "[6311-egress] Case 4: vacuity proof — every assertion goes RED on its own mutation"
mutate_must_fail() {  # $1 = label  $2 = python mutation; must end in dump()
  local label="$1" mutation="$2"
  python3 - "$TMP/canonical.yaml" "$TMP/mutated.yaml" <<PYEOF
import sys, yaml
src, dst = sys.argv[1], sys.argv[2]
docs = [d for d in yaml.safe_load_all(open(src)) if d]

def job():
    return next(d for d in docs if d.get('kind') == 'Job'
                and d['metadata']['name'].endswith('-oidc-config'))

def deploy():
    return next(d for d in docs if d.get('kind') == 'Deployment')

def container(doc, name):
    spec = doc['spec']['template']['spec']
    return next(c for c in (spec.get('initContainers') or []) + spec['containers']
                if c['name'] == name)

def dump():
    open(dst, 'w').write("\n---\n".join(yaml.safe_dump(d) for d in docs))

${mutation}
PYEOF
  if assert_no_egress "$TMP/mutated.yaml" >/dev/null 2>&1; then
    echo "FAIL: assertions PASSED against the mutated render — ${label}" >&2
    echo "      That assertion is vacuous: it cannot detect its own defect." >&2
    exit 1
  fi
  echo "  RED as required: ${label}"
}

# B1 — reintroduce the exact pre-fix pg4wp fetch, in the Job (step 0's shape).
mutate_must_fail "B1 github.com pg4wp fetch reintroduced in the Job" \
  "c = container(job(), 'oidc-config'); c['command'][-1] += '\ncurl -fsSL --max-time 120 -o /tmp/pg4wp.zip https://github.com/PostgreSQL-For-Wordpress/postgresql-for-wordpress/archive/refs/tags/v3.3.1.zip\n'; dump()"
# B1 — reintroduce it in an INIT container, which a main-container-only scan
#      would miss entirely.
mutate_must_fail "B1 wordpress.org fetch reintroduced in an initContainer" \
  "c = container(deploy(), 'wp-plugin-install'); c['command'][-1] += '\ncurl -fsSL -o oidc.zip https://downloads.wordpress.org/plugin/daggerhart-openid-connect-generic.latest-stable.zip\n'; dump()"
# B2 — wp-cli's own network installer (no hostname on the line at all).
mutate_must_fail "B2 wp plugin install reintroduced" \
  "c = container(job(), 'oidc-config'); c['command'][-1] += '\nwp plugin install openid-connect-generic --activate --allow-root\n'; dump()"
mutate_must_fail "B2 wp theme install reintroduced" \
  "c = container(job(), 'oidc-config'); c['command'][-1] += '\nwp theme install twentytwentyfive --activate --allow-root\n'; dump()"
# B3 — drop the Job's artifact initContainer (nothing supplies the bytes).
mutate_must_fail "B3 wp-artifacts initContainer removed from the Job" \
  "s = job()['spec']['template']['spec']; s['initContainers'] = [c for c in s['initContainers'] if c['name'] != 'wp-artifacts']; dump()"
# B3 — drop the Deployment's baked-path seed.
mutate_must_fail "B3 Deployment no longer seeds from the baked path" \
  "c = container(deploy(), 'wp-plugin-install'); c['command'][-1] = c['command'][-1].replace('/usr/src/openova-wp-artifacts', '/tmp/nowhere'); dump()"
# B4 — point wp-artifacts at the upstream wp-cli image, which carries nothing.
mutate_must_fail "B4 wp-artifacts runs the upstream wp-cli image" \
  "container(job(), 'wp-artifacts')['image'] = 'wordpress:cli-2.12.0-php8.3'; dump()"
# B4 — mount nothing, so the copy lands in the container's own filesystem.
mutate_must_fail "B4 wp-artifacts does not mount wp-content" \
  "container(job(), 'wp-artifacts')['volumeMounts'] = []; dump()"
# B5 — replace the fail-closed guard with a silent network fallback.
mutate_must_fail "B5 fail-closed guard replaced by a fallback in the Job" \
  "c = container(job(), 'wp-artifacts'); c['command'][-1] = c['command'][-1].replace('exit 1', 'curl -fsSL -o /tmp/x.zip https://github.com/x/y/archive/refs/tags/v1.zip'); dump()"
mutate_must_fail "B5 fail-closed guard removed from the Deployment init" \
  "c = container(deploy(), 'wp-plugin-install'); c['command'][-1] = c['command'][-1].replace('exit 1', 'true'); dump()"
echo "  PASS"

echo "[6311-egress] All #6311 no-install-time-egress gates green."
