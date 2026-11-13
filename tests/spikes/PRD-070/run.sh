#!/usr/bin/env bash
# Spike PRD-070: one arm. Starts a test realm (Keycloak, HTTPS), MongoDB, the
# stub endpoint and one LibreChat version on a private docker network, signs
# two users in through the realm with a real browser, sends messages, and
# analyses what the stub received.
# Usage: run.sh <label> <librechat-image> <config-version> <keycloak-version> <openid-reuse-tokens>
# Env: OUT (output dir), MONGO_IMAGE, STUB_IMAGE, PW_DIR (playwright install), EXPIRY_WAIT
set -euo pipefail
LABEL=$1 IMAGE=$2 CONFIG_VERSION=$3 KC_VERSION=$4 REUSE=$5
TAG=${IMAGE##*:}
HERE=$(cd "$(dirname "$0")" && pwd)
KC_IMAGE=quay.io/keycloak/keycloak:$KC_VERSION
ISSUER=https://kc.spike.test:8443/realms/spike
CLIENT_VALUE=spike-prd-070-throwaway-client-value
mkdir -p "$OUT/certs" "$OUT/cfg" "$OUT/shots"
redact() { sed -E 's/eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+/<jwt>/g'; }

echo "== certificates for the test realm (throwaway CA made in this job)"
cd "$OUT/certs"
openssl req -x509 -newkey rsa:2048 -nodes -keyout ca.key -out ca.pem -days 2 -subj "/CN=spike-prd-070-ca" 2>/dev/null
openssl req -newkey rsa:2048 -nodes -keyout tls.key -out tls.csr -subj "/CN=kc.spike.test" 2>/dev/null
printf 'subjectAltName=DNS:kc.spike.test,DNS:localhost\nbasicConstraints=CA:FALSE\nkeyUsage=digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\n' > ext.cnf
openssl x509 -req -in tls.csr -CA ca.pem -CAkey ca.key -CAcreateserial -out tls.pem -days 2 -extfile ext.cnf 2>/dev/null
chmod 644 ./*
cd - >/dev/null
echo "127.0.0.1 kc.spike.test" | sudo tee -a /etc/hosts >/dev/null

echo "== pull images"
for img in "$KC_IMAGE" "$MONGO_IMAGE" "$STUB_IMAGE" "$IMAGE"; do docker pull -q "$img"; done
for img in "$KC_IMAGE" "$MONGO_IMAGE" "$STUB_IMAGE" "$IMAGE"; do
  echo "image $img -> $(docker image inspect --format '{{index .RepoDigests 0}}' "$img")"
done
LC_DIGEST=$(docker image inspect --format '{{index .RepoDigests 0}}' "$IMAGE")
docker run --rm --entrypoint node "$IMAGE" --version | sed 's/^/librechat node: /'
docker run --rm --entrypoint sh "$IMAGE" -c 'grep -m1 "\"version\"" /app/package.json || true' | sed 's/^/librechat package.json: /'
# Does the shipped server code know the user-token placeholders and the reuse flag at all?
# resolveHeaders is a positive control: both versions have that function.
for needle in resolveHeaders LIBRECHAT_OPENID LIBRECHAT_USER_ OPENID_REUSE_TOKENS federatedTokens; do
  n=$(docker run --rm --entrypoint sh "$IMAGE" -c \
    "find /app/api /app/packages -path '*/node_modules' -prune -o -type f -print 2>/dev/null | xargs grep -l '$needle' 2>/dev/null | wc -l")
  echo "librechat image: files under /app/api and /app/packages (outside node_modules) that mention $needle: $n"
done

docker network create spike >/dev/null

echo "== test realm: Keycloak $KC_VERSION over HTTPS, access-token lifespan ${TOKEN_LIFESPAN:-90}s"
jq --argjson l "${TOKEN_LIFESPAN:-90}" '.accessTokenLifespan = $l' "$HERE/realm.json" > "$OUT/realm.json"
chmod 644 "$OUT/realm.json"
docker run -d --name kc --network spike --network-alias kc.spike.test -p 8443:8443 \
  -e KC_BOOTSTRAP_ADMIN_USERNAME=admin -e KC_BOOTSTRAP_ADMIN_PASSWORD=admin \
  -e KC_HTTPS_CERTIFICATE_FILE=/certs/tls.pem -e KC_HTTPS_CERTIFICATE_KEY_FILE=/certs/tls.key \
  -v "$OUT/certs:/certs:ro" -v "$OUT/realm.json:/opt/keycloak/data/import/realm.json:ro" \
  "$KC_IMAGE" start-dev --import-realm >/dev/null
for i in $(seq 1 90); do
  if curl -sf --cacert "$OUT/certs/ca.pem" "$ISSUER/.well-known/openid-configuration" -o "$OUT/discovery.json"; then
    echo "realm ready after ~$((i * 2))s; issuer $(jq -r .issuer "$OUT/discovery.json")"; break
  fi
  if [ "$i" = 90 ]; then docker logs kc 2>&1 | tail -50; echo "realm did not start"; exit 1; fi
  sleep 2
done

echo "== MongoDB and the stub endpoint"
docker run -d --name mongo --network spike "$MONGO_IMAGE" >/dev/null
touch "$OUT/captured.jsonl"; chmod 666 "$OUT/captured.jsonl"
docker run -d --name stub --network spike -v "$HERE/stub.py:/stub.py:ro" -v "$OUT:/out" \
  "$STUB_IMAGE" python -u /stub.py 9099 /out/captured.jsonl >/dev/null
for i in $(seq 1 60); do
  if docker exec mongo mongosh --quiet --eval 'db.runCommand({ping:1}).ok' 2>/dev/null | grep -q 1; then echo "mongo ready"; break; fi
  if [ "$i" = 60 ]; then echo "mongo did not start"; exit 1; fi
  sleep 2
done

echo "== LibreChat $TAG"
sed "s/__CONFIG_VERSION__/$CONFIG_VERSION/" "$HERE/librechat.yaml" > "$OUT/cfg/librechat.yaml"
docker run -d --name librechat --network spike -p 3080:3080 \
  -e HOST=0.0.0.0 -e PORT=3080 -e NODE_ENV=production \
  -e MONGO_URI=mongodb://mongo:27017/librechat \
  -e DOMAIN_CLIENT=http://localhost:3080 -e DOMAIN_SERVER=http://localhost:3080 \
  -e CREDS_KEY="$(openssl rand -hex 32)" -e CREDS_IV="$(openssl rand -hex 16)" \
  -e JWT_SECRET="$(openssl rand -hex 32)" -e JWT_REFRESH_SECRET="$(openssl rand -hex 32)" \
  -e ALLOW_EMAIL_LOGIN=true -e ALLOW_REGISTRATION=false \
  -e ALLOW_SOCIAL_LOGIN=true -e ALLOW_SOCIAL_REGISTRATION=true \
  -e OPENID_ISSUER="$ISSUER" -e OPENID_CLIENT_ID=librechat -e OPENID_CLIENT_SECRET="$CLIENT_VALUE" \
  -e OPENID_SESSION_SECRET="$(openssl rand -hex 32)" -e OPENID_SCOPE="openid profile email" \
  -e OPENID_CALLBACK_URL=/oauth/openid/callback -e OPENID_BUTTON_LABEL="Test realm" \
  -e OPENID_REUSE_TOKENS="$REUSE" \
  -e NODE_EXTRA_CA_CERTS=/certs/ca.pem -e CONFIG_PATH=/spike/librechat.yaml \
  -e SEARCH=false -e DEBUG_LOGGING=true -e DEBUG_CONSOLE=false \
  -v "$OUT/certs:/certs:ro" -v "$OUT/cfg:/spike:ro" "$IMAGE" >/dev/null
for i in $(seq 1 150); do
  if curl -sf http://localhost:3080/api/config -o "$OUT/api-config.json"; then
    echo "librechat ready after ~$((i * 2))s"; break
  fi
  if [ "$i" = 150 ]; then docker logs librechat 2>&1 | redact | tail -80; echo "librechat did not start"; exit 1; fi
  sleep 2
done
jq -c '{openidLoginEnabled, socialLogins, socialLoginEnabled, emailLoginEnabled, modelSpecs: (.modelSpecs.list // [] | map(.name))}' "$OUT/api-config.json" \
  | sed 's/^/librechat \/api\/config: /'
docker logs librechat 2>&1 | redact | grep -iE "custom config|config file|invalid|openid|error" | head -30 || true

echo "== browser walk (two users, test realm sign-in)"
cp "$HERE/walk.mjs" "$PW_DIR/walk.mjs"
(cd "$PW_DIR" && node walk.mjs http://localhost:3080 "$OUT/captured.jsonl" "$OUT/shots" "$EXPIRY_WAIT") || echo "walk exited non-zero"

echo "== what the stub printed"
docker logs stub 2>&1 | redact

echo "== LibreChat log lines about sign-in, refresh, tokens and errors"
docker logs librechat > "$OUT/librechat.log" 2>&1 || true
redact < "$OUT/librechat.log" | grep -iE "openid|refresh|federated|placeholder|re-auth|token|error|warn|401" \
  | grep -viE "tokenizer|tokenCount|\"X-Spike|\"Authorization\"|^\S*\s+\"" | tail -80 || true

echo "== analysis"
set +e
python "$HERE/analyse.py" "$LABEL (OPENID_REUSE_TOKENS=$REUSE, token lifespan ${TOKEN_LIFESPAN:-90}s)" "$LC_DIGEST" "$OUT/captured.jsonl" "$OUT/shots/walk.json" "$ISSUER" "$OUT/certs/ca.pem" "$OUT" \
  | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"
RC=${PIPESTATUS[0]}
set -e

# Keep evidence, drop key material and raw tokens of the throwaway realm.
redact < "$OUT/captured.jsonl" > "$OUT/captured-redacted.jsonl"
redact < "$OUT/librechat.log" > "$OUT/librechat-redacted.log"
rm -f "$OUT/captured.jsonl" "$OUT/librechat.log" "$OUT/certs/"*.key
exit "$RC"
