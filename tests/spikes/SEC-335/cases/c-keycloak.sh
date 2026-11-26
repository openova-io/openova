#!/bin/sh
# Upstream Keycloak pod (bitnami keycloak chart 25.2.0, image 26.3.3) on route 2, and the chart's
# keycloak-config-cli hook container (image 6.4.0) on route 1 (Spring Boot configtree).
. /home/openova/.cache/ns-scratch/SEC-335-tools/cases/lib.sh
D=$C/keycloak
rm -rf "$D"; mkdir -p "$D/w" "$D/config"; chmod 0777 "$D/w"
V=$(newvalue)
DBPW=$(newvalue)
putfile "$D/openova" admin-password "$V"
putfile "$D/openova" keycloak.password "$V"
# The database password stays on its own projected path (Secret keycloak-postgresql, class T, not this entry).
putfile "$D/kc-secrets" db-password "$DBPW"
printf '%s' '{"realm":"sec335","enabled":true}' > "$D/config/realm.json"; chmod 0644 "$D/config/realm.json"
# Rendered env ConfigMap of the StatefulSet. Change: the admin password file is the mounted path (route 2).
# Test-only: the database is a PostgreSQL in the same pod, and the hostname is not strict (the frontend env
# ConfigMap renders empty in this render). The rendered cache file (JDBC_PING2 discovery) is mounted as rendered.
yq 'select(.kind=="ConfigMap" and .metadata.name=="keycloak-env-vars") | .data | to_entries | .[] | .key + "=" + .value' "$R/keycloak.yaml" \
  | sed 's#^KC_BOOTSTRAP_ADMIN_PASSWORD_FILE=.*#KC_BOOTSTRAP_ADMIN_PASSWORD_FILE=/var/run/secrets/openova/admin-password#; s#^KC_DB_URL=.*#KC_DB_URL=jdbc:postgresql://127.0.0.1:5432/keycloak?currentSchema=public#' > "$D/kc.env"
echo "KC_HOSTNAME_STRICT=false" >> "$D/kc.env"
mkdir -p "$D/cluster"
yq 'select(.kind=="ConfigMap" and .metadata.name=="keycloak-cache-ispn") | .data["cache-ispn.xml"]' "$R/keycloak.yaml" > "$D/cluster/cache-ispn.xml"
chmod 0644 "$D/cluster/cache-ispn.xml"
grep -q -x 'KC_BOOTSTRAP_ADMIN_PASSWORD_FILE=/var/run/secrets/openova/admin-password' "$D/kc.env" || { result keycloak-route2 FAIL "env edit did not apply"; exit 1; }
# The hook Job's container as rendered, without its KEYCLOAK_PASSWORD secretKeyRef; KEYCLOAK_URL points into the pod.
J='select(.kind=="Job" and .metadata.name=="keycloak-keycloak-config-cli") | .spec.template.spec.containers[0]'
yq "$J | .env[] | select(.value != null) | .name + \"=\" + .value" "$R/keycloak.yaml" \
  | sed 's#^KEYCLOAK_URL=.*#KEYCLOAK_URL=http://127.0.0.1:8080/#' > "$D/cli.env"
