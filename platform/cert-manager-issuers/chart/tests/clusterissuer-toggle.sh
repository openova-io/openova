#!/usr/bin/env bash
# bp-cert-manager-issuers ClusterIssuer toggle integration test (#3734).
#
# Sister test to platform/external-secrets-stores/chart/tests/
# clustersecretstore-toggle.sh. Verifies the chart split shipped for #3734:
#   - default render produces the letsencrypt-http01-prod ClusterIssuer CR
#     (http01 default-enabled) but NOT letsencrypt-dns01-prod (dns01
#     default-disabled)
#   - dns01.enabled=true adds the letsencrypt-dns01-prod ClusterIssuer
#   - http01.enabled=false suppresses letsencrypt-http01-prod
#   - the issuers render as PLAIN manifests, NOT Helm hooks (the whole point
#     of the split — the in-release post-install hook is what tripped the
#     helm-controller RESTMapper-cache race in bp-cert-manager)
#   - the pre-install crd-gate is present (belt-and-suspenders Established gate)
#   - acmeServer override propagates
#
# Mirrors the bp-external-secrets-stores / bp-crossplane-claims pattern.
#
# Usage: bash tests/clusterissuer-toggle.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

echo "[issuer-toggle] Case 1: default render includes http01 issuer, NOT dns01"
helm template smoke-cm-issuers . > "$TMP/default.yaml"
if ! grep -qE "^  name: letsencrypt-http01-prod$" "$TMP/default.yaml"; then
  echo "FAIL: default render is missing the letsencrypt-http01-prod ClusterIssuer (http01 defaults enabled)." >&2
  exit 1
fi
if grep -qE "^  name: letsencrypt-dns01-prod$" "$TMP/default.yaml"; then
  echo "FAIL: default render contains letsencrypt-dns01-prod, but dns01 defaults DISABLED." >&2
  exit 1
fi
echo "  PASS"

echo "[issuer-toggle] Case 2: issuers render as PLAIN manifests (no Helm hooks)"
# The ClusterIssuer documents must NOT carry helm.sh/hook annotations — the
# in-release post-install hook is the exact machinery that tripped the
# RESTMapper-cache race in bp-cert-manager (#3734). The crd-gate Job/RBAC
# legitimately carry hooks; those are NOT ClusterIssuers. So we split the
# render on the `---` document separator and, for each document whose `kind:`
# is ClusterIssuer, assert it carries no helm.sh/hook annotation. (Render the
# dns01 issuer too so BOTH issuers are covered by this gate.)
helm template smoke-cm-issuers . \
  --set "certManager.issuers.dns01.enabled=true" > "$TMP/both.yaml"
csplit -z -s -f "$TMP/doc-" -b '%03d.yaml' "$TMP/both.yaml" '/^---$/' '{*}' 2>/dev/null || true
hooked_issuer=0
for d in "$TMP"/doc-*.yaml; do
  [ -f "$d" ] || continue
  if grep -qE "^kind: ClusterIssuer$" "$d" && grep -qE "helm.sh/hook" "$d"; then
    echo "FAIL: a ClusterIssuer document carries a helm.sh/hook annotation — the split must ship them as plain manifests:" >&2
    echo "      $d" >&2
    hooked_issuer=1
  fi
done
if [ "$hooked_issuer" -ne 0 ]; then
  exit 1
fi
echo "  PASS"

echo "[issuer-toggle] Case 3: pre-install crd-gate present (belt-and-suspenders)"
if ! grep -q "cert-manager-issuers CRD gate" "$TMP/default.yaml"; then
  echo "FAIL: default render is missing the crd-gate Job (Established=True gate)." >&2
  exit 1
fi
if ! grep -qE '"helm.sh/hook": pre-install,pre-upgrade' "$TMP/default.yaml"; then
  echo "FAIL: crd-gate is not a pre-install hook (the CRD is provided by the already-Ready bp-cert-manager release, so the gate must be pre-install)." >&2
  exit 1
fi
echo "  PASS"

echo "[issuer-toggle] Case 4: dns01.enabled=true adds letsencrypt-dns01-prod"
if ! helm template smoke-cm-issuers . \
    --set "certManager.issuers.dns01.enabled=true" \
    > "$TMP/dns01.yaml" 2> "$TMP/dns01.err"; then
  echo "FAIL: dns01.enabled=true render failed:" >&2
  cat "$TMP/dns01.err" >&2
  exit 1
fi
if ! grep -qE "^  name: letsencrypt-dns01-prod$" "$TMP/dns01.yaml"; then
  echo "FAIL: dns01.enabled=true did NOT render the letsencrypt-dns01-prod ClusterIssuer." >&2
  exit 1
fi
echo "  PASS"

echo "[issuer-toggle] Case 5: http01.enabled=false omits letsencrypt-http01-prod"
if ! helm template smoke-cm-issuers . \
    --set "certManager.issuers.http01.enabled=false" \
    > "$TMP/http01-off.yaml" 2> "$TMP/http01-off.err"; then
  echo "FAIL: http01.enabled=false render failed:" >&2
  cat "$TMP/http01-off.err" >&2
  exit 1
fi
if grep -qE "^  name: letsencrypt-http01-prod$" "$TMP/http01-off.yaml"; then
  echo "FAIL: http01.enabled=false still renders letsencrypt-http01-prod — toggle broken." >&2
  exit 1
fi
echo "  PASS"

echo "[issuer-toggle] Case 6: acmeServer override propagates"
if ! helm template smoke-cm-issuers . \
    --set "certManager.issuers.acmeServer=https://acme-staging-v02.api.letsencrypt.org/directory" \
    > "$TMP/srv.yaml" 2> "$TMP/srv.err"; then
  echo "FAIL: acmeServer override render failed:" >&2
  cat "$TMP/srv.err" >&2
  exit 1
fi
if ! grep -q 'server: "https://acme-staging-v02.api.letsencrypt.org/directory"' "$TMP/srv.yaml"; then
  echo "FAIL: acmeServer override did not propagate into rendered ClusterIssuer." >&2
  exit 1
fi
echo "  PASS"

echo "[issuer-toggle] All bp-cert-manager-issuers toggle gates green."
