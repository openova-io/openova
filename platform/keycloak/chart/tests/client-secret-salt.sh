#!/usr/bin/env bash
# SEC-060 — no Keycloak client secret of a Sovereign can be computed from its
# release name and namespace.
#
# Every client secret this chart derives (the eight realm clients, the
# catalyst-pin broker and each per-Org sovereign-broker) is the SHA-256 of a
# seed made of the release name, the namespace and a fixed word. Since SEC-060
# the seed also carries sso.tier1ClientSecretSalt, a random salt per Sovereign
# that bootstrap-kit slot 09 reads from the seed Secret of first boot. The
# Keycloak credentials host bridge of bp-catalyst-platform derives the
# catalyst-api-server and catalyst-pin secrets again, from the same salt.
#
# Asserts:
#   1. two salts give ten different secrets each: the eight clients, the pin
#      broker and one per-Org broker, in the realm JSON and in the Secrets;
#   2. one salt renders the same ten secrets every time;
#   3. an install by Flux (the cluster serves helm.toolkit.fluxcd.io/v2) with
#      sso.requireSalt=true and no salt fails, naming SEC-060; the same values
#      rendered without a cluster (helm template, the kit renderers of CI, which
#      never read valuesFrom) render;
#   4. an upgrade by Flux with sso.requireSalt=true and no salt (a Sovereign
#      installed before the salt existed) renders the secrets it had before: the
#      pin broker is still SHA-256 of "<release>|<namespace>|catalyst-pin-broker";
#   5. the host bridge of bp-catalyst-platform, given the same salt, renders
#      the catalyst-api-server and catalyst-pin secrets byte-equal to this
#      chart's, with a salt and without one.
#
# Runs with helm, yq (v4.44.3 in CI) and jq; plain selections, no slices.
set -euo pipefail

CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)"
REPO_ROOT="$(cd "$CHART_DIR/../../.." && pwd)"
PLATFORM_CHART="$REPO_ROOT/products/catalyst/chart"
# That chart depends on platform/secrets-lib (SEC-216): build it into its charts/ before case 5 renders it.
helm dependency build --skip-refresh "$PLATFORM_CHART" > /dev/null
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

REALM_CM="keycloak-sovereign-realm-config"
ORG_CM="keycloak-org-realm-acme-config"
CLIENTS="catalyst-api-server grafana gitea harbor openbao guacamole powerdns-admin hubble-ui"

fail() { echo "FAIL: $*" >&2; exit 1; }

# render OUT [helm flags...]: the chart as slot 09 installs it (release
# keycloak in namespace keycloak), with one per-Org realm. The bundled
# PostgreSQL is off: its subchart refuses an upgrade render without its
# passwords, and no secret of this test comes from it.
render() {
  local out="$1"
  shift
  helm template keycloak "$CHART_DIR" --namespace keycloak \
    --set sovereignFQDN=hw00.omani.works \
    --set "tenantRealms[0].slug=acme" \
    --set keycloak.postgresql.enabled=false --set keycloak.externalDatabase.host=pg.test --set keycloak.externalDatabase.existingSecret=keycloak-database-secret \
    --set sso.requireSalt=true "$@" >"$out" 2>"$out.err"
}

