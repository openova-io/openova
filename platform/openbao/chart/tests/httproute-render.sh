#!/usr/bin/env bash
# bp-openbao — HTTPRoute render audit (Wave 5.85 #2425).
#
# Verifies the chart renders a Cilium Gateway HTTPRoute when the
# bootstrap-kit overlay flips `gateway.enabled: true` + sets
# `gateway.host`. The default-values CI smoke does NOT exercise this
# path because the chart defaults `gateway.enabled` to false (correct
# for standalone-install on non-Catalyst clusters). Without this guard
# a regression in `templates/httproute.yaml` (gate condition, hostname
# templating, parentRef typo) ships fresh-prov NXDOMAIN to every
# Sovereign + only surfaces on operator walk — exactly the #2421
# symptom that drove Wave 5.84 for bp-newapi.
#
# Cases:
#   1. Overlay-enabled render — HTTPRoute kind present with correct
#      hostname
#   2. parentRefs cite canonical Cilium Gateway
#      (`cilium-gateway` in `kube-system`)
#   3. Default-off path — no HTTPRoute kind in render output
#      (silent-skip behaviour preserved for standalone-install
#      contexts on non-Catalyst clusters)
#   4. Operator host override propagates correctly

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

# ── Case 1: HTTPRoute renders with overlay-enabled values ─────────────
echo "[bp-openbao] Case 1: overlay-enabled render — HTTPRoute present + correct hostname"
out=$("$helm" template smoke "$chart_dir" \
        --set gateway.enabled=true \
        --set gateway.host=openbao.smoke.omani.works 2>&1)

route_block=$(echo "$out" | awk '/^---$/{f=0} /^kind: HTTPRoute$/{f=1} f')
if [ -z "$route_block" ]; then
  echo "FAIL: HTTPRoute did not render with gateway.enabled=true + gateway.host"
  echo "(would surface on fresh Sovereign prov as NXDOMAIN for openbao.<fqdn>)"
  exit 1
fi
if ! grep -q "openbao.smoke.omani.works" <<<"$route_block"; then
  echo "FAIL: HTTPRoute hostname does not match gateway.host"
  echo "$route_block"
  exit 1
fi
echo "[bp-openbao] Case 1: PASS"

# ── Case 2: parentRefs cite canonical Cilium Gateway ──────────────────
echo "[bp-openbao] Case 2: parentRefs cite 'cilium-gateway' in 'kube-system'"
if ! grep -q "name: \"cilium-gateway\"" <<<"$route_block"; then
  echo "FAIL: HTTPRoute parentRefs do not cite name=cilium-gateway"
  exit 1
fi
if ! grep -q "namespace: \"kube-system\"" <<<"$route_block"; then
  echo "FAIL: HTTPRoute parentRefs do not cite namespace=kube-system"
  exit 1
fi
echo "[bp-openbao] Case 2: PASS"

# ── Case 3: default-off path — HTTPRoute absent ───────────────────────
echo "[bp-openbao] Case 3: default-off path — HTTPRoute not rendered"
out_default=$("$helm" template smoke "$chart_dir" 2>&1)
if grep -q "^kind: HTTPRoute$" <<<"$out_default"; then
  echo "FAIL: HTTPRoute should NOT render when gateway.enabled is unset (default)"
  exit 1
fi
echo "[bp-openbao] Case 3: PASS"

# ── Case 4: operator host override propagates ─────────────────────────
echo "[bp-openbao] Case 4: operator override of gateway.host"
out_override=$("$helm" template smoke "$chart_dir" \
        --set gateway.enabled=true \
        --set gateway.host=custom.smoke.example 2>&1)

route_block_override=$(echo "$out_override" | awk '/^---$/{f=0} /^kind: HTTPRoute$/{f=1} f')
if ! grep -q "custom.smoke.example" <<<"$route_block_override"; then
  echo "FAIL: explicit gateway.host override not propagated to HTTPRoute hostnames"
  exit 1
fi
if grep -q "openbao.smoke.omani.works" <<<"$route_block_override"; then
  echo "FAIL: explicit override leaked the case-1 hostname"
  exit 1
fi
echo "[bp-openbao] Case 4: PASS"

# ── Case 5: bootstrap Application CR placement is canonical ────────────
# #3375 / #3768 / #3786 — ONE canonical vocabulary on the wire. When the
# bootstrap-kit slot opts the chart into self-registering its Application
# CR (bootstrapOwned.enabled=true), spec.placement MUST emit the canonical
# token `singleton` — the read path (endpoint_handler.go readTopology)
# serves it VERBATIM to the Topology tab + the Instances-table chip, so the
# banned legacy `single-region` spelling would render raw to the operator
# (exactly the hw159 #3375 finding).
echo "[bp-openbao] Case 5: bootstrap Application CR placement is canonical 'singleton'"
appcr=$("$helm" template smoke "$chart_dir" \
        --set bootstrapOwned.enabled=true \
        --set bootstrapOwned.helmRelease.name=bp-openbao \
        --api-versions apps.openova.io/v1 2>&1)
if ! grep -qE '^  placement: singleton$' <<<"$appcr"; then
  echo "FAIL: Application CR placement must be canonical 'singleton' (not banned 'single-region')"
  echo "$appcr" | grep -E '^  placement:' || true
  exit 1
fi
if grep -qE '^  placement: single-region$' <<<"$appcr"; then
  echo "FAIL: #3375 REGRESSION — Application CR re-introduced the banned 'single-region' placement"
  exit 1
fi
echo "[bp-openbao] Case 5: PASS"

echo "[bp-openbao] All HTTPRoute render cases PASS"
