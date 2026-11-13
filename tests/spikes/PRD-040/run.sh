#!/usr/bin/env bash
# PRD-040 lab test: start the prepared FAPI SIG environment (Keycloak under
# test, stub extension, stub resource server, OpenID Foundation conformance
# suite) and run six FAPI1 Advanced Final plans. Evidence goes to $OUT.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
: "${KC_VERSION:?}" "${KCSIG:?}" "${OUT:?}" "${RUNNER_TEMP:?}"
mkdir -p "$OUT"
ENVDIR="$KCSIG/conformance-tests-env"

export OPENID_GIT_URL=https://gitlab.com/openid/conformance-suite.git
export OPENID_GIT_TAG=release-v5.3.4
export RESOURCE_FQDN=rs.keycloak-fapi.org CONSENT_FQDN=cs.keycloak-fapi.org
export CONFORMANCE_SUITE_FQDN=conformance-suite.keycloak-fapi.org
export AUTOMATE_TESTS=false TEST_PLAN=--none
export KEYCLOAK_BASE_IMAGE="quay.io/keycloak/keycloak:${KC_VERSION}"
export KEYCLOAK_FEATURES=scripts KEYCLOAK_HTTP_RELATIVE_PATH=/auth
export KEYCLOAK_REALM=test KEYCLOAK_USER=admin KEYCLOAK_PASSWORD=admin
export KEYCLOAK_REALM_IMPORT_FILENAME=realm-prd040.json
export KEYCLOAK_FQDN=as.keycloak-fapi.org AUTH_ENTITY_FQDN=aes.keycloak-fapi.org
export KEYCLOAK_FRONTEND_URL=https://as.keycloak-fapi.org/auth
export KEYCLOAK_INTROSPECTION_ENDPOINT_FROM_API_GATEWAY=https://as.keycloak-fapi.org/auth/realms/test/protocol/openid-connect/token/introspect
export KEYCLOAK_SPI_0=prd040-consent-stub.jar
export MVN_HOME="$RUNNER_TEMP/m2"
export BUILDKIT_PROGRESS=plain
mkdir -p "$MVN_HOME"

dc() { docker compose -p keycloak-fapi -f "$ENVDIR/docker-compose.yml" "$@"; }

wait_for() {
  local name=$1 limit=$2 start=$SECONDS
  shift 2
  until "$@" >/dev/null 2>&1; do
    if (( SECONDS - start > limit )); then
      echo "TIMEOUT waiting for $name after ${limit}s"
      return 1
    fi
    sleep 5
  done
  echo "ready: $name after $((SECONDS - start))s"
}

collect() {
  dc ps -a > "$OUT/compose-ps.txt" 2>&1 || true
  docker logs keycloak-fapi-keycloak-1 > "$OUT/keycloak.log" 2>&1 || true
  docker logs keycloak-fapi-keycloak-import-1 > "$OUT/keycloak-import.log" 2>&1 || true
  docker logs keycloak-fapi-resource_server-1 > "$OUT/resource-server.log" 2>&1 || true
  docker logs keycloak-fapi-api_gateway_nginx-1 > "$OUT/api-gateway.log" 2>&1 || true
  docker logs keycloak-fapi-server-1 > "$OUT/suite-server.log" 2>&1 || true
  if [ -d "$ENVDIR/report" ]; then
    sudo cp -r "$ENVDIR/report" "$OUT/report" && sudo chown -R "$(id -u):$(id -g)" "$OUT/report"
  fi
}

echo "== build images (Keycloak ${KC_VERSION}, suite ${OPENID_GIT_TAG})"
if ! dc build > "$OUT/build.log" 2>&1; then
  tail -n 80 "$OUT/build.log"
  echo "BUILD FAILED"
  exit 1
fi
grep -A4 "PRD040 guard" "$OUT/build.log" | sed 's/^#[0-9]* [0-9.]* //' | head -20

echo "== start"
dc up -d 2>&1 | tail -n 30
wait_for "conformance suite" 900 docker exec keycloak-fapi-server-1 curl -s -o /dev/null http://localhost:8080/ || { collect; exit 1; }
wait_for "Keycloak realm test" 900 curl -skf https://as.keycloak-fapi.org/auth/realms/test/.well-known/openid-configuration || { collect; exit 1; }
wait_for "resource server" 120 docker exec keycloak-fapi-server-1 curl -sf -o /dev/null http://resource_server:3000/ || { collect; exit 1; }

