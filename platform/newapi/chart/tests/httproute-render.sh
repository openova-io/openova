#!/usr/bin/env bash
# bp-newapi — HTTPRoute render audit (Wave 5.84 #2421).
#
# Verifies the chart renders a Cilium Gateway HTTPRoute for
# `newapi.<sovereignFQDN>` when the bootstrap-kit overlay flips
# `ingress.httpRoute.enabled: true` + sets `sovereignFQDN`.
#
# Why this guard: the default values.yaml ships `httpRoute.enabled: false`
# (sensible: a chart installed standalone on a non-Catalyst cluster
# shouldn't fight whatever Gateway / Ingress the operator owns). The
# bootstrap-kit overlay at clusters/_template/bootstrap-kit/80-newapi.yaml
# flips it on. The CI default-values smoke (blueprint-release.yaml) does
# NOT exercise the overlay-enabled path, so a regression in
# templates/httproute.yaml (gate condition, typo in parentRef, hostname
# templating) would slip through smoke + only surface on a fresh prov as
# NXDOMAIN for `newapi.<fqdn>`.
#
# #2421 observed exactly this on hw01.omani.works (Wave 5.80 walk).
#
# Cases:
#   1. Overlay-enabled render — HTTPRoute kind present with correct
#      hostname (`newapi.<sovereignFQDN>`)
#   2. Overlay-enabled render — HTTPRoute parentRefs cite the canonical
#      Cilium Gateway (`cilium-gateway` in `kube-system`)
#   3. Overlay-enabled render — HTTPRoute backendRefs point at the
#      bp-newapi Service on the chart's Service port
#   4. Default-off path — no HTTPRoute kind in render output (no
#      regression of the silent-skip behaviour the standalone-install
#      path relies on)
#   5. Operator override — explicit `host` overrides the
#      `newapi.<fqdn>` derived hostname
#   7. #5612 — the Exact `/login` recovery-path match is present + routed
#      to the sandbox-bridge Service when sovereignFQDN is set (the inert
#      "Continue with OpenOva SSO" button fix)
#   8. #5612 — the `/login` match is ABSENT when sovereignFQDN is unset
#      (guards against routing the recovery path into a bridge whose own
#      fallback would redirect back to /login — a self-loop)

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

# Common values that mirror the bootstrap-kit overlay at slot 80 — minus
# the cluster-specific creds, plus the masterKey shortcut so we don't
# need a Keycloak issuer for smoke render.
overlay_values=$(mktemp)
trap 'rm -f "$overlay_values"' EXIT
cat > "$overlay_values" <<'EOF'
sovereignFQDN: smoke.omani.works
auth:
  adminUI:
    mode: masterKey
database:
  existingSecret: test-dsn
ingress:
  host: api.smoke.omani.works
  httpRoute:
    enabled: true
EOF

# ── Case 1: HTTPRoute renders with derived hostname ──────────────────
echo "[bp-newapi] Case 1: overlay-enabled render — HTTPRoute present + correct hostname"
out=$("$helm" template smoke "$chart_dir" -f "$overlay_values" 2>&1)

route_block=$(echo "$out" | awk '/^---$/{f=0} /^kind: HTTPRoute$/{f=1} f')
if [ -z "$route_block" ]; then
  echo "FAIL: HTTPRoute did not render with httpRoute.enabled=true + sovereignFQDN"
  echo "(would surface on fresh Sovereign prov as NXDOMAIN for newapi.<fqdn> — exactly the #2421 symptom)"
  exit 1
fi
if ! grep -q "newapi.smoke.omani.works" <<<"$route_block"; then
  echo "FAIL: HTTPRoute hostname is not derived as 'newapi.<sovereignFQDN>'"
  echo "$route_block"
  exit 1
fi
echo "[bp-newapi] Case 1: PASS"

# ── Case 2: parentRefs cite canonical Cilium Gateway ──────────────────
echo "[bp-newapi] Case 2: parentRefs cite 'cilium-gateway' in 'kube-system'"
if ! grep -q "name: \"cilium-gateway\"" <<<"$route_block"; then
  echo "FAIL: HTTPRoute parentRefs do not cite name=cilium-gateway"
  exit 1
fi
if ! grep -q "namespace: \"kube-system\"" <<<"$route_block"; then
  echo "FAIL: HTTPRoute parentRefs do not cite namespace=kube-system"
  exit 1
fi
echo "[bp-newapi] Case 2: PASS"

# ── Case 3: backendRefs point at bp-newapi Service ────────────────────
echo "[bp-newapi] Case 3: backendRefs point at bp-newapi Service"
if ! grep -q "smoke-bp-newapi" <<<"$route_block"; then
  echo "FAIL: HTTPRoute backendRefs name does not match include 'bp-newapi.fullname'"
  exit 1
fi
echo "[bp-newapi] Case 3: PASS"

# ── Case 4: default-off path — HTTPRoute absent ───────────────────────
echo "[bp-newapi] Case 4: default-off path — HTTPRoute not rendered"
default_values=$(mktemp)
cat > "$default_values" <<'EOF'
auth:
  adminUI:
    mode: masterKey
database:
  existingSecret: test-dsn
EOF
out_default=$("$helm" template smoke "$chart_dir" -f "$default_values" 2>&1)
rm -f "$default_values"

if grep -q "^kind: HTTPRoute$" <<<"$out_default"; then
  echo "FAIL: HTTPRoute should NOT render when httpRoute.enabled is unset (default)"
  exit 1
fi
echo "[bp-newapi] Case 4: PASS"

