#!/usr/bin/env bash
# bp-sso-bridge — #4437 render test: the reconcile loop re-reads the
# keycloak credentials Secret EACH TICK so a reflected `addr`/`client-secret`
# change is picked up WITHOUT a pod roll.
#
# LIVE RCA (omantel.biz dep 4635277cae4ffed9): grafana 2/3
# CreateContainerConfigError → 503 because `grafana-sso-oidc-credentials`
# never materialised — its chart ExternalSecret reported
# `SecretSyncedError: Secret does not exist` for OpenBao `sso/grafana`, which
# bp-sso-bridge never seeded because it was `skipping tick (no KC token)`
# every tick with `Could not resolve host:
# keycloak-x-keycloak-x-mgmt-vcluster.mgmt.svc`. #4325/#4347 de-vclustered
# keycloak back to host ns `keycloak`; bp-keycloak re-emitted the corrected
# `addr`, but KC_ADDR is a secretKeyRef ENV snapshot that never hot-reloads,
# so the long-lived reconciler kept the stale dead host. The fix re-reads the
# live Secret each tick + re-homes the stale `mgmt` egress NetworkPolicy
# namespaces to the host namespaces.
#
# Pure render test (helm template) — no cluster needed.
set -euo pipefail

CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)"
RENDER="$(helm template sso-bridge "$CHART_DIR" --set enabled=true)"

fail() { echo "[4437-kc-credentials-pertick-refresh] FAIL: $1" >&2; exit 1; }

# Here-strings (<<<), NOT `echo | grep -q` — see 3646 test for the SIGPIPE
# rationale on >64KB renders.

# 1. The reconcile script defines refresh_kc_credentials().
grep -q 'refresh_kc_credentials() {' <<<"$RENDER" \
  || fail "refresh_kc_credentials() function not present in reconcile.sh"

# 2. It is CALLED in the main loop BEFORE mint_kc_token (so the live Secret
#    is re-read before the token mint each tick).
SCRIPT="$(awk '/refresh_kc_credentials$/{print "CALL"} /mint_kc_token \|\| \{ warn "skipping tick/{print "MINT"}' <<<"$RENDER")"
# The first non-definition occurrence must be the bare call, and it must
# precede the loop's mint_kc_token guard.
CALL_LINE="$(grep -n '^      refresh_kc_credentials$' <<<"$RENDER" | head -1 | cut -d: -f1)"
MINT_LINE="$(grep -n 'mint_kc_token || { warn "skipping tick' <<<"$RENDER" | head -1 | cut -d: -f1)"
[ -n "$CALL_LINE" ] || fail "refresh_kc_credentials is never CALLED in the loop"
[ -n "$MINT_LINE" ] || fail "mint_kc_token loop guard not found"
[ "$CALL_LINE" -lt "$MINT_LINE" ] \
  || fail "refresh_kc_credentials must be called BEFORE mint_kc_token (call=$CALL_LINE mint=$MINT_LINE)"

# 3. The refresh reads from the configured Secret NAME (not a hardcoded one)
#    and resolves it in the reconciler's own namespace.
grep -q 'get secret "${KC_CREDENTIALS_SECRET_NAME}" -n "${RECONCILER_NAMESPACE}"' <<<"$RENDER" \
  || fail "refresh does not read \${KC_CREDENTIALS_SECRET_NAME} in \${RECONCILER_NAMESPACE}"

# 4. The Deployment passes the Secret names + namespace (downward API) so the
#    script can read the live Secret by name.
grep -q 'name: KC_CREDENTIALS_SECRET_NAME' <<<"$RENDER" \
  || fail "Deployment missing KC_CREDENTIALS_SECRET_NAME env"
grep -q 'name: RECONCILER_NAMESPACE' <<<"$RENDER" \
  || fail "Deployment missing RECONCILER_NAMESPACE env"
grep -q 'fieldPath: metadata.namespace' <<<"$RENDER" \
  || fail "RECONCILER_NAMESPACE not sourced from the downward API (metadata.namespace)"

# 5. The egress NetworkPolicy targets the DE-VCLUSTERED host namespaces
#    (keycloak / openbao), NOT the dead `mgmt` vCluster namespace — once
#    KC_ADDR is corrected, Cilium egress must allow that namespace or the
#    traffic is policy-dropped.
NP_BLOCK="$(awk '/^kind: NetworkPolicy$/{c=1} c{print} c&&/^---/{c=0}' <<<"$RENDER")"
grep -q 'kubernetes.io/metadata.name: "keycloak"' <<<"$NP_BLOCK" \
  || fail "egress NetworkPolicy does not allow the host ns 'keycloak'"
grep -q 'kubernetes.io/metadata.name: "openbao"' <<<"$NP_BLOCK" \
  || fail "egress NetworkPolicy does not allow the host ns 'openbao'"
if grep -q 'kubernetes.io/metadata.name: "mgmt"' <<<"$NP_BLOCK"; then
  fail "egress NetworkPolicy still targets the dead 'mgmt' vCluster namespace (post-#4325 it must be keycloak/openbao)"
fi

echo "[4437-kc-credentials-pertick-refresh] PASS"
