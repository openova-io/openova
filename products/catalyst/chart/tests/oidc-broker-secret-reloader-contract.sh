#!/usr/bin/env bash
# bp-catalyst-platform — catalyst-api OIDC-broker-secret Reloader watch contract gate (#5277).
#
# Guards the cold-broker SSO 404 root cause diagnosed on hw278 (cutoverComplete=true):
# ten UAT rows (harbor 32/111/181, openbao 33/113/183, guacamole 35/115,
# pdns-admin 36, hubble 39) failed SSO identically — the cold catalyst-pin OIDC
# broker roundtrip to api.<fqdn>/oidc/* returned HTTP 404, so Keycloak rendered
# "Unexpected error when authenticating with identity provider". WARM SSO (an
# existing realm session, which skips the broker) worked.
#
# Mechanism: catalyst-api wires CATALYST_PIN_BROKER_CLIENT_SECRET from the
# reflector-mirrored `catalyst-pin-broker-credentials` Secret via an
# `optional: true` valueFrom. main.go (cmd/api/main.go) registers the /oidc/*
# provider routes ONLY when that secret env is non-empty at process start. When
# catalyst-api boots — or is rolled by the cutover catalyst-api-env-patch step —
# before the Secret is reflected into catalyst-system, the value resolves "" and
# the routes are silently skipped for the Pod's lifetime, permanently 404-ing the
# cold broker path.
#
# The self-heal is Stakater Reloader: the Pod-template `secret.reloader.stakater.com/reload`
# annotation must list `catalyst-pin-broker-credentials` so the Deployment rolls
# when the Secret lands (identical idiom already used for handover-jwt-public — the
# SOVEREIGN_FQDN race, otech62 — and provisioning-github-token — #3122, hw104).
# It was omitted; this gate makes any future drop of that watch (or of the env
# wiring it protects) fail Blueprint Release publish BEFORE the OCI artifact
# reaches a Sovereign.
#
# Test framework: pure `helm template` + grep on rendered YAML, matching the
# established tests/sovereign-fqdn-lb-ip-contract.sh pattern. Picked up
# automatically by .github/workflows/blueprint-release.yaml.
#
# Usage: bash tests/oidc-broker-secret-reloader-contract.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

API_TEMPLATE="templates/api-deployment.yaml"
PIN_SECRET="catalyst-pin-broker-credentials"

helm template smoke . \
  --set global.sovereignFQDN=omantel.biz \
  --show-only "${API_TEMPLATE}" > "$TMP/api.yaml"

echo "[oidc-broker-reloader] Case 1: secret.reloader watch lists ${PIN_SECRET} (#5277)"
RELOAD_LINE="$(grep -E '^[[:space:]]*secret\.reloader\.stakater\.com/reload:' "$TMP/api.yaml" | head -1)"
if [ -z "${RELOAD_LINE}" ]; then
  echo "FAIL: catalyst-api Pod template carries NO secret.reloader.stakater.com/reload annotation — the boot-race self-heal for optional valueFrom secrets is gone (#5277 / #3122)" >&2
  exit 1
fi
if ! printf '%s' "${RELOAD_LINE}" | grep -q "${PIN_SECRET}"; then
  echo "FAIL: secret.reloader watch omits ${PIN_SECRET} — catalyst-api that booted before the pin-broker Secret was reflected will NEVER roll, so /oidc/* stays unwired and Keycloak's cold catalyst-pin broker 404s (hw278 SSO)" >&2
  echo "  rendered: ${RELOAD_LINE}" >&2
  exit 1
fi
echo "  PASS (${RELOAD_LINE##*reload: })"

echo "[oidc-broker-reloader] Case 2: the existing boot-race watches are NOT regressed by the additive fix"
for SECRET in handover-jwt-public provisioning-github-token; do
  if ! printf '%s' "${RELOAD_LINE}" | grep -q "${SECRET}"; then
    echo "FAIL: secret.reloader watch dropped ${SECRET} — a pre-existing boot-race self-heal (SOVEREIGN_FQDN / gitops-token) regressed" >&2
    echo "  rendered: ${RELOAD_LINE}" >&2
    exit 1
  fi
done
echo "  PASS (handover-jwt-public + provisioning-github-token still watched)"

echo "[oidc-broker-reloader] Case 3: the watched Secret is the SAME one CATALYST_PIN_BROKER_CLIENT_SECRET sources from (lockstep — no drift)"
if ! grep -q "CATALYST_PIN_BROKER_CLIENT_SECRET" "$TMP/api.yaml"; then
  echo "FAIL: catalyst-api no longer wires CATALYST_PIN_BROKER_CLIENT_SECRET — the /oidc/* provider secret env is gone; the Reloader watch protects nothing" >&2
  exit 1
fi
# The env's secretKeyRef.name must be the SAME secret the Reloader watches, or the
# self-heal targets the wrong resource and the cold-broker 404 reappears silently.
if ! awk '/CATALYST_PIN_BROKER_CLIENT_SECRET/,/key:/' "$TMP/api.yaml" | grep -q "name: ${PIN_SECRET}"; then
  echo "FAIL: CATALYST_PIN_BROKER_CLIENT_SECRET does not source from ${PIN_SECRET} — env/Reloader-watch names drifted; the #5277 self-heal watches a Secret the Pod does not read" >&2
  awk '/CATALYST_PIN_BROKER_CLIENT_SECRET/,/key:/' "$TMP/api.yaml" >&2 || true
  exit 1
fi
echo "  PASS (CATALYST_PIN_BROKER_CLIENT_SECRET ← ${PIN_SECRET}, matches the Reloader watch)"

echo "[oidc-broker-reloader] All gates green."
