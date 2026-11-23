#!/usr/bin/env bash
# bp-sso-bridge — a failed read is never taken as "nothing there".
#
# Read on main, 22 Nov 2026: the reconciler took a FAILED listing of the
# HelmReleases (`k_retry ... || true`) as "no sso-capable HR" and its orphan
# sweep then deleted every ExternalSecret labelled managed=true; a tick that
# hit its budget broke out of the loop before the remaining client ids were
# recorded, so their ExternalSecrets were swept; a failed listing of the
# app-registration ConfigMaps (`|| true`) emptied that keep-list, and an
# app-registration whose upsert failed this tick (a Keycloak or OpenBao
# hiccup) was left off it too; and a failed GET of a client's secret from
# Keycloak gave "" which was written to OpenBao as sso/<cid>.client_secret.
#
# EXECUTION of the rendered reconcile.sh, one real tick of its main loop: a
# recording kubectl (the API server), a stub curl (Keycloak), and shell
# functions in place of the token mints, the per-HR work and the OpenBao PUT.
# Each part able to fail; on the chart before this change 1, 2, 3, 4, 5 and 7
# fail (1 because of the case of 5):
#   (1) control: a tick whose listings succeed deletes exactly the
#       ExternalSecrets whose HR or app-registration is gone;
#   (2) the HelmRelease listing fails: nothing is deleted;
#   (3) the tick budget is spent before any HR: no listed HR loses its
#       ExternalSecret (the gone ones are still swept);
#   (4) the app-registration listing fails: nothing is deleted;
#   (5) an app-registration whose upsert fails this tick keeps its
#       ExternalSecret (seen in every run: its upsert always fails here);
#   (6) control: a client secret read from Keycloak is written to OpenBao;
#   (7) the read of the client secret fails: nothing is written to OpenBao
#       and the upsert reports the failure.
#
# Usage: bash tests/failed-read-keeps-what-is-there.sh [CHART_DIR]
# shellcheck disable=SC2015,SC2016  # pass/fail chains; ${T_DIR} is expanded by the script under test
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

helm template sso-bridge "${CHART_DIR}" --set enabled=true >"${TMP}/render.yaml" 2>"${TMP}/render.err" \
  || { echo "helm template failed:"; cat "${TMP}/render.err"; exit 1; }
# reconcile.sh: the block of the reconciler ConfigMap, its 4 spaces of indent off.
awk '
  /^---/ { doc = 0; blk = 0 }
  /^kind: ConfigMap$/ { doc = 1 }
  blk && /^    / { print substr($0, 5); next }
  blk && /^ *$/ { print ""; next }
  blk { blk = 0 }
  doc && /^  reconcile.sh: \|$/ { blk = 1 }
' "${TMP}/render.yaml" | sed -e 's#/tmp/#${T_DIR}/#g' \
  -e 's#^SA_DIR=/var/run/secrets/kubernetes.io/serviceaccount$#SA_DIR="${T_DIR}/sa"#' >"${TMP}/reconcile.sh"
[ "$(grep -c '^# ─── main loop' "${TMP}/reconcile.sh")" = 1 ] && grep -q '^SA_DIR="${T_DIR}/sa"$' "${TMP}/reconcile.sh" \
  || { echo "FAIL: the render has no reconcile.sh with one main-loop marker and the SA_DIR line this test moves"; exit 1; }
sed -n '1,/^# ─── main loop/p' "${TMP}/reconcile.sh" >"${TMP}/funcs.sh"
sed -n '/^# ─── main loop/,$p' "${TMP}/reconcile.sh" >"${TMP}/loop.sh"
for f in mint_kc_token openbao_login refresh_kc_credentials operator_email sovereign_fqdn reconcile_one \
  reconcile_per_org_realms upsert_per_org_client openbao_put upsert_client emit_external_secret k_retry; do
  grep -q "^${f}() {" "${TMP}/funcs.sh" || { echo "FAIL: reconcile.sh no longer defines ${f}, which this test replaces or calls"; exit 1; }
done

# The replaced parts. reconcile_one and the app-registration upsert record their call; the
# upsert always fails (the Keycloak hiccup of part 5). sleep ends the tick at the loop's interval.
cat >"${TMP}/overrides.sh" <<'EOF'
mint_kc_token() { KC_ADMIN_TOKEN=test; }
openbao_login() { :; }
refresh_kc_credentials() { :; }
operator_email() { :; }
sovereign_fqdn() { echo sov.example.test; }
reconcile_per_org_realms() { :; }
reconcile_one() { printf '%s\n' "$1" | jq -r '.metadata.name' >>"${T_DIR}/reconciled"; }
upsert_per_org_client() { printf '%s\n' "$1" | jq -r '.metadata.name' >>"${T_DIR}/upserted"; return 1; }
openbao_put() { printf '%s %s\n' "$1" "$2" >>"${T_DIR}/openbao"; }
sleep() { [ "$1" = "${INTERVAL_SECONDS}" ] && exit 0; return 0; }
EOF
cat "${TMP}/funcs.sh" "${TMP}/overrides.sh" "${TMP}/loop.sh" >"${TMP}/tick.sh"

