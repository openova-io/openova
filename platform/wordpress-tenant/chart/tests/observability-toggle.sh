#!/usr/bin/env bash
# bp-wordpress-tenant observability-toggle integration test (issue #182).
#
# Verifies docs/BLUEPRINT-AUTHORING.md §11.2 (Observability toggles must
# default false): a fresh-Sovereign install of bp-wordpress-tenant must
# NOT render a `monitoring.coreos.com/v1` ServiceMonitor or PodMonitor
# by default — those CRDs ship with kube-prometheus-stack which
# depends on the bootstrap-kit (circular dependency on a fresh
# Sovereign).
#
# Usage: bash tests/observability-toggle.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"
# Skip helm dep build when charts/ is already vendored (CI populates
# it before this step runs, and re-running on CI without `helm repo
# add` fails). Pattern lifted from bp-cilium tests/observability-toggle.sh.
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

# bp-wordpress-tenant requires several values with no sensible default
# (smeDomain, keycloak.realmURL, keycloak.clientSecretName, adminUser.email);
# we supply minimal stubs so the render proceeds.
COMMON_SET=(
  --set "smeDomain=acme.example.local"
  --set "keycloak.realmURL=https://auth.acme.example.local/realms/sme"
  --set "keycloak.clientSecretName=wordpress-oidc"
  --set "adminUser.email=admin@acme.example.local"
)

echo "[observability-toggle] Case 1: default render produces no PodMonitor / ServiceMonitor"
helm template smoke-wp . "${COMMON_SET[@]}" > "$TMP/default.yaml"
if grep -qE "^kind: (PodMonitor|ServiceMonitor)$" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-wordpress-tenant contains a PodMonitor/ServiceMonitor CR." >&2
  echo "      docs/BLUEPRINT-AUTHORING.md §11.2 forbids this — observability toggles must default false." >&2
  grep -nE "^kind: (PodMonitor|ServiceMonitor)$" "$TMP/default.yaml" >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] Case 2: explicit serviceMonitor.enabled=false renders cleanly"
if ! helm template smoke-wp . "${COMMON_SET[@]}" \
    --set "serviceMonitor.enabled=false" \
    > "$TMP/off.yaml" 2> "$TMP/off.err"; then
  echo "FAIL: explicit-off render failed:" >&2
  cat "$TMP/off.err" >&2
  exit 1
fi
if grep -qE "^kind: (PodMonitor|ServiceMonitor)$" "$TMP/off.yaml"; then
  echo "FAIL: explicit-off render still contains a PodMonitor/ServiceMonitor CR." >&2
  exit 1
fi
echo "  PASS"

# bp-wordpress-tenant doesn't currently render a ServiceMonitor template
# (it would require a wp-prometheus exporter sidecar — see values.yaml
# comment). The toggle is reserved for future use; this test still
# guarantees the default never produces one.

echo "[observability-toggle] All bp-wordpress-tenant observability-toggle gates green."
