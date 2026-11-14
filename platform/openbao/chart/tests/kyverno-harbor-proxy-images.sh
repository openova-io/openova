#!/usr/bin/env bash
# #3375 — assert EVERY image in the rendered bp-openbao manifests is
# proxy-globbed through the local Harbor, so kyverno's `harbor-proxy-pull`
# Enforce policy (`*/proxy-*/*` or `*/openova-io/*`) admits every Pod on a
# converged Sovereign. The upstream subchart defaults the server/injector/
# agent images to raw quay.io/docker.io — which a converged Enforce env
# DENIES on the first chart-roll, wedging the openbao→ESO→sso-bridge chain
# (the documented kyverno-roll landmine; hw130 hit exactly this).
set -euo pipefail
cd "$(dirname "$0")/.."

fail=0
check() {
  local label="$1"; shift
  local raw
  raw=$(helm template bp-openbao . "$@" 2>/dev/null \
    | grep -E '^[[:space:]]*(image:|- name: AGENT_INJECT_VAULT_IMAGE)' -A0 \
    | grep -oE '(image: *"?|value: *"?)[^"]+' \
    | sed -E 's/^(image|value): *"?//' \
    | grep -iE 'quay\.io|docker\.io|ghcr\.io|^[a-z0-9]+/[a-z0-9]' \
    | grep -ivE 'harbor\.openova\.io|registry\.openova' || true)
  # Also catch the AGENT_INJECT_VAULT_IMAGE env value explicitly.
  local agent
  agent=$(helm template bp-openbao . "$@" 2>/dev/null \
    | grep -A1 'AGENT_INJECT_VAULT_IMAGE' | grep 'value:' \
    | grep -ivE 'harbor\.openova\.io' || true)
  if [ -n "$raw$agent" ]; then
    echo "FAIL [$label]: raw (non-Harbor-proxy) image(s) found:"
    printf '  %s\n' $raw $agent
    fail=1
  else
    echo "PASS [$label]: all images proxy-globbed through Harbor"
  fi
}

check "default" 
check "snapshot-primary" --set snapshotReplication.enabled=true --set snapshotReplication.role=primary
check "snapshot-secondary" --set snapshotReplication.enabled=true --set snapshotReplication.role=secondary

if [ "$fail" -ne 0 ]; then
  echo "[kyverno-harbor-proxy-images] FAILED"
  exit 1
fi
echo "[kyverno-harbor-proxy-images] All bp-openbao images Harbor-proxy compliant."