mkdir -p "${TMP}/bin"
cat >"${TMP}/bin/kubectl" <<'EOF'
#!/bin/sh
# The API server. HR_LIST / APPREG_LIST = fail make that listing fail.
case " $* " in
  *" get hr "*) [ "${HR_LIST:-}" = fail ] && exit 1; cat "${T_DIR}/../hrs.json" ;;
  *" get configmaps "*"sso.openova.io/app-registration"*) [ "${APPREG_LIST:-}" = fail ] && exit 1; cat "${T_DIR}/../appreg.json" ;;
  *" get externalsecrets "*) cat "${T_DIR}/../es.json" ;;
  *" delete externalsecret "*) echo "$*" | sed 's/.* delete externalsecret \([^ ]*\) -n \([^ ]*\).*/\2\/\1/' >>"${T_DIR}/deleted" ;;
esac
exit 0
EOF
cat >"${TMP}/bin/curl" <<'EOF'
#!/bin/sh
# Keycloak: the client exists; KC_SECRET_READ = fail makes the read of its secret fail.
u=""; for a in "$@"; do u="${a}"; done
case "${u}" in
  *"/clients?clientId="*) echo '[{"id":"k1","clientId":"gitea"}]' ;;
  */client-secret) [ "${KC_SECRET_READ:-}" = fail ] && exit 22; echo '{"value":"s3cret"}' ;;
esac
exit 0
EOF
chmod +x "${TMP}/bin/kubectl" "${TMP}/bin/curl"
es() { printf '{"metadata":{"name":"%s","namespace":"%s","labels":{"bp-sso-bridge.openova.io/managed":"true","bp-sso-bridge.openova.io/client-id":"%s"}}}' "$1" "$2" "$3"; }
printf '{"items":[{"metadata":{"name":"bp-gitea","namespace":"flux-system"},"spec":{"values":{"sso":{"enabled":true}}}},%s]}\n' \
  '{"metadata":{"name":"bp-openbao","namespace":"flux-system"},"spec":{"values":{"sso":{"sovereignFqdn":"sov.example.test"}}}}' >"${TMP}/hrs.json"
printf '{"items":[{"metadata":{"name":"hubble-ui-sso","namespace":"kube-system","labels":{"sso.openova.io/app-registration":"hubble-ui"}},"data":{"realm":"sovereign","client.json":"{}","externalSecret.json":"{\\"name\\":\\"hubble-ui-oidc\\",\\"namespace\\":\\"kube-system\\",\\"secretKey\\":\\"s\\"}"}}]}\n' >"${TMP}/appreg.json"
printf '{"items":[%s,%s,%s,%s,%s]}\n' "$(es gitea-sso-oidc-credentials gitea gitea)" "$(es openbao-sso-oidc-credentials openbao openbao)" \
  "$(es hubble-ui-oidc kube-system hubble-ui)" "$(es stale-sso stale stale)" "$(es flow-oidc flow openova-flow)" >"${TMP}/es.json"
jq -e . "${TMP}/hrs.json" "${TMP}/appreg.json" "${TMP}/es.json" >/dev/null || { echo "FAIL: the test's own listings are not JSON"; exit 1; }

run_script() {  # $1 = script, $2 = run name; the rest: VAR=value of the scenario
  local s="$1" d="${TMP}/$2"; shift 2
  mkdir -p "${d}/sa" && echo token >"${d}/sa/token" && : >"${d}/deleted" && : >"${d}/reconciled" && : >"${d}/openbao"
  env T_DIR="${d}" PATH="${TMP}/bin:${PATH}" KC_REALM=sovereign KC_ADDR=http://keycloak.test KC_ADMIN_TOKEN=test \
    OPENBAO_ADDR=http://openbao.test OPENBAO_KV_MOUNT=secret OPENBAO_K8S_AUTH_PATH=kubernetes OPENBAO_K8S_AUTH_ROLE=sso-bridge \
    INTERVAL_SECONDS=77 TIMEOUT_SECONDS=600 SOVEREIGN_FQDN_CM_NS=catalyst SOVEREIGN_FQDN_CM_NAME=sovereign-fqdn \
    SOVEREIGN_FQDN_ORG_EMAIL_KEY=orgEmail SOVEREIGN_FQDN_SOVEREIGN_FQDN_KEY=sovereignFQDN RECONCILER_NAMESPACE=sso-bridge \
    "$@" timeout 60 bash "${s}" >"${d}/log" 2>&1
}
run_tick() {  # $1 = run name; the rest: VAR=value of the scenario
  run_script "${TMP}/tick.sh" "$@" || fail "$1: the tick did not end at its sleep: $(tail -n 3 "${TMP}/$1/log" | tr '\n' ' ')"
}
deleted() { LC_ALL=C sort "${TMP}/$1/deleted" | tr '\n' ' '; }
GONE="flow/flow-oidc stale/stale-sso "

