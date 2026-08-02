#!/usr/bin/env bash
#
# check-uat-walkability.sh — is the environment able to walk a UAT row RIGHT NOW?
#
# WHY THIS EXISTS
#
# UAT rows assert different things at different layers, and only some are
# walkable at any moment. Repeatedly discovering "the surface is down" by
# hand — once per session, after picking a row — wastes the pick and produces
# a stale claim either way. This answers the question BEFORE a row is chosen,
# and prints WHICH CLASS of row is currently reachable.
#
# On 2026-08-01 the mothership `catalyst` namespace sat at zero pods for 3+
# hours with the console 503, while repo- and registry-scoped rows stayed
# perfectly walkable (R21 was closed at full population against ghcr during
# exactly that window). Without this distinction, "no surface" reads as "no
# work", which is wrong.
#
# THE FOUR CLASSES
#
#   REPO      assertions about chart/manifest/code content
#             -> always walkable; needs nothing but a checkout
#   REGISTRY  assertions about published artifacts (pins vs ghcr)
#             -> needs network + gh auth
#   MOTHER    assertions about the mothership control plane (janitor,
#             deployment registry, the deployment wizard)
#             -> needs catalyst-api Running and the console serving
#   SOVEREIGN assertions about a provisioned Sovereign (Organizations,
#             Applications, Environments, DR, cutover)
#             -> needs Catalyst CRDs present AND a live Sovereign
#
# EXIT CODES
#   0  at least one class is walkable (normal — REPO always is)
#   1  nothing is walkable (should be impossible; means the checkout is broken)
#   2  self-test failed (fails closed)

set -uo pipefail

KUBECONFIG_PATH="${KUBECONFIG:-$HOME/.kube/config}"
CONSOLE="${CONSOLE_URL:-https://console.openova.io}"

ok()   { printf '  \033[32m%-9s WALKABLE\033[0m  %s\n' "$1" "$2"; }
no()   { printf '  \033[31m%-9s BLOCKED \033[0m  %s\n' "$1" "$2"; }

# --- REPO -------------------------------------------------------------------
repo_class() {
  if [ -f "docs/ledger/UAT.md" ] && [ -d "platform" ]; then
    ok "REPO" "chart/manifest/code assertions — e.g. R3, R4, R6, R10, R14, R18"
    return 0
  fi
  no "REPO" "not in an openova checkout"
  return 1
}

# --- REGISTRY ---------------------------------------------------------------
registry_class() {
  if command -v gh >/dev/null 2>&1 && \
     gh api /orgs/openova-io/packages/container/bp-cnpg --jq .name >/dev/null 2>&1; then
    ok "REGISTRY" "published-artifact assertions — e.g. R21 (catalog-seed pins vs ghcr)"
    return 0
  fi
  no "REGISTRY" "gh unauthenticated or ghcr unreachable"
  return 1
}

# --- MOTHER -----------------------------------------------------------------
mother_class() {
  local pods code
  pods="$(timeout 25 kubectl --kubeconfig "$KUBECONFIG_PATH" -n catalyst \
            get pods --no-headers 2>/dev/null | grep -c Running || true)"
  code="$(timeout 20 curl -s -o /dev/null -w '%{http_code}' "$CONSOLE/sovereign/" 2>/dev/null || echo 000)"
  if [ "${pods:-0}" -gt 0 ] && [ "$code" = "200" ]; then
    ok "MOTHER" "mothership assertions — janitor (M1/G5/R1), deployment wizard, registry"
    return 0
  fi
  no "MOTHER" "catalyst Running pods=${pods:-0}, console/sovereign/=${code} (need >0 and 200)"
  return 1
}

# --- SOVEREIGN --------------------------------------------------------------
sovereign_class() {
  local crds
  crds="$(timeout 25 kubectl --kubeconfig "$KUBECONFIG_PATH" get crd -o name 2>/dev/null \
            | grep -cE 'openova|catalyst' || true)"
  if [ "${crds:-0}" -gt 0 ]; then
    ok "SOVEREIGN" "Organization/Application/Environment/DR assertions"
    return 0
  fi
  no "SOVEREIGN" "zero Catalyst CRDs on this cluster — no object for a CRD-backed row to render"
  return 1
}

# --- self-test --------------------------------------------------------------
# A probe that cannot fail is worth nothing. Prove the MOTHER/SOVEREIGN checks
# actually discriminate by running them against a kubeconfig that cannot work.
self_test() {
  local rc=0 saved="$KUBECONFIG_PATH" savedc="$CONSOLE"
  KUBECONFIG_PATH="/nonexistent/kubeconfig"; CONSOLE="http://127.0.0.1:1"
  if mother_class    >/dev/null 2>&1; then echo "  SELF-TEST FAIL: MOTHER passed with no cluster";    rc=1; fi
  if sovereign_class >/dev/null 2>&1; then echo "  SELF-TEST FAIL: SOVEREIGN passed with no cluster"; rc=1; fi
  KUBECONFIG_PATH="$saved"; CONSOLE="$savedc"
  if ! repo_class >/dev/null 2>&1; then echo "  SELF-TEST FAIL: REPO blocked inside a checkout"; rc=1; fi
  [ "$rc" -eq 0 ] && echo "OK — self-test passed (cluster classes fail without a cluster; REPO holds)."
  return "$rc"
}

if [ "${1:-}" = "--self-test" ]; then self_test; exit $?; fi
if ! self_test >/dev/null 2>&1; then
  echo "FAIL: self-test did not pass — refusing to report. Run: $0 --self-test" >&2
  exit 2
fi

echo "UAT walkability — $(date -u +%Y-%m-%dT%H:%M:%SZ)"
any=1
repo_class      && any=0
registry_class  && any=0
mother_class    && any=0
sovereign_class && any=0
echo
if [ "$any" -ne 0 ]; then
  echo "Nothing walkable — this should be impossible inside a checkout."
  exit 1
fi
echo "Pick a row whose assertion lives in a WALKABLE class. A row is not"
echo "'unwalked because nobody tried' when its class is BLOCKED — record the"
echo "blocked class on the row instead of leaving it silently unexplained."
exit 0
