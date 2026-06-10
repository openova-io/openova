#!/usr/bin/env bash
# bp-powerdns — HTTPRoute root-redirect render audit (#3225).
#
# A bare GET / on pdns.<fqdn> previously fell through unmatched (the only
# rule matched PathPrefix /api → powerdns:8081) and returned a 404 in the
# founder's browser. The PowerDNS API host serves no human UI; the UI lives
# at pdns-admin.<fqdn> (bp-powerdns-admin). This test pins the path-scoped
# root redirect that lands the browser on the UI instead of a 404.
#
# Cases:
#   1. With api.uiRedirectHost set, the HTTPRoute carries a second rule that
#      matches PathPrefix / and emits a RequestRedirect to that host (302).
#   2. The existing PathPrefix /api → powerdns backend forward rule is
#      PRESERVED (the redirect must not displace API routing — Gateway API
#      picks the more-specific /api match over the catch-all /).
#   3. Default-off: with api.uiRedirectHost unset/empty, NO RequestRedirect
#      filter renders (zero regression for clusters that don't set it).

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"
"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

redirect_host="pdns-admin.example.com"

overlay=(
  --set api.gateway.enabled=true
  --set api.host=pdns.example.com
  --set api.uiRedirectHost="$redirect_host"
)

echo "[bp-powerdns] Case 1: root → UI RequestRedirect renders"
out=$("$helm" template smoke "$chart_dir" "${overlay[@]}" 2>&1)
route_block=$(echo "$out" | awk '/^---$/{f=0} /^kind: HTTPRoute$/{f=1} f')
if [ -z "$route_block" ]; then
  echo "FAIL: HTTPRoute did not render with api.gateway.enabled=true"
  exit 1
fi
if ! echo "$route_block" | grep -q "type: RequestRedirect"; then
  echo "FAIL: no RequestRedirect filter for root path"
  exit 1
fi
if ! echo "$route_block" | grep -Eq "hostname: \"?$redirect_host\"?"; then
  echo "FAIL: RequestRedirect hostname != $redirect_host"
  exit 1
fi
if ! echo "$route_block" | grep -q "statusCode: 302"; then
  echo "FAIL: RequestRedirect statusCode != 302"
  exit 1
fi
# The redirect must match the catch-all root prefix.
if ! echo "$route_block" | grep -Eq 'value: "?/"?'; then
  echo "FAIL: redirect rule does not match PathPrefix /"
  exit 1
fi
echo "[bp-powerdns] Case 1: PASS"

echo "[bp-powerdns] Case 2: /api → powerdns backend forward rule PRESERVED"
if ! echo "$route_block" | grep -Eq 'value: "?/api"?'; then
  echo "FAIL: /api match value not present — API forward rule lost"
  exit 1
fi
if ! echo "$route_block" | grep -q "name: powerdns"; then
  echo "FAIL: powerdns backendRef lost — API no longer routed"
  exit 1
fi
echo "[bp-powerdns] Case 2: PASS"

echo "[bp-powerdns] Case 3: default-off — no RequestRedirect"
out_default=$("$helm" template smoke "$chart_dir" \
  --set api.gateway.enabled=true \
  --set api.host=pdns.example.com 2>&1)
default_route=$(echo "$out_default" | awk '/^---$/{f=0} /^kind: HTTPRoute$/{f=1} f')
if echo "$default_route" | grep -q "type: RequestRedirect"; then
  echo "FAIL: RequestRedirect rendered without api.uiRedirectHost set"
  exit 1
fi
# The API forward rule must still be present in the default case.
if ! echo "$default_route" | grep -q "name: powerdns"; then
  echo "FAIL: powerdns backendRef missing in default render"
  exit 1
fi
echo "[bp-powerdns] Case 3: PASS"

echo "[bp-powerdns] All HTTPRoute UI-redirect cases PASS"
