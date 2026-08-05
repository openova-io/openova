#!/usr/bin/env bash
# bp-cilium — #5602 Hubble UI cross-region singleton guard.
#
# WHAT THIS PROTECTS
#
# Hubble UI's `/api/service-map-stream` + `/api/control-stream` are NOT a
# stateless REST API. They speak hubble-ui's own `customprotocol` long-poll:
# the OPENING POST carries the serialized GetEventsRequest and mints a channel
# held in an IN-PROCESS map (hubble-ui
# backend/internal/customprotocol/route/route.go `getChannel` ->
# `r.channels.GetByIdUnsafe(cid)`); every FOLLOW-UP poll for that channel
# carries an EMPTY body and only drains it. A poll that lands on a DIFFERENT
# backend process finds no channel, mints a fresh one from the empty body, and
# backend/internal/apiserver/service_map_stream.go answers
# `len(req.GetEventTypes()) == 0` with `ch.TerminateStatus(
# http.StatusBadRequest)` — HTTP 400, which the SPA paints as a permanent
# "Idle / Data streams are reconnecting…" surface.
#
# Bootstrap-kit slot 01 installs bp-cilium on EVERY region, so a 2-region
# Sovereign ran TWO hubble-ui backends behind ONE `hubble.<fqdn>` hostname
# whose shared ELB fans TCP across both gateways. The fix anchors the workload
# in the PRIMARY region; the SECONDARY keeps only a zero-backend ClusterMesh
# stub so `service.cilium.io/affinity: local` falls through to the primary.
#
# Cases:
#   1. DEFAULT (crossRegion absent/disabled) → NO mesh annotations anywhere and
#      the oauth2-proxy Deployment still renders. Proves the fix cannot be
#      satisfied by blanket suppression and that single-region is untouched.
#   2. crossRegion.enabled + role=primary → Deployment renders; Service carries
#      global=true, shared=true, affinity=local.
#   3. crossRegion.enabled + role=secondary → NO Deployment (the zero-local-
#      backend precondition), Service still renders with global=true,
#      shared=FALSE, affinity=local.
#      >>> THIS IS THE VACUITY-BREAKING CASE: against the pre-fix chart the
#          secondary renders a full oauth2-proxy Deployment and an
#          annotation-free Service, i.e. two live Hubble UIs. <<<
#   4. auth=none + crossRegion.enabled → documented no-op (the HTTPRoute
#      targets the UPSTREAM hubble-ui Service, which this chart does not own),
#      so NO mesh annotations may appear and nothing may be suppressed.
#   5. invalid role → render FAILS (a typo must never silently degrade to a
#      second primary, which is the exact split being removed).

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

fqdn="smoke.example.com"
common=(
  --set catalystOverlay.hubbleUI.enabled=true
  --set "catalystOverlay.hubbleUI.sovereignFQDN=${fqdn}"
  --set cilium.clustermesh.apiserver.service.lbIpam.enabled=false
)

show() {
  local tmpl="$1"; shift
  "$helm" template smoke "$chart_dir" "${common[@]}" "$@" --show-only "templates/${tmpl}" 2>/dev/null || true
}

# Strip YAML comments so the templates' own prose (which names the annotations
# and the word Deployment) can never satisfy an assertion.
uncomment() { sed 's/[[:space:]]*#.*$//'; }

has_deployment() { grep -q "^kind: Deployment" <<<"$(uncomment <<<"$1")"; }

assert_ann() { # haystack key value label
  local got
  got="$(uncomment <<<"$1" | awk -v k="$2" '$1 == k":" {v=$2; gsub(/"/,"",v); print v; exit}')"
  if [ "$got" != "$3" ]; then
    echo "FAIL: $4 — $2 is '${got:-<absent>}' (expected '$3')" >&2; exit 1
  fi
}

assert_no_ann() { # haystack label
  if uncomment <<<"$1" | grep -q "service.cilium.io/"; then
    echo "FAIL: $2 — ClusterMesh annotations rendered where they must not be" >&2; exit 1
  fi
}

oidc=(--set catalystOverlay.hubbleUI.auth=oidc)

# ── Case 1: default render is untouched ───────────────────────────────────
echo "[hubble-crossregion] Case 1: default (crossRegion off) — no mesh, proxy present"
dep_def="$(show hubble-ui-oauth2-proxy-deployment.yaml "${oidc[@]}")"
svc_def="$(show hubble-ui-oauth2-proxy-service.yaml "${oidc[@]}")"
has_deployment "$dep_def" || { echo "FAIL: default render lost the oauth2-proxy Deployment" >&2; exit 1; }
assert_no_ann "$svc_def" "default render"
echo "  PASS (Deployment renders, Service annotation-free)"

