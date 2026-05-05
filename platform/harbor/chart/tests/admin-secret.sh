#!/usr/bin/env bash
# bp-harbor admin-secret consumer-contract gate (issue #935).
#
# Verifies that the Catalyst-curated Harbor admin Secret renders with
# the shape bp-self-sovereign-cutover Step 02 (harbor-projects) Job
# depends on:
#
#   1. A Secret named `harbor-admin` (or .Values.harborAdmin.secretName)
#      renders in the chart's release namespace.
#   2. The Secret has key HARBOR_ADMIN_PASSWORD (the upstream Harbor
#      `existingSecretAdminPassword` contract; key configurable via
#      `existingSecretAdminPasswordKey`).
#   3. The Secret carries Reflector mirror annotations that propagate
#      it into the `catalyst` namespace (the cutover Job's runtime
#      namespace). reflection-allowed=true,
#      reflection-allowed-namespaces=catalyst,
#      reflection-auto-enabled=true,
#      reflection-auto-namespaces=catalyst.
#   4. The Secret carries helm.sh/resource-policy=keep so the
#      generated random password survives `helm uninstall`.
#   5. The upstream Harbor subchart consumes the Secret via
#      `existingSecretAdminPassword: harbor-admin` (the harbor-core
#      Deployment's HARBOR_ADMIN_PASSWORD env reads from harbor-admin,
#      not from the upstream-default `harbor-core` Secret).
#
# Background: on otech113 2026-05-05 the Step 02 Job in the `catalyst`
# namespace was hitting `secret "harbor-core" not found` because the
# upstream `harbor-core` Secret only exists in `harbor` and K8s forbids
# cross-namespace secretKeyRef. This gate guards against any future
# refactor that loses the Reflector annotations or renames the Secret.
#
# Usage: bash tests/admin-secret.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

helm template smoke-harbor . --namespace harbor > "$TMP/render.yaml"

# Extract the Catalyst-curated harbor-admin Secret (NOT the upstream
# subchart's harbor-core Secret). The umbrella's admin-secret.yaml
# template is the only place harbor-admin is emitted.
admin_block=$(awk '
  /^# Source: bp-harbor\/templates\/admin-secret\.yaml$/ { capture=1 }
  capture { print }
  capture && /^---$/ && NR > 1 { capture=0 }
' "$TMP/render.yaml")

if [ -z "$admin_block" ]; then
  echo "FAIL: harbor admin-secret template did not render" >&2
  exit 1
fi

echo "[admin-secret] Case 1: harbor-admin Secret renders in harbor ns"
if ! printf '%s\n' "$admin_block" | grep -q '^  name: harbor-admin$'; then
  echo "FAIL: harbor-admin Secret name not found in admin-secret render" >&2
  exit 1
fi
if ! printf '%s\n' "$admin_block" | grep -q '^  namespace: harbor$'; then
  echo "FAIL: harbor-admin Secret not in harbor namespace" >&2
  exit 1
fi
echo "  PASS"

echo "[admin-secret] Case 2: HARBOR_ADMIN_PASSWORD key present"
if ! printf '%s\n' "$admin_block" | grep -q '^  HARBOR_ADMIN_PASSWORD:'; then
  echo "FAIL: HARBOR_ADMIN_PASSWORD key missing — upstream chart contract requires this key" >&2
  exit 1
fi
echo "  PASS"

echo "[admin-secret] Case 3: Reflector mirror annotations present (allowed-namespaces=catalyst)"
for anno in \
    'reflector.v1.k8s.emberstack.com/reflection-allowed: "true"' \
    'reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces: "catalyst"' \
    'reflector.v1.k8s.emberstack.com/reflection-auto-enabled: "true"' \
    'reflector.v1.k8s.emberstack.com/reflection-auto-namespaces: "catalyst"' ; do
  if ! printf '%s\n' "$admin_block" | grep -qF "${anno}"; then
    echo "FAIL: missing annotation: ${anno}" >&2
    exit 1
  fi
done
echo "  PASS"

echo "[admin-secret] Case 4: helm.sh/resource-policy: keep present (survive uninstall)"
if ! printf '%s\n' "$admin_block" | grep -q 'helm.sh/resource-policy: keep'; then
  echo "FAIL: missing helm.sh/resource-policy: keep — uninstall would lose the password" >&2
  exit 1
fi
echo "  PASS"

echo "[admin-secret] Case 5: upstream chart consumes harbor-admin via existingSecretAdminPassword"
# The upstream Harbor chart references the existingSecretAdminPassword
# value as a secretKeyRef on the harbor-core Deployment's
# HARBOR_ADMIN_PASSWORD env. With the umbrella's
# `existingSecretAdminPassword: harbor-admin` setting, the env's
# secretKeyRef.name MUST resolve to harbor-admin (NOT the upstream-
# default harbor-core).
if ! grep -B1 -A3 '^          - name: HARBOR_ADMIN_PASSWORD$' "$TMP/render.yaml" | grep -A2 'secretKeyRef:' | grep -q 'name: harbor-admin'; then
  echo "FAIL: harbor-core Deployment does NOT consume harbor-admin via existingSecretAdminPassword" >&2
  echo "      the upstream Harbor `harbor-core` Secret will be re-emitted with the legacy default password" >&2
  exit 1
fi
echo "  PASS"

echo "[admin-secret] All gates green."
