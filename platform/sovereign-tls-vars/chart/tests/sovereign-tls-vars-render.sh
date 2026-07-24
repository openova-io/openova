#!/usr/bin/env bash
# bp-sovereign-tls-vars render gate (Refs #5187).
#
# Asserts the chart's load-bearing rules — the SAME contract the removed
# products/catalyst/chart/templates/sovereign-tls-vars-cm.yaml template
# satisfied, now on its own dedicated (always-on, every-region) chart:
#
#   1. Default render (global.sovereignFQDN empty) → ZERO resources
#      (Catalyst-Zero / not-yet-provisioned safe-empty shape).
#   2. Single-zone render (parentZones empty, only sovereignFQDN set) →
#      exactly 1 ConfigMap named sovereign-tls-vars in flux-system,
#      carrying PARENT_DOMAINS_LISTENERS_YAML with bare "https"/"http"
#      listener names + CONSOLE_LISTENERS_YAML with SPECIFIC per-host
#      listeners (console-/api-/marketplace-https+http hostnamed to the
#      exact 2-label endpoint, NEVER `*.<fqdn>` — #5341), all referencing
#      the per-prov wildcard Secret.
#   3. Multi-zone render (2 parentZones) → per-zone-sanitised listener
#      names, org-pool zone bound to its OWN per-zone Secret (#3376).
#   4. consoleGateway port overrides (Huawei hostNetwork 8443/8080) flow
#      into CONSOLE_LISTENERS_YAML (#4706).
#
# Usage: bash tests/sovereign-tls-vars-render.sh [CHART_DIR]
#
# CI consumes this via blueprint-release.yaml's `tests/*.sh` gate (the
# chart is annotated catalyst.openova.io/smoke-render-mode: default-off,
# so the generic smoke-render step expects a short default render and
# this script covers the enabled-render path per that gate's contract).

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

# ── Case 1: default render (empty sovereignFQDN) → ZERO resources ──
echo "[render] Case 1: default render (global.sovereignFQDN empty) emits ZERO resources"
helm template smoke-sovereign-tls-vars . > "$TMP/disabled.yaml" 2> "$TMP/disabled.err" || {
  echo "FAIL: default render errored:" >&2
  cat "$TMP/disabled.err" >&2
  exit 1
}
KINDS=$(grep -cE "^kind: " "$TMP/disabled.yaml" || true)
if [ "$KINDS" -ne 0 ]; then
  echo "FAIL: default render produced $KINDS resource(s); expected 0." >&2
  grep -E "^kind: " "$TMP/disabled.yaml" >&2
  exit 1
fi
echo "  PASS"

# ── Case 2: single-zone render ──────────────────────────────────────
echo "[render] Case 2: single-zone render (only global.sovereignFQDN set)"
helm template smoke-sovereign-tls-vars . \
  --set global.sovereignFQDN=t99.omani.works \
  > "$TMP/single.yaml" 2> "$TMP/single.err" || {
  echo "FAIL: single-zone render errored:" >&2
  cat "$TMP/single.err" >&2
  exit 1
}
if [ "$(grep -cE '^kind: ConfigMap$' "$TMP/single.yaml")" != "1" ]; then
  echo "FAIL: expected exactly 1 ConfigMap." >&2
  grep -E "^kind: " "$TMP/single.yaml" >&2
  exit 1
fi
grep -qE '^  name: sovereign-tls-vars$' "$TMP/single.yaml" || {
  echo "FAIL: ConfigMap name != sovereign-tls-vars." >&2
  exit 1
}
grep -qE '^  namespace: flux-system$' "$TMP/single.yaml" || {
  echo "FAIL: ConfigMap not rendered into flux-system." >&2
  exit 1
}
grep -q 'PARENT_DOMAINS_LISTENERS_YAML:' "$TMP/single.yaml" || {
  echo "FAIL: missing PARENT_DOMAINS_LISTENERS_YAML key." >&2
  exit 1
}
grep -q 'CONSOLE_LISTENERS_YAML:' "$TMP/single.yaml" || {
  echo "FAIL: missing CONSOLE_LISTENERS_YAML key." >&2
  exit 1
}
# Single zone → bare "https"/"http" listener names (escaped inside the
# quoted+escaped JSON string Helm emits).
grep -q '\\"name\\":\\"https\\"' "$TMP/single.yaml" || {
  echo "FAIL: single-zone PARENT_DOMAINS_LISTENERS_YAML missing bare 'https' listener name." >&2
  cat "$TMP/single.yaml" >&2
  exit 1
}
# CONSOLE_LISTENERS_YAML (#5341) — the dedicated console gateway MUST hostname
# each listener to the SPECIFIC operator endpoint it serves (console./api./
# marketplace.<fqdn>), NEVER the apex wildcard `*.<fqdn>`. A wildcard here made
# the console CEC's cilium-envoy filter chain claim every `*.<fqdn>` TLS
# handshake, colliding with the shared gateway's own `*.<fqdn>` chain and
# 404'ing ~50% of every OTHER subdomain (e.g. mcp.<fqdn>).
CONSOLE_LINE="$(grep 'CONSOLE_LISTENERS_YAML:' "$TMP/single.yaml")"
for want in \
  '\\"name\\":\\"console-https\\"' \
  '\\"hostname\\":\\"console.t99.omani.works\\"' \
  '\\"name\\":\\"api-https\\"' \
  '\\"hostname\\":\\"api.t99.omani.works\\"' \
  '\\"name\\":\\"marketplace-https\\"' \
  '\\"hostname\\":\\"marketplace.t99.omani.works\\"'; do
  echo "$CONSOLE_LINE" | grep -q "$want" || {
    echo "FAIL: CONSOLE_LISTENERS_YAML missing expected #5341 token: $want" >&2
    echo "$CONSOLE_LINE" >&2
    exit 1
  }
