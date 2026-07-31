#!/usr/bin/env bash
# bp-oidc-gate — #4556 spaTokenSeed render audit.
#
# An app whose SPA gates on a CLIENT-SIDE bootstrap token (localStorage),
# even when its daemon API is unauthenticated, cannot be made zero-click by
# the gate's SSO alone: agenity/chepherd runs CHEPHERD_AUTH_MODE=none (open
# API) yet the `/app/` SPA shows "Paste the bootstrap token" purely because
# localStorage['chepherd-token'] is empty (it never probes the daemon auth
# mode). The SPA DOES ingest a `?token=` URL param on mount, and any value is
# accepted because the daemon is unauthenticated. The per-instance
# `spaTokenSeed` makes the gate 302 ONLY the bare root (Exact `/`), AFTER the
# gate's SSO, to `<dashboardPath>?token=<value>` so the SPA seeds localStorage
# and lands authenticated with NO paste.
#
# This test asserts:
#   1. an instance WITH spaTokenSeed renders an Exact `/` RequestRedirect to
#      `<dashboardPath>?token=<value>` (the query carried in replaceFullPath)
#      AHEAD of the PathPrefix `/` backend;
#   2. an instance WITHOUT it renders NO redirect (only the PathPrefix
#      backend);
#   3. spaTokenSeed.port overrides the redirect port, and dashboardPath/value
#      defaults are /app/ and sso;
#   4. spaTokenSeed takes precedence over rootRedirectPath (mutual exclusivity:
#      the seed wins, no double Exact-/ rule).

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"

vals="$(mktemp)"
trap 'rm -f "$vals"' EXIT
cat >"$vals" <<'YAML'
sovereignFQDN: smoke.example.com
realm: sovereign
instances:
  # token-paste SPA — spaTokenSeed set (explicit path/value/port)
  - name: agenity-org1
    enabled: true
    clientId: agenity-org1
    hostname: agenity.org1.smoke.example.com
    upstream: http://agenity.org1.svc.cluster.local:8080
    spaTokenSeed:
      dashboardPath: /app/
      value: sso
      port: 443
  # token-paste SPA — spaTokenSeed defaults (no path/value/port)
  - name: agenity-defaults
    enabled: true
    clientId: agenity-defaults
    hostname: agenity.defaults.smoke.example.com
    upstream: http://agenity.defaults.svc.cluster.local:8080
    spaTokenSeed: {}
  # custom dashboard path + value + port
  - name: agenity-custom
    enabled: true
    clientId: agenity-custom
    hostname: agenity.custom.smoke.example.com
    upstream: http://agenity.custom.svc.cluster.local:8080
    spaTokenSeed:
      dashboardPath: /dashboard/
      value: seed42
      port: 8443
  # login-less app — NO spaTokenSeed (control)
  - name: flowless
    enabled: true
    clientId: flowless
    upstream: http://flowless.default.svc.cluster.local:80
  # spaTokenSeed precedence over rootRedirectPath
  - name: agenity-precedence
    enabled: true
    clientId: agenity-precedence
    hostname: agenity.prec.smoke.example.com
    upstream: http://agenity.prec.svc.cluster.local:8080
    rootRedirectPath: /oidc/login
    spaTokenSeed:
      value: sso
YAML

out="$("$helm" template oidc-gate "$chart_dir" -f "$vals" 2>/dev/null)"

route_doc() {
  echo "$out" | awk -v want="oidc-gate-$1" '
    BEGIN{RS="\n---\n"}
    /kind: HTTPRoute/ && $0 ~ ("name: \"?" want "\"?") {print}'
}

