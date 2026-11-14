#!/usr/bin/env bash
# integration.sh - run the fapi-consent extension in Keycloak and test it end to end.
#
#   bash platform/keycloak/extensions/fapi-consent/test/integration.sh <keycloak image> <archive>
#
# Starts the stand-in authorisation API (stub_authz.py) and Keycloak in development mode with the
# provider archive and the test realm (realm.json), both on the loopback address of the host,
# then runs drive.py against them. Ports 18080 (Keycloak) and 18091 (the stand-in) must be free.
# The test client's secret and the test user's password are made up for each run, written into a
# copy of the realm, and handed to drive.py in the environment; the repository holds neither.
# The container engine is docker; set CONTAINER_ENGINE=podman to use podman.
# Exit codes: 0 every case passed; 1 a case failed or Keycloak did not start; 2 wrong arguments.
set -euo pipefail
export LC_ALL=C

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ "$#" -ne 2 ] || [ ! -s "$2" ]; then
  echo "usage: integration.sh <keycloak image> <provider archive>" >&2
  exit 2
fi
image="$1"
archive="$(cd "$(dirname "$2")" && pwd)/$(basename "$2")"
engine="${CONTAINER_ENGINE:-docker}"
kc_port=18080
stub_port=18091
kc="http://127.0.0.1:${kc_port}"
name="fapi-consent-it-$$"
stub_pid=""
work="$(mktemp -d)"

cleanup() {
  if [ -n "$stub_pid" ]; then kill "$stub_pid" 2>/dev/null || true; fi
  "$engine" rm -f "$name" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

FAPI_TEST_CLIENT_SECRET="$(python3 -c 'import secrets; print(secrets.token_urlsafe(24))')"
FAPI_TEST_USER_PASSWORD="$(python3 -c 'import secrets; print(secrets.token_urlsafe(24))')"
export FAPI_TEST_CLIENT_SECRET FAPI_TEST_USER_PASSWORD
python3 -c 'import os, sys; s = open(sys.argv[1], encoding="utf-8").read(); s = s.replace("@CLIENT_SECRET@", os.environ["FAPI_TEST_CLIENT_SECRET"]).replace("@USER_PASSWORD@", os.environ["FAPI_TEST_USER_PASSWORD"]); open(sys.argv[2], "w", encoding="utf-8").write(s)' "$here/realm.json" "$work/realm.json"
chmod 644 "$work/realm.json"

python3 "$here/stub_authz.py" --port "$stub_port" --issuer "${kc}/realms/fapi" \
  --jwks "${kc}/realms/fapi/protocol/openid-connect/certs" &
stub_pid=$!

"$engine" run -d --name "$name" --network host \
  -v "${archive}:/opt/keycloak/providers/fapi-consent.jar:ro" \
  -v "${work}/realm.json:/opt/keycloak/data/import/realm.json:ro" \
  "$image" start-dev --http-port "$kc_port" --import-realm \
  --spi-authenticator-fapi-consent-authz-url="http://127.0.0.1:${stub_port}" \
  --spi-authenticator-fapi-consent-identity-scheme=test-bank >/dev/null

echo "integration: waiting for Keycloak (at most 300 s)"
deadline=$((SECONDS + 300))
until python3 -c 'import sys, urllib.request; urllib.request.urlopen(sys.argv[1], timeout=5)' \
    "${kc}/realms/fapi/.well-known/openid-configuration" 2>/dev/null; do
  if [ "$SECONDS" -ge "$deadline" ] || [ -z "$("$engine" ps -q --filter "name=^${name}$")" ]; then
    echo "integration: Keycloak did not start" >&2
    "$engine" logs "$name" 2>&1 | tail -60 >&2 || true
    exit 1
  fi
  sleep 3
done

status=0
python3 "$here/drive.py" --kc "$kc" --stub "http://127.0.0.1:${stub_port}" || status=1
if [ "$status" -ne 0 ]; then
  "$engine" logs "$name" 2>&1 | grep -E 'ERROR|WARN|fapi' | tail -60 >&2 || true
fi
exit "$status"