yq "$J | .args[0]" "$R/keycloak.yaml" > "$D/cli-args.sh"
CMD=$(yq -o=json -I=0 "$J | .command" "$R/keycloak.yaml")
cat > "$D/check.sh" <<'EOF'
#!/bin/sh
# $1 = password to try. Prints the HTTP code of a master-realm admin login.
curl -s -o /w/token.json -w '%{http_code}' -d grant_type=password -d client_id=admin-cli -d username=admin --data-urlencode "password=$1" http://127.0.0.1:8080/realms/master/protocol/openid-connect/token
EOF
cat > "$D/wait.sh" <<'EOF'
#!/bin/sh
for i in $(seq 1 240); do curl -fsS -o /dev/null http://127.0.0.1:8080/realms/master && exit 0; sleep 1; done; exit 1
EOF
cat > "$D/realm.sh" <<'EOF'
#!/bin/sh
# $1 = admin password. Prints the HTTP code of GET /admin/realms/sec335.
curl -s -o /w/token2.json -d grant_type=password -d client_id=admin-cli -d username=admin --data-urlencode "password=$1" http://127.0.0.1:8080/realms/master/protocol/openid-connect/token
t=$(sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p' /w/token2.json)
curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $t" http://127.0.0.1:8080/admin/realms/sec335
EOF
chmod 0755 "$D/check.sh" "$D/wait.sh" "$D/realm.sh"
P=sec335-keycloak
podman pod create --name "$P" --network none > /dev/null
podman run -d --name "$P-pg" --pod "$P" -e POSTGRES_USER=keycloak -e POSTGRES_DB=keycloak -e "POSTGRES_PASSWORD=$DBPW" "$IMG_PG" > /dev/null
podman run -d --name "$P-kc" --pod "$P" --env-file "$D/kc.env" -v "$D/openova:/var/run/secrets/openova:ro" -v "$D/kc-secrets:/opt/bitnami/keycloak/secrets:ro" -v "$D/cluster:/opt/bitnami/keycloak/conf/cluster:ro" "$IMG_KC" > /dev/null
podman run --rm --pod "$P" -v "$D/w:/w" -v "$D/wait.sh:/wait.sh:ro" "$IMG_CURL" /bin/sh /wait.sh > /dev/null 2>&1; echo "wait=$?" > "$D/wait.txt"
good=$(podman run --rm --pod "$P" -v "$D/w:/w" -v "$D/check.sh:/check.sh:ro" "$IMG_CURL" /bin/sh /check.sh "$V" 2>/dev/null)
bad=$(podman run --rm --pod "$P" -v "$D/w:/w" -v "$D/check.sh:/check.sh:ro" "$IMG_CURL" /bin/sh /check.sh "not-the-$V" 2>/dev/null)
echo "good=$good bad=$bad" > "$D/login.txt"
if [ "$good" = 200 ] && [ "$bad" = 401 ]; then
  result keycloak-route2 PASS "admin login with the mounted value 200, with another value 401"
else
  result keycloak-route2 FAIL "admin login with the mounted value $good, with another value $bad"
fi
# config-cli control first: no configtree import, so no password reaches it.
podman run --rm --name "$P-cli-without" --pod "$P" --env-file "$D/cli.env" -e KEYCLOAK_AVAILABILITYCHECK_TIMEOUT=60s -v "$D/config:/config:ro" -v "$D/openova:/var/run/secrets/openova:ro" \
  --entrypoint "$CMD" "$IMG_KCCLI" "$(cat "$D/cli-args.sh")" > "$D/cli-without.log" 2>&1; echo "$?" > "$D/cli-without.rc"
r0=$(podman run --rm --pod "$P" -v "$D/w:/w" -v "$D/realm.sh:/realm.sh:ro" "$IMG_CURL" /bin/sh /realm.sh "$V" 2>/dev/null)
podman run --rm --name "$P-cli-with" --pod "$P" --env-file "$D/cli.env" -e KEYCLOAK_AVAILABILITYCHECK_TIMEOUT=60s -e SPRING_CONFIG_IMPORT=configtree:/var/run/secrets/openova/ -v "$D/config:/config:ro" -v "$D/openova:/var/run/secrets/openova:ro" \
  --entrypoint "$CMD" "$IMG_KCCLI" "$(cat "$D/cli-args.sh")" > "$D/cli-with.log" 2>&1; echo "$?" > "$D/cli-with.rc"
r1=$(podman run --rm --pod "$P" -v "$D/w:/w" -v "$D/realm.sh:/realm.sh:ro" "$IMG_CURL" /bin/sh /realm.sh "$V" 2>/dev/null)
echo "realm before=$r0 after=$r1 rc-without=$(cat "$D/cli-without.rc") rc-with=$(cat "$D/cli-with.rc")" > "$D/cli.txt"
if [ "$(cat "$D/cli-with.rc")" = 0 ] && [ "$r1" = 200 ]; then
  result keycloak-config-cli-route1 PASS "exit 0 and realm sec335 imported (GET 200) with the password from the configtree file"
else
  result keycloak-config-cli-route1 FAIL "$(cat "$D/cli.txt")"
fi
if [ "$(cat "$D/cli-without.rc")" != 0 ] && [ "$r0" = 404 ]; then
  result keycloak-config-cli-control PASS "without the configtree import: exit $(cat "$D/cli-without.rc"), realm absent (GET 404)"
else
  result keycloak-config-cli-control FAIL "$(cat "$D/cli.txt")"
fi
podman logs "$P-kc" > "$D/kc.log" 2>&1
podman pod rm -f "$P" > /dev/null 2>&1