# secrets RENDER: one line "<name> <secret>" for each of the ten secrets, and
# the two Secrets that carry a realm value.
secrets() {
  local r="$1" realm org
  realm=$(yq -r "select(.kind == \"ConfigMap\" and .metadata.name == \"$REALM_CM\") | .data[\"sovereign-realm.json\"]" "$r")
  org=$(yq -r "select(.kind == \"ConfigMap\" and .metadata.name == \"$ORG_CM\") | .data[\"org-realm.json\"]" "$r")
  for c in $CLIENTS; do
    printf '%s %s\n' "client/$c" "$(printf '%s' "$realm" | jq -r --arg c "$c" '.clients[] | select(.clientId == $c) | .secret')"
  done
  printf '%s %s\n' "idp/catalyst-pin" "$(printf '%s' "$realm" | jq -r '.identityProviders[] | select(.alias == "catalyst-pin") | .config.clientSecret')"
  printf '%s %s\n' "org/acme/sovereign-broker" "$(printf '%s' "$org" | jq -r '.identityProviders[] | select(.alias == "sovereign-broker") | .config.clientSecret')"
  printf '%s %s\n' "secret/catalyst-kc-sa-credentials" "$(yq -r 'select(.kind == "Secret" and .metadata.name == "catalyst-kc-sa-credentials") | .stringData["client-secret"]' "$r")"
  printf '%s %s\n' "secret/catalyst-pin-broker-credentials" "$(yq -r 'select(.kind == "Secret" and .metadata.name == "catalyst-pin-broker-credentials") | .stringData["client-secret"]' "$r")"
}

# FLUX: the API helm-controller's install sees on a Sovereign and a render
# without a cluster does not.
FLUX="--api-versions=helm.toolkit.fluxcd.io/v2"

sha() { printf '%s' "$1" | sha256sum | cut -d' ' -f1; }

value() { awk -v k="$2" '$1 == k { print $2 }' "$1"; }

echo "=== SEC-060 client-secret salt (bp-keycloak) ==="

SALT_A="sec060-salt-a-0123456789abcdefghijklmnopqrstuvwxyzABCD"
SALT_B="sec060-salt-b-0123456789abcdefghijklmnopqrstuvwxyzABCD"
render "$TMP/a.yaml" --set "sso.tier1ClientSecretSalt=$SALT_A" || fail "the render with salt A failed: $(cat "$TMP/a.yaml.err")"
render "$TMP/a2.yaml" --set "sso.tier1ClientSecretSalt=$SALT_A" || fail "the second render with salt A failed"
render "$TMP/b.yaml" --set "sso.tier1ClientSecretSalt=$SALT_B" || fail "the render with salt B failed"
secrets "$TMP/a.yaml" >"$TMP/a.txt"
secrets "$TMP/a2.yaml" >"$TMP/a2.txt"
secrets "$TMP/b.yaml" >"$TMP/b.txt"

# 1. ten secrets per render, every one 64 hex characters, none shared by the two salts
n=$(grep -c -E ' [0-9a-f]{64}$' "$TMP/a.txt" || true)
[ "$n" = 12 ] || fail "1: salt A renders $n of 12 values as 64 hex characters: $(awk '{print $1 "=" length($2)}' "$TMP/a.txt" | tr '\n' ' ')"
n=$(grep -c -E ' [0-9a-f]{64}$' "$TMP/b.txt" || true)
[ "$n" = 12 ] || fail "1: salt B renders $n of 12 values as 64 hex characters"
shared=$(awk '{print $2}' "$TMP/a.txt" | sort -u | grep -c -x -F -f <(awk '{print $2}' "$TMP/b.txt") || true)
[ "$shared" = 0 ] || fail "1: $shared secret(s) are the same under two salts"
[ "$(value "$TMP/a.txt" client/catalyst-api-server)" = "$(value "$TMP/a.txt" secret/catalyst-kc-sa-credentials)" ] \
  || fail "1: the realm's catalyst-api-server secret and Secret catalyst-kc-sa-credentials differ"
[ "$(value "$TMP/a.txt" idp/catalyst-pin)" = "$(value "$TMP/a.txt" secret/catalyst-pin-broker-credentials)" ] \
  || fail "1: the realm's catalyst-pin secret and Secret catalyst-pin-broker-credentials differ"
echo "PASS 1: two salts give ten different secrets (8 clients, pin broker, per-Org broker)"

# 2. render-stable
cmp -s "$TMP/a.txt" "$TMP/a2.txt" || fail "2: one salt rendered different secrets twice"
echo "PASS 2: one salt renders the same secrets every time"

