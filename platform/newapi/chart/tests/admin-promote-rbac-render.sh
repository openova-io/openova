#!/usr/bin/env bash
# bp-newapi — admin-promote CronJob RBAC render audit (issue #4278).
#
# REGRESSION GUARD for the live defect measured on omantel.biz demo Org
# `org-7283eb4a-…` (chart 1.4.125): the per-minute admin-promote CronJob
# referenced the one-shot admin-SEED Job's Helm-HOOK ServiceAccount
# (`bp-newapi-admin-seed`). On a failed/timed-out install the hook phase
# deleted that hook-scoped SA, but the long-lived (regular-resource)
# CronJob survived and fired forever against a now-missing SA →
#   pods "...-..." is forbidden: error looking up service account
#   .../bp-newapi-admin-seed: serviceaccount "bp-newapi-admin-seed" not found
# → every spawned Job DeadlineExceeded → the Helm post-install hook never
# completed → bp-newapi HR wedged "context deadline exceeded".
#
# FIX (#4278): the admin-promote CronJob now has its OWN dedicated,
# RELEASE-MANAGED ServiceAccount + Role + RoleBinding (`bp-newapi-admin-promote`,
# no Helm-hook annotations), and the shared scripts ConfigMap is also
# release-managed (not a `before-hook-creation` hook). This test asserts:
#
#   1. A `bp-newapi-admin-promote` ServiceAccount renders WITHOUT any
#      helm.sh/hook annotation (release-managed, never deleted by hook cleanup).
#   2. A matching Role + RoleBinding (`bp-newapi-admin-promote`) render,
#      release-managed, and the RoleBinding binds the SA to that Role.
#   3. The CronJob's pod-template `serviceAccountName` is `bp-newapi-admin-promote`
#      (its own SA) — NOT `bp-newapi-admin-seed` (the hook SA).
#   4. The scripts ConfigMap `bp-newapi-admin-seed-scripts` is release-managed
#      (carries NO helm.sh/hook annotation) so it cannot vanish out from under
#      the orphaned CronJob.
#   5. The one-shot admin-seed Job + its SA REMAIN Helm hooks (correct —
#      they are genuinely one-shot post-install/post-upgrade work).

set -euo pipefail

# CI invokes `bash <script> <chart_dir>`; fall back to the script's parent.
chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm="${HELM_BIN:-helm}"

# Build chart deps once so `helm template` does not error on the `common`
# library subchart reference.
"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

# Values that flip the admin-seed render gate to TRUE:
#   and $seedEnabled .Values.newapi.enabled (keycloak adminUI) .Values.cnpg.enabled .Values.sovereignFQDN
# The chart defaults already set newapi/cnpg enabled + keycloak mode + sso
# bootstrap; we only need a sovereignFQDN to satisfy the final gate term.
#
# Release name `bp-newapi`: bp-newapi.fullname collapses to `bp-newapi`
# (the release name already contains the chart name), so the rendered
# object names match the live per-Org names asserted below
# (`bp-newapi-admin-promote`, `bp-newapi-admin-seed`).
out=$("$helm" template bp-newapi "$chart_dir" \
  --set sovereignFQDN=t99.omani.works \
  --namespace org-test \
  --show-only templates/admin-sso-seed-job.yaml 2>&1)

if [ -z "$out" ]; then
  echo "FAIL: admin-sso-seed-job.yaml rendered EMPTY — render gate not satisfied"
  echo "$out"
  exit 1
fi

# Split the multi-doc render into per-document files, then locate the doc
# for a given kind+name. Several objects share a kind (ServiceAccount,
# Role, RoleBinding), so we key on BOTH kind and name to assert hook-vs-
# release on the RIGHT object.
tmpd=$(mktemp -d)
trap 'rm -rf "$tmpd"' EXIT
echo "$out" | awk -v d="$tmpd" '
  BEGIN { n=0 }
  /^---$/ { n++; next }
  { print >> (d "/doc-" n ".yaml") }
'

find_doc() {  # find_doc <kind> <name> → prints path of the matching doc
  local want_kind="$1" want_name="$2" f
  for f in "$tmpd"/doc-*.yaml; do
    [ -f "$f" ] || continue
    if grep -qx "kind: $want_kind" "$f" && grep -qx "  name: $want_name" "$f"; then
      echo "$f"; return 0
    fi
  done
  return 1
}

assert_no_hook() {  # assert_no_hook <file> <human-label>
  if grep -q 'helm.sh/hook"' "$1" || grep -q '"helm.sh/hook":' "$1"; then
    echo "FAIL: $2 carries a helm.sh/hook annotation — must be RELEASE-managed (#4278)"
    sed -n '1,25p' "$1"
    exit 1
  fi
}

