#!/usr/bin/env bash
# bp-catalyst-platform — NATS + Valkey host-ns render contract gate (#4368 / #4373).
#
# Verifies that on a Sovereign render every NATS *and* Valkey endpoint default
# resolves to its HOST namespace Service
#
#   nats://nats-jetstream.nats-system.svc.cluster.local:4222
#   valkey-primary.valkey.svc.cluster.local:6379
#
# and NEVER to a dead plane-vCluster-syncer-mangled name
#
#   nats://nats-jetstream-x-nats-system-x-mgmt-vcluster.mgmt.svc.cluster.local:4222
#   valkey-primary-x-valkey-x-rtz-vcluster.rtz.svc.cluster.local:6379
#
# WHY THIS GATE EXISTS (#4368 / #4373, live kom4dc/omantel.biz dep
# 4635277cae4ffed9, 2026-06-26):
#
# #3373/#2697 once placed bp-nats-jetstream INSIDE the mgmt vCluster, so the
# chart defaults pointed at the bp-mgmt-vcluster syncer-mangled host name
# `nats-jetstream-x-nats-system-x-mgmt-vcluster.mgmt.svc`. The de-vcluster
# re-home (#4325/#4347/#4348) moved bp-nats-jetstream BACK to the host
# `nats-system` namespace — the mgmt vCluster, its `mgmt` namespace, and the
# mangled syncer Service no longer exist on a Sovereign. The mangled defaults
# were NOT reverted in this chart, so on the live Sovereign:
#   - org-services-config ConfigMap NATS_URL = the mangled name → EVERY
#     Organization service (provisioning/auth/billing/tenant/domain/notification)
#     CrashLooped on `nats connect: dial tcp: lookup ...-mgmt-vcluster...
#     no such host`,
#   - which took the marketplace FUNNEL door (POST /tenant/orgs) DOWN → no Org
#     creation/cart, blocking every funnel walk (#4364 cart install, #4322).
#
# Nothing in CI rendered the chart and asserted the NATS default points at the
# live host-ns Service. This gate fills that hole so any future regression (a
# placement re-home that re-mangles the name, a values default reverted, a typo)
# fails Blueprint Release publish BEFORE the OCI artifact reaches a Sovereign.
#
# Test framework: pure `helm template` + grep on rendered YAML, matching the
# established tests/sovereign-fqdn-lb-ip-contract.sh + tests/baseline-cnp-allowlist.sh
# pattern. Picked up automatically by .github/workflows/blueprint-release.yaml
# ("Run chart integration tests (chart/tests/*.sh)").
#
# Usage: bash tests/nats-url-host-ns-contract.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

HOST_NATS="nats://nats-jetstream.nats-system.svc.cluster.local:4222"
DEAD_NATS="nats-jetstream-x-nats-system-x-mgmt-vcluster"
HOST_VALKEY="valkey-primary.valkey.svc.cluster.local:6379"
DEAD_VALKEY="valkey-primary-x-valkey-x-rtz-vcluster"

CM_TEMPLATE="templates/org-services/configmap.yaml"
API_TEMPLATE="templates/api-deployment.yaml"
API_KUSTOMIZE_TEMPLATE="templates/api-deployment-kustomize.yaml"

echo "[nats-url] Case 1: org-services-config ConfigMap NATS_URL renders the HOST-ns name on a Sovereign (#4368/#4373)"
helm template smoke . \
  --set global.sovereignFQDN=omantel.biz \
  --set ingress.marketplace.enabled=true \
  --show-only "${CM_TEMPLATE}" > "$TMP/cm.yaml"

if ! grep -qE '^[[:space:]]*NATS_URL:[[:space:]]*"'"${HOST_NATS}"'"' "$TMP/cm.yaml"; then
  echo "FAIL: org-services-config ConfigMap NATS_URL did not render '${HOST_NATS}' — org-services will CrashLoop 'nats connect: no such host' → funnel door (POST /tenant/orgs) DOWN (#4368/#4373)" >&2
  grep -E '^[[:space:]]*NATS_URL:' "$TMP/cm.yaml" >&2 || true
  exit 1
fi
if grep -q "${DEAD_NATS}" "$TMP/cm.yaml"; then
  echo "FAIL: org-services-config ConfigMap still references the DEAD mgmt-vcluster-mangled NATS name '${DEAD_NATS}' — NXDOMAIN after #4325 de-vcluster (#4368/#4373)" >&2
  grep "${DEAD_NATS}" "$TMP/cm.yaml" >&2 || true
  exit 1
fi
echo "  PASS (NATS_URL=${HOST_NATS}; no mgmt-vcluster-mangled name)"

