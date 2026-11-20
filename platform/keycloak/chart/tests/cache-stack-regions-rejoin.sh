#!/usr/bin/env bash
# bp-keycloak — the regions' Keycloaks rejoin after one of them restarts.
#
# Guards the split measured on the fresh two-region install of 2026-11-19/20
# (FND-G07 FAIL): a drain moved region a's Keycloak and the shared PostgreSQL
# in the same minute; Keycloak 26.3.3's KEYCLOAK_JDBC_PING2 threw a
# RuntimeException out of region b's view change, MERGE3 never restarted, and
# the two regions stayed two clusters of one ("already used" login codes)
# until a person restarted a Pod. Cause and stack:
# templates/configmap-cache-ispn.yaml.
#
# Cases:
#   1. the env ConfigMap sets no KC_CACHE_STACK (it would override the file's
#      stack and bring KEYCLOAK_JDBC_PING2 back) and points
#      KC_CACHE_CONFIG_FILE at the shipped file
#   2. the cache ConfigMap renders and carries that file
#   3. the transport uses a stack that extends `tcp` (MERGE3, FD_ALL3, FD_SOCK2
#      inherited, none removed) with JGroups' own JDBC_PING2 in MPING's place:
#      not KEYCLOAK_JDBC_PING2, rows not in the view are removed, a
#      coordinator rewrites its own row on discovery, bounded DB timeouts,
#      credentials only from the environment
#   4. every cache of Keycloak 26.3.3's default file is still defined
#   5. the StatefulSet mounts the ConfigMap where Keycloak resolves the file
#      (<home>/conf/<KC_CACHE_CONFIG_FILE>) and hashes it in a pod annotation
#   6. the image is still 26.3.3 (the file is that version's default; a new
#      image must re-base it and re-check upstream keycloak/keycloak#45980)
#
# On main this fails at case 1 (KC_CACHE_STACK: "jdbc-ping").

set -euo pipefail

chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm="${HELM_BIN:-helm}"

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

"$helm" template keycloak "$chart_dir" \
  --namespace keycloak \
  --set gateway.host=auth.smoke.omani.works \
  --set sovereignFQDN=smoke.omani.works > "$tmp/out.yaml"

fail() {
  echo "FAIL: $*"
  exit 1
}

# doc KIND NAME — print the rendered document of that kind and name.
doc() {
  awk -v k="$1" -v n="$2" '
    function flush() { if (hk && hn) printf "%s", buf; buf = ""; hk = 0; hn = 0 }
    /^---/ { flush(); next }
    { buf = buf $0 "\n" }
    $0 == "kind: " k { hk = 1 }
    $0 == "  name: " n { hn = 1 }
    END { flush() }
  ' "$tmp/out.yaml"
}

# ── Case 1 ───────────────────────────────────────────────────────────────
echo "[bp-keycloak] Case 1: no KC_CACHE_STACK, KC_CACHE_CONFIG_FILE names the shipped file"
env_cm="$(doc ConfigMap keycloak-env-vars)"
[ -n "$env_cm" ] || fail "no ConfigMap keycloak-env-vars rendered"
grep -q -x '  KC_CACHE: "ispn"' <<<"$env_cm" || fail "KC_CACHE is not ispn: the regions would not cluster at all"
if grep -q '^  KC_CACHE_STACK:' <<<"$env_cm"; then
  fail "KC_CACHE_STACK is set ($(grep '^  KC_CACHE_STACK:' <<<"$env_cm")); it overrides the file's stack and brings back KEYCLOAK_JDBC_PING2, whose RuntimeException on a database outage split the regions on 2026-11-20"
fi
cfg="$(sed -n 's/^  KC_CACHE_CONFIG_FILE: "\(.*\)"$/\1/p' <<<"$env_cm")"
[ "$cfg" = "cluster/cache-ispn.xml" ] || fail "KC_CACHE_CONFIG_FILE is '${cfg}', want 'cluster/cache-ispn.xml'"
echo "[bp-keycloak] Case 1: PASS"

# ── Case 2 ───────────────────────────────────────────────────────────────
echo "[bp-keycloak] Case 2: the cache ConfigMap carries $(basename "$cfg")"
cache_cm="$(doc ConfigMap keycloak-cache-ispn)"
[ -n "$cache_cm" ] || fail "no ConfigMap keycloak-cache-ispn rendered"
grep -q -x "  $(basename "$cfg"): |" <<<"$cache_cm" || fail "ConfigMap keycloak-cache-ispn has no data key $(basename "$cfg")"
echo "[bp-keycloak] Case 2: PASS"

