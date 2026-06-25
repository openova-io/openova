#!/usr/bin/env bash
# #4354 (Refs #4325 #4353) — assert the git-data PRESERVATION guard is wired so a
# vCluster→host-ns re-home of gitea can NEVER boot a fresh-empty git-data volume
# while a prior PV or a non-empty metadata DB exists.
#
# THE REGRESSION this gate locks down
# ===================================
# #4325 de-vclustered gitea (mgmt vCluster → host ns `gitea`) via a FRESH helm
# install. The upstream subchart dynamically provisioned a brand-NEW empty
# `gitea-shared-storage` PVC and ABANDONED the prior PV, while gitea's external
# CNPG metadata DB survived → catastrophic DB↔disk drift (the live #4353 P0: 42
# repos `empty:false` but every contents 500s "no such file or directory").
#
# The fix is templates/gitdata-preservation-hook.yaml — a pre-install,pre-upgrade
# Helm hook that ADOPTS an orphan PV (preserve), NO-OPs on a fresh prov, and
# FAILS LOUD when the DB reports repos but no PV can be adopted. This test proves
# the hook renders with all three guarantees and is a clean no-op when disabled.
#
# Self-contained `helm template` assertions only (no kind cluster), per the CI
# convention in .github/workflows/blueprint-release.yaml.
set -euo pipefail

# CI invokes `bash <script> <chart_dir>`; fall back to the script's own dir.
chart_dir="${1:-$(dirname "$0")/..}"
cd "$chart_dir"
chart_dir="$(pwd)"

# The upstream gitea subchart is vendored under charts/*.tgz (gitignored, rebuilt
# from Chart.lock). CI runs `helm dependency build` before tests; build it here
# too so the test is runnable standalone.
if ! ls charts/*.tgz >/dev/null 2>&1; then
  helm dependency build "$chart_dir" >/dev/null 2>&1 || true
fi

render() {
  # All renders set sso.sovereignFqdn so the sso-configure Deployment also
  # renders (unrelated, but keeps the manifest set realistic).
  helm template gitea . \
    --set sso.sovereignFqdn=example.test \
    --set global.sovereignFQDN=example.test \
    --namespace gitea "$@" 2>/dev/null
}

fail=0
note() { echo "  $1"; }
ok()   { echo "PASS [$1]"; }
bad()  { echo "FAIL [$1]"; fail=1; }

DEFAULT="$(render)"
DISABLED="$(render --set gitDataMigration.enabled=false)"

# ── 1. By default the guard renders (enabled). ────────────────────────────────
if grep -q "catalyst.openova.io/component: gitdata-guard" <<<"$DEFAULT"; then
  ok "guard renders by default"
else
  bad "guard renders by default"; note "expected a gitdata-guard Job in the default render"
fi

# ── 2. It is a pre-install,pre-upgrade hook (runs BEFORE the chart PVC). ───────
# Isolate the guard hook's own document set and assert the Job carries the hook
# annotation. A post-render Job would land too late to pre-bind the PVC.
GUARD_BLOCK="$(awk '
  /^# Source:/ { src=$0 }
  src=="# Source: bp-gitea/templates/gitdata-preservation-hook.yaml" { print }
' <<<"$DEFAULT")"
if grep -q 'pre-install,pre-upgrade' <<<"$GUARD_BLOCK"; then
  ok "guard is a pre-install,pre-upgrade hook"
else
  bad "guard is a pre-install,pre-upgrade hook"; note "the hook MUST run before the subchart PVC lands"
fi

# ── 3. The guard owns a Job + its RBAC (SA, ClusterRole for PV, Role for PVC). ─
for kind in "kind: ServiceAccount" "kind: ClusterRole" "kind: ClusterRoleBinding" "kind: Role" "kind: RoleBinding" "kind: Job"; do
  if grep -q "$kind" <<<"$GUARD_BLOCK"; then
    ok "guard renders $kind"
  else
    bad "guard renders $kind"
  fi
done

# ClusterRole must grant patch on persistentvolumes (force Retain + clear claimRef).
if grep -q 'persistentvolumes' <<<"$GUARD_BLOCK" && grep -Eq '"get", "list", "patch"' <<<"$GUARD_BLOCK"; then
  ok "guard ClusterRole can patch PersistentVolumes"
else
  bad "guard ClusterRole can patch PersistentVolumes"; note "needed to force reclaimPolicy Retain + clear claimRef on the orphan PV"
fi

# ── 4. ADOPT path: the guard pre-binds the host PVC to the orphan PV via
#       volumeName (preserve), and forces Retain so the data can't be reaped. ──
if grep -q 'volumeName:' <<<"$GUARD_BLOCK"; then
  ok "guard pre-binds the PVC via volumeName (PV adoption, not fresh provision)"
else
  bad "guard pre-binds the PVC via volumeName"; note "without volumeName the re-home would provision a FRESH empty PV (the #4354 bug)"
fi
if grep -q 'persistentVolumeReclaimPolicy":"Retain' <<<"$GUARD_BLOCK"; then
  ok "guard forces reclaimPolicy Retain on the adopted PV"
else
  bad "guard forces reclaimPolicy Retain on the adopted PV"; note "closes the #4352 evs-ssd Retain→Delete interaction"
fi

# ── 5. FAIL-LOUD path: the guard exits non-zero on DB↔disk drift. ─────────────
# The bash carries the explicit `exit 1` drift guard + the FAIL_ON_DRIFT switch.
if grep -q 'FAIL_ON_DRIFT' <<<"$GUARD_BLOCK" && grep -Eq 'DB.disk DRIFT' <<<"$GUARD_BLOCK" && grep -q 'exit 1' <<<"$GUARD_BLOCK"; then
  ok "guard FAILS LOUD on DB↔disk drift (no silent empty boot)"
else
  bad "guard FAILS LOUD on DB↔disk drift"; note "must exit non-zero when the DB reports repos but no git-data PV can be adopted"
fi

# ── 6. failOnDbDiskDrift default true is surfaced into the Job env. ───────────
if grep -A1 'name: FAIL_ON_DRIFT' <<<"$GUARD_BLOCK" | grep -q 'value: "true"'; then
  ok "failOnDbDiskDrift defaults to true (fail-closed)"
else
  bad "failOnDbDiskDrift defaults to true"; note "the safe default is to BLOCK the install on drift, not allow an empty boot"
fi

# ── 7. Disabled → ZERO guard resources (clean opt-out for object-store-only). ─
if grep -q 'gitdata-guard' <<<"$DISABLED"; then
  bad "disabled removes ALL guard resources"; note "gitDataMigration.enabled=false should render no guard"
else
  ok "disabled removes ALL guard resources"
fi

# ── 8. The render is still valid YAML with the guard present. ─────────────────
if python3 -c "import sys,yaml; list(yaml.safe_load_all(sys.stdin))" <<<"$DEFAULT" 2>/dev/null; then
  ok "rendered manifest set is valid YAML"
else
  bad "rendered manifest set is valid YAML"
fi

if [ "$fail" -ne 0 ]; then
  echo "gitdata-preservation-guard: one or more guarantees failed — a vCluster→host re-home could boot an EMPTY git-data volume (#4354 regression)."
  exit 1
fi
echo "gitdata-preservation-guard: git-data is preserved on re-home (adopt-or-fail-loud); the silent empty-PVC boot of #4353 cannot recur."