echo "[failed-read] (1) control: listings that succeed sweep exactly what is gone"
run_tick ok
[ "$(deleted ok)" = "${GONE}" ] && pass "deleted: ${GONE}(the HR stale and the app-registration flow are gone)" \
  || fail "a good tick deleted '$(deleted ok)', want '${GONE}'"
[ "$(LC_ALL=C sort "${TMP}/ok/reconciled" | tr '\n' ' ')" = "bp-gitea bp-openbao " ] && pass "both listed HRs were reconciled" \
  || fail "the good tick reconciled '$(tr '\n' ' ' <"${TMP}/ok/reconciled")'"

echo "[failed-read] (2) the HelmRelease listing fails"
run_tick hr-fail HR_LIST=fail
[ -z "$(deleted hr-fail)" ] && pass "no ExternalSecret deleted" || fail "a failed HR listing deleted: $(deleted hr-fail)"

echo "[failed-read] (3) the tick budget is spent before the first HR"
run_tick budget TIMEOUT_SECONDS=0
grep -q 'tick budget exceeded' "${TMP}/budget/log" && [ ! -s "${TMP}/budget/reconciled" ] \
  && pass "the budget stopped the tick before any HR (the case bites)" || fail "the tick with no budget still reconciled an HR"
[ "$(deleted budget)" = "${GONE}" ] && pass "only the gone ones swept; gitea and openbao, listed but deferred, kept" \
  || fail "a tick out of budget deleted '$(deleted budget)', want '${GONE}'"

echo "[failed-read] (4) the app-registration listing fails"
run_tick appreg-fail APPREG_LIST=fail
[ -z "$(deleted appreg-fail)" ] && pass "no ExternalSecret deleted" || fail "a failed app-registration listing deleted: $(deleted appreg-fail)"

echo "[failed-read] (5) an app-registration whose upsert fails keeps its ExternalSecret"
grep -q '^hubble-ui-sso$' "${TMP}/ok/upserted" && pass "its upsert ran and failed (the case bites)" || fail "the app-registration upsert never ran"
grep -q 'hubble-ui' "${TMP}/ok/deleted" && fail "kube-system/hubble-ui-oidc was deleted after its upsert failed" \
  || pass "kube-system/hubble-ui-oidc kept"

# Parts 6 and 7: the upsert of a Tier-1 client and the OpenBao write that follows it in reconcile_one.
cat "${TMP}/funcs.sh" "${TMP}/overrides.sh" >"${TMP}/client.sh"
printf 'upsert_client gitea sov.example.test || exit 3\nemit_external_secret gitea gitea true\n' >>"${TMP}/client.sh"
run_client() { run_script "${TMP}/client.sh" "$@"; }  # $1 = run name; the rest: VAR=value

echo "[failed-read] (6) control: the client secret read from Keycloak reaches OpenBao"
run_client secret-ok
grep -q '^sso/gitea .*"client_secret":"s3cret"' "${TMP}/secret-ok/openbao" && pass "sso/gitea written with the secret Keycloak gave" \
  || fail "the good upsert wrote no sso/gitea with the secret: $(head -c 300 "${TMP}/secret-ok/openbao") $(tail -n 2 "${TMP}/secret-ok/log")"

echo "[failed-read] (7) the read of the client secret fails"
run_client secret-fail KC_SECRET_READ=fail
rc=$?
[ ! -s "${TMP}/secret-fail/openbao" ] && pass "nothing written to OpenBao" \
  || fail "a failed secret read wrote to OpenBao: $(head -c 300 "${TMP}/secret-fail/openbao")"
[ "${rc}" = 3 ] && grep -q 'no client secret read from Keycloak' "${TMP}/secret-fail/log" \
  && pass "upsert_client failed and said why" || fail "upsert_client did not report the failed read (exit ${rc})"

if [ "${FAILURES}" -gt 0 ]; then echo "failed-read-keeps-what-is-there: ${FAILURES} failure(s)"; exit 1; fi
echo "failed-read-keeps-what-is-there: all checks passed"
