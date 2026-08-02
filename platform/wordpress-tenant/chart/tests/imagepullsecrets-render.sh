#!/usr/bin/env bash
# bp-wordpress-tenant — imagePullSecrets render audit (#4152, Refs #3858).
#
# Since #4153 the WordPress runtime image is the CUSTOM
# `ghcr.io/openova-io/openova/wordpress-tenant-pg` build (official base + the
# pdo_pgsql/pgsql PHP extensions pg4wp REQUIRES to serve against bp-cnpg
# Postgres). That ghcr package is PRIVATE, so the kubelet's anonymous pull
# fails with `failed to authorize: ... 401 Unauthorized` and the Pod stalls in
# ImagePullBackOff (root-caused live on omantel.biz demo Org / kom4dc). The old
# official `wordpress` image was public dockerhub, so the chart shipped no pull
# secret — hence this gap.
#
# Cases:
#   1. Default render — the runtime Deployment carries
#      `imagePullSecrets: [{name: ghcr-pull}]` (covers all three initContainers
#      + the main `wordpress` container, which all use the PRIVATE image).
#   2. admin-user Job (private image) also carries the reference.
#   3. oidc-config Job (PUBLIC `wordpress:cli-*` image) must NOT carry it.
#   4. Operator override `wordpress.image.pullSecrets: []` suppresses the block.
#   5. Operator override with a custom secret name renders that name (per
#      Inviolable Principle #4 — never hardcode).
#
# (#4936 dropped the db-secret-sync Job — WORDPRESS_DB_PASSWORD now binds
# directly to the CNPG `<cluster>-app` Secret — so its former no-pull-secret
# case is gone with it.)

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"

# Build chart deps once so `helm template` does not error on the `common`
# library subchart reference.
"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

# adminUser.email is required for the admin-user Job to render; oidc.enabled
# (default true) renders the oidc-config Job.
base_values=$(mktemp)
trap 'rm -f "$base_values"' EXIT
cat > "$base_values" <<'EOF'
adminUser:
  email: admin@acme.omani.homes
EOF

# Extract the Job doc whose `metadata.name` contains the given suffix from a
# multi-doc render. $1 = full render, $2 = name suffix. Matches the `name:`
# LINE (not end-of-record) so the trailing Pod-spec content does not defeat the
# anchor, and scopes to `kind: Job` so any shared-named non-Job docs are
# excluded.
job_by_name() {
  echo "$1" | awk -v SUF="$2" '
    BEGIN { RS="---\n" }
    /kind: Job/ {
      if ($0 ~ ("\n[[:space:]]*name: [^\n]*" SUF "\n")) print $0
    }'
}

# ── Case 1: default render — runtime Deployment carries ghcr-pull ─────────
echo "[bp-wordpress-tenant] Case 1: default render — Deployment imagePullSecrets present"
out=$("$helm" template wp "$chart_dir" -f "$base_values" 2>&1)

deployment_block=$(echo "$out" | awk '/^---$/{f=0} /^kind: Deployment$/{f=1} f')
if [ -z "$deployment_block" ]; then
  echo "FAIL: Deployment did not render"; exit 1
fi
if ! grep -q "imagePullSecrets:" <<<"$deployment_block"; then
  echo "FAIL: Deployment has no imagePullSecrets block"; exit 1
fi
if ! grep -q "name: ghcr-pull" <<<"$deployment_block"; then
  echo "FAIL: Deployment imagePullSecrets does not reference ghcr-pull"; exit 1
fi
echo "[bp-wordpress-tenant] Case 1: PASS"

# ── Case 2: admin-user Job (private image) carries ghcr-pull ─────────────
echo "[bp-wordpress-tenant] Case 2: admin-user Job — imagePullSecrets present"
admin_job=$(job_by_name "$out" "-admin-user")
if [ -z "$admin_job" ]; then
  echo "FAIL: admin-user Job did not render (adminUser.email set?)"; exit 1
fi
if ! grep -q "imagePullSecrets:" <<<"$admin_job"; then
  echo "FAIL: admin-user Job has no imagePullSecrets block"; exit 1
fi
if ! grep -q "name: ghcr-pull" <<<"$admin_job"; then
  echo "FAIL: admin-user Job imagePullSecrets does not reference ghcr-pull"; exit 1
fi
echo "[bp-wordpress-tenant] Case 2: PASS"

# ── Case 3: oidc-config Job (PUBLIC cli image) has NO imagePullSecrets ────
echo "[bp-wordpress-tenant] Case 3: oidc-config Job — no imagePullSecrets (public cli image)"
oidc_job=$(job_by_name "$out" "-oidc-config")
if [ -z "$oidc_job" ]; then
  echo "FAIL: oidc-config Job did not render"; exit 1
fi
if grep -q "imagePullSecrets:" <<<"$oidc_job"; then
  echo "FAIL: oidc-config Job (public wordpress:cli-* image) must not carry imagePullSecrets"; exit 1
fi
echo "[bp-wordpress-tenant] Case 3: PASS"

# ── Case 4: operator override `pullSecrets: []` suppresses ───────────────
echo "[bp-wordpress-tenant] Case 4: empty override — imagePullSecrets omitted"
empty_values=$(mktemp)
cat > "$empty_values" <<'EOF'
adminUser:
  email: admin@acme.omani.homes
wordpress:
  image:
    pullSecrets: []
EOF
out5=$("$helm" template wp "$chart_dir" -f "$empty_values" 2>&1)
rm -f "$empty_values"
deployment_block5=$(echo "$out5" | awk '/^---$/{f=0} /^kind: Deployment$/{f=1} f')
if [ -z "$deployment_block5" ]; then
  echo "FAIL: Deployment did not render with empty pullSecrets"; exit 1
fi
if grep -q "imagePullSecrets:" <<<"$deployment_block5"; then
  echo "FAIL: empty override should suppress imagePullSecrets block (Inviolable Principle #4)"; exit 1
fi
echo "[bp-wordpress-tenant] Case 4: PASS"

# ── Case 5: custom secret name flows through ─────────────────────────────
echo "[bp-wordpress-tenant] Case 5: custom secret name — operator override"
custom_values=$(mktemp)
cat > "$custom_values" <<'EOF'
adminUser:
  email: admin@acme.omani.homes
wordpress:
  image:
    pullSecrets:
      - name: my-private-registry-creds
EOF
out6=$("$helm" template wp "$chart_dir" -f "$custom_values" 2>&1)
rm -f "$custom_values"
deployment_block6=$(echo "$out6" | awk '/^---$/{f=0} /^kind: Deployment$/{f=1} f')
if ! grep -q "name: my-private-registry-creds" <<<"$deployment_block6"; then
  echo "FAIL: custom imagePullSecret name not propagated to Deployment"; exit 1
fi
if grep -q "name: ghcr-pull" <<<"$deployment_block6"; then
  echo "FAIL: custom override leaked default ghcr-pull reference"; exit 1
fi
echo "[bp-wordpress-tenant] Case 5: PASS"

echo "[bp-wordpress-tenant] All imagePullSecrets render cases PASS"