# ── Case 3 ───────────────────────────────────────────────────────────────
echo "[bp-keycloak] Case 3: the transport stack extends tcp and discovers with JGroups' JDBC_PING2"
stack="$(sed -n 's/.*<transport [^>]*stack="\([^"]*\)".*/\1/p' <<<"$cache_cm")"
[ -n "$stack" ] || fail "the transport names no stack; Keycloak would default to jdbc-ping (KEYCLOAK_JDBC_PING2)"
[ "$stack" != "jdbc-ping" ] || fail "the transport names the stack jdbc-ping, Keycloak's KEYCLOAK_JDBC_PING2"
grep -q "<stack name=\"${stack}\" extends=\"tcp\">" <<<"$cache_cm" || fail "no <stack name=\"${stack}\" extends=\"tcp\"> in the file"
if grep -q 'KEYCLOAK_JDBC_PING2' <<<"$cache_cm"; then
  fail "the file uses KEYCLOAK_JDBC_PING2"
fi
if grep -q 'stack.combine="REMOVE"' <<<"$cache_cm"; then
  fail "the stack removes a protocol of tcp; MERGE3 and the failure detectors must stay"
fi
ping="$(awk '/<JDBC_PING2 /{on=1} on{print} on && /\/>/{on=0}' <<<"$cache_cm")"
[ -n "$ping" ] || fail "no JDBC_PING2 protocol in the stack"
for want in \
  'stack.combine="REPLACE"' \
  'stack.position="MPING"' \
  'connection_driver="org.postgresql.Driver"' \
  'connection_username="${env.KC_DB_USERNAME}"' \
  'connection_password="${env.KC_DB_PASSWORD}"' \
  'remove_all_data_on_view_change="true"' \
  'write_data_on_find="true"' \
  'select_all_pingdata_sql="SELECT address, name, ip, coord FROM JGROUPS_PING WHERE cluster_name=?"'; do
  grep -q -F "$want" <<<"$ping" || fail "JDBC_PING2 lacks ${want}"
done
url="$(sed -n 's/.*connection_url="\([^"]*\)".*/\1/p' <<<"$ping")"
case "$url" in
  '${env.KC_DB_URL}'*socketTimeout=*) ;;
  *) fail "connection_url '${url}' is not Keycloak's own KC_DB_URL with a bounded socketTimeout" ;;
esac
echo "[bp-keycloak] Case 3: PASS (stack ${stack})"

# ── Case 4 ───────────────────────────────────────────────────────────────
echo "[bp-keycloak] Case 4: every cache of the Keycloak 26.3.3 default file is defined"
for c in realms users sessions authenticationSessions offlineSessions clientSessions \
  offlineClientSessions loginFailures authorization work keys crl actionTokens; do
  grep -q -E "<(local|distributed|replicated)-cache name=\"${c}\"" <<<"$cache_cm" || fail "cache ${c} is missing from the file"
done
echo "[bp-keycloak] Case 4: PASS"

# ── Case 5 ───────────────────────────────────────────────────────────────
echo "[bp-keycloak] Case 5: the StatefulSet mounts the file where Keycloak resolves it"
sts="$(doc StatefulSet keycloak)"
[ -n "$sts" ] || fail "no StatefulSet keycloak rendered"
mount_dir="/opt/bitnami/keycloak/conf/$(dirname "$cfg")"
# toYaml sorts keys, so `name: cache-ispn` closes both entries: the mount
# (`- mountPath:` above it) and the volume (`- configMap:` + its name above it).
entries="$(grep -B2 -E '^ +name: cache-ispn$' <<<"$sts" || true)"
grep -q -E "^ +- mountPath: ${mount_dir}$" <<<"$entries" || fail "no volumeMount cache-ispn at ${mount_dir}"
grep -q -E "^ +name: '?keycloak-cache-ispn'?$" <<<"$entries" || fail "volume cache-ispn does not reference ConfigMap keycloak-cache-ispn"
grep -q -E "checksum/cache-ispn: '?[0-9a-f]{64}'?$" <<<"$sts" || fail "no checksum/cache-ispn pod annotation; an edit of the file would not restart Keycloak"
echo "[bp-keycloak] Case 5: PASS"

# ── Case 6 ───────────────────────────────────────────────────────────────
echo "[bp-keycloak] Case 6: the image is the 26.3.3 the file is copied from"
grep -q -E 'image: [^ ]*/keycloak:26\.3\.3-' <<<"$sts" || fail "the Keycloak image is no longer 26.3.3: re-base templates/configmap-cache-ispn.yaml on the new image's conf/cache-ispn.xml and check whether it carries keycloak/keycloak#45980, then update this case"
echo "[bp-keycloak] Case 6: PASS"

echo "[bp-keycloak] cache-stack-regions-rejoin: all cases PASS"
