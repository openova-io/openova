#!/usr/bin/env bash
# bp-agenity — #4553/#4556 oidc-gate companion render audit.
#
# Every per-Org agenity install must AUTOMATICALLY get a chart-managed
# bp-oidc-gate companion in front of its host (zero-click SSO + spaTokenSeed
# no-paste landing) — NOT the agnstar walk's hand-created drift-disabled live
# instance. This test asserts the durable contract:
#
#   1. GATED install (hostname set, oidcGate.enabled default true):
#      - the gate's resource set renders (oauth2-proxy Deployment, Service,
#        AppRegistration ConfigMap, ExternalSecret, HTTPRoute);
#      - the cookie secret arrives via that SAME ExternalSecret bundle and is
#        consumed by the Deployment — never chart-minted, which would generate
#        it independently per region (#5416 / PRINCIPLES.md A16);
#      - the gate's HTTPRoute carries the #4556 spaTokenSeed Exact `/` ->
#        /app/?token=sso 302 redirect;
#      - the oauth2-proxy --upstream points at the agenity Service;
#      - the client id derives `agenity-<slug>` from the host;
#      - EXACTLY ONE HTTPRoute renders (the gate's) — the chart's OWN route is
#        SUPPRESSED (two routes on one host is undefined routing);
#      - a gateway-ingress CiliumNetworkPolicy admits the `ingress` entity.
#   2. NON-gated install (oidcGate.enabled=false): NO gate resources; the
#      chart's OWN HTTPRoute renders with the bare-root -> /app/ redirect.
#   3. FAIL-CLOSED (no hostname, no sovereignFqdn): the gate renders nothing
#      (Inviolable Principle #4) — no oidc-gate-* resources.
#   4. clientId override is honoured.

set -euo pipefail

chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm="${HELM_BIN:-helm}"

HOST=agenity.agnstar.omani.homes
FQDN=agnstar.omani.homes

render_gated() {
  "$helm" template agenity "$chart_dir" \
    --set "sovereignFqdn=$FQDN" \
    --set "httpRoute.hostnames[0]=$HOST" \
    --api-versions "cilium.io/v2" \
    --api-versions "external-secrets.io/v1beta1" \
    "$@" 2>/dev/null
}

# ── Case 1: gated install renders the full companion + suppresses own route ──
echo "[oidc-gate-companion] Case 1: gated per-Org install renders the gate + suppresses chart route"
g="$(render_gated)"

grep -qE 'name: "oidc-gate-agenity-agnstar"' <<<"$g"                  || { echo "FAIL: no gate Deployment/Service/HTTPRoute oidc-gate-agenity-agnstar" >&2; echo "$g" >&2; exit 1; }
grep -qE 'kind: Deployment' <<<"$g"                                   || { echo "FAIL: no gate Deployment" >&2; exit 1; }
grep -qE 'name: "oidc-gate-agenity-agnstar-sso-registration"' <<<"$g" || { echo "FAIL: no AppRegistration ConfigMap" >&2; exit 1; }
grep -qE 'sso.openova.io/app-registration: "agenity-agnstar"' <<<"$g" || { echo "FAIL: AppRegistration not labelled for bp-sso-bridge" >&2; exit 1; }
grep -qE 'kind: ExternalSecret' <<<"$g"                               || { echo "FAIL: no client-secret ExternalSecret (with external-secrets API)" >&2; exit 1; }
grep -qE 'key: "sso/sovereign/agenity-agnstar"' <<<"$g"               || { echo "FAIL: ExternalSecret remoteRef not sso/sovereign/agenity-agnstar" >&2; exit 1; }
# #5416 — the cookie secret is NO LONGER a chart-minted Secret. It is carried in
# the SAME bp-sso-bridge/OpenBao bundle as the client secret and delivered by the
# `-oidc` ExternalSecret, because a chart-minted value is generated INDEPENDENTLY
# PER REGION: both regions serve one hostname behind one VIP, so a cookie sealed
# in region A cannot be opened by region B and roughly half of all requests fail.
# That read as "flakiness" for a long time (see PRINCIPLES.md A16).
#
# So assert the WIRING, not a Secret name — it is a strictly stronger check:
#   (a) the `-oidc` ExternalSecret publishes a cookie-secret key,
#   (b) it resolves the shared bundle's cookie_secret property,
#   (c) the oauth2-proxy Deployment actually CONSUMES it from that Secret.
# (c) matters most: an unconsumed key would satisfy (a)+(b) while the Pod still
# read a per-region value — exactly the inert-copy trap hubble was carrying.
grep -qE 'cookie-secret: "\{\{ \.cookie_secret \}\}"' <<<"$g"         || { echo "FAIL: -oidc ExternalSecret template does not publish a cookie-secret key (#5416)" >&2; echo "$g" >&2; exit 1; }
grep -qE 'property: cookie_secret' <<<"$g"                            || { echo "FAIL: -oidc ExternalSecret does not resolve the shared bundle's cookie_secret (#5416)" >&2; exit 1; }
grep -qzE 'name: OAUTH2_PROXY_COOKIE_SECRET[[:space:]]+valueFrom:[[:space:]]+secretKeyRef:[[:space:]]+name: "oidc-gate-agenity-agnstar-oidc"[[:space:]]+key: cookie-secret' <<<"$g" \
  || { echo "FAIL: OAUTH2_PROXY_COOKIE_SECRET is not consumed from oidc-gate-agenity-agnstar-oidc#cookie-secret (#5416)" >&2; echo "$g" >&2; exit 1; }
