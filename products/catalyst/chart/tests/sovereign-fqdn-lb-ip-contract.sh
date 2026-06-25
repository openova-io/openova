#!/usr/bin/env bash
# bp-catalyst-platform — sovereign-fqdn ConfigMap LB/CP IP render contract gate.
#
# Verifies that `templates/sovereign-fqdn-configmap.yaml` renders the three
# load-balancer / control-plane IP fields (lbIP / consoleLBIP / controlPlaneIP)
# with the REAL tofu-output-derived values when those values reach the chart
# via the provision-time wiring chain:
#
#   tofu output (load_balancer_ip / console_load_balancer_ip)
#     → cloud-init postBuild.substitute (SOVEREIGN_LB_IP / SOVEREIGN_CONSOLE_LB_IP
#       / SOVEREIGN_CONTROL_PLANE_IP)
#     → bootstrap-kit slot-13 HR values (global.sovereignLBIP /
#       global.sovereignConsoleLBIP / sovereign.controlPlaneIP)
#     → this ConfigMap
#     → catalyst-api CATALYST_OTECH_INGRESS_IPV4 (= lbIP)
#       + org-controller CATALYST_TENANT_CONSOLE_LB_IPV4 (= consoleLBIP)
#
# This gate was created in response to issue #4330: on omantel.biz (dep
# 4635277cae4ffed9) the ConfigMap rendered ALL THREE IP fields EMPTY, so both
# CATALYST_OTECH_INGRESS_IPV4 and CATALYST_TENANT_CONSOLE_LB_IPV4 were empty and
# the org-controller halted per-Org DNS GLOBALLY with
# `tenant-dns: skipped — no console-ELB IPv4 configured`. console.<org>.<pool>
# and mail.<pool> were NXDOMAIN for every Org.
#
# Two provision-time gaps caused it:
#   1. SOVEREIGN_CONTROL_PLANE_IP was NEVER set in cloud-init → controlPlaneIP
#      rendered empty on EVERY Sovereign (even a fresh prov). Fixed in #4330 by
#      adding the substitute in infra/providers/_shared/cloudinit-control-plane.tftpl
#      sourced from load_balancer_ipv4 (the only CP-class IP uniformly known at
#      template-render time on both Hetzner + Huawei).
#   2. SOVEREIGN_LB_IP / SOVEREIGN_CONSOLE_LB_IP were wired late (#4240), so
#      Sovereigns provisioned before that rendered lbIP/consoleLBIP empty.
#
# Nothing in CI rendered the ConfigMap and asserted the IP fields are non-empty
# when the values are present, nor that the env wiring keys off them. This gate
# fills that hole so any future regression (a renamed key, a dropped value
# default, a wiring removal) fails Blueprint Release publish BEFORE the OCI
# artifact reaches a Sovereign.
#
# Test framework: pure `helm template` + grep/awk on rendered YAML, matching the
# established tests/baseline-cnp-allowlist.sh pattern. Picked up automatically by
# .github/workflows/blueprint-release.yaml ("Run chart integration tests
# (chart/tests/*.sh)").
#
# Usage: bash tests/sovereign-fqdn-lb-ip-contract.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

CM_TEMPLATE="templates/sovereign-fqdn-configmap.yaml"

# Representative tofu-output-derived IPs (omantel.biz / kom4dc dep
# 4635277cae4ffed9 — the live env #4330 was diagnosed on).
LB_IP="212.72.24.31"        # load_balancer_ip      → global.sovereignLBIP
CONSOLE_LB_IP="212.72.24.33" # console_load_balancer_ip → global.sovereignConsoleLBIP
CP_IP="212.72.24.31"        # control-plane IP (== load_balancer_ipv4 per #1844 intent)

