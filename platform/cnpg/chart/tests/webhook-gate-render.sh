#!/usr/bin/env bash
# bp-cnpg — #4220 render gate for the webhook readiness-gate skip in
# webhook-LESS mode.
#
# A per-Org bp-cnpg install runs webhook-less (#4143/#4201:
# cloudnative-pg.webhook.{mutating,validating}.create=false) so the per-Org
# operator never fights the platform cnpg-system operator for the
# cluster-singleton webhook configs. In that mode the G53 webhook-gate Job +
# RBAC and the G21 webhook-cert bootstrap MUST NOT render — the gate Job is
# created directly by Helm with no Flux owner, so the flux-managed Kyverno
# Enforce policy DENIES it and the bp-cnpg HelmRelease InstallFailed, wedging
# the whole Org cascade (live omantel.biz dep 4635277cae4ffed9, #4220).
#
# This test asserts:
#   1. DEFAULT (single-operator, webhook configs created): the webhook-gate Job
#      renders (gate behaviour preserved — no regression).
#   2. WEBHOOK-LESS (both webhook.create=false): ZERO webhook-gate Job, ZERO
#      gate RBAC, ZERO cert-bootstrap resources.
#   3. EXPLICIT webhookGate.enabled=false: ZERO gate even with webhook configs on.
#
# Usage: bash tests/webhook-gate-render.sh [CHART_DIR]
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Build deps if the upstream subchart isn't vendored yet (CI builds before
# tests, but support a clean local checkout too).
if [ ! -d "$CHART_DIR/charts" ] || ! ls "$CHART_DIR"/charts/cloudnative-pg-*.tgz >/dev/null 2>&1; then
  helm dependency build "$CHART_DIR" >/dev/null 2>&1 || true
fi

render() {
  # $1 = output file, rest = extra --set args
  local out="$1"; shift
  helm template bp-cnpg "$CHART_DIR" \
    --namespace org-demo \
    "$@" > "$out" 2>"$TMP/err" || { echo "helm template FAILED:"; cat "$TMP/err"; exit 1; }
}

fail() { echo "FAIL: $1"; exit 1; }

# ── Case 1: DEFAULT — webhook configs created → gate Job MUST render ─────────
render "$TMP/default.yaml"
if ! grep -q "bp-cnpg-webhook-gate" "$TMP/default.yaml"; then
  fail "default render is missing the webhook-gate Job (regression — single-operator installs still need the gate)"
fi
echo "PASS: default render emits the webhook-gate Job"

# ── Case 2: WEBHOOK-LESS — both webhook.create=false → ZERO gate/cert ────────
render "$TMP/webhookless.yaml" \
  --set "cloudnative-pg.webhook.mutating.create=false" \
  --set "cloudnative-pg.webhook.validating.create=false"
if grep -q "webhook-gate" "$TMP/webhookless.yaml"; then
  echo "--- offending render ---"; grep -n "webhook-gate" "$TMP/webhookless.yaml" | head
  fail "webhook-less render STILL emits webhook-gate resources (flux-managed will deny the un-owned hook Job → #4220)"
fi
if grep -qE "cnpg-webhook-cert-wait|cnpg-webhook-bootstrap-issuer|cnpg-webhook-cert-bootstrap" "$TMP/webhookless.yaml"; then
  fail "webhook-less render STILL emits webhook-cert-bootstrap resources"
fi
echo "PASS: webhook-less render emits ZERO webhook-gate + ZERO cert-bootstrap resources"

# ── Case 3: EXPLICIT webhookGate.enabled=false → ZERO gate ──────────────────
render "$TMP/explicit-off.yaml" --set "webhookGate.enabled=false"
if grep -q "webhook-gate" "$TMP/explicit-off.yaml"; then
  fail "webhookGate.enabled=false STILL emits the gate Job"
fi
echo "PASS: explicit webhookGate.enabled=false suppresses the gate"

echo "All bp-cnpg webhook-gate render assertions passed (#4220)."
