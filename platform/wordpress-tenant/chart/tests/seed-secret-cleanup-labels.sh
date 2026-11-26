#!/usr/bin/env bash
# bp-wordpress-tenant — the seed CA Secret carries the labels the cleanup
# policy selects (check FND-G12, journey J5.2).
#
# On hw307 (23 November 2026) the Secret <pair>-replication-ca-seed stayed in
# the Organization namespace after every uninstall: cert-manager writes it
# from the seed Certificate and never deletes it. The DeletingPolicy
# replication-ca-seed-orphan (platform/kyverno-policies) deletes it once its
# Application has no HelmRelease in the Secret's namespace; the Secret must
# say which Application it belongs to, through the Certificate's
# secretTemplate.
#
# Cases:
#   L1  the primary's seed Certificate has secretTemplate labels (labels only)
#       catalyst.openova.io/component=replication-ca-seed and
#       catalyst.openova.io/app=<openova.application.name>;
#   L2  those labels are what the policy selects: its matchLabels and its
#       Exists key, rendered from platform/kyverno-policies;
#   L3  no openova.application.name: no secretTemplate (the HelmRelease
#       cannot be identified, so the Secret must never be selected);
#   L4  the seed outside the release namespace (database.cluster.namespace),
#       or openova.application.namespace not the release namespace: no
#       secretTemplate (the policy would look for the HelmRelease in the
#       wrong namespace and delete a live seed).
#
# Vacuity: L1's body against the L3 render must FAIL.
#
# Usage: bash tests/seed-secret-cleanup-labels.sh [CHART_DIR]
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cd "$CHART_DIR"

if [ "$(find charts -mindepth 1 -maxdepth 1 2>/dev/null | wc -l)" -lt 2 ]; then
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
  --set "openova.topology.self=$A"
  --set "openova.topology.primary=$A"
  --set "openova.topology.peers={$B}"
  --set database.topology.promotion.mechanism=manual
)
APP=(--set openova.application.name=blog --set openova.application.namespace=acme)

helm template blog-rtz-a . "${COMMON[@]}" "${APP[@]}" > "$TMP/primary.yaml"
helm template wordpress-rtz-a . "${COMMON[@]}" > "$TMP/noapp.yaml"
helm template blog-rtz-a . "${COMMON[@]}" "${APP[@]}" --set database.cluster.namespace=elsewhere > "$TMP/otherns.yaml"
helm template blog-rtz-a . "${COMMON[@]}" --set openova.application.name=blog --set openova.application.namespace=other > "$TMP/appns.yaml"

# The policy, rendered from its own chart.
KP="$(cd "$CHART_DIR/../../kyverno-policies/chart" && pwd)"
if ! ls "$KP"/charts/kube-access-lib-*.tgz >/dev/null 2>&1; then
  helm dependency build "$KP" >/dev/null
fi
helm template kyverno-policies "$KP" --show-only templates/cleanup/replication-ca-seed-orphan.yaml > "$TMP/policy.yaml"

fails=0
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; fails=$((fails + 1)); }

# cert <render> <expression>: the expression on the one seed Certificate.
cert() {
  yq ea '[select(.kind == "Certificate" and (.metadata.name | test("-replication-ca-seed$")))] | .[0] | '"$2" "$1"
}

# L1 body: prints nothing when the render's seed Certificate carries exactly
# the two labels for Application $2 and no annotations, else the reason.
labels_fault() {  # $1 render $2 application
  local n c a k ann
  n="$(yq ea '[select(.kind == "Certificate" and (.metadata.name | test("-replication-ca-seed$")))] | length' "$1")"
  c="$(cert "$1" '.spec.secretTemplate.labels["catalyst.openova.io/component"]')"
  a="$(cert "$1" '.spec.secretTemplate.labels["catalyst.openova.io/app"]')"
  k="$(cert "$1" '.spec.secretTemplate.labels | length')"
  ann="$(cert "$1" '.spec.secretTemplate.annotations')"
  if [ "$n" != "1" ] || [ "$c" != "replication-ca-seed" ] || [ "$a" != "$2" ] || [ "$k" != "2" ] || [ "$ann" != "null" ]; then
    echo "want one seed Certificate with secretTemplate labels {component: replication-ca-seed, app: $2} only, got $n Certificate(s), component=$c app=$a labels=$k annotations=$ann"
  fi
}

f="$(labels_fault "$TMP/primary.yaml" blog)"
if [ -z "$f" ]; then pass "L1 the seed Certificate labels its Secret with the Application"; else fail "L1 $f"; fi

f="$(labels_fault "$TMP/noapp.yaml" blog)"
if [ -n "$f" ]; then pass "L1 vacuity: the body fails on a render without the Application identity"; else fail "L1 vacuity: the body passes without openova.application.name"; fi

pc="$(yq '.spec.matchConstraints.objectSelector.matchLabels["catalyst.openova.io/component"]' "$TMP/policy.yaml")"
pm="$(yq '.spec.matchConstraints.objectSelector.matchLabels | length' "$TMP/policy.yaml")"
pk="$(yq '.spec.matchConstraints.objectSelector.matchExpressions[0].key + " " + .spec.matchConstraints.objectSelector.matchExpressions[0].operator' "$TMP/policy.yaml")"
cc="$(cert "$TMP/primary.yaml" '.spec.secretTemplate.labels["catalyst.openova.io/component"]')"
if [ "$pc" = "$cc" ] && [ "$pm" = "1" ] && [ "$pk" = "catalyst.openova.io/app Exists" ]; then
  pass "L2 the policy selects component=$pc and the Application label the Certificate writes"
else
  fail "L2 the policy selects component=$pc ($pm matchLabels) and '$pk', the Certificate writes component=$cc"
fi

for r in noapp otherns appns; do
  t="$(cert "$TMP/$r.yaml" '.spec.secretTemplate')"
  n="$(yq ea '[select(.kind == "Certificate" and (.metadata.name | test("-replication-ca-seed$")))] | length' "$TMP/$r.yaml")"
  if [ "$n" = "1" ] && [ "$t" = "null" ]; then pass "L3/L4 $r: the seed Certificate renders without secretTemplate"; else fail "L3/L4 $r: $n seed Certificate(s), secretTemplate=$t"; fi
done

if [ "$fails" -ne 0 ]; then echo "FAIL: bp-wordpress-tenant seed Secret cleanup labels ($fails)"; exit 1; fi
echo "PASS: bp-wordpress-tenant seed Secret carries the cleanup policy's labels only where its HelmRelease lives (yq $(yq --version | awk '{print $NF}'))"