echo "[sovereign-fqdn-ip] Case 1: ConfigMap renders all three IP fields NON-EMPTY when values present (#4330)"
helm template smoke . \
  --set global.sovereignFQDN=omantel.biz \
  --set global.sovereignLBIP="${LB_IP}" \
  --set global.sovereignConsoleLBIP="${CONSOLE_LB_IP}" \
  --set sovereign.controlPlaneIP="${CP_IP}" \
  --show-only "${CM_TEMPLATE}" > "$TMP/cm.yaml"

if ! grep -qE '^[[:space:]]*lbIP:[[:space:]]*"'"${LB_IP}"'"' "$TMP/cm.yaml"; then
  echo "FAIL: sovereign-fqdn ConfigMap lbIP did not render '${LB_IP}' — catalyst-api CATALYST_OTECH_INGRESS_IPV4 will be empty → per-Org DNS halted (#4330)" >&2
  grep -E '^[[:space:]]*lbIP:' "$TMP/cm.yaml" >&2 || true
  exit 1
fi
if ! grep -qE '^[[:space:]]*consoleLBIP:[[:space:]]*"'"${CONSOLE_LB_IP}"'"' "$TMP/cm.yaml"; then
  echo "FAIL: sovereign-fqdn ConfigMap consoleLBIP did not render '${CONSOLE_LB_IP}' — org-controller CATALYST_TENANT_CONSOLE_LB_IPV4 will be empty → 'tenant-dns: skipped' for every Org (#4330)" >&2
  grep -E '^[[:space:]]*consoleLBIP:' "$TMP/cm.yaml" >&2 || true
  exit 1
fi
if ! grep -qE '^[[:space:]]*controlPlaneIP:[[:space:]]*"'"${CP_IP}"'"' "$TMP/cm.yaml"; then
  echo "FAIL: sovereign-fqdn ConfigMap controlPlaneIP did not render '${CP_IP}' — Settings page controlPlaneIP blank (#4330 / #1844 intent never wired)" >&2
  grep -E '^[[:space:]]*controlPlaneIP:' "$TMP/cm.yaml" >&2 || true
  exit 1
fi
echo "  PASS (lbIP=${LB_IP}, consoleLBIP=${CONSOLE_LB_IP}, controlPlaneIP=${CP_IP} all rendered non-empty)"

echo "[sovereign-fqdn-ip] Case 2: empty values still render the keys (back-compat) but as empty strings — no template crash, env optional:true degrades cleanly"
helm template smoke-empty . \
  --set global.sovereignFQDN=omantel.biz \
  --show-only "${CM_TEMPLATE}" > "$TMP/cm-empty.yaml"
# The keys MUST still be present (so the catalyst-api / org-controller
# configMapKeyRef with optional:true resolves to "" rather than failing the
# Pod). They render as empty strings on a Sovereign whose provision-time chain
# has not supplied the values (pre-#4240/#4330 boot).
for key in lbIP consoleLBIP controlPlaneIP; do
  if ! grep -qE "^[[:space:]]*${key}:" "$TMP/cm-empty.yaml"; then
    echo "FAIL: ConfigMap dropped key '${key}' when value empty — configMapKeyRef would fail the consumer Pod" >&2
    exit 1
  fi
done
echo "  PASS (lbIP / consoleLBIP / controlPlaneIP keys present even with empty values)"

echo "[sovereign-fqdn-ip] Case 3: ConfigMap is gated on global.sovereignFQDN (NOT emitted on Catalyst-Zero / contabo)"
helm template smoke-zero . --show-only "${CM_TEMPLATE}" > "$TMP/cm-zero.yaml" 2>&1 || true
if grep -qE '^[[:space:]]*name:[[:space:]]*sovereign-fqdn' "$TMP/cm-zero.yaml"; then
  echo "FAIL: sovereign-fqdn ConfigMap rendered without global.sovereignFQDN — must be Sovereign-only (contabo is the signer, never the validator)" >&2
  exit 1
fi
echo "  PASS (ConfigMap absent when global.sovereignFQDN unset)"

