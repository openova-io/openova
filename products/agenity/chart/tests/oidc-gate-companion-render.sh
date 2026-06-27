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
#        cookie Secret, AppRegistration ConfigMap, ExternalSecret, HTTPRoute);
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

echo "$g" | grep -qE 'name: "oidc-gate-agenity-agnstar"'                  || { echo "FAIL: no gate Deployment/Service/HTTPRoute oidc-gate-agenity-agnstar" >&2; echo "$g" >&2; exit 1; }
echo "$g" | grep -qE 'kind: Deployment'                                   || { echo "FAIL: no gate Deployment" >&2; exit 1; }
echo "$g" | grep -qE 'name: "oidc-gate-agenity-agnstar-sso-registration"' || { echo "FAIL: no AppRegistration ConfigMap" >&2; exit 1; }
echo "$g" | grep -qE 'sso.openova.io/app-registration: "agenity-agnstar"' || { echo "FAIL: AppRegistration not labelled for bp-sso-bridge" >&2; exit 1; }
echo "$g" | grep -qE 'kind: ExternalSecret'                               || { echo "FAIL: no client-secret ExternalSecret (with external-secrets API)" >&2; exit 1; }
echo "$g" | grep -qE 'key: "sso/sovereign/agenity-agnstar"'               || { echo "FAIL: ExternalSecret remoteRef not sso/sovereign/agenity-agnstar" >&2; exit 1; }
echo "$g" | grep -qE 'name: "oidc-gate-agenity-agnstar-cookie"'           || { echo "FAIL: no gate cookie Secret" >&2; exit 1; }
echo "$g" | grep -qE -- '--client-id=agenity-agnstar'                     || { echo "FAIL: oauth2-proxy --client-id not agenity-agnstar" >&2; exit 1; }
echo "$g" | grep -qE -- '--upstream=http://agenity-bp-agenity\..*\.svc\.cluster\.local:8080' || { echo "FAIL: gate --upstream not the agenity Service" >&2; echo "$g" >&2; exit 1; }
echo "$g" | grep -qE -- '--login-url=https://auth\.agnstar\.omani\.homes/realms/sovereign.*kc_idp_hint=catalyst-pin' || { echo "FAIL: silent SSO login-url/kc_idp_hint missing" >&2; exit 1; }
echo "  PASS — gate resource set rendered"

# spaTokenSeed redirect present (Exact / -> /app/?token=sso 302)
echo "$g" | grep -qE 'replaceFullPath: "/app/\?token=sso"' || { echo "FAIL: missing spaTokenSeed /app/?token=sso redirect" >&2; echo "$g" >&2; exit 1; }
echo "$g" | grep -qE 'statusCode: 302'                     || { echo "FAIL: spaTokenSeed redirect not 302" >&2; exit 1; }
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
echo "$g" | grep -qE 'name: "oidc-gate-agenity-agnstar-gateway-ingress"' || { echo "FAIL: no gate gateway-ingress CiliumNetworkPolicy" >&2; exit 1; }
echo "$g" | grep -qE 'kind: CiliumNetworkPolicy'                          || { echo "FAIL: no CiliumNetworkPolicy" >&2; exit 1; }
echo "$g" | grep -qE 'port: "4180"'                                       || { echo "FAIL: gate CNP not admitting port 4180" >&2; exit 1; }
echo "  PASS — gateway-entity CNP present"

# ── Case 2: oidcGate.enabled=false → chart's own route, NO gate ──────────────
echo "[oidc-gate-companion] Case 2: oidcGate.enabled=false renders the chart's own route, NO gate"
ng="$("$helm" template agenity "$chart_dir" --set "sovereignFqdn=$FQDN" --set "httpRoute.hostnames[0]=$HOST" --set oidcGate.enabled=false 2>/dev/null)"
if echo "$ng" | grep -qE 'oidc-gate-'; then echo "FAIL: gate resources rendered with oidcGate.enabled=false" >&2; echo "$ng" >&2; exit 1; fi
n2="$(echo "$ng" | grep -c 'kind: HTTPRoute' || true)"
[ "$n2" -eq 1 ] || { echo "FAIL: expected the chart's own HTTPRoute (1), got $n2" >&2; exit 1; }
echo "$ng" | grep -qE 'replaceFullPath: "/app/"' || { echo "FAIL: chart's own bare-root -> /app/ redirect missing" >&2; exit 1; }
echo "  PASS"

# ── Case 3: fail-closed — no hostname, no fqdn → no gate ─────────────────────
echo "[oidc-gate-companion] Case 3: fail-closed (no hostname, no sovereignFqdn) renders no gate"
fc="$("$helm" template agenity "$chart_dir" --set 'httpRoute.hostnames={}' 2>/dev/null)"
if echo "$fc" | grep -qE 'oidc-gate-'; then echo "FAIL: gate rendered with no hostname (must fail closed, Inviolable #4)" >&2; echo "$fc" >&2; exit 1; fi
echo "  PASS"

# ── Case 4: clientId override honoured ──────────────────────────────────────
echo "[oidc-gate-companion] Case 4: oidcGate.clientId override honoured"
co="$(render_gated --set oidcGate.clientId=agenity-custom)"
echo "$co" | grep -qE -- '--client-id=agenity-custom'        || { echo "FAIL: clientId override not applied to --client-id" >&2; exit 1; }
echo "$co" | grep -qE 'name: "oidc-gate-agenity-custom"'     || { echo "FAIL: gate name did not pick up clientId override" >&2; exit 1; }
echo "$co" | grep -qE 'key: "sso/sovereign/agenity-custom"'  || { echo "FAIL: ExternalSecret key did not pick up clientId override" >&2; exit 1; }
echo "  PASS"

echo "[bp-agenity #4553/#4556 oidc-gate companion] All cases PASS"
