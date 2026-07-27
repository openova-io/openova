#!/usr/bin/env bash
# bp-cilium — G117.5 #2744 Hubble UI oauth2-proxy OIDC enforcement guard.
#
# Closes the Tier-2 SSO gap the 1.3.7 decorative-gate removal left open:
# verifies the oauth2-proxy sidecar genuinely sits in front of the Hubble
# UI Service when auth=oidc, so a fresh prov is OIDC-gated (no
# unauthenticated exposure), and that the legacy auth=none path is a
# direct passthrough (no proxy rendered).
#
# Cases:
#   1. auth=none  → NO oauth2-proxy Deployment/Service/Secret; HTTPRoute
#      backend is the upstream hubble-ui Service.
#   2. auth=oidc  → oauth2-proxy Deployment + Service + chart-managed
#      Secret render; HTTPRoute backend is the oauth2-proxy Service.
#   3. auth=oidc  → oauth2-proxy args carry the sovereign-realm issuer,
#      hubble-ui client-id, /oauth2/callback redirect, the upstream
#      hubble-ui Service, and kc_idp_hint=catalyst-pin (silent SSO).
#   4. auth=oidc + chartManagedSecret=false → Secret suppressed, Deployment
#      still references the existingSecret (BYO ExternalSecret path).

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

fqdn="smoke.example.com"
common=(
  --set catalystOverlay.hubbleUI.enabled=true
  --set "catalystOverlay.hubbleUI.sovereignFQDN=${fqdn}"
  --set cilium.clustermesh.apiserver.service.lbIpam.enabled=false
)

# Render a single template (deps are built, so --show-only is safe).
show() {
  local tmpl="$1"; shift
  "$helm" template smoke "$chart_dir" "${common[@]}" "$@" --show-only "templates/${tmpl}" 2>/dev/null || true
}

