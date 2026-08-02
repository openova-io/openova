#!/usr/bin/env bash
# bp-oidc-gate — #3844 backchannel-via-internal-Service render audit.
#
# oauth2-proxy does its OIDC discovery (and every backchannel leg) at
# `--oidc-issuer-url`, which is the PUBLIC issuer `https://auth.<fqdn>/...`.
# On Huawei/kom4dc a Pod cannot dial the Sovereign's own public EIP (ELB-DNAT'd,
# no hairpin — same class as #3241), so startup discovery times out
# ("error while discovery OIDC configuration: dial tcp <eip>:443: i/o timeout")
# and EVERY oidc-gate instance never initialises (CrashLoop) — UAT rows
# 3374-36/37/38 RED at the POD level (proven live hw167).
#
# FIX: when `keycloakInternalURL` is set the gate uses --skip-oidc-discovery
# plus explicit --redeem-url/--oidc-jwks-url/--profile-url pointed at the
# in-cluster keycloak Service, keeping --oidc-issuer-url + --login-url +
# --redirect-url PUBLIC (browser leg + `iss` validation; KC_HOSTNAME is pinned
# public by bp-keycloak so the token `iss` still matches).
#
# This test asserts:
#   1. with keycloakInternalURL set (default), the gate renders
#      --skip-oidc-discovery=true + internal redeem/jwks/profile URLs, and the
#      issuer + login + redirect URLs stay PUBLIC;
#   2. the backchannel URLs point at the in-cluster Service, NOT the public EIP;
#   3. with keycloakInternalURL="" the gate renders NONE of the skip-discovery
#      flags (byte-identical to the pre-#3844 public-discovery shape).

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"

fqdn="smoke.example.com"
internal="http://keycloak.keycloak.svc.cluster.local"

vals="$(mktemp)"
trap 'rm -f "$vals"' EXIT
cat >"$vals" <<YAML
sovereignFQDN: ${fqdn}
realm: sovereign
instances:
  - name: openova-flow
    enabled: true
    clientId: openova-flow
    upstream: http://openova-flow-server.catalyst-system.svc.cluster.local:80
YAML

# ── Case 1: fix ON (keycloakInternalURL default) ────────────────────────────
echo "[internal-discovery] Case 1: fix ON renders skip-discovery + internal backchannel"
on="$("$helm" template oidc-gate "$chart_dir" -f "$vals" 2>/dev/null)"

assert_arg() { grep -qF -- "$1" <<<"$on" || { echo "FAIL: missing arg '$1'" >&2; echo "$on" >&2; exit 1; }; }

assert_arg "--skip-oidc-discovery=true"
assert_arg "--redeem-url=${internal}/realms/sovereign/protocol/openid-connect/token"
assert_arg "--oidc-jwks-url=${internal}/realms/sovereign/protocol/openid-connect/certs"
assert_arg "--profile-url=${internal}/realms/sovereign/protocol/openid-connect/userinfo"
# the browser-facing + iss-validation URLs stay PUBLIC
assert_arg "--oidc-issuer-url=https://auth.${fqdn}/realms/sovereign"
assert_arg "--login-url=https://auth.${fqdn}/realms/sovereign/protocol/openid-connect/auth?kc_idp_hint=catalyst-pin"
assert_arg "--redirect-url=https://openova-flow.${fqdn}/oauth2/callback"
echo "  PASS"

# ── Case 2: backchannel never dials the public EIP host ─────────────────────
echo "[internal-discovery] Case 2: no backchannel arg points at the public auth host"
for bc in redeem-url oidc-jwks-url profile-url; do
  line="$(echo "$on" | grep -E -- "--${bc}=" | head -1)"
  if grep -qF "auth.${fqdn}" <<<"$line"; then
    echo "FAIL: --${bc} dials the public EIP host (auth.${fqdn}) — hairpin bug not fixed" >&2
    echo "$line" >&2; exit 1
  fi
done
echo "  PASS"

# ── Case 3: fix OFF (keycloakInternalURL="") — pre-#3844 shape ──────────────
echo "[internal-discovery] Case 3: keycloakInternalURL=\"\" renders NO skip-discovery flags"
off="$("$helm" template oidc-gate "$chart_dir" -f "$vals" --set keycloakInternalURL="" 2>/dev/null)"
if grep -qE -- '--skip-oidc-discovery|--redeem-url|--oidc-jwks-url|--profile-url' <<<"$off"; then
  echo "FAIL: empty keycloakInternalURL must NOT render skip-discovery flags (pre-#3844 shape)" >&2
  echo "$off" | grep -E -- '--skip-oidc-discovery|--redeem-url|--oidc-jwks-url|--profile-url' >&2
  exit 1
fi
# the public issuer + login URL still render in the OFF path
grep -qF -- "--oidc-issuer-url=https://auth.${fqdn}/realms/sovereign" <<<"$off" \
  || { echo "FAIL: OFF path lost the public --oidc-issuer-url" >&2; exit 1; }
echo "  PASS"

echo "[bp-oidc-gate #3844 internal-discovery] All cases PASS"
