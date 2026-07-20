#!/usr/bin/env bash
# bp-harbor — registry S3 blob-redirect wiring guard (#5302).
#
# The self-contained sovereign registry MUST serve blob bytes over its own
# trusted host (registry.<fqdn>) and NEVER 302-redirect a client to the
# differently-certed Object Storage backend (obs.<region>.<sov>.nationalcloud.om
# / S3). If it redirects, every in-cluster TLS-pinned client (source-controller
# OCI pulls, containerd, skopeo) fails on a COLD blob fetch with
#   x509: certificate is valid for *.obs... not registry.<fqdn>
# which wedges post-cutover Flux self-heal after any cache-evicting restart
# (region-kill G12 face-6 on hw281, dep 6db2745323dff4aa).
#
# The control is `persistence.imageChartStorage.disableredirect` — note the key
# is ALL-LOWERCASE. Upstream Harbor's registry-cm renders
#   redirect: { disable: {{ $storage.disableredirect }} }
# Helm values are case-sensitive, so the camelCase `disableRedirect` that
# #4573 F2 / #4575 shipped was silently dropped and the redirect stayed ENABLED
# for the fix's entire life (masked because cutover prewarm keeps blobs in
# Harbor's local cache — only a cold pull after eviction hits the redirect).
#
# Cases:
#   1. Sovereign overlay (type=s3, lowercase disableredirect=true) → disable: true
#   2. Lowercase key genuinely controls it (disableredirect=false → disable: false)
#   3. GUARD: chart default renders disable: true (redirect off by default)
#   4. GUARD: a camelCase `disableRedirect` override is INERT — it cannot flip
#      the redirect back on (the exact #4575 typo must never silently regress)

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"
cm="charts/harbor/templates/registry/registry-cm.yaml"

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

# Extract the `disable:` value from the registry config's storage.redirect block.
redirect_disable() {
  # Render only the registry configmap, then read the `redirect: { disable: X }`
  # line inside the embedded config.yml.
  "$helm" template smoke "$chart_dir" "$@" --show-only "$cm" 2>&1 \
    | awk '/redirect:/{f=1;next} f&&/disable:/{print $2;exit}'
}

# ── Case 1: sovereign overlay → redirect disabled ─────────────────────
echo "[bp-harbor] Case 1: type=s3 + disableredirect=true → registry redirect DISABLED"
v=$(redirect_disable \
      --set harbor.persistence.imageChartStorage.type=s3 \
      --set harbor.persistence.imageChartStorage.disableredirect=true)
if [ "$v" != "true" ]; then
  echo "FAIL: expected redirect.disable=true, got '$v'"
  echo "(cold chart pulls would 302 to OBS and x509-fail → post-cutover self-heal wedged)"
  exit 1
fi
echo "[bp-harbor] Case 1: PASS"

# ── Case 2: lowercase key genuinely controls the value ────────────────
echo "[bp-harbor] Case 2: lowercase disableredirect=false → redirect ENABLED (key is live)"
v=$(redirect_disable \
      --set harbor.persistence.imageChartStorage.type=s3 \
      --set harbor.persistence.imageChartStorage.disableredirect=false)
if [ "$v" != "false" ]; then
  echo "FAIL: lowercase disableredirect=false did not reach registry-cm (got '$v')"
  exit 1
fi
echo "[bp-harbor] Case 2: PASS"

# ── Case 3: chart default keeps redirect OFF ──────────────────────────
echo "[bp-harbor] Case 3: chart default → redirect DISABLED (safe sovereign default)"
v=$(redirect_disable)
if [ "$v" != "true" ]; then
  echo "FAIL: #5302 REGRESSION — chart default no longer disables the S3 blob redirect (got '$v')"
  exit 1
fi
echo "[bp-harbor] Case 3: PASS"

# ── Case 4: camelCase override is INERT (the #4575 typo cannot regress) ─
echo "[bp-harbor] Case 4: GUARD — camelCase disableRedirect cannot flip redirect back on"
v=$(redirect_disable \
      --set harbor.persistence.imageChartStorage.type=s3 \
      --set harbor.persistence.imageChartStorage.disableRedirect=false)
if [ "$v" != "true" ]; then
  echo "FAIL: a camelCase 'disableRedirect' override changed the rendered redirect —"
  echo "either the upstream key changed or the default was reverted (got '$v')."
  echo "The wired key is lowercase 'disableredirect'; camelCase must be inert."
  exit 1
fi
echo "[bp-harbor] Case 4: PASS"

echo "[bp-harbor] All registry disableredirect wiring cases PASS"