# ── Case 1: explicit spaTokenSeed → Exact / -> /app/?token=sso ──────────────
echo "[spa-token-seed] Case 1: agenity-org1 renders Exact / RequestRedirect to /app/?token=sso"
a1="$(route_doc agenity-org1)"
if [ -z "$a1" ]; then echo "FAIL: no HTTPRoute oidc-gate-agenity-org1 rendered" >&2; echo "$out" >&2; exit 1; fi
grep -qE 'type:\s*Exact' <<<"$a1"                                  || { echo "FAIL: missing Exact path match" >&2; echo "$a1" >&2; exit 1; }
grep -qE 'type:\s*RequestRedirect' <<<"$a1"                        || { echo "FAIL: missing RequestRedirect filter" >&2; echo "$a1" >&2; exit 1; }
grep -qE 'replaceFullPath:\s*"?/app/\?token=sso"?' <<<"$a1"        || { echo "FAIL: redirect not to /app/?token=sso" >&2; echo "$a1" >&2; exit 1; }
grep -qE 'port:\s*443' <<<"$a1"                                    || { echo "FAIL: redirect not on port 443" >&2; echo "$a1" >&2; exit 1; }
grep -qE 'statusCode:\s*302' <<<"$a1"                              || { echo "FAIL: redirect not 302" >&2; echo "$a1" >&2; exit 1; }
echo "  PASS"

# ── Case 2: defaults → /app/?token=sso on port 443 ─────────────────────────
echo "[spa-token-seed] Case 2: agenity-defaults uses dashboardPath=/app/ value=sso port=443 defaults"
a2="$(route_doc agenity-defaults)"
grep -qE 'replaceFullPath:\s*"?/app/\?token=sso"?' <<<"$a2" || { echo "FAIL: defaults not /app/?token=sso" >&2; echo "$a2" >&2; exit 1; }
grep -qE 'port:\s*443' <<<"$a2"                            || { echo "FAIL: default port not 443" >&2; echo "$a2" >&2; exit 1; }
echo "  PASS"

# ── Case 3: custom path/value/port honoured ────────────────────────────────
echo "[spa-token-seed] Case 3: agenity-custom honours /dashboard/?token=seed42 on 8443"
a3="$(route_doc agenity-custom)"
grep -qE 'replaceFullPath:\s*"?/dashboard/\?token=seed42"?' <<<"$a3" || { echo "FAIL: custom path/value wrong" >&2; echo "$a3" >&2; exit 1; }
grep -qE 'port:\s*8443' <<<"$a3"                                    || { echo "FAIL: custom port 8443 not honoured" >&2; echo "$a3" >&2; exit 1; }
echo "  PASS"

# ── Case 4: control (no spaTokenSeed) renders NO redirect ──────────────────
echo "[spa-token-seed] Case 4: flowless (no spaTokenSeed) renders NO redirect"
fl="$(route_doc flowless)"
if grep -qE 'RequestRedirect|type:\s*Exact' <<<"$fl"; then
  echo "FAIL: flowless should NOT render a redirect" >&2; echo "$fl" >&2; exit 1
fi
grep -qE 'type:\s*PathPrefix' <<<"$fl" || { echo "FAIL: flowless missing PathPrefix backend" >&2; exit 1; }
echo "  PASS"

# ── Case 5: spaTokenSeed precedence over rootRedirectPath ──────────────────
echo "[spa-token-seed] Case 5: spaTokenSeed wins over rootRedirectPath (no /oidc/login redirect)"
ap="$(route_doc agenity-precedence)"
grep -qE 'replaceFullPath:\s*"?/app/\?token=sso"?' <<<"$ap" || { echo "FAIL: precedence instance did not render the seed redirect" >&2; echo "$ap" >&2; exit 1; }
if grep -qE 'replaceFullPath:\s*"?/oidc/login"?' <<<"$ap"; then
  echo "FAIL: rootRedirectPath rule rendered alongside spaTokenSeed (must be mutually exclusive)" >&2; echo "$ap" >&2; exit 1
fi
# exactly ONE Exact-/ match in this route (no double redirect rule)
n_exact="$(echo "$ap" | grep -cE 'type:\s*Exact' || true)"
[ "$n_exact" -eq 1 ] || { echo "FAIL: expected exactly 1 Exact-/ rule, got $n_exact" >&2; echo "$ap" >&2; exit 1; }
echo "  PASS"

echo "[bp-oidc-gate #4556 spaTokenSeed] All cases PASS"