done
# Collision guard: the console listener set must carry NO `*.<fqdn>` hostname.
if echo "$CONSOLE_LINE" | grep -q '\\"hostname\\":\\"\*\.t99\.omani\.works\\"'; then
  echo "FAIL: CONSOLE_LISTENERS_YAML still carries a wildcard '*.t99.omani.works' hostname — the #5341 gateway collision is back." >&2
  echo "$CONSOLE_LINE" >&2
  exit 1
fi
grep -q '\\"name\\":\\"sovereign-wildcard-tls-t99-omani-works\\"' "$TMP/single.yaml" || {
  echo "FAIL: listener certificateRefs does not target the per-prov wildcard Secret sovereign-wildcard-tls-t99-omani-works." >&2
  exit 1
}
echo "  PASS"

# ── Case 3: multi-zone render (org-pool zone, own per-zone Secret) ──
echo "[render] Case 3: multi-zone render — org-pool zone binds its OWN per-zone Secret (#3376)"
helm template smoke-sovereign-tls-vars . \
  --set global.sovereignFQDN=t99.omani.works \
  --set 'parentZones[0].name=t99.omani.works' \
  --set 'parentZones[0].role=primary' \
  --set 'parentZones[1].name=omani.homes' \
  --set 'parentZones[1].role=org-pool' \
  > "$TMP/multi.yaml" 2> "$TMP/multi.err" || {
  echo "FAIL: multi-zone render errored:" >&2
  cat "$TMP/multi.err" >&2
  exit 1
}
grep -q '\\"name\\":\\"https-omani-homes\\"' "$TMP/multi.yaml" || {
  echo "FAIL: multi-zone render missing sanitised 'https-omani-homes' listener name." >&2
  cat "$TMP/multi.yaml" >&2
  exit 1
}
grep -q '\\"name\\":\\"sovereign-wildcard-tls-omani-homes\\"' "$TMP/multi.yaml" || {
  echo "FAIL: org-pool zone (omani.homes) must bind its OWN per-zone Secret sovereign-wildcard-tls-omani-homes, not the primary cert (#3376)." >&2
  exit 1
}
echo "  PASS"

# ── Case 4: consoleGateway port override (#4706 Huawei hostNetwork) ─
echo "[render] Case 4: consoleGateway httpsPort/httpPort override flows into CONSOLE_LISTENERS_YAML"
helm template smoke-sovereign-tls-vars . \
  --set global.sovereignFQDN=t99.omani.works \
  --set consoleGateway.httpsPort=8443 \
  --set consoleGateway.httpPort=8080 \
  > "$TMP/consoleport.yaml" 2> "$TMP/consoleport.err" || {
  echo "FAIL: consoleGateway port-override render errored:" >&2
  cat "$TMP/consoleport.err" >&2
  exit 1
}
grep -q '\\"port\\":8443' "$TMP/consoleport.yaml" || {
  echo "FAIL: CONSOLE_LISTENERS_YAML did not pick up consoleGateway.httpsPort=8443." >&2
  cat "$TMP/consoleport.yaml" >&2
  exit 1
}
grep -q '\\"port\\":8080' "$TMP/consoleport.yaml" || {
  echo "FAIL: CONSOLE_LISTENERS_YAML did not pick up consoleGateway.httpPort=8080." >&2
  exit 1
}
echo "  PASS"

echo "[render] All bp-sovereign-tls-vars render gates green."