route_backend() {
  # first backendRefs name from the rendered HTTPRoute
  awk '
    /backendRefs:/ {inb=1; next}
    inb && /name:/ { v=$0; sub(/.*name:[ \t]*/,"",v); gsub(/["[:space:]]/,"",v); print v; exit }
  ' <<<"$1"
}

# ── Case 1: auth=none → no proxy, direct passthrough ──────────────────────
echo "[g117-hubble-oauth2-proxy] Case 1: auth=none renders NO proxy + direct route"
dep_none="$(show hubble-ui-oauth2-proxy-deployment.yaml --set catalystOverlay.hubbleUI.auth=none)"
if grep -q "kind: Deployment" <<<"$dep_none"; then
  echo "FAIL: oauth2-proxy Deployment rendered under auth=none" >&2; exit 1
fi
route_none="$(show hubble-ui-httproute.yaml --set catalystOverlay.hubbleUI.auth=none)"
b1="$(route_backend "$route_none")"
if [ "$b1" != "hubble-ui" ]; then
  echo "FAIL: auth=none HTTPRoute backend is '$b1' (expected hubble-ui)" >&2; exit 1
fi
echo "  PASS (backend=hubble-ui, no proxy)"

# ── Case 2: auth=oidc → proxy Deployment+Service + route→proxy ────────────
#
# #5416: under the Catalyst default (ssoBridgeSync on) NO chart-managed Secret
# renders and BOTH credentials come from the bp-sso-bridge-published
# `hubble-ui-oauth2-proxy-oidc`. Before #5416 the cookie-secret was minted here
# by `lookup`-or-generate, which yields a DIFFERENT value in each region of a
# 2-region Sovereign (separate apiservers ⇒ separate Helm ⇒ the lookup misses),
# so the two proxies behind the one shared wildcard VIP rejected each other's
# session cookie. The chart also minted a SECOND client-secret that nothing
# consumed — the reason a per-gate credential audit finds hubble's client
# secret split across regions while the other three gates match.
echo "[g117-hubble-oauth2-proxy] Case 2: auth=oidc renders proxy + routes through it"
dep_oidc="$(show hubble-ui-oauth2-proxy-deployment.yaml --set catalystOverlay.hubbleUI.auth=oidc)"
svc_oidc="$(show hubble-ui-oauth2-proxy-service.yaml --set catalystOverlay.hubbleUI.auth=oidc)"
sec_oidc="$(show hubble-ui-oauth2-proxy-secret.yaml --set catalystOverlay.hubbleUI.auth=oidc)"
grep -q "kind: Deployment" <<<"$dep_oidc" || { echo "FAIL: oauth2-proxy Deployment missing under auth=oidc" >&2; exit 1; }
grep -q "kind: Service"    <<<"$svc_oidc" || { echo "FAIL: oauth2-proxy Service missing under auth=oidc" >&2; exit 1; }
if grep -q "kind: Secret" <<<"$sec_oidc"; then
  echo "FAIL: #5416 — chart-managed per-region Secret rendered under the ssoBridgeSync default" >&2; exit 1
fi
route_oidc="$(show hubble-ui-httproute.yaml --set catalystOverlay.hubbleUI.auth=oidc)"
b2="$(route_backend "$route_oidc")"
if [ "$b2" != "hubble-ui-oauth2-proxy" ]; then
  echo "FAIL: auth=oidc HTTPRoute backend is '$b2' (expected hubble-ui-oauth2-proxy)" >&2; exit 1
fi
echo "  PASS (Deployment+Service render, no per-region Secret; route→proxy)"

# ── Case 2b: #5416 — BOTH credentials resolve from the bridge-published Secret
echo "[g117-hubble-oauth2-proxy] Case 2b: #5416 both creds come from hubble-ui-oauth2-proxy-oidc"
env_src() {
  # Secret name backing the given env var in the rendered Deployment.
  awk -v want="$1" '
    $0 ~ ("- name: " want) {hit=1; next}
    hit && /name:/ { v=$0; sub(/.*name:[ \t]*/,"",v); gsub(/["[:space:]]/,"",v); print v; exit }
  ' <<<"$dep_oidc"
}
for var in OAUTH2_PROXY_CLIENT_SECRET OAUTH2_PROXY_COOKIE_SECRET; do
  src="$(env_src "$var")"
  if [ "$src" != "hubble-ui-oauth2-proxy-oidc" ]; then
    echo "FAIL: #5416 — ${var} sourced from '${src}' (expected hubble-ui-oauth2-proxy-oidc)" >&2; exit 1
  fi
done
# The declared ExternalSecret must ask bp-sso-bridge for BOTH properties.
cm_reg="$(show hubble-ui-sso-registration-configmap.yaml --set catalystOverlay.hubbleUI.auth=oidc)"
grep -q '"property": "client_secret"' <<<"$cm_reg" || { echo "FAIL: declaration missing client_secret property" >&2; exit 1; }
grep -q '"property": "cookie_secret"' <<<"$cm_reg" || { echo "FAIL: #5416 — declaration missing cookie_secret property" >&2; exit 1; }
echo "  PASS (client-secret + cookie-secret both from the bridge-published Secret)"

# ── Case 3: oauth2-proxy args wire the silent-SSO chain ───────────────────
echo "[g117-hubble-oauth2-proxy] Case 3: oauth2-proxy args wire silent SSO"
assert_arg() {
  grep -qF -- "$1" <<<"$dep_oidc" || { echo "FAIL: oauth2-proxy missing arg: $1" >&2; exit 1; }
}
assert_arg "--oidc-issuer-url=https://auth.${fqdn}/realms/sovereign"
assert_arg "--client-id=hubble-ui"
assert_arg "--redirect-url=https://hubble.${fqdn}/oauth2/callback"
assert_arg "--upstream=http://hubble-ui.kube-system.svc.cluster.local:80"
assert_arg "kc_idp_hint=catalyst-pin"
echo "  PASS (issuer + client + redirect + upstream + kc_idp_hint)"

# ── Case 4: ssoBridgeSync=false → chart-managed fallback still works ──────
# The single-region / no-bridge path is unchanged by #5416: the chart mints
# both keys and the Deployment reads both from `existingSecret`.
echo "[g117-hubble-oauth2-proxy] Case 4: ssoBridgeSync=false → chart-managed fallback"
nobridge=(--set catalystOverlay.hubbleUI.auth=oidc --set catalystOverlay.hubbleUI.oauth2Proxy.ssoBridgeSync.enabled=false)
sec_nb="$(show hubble-ui-oauth2-proxy-secret.yaml "${nobridge[@]}")"
grep -q "kind: Secret"   <<<"$sec_nb" || { echo "FAIL: chart-managed Secret missing with ssoBridgeSync=false" >&2; exit 1; }
grep -q "client-secret:" <<<"$sec_nb" || { echo "FAIL: fallback Secret missing client-secret key" >&2; exit 1; }
grep -q "cookie-secret:" <<<"$sec_nb" || { echo "FAIL: fallback Secret missing cookie-secret key" >&2; exit 1; }
# Strip YAML comments — the template's #5416 rationale names the bridge Secret
# in prose, which would otherwise match these greps.
uncomment() { sed 's/[[:space:]]*#.*$//' <<<"$1"; }
dep_nb="$(uncomment "$(show hubble-ui-oauth2-proxy-deployment.yaml "${nobridge[@]}")")"
if grep -q "hubble-ui-oauth2-proxy-oidc" <<<"$dep_nb"; then
  echo "FAIL: bridge Secret referenced with ssoBridgeSync=false" >&2; exit 1
fi
grep -q "hubble-ui-oauth2-proxy-sso"  <<<"$dep_nb" || { echo "FAIL: Deployment does not reference existingSecret" >&2; exit 1; }
echo "  PASS (both keys chart-managed; Deployment references existingSecret)"

# ── Case 4b: chartManagedSecret=false suppresses the fallback Secret ──────
echo "[g117-hubble-oauth2-proxy] Case 4b: chartManagedSecret=false → BYO secret"
sec_byo="$(show hubble-ui-oauth2-proxy-secret.yaml "${nobridge[@]}" --set catalystOverlay.hubbleUI.oauth2Proxy.chartManagedSecret=false)"
if grep -q "kind: Secret" <<<"$sec_byo"; then
  echo "FAIL: chart-managed Secret rendered despite chartManagedSecret=false" >&2; exit 1
fi
dep_byo="$(show hubble-ui-oauth2-proxy-deployment.yaml "${nobridge[@]}" --set catalystOverlay.hubbleUI.oauth2Proxy.chartManagedSecret=false)"
grep -q "kind: Deployment" <<<"$dep_byo" || { echo "FAIL: Deployment missing under chartManagedSecret=false" >&2; exit 1; }
grep -q "hubble-ui-oauth2-proxy-sso" <<<"$dep_byo" || { echo "FAIL: Deployment does not reference existingSecret" >&2; exit 1; }
echo "  PASS (Secret suppressed; Deployment references existingSecret)"

# ── Case 5: #4507 — /api XHR returns 401 not 302 (gRPC-Web flow stream) ────
# The Hubble UI flow stream is a gRPC-Web/Connect server-streaming XHR to
# /api/v1/...; a gRPC-Web client cannot follow a 302 to the Keycloak login
# HTML page, so on a missing/expired session the default 302-to-login
# silently wedges the stream and flows never render (UAT row 39). --api-route
# makes oauth2-proxy return HTTP 401 for these paths. It still AUTHs the path
# (unlike --skip-auth-route) — no unauthenticated-exposure regression.
echo "[g117-hubble-oauth2-proxy] Case 5: --api-route=^/api/ on flow-stream path"
grep -qF -- "--api-route=^/api/" <<<"$dep_oidc" || {
  echo "FAIL: oauth2-proxy missing --api-route=^/api/ — flow XHR would 302-to-KC-login (flows never render, #4507)" >&2; exit 1
}
# Guard against a regression to --skip-auth-route on the API path (that would
# expose the flow stream UNAUTHENTICATED).
if grep -qF -- "--skip-auth-route=^/api/" <<<"$dep_oidc"; then
  echo "FAIL: flow API path is on --skip-auth-route (unauthenticated exposure) — must be --api-route" >&2; exit 1
fi
# Custom apiRoutes override is honored.
dep_custom="$(show hubble-ui-oauth2-proxy-deployment.yaml --set catalystOverlay.hubbleUI.auth=oidc --set 'catalystOverlay.hubbleUI.oauth2Proxy.apiRoutes={^/api/,^/v1/grpc/}')"
grep -qF -- "--api-route=^/v1/grpc/" <<<"$dep_custom" || {
  echo "FAIL: custom oauth2Proxy.apiRoutes not honored" >&2; exit 1
}
echo "  PASS (--api-route=^/api/ present; not skip-auth; override honored)"

echo "[bp-cilium G117.5 #2744 hubble oauth2-proxy enforcement] All cases PASS"
