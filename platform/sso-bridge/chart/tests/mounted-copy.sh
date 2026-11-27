#!/usr/bin/env bash
# bp-sso-bridge — SEC-344: an HR with `sso.mountedCopy: true` gets its client
# bundle at mounted/data/<targetNamespace>/shared/sso-oidc/e1, written with the
# write-only producer identity of bp-openbao (producers entry sso-bridge).
#
# Renders the chart, then EXECUTES the functions of the rendered reconcile.sh
# with a stub curl that records every request (OpenBao) and answers like it:
#   1. the Deployment hands the reconciler the producer role producer-sso-bridge
#      and a projected token for audience openbao at /var/run/secrets/openbao;
#   2. reconcile_one passes the HR's sso.mountedCopy and targetNamespace on;
#   3. mountedCopy true: one login as producer-sso-bridge with the projected
#      token, then one write to mounted/data/grafana/shared/sso-oidc/e1 with the
#      token of that login, carrying the client id and the client secret;
#   4. control, mountedCopy false: no login and no write below mounted/;
#   5. the login is refused: nothing is written and the call fails;
#   6. the write is refused (403): the call fails.
# On the chart before SEC-344 cases 1, 2 and 3 fail (5 and 6 then fail too,
# because the call succeeds without writing).
#
# Usage: bash tests/mounted-copy.sh [CHART_DIR]
# shellcheck disable=SC2016  # ${T_DIR} is expanded by the script under test
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
check() { if [ "$2" = "$3" ]; then echo "  PASS  $1"; else echo "  FAIL  $1: got [$2], want [$3]"; FAILURES=$((FAILURES + 1)); fi; }

helm template sso-bridge "${CHART_DIR}" --set enabled=true >"${TMP}/render.yaml" 2>"${TMP}/render.err" \
  || { echo "helm template failed:"; cat "${TMP}/render.err"; exit 1; }

echo "[mounted-copy] SEC-344"
c='[select(.kind == "Deployment")] | .[0].spec.template.spec'
check "1 the reconciler's producer role and token file" \
  "$(yq ea "${c}"' | .containers[0].env[] | select(.name == "OPENBAO_PRODUCER_ROLE" or .name == "OPENBAO_PRODUCER_JWT_FILE") | .name + "=" + .value' "${TMP}/render.yaml" | tr '\n' ' ')" \
  "OPENBAO_PRODUCER_ROLE=producer-sso-bridge OPENBAO_PRODUCER_JWT_FILE=/var/run/secrets/openbao/token "
check "1 volume openbao-token is a projected token for audience openbao, mounted read-only at /var/run/secrets/openbao" \
  "$(yq ea "${c}"' | (.volumes[] | select(.name == "openbao-token") | .projected.sources[0].serviceAccountToken | .audience + " " + .path) + " " + (.containers[0].volumeMounts[] | select(.name == "openbao-token") | .mountPath + " " + (.readOnly | tostring))' "${TMP}/render.yaml")" \
  "openbao token /var/run/secrets/openbao true"

# reconcile.sh: the block of the reconciler ConfigMap, its 4 spaces of indent off; the functions only.
awk '
  /^---/ { doc = 0; blk = 0 }
  /^kind: ConfigMap$/ { doc = 1 }
  blk && /^    / { print substr($0, 5); next }
  blk && /^ *$/ { print ""; next }
  blk { blk = 0 }
  doc && /^  reconcile.sh: \|$/ { blk = 1 }
' "${TMP}/render.yaml" | sed -e 's#/tmp/#${T_DIR}/#g' | sed -n '1,/^# ─── main loop/p' >"${TMP}/funcs.sh"
for f in openbao_put emit_external_secret reconcile_one upsert_client; do
  grep -q "^${f}() {" "${TMP}/funcs.sh" || { echo "FAIL: reconcile.sh no longer defines ${f}, which this test replaces or calls"; exit 1; }
done

mkdir -p "${TMP}/bin"
cat >"${TMP}/bin/curl" <<'EOF'
#!/bin/sh
# OpenBao. Records "<url> <X-Vault-Token or -> <-d body>" per request.
# LOGIN=fail answers the login without a token; PUT_CODE sets the code of a write.
url=""; tok="-"; body=""; prev=""
for a in "$@"; do
  case "${prev}" in
    -H) case "${a}" in "X-Vault-Token: "*) tok="${a#X-Vault-Token: }" ;; esac ;;
    -d) body="${a}" ;;
  esac
  case "${a}" in http*) url="${a}" ;; esac
  prev="${a}"
