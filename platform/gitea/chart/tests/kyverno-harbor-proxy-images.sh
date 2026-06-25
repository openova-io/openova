#!/usr/bin/env bash
# #4369 (Refs #4367 #4354) — assert EVERY image in the rendered bp-gitea
# install/upgrade manifest set is sourced through the local Harbor, so the
# cluster-wide Kyverno `harbor-proxy-pull` Enforce ClusterPolicy admits every
# Pod/Job on a converged Sovereign.
#
# THE REGRESSION this gate locks down
# ===================================
# #4367 (chart 1.2.43) added templates/gitdata-preservation-hook.yaml. Its guard
# Job defaulted its kubectl image to a BARE `bitnamilegacy/kubectl:1.30.7`
# (docker.io) ref. `harbor-proxy-pull` admits ONLY an image matching
# `*/proxy-*/*` (proxy-cache) OR `*/openova-io/*` (native-push). The bare ref was
# DENIED at admission ⇒ the pre-install,pre-upgrade hook Job could not schedule ⇒
# the pre-upgrade gate FAILED ⇒ the whole bp-gitea HR rollback-looped 1.2.43→1.2.42
# ⇒ bp-catalyst-platform / bp-continuum / bp-sandbox all stranded (the live #4369
# P0 on kom4dc). #4369 defaults the guard image to the harbor-proxy form in BOTH
# values.yaml and the template inline default.
#
# WHAT THIS TEST ASSERTS
# ======================
# Across the realistic render set (default, sso-enabled, the explicit override
# path), NO container `image:` is a raw docker.io / quay.io / ghcr.io / bare
# `repo/name` ref. Every image must be either:
#   - `harbor.openova.io/proxy-*/...`  (proxy-cache, matches `*/proxy-*/*`), or
#   - `harbor.openova.io/openova-io/...`(native-push, matches `*/openova-io/*`).
#
# SCOPING: the upstream gitea subchart ships a `helm test` connection-probe Pod
# (charts/gitea/templates/tests/test-http-connection.yaml, `busybox:latest`).
# That is a `helm.sh/hook: test` artifact — it is NEVER applied during
# install/upgrade admission (only on an explicit `helm test`), so it is not part
# of the admitted workload set and is excluded here. Everything that lands during
# install/upgrade (incl. the #4369 guard hook Job) MUST be Harbor-compliant.
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

fail=0

# Extract every container `image:` value, dropping the upstream `helm test` Pod's
# image (it is hook=test, never admitted on install/upgrade — see header).
images_of() {
  helm template gitea . \
    --namespace gitea \
    --set sso.sovereignFqdn=example.test \
    --set global.sovereignFQDN=example.test \
    "$@" 2>/dev/null \
  | awk '
      /^# Source:/ { src=$0 }
      # Skip the upstream helm-test connection probe (hook=test, not admitted).
      src ~ /templates\/tests\/test-http-connection\.yaml/ { next }
      /^[[:space:]]*image:[[:space:]]*/ {
        line=$0
        sub(/^[[:space:]]*image:[[:space:]]*/, "", line)
        gsub(/"/, "", line)
        print line
      }
    ' | sort -u
}

# A Harbor-compliant image matches the harbor-proxy-pull globs:
#   `*/proxy-*/*`  (e.g. harbor.openova.io/proxy-dockerhub/bitnamilegacy/kubectl:..)
#   `*/openova-io/*` (native-push)
compliant() {
  case "$1" in
    */proxy-*/*) return 0 ;;
    */openova-io/*) return 0 ;;
    *) return 1 ;;
  esac
}

check() {
  local label="$1"; shift
  local bad=""
  while IFS= read -r img; do
    [ -z "$img" ] && continue
    if ! compliant "$img"; then
      bad+="    $img"$'\n'
    fi
  done < <(images_of "$@")
  if [ -n "$bad" ]; then
    echo "FAIL [$label]: image(s) NOT matching harbor-proxy-pull (\`*/proxy-*/*\` or \`*/openova-io/*\`):"
    printf '%s' "$bad"
    fail=1
  else
    echo "PASS [$label]: all admitted images route through the local Harbor"
  fi
}

# 1. Default render — the guard hook renders (gitDataMigration.enabled default true).
check "default"
# 2. SSO enabled — renders the sso-configure Deployment too.
check "sso-enabled" --set sso.enabled=true
# 3. Explicit operator override of the guard image (must stay overridable + the
#    proxy default must NOT be hard-coded past the override).
check "guard-image-override" \
  --set gitDataMigration.image=harbor.openova.io/proxy-dockerhub/bitnamilegacy/kubectl:1.31.0

# 4. Pinpoint assertion: the #4369 offender — the gitdata-guard Job image — is the
#    harbor-proxy form by DEFAULT (no override), and is NOT a bare docker.io ref.
GUARD_IMG="$(helm template gitea . \
  --namespace gitea \
  --set sso.sovereignFqdn=example.test \
  --set global.sovereignFQDN=example.test 2>/dev/null \
  | awk '
      /^# Source:/ { src=$0 }
      src ~ /templates\/gitdata-preservation-hook\.yaml/ && /^[[:space:]]*image:/ {
        line=$0; sub(/^[[:space:]]*image:[[:space:]]*/, "", line); gsub(/"/, "", line); print line
      }')"
if [ -z "$GUARD_IMG" ]; then
  echo "FAIL [guard-image-default]: could not locate the gitdata-guard Job image in the render"
  fail=1
elif compliant "$GUARD_IMG"; then
  echo "PASS [guard-image-default]: gitdata-guard image is Harbor-proxy compliant ($GUARD_IMG)"
else
  echo "FAIL [guard-image-default]: gitdata-guard image is NOT Harbor-proxy compliant ($GUARD_IMG)"
  echo "  → Kyverno harbor-proxy-pull (Enforce) would DENY the pre-upgrade hook Job (the #4369 regression)."
  fail=1
fi

if [ "$fail" -ne 0 ]; then
  echo "[kyverno-harbor-proxy-images] FAILED — one or more bp-gitea images would be DENIED by the host-ns Kyverno harbor-proxy-pull Enforce policy."
  exit 1
fi
echo "[kyverno-harbor-proxy-images] All bp-gitea install/upgrade images are Harbor-proxy compliant; the #4369 pre-upgrade-hook denial cannot recur."
