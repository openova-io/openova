#!/usr/bin/env bash
# bp-newapi — #5466 / #5480 (A16 class): SESSION_SECRET/CRYPTO_SECRET must be
# region-consistent on the sovereign-admin default path.
#
# THE DEFECT (hw291, UAT rows 37/38): credentials-secret.yaml minted the pair
# with `lookup`-or-`randAlphaNum 64`. Each region's own Flux/Helm hits its own
# apiserver, so the lookup misses in the second region and a DIFFERENT pair is
# generated there — behind the one shared VIP the SSO code exchange succeeded
# in region-B while region-A logged `[sessions] ERROR! securecookie: the value
# is not valid` and 401'd the same page load. The fix rides the bp-sso-bridge
# → OpenBao → ExternalSecret carrier (#3374 client secret, #5416 cookie
# secret, now session/crypto): bp-sso-bridge >= 0.2.27 derives both once per
# KC client into sso/sovereign/<cid> and every region resolves that ONE bundle.
#
# Cases (assert on VALUES, not key presence — the #5639 lesson):
#   1.  Sovereign shape (role=all + kc + sync + ESO capability): the per-region
#       generator renders NOTHING. This case FAILS on the pre-#5466 chart.
#   1b. Same shape WITHOUT the ESO capability: the generator DOES render — the
#       capability leg of the gate cannot wedge a cluster with no ESO CRD
#       (and proves Case 1's empty render is the gate, not a broken template).
#   2.  Deployment SESSION_SECRET + CRYPTO_SECRET secretKeyRefs pivot to the
#       bridge-published `-app-creds-oidc` Secret. FAILS pre-#5466.
#   3.  credentials-externalsecret.yaml delivers BOTH properties from
#       sso/sovereign/<clientId> into that exact Secret (Owner).
#   4.  Fallback (sync off) — vacuity control: the chart-managed Secret still
#       renders with NON-EMPTY values for both keys and the Deployment
#       references `-app-creds`; the ExternalSecret does NOT render.
#   5.  Divergence control — two renders of the fallback generator (each
#       render == one region's Helm run with an empty lookup) yield DIFFERENT
#       SESSION_SECRET values: the exact per-region divergence mechanism the
#       default path abandoned, reproduced in-test so Case 1/2 can never pass
#       vacuously against a generator that was never divergent.
#   6.  vcluster-app (no ESO inside the vCluster): the in-vCluster placeholder
#       path is UNCHANGED — generator renders, no ExternalSecret, Deployment
#       references `-app-creds`.
#   7.  Operator override (credentials.existingSecret) beats the bridge path
#       (Principle #4): Deployment references the BYO name, no ExternalSecret,
#       no chart-managed Secret.

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"
eso_api=(--api-versions external-secrets.io/v1beta1 --api-versions postgresql.cnpg.io/v1)
no_eso_api=(--api-versions postgresql.cnpg.io/v1)

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

# Release name `newapi` == bootstrap-kit slot 80's releaseName, so
# bp-newapi.fullname renders `newapi-bp-newapi` exactly as live.
release="newapi"
creds_fallback="newapi-bp-newapi-app-creds"
creds_bridge="newapi-bp-newapi-app-creds-oidc"

sovereign_values=$(mktemp)
trap 'rm -f "$sovereign_values"' EXIT
cat > "$sovereign_values" <<'EOF'
placement:
  role: all
cnpg:
  enabled: true
sovereignFQDN: t00.omani.works
auth:
  adminUI:
    mode: keycloak
    keycloak:
      issuer: https://auth.t00.omani.works/realms/sovereign
      existingSecret: newapi-oidc
# The admin-token ExternalSecret hard-fails the WHOLE render when enabled
# without its OpenBao path (#4477) — supply the canonical value the
# bootstrap-kit overlay ships so every case renders the full chart.
catalystIntegration:
  externalSecret:
    remoteRef:
      key: catalyst/newapi/admin-token
EOF

# --show-only against a template whose render is empty makes helm exit
# non-zero — capture output, tolerate THAT exit code, assert on content.
# But a full-chart EXECUTION error also exits non-zero with no manifests,
# which would make every renders-NOTHING assertion pass vacuously (this
# exact trap fired during authoring: a missing required value failed the
# whole render and Case 1 "passed" on the error text) — so hard-fail on it.
show() { # show <template> <api-array-name> [extra helm args...]
  local tpl="$1" apiname="$2"; shift 2
  local -n api="$apiname"
  local out
  out=$("$helm" template "$release" "$chart_dir" -f "$sovereign_values" "${api[@]}" \
    --show-only "templates/${tpl}" "$@" 2>&1) || true
  if grep -qE "execution error|parse error|YAML parse" <<<"$out"; then
    echo "FAIL: helm render ERRORED while showing ${tpl} — an empty-render assertion against this output would be vacuous:" >&2
    echo "$out" | head -3 >&2
    exit 1
  fi
  printf '%s' "$out"
}