# ── Case 2: primary owns the singleton ────────────────────────────────────
echo "[hubble-crossregion] Case 2: crossRegion primary — proxy present, Service exported"
prim=("${oidc[@]}" --set catalystOverlay.hubbleUI.crossRegion.enabled=true --set catalystOverlay.hubbleUI.crossRegion.role=primary)
dep_p="$(show hubble-ui-oauth2-proxy-deployment.yaml "${prim[@]}")"
svc_p="$(show hubble-ui-oauth2-proxy-service.yaml "${prim[@]}")"
has_deployment "$dep_p" || { echo "FAIL: primary lost the oauth2-proxy Deployment" >&2; exit 1; }
assert_ann "$svc_p" "service.cilium.io/global"   "true"  "primary"
assert_ann "$svc_p" "service.cilium.io/shared"   "true"  "primary"
assert_ann "$svc_p" "service.cilium.io/affinity" "local" "primary"
echo "  PASS (global=true shared=true affinity=local, Deployment present)"

# ── Case 3: secondary is a zero-backend mesh stub (VACUITY-BREAKING) ──────
echo "[hubble-crossregion] Case 3: crossRegion secondary — NO proxy, Service is a mesh stub"
sec=("${oidc[@]}" --set catalystOverlay.hubbleUI.crossRegion.enabled=true --set catalystOverlay.hubbleUI.crossRegion.role=secondary)
dep_s="$(show hubble-ui-oauth2-proxy-deployment.yaml "${sec[@]}")"
svc_s="$(show hubble-ui-oauth2-proxy-service.yaml "${sec[@]}")"
if has_deployment "$dep_s"; then
  echo "FAIL: #5602 — the SECONDARY region still renders an oauth2-proxy Deployment." >&2
  echo "      Its Service then has LOCAL backends, affinity=local never falls through" >&2
  echo "      the mesh, and the Sovereign runs TWO stateful hubble-ui backends behind" >&2
  echo "      one hostname — every cross-region poll answers HTTP 400." >&2
  exit 1
fi
grep -q "^kind: Service" <<<"$(uncomment <<<"$svc_s")" || {
  echo "FAIL: secondary dropped the Service — the HTTPRoute backendRef would not resolve" >&2; exit 1; }
assert_ann "$svc_s" "service.cilium.io/global"   "true"  "secondary"
assert_ann "$svc_s" "service.cilium.io/shared"   "false" "secondary"
assert_ann "$svc_s" "service.cilium.io/affinity" "local" "secondary"
echo "  PASS (no Deployment; Service global=true shared=false affinity=local)"

# ── Case 4: auth=none is a documented no-op ───────────────────────────────
echo "[hubble-crossregion] Case 4: auth=none + crossRegion — documented no-op"
none_sec=(--set catalystOverlay.hubbleUI.auth=none --set catalystOverlay.hubbleUI.crossRegion.enabled=true --set catalystOverlay.hubbleUI.crossRegion.role=secondary)
svc_n="$(show hubble-ui-oauth2-proxy-service.yaml "${none_sec[@]}")"
dep_n="$(show hubble-ui-oauth2-proxy-deployment.yaml "${none_sec[@]}")"
assert_no_ann "$svc_n" "auth=none"
# auth=none never renders the proxy at all (pre-existing behaviour) — assert the
# route still points at the upstream Service so nothing regressed.
route_n="$(show hubble-ui-httproute.yaml "${none_sec[@]}")"
grep -q "name: \"hubble-ui\"" <<<"$route_n" || {
  echo "FAIL: auth=none HTTPRoute no longer targets the upstream hubble-ui Service" >&2; exit 1; }
if has_deployment "$dep_n"; then
  echo "FAIL: auth=none rendered an oauth2-proxy Deployment" >&2; exit 1
fi
echo "  PASS (no annotations, route unchanged)"

# ── Case 5: an invalid role must fail the render, not degrade ─────────────
echo "[hubble-crossregion] Case 5: invalid crossRegion.role fails the render"
if "$helm" template smoke "$chart_dir" "${common[@]}" "${oidc[@]}" \
     --set catalystOverlay.hubbleUI.crossRegion.enabled=true \
     --set catalystOverlay.hubbleUI.crossRegion.role=replica \
     --show-only templates/hubble-ui-oauth2-proxy-service.yaml >/dev/null 2>&1; then
  echo "FAIL: crossRegion.role=replica rendered instead of failing — a typo would" >&2
  echo "      silently produce a SECOND primary, the exact split this guard covers." >&2
  exit 1
fi
echo "  PASS (render fails closed)"

echo "[bp-cilium #5602 hubble cross-region singleton] All cases PASS"
