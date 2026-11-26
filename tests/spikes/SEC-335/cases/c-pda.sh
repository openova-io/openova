#!/bin/sh
# PowerDNS-Admin (bp-powerdns-admin templates/deployment.yaml, image pda-legacy 0.4.2), route 2: OIDC_OAUTH_SECRET_FILE.
. /home/openova/.cache/ns-scratch/SEC-335-tools/cases/lib.sh
D=$C/pda
rm -rf "$D"; mkdir -p "$D/w"; chmod 0777 "$D/w"
V=$(newvalue)
putfile "$D/openova" client-secret "$V"
# Rendered plain env of the powerdns-admin container. Test-only changes: SQLite in /tmp instead of the
# database binding (class T, not this entry), a port above 1024, one worker, and values for the two
# other Secrets the container reads (Flask key, PowerDNS API key; not this entry).
yq 'select(.kind=="Deployment") | .spec.template.spec.containers[] | select(.name=="powerdns-admin") | .env[] | select(.value != null) | .name + "=" + .value' "$R/powerdns-admin.yaml" \
  | grep -v '^SQLA' > "$D/env"
cat >> "$D/env" <<'EOF'
SQLALCHEMY_DATABASE_URI=sqlite:////tmp/pda.db
BIND_ADDRESS=0.0.0.0:9191
GUNICORN_WORKERS=1
SECRET_KEY=sec335-test-flask-key
PDNS_API_KEY=sec335-test-pdns-key
OIDC_OAUTH_KEY=pda-test-client
OIDC_OAUTH_SECRET_FILE=/var/run/secrets/openova/client-secret
OIDC_OAUTH_API_URL=http://127.0.0.1:8099/
OIDC_OAUTH_TOKEN_URL=http://127.0.0.1:8099/token
OIDC_OAUTH_AUTHORIZE_URL=http://127.0.0.1:8099/auth
EOF
cat > "$D/flow.sh" <<'EOF'
#!/bin/sh
for i in $(seq 1 180); do curl -fsS -o /dev/null http://127.0.0.1:9191/login && break; sleep 1; done
curl -s -c /w/jar -o /dev/null -D /w/h1 http://127.0.0.1:9191/oidc/login
state=$(grep -i '^location:' /w/h1 | tr -d '\r' | sed -n 's/.*[?&]state=\([^&]*\).*/\1/p')
echo "state=$state" > /w/state
curl -s -b /w/jar -c /w/jar -o /w/b2 -D /w/h2 "http://127.0.0.1:9191/oidc/authorized?code=sec335code&state=$state"
EOF
chmod 0755 "$D/flow.sh"
run() { # $1 = with | without (the mount)
  P="sec335-pda-$1"
  podman pod create --name "$P" --network none > /dev/null
  capture_start "$P" "$D/cap-$1"
  M=""; [ "$1" = with ] && M="-v $D/openova:/var/run/secrets/openova:ro"
  podman run -d --name "$P-pda" --pod "$P" --env-file "$D/env" $M "$IMG_PDA" > /dev/null
  if [ "$1" = with ]; then
    podman run --rm --pod "$P" -v "$D/w:/w" -v "$D/flow.sh:/flow.sh:ro" "$IMG_CURL" /bin/sh /flow.sh > "$D/flow.log" 2>&1
  else
    sleep 40
  fi
  podman inspect -f '{{.State.Status}} {{.State.ExitCode}}' "$P-pda" > "$D/$1.state" 2>/dev/null
  podman logs "$P-pda" > "$D/$1.log" 2>&1
  podman pod rm -f "$P" > /dev/null 2>&1
}
run with
run without
F=$(grep -l -F "POST /token" "$D"/cap-with/req.* 2>/dev/null | head -n 1)
got=""
if [ -n "$F" ]; then
  b=$(grep -i '^authorization: basic ' "$F" | tr -d '\r' | awk '{print $3}')
  [ -n "$b" ] && got=$(printf '%s' "$b" | base64 -d 2>/dev/null)
fi
if [ "$got" = "pda-test-client:$V" ] || { [ -n "$F" ] && grep -q -F "client_secret=$V" "$F"; }; then
  result pda-route2 PASS "the token request to the identity provider carried the mounted value as the client secret"
else
  result pda-route2 FAIL "no token request carried the mounted value (state: $(cat "$D/with.state"))"
fi
if grep -q "exited [1-9]" "$D/without.state" && grep -q -F "/var/run/secrets/openova/client-secret" "$D/without.log"; then
  result pda-control PASS "without the mount: PowerDNS-Admin exits non-zero and names the missing file ($(cat "$D/without.state"))"
else
  result pda-control FAIL "without the mount: $(cat "$D/without.state")"
fi