done
printf '%s %s %s\n' "${url}" "${tok}" "${body}" >>"${T_DIR}/requests"
case "${url}" in
  */v1/auth/kubernetes/login) [ "${LOGIN:-}" = fail ] && { echo '{"errors":["permission denied"]}'; exit 22; }
    echo '{"auth":{"client_token":"producer-token-1"}}' ;;
  */v1/mounted/*) printf '%s' "${PUT_CODE:-204}" ;;
esac
exit 0
EOF
chmod +x "${TMP}/bin/curl"
cat >"${TMP}/overrides.sh" <<'EOF'
openbao_put() { printf '%s\n' "$1" >>"${T_DIR}/legacy"; }
upsert_client() { :; }
EOF

run() {  # $1 = run name, $2 = shell code to run after the functions; the rest: VAR=value
  local d="${TMP}/$1" code="$2"; shift 2
  mkdir -p "${d}" && : >"${d}/requests" && : >"${d}/legacy"
  printf 'client-secret-of-test\n' >"${d}/grafana.secret" && printf 'https://auth.t01.omani.works/realms/sovereign\n' >"${d}/grafana.issuer"
  printf 'projected-jwt-1' >"${d}/token"
  printf '%s\n' "${code}" >"${d}/run.sh"
  env T_DIR="${d}" PATH="${TMP}/bin:${PATH}" KC_REALM=sovereign KC_ADDR=http://keycloak.test \
    OPENBAO_ADDR=http://openbao.test OPENBAO_KV_MOUNT=secret OPENBAO_K8S_AUTH_PATH=kubernetes OPENBAO_K8S_AUTH_ROLE=sso-bridge \
    INTERVAL_SECONDS=77 TIMEOUT_SECONDS=600 SOVEREIGN_FQDN_CM_NS=catalyst SOVEREIGN_FQDN_CM_NAME=sovereign-fqdn \
    SOVEREIGN_FQDN_ORG_EMAIL_KEY=orgEmail SOVEREIGN_FQDN_SOVEREIGN_FQDN_KEY=sovereignFQDN RECONCILER_NAMESPACE=sso-bridge \
    OPENBAO_PRODUCER_ROLE=producer-sso-bridge OPENBAO_PRODUCER_JWT_FILE="${d}/token" "$@" \
    bash -c '. "$1"; . "$2"; . "$3"' run "${TMP}/funcs.sh" "${TMP}/overrides.sh" "${d}/run.sh" >"${d}/log" 2>&1
  echo $? >"${d}/rc"
}
req() { grep -F "$2" "${TMP}/$1/requests" || true; }

run hr 'emit_external_secret() { printf "%s|%s|%s|%s\n" "$@" >"${T_DIR}/emit"; }
reconcile_one '"'"'{"metadata":{"name":"bp-grafana","namespace":"flux-system"},"spec":{"targetNamespace":"grafana","values":{"sso":{"enabled":true,"skipExternalSecret":true,"mountedCopy":true}}}}'"'"' "" t01.omani.works'
check "2 reconcile_one hands emit_external_secret the namespace and mountedCopy" "$(cat "${TMP}/hr/emit" 2>/dev/null)" "grafana|grafana|true|true"

run on 'emit_external_secret grafana grafana true true'
check "3 the call succeeds" "$(cat "${TMP}/on/rc")" 0
check "3 one login, as producer-sso-bridge with the projected token" \
  "$(req on /v1/auth/kubernetes/login | cut -d' ' -f3- | jq -c .)" '{"role":"producer-sso-bridge","jwt":"projected-jwt-1"}'
w="$(req on /v1/mounted/)"
check "3 one write, to mounted/data/grafana/shared/sso-oidc/e1 with the login's token" \
  "$(printf '%s\n' "${w}" | cut -d' ' -f1-2)" "http://openbao.test/v1/mounted/data/grafana/shared/sso-oidc/e1 producer-token-1"
check "3 the write carries the client id and the client secret" \
  "$(printf '%s\n' "${w}" | cut -d' ' -f3- | jq -r '.data.client_id + " " + .data.client_secret')" "grafana client-secret-of-test"
check "3 the legacy bundle sso/grafana is still written" "$(cat "${TMP}/on/legacy")" "sso/grafana"

run off 'emit_external_secret grafana grafana true false'
check "4 control, mountedCopy false: the call succeeds" "$(cat "${TMP}/off/rc")" 0
check "4 control, mountedCopy false: no request to OpenBao besides the legacy bundle" "$(wc -l <"${TMP}/off/requests" | tr -d ' ')" 0

run nologin 'emit_external_secret grafana grafana true true' LOGIN=fail
check "5 the login is refused: the call fails" "$(cat "${TMP}/nologin/rc")" 1
check "5 the login is refused: nothing is written below mounted/" "$(req nologin /v1/mounted/)" ""

run denied 'emit_external_secret grafana grafana true true' PUT_CODE=403
check "6 the write is refused: the call fails" "$(cat "${TMP}/denied/rc")" 1

if [ "${FAILURES}" -ne 0 ]; then echo "[mounted-copy] ${FAILURES} case(s) FAILED"; exit 1; fi
echo "[mounted-copy] all cases PASS"