# Secret-name backing a given env var in a rendered Deployment (VALUE assert).
env_secret() { # env_secret <rendered> <ENV_NAME>
  awk -v want="$2" '
    $0 ~ ("- name: " want "$") {hit=1; next}
    hit && /name:/ { v=$0; sub(/.*name:[ \t]*/,"",v); gsub(/["[:space:]]/,"",v); print v; exit }
  ' <<<"$1"
}

# b64 value of a data key in a rendered Secret (VALUE assert).
secret_value() { # secret_value <rendered> <KEY>
  awk -v want="^[ \t]*$2:" '
    $0 ~ want { v=$0; sub(/^[^:]*:[ \t]*/,"",v); gsub(/"/,"",v); print v; exit }
  ' <<<"$1"
}

# ── Case 1: sovereign default — the per-region generator is GONE ─────────
echo "[5466] Case 1: sovereign shape renders NO chart-managed credentials Secret"
gen=$(show credentials-secret.yaml eso_api)
if grep -q "kind: Secret" <<<"$gen"; then
  echo "FAIL: #5466 — per-region lookup-or-generate credentials Secret still renders under the bridge-synced default" >&2
  exit 1
fi
echo "  PASS"

# ── Case 1b: no ESO capability → generator returns (no wedge) ────────────
echo "[5466] Case 1b: without the ESO CRD capability the generator still renders"
gen_noeso=$(show credentials-secret.yaml no_eso_api)
if ! grep -q "kind: Secret" <<<"$gen_noeso"; then
  echo "FAIL: capability leg — a cluster without ESO would get NO credentials Secret at all (Pod wedged forever)" >&2
  exit 1
fi
echo "  PASS"

# ── Case 2: Deployment env pivots to the bridge-published Secret ─────────
echo "[5466] Case 2: SESSION_SECRET/CRYPTO_SECRET resolve from ${creds_bridge}"
dep=$("$helm" template "$release" "$chart_dir" -f "$sovereign_values" "${eso_api[@]}" \
  --show-only templates/deployment.yaml 2>&1)
grep -q "kind: Deployment" <<<"$dep" || { echo "FAIL: Deployment did not render under the sovereign shape" >&2; exit 1; }
for var in SESSION_SECRET CRYPTO_SECRET; do
  src=$(env_secret "$dep" "$var")
  if [ "$src" != "$creds_bridge" ]; then
    echo "FAIL: #5466 — ${var} sourced from '${src}' (expected ${creds_bridge})" >&2
    exit 1
  fi
done
echo "  PASS"

# ── Case 3: the ExternalSecret delivers BOTH bundle properties ───────────
echo "[5466] Case 3: credentials-externalsecret.yaml maps session_secret + crypto_secret"
es=$(show credentials-externalsecret.yaml eso_api)
grep -q "kind: ExternalSecret" <<<"$es" || { echo "FAIL: credentials ExternalSecret did not render" >&2; exit 1; }
grep -qE "name:[ \t]*${creds_bridge}$" <<<"$es" || { echo "FAIL: ExternalSecret target is not ${creds_bridge}" >&2; exit 1; }
grep -q "creationPolicy: Owner" <<<"$es" || { echo "FAIL: ExternalSecret is not creationPolicy Owner" >&2; exit 1; }
# VALUE asserts: the exact remoteRef bundle path + both properties + the
# exact template mappings (an empty property or a renamed key must fail).
[ "$(grep -c "key: sso/sovereign/newapi-admin" <<<"$es")" -eq 2 ] \
  || { echo "FAIL: expected exactly 2 remoteRefs to sso/sovereign/newapi-admin" >&2; exit 1; }
grep -q "property: session_secret" <<<"$es" || { echo "FAIL: remoteRef property session_secret missing" >&2; exit 1; }
grep -q "property: crypto_secret"  <<<"$es" || { echo "FAIL: remoteRef property crypto_secret missing" >&2; exit 1; }
grep -qE 'SESSION_SECRET:[ \t]*"\{\{ \.session_secret \}\}"' <<<"$es" \
  || { echo "FAIL: template does not map SESSION_SECRET from .session_secret" >&2; exit 1; }
grep -qE 'CRYPTO_SECRET:[ \t]*"\{\{ \.crypto_secret \}\}"' <<<"$es" \
  || { echo "FAIL: template does not map CRYPTO_SECRET from .crypto_secret" >&2; exit 1; }
echo "  PASS"

# ── Case 4: sync off — chart-managed fallback intact (vacuity control) ───
echo "[5466] Case 4: ssoBridgeSync=false keeps the chart-managed path byte-for-byte"
off=(--set auth.adminUI.keycloak.ssoBridgeSync.enabled=false)
gen_off=$(show credentials-secret.yaml eso_api "${off[@]}")
grep -q "kind: Secret" <<<"$gen_off" || { echo "FAIL: fallback Secret missing with sync off" >&2; exit 1; }
for key in SESSION_SECRET CRYPTO_SECRET; do
  val=$(secret_value "$gen_off" "$key")
  if [ -z "$val" ]; then
    echo "FAIL: fallback Secret ${key} rendered EMPTY — the grep would pass on a hollow key (#5639 class)" >&2
    exit 1
  fi
done
dep_off=$("$helm" template "$release" "$chart_dir" -f "$sovereign_values" "${eso_api[@]}" "${off[@]}" \
  --show-only templates/deployment.yaml 2>&1)
for var in SESSION_SECRET CRYPTO_SECRET; do
  src=$(env_secret "$dep_off" "$var")
  if [ "$src" != "$creds_fallback" ]; then
    echo "FAIL: sync off — ${var} sourced from '${src}' (expected ${creds_fallback})" >&2
    exit 1
  fi
done
es_off=$(show credentials-externalsecret.yaml eso_api "${off[@]}")
if grep -q "kind: ExternalSecret" <<<"$es_off"; then
  echo "FAIL: credentials ExternalSecret rendered with sync off" >&2
  exit 1
fi
echo "  PASS"

# ── Case 5: divergence control — the generator IS per-render divergent ───
echo "[5466] Case 5: two fallback renders mint DIFFERENT SESSION_SECRETs (the A16 mechanism)"
gen_off2=$(show credentials-secret.yaml eso_api "${off[@]}")
v1=$(secret_value "$gen_off" SESSION_SECRET)
v2=$(secret_value "$gen_off2" SESSION_SECRET)
if [ -z "$v1" ] || [ -z "$v2" ]; then
  echo "FAIL: divergence control extracted an empty value — control is vacuous" >&2
  exit 1
fi
if [ "$v1" = "$v2" ]; then
  echo "FAIL: two independent renders produced the SAME SESSION_SECRET — the generator is not the per-region divergence source this test exists to fence off; re-examine the template" >&2
  exit 1
fi
echo "  PASS (render1 != render2 — exactly what two regions' Helm runs do)"

# ── Case 6: vcluster-app placeholder path unchanged ──────────────────────
echo "[5466] Case 6: vcluster-app keeps the in-vCluster placeholder (no ESO there)"
vc=(--set placement.role=vcluster-app --set cnpg.enabled=false --set database.existingSecret=bp-newapi-newapi-db-dsn)
gen_vc=$(show credentials-secret.yaml eso_api "${vc[@]}")
grep -q "kind: Secret" <<<"$gen_vc" || { echo "FAIL: vcluster-app credentials placeholder no longer renders" >&2; exit 1; }
es_vc=$(show credentials-externalsecret.yaml eso_api "${vc[@]}")
if grep -q "kind: ExternalSecret" <<<"$es_vc"; then
  echo "FAIL: credentials ExternalSecret rendered under vcluster-app (ESO does not exist in the vCluster)" >&2
  exit 1
fi
dep_vc=$("$helm" template "$release" "$chart_dir" -f "$sovereign_values" "${eso_api[@]}" "${vc[@]}" \
  --show-only templates/deployment.yaml 2>&1)
src=$(env_secret "$dep_vc" SESSION_SECRET)
if [ "$src" != "$creds_fallback" ]; then
  echo "FAIL: vcluster-app SESSION_SECRET sourced from '${src}' (expected ${creds_fallback})" >&2
  exit 1
fi
echo "  PASS"

# ── Case 7: operator BYO override beats the bridge path ──────────────────
echo "[5466] Case 7: credentials.existingSecret wins over the bridge carrier"
byo=(--set credentials.existingSecret=my-byo-creds)
dep_byo=$("$helm" template "$release" "$chart_dir" -f "$sovereign_values" "${eso_api[@]}" "${byo[@]}" \
  --show-only templates/deployment.yaml 2>&1)
src=$(env_secret "$dep_byo" SESSION_SECRET)
if [ "$src" != "my-byo-creds" ]; then
  echo "FAIL: BYO override — SESSION_SECRET sourced from '${src}' (expected my-byo-creds)" >&2
  exit 1
fi
es_byo=$(show credentials-externalsecret.yaml eso_api "${byo[@]}")
if grep -q "kind: ExternalSecret" <<<"$es_byo"; then
  echo "FAIL: credentials ExternalSecret rendered despite operator existingSecret" >&2
  exit 1
fi
gen_byo=$(show credentials-secret.yaml eso_api "${byo[@]}")
if grep -q "kind: Secret" <<<"$gen_byo"; then
  echo "FAIL: chart-managed credentials Secret rendered despite operator existingSecret" >&2
  exit 1
fi
echo "  PASS"

echo "[5466] ALL CASES PASS"