echo "[nats-url] Case 1b: org-services-config ConfigMap VALKEY_ADDR renders the HOST-ns name (same de-vcluster fallout, same funnel-blocking ConfigMap — #4368/#4373)"
# auth (a funnel-door dependency) dials Valkey at boot; the #4325 de-vcluster
# re-homed bp-valkey from the rtz vCluster back to host ns `valkey`, leaving the
# rtz-vcluster-mangled name NXDOMAIN → auth CrashLoops on `dial tcp: lookup
# ...-rtz-vcluster... no such host` exactly like the NATS case.
if ! grep -qE '^[[:space:]]*VALKEY_ADDR:[[:space:]]*"'"${HOST_VALKEY}"'"' "$TMP/cm.yaml"; then
  echo "FAIL: org-services-config ConfigMap VALKEY_ADDR did not render '${HOST_VALKEY}' — org-services/auth will CrashLoop 'no such host' on the dead rtz-vcluster name (#4368/#4373)" >&2
  grep -E '^[[:space:]]*VALKEY_ADDR:' "$TMP/cm.yaml" >&2 || true
  exit 1
fi
if grep -q "${DEAD_VALKEY}" "$TMP/cm.yaml"; then
  echo "FAIL: org-services-config ConfigMap still references the DEAD rtz-vcluster-mangled Valkey name '${DEAD_VALKEY}' — NXDOMAIN after #4325 de-vcluster (#4368/#4373)" >&2
  grep "${DEAD_VALKEY}" "$TMP/cm.yaml" >&2 || true
  exit 1
fi
echo "  PASS (VALKEY_ADDR=${HOST_VALKEY}; no rtz-vcluster-mangled name)"

echo "[nats-url] Case 2: catalyst-api CATALYST_NATS_URL default renders the HOST-ns name (#4368/#4373)"
helm template smoke-api . \
  --set global.sovereignFQDN=omantel.biz \
  --show-only "${API_TEMPLATE}" > "$TMP/api.yaml"

if ! grep -qE "value:[[:space:]]*'?${HOST_NATS}'?" "$TMP/api.yaml"; then
  echo "FAIL: catalyst-api CATALYST_NATS_URL default did not render '${HOST_NATS}' (#4368/#4373)" >&2
  grep -A6 'name: CATALYST_NATS_URL' "$TMP/api.yaml" | grep -E 'value:' >&2 || true
  exit 1
fi
if grep -qE "value:.*${DEAD_NATS}" "$TMP/api.yaml"; then
  echo "FAIL: catalyst-api CATALYST_NATS_URL default still renders the DEAD mgmt-vcluster-mangled name '${DEAD_NATS}' (#4368/#4373)" >&2
  exit 1
fi
echo "  PASS (CATALYST_NATS_URL default → ${HOST_NATS})"

echo "[nats-url] Case 3: api-deployment-kustomize twin carries the same host-ns default (render-split parity)"
# The kustomize twin renders only on the kustomize path; assert the source
# default directly so a drift between the two api-deployment templates is caught
# even when the twin is not selected by --show-only on this values set.
if grep -qE "${DEAD_NATS}" "${API_KUSTOMIZE_TEMPLATE}"; then
  echo "FAIL: ${API_KUSTOMIZE_TEMPLATE} still hardcodes the DEAD mgmt-vcluster-mangled NATS default (#4368/#4373)" >&2
  grep -nE "${DEAD_NATS}" "${API_KUSTOMIZE_TEMPLATE}" >&2 || true
  exit 1
fi
if ! grep -qF "${HOST_NATS}" "${API_KUSTOMIZE_TEMPLATE}"; then
  echo "FAIL: ${API_KUSTOMIZE_TEMPLATE} does not carry the host-ns NATS default '${HOST_NATS}' — drift vs api-deployment.yaml (#4368/#4373)" >&2
  exit 1
fi
echo "  PASS (api-deployment-kustomize.yaml default → ${HOST_NATS})"

echo "[nats-url] Case 4: no mangled NATS or Valkey default anywhere live in the chart source"
# Belt-and-braces: the whole chart source tree must carry ZERO live references
# to either mangled name. Comments documenting the HISTORY of the mangled names
# are allowed (they explain why the gate exists), so scope the scan to value
# lines (`url:`/`addr:`/`host:`/`value:`/`NATS_URL:`/`VALKEY_ADDR:`) rather than
# full-text.
SCAN_KEYS='(url|addr|host|value|NATS_URL|VALKEY_ADDR):'
if grep -rnE "${SCAN_KEYS}.*${DEAD_NATS}" templates/ values.yaml >/dev/null 2>&1; then
  echo "FAIL: a live chart value still points at the DEAD mgmt-vcluster-mangled NATS name (#4368/#4373)" >&2
  grep -rnE "${SCAN_KEYS}.*${DEAD_NATS}" templates/ values.yaml >&2 || true
  exit 1
fi
if grep -rnE "${SCAN_KEYS}.*${DEAD_VALKEY}" templates/ values.yaml >/dev/null 2>&1; then
  echo "FAIL: a live chart value still points at the DEAD rtz-vcluster-mangled Valkey name (#4368/#4373)" >&2
  grep -rnE "${SCAN_KEYS}.*${DEAD_VALKEY}" templates/ values.yaml >&2 || true
  exit 1
fi
echo "  PASS (no live mgmt-vcluster / rtz-vcluster mangled NATS or Valkey value in templates/ or values.yaml)"

echo "[nats-url] ALL CASES PASS — NATS + Valkey defaults resolve to their host-ns Services post-#4325 de-vcluster."