assert_has_hook() {  # assert_has_hook <file> <human-label>
  if ! grep -q 'helm.sh/hook' "$1"; then
    echo "FAIL: $2 is missing its helm.sh/hook annotation — one-shot seed must stay a hook (#4278)"
    sed -n '1,25p' "$1"
    exit 1
  fi
}

# ── Case 1: admin-promote ServiceAccount renders, release-managed ──────
echo "[bp-newapi] Case 1: admin-promote ServiceAccount — release-managed, no hook"
sa_doc=$(find_doc ServiceAccount bp-newapi-admin-promote) || {
  echo "FAIL: ServiceAccount/bp-newapi-admin-promote did not render"
  exit 1
}
assert_no_hook "$sa_doc" "ServiceAccount/bp-newapi-admin-promote"
echo "[bp-newapi] Case 1: PASS"

# ── Case 2: admin-promote Role + RoleBinding render, bound correctly ───
echo "[bp-newapi] Case 2: admin-promote Role + RoleBinding — release-managed, bound to its SA"
role_doc=$(find_doc Role bp-newapi-admin-promote) || {
  echo "FAIL: Role/bp-newapi-admin-promote did not render"
  exit 1
}
assert_no_hook "$role_doc" "Role/bp-newapi-admin-promote"

rb_doc=$(find_doc RoleBinding bp-newapi-admin-promote) || {
  echo "FAIL: RoleBinding/bp-newapi-admin-promote did not render"
  exit 1
}
assert_no_hook "$rb_doc" "RoleBinding/bp-newapi-admin-promote"
# RoleBinding subject SA + roleRef both point at bp-newapi-admin-promote.
if ! grep -q 'name: bp-newapi-admin-promote' "$rb_doc"; then
  echo "FAIL: RoleBinding/bp-newapi-admin-promote does not reference SA/Role bp-newapi-admin-promote"
  cat "$rb_doc"
  exit 1
fi
echo "[bp-newapi] Case 2: PASS"

# ── Case 3: CronJob pod serviceAccountName == its OWN SA, not the hook SA ─
echo "[bp-newapi] Case 3: admin-promote CronJob serviceAccountName == bp-newapi-admin-promote"
cron_doc=$(find_doc CronJob bp-newapi-admin-promote) || {
  echo "FAIL: CronJob/bp-newapi-admin-promote did not render"
  exit 1
}
if ! grep -q 'serviceAccountName: bp-newapi-admin-promote' "$cron_doc"; then
  echo "FAIL: CronJob does not use serviceAccountName: bp-newapi-admin-promote"
  grep -n 'serviceAccountName' "$cron_doc" || true
  exit 1
fi
if grep -q 'serviceAccountName: bp-newapi-admin-seed' "$cron_doc"; then
  echo "FAIL: CronJob still binds the HOOK SA bp-newapi-admin-seed (#4278 regression)"
  exit 1
fi
echo "[bp-newapi] Case 3: PASS"

# ── Case 4: shared scripts ConfigMap is release-managed (no hook) ──────
echo "[bp-newapi] Case 4: scripts ConfigMap — release-managed, no hook"
cm_doc=$(find_doc ConfigMap bp-newapi-admin-seed-scripts) || {
  echo "FAIL: ConfigMap/bp-newapi-admin-seed-scripts did not render"
  exit 1
}
assert_no_hook "$cm_doc" "ConfigMap/bp-newapi-admin-seed-scripts"
echo "[bp-newapi] Case 4: PASS"

# ── Case 5: one-shot admin-seed Job + SA REMAIN Helm hooks ─────────────
echo "[bp-newapi] Case 5: admin-seed Job + SA remain Helm hooks (one-shot)"
seed_sa_doc=$(find_doc ServiceAccount bp-newapi-admin-seed) || {
  echo "FAIL: ServiceAccount/bp-newapi-admin-seed did not render"
  exit 1
}
assert_has_hook "$seed_sa_doc" "ServiceAccount/bp-newapi-admin-seed"

seed_job_doc=$(find_doc Job bp-newapi-admin-seed) || {
  echo "FAIL: Job/bp-newapi-admin-seed did not render"
  exit 1
}
assert_has_hook "$seed_job_doc" "Job/bp-newapi-admin-seed"
# The seed Job correctly still binds its own hook SA.
if ! grep -q 'serviceAccountName: bp-newapi-admin-seed' "$seed_job_doc"; then
  echo "FAIL: seed Job does not bind serviceAccountName: bp-newapi-admin-seed"
  exit 1
fi
echo "[bp-newapi] Case 5: PASS"

echo "[bp-newapi] All admin-promote RBAC render cases PASS"