echo "== facts"
{
  echo "keycloak image: $KEYCLOAK_BASE_IMAGE"
  docker image inspect --format '{{index .RepoDigests 0}}' "$KEYCLOAK_BASE_IMAGE" 2>/dev/null || true
  docker exec keycloak-fapi-keycloak-1 ls /opt/keycloak/lib/lib/main | grep -E '^org\.keycloak\.keycloak-(core|services)-[0-9]'
  docker exec keycloak-fapi-keycloak-1 ls /opt/keycloak/providers
  echo "conformance suite: $OPENID_GIT_URL at $OPENID_GIT_TAG"
  docker exec keycloak-fapi-server-1 sh -c 'grep -n -A2 "case \"openbanking_uk\"" /conformance-suite/src/main/java/net/openid/conformance/fapi1advancedfinal/FAPI1AdvancedFinalTestPlan.java'
} 2>&1 | tee "$OUT/facts.txt"
curl -sk https://as.keycloak-fapi.org/auth/realms/test/.well-known/openid-configuration > "$OUT/discovery.json"
jq '{issuer, pushed_authorization_request_endpoint, require_pushed_authorization_requests,
     request_parameter_supported, request_uri_parameter_supported, require_request_uri_registration,
     claims_parameter_supported, request_object_signing_alg_values_supported,
     id_token_signing_alg_values_supported, token_endpoint_auth_methods_supported,
     tls_client_certificate_bound_access_tokens, code_challenge_methods_supported,
     authorization_details_types_supported, mtls_endpoint_aliases: (.mtls_endpoint_aliases | keys?)}' \
  "$OUT/discovery.json" | tee "$OUT/discovery-summary.json"
docker logs keycloak-fapi-keycloak-1 2>&1 | grep -i -E "prd040|KC-SERVICES0047|Keycloak [0-9.]+ on JVM" | head -10 | tee -a "$OUT/facts.txt"
docker logs keycloak-fapi-keycloak-import-1 2>&1 | tail -n 5 | tee -a "$OUT/facts.txt"

echo "== run six plans"
PLAN=fapi1-advanced-final-test-plan
CFGC=/conformance-suite/.gitlab-ci/fapi-conformance-suite-configs/prd040
v() { echo "[client_auth_type=$1][fapi_profile=$2][fapi_response_mode=plain_response][fapi_auth_request_method=$3]"; }
ARGS=(
  "$PLAN$(v private_key_jwt openbanking_uk by_value)" "$CFGC/p1-private-key.json"
  "$PLAN$(v mtls openbanking_uk by_value)" "$CFGC/p1-mtls.json"
  "$PLAN$(v private_key_jwt plain_fapi pushed)" "$CFGC/p2-private-key.json"
  "$PLAN$(v mtls plain_fapi pushed)" "$CFGC/p2-mtls.json"
  "$PLAN$(v private_key_jwt openbanking_uk pushed)" "$CFGC/p3-private-key.json"
  "$PLAN$(v mtls openbanking_uk pushed)" "$CFGC/p3-mtls.json"
)
printf '%s\n' "${ARGS[@]}" > "$OUT/plans.txt"
docker exec keycloak-fapi-server-1 mkdir -p /conformance-suite/report/prd040
START=$SECONDS
docker exec -w /conformance-suite keycloak-fapi-server-1 timeout 7800 \
  python3 scripts/run-test-plan.py --verbose --export-dir /conformance-suite/report/prd040 "${ARGS[@]}" \
  > "$OUT/run-test-plan.log" 2>&1
RC=$?
echo "run-test-plan exit code: $RC after $((SECONDS - START))s" | tee -a "$OUT/run-test-plan.log"
grep -E "^(.\[[0-9;]*m)?(Test \[|Overall totals|Running plan|\*\*)" "$OUT/run-test-plan.log" | tail -n 400

collect
python3 "$HERE/summarize.py" "$OUT" | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"
exit 0