# Regression guard: no chart-minted per-region cookie value may come back.
if grep -qE 'name: "oidc-gate-agenity-agnstar-cookie"' <<<"$g"; then
  echo "FAIL: a chart-minted cookie Secret rendered — that value is generated per region and breaks ~50% of requests behind the shared VIP (#5416)" >&2; exit 1
fi
grep -qE -- '--client-id=agenity-agnstar' <<<"$g"                     || { echo "FAIL: oauth2-proxy --client-id not agenity-agnstar" >&2; exit 1; }
grep -qE -- '--upstream=http://agenity-bp-agenity\..*\.svc\.cluster\.local:8080' <<<"$g" || { echo "FAIL: gate --upstream not the agenity Service" >&2; echo "$g" >&2; exit 1; }
grep -qE -- '--login-url=https://auth\.agnstar\.omani\.homes/realms/sovereign.*kc_idp_hint=catalyst-pin' <<<"$g" || { echo "FAIL: silent SSO login-url/kc_idp_hint missing" >&2; exit 1; }
echo "  PASS — gate resource set rendered"

# spaTokenSeed redirect present (Exact / -> /app/?token=sso 302)
grep -qE 'replaceFullPath: "/app/\?token=sso"' <<<"$g" || { echo "FAIL: missing spaTokenSeed /app/?token=sso redirect" >&2; echo "$g" >&2; exit 1; }
grep -qE 'statusCode: 302' <<<"$g"                     || { echo "FAIL: spaTokenSeed redirect not 302" >&2; exit 1; }
echo "  PASS — #4556 spaTokenSeed no-paste redirect present"

# EXACTLY ONE HTTPRoute (the gate's) — chart's own route suppressed
n_routes="$(echo "$g" | grep -c 'kind: HTTPRoute' || true)"
[ "$n_routes" -eq 1 ] || { echo "FAIL: expected exactly 1 HTTPRoute (the gate's), got $n_routes — chart's own route not suppressed" >&2; echo "$g" >&2; exit 1; }
# and that ONE route is the gate's, not the chart's
if echo "$g" | awk 'BEGIN{RS="\n---\n"} /kind: HTTPRoute/ && /name: agenity-bp-agenity/{found=1} END{exit !found}'; then
  echo "FAIL: the chart's own HTTPRoute (agenity-bp-agenity) rendered alongside the gate" >&2; echo "$g" >&2; exit 1
fi
echo "  PASS — chart's own HTTPRoute suppressed (gate owns the host)"

# gateway-ingress CNP admits the ingress entity to the gate
grep -qE 'name: "oidc-gate-agenity-agnstar-gateway-ingress"' <<<"$g" || { echo "FAIL: no gate gateway-ingress CiliumNetworkPolicy" >&2; exit 1; }
grep -qE 'kind: CiliumNetworkPolicy' <<<"$g"                          || { echo "FAIL: no CiliumNetworkPolicy" >&2; exit 1; }
grep -qE 'port: "4180"' <<<"$g"                                       || { echo "FAIL: gate CNP not admitting port 4180" >&2; exit 1; }
echo "  PASS — gateway-entity CNP present"

# ── Case 2: oidcGate.enabled=false → chart's own route, NO gate ──────────────
echo "[oidc-gate-companion] Case 2: oidcGate.enabled=false renders the chart's own route, NO gate"
ng="$("$helm" template agenity "$chart_dir" --set "sovereignFqdn=$FQDN" --set "httpRoute.hostnames[0]=$HOST" --set oidcGate.enabled=false 2>/dev/null)"
if grep -qE 'oidc-gate-' <<<"$ng"; then echo "FAIL: gate resources rendered with oidcGate.enabled=false" >&2; echo "$ng" >&2; exit 1; fi
n2="$(echo "$ng" | grep -c 'kind: HTTPRoute' || true)"
[ "$n2" -eq 1 ] || { echo "FAIL: expected the chart's own HTTPRoute (1), got $n2" >&2; exit 1; }
grep -qE 'replaceFullPath: "/app/"' <<<"$ng" || { echo "FAIL: chart's own bare-root -> /app/ redirect missing" >&2; exit 1; }
echo "  PASS"

# ── Case 3: fail-closed — no hostname, no fqdn → no gate ─────────────────────
echo "[oidc-gate-companion] Case 3: fail-closed (no hostname, no sovereignFqdn) renders no gate"
fc="$("$helm" template agenity "$chart_dir" --set 'httpRoute.hostnames={}' 2>/dev/null)"
if grep -qE 'oidc-gate-' <<<"$fc"; then echo "FAIL: gate rendered with no hostname (must fail closed, Inviolable #4)" >&2; echo "$fc" >&2; exit 1; fi
echo "  PASS"

# ── Case 4: clientId override honoured ──────────────────────────────────────
echo "[oidc-gate-companion] Case 4: oidcGate.clientId override honoured"
co="$(render_gated --set oidcGate.clientId=agenity-custom)"
grep -qE -- '--client-id=agenity-custom' <<<"$co"        || { echo "FAIL: clientId override not applied to --client-id" >&2; exit 1; }
grep -qE 'name: "oidc-gate-agenity-custom"' <<<"$co"     || { echo "FAIL: gate name did not pick up clientId override" >&2; exit 1; }
grep -qE 'key: "sso/sovereign/agenity-custom"' <<<"$co"  || { echo "FAIL: ExternalSecret key did not pick up clientId override" >&2; exit 1; }
echo "  PASS"

echo "[bp-agenity #4553/#4556 oidc-gate companion] All cases PASS"