# ── Case 5: operator override of explicit host ────────────────────────
echo "[bp-newapi] Case 5: operator override of httpRoute.host"
override_values=$(mktemp)
cat > "$override_values" <<'EOF'
sovereignFQDN: smoke.omani.works
auth:
  adminUI:
    mode: masterKey
database:
  existingSecret: test-dsn
ingress:
  host: api.smoke.omani.works
  httpRoute:
    enabled: true
    host: llm.smoke.omani.works
EOF
out_override=$("$helm" template smoke "$chart_dir" -f "$override_values" 2>&1)
rm -f "$override_values"

route_block_override=$(echo "$out_override" | awk '/^---$/{f=0} /^kind: HTTPRoute$/{f=1} f')
if ! grep -q "llm.smoke.omani.works" <<<"$route_block_override"; then
  echo "FAIL: explicit httpRoute.host override not propagated to HTTPRoute hostnames"
  exit 1
fi
if grep -q "newapi.smoke.omani.works" <<<"$route_block_override"; then
  echo "FAIL: explicit override leaked the derived 'newapi.<fqdn>' default"
  exit 1
fi
echo "[bp-newapi] Case 5: PASS"

# ── Case 6: bootstrap Application CR placement is canonical ────────────
# #3375 / #3768 / #3786 — ONE canonical vocabulary on the wire. When the
# bootstrap-kit slot opts the chart into self-registering its Application
# CR (bootstrapOwned.enabled=true), spec.placement MUST emit the canonical
# token `singleton` — the read path (endpoint_handler.go readTopology)
# serves it VERBATIM to the Topology tab + the Instances-table chip, so the
# banned legacy `single-region` spelling would render raw to the operator
# (exactly the hw159 #3375 finding). The masterKey + DSN values keep the
# rest of the chart renderable so the CR template is reached.
echo "[bp-newapi] Case 6: bootstrap Application CR placement is canonical 'singleton'"
appcr_values=$(mktemp)
cat > "$appcr_values" <<'EOF'
auth:
  adminUI:
    mode: masterKey
database:
  existingSecret: test-dsn
bootstrapOwned:
  enabled: true
  helmRelease:
    name: bp-newapi
EOF
appcr=$("$helm" template smoke "$chart_dir" -f "$appcr_values" \
        --api-versions apps.openova.io/v1 2>&1)
rm -f "$appcr_values"
if ! grep -qE '^  placement: singleton$' <<<"$appcr"; then
  echo "FAIL: Application CR placement must be canonical 'singleton' (not banned 'single-region')"
  echo "$appcr" | grep -E '^  placement:' || true
  exit 1
fi
if grep -qE '^  placement: single-region$' <<<"$appcr"; then
  echo "FAIL: #3375 REGRESSION — Application CR re-introduced the banned 'single-region' placement"
  exit 1
fi
echo "[bp-newapi] Case 6: PASS"

# ── Case 7: #5612 recovery-path Exact /login match present ────────────
# NewAPI's own "Continue with OpenOva SSO" button on /login?expired=true is
# inert (verified live on hw292 — no navigation, no network request). The
# chart now routes an Exact /login match to the SAME bridge Service that
# already serves the bare-root zero-click landing page, so a full
# navigation/reload of the expired-session page recovers deterministically.
echo "[bp-newapi] Case 7: #5612 — Exact /login match routes to the bridge Service"
login_matches=$(echo "$route_block" | grep -c 'value: "/login"' || true)
if [ "$login_matches" -lt 1 ]; then
  echo "FAIL: no Exact /login match rendered with sovereignFQDN set — #5612 recovery path missing"
  echo "$route_block"
  exit 1
fi
if ! echo "$route_block" | grep -A2 'value: "/login"' | grep -q "smoke-bp-newapi-bridge"; then
  echo "FAIL: /login match does not backendRef the bridge Service"
  exit 1
fi
echo "[bp-newapi] Case 7: PASS"

# ── Case 8: #5612 /login match absent without sovereignFQDN ───────────
# Without sovereignFQDN, bp-newapi.sandboxBridgeEnv never populates
# NEWAPI_SSO_AUTHORIZE_URL/CLIENT_ID (_helpers.tpl), so the bridge's own
# fallback() would redirect an unconfigured /login hit back to /login — a
# self-loop. The HTTPRoute must gate the /login match on the SAME signal so
# an unconfigured overlay keeps NewAPI's stock SPA /login page instead.
echo "[bp-newapi] Case 8: #5612 — /login match ABSENT when sovereignFQDN is unset"
nofqdn_values=$(mktemp)
cat > "$nofqdn_values" <<'EOF'
auth:
  adminUI:
    mode: masterKey
database:
  existingSecret: test-dsn
ingress:
  host: api.smoke.omani.works
  httpRoute:
    enabled: true
    host: newapi.smoke.omani.works
EOF
out_nofqdn=$("$helm" template smoke "$chart_dir" -f "$nofqdn_values" 2>&1)
rm -f "$nofqdn_values"
route_block_nofqdn=$(echo "$out_nofqdn" | awk '/^---$/{f=0} /^kind: HTTPRoute$/{f=1} f')
if [ -z "$route_block_nofqdn" ]; then
  echo "FAIL: HTTPRoute did not render with an explicit httpRoute.host + no sovereignFQDN"
  exit 1
fi
if grep -q 'value: "/login"' <<<"$route_block_nofqdn"; then
  echo "FAIL: #5612 REGRESSION — /login match rendered WITHOUT sovereignFQDN (self-redirect-loop risk)"
  echo "$route_block_nofqdn"
  exit 1
fi
echo "[bp-newapi] Case 8: PASS"

echo "[bp-newapi] All HTTPRoute render cases PASS"