# 3. an install by Flux without a salt is refused; a render without a cluster is not
if render "$TMP/none.yaml" "$FLUX"; then
  fail "3: an install by Flux with sso.requireSalt=true and no salt rendered"
fi
grep -q 'sso.requireSalt is true and sso.tier1ClientSecretSalt is empty' "$TMP/none.yaml.err" \
  || fail "3: the refusal does not say why: $(cat "$TMP/none.yaml.err")"
grep -q 'SEC-060' "$TMP/none.yaml.err" || fail "3: the refusal does not name SEC-060"
render "$TMP/kit.yaml" || fail "3: the render without a cluster failed: $(cat "$TMP/kit.yaml.err")"
echo "PASS 3: an install by Flux with sso.requireSalt=true and no salt fails; the render without a cluster does not"

# 4. an upgrade of a Sovereign installed before the salt keeps its secrets
render "$TMP/up.yaml" "$FLUX" --is-upgrade || fail "4: an upgrade without a salt was refused: $(cat "$TMP/up.yaml.err")"
helm template keycloak "$CHART_DIR" --namespace keycloak --set sovereignFQDN=hw00.omani.works \
  --set "tenantRealms[0].slug=acme" --set keycloak.postgresql.enabled=false --set keycloak.externalDatabase.host=pg.test --set keycloak.externalDatabase.existingSecret=keycloak-database-secret >"$TMP/old.yaml" 2>/dev/null || fail "4: the render without requireSalt failed"
secrets "$TMP/up.yaml" >"$TMP/up.txt"
secrets "$TMP/old.yaml" >"$TMP/old.txt"
cmp -s "$TMP/up.txt" "$TMP/old.txt" || fail "4: the upgrade without a salt changed a secret"
[ "$(value "$TMP/up.txt" idp/catalyst-pin)" = "$(sha 'keycloak|keycloak|catalyst-pin-broker')" ] \
  || fail "4: the unsalted pin-broker secret is not the SHA-256 of its old seed"
[ "$(value "$TMP/a.txt" idp/catalyst-pin)" = "$(sha "keycloak|keycloak|catalyst-pin-broker|$SALT_A")" ] \
  || fail "4: the salted pin-broker secret is not the SHA-256 of <seed>|<salt>"
echo "PASS 4: an upgrade without a salt renders the secrets of before; with a salt the pin seed is <seed>|<salt>"

# 5. the host bridge of bp-catalyst-platform derives the same two secrets
bridge() {
  helm template catalyst-platform "$PLATFORM_CHART" --namespace catalyst-system \
    --set global.sovereignFQDN=hw00.omani.works \
    --set sso.requireSalt=true "$@" \
    --show-only templates/catalyst-keycloak-credentials-host-bridge.yaml 2>/dev/null
}
bridge --set "sso.tier1ClientSecretSalt=$SALT_A" >"$TMP/bridge-a.yaml" || fail "5: the host bridge with salt A did not render"
bridge "$FLUX" --is-upgrade >"$TMP/bridge-up.yaml" || fail "5: the host bridge upgrade without a salt did not render"
# same_as_bridge KEYCLOAK_VALUES BRIDGE_RENDER
same_as_bridge() {
  local s got
  for s in catalyst-kc-sa-credentials catalyst-pin-broker-credentials; do
    got=$(yq -r "select(.kind == \"Secret\" and .metadata.name == \"$s\") | .stringData[\"client-secret\"]" "$TMP/$2")
    [ -n "$got" ] && [ "$got" = "$(value "$TMP/$1" "secret/$s")" ] \
      || fail "5: $s of the host bridge ($2) differs from bp-keycloak's ($1)"
  done
}
same_as_bridge a.txt bridge-a.yaml
same_as_bridge up.txt bridge-up.yaml
echo "PASS 5: the host bridge derives the catalyst-api-server and pin-broker secrets byte-equal, salted and unsalted"

echo "=== ALL TESTS PASS ==="
