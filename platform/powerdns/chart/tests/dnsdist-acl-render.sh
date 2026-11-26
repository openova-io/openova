#!/usr/bin/env bash
# bp-powerdns: dnsdist answers queries from public sources (gate check NET-G19).
#
# Without setACL dnsdist keeps its built-in access list (10.0.0.0/8,
# 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16,
# ::1/128, fc00::/7, fe80::/10). This dnsdist is the Sovereign's public name
# server: public port 53 reaches it with the client's address kept, so the
# built-in list dropped every query from the internet and NET-G19 failed
# (udp timed out, tcp was reset on read).
#
# Cases, on the default render and on the hostPort-53 DaemonSet render:
#   1. the ConfigMap dnsdist-config carries a dnsdist.conf (the extraction is
#      not empty: it holds the setLocal of port 53).
#   2. the last setACL that is not a comment (the one in effect) lists both
#      0.0.0.0/0 and ::/0.
#   3. the only backend is the authoritative server on port 5353
#      (one newServer), so the open list opens no resolver.
#   4. the predicate of case 2 fails on the same configuration with its
#      setACL removed (the check can fail).
#
# CI runs every chart/tests/*.sh as `bash <script> <chart dir>` under the yq of
# the release (v4.44.3); only plain selections and index access are used.
set -euo pipefail

chart_dir="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
helm="${HELM_BIN:-helm}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL: $1" >&2; exit 1; }

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

# effective_acl <conf> prints the last setACL line that is not a Lua comment.
effective_acl() {
  awk '/^[[:space:]]*setACL[[:space:]]*\(/ { line = $0 } END { print line }' "$1"
}

# acl_admits_public <conf> exits 0 when the setACL in effect lists 0.0.0.0/0 and ::/0.
acl_admits_public() {
  local acl
  acl="$(effective_acl "$1")"
  [ -n "$acl" ] || return 1
  grep -q -E "['\"]0\.0\.0\.0/0['\"]" <<<"$acl" || return 1
  grep -q -E "['\"]::/0['\"]" <<<"$acl" || return 1
}

check_render() {
  local label="$1"
  shift
  local render="$tmp/$label-render.yaml" conf="$tmp/$label-dnsdist.conf"
  echo "[bp-powerdns] dnsdist access list, $label render"
  "$helm" template smoke "$chart_dir" --show-only templates/dnsdist.yaml "$@" > "$render"
  yq 'select(.kind == "ConfigMap") | .data["dnsdist.conf"]' "$render" > "$conf"

  # Case 1
  grep -q -F "setLocal('0.0.0.0:53')" "$conf" \
    || fail "$label: no dnsdist.conf with the port-53 setLocal was read from the ConfigMap dnsdist-config"

  # Case 2
  if ! acl_admits_public "$conf"; then
    echo "setACL in effect: '$(effective_acl "$conf")'" >&2
    fail "$label: dnsdist keeps an access list that drops public sources (no setACL listing 0.0.0.0/0 and ::/0)"
  fi

  # Case 3
  local servers
  servers="$(grep -c -E '^[[:space:]]*newServer[[:space:]]*\(' "$conf" || true)"
  [ "$servers" = 1 ] || fail "$label: expected exactly one newServer (the authoritative backend), found $servers"
  grep -q -E "^[[:space:]]*newServer[[:space:]]*\(.*:5353.*name = 'powerdns-auth'" "$conf" \
    || fail "$label: the one backend is not the authoritative server on port 5353"

  # Case 4
  grep -v -E '^[[:space:]]*setACL[[:space:]]*\(' "$conf" > "$tmp/$label-no-acl.conf" || true
  if acl_admits_public "$tmp/$label-no-acl.conf"; then
    fail "$label: the access-list check passed on a configuration without setACL (it cannot fail)"
  fi
  echo "  ok: $(effective_acl "$conf" | sed 's/^[[:space:]]*//')"
}

check_render default
check_render hostport --set dnsdist.hostPortMode=true

echo "[bp-powerdns] dnsdist access-list render: PASS"
