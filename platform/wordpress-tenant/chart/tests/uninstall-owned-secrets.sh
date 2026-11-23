#!/usr/bin/env bash
# bp-wordpress-tenant — the Secrets an Application writes go with it.
#
# On hw307 (23 November 2026, J5 of check FND-G12) the uninstall of a
# two-region WordPress Application left two Secrets in its namespace in BOTH
# regions:
#   <release>-media-s3            carried helm.sh/resource-policy: keep
#   <pair>-replication-ca-seed    written by cert-manager from the seed
#                                 Certificate; cert-manager never deletes it
#                                 (no --enable-certificate-owner-ref)
#
# Cases:
#   U1  the media-s3 Secret is rendered, and without helm.sh/resource-policy,
#       so Helm deletes it on uninstall and when it leaves the manifest.
#   U2  the primary renders the seed CA Secret <pair>-replication-ca-seed
#       itself (the Certificate's secretName, in its namespace), so Helm
#       deletes it with the Certificate on uninstall and on a Primary move.
#       On the first install it is Opaque and has no key: the seed PushSecret
#       finds no tls.crt and tries again until cert-manager wrote it, so no
#       empty CA reaches the write-once store.
#   U3  the replica renders neither the seed CA Secret nor its Certificate.
#
# Vacuity: U2's body against the replica render must FAIL.
#
# Usage: bash tests/uninstall-owned-secrets.sh [CHART_DIR]
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cd "$CHART_DIR"

if [ ! -d charts ] || [ "$(ls charts 2>/dev/null | wc -l)" -lt 2 ]; then
  helm dependency build >/dev/null
fi

A="hw-me-east-215-a-rtz-prod"
B="hw-me-east-215-b-rtz-prod"
COMMON=(
  --namespace acme
  --set smeDomain=acme.example.local
  --set adminUser.email=admin@acme.example.local
  --api-versions postgresql.cnpg.io/v1
  --api-versions cilium.io/v2
  --api-versions external-secrets.io/v1alpha1
  --api-versions external-secrets.io/v1beta1
  --api-versions cert-manager.io/v1
  --set openova.topology.mode=active-hot-standby
  --set database.topology.promotion.mechanism=manual
)

fails=0
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; fails=$((fails + 1)); }

helm template wordpress-rtz-a . "${COMMON[@]}" \
  --set openova.topology.self="$A" --set openova.topology.primary="$A" --set "openova.topology.peers={$B}" \
  > "$TMP/primary.yaml"
helm template wordpress-rtz-b . "${COMMON[@]}" \
  --set openova.topology.self="$B" --set openova.topology.primary="$A" --set "openova.topology.peers={$A}" \
  > "$TMP/replica.yaml"

# U1
media="$(yq ea '[select(.kind == "Secret" and (.metadata.name | test("-media-s3$")))] | length' "$TMP/primary.yaml")"
media_keep="$(yq ea '[select(.kind == "Secret" and (.metadata.name | test("-media-s3$")) and .metadata.annotations["helm.sh/resource-policy"] != null)] | length' "$TMP/primary.yaml")"
if [ "$media" = "1" ] && [ "$media_keep" = "0" ]; then
  pass "U1 one media-s3 Secret, without helm.sh/resource-policy"
else
  fail "U1 want one media-s3 Secret without helm.sh/resource-policy, got $media Secret(s), $media_keep with the policy"
fi

# U2 body: prints nothing when the seed CA Secret is rendered as the
# Certificate's own Secret, else the reason.
seed_secret_fault() {  # $1 render
  local cert ns name type secret_ns
  cert="$(yq ea '[select(.kind == "Certificate" and (.metadata.name | test("-replication-ca-seed$")))] | length' "$1")"
  if [ "$cert" != "1" ]; then
    echo "want one seed CA Certificate, got $cert"
    return
  fi
  name="$(yq ea 'select(.kind == "Certificate" and (.metadata.name | test("-replication-ca-seed$"))) | .spec.secretName' "$1")"
  ns="$(yq ea 'select(.kind == "Certificate" and (.metadata.name | test("-replication-ca-seed$"))) | .metadata.namespace' "$1")"
  type="$(yq ea "select(.kind == \"Secret\" and .metadata.name == \"$name\") | .type" "$1")"
  secret_ns="$(yq ea "select(.kind == \"Secret\" and .metadata.name == \"$name\") | .metadata.namespace" "$1")"
  keys="$(yq ea "select(.kind == \"Secret\" and .metadata.name == \"$name\") | (.data // {}) | keys | length" "$1")"
  if [ "$type" != "Opaque" ] || [ "$secret_ns" != "$ns" ]; then
    echo "the Secret $name of the seed Certificate is not rendered as an Opaque Secret in $ns (type '$type', namespace '$secret_ns')"
  elif [ "$keys" != "0" ]; then
    # On the first install the PushSecret must find no key and try again,
    # never push an empty CA into the write-once store.
    echo "the Secret $name carries $keys key(s) on the first install"
  fi
}

f="$(seed_secret_fault "$TMP/primary.yaml")"
if [ -z "$f" ]; then pass "U2 the primary renders the seed CA Secret, so Helm deletes it with the Certificate"; else fail "U2 $f"; fi

# Vacuity: the replica renders no seed, so U2's body must fail there.
f="$(seed_secret_fault "$TMP/replica.yaml")"
if [ -n "$f" ]; then pass "U2 vacuity: the body fails on the replica render ($f)"; else fail "U2 vacuity: the body passes on a render without a seed"; fi

# U3
left="$(yq ea '[select((.kind == "Secret" or .kind == "Certificate") and (.metadata.name | test("-replication-ca-seed$")))] | length' "$TMP/replica.yaml")"
if [ "$left" = "0" ]; then pass "U3 the replica renders no seed CA Secret or Certificate"; else fail "U3 the replica renders $left seed CA object(s)"; fi

if [ "$fails" -gt 0 ]; then
  echo "uninstall-owned-secrets: $fails failure(s)"
  exit 1
fi
echo "uninstall-owned-secrets: all cases pass"
