#!/usr/bin/env bash
# bp-oidc-gate — #3374 rootRedirectPath render audit.
#
# An app that ships its OWN OIDC (pda-legacy authlib) cannot be made
# zero-click by oauth2-proxy alone: the proxy auths the BROWSER at the gate
# then proxies path-for-path, so a bare-root hit reaches the app's `/`, whose
# unauthenticated app-session 302s to the app's OWN /login FORM (a manual
# "Sign in with…" button = zero-click FAIL, measured live hw159). The
# per-instance `rootRedirectPath` makes the gate 302 ONLY the bare root
# (Exact `/`) to the app's native OIDC login entrypoint so the app starts its
# own silent KC round-trip and lands signed-in.
#
# This test asserts:
#   1. an instance WITH rootRedirectPath renders an Exact `/` RequestRedirect
#      to that path (port 443 default) AHEAD of the PathPrefix `/` backend;
#   2. an instance WITHOUT it renders NO redirect (byte-identical to a
#      login-less gated app — only the PathPrefix `/` backend);
#   3. rootRedirectPort overrides the redirect port.

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"

vals="$(mktemp)"
trap 'rm -f "$vals"' EXIT
cat >"$vals" <<'YAML'
sovereignFQDN: smoke.example.com
realm: sovereign
instances:
  # login-less app — NO rootRedirectPath (control)
  - name: flowless
    enabled: true
    clientId: flowless
    upstream: http://flowless.default.svc.cluster.local:80
  # app with native OIDC — rootRedirectPath set
  - name: pda
    enabled: true
    clientId: pda
    hostname: pdns-admin.smoke.example.com
    upstream: http://pda.default.svc.cluster.local:80
    rootRedirectPath: /oidc/login
  # custom redirect port
  - name: customport
    enabled: true
    clientId: customport
    upstream: http://customport.default.svc.cluster.local:80
    rootRedirectPath: /sso/start
    rootRedirectPort: 8443
YAML

out="$("$helm" template oidc-gate "$chart_dir" -f "$vals" 2>/dev/null)"

# Split into per-document chunks so we can scope assertions to one HTTPRoute.
route_doc() {
  # prints the HTTPRoute document whose metadata.name == oidc-gate-<arg>
  echo "$out" | awk -v want="oidc-gate-$1" '
    BEGIN{RS="\n---\n"}
    /kind: HTTPRoute/ && $0 ~ ("name: \"?" want "\"?") {print}'
}

# ── Case 1: pda gets the Exact / -> /oidc/login redirect ────────────────────
echo "[root-redirect] Case 1: pda renders Exact / RequestRedirect to /oidc/login"
pda="$(route_doc pda)"
if [ -z "$pda" ]; then echo "FAIL: no HTTPRoute oidc-gate-pda rendered" >&2; echo "$out" >&2; exit 1; fi
grep -qE 'type:\s*Exact' <<<"$pda"                       || { echo "FAIL: pda missing Exact path match" >&2; echo "$pda" >&2; exit 1; }
grep -qE 'type:\s*RequestRedirect' <<<"$pda"             || { echo "FAIL: pda missing RequestRedirect filter" >&2; echo "$pda" >&2; exit 1; }
grep -qE 'replaceFullPath:\s*"?/oidc/login"?' <<<"$pda"  || { echo "FAIL: pda redirect not to /oidc/login" >&2; echo "$pda" >&2; exit 1; }
grep -qE 'port:\s*443' <<<"$pda"                         || { echo "FAIL: pda redirect not on default port 443" >&2; echo "$pda" >&2; exit 1; }
grep -qE 'statusCode:\s*302' <<<"$pda"                   || { echo "FAIL: pda redirect not 302" >&2; echo "$pda" >&2; exit 1; }
echo "  PASS"

# ── Case 2: flowless (no rootRedirectPath) renders NO redirect ──────────────
echo "[root-redirect] Case 2: flowless renders NO redirect (only PathPrefix backend)"
flowless="$(route_doc flowless)"
if [ -z "$flowless" ]; then echo "FAIL: no HTTPRoute oidc-gate-flowless rendered" >&2; exit 1; fi
if grep -qE 'RequestRedirect|type:\s*Exact' <<<"$flowless"; then
  echo "FAIL: flowless (no rootRedirectPath) should NOT render a redirect" >&2; echo "$flowless" >&2; exit 1
fi
grep -qE 'type:\s*PathPrefix' <<<"$flowless" || { echo "FAIL: flowless missing PathPrefix backend" >&2; exit 1; }
echo "  PASS"

# ── Case 3: rootRedirectPort override ───────────────────────────────────────
echo "[root-redirect] Case 3: customport honours rootRedirectPort: 8443"
cp="$(route_doc customport)"
grep -qE 'replaceFullPath:\s*"?/sso/start"?' <<<"$cp" || { echo "FAIL: customport redirect path wrong" >&2; echo "$cp" >&2; exit 1; }
grep -qE 'port:\s*8443' <<<"$cp"                      || { echo "FAIL: customport did not honour rootRedirectPort 8443" >&2; echo "$cp" >&2; exit 1; }
echo "  PASS"

echo "[bp-oidc-gate #3374 rootRedirectPath] All cases PASS"
