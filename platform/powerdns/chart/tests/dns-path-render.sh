#!/usr/bin/env bash
# bp-powerdns: dnsdist's one backend is a DNS port of the powerdns Service, over
# UDP and over TCP (gate check NET-G19, NET-187).
#
# dnsdist forwards every query to one backend: the address of the variable
# <NAME>_SERVICE_HOST, which Kubernetes sets in every Pod of the namespace for
# the Service <name>, and a port (values.yaml dnsdist.config). A ClusterIP
# answers only on the ports its Service declares. The subchart's Service
# declares 53 (dns-udp and dns-tcp, which forward to the server's own port
# 5353) and 8081. With 5353 as the backend's port dnsdist never reached
# PowerDNS: it dropped every UDP query and closed every TCP connection, and
# NET-G19 failed in both regions (udp timed out, tcp read EOF; 27 Nov 2026).
#
# The release is named as kit slot 11 names it (spec.releaseName), so the
# Service the variable names is the one the kit installs.
#
# Cases, on the default render and on the hostPort-53 DaemonSet render:
#   1. dnsdist.conf holds one newServer, whose address is a variable set from
#      os.getenv("<NAME>_SERVICE_HOST") and a port.
#   2. the render holds the Service <name> (lower case, - for _).
#   3. that Service declares the backend's port for UDP and for TCP.
#   4. the predicate of case 3 fails on the same render with the port 5353
#      (the check can fail).
#
# CI runs every chart/tests/*.sh as `bash <script> <chart dir>` under the yq of
# the release (v4.44.3); only plain selections and index access are used.
set -euo pipefail

chart_dir="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
root="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
slot="$root/clusters/_template/bootstrap-kit/11-powerdns.yaml"
helm="${HELM_BIN:-helm}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL: $1" >&2; exit 1; }

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

release="$(yq 'select(.kind == "HelmRelease" and .metadata.name == "bp-powerdns") | .spec.releaseName' "$slot")"
case "$release" in
  '' | null) fail "$slot: no spec.releaseName on the HelmRelease bp-powerdns" ;;
esac

# serves <render> <service> <protocol> <port> exits 0 when the Service declares
# that port for that protocol, and 1 when it does not or is not in the render.
serves() {
  local sel n i
  sel="select(.kind == \"Service\" and .metadata.name == \"$2\")"
  n="$(yq "$sel | .spec.ports | length" "$1")"
  case "$n" in '' | 0) return 1 ;; esac
  for ((i = 0; i < n; i++)); do
    if [ "$(yq "$sel | .spec.ports[$i].protocol" "$1")" = "$3" ] \
      && [ "$(yq "$sel | .spec.ports[$i].port" "$1")" = "$4" ]; then
      return 0
    fi
  done
  return 1
}

check_render() {
  local label="$1"
  shift
  local render="$tmp/$label-render.yaml" conf="$tmp/$label-dnsdist.conf"
  echo "[bp-powerdns] dnsdist backend, $label render (release $release)"
  "$helm" template "$release" "$chart_dir" --namespace "$release" "$@" > "$render"
  yq 'select(.kind == "ConfigMap" and .metadata.name == "dnsdist-config") | .data["dnsdist.conf"]' "$render" > "$conf"

  # Case 1
  local lines line var port env
  lines="$(grep -c -E '^[[:space:]]*newServer[[:space:]]*\(' "$conf" || true)"
  [ "$lines" = 1 ] || fail "$label: expected exactly one newServer in dnsdist.conf, found $lines"
  line="$(grep -E '^[[:space:]]*newServer[[:space:]]*\(' "$conf")"
  var="$(sed -n -E 's/.*address = ([A-Za-z_][A-Za-z0-9_]*) \.\. "?:([0-9]+)".*/\1/p' <<<"$line")"
  port="$(sed -n -E 's/.*address = ([A-Za-z_][A-Za-z0-9_]*) \.\. "?:([0-9]+)".*/\2/p' <<<"$line")"
  if [ -z "$var" ] || [ -z "$port" ]; then
    fail "$label: the newServer address is not <variable> .. \":<port>\": $line"
  fi
  env="$(sed -n -E "s/^[[:space:]]*local $var = os\.getenv\(\"([A-Z0-9_]+)_SERVICE_HOST\"\).*/\1/p" "$conf")"
  [ -n "$env" ] || fail "$label: '$var' is not set from os.getenv(\"<NAME>_SERVICE_HOST\")"

  # Case 2
  local svc
  svc="$(tr 'A-Z_' 'a-z-' <<<"$env")"
  [ "$(yq "select(.kind == \"Service\" and .metadata.name == \"$svc\") | .metadata.name" "$render")" = "$svc" ] \
    || fail "$label: dnsdist reads ${env}_SERVICE_HOST, but the render has no Service $svc"

  # Case 3
  local proto
  for proto in UDP TCP; do
    serves "$render" "$svc" "$proto" "$port" \
      || fail "$label: the Service $svc declares no port $port/$proto: dnsdist's backend $svc:$port is never reached"
  done

  # Case 4
  if serves "$render" "$svc" UDP 5353 || serves "$render" "$svc" TCP 5353; then
    fail "$label: the port check passed for 5353, a port the Service does not declare (it cannot fail)"
  fi
  echo "  ok: backend $svc:$port, declared for UDP and TCP"
}

check_render default
check_render hostport --set dnsdist.hostPortMode=true

echo "[bp-powerdns] dnsdist backend render: PASS"
