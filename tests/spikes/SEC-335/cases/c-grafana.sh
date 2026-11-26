#!/bin/sh
# Upstream Grafana pod (grafana chart 10.5.15, image 12.3.1), route 1: grafana.ini reads the file by $__file{}.
. /home/openova/.cache/ns-scratch/SEC-335-tools/cases/lib.sh
D=$C/grafana
rm -rf "$D"; mkdir -p "$D/w"; chmod 0777 "$D/w"
V=$(newvalue)
putfile "$D/openova" client-secret "$V"
yq 'select(.kind=="ConfigMap" and .metadata.name=="grafana") | .data["grafana.ini"]' "$R/grafana.yaml" \
  | sed 's#^client_secret = .*#client_secret = $__file{/var/run/secrets/openova/client-secret}#' > "$D/grafana.ini"
chmod 0644 "$D/grafana.ini"
grep -q -x 'client_secret = $__file{/var/run/secrets/openova/client-secret}' "$D/grafana.ini" || { result grafana-route1 FAIL "ini edit did not apply"; exit 1; }
# Rendered plain env of the grafana container, then the values the Secret carries today that are not secret,
# pointed at the local listener: client id, the three endpoints and the root URL.
yq 'select(.kind=="Deployment" and .metadata.name=="grafana") | .spec.template.spec.containers[] | select(.name=="grafana") | .env[] | select(.value != null) | .name + "=" + .value' "$R/grafana.yaml" > "$D/env"
cat >> "$D/env" <<'EOF'
GF_AUTH_GENERIC_OAUTH_CLIENT_ID=grafana-test-client
GF_AUTH_GENERIC_OAUTH_AUTH_URL=http://127.0.0.1:8099/auth
GF_AUTH_GENERIC_OAUTH_TOKEN_URL=http://127.0.0.1:8099/token
GF_AUTH_GENERIC_OAUTH_API_URL=http://127.0.0.1:8099/userinfo
GF_SERVER_ROOT_URL=http://127.0.0.1:3000/
EOF
cat > "$D/flow.sh" <<'EOF'
#!/bin/sh
for i in $(seq 1 120); do curl -fsS -o /dev/null http://127.0.0.1:3000/api/health && break; sleep 1; done
curl -s -c /w/jar -o /dev/null -D /w/h1 http://127.0.0.1:3000/login/generic_oauth
state=$(grep -i '^location:' /w/h1 | tr -d '\r' | sed -n 's/.*[?&]state=\([^&]*\).*/\1/p')
echo "state=$state" > /w/state
curl -s -b /w/jar -c /w/jar -o /w/b2 -D /w/h2 "http://127.0.0.1:3000/login/generic_oauth?code=sec335code&state=$state"
EOF
chmod 0755 "$D/flow.sh"
run() { # $1 = with | without (the mount)
  P="sec335-grafana-$1"
  podman pod create --name "$P" --network none > /dev/null
  capture_start "$P" "$D/cap-$1"
  M=""; [ "$1" = with ] && M="-v $D/openova:/var/run/secrets/openova:ro"
  podman run -d --name "$P-grafana" --pod "$P" --env-file "$D/env" -v "$D/grafana.ini:/etc/grafana/grafana.ini:ro" $M "$IMG_GRAFANA" > /dev/null
  if [ "$1" = with ]; then
    podman run --rm --pod "$P" -v "$D/w:/w" -v "$D/flow.sh:/flow.sh:ro" "$IMG_CURL" /bin/sh /flow.sh > "$D/flow.log" 2>&1
  else
    sleep 25
  fi
  podman inspect -f '{{.State.Status}} {{.State.ExitCode}}' "$P-grafana" > "$D/$1.state" 2>/dev/null
  podman logs "$P-grafana" > "$D/$1.log" 2>&1
  podman pod rm -f "$P" > /dev/null 2>&1
}
run with
run without
F=$(grep -l -F "POST /token" "$D"/cap-with/req.* 2>/dev/null | head -n 1)
got=""
if [ -n "$F" ]; then
  b=$(grep -i '^authorization: basic ' "$F" | tr -d '\r' | awk '{print $3}')
  [ -n "$b" ] && got=$(printf '%s' "$b" | base64 -d 2>/dev/null)
  echo "token request: $F" > "$D/token-request.txt"
fi
if [ "$got" = "grafana-test-client:$V" ] || { [ -n "$F" ] && grep -q -F "client_secret=$V" "$F"; }; then
  result grafana-route1 PASS "the token request to the identity provider carried the mounted value as the client secret"
else
  result grafana-route1 FAIL "no token request carried the mounted value (state: $(cat "$D/with.state"))"
fi
if grep -q "exited [1-9]" "$D/without.state" && grep -q -F "/var/run/secrets/openova/client-secret" "$D/without.log"; then
  result grafana-control PASS "without the mount: Grafana exits non-zero and names the missing file ($(cat "$D/without.state"))"
else
  result grafana-control FAIL "without the mount: $(cat "$D/without.state")"
fi
