#!/usr/bin/env bash
# bp-external-secrets observability-toggle integration test (issue #182).
#
# Verifies the Catalyst rule from docs/BLUEPRINT-AUTHORING.md §11.2
# (Observability toggles must default false): a fresh-Sovereign install
# of bp-external-secrets must NOT render `monitoring.coreos.com/v1`
# ServiceMonitor by default — those CRDs ship with kube-prometheus-stack
# which depends on the bootstrap-kit (circular dependency on a fresh
# Sovereign).
#
# Usage: bash tests/observability-toggle.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"
# See bp-cilium tests/observability-toggle.sh for rationale: skip helm
# dep build when charts/ is already vendored (CI populates it before
# this step runs, and re-running on CI without `helm repo add` fails).
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

echo "[observability-toggle] Case 1: default render produces no ServiceMonitor"
helm template smoke-eso . > "$TMP/default.yaml"
if grep -q "monitoring.coreos.com" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-external-secrets contains monitoring.coreos.com references." >&2
  echo "      docs/BLUEPRINT-AUTHORING.md §11.2 forbids this — observability toggles must default false." >&2
  grep -n "monitoring.coreos.com" "$TMP/default.yaml" | head -5 >&2
  exit 1
fi
if grep -q "kind: ServiceMonitor" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-external-secrets contains kind: ServiceMonitor." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] Case 2: opt-in (serviceMonitor.enabled=true) renders cleanly"
# Upstream ESO chart gates the ServiceMonitor template behind
# `.Capabilities.APIVersions.Has "monitoring.coreos.com/v1"`, so we must
# pass --api-versions on this opt-in render to simulate a cluster on which
# kube-prometheus-stack has installed the CRD. Without --api-versions
# the template is skipped (as it correctly is on a fresh Sovereign).
if ! helm template smoke-eso . \
    --set "external-secrets.serviceMonitor.enabled=true" \
    --api-versions "monitoring.coreos.com/v1" \
    > "$TMP/optin.yaml" 2> "$TMP/optin.err"; then
  echo "FAIL: opt-in render failed:" >&2
  cat "$TMP/optin.err" >&2
  exit 1
fi
if ! grep -q "kind: ServiceMonitor" "$TMP/optin.yaml"; then
  echo "FAIL: opt-in render did NOT produce a ServiceMonitor — the toggle is broken." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] Case 3: explicit serviceMonitor.enabled=false renders cleanly"
if ! helm template smoke-eso . \
    --set "external-secrets.serviceMonitor.enabled=false" \
    > "$TMP/off.yaml" 2> "$TMP/off.err"; then
  echo "FAIL: explicit-off render failed:" >&2
  cat "$TMP/off.err" >&2
  exit 1
fi
if grep -q "monitoring.coreos.com" "$TMP/off.yaml"; then
  echo "FAIL: explicit-off render still contains monitoring.coreos.com references." >&2
  exit 1
fi
echo "  PASS"

# ── Case 4: default ClusterSecretStore present, opt-out works ────────────
echo "[observability-toggle] Case 4: default render includes vault-region1 ClusterSecretStore (post-install hook)"
# The Catalyst-curated wrapper must ship a default ClusterSecretStore CR
# wired to bp-openbao — distinct from the upstream chart's ClusterSecretStore
# CRD definition. Match the CR's name field at the metadata indent level.
if ! grep -qE "^kind: ClusterSecretStore$" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-external-secrets is missing the vault-region1 ClusterSecretStore CR." >&2
  echo "      The Catalyst-curated wrapper must ship a default ClusterSecretStore wired to bp-openbao." >&2
  exit 1
fi
if ! grep -q "name: \"vault-region1\"" "$TMP/default.yaml"; then
  echo "FAIL: default render of bp-external-secrets is missing the vault-region1 name." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] Case 5: clusterSecretStore.enabled=false omits the default ClusterSecretStore"
if ! helm template smoke-eso . \
    --set "clusterSecretStore.enabled=false" \
    > "$TMP/css-off.yaml" 2> "$TMP/css-off.err"; then
  echo "FAIL: clusterSecretStore.enabled=false render failed:" >&2
  cat "$TMP/css-off.err" >&2
  exit 1
fi
if grep -qE "^kind: ClusterSecretStore$" "$TMP/css-off.yaml"; then
  # Note: an `^kind: ClusterSecretStore$` line at column 0 is a CR; the
  # upstream chart's CRD definition mentions ClusterSecretStore inside
  # the `kind:` field of the CRD spec but at non-zero indentation.
  echo "FAIL: clusterSecretStore.enabled=false still renders a ClusterSecretStore CR — the toggle is broken." >&2
  exit 1
fi
echo "  PASS"

echo "[observability-toggle] All bp-external-secrets observability-toggle gates green."
