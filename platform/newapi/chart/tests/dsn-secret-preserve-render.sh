#!/usr/bin/env bash
# bp-newapi — DSN-placeholder Secret self-preservation render audit
# (#4278-followup, 2026-06-25).
#
# Guards the DSN-CLOBBER regression that wedged a fresh Org's data plane
# in `Init:CrashLoopBackOff`:
#
#   The `bp-newapi-newapi-db-dsn` Secret renders `SQL_DSN: ""` only as a
#   first-install placeholder; the database-secret-sync-job (a POST-install
#   Helm HOOK) PATCHes the real DSN AFTER the render. Pre-fix the Secret was
#   a plain chart-managed resource with NO `helm.sh/resource-policy: keep`,
#   so helm-controller re-applied the empty placeholder on EVERY render —
#   resetting SQL_DSN to "" whenever a post-upgrade hook chain failed (e.g.
#   the admin-promote `context deadline exceeded`, or a fresh install
#   failing at the cluster-singleton CNPG mutating-webhook). The
#   wait-for-sql-dsn initContainer then polled the empty DSN for its full
#   budget, exit 1, CrashLoop — even though CNPG was healthy.
#
#   The fix makes the Secret SELF-PRESERVING: it carries
#   `helm.sh/resource-policy: keep` and `lookup`s its own existing value so
#   a populated SQL_DSN is never clobbered on a later render. On a true
#   first install (no existing Secret / dry-run) the lookup is nil → "".
#
# Cases:
#   1. role=all + cnpg.enabled=true: the DSN Secret renders WITH
#      `helm.sh/resource-policy: keep` and a `SQL_DSN` key.
#   2. First-install/dry-run render emits the EMPTY placeholder (lookup nil)
#      so kubelet does not trip CreateContainerConfigError on first schedule.
#   3. The wait-for-sql-dsn init container's `sql-dsn-gate` volume and the
#      Deployment's SQL_DSN secretKeyRef both reference the SAME Secret name
#      the placeholder renders — the gate guards exactly the preserved key.
#   4. role=vcluster-app + cnpg.enabled=false: cnpg-cluster.yaml renders
#      NOTHING (the Secret + Cluster are host-side; the vCluster reads the
#      mirrored DSN), so the preservation logic never double-renders.

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"
api_flag=(--api-versions postgresql.cnpg.io/v1)
dsn_secret_name="bp-newapi-newapi-db-dsn"

# Build chart deps once so `helm template` does not error on the
# `common` library subchart reference.
"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

# Values that flip the Pod-render gate to TRUE (database Secret resolvable
# via cnpg.enabled + masterKey mode to skip the keycloak issuer requirement).
common_values=$(mktemp)
trap 'rm -f "$common_values"' EXIT
cat > "$common_values" <<'EOF'
placement:
  role: all
cnpg:
  enabled: true
auth:
  adminUI:
    mode: masterKey
EOF

# Release name `bp-newapi` makes bp-newapi.fullname resolve to `bp-newapi`
# (the helper drops the release prefix when it already contains the chart
# name) — identical to the live per-Org HR `metadata.name: bp-newapi`, so
# the DSN Secret is exactly `bp-newapi-newapi-db-dsn`.
release="bp-newapi"

# ── Case 1 + 2: DSN Secret renders with keep + empty placeholder ──────
echo "[bp-newapi] Case 1+2: DSN Secret carries resource-policy:keep + empty placeholder"
# The cnpg-cluster.yaml render emits the Secret first, then (under the CNPG
# CRD) the Cluster; slice the doc that starts at `kind: Secret`.
secret_block=$("$helm" template "$release" "$chart_dir" -f "$common_values" "${api_flag[@]}" \
  --show-only templates/cnpg-cluster.yaml 2>&1 \
  | awk '/^---$/{f=0} /^kind: Secret$/{f=1} f')

if [ -z "$secret_block" ]; then
  echo "FAIL: DSN placeholder Secret did not render"
  exit 1
fi
if ! grep -q "name: ${dsn_secret_name}" <<<"$secret_block"; then
  echo "FAIL: rendered Secret is not the expected ${dsn_secret_name}"
  echo "$secret_block" | grep "name:" | head -1
  exit 1
fi
if ! grep -q "helm.sh/resource-policy: keep" <<<"$secret_block"; then
  echo "FAIL: DSN Secret missing 'helm.sh/resource-policy: keep' — it will be"
  echo "      clobbered to empty on the next render and CrashLoop the data plane"
  exit 1
fi
if ! grep -qE '^[[:space:]]*SQL_DSN:' <<<"$secret_block"; then
  echo "FAIL: DSN Secret has no SQL_DSN key"
  exit 1
fi
# First-install render (no existing Secret in a dry-run) must be the empty
# placeholder so kubelet does not trip CreateContainerConfigError.
if ! grep -qE '^[[:space:]]*SQL_DSN:[[:space:]]*""[[:space:]]*$' <<<"$secret_block"; then
  echo "FAIL: first-install/dry-run SQL_DSN should be the empty placeholder"
  exit 1
fi
echo "[bp-newapi] Case 1+2: PASS"

# ── Case 3: gate volume + deployment env reference the SAME secret ────
echo "[bp-newapi] Case 3: wait-for-sql-dsn gate + Deployment env target the preserved Secret"
full=$("$helm" template "$release" "$chart_dir" -f "$common_values" "${api_flag[@]}" 2>&1)

if ! grep -q "name: wait-for-sql-dsn" <<<"$full"; then
  echo "FAIL: wait-for-sql-dsn initContainer did not render"
  exit 1
fi
gate_vol=$(echo "$full" | awk '/name: sql-dsn-gate/{f=1} f&&/secretName:/{print; exit}')
if ! grep -q "secretName: ${dsn_secret_name}" <<<"$gate_vol"; then
  echo "FAIL: sql-dsn-gate volume does not mount ${dsn_secret_name}"
  echo "      (got: ${gate_vol})"
  exit 1
fi
# The Deployment's SQL_DSN env secretKeyRef must target the same Secret.
sqldsn_ref=$(echo "$full" | awk '/name: SQL_DSN/{f=1} f&&/name: '"${dsn_secret_name}"'/{print; exit}')
if [ -z "$sqldsn_ref" ]; then
  echo "FAIL: Deployment SQL_DSN secretKeyRef does not reference ${dsn_secret_name}"
  exit 1
fi
echo "[bp-newapi] Case 3: PASS"

# ── Case 4: vcluster-app renders nothing from cnpg-cluster.yaml ───────
echo "[bp-newapi] Case 4: vcluster-app (cnpg.enabled=false) emits no DSN Secret/Cluster"
vc_values=$(mktemp)
cat > "$vc_values" <<'EOF'
placement:
  role: vcluster-app
cnpg:
  enabled: false
database:
  existingSecret: mirrored-dsn
auth:
  adminUI:
    mode: masterKey
EOF
# --show-only on a template that renders nothing exits non-zero with
# "could not find template" — that is the PASS signal here.
if vc_out=$("$helm" template "$release" "$chart_dir" -f "$vc_values" "${api_flag[@]}" \
     --show-only templates/cnpg-cluster.yaml 2>&1); then
  if grep -q "${dsn_secret_name}" <<<"$vc_out"; then
    echo "FAIL: vcluster-app should NOT render the host-side DSN Secret"
    rm -f "$vc_values"
    exit 1
  fi
fi
rm -f "$vc_values"
echo "[bp-newapi] Case 4: PASS"

echo "[bp-newapi] All DSN-secret-preserve render cases PASS"