echo "[sovereign-fqdn-ip] Case 4: catalyst-api wires CATALYST_OTECH_INGRESS_IPV4 from the sovereign-fqdn ConfigMap lbIP key"
# Cross-check the consumer side so a future rename of the ConfigMap key (or the
# env var) that would silently re-break the org-controller DNS writer fails here.
helm template smoke-api . \
  --set global.sovereignFQDN=omantel.biz \
  --set global.sovereignLBIP="${LB_IP}" \
  --show-only templates/api-deployment.yaml > "$TMP/api.yaml" 2>/dev/null || true
if [ -s "$TMP/api.yaml" ]; then
  if ! grep -q "CATALYST_OTECH_INGRESS_IPV4" "$TMP/api.yaml"; then
    echo "FAIL: catalyst-api Deployment no longer references CATALYST_OTECH_INGRESS_IPV4 — the org-controller DNS fallback IP wiring is broken (#4330)" >&2
    exit 1
  fi
  # The env MUST source from the sovereign-fqdn ConfigMap's lbIP key.
  if ! awk '/CATALYST_OTECH_INGRESS_IPV4/,/key:/' "$TMP/api.yaml" | grep -q 'sovereign-fqdn'; then
    echo "FAIL: CATALYST_OTECH_INGRESS_IPV4 not sourced from the sovereign-fqdn ConfigMap — key wiring drifted (#4330)" >&2
    exit 1
  fi
  echo "  PASS (catalyst-api CATALYST_OTECH_INGRESS_IPV4 ← sovereign-fqdn ConfigMap)"
else
  # Template path may be relocated by a future refactor — fall back to source.
  if ! grep -RqE 'CATALYST_OTECH_INGRESS_IPV4' templates/api-deployment.yaml; then
    echo "FAIL: api-deployment template no longer references CATALYST_OTECH_INGRESS_IPV4" >&2
    exit 1
  fi
  echo "  PASS (api-deployment template references CATALYST_OTECH_INGRESS_IPV4)"
fi

echo "[sovereign-fqdn-ip] Case 5: org-controller wires CATALYST_TENANT_CONSOLE_LB_IPV4 from the sovereign-fqdn ConfigMap consoleLBIP key"
helm template smoke-org . \
  --set global.sovereignFQDN=omantel.biz \
  --set global.sovereignConsoleLBIP="${CONSOLE_LB_IP}" \
  --show-only templates/controllers/organization-controller-deployment.yaml > "$TMP/org.yaml" 2>/dev/null || true
if [ -s "$TMP/org.yaml" ]; then
  if ! grep -q "CATALYST_TENANT_CONSOLE_LB_IPV4" "$TMP/org.yaml"; then
    echo "FAIL: organization-controller Deployment no longer references CATALYST_TENANT_CONSOLE_LB_IPV4 — per-Org console-host DNS will not resolve (#4330)" >&2
    exit 1
  fi
  if ! awk '/CATALYST_TENANT_CONSOLE_LB_IPV4/,/key:/' "$TMP/org.yaml" | grep -q 'sovereign-fqdn'; then
    echo "FAIL: CATALYST_TENANT_CONSOLE_LB_IPV4 not sourced from the sovereign-fqdn ConfigMap — key wiring drifted (#4330)" >&2
    exit 1
  fi
  echo "  PASS (org-controller CATALYST_TENANT_CONSOLE_LB_IPV4 ← sovereign-fqdn ConfigMap)"
else
  if ! grep -RqE 'CATALYST_TENANT_CONSOLE_LB_IPV4' templates/controllers/organization-controller-deployment.yaml; then
    echo "FAIL: organization-controller template no longer references CATALYST_TENANT_CONSOLE_LB_IPV4" >&2
    exit 1
  fi
  echo "  PASS (org-controller template references CATALYST_TENANT_CONSOLE_LB_IPV4)"
fi

echo "[sovereign-fqdn-ip] All gates green."
