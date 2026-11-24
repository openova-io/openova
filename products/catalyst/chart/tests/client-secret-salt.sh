#!/usr/bin/env bash
# SEC-060 — the Keycloak credentials host bridge derives its client secrets
# with the Sovereign's salt.
#
# templates/catalyst-keycloak-credentials-host-bridge.yaml re-derives two
# secrets that bp-keycloak (slot 09) bakes into the realm: the catalyst-api-server
# service-account secret and the catalyst-pin broker secret, each the SHA-256
# of "<keycloak release>|<keycloak namespace>|<word>". Since SEC-060 both seeds
# end in "|<salt>" when sso.tier1ClientSecretSalt is set (slot 13 reads it from
# the seed Secret of first boot, as slot 09 does). The equality with
# bp-keycloak's own render is asserted by platform/keycloak/chart/tests/client-secret-salt.sh.
#
# Asserts:
#   1. two salts give two different pairs of secrets, and with a salt each
#      secret is the SHA-256 of "<seed>|<salt>";
#   2. an install by Flux (the cluster serves helm.toolkit.fluxcd.io/v2) with
#      sso.requireSalt=true and no salt fails, naming SEC-060; the same values
#      rendered without a cluster (helm template, the kit renderers of CI) render;
#   3. an upgrade by Flux with sso.requireSalt=true and no salt (a Sovereign
#      installed before the salt existed) renders the secrets of before, the
#      SHA-256 of the seed alone.
#
# Runs with helm and yq (v4.44.3 in CI); plain selections, no slices.
set -euo pipefail

CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
# FLUX: the API helm-controller's install sees on a Sovereign and a render
# without a cluster does not.
FLUX="--api-versions=helm.toolkit.fluxcd.io/v2"
sha() { printf '%s' "$1" | sha256sum | cut -d' ' -f1; }

# render OUT [helm flags...]: the host bridge as slot 13 installs it.
render() {
  local out="$1"
  shift
  helm template catalyst-platform "$CHART_DIR" --namespace catalyst-system \
    --set global.sovereignFQDN=hw00.omani.works \
    --set sso.requireSalt=true "$@" \
    --show-only templates/catalyst-keycloak-credentials-host-bridge.yaml >"$out" 2>"$out.err"
}

# secret RENDER NAME: the client-secret of Secret NAME.
secret() { yq -r "select(.kind == \"Secret\" and .metadata.name == \"$2\") | .stringData[\"client-secret\"]" "$1"; }

SA_SEED='keycloak|keycloak|catalyst-api-server-sa'
PIN_SEED='keycloak|keycloak|catalyst-pin-broker'

echo "=== SEC-060 client-secret salt (bp-catalyst-platform host bridge) ==="

SALT_A="sec060-salt-a-0123456789abcdefghijklmnopqrstuvwxyzABCD"
SALT_B="sec060-salt-b-0123456789abcdefghijklmnopqrstuvwxyzABCD"
render "$TMP/a.yaml" --set "sso.tier1ClientSecretSalt=$SALT_A" || fail "the render with salt A failed: $(cat "$TMP/a.yaml.err")"
render "$TMP/b.yaml" --set "sso.tier1ClientSecretSalt=$SALT_B" || fail "the render with salt B failed: $(cat "$TMP/b.yaml.err")"

# 1. salted, and different per salt
for s in catalyst-kc-sa-credentials catalyst-pin-broker-credentials; do
  a=$(secret "$TMP/a.yaml" "$s")
  b=$(secret "$TMP/b.yaml" "$s")
  [ -n "$a" ] && [ "$a" != "$b" ] || fail "1: $s is the same under two salts (or empty)"
done
[ "$(secret "$TMP/a.yaml" catalyst-kc-sa-credentials)" = "$(sha "$SA_SEED|$SALT_A")" ] \
  || fail "1: the SA secret is not the SHA-256 of <seed>|<salt>"
[ "$(secret "$TMP/a.yaml" catalyst-pin-broker-credentials)" = "$(sha "$PIN_SEED|$SALT_A")" ] \
  || fail "1: the pin-broker secret is not the SHA-256 of <seed>|<salt>"
echo "PASS 1: both secrets carry the salt and differ between two salts"

# 2. an install by Flux without a salt is refused; a render without a cluster is not
if render "$TMP/none.yaml" "$FLUX"; then
  fail "2: an install by Flux with sso.requireSalt=true and no salt rendered"
fi
grep -q 'sso.requireSalt is true and sso.tier1ClientSecretSalt is empty' "$TMP/none.yaml.err" \
  || fail "2: the refusal does not say why: $(cat "$TMP/none.yaml.err")"
grep -q 'SEC-060' "$TMP/none.yaml.err" || fail "2: the refusal does not name SEC-060"
render "$TMP/kit.yaml" || fail "2: the render without a cluster failed: $(cat "$TMP/kit.yaml.err")"
echo "PASS 2: an install by Flux with sso.requireSalt=true and no salt fails; the render without a cluster does not"

# 3. an upgrade of a Sovereign installed before the salt keeps its secrets
render "$TMP/up.yaml" "$FLUX" --is-upgrade || fail "3: an upgrade without a salt was refused: $(cat "$TMP/up.yaml.err")"
[ "$(secret "$TMP/up.yaml" catalyst-kc-sa-credentials)" = "$(sha "$SA_SEED")" ] \
  || fail "3: the unsalted SA secret changed"
[ "$(secret "$TMP/up.yaml" catalyst-pin-broker-credentials)" = "$(sha "$PIN_SEED")" ] \
  || fail "3: the unsalted pin-broker secret changed"
echo "PASS 3: an upgrade without a salt renders the secrets of before"

echo "=== ALL TESTS PASS ==="
