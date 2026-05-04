#!/usr/bin/env bash
# bp-openclaw render-toggle integration test.
#
# Drives the helm-template gate run by .github/workflows/blueprint-release.yaml.
# Verifies:
#   1. Default render SUCCEEDS (placeholder defaults are valid bytes).
#   2. assertNoPlaceholders=true with placeholder values FAILS render.
#   3. RBAC: `create` verbs are NOT combined with `resourceNames`
#      (per feedback_rbac_create_no_resourcenames.md).
#   4. ServiceMonitor toggle defaults to off (per BLUEPRINT-AUTHORING §11.2).
#   5. networkPolicy toggle suppresses NetworkPolicy when off.
#   6. Per-user pod template ConfigMap is rendered.
#   7. Ingress carries cert-manager cluster-issuer annotation.
#
# Usage: bash tests/render-toggles.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

echo "[render-toggles] Case 1: default render succeeds (placeholder defaults are valid for smoke)"
if ! helm template smoke-openclaw . > "$TMP/default.yaml" 2> "$TMP/default.err"; then
  echo "FAIL: default render failed (placeholder defaults should let smoke render pass):" >&2
  cat "$TMP/default.err" >&2
  exit 1
fi
for kind in Deployment Service Ingress Role RoleBinding ConfigMap NetworkPolicy ServiceAccount; do
  if ! grep -qE "^kind: ${kind}$" "$TMP/default.yaml"; then
    echo "FAIL: default render is missing kind=${kind}" >&2
    exit 1
  fi
done
echo "  PASS"

echo "[render-toggles] Case 2: assertNoPlaceholders=true with default values FAILS render"
if helm template smoke-openclaw . --set "assertNoPlaceholders=true" > "$TMP/assert.yaml" 2> "$TMP/assert.err"; then
  echo "FAIL: assertNoPlaceholders=true rendered successfully — guard is broken." >&2
  echo "      Expected at least one placeholder-rejection message." >&2
  exit 1
fi
if ! grep -q "placeholder" "$TMP/assert.err"; then
  echo "FAIL: assertNoPlaceholders=true failure did not include the expected 'placeholder' message." >&2
  cat "$TMP/assert.err" >&2
  exit 1
fi
echo "  PASS"

echo "[render-toggles] Case 2b: assertNoPlaceholders=true with all real values renders successfully"
if ! helm template smoke-openclaw . \
    --set "assertNoPlaceholders=true" \
    --set "keycloak.realmURL=https://kc.acme.example/realms/acme" \
    --set "keycloak.clientSecretName=openclaw-oidc" \
    --set "tenant.namespace=sme-acme" \
    --set "newapi.baseURL=https://newapi.example" \
    --set "controller.image.tag=sha-deadbeefdeadbeefdeadbeefdeadbeefdeadbeef" \
    --set "perUserPod.image.tag=sha-cafef00dcafef00dcafef00dcafef00dcafef00d" \
    --set "ingress.host=openclaw.acme.example" \
    > "$TMP/real.yaml" 2> "$TMP/real.err"; then
  echo "FAIL: assertNoPlaceholders=true with real values failed:" >&2
  cat "$TMP/real.err" >&2
  exit 1
fi
echo "  PASS"

echo "[render-toggles] Case 3: RBAC — 'create' verb is NOT combined with resourceNames"
# Per feedback_rbac_create_no_resourcenames.md (2026-05-03): combining
# `create` with resourceNames produces 403 every time (resourceNames
# cannot constrain a not-yet-existing resource). Label-based ownership
# is enforced at the controller, not in RBAC.
RENDER_OUT="$TMP/default.yaml" python3 - <<'PY'
import os, sys, yaml
path = os.environ["RENDER_OUT"]
with open(path) as f:
    docs = list(yaml.safe_load_all(f))
violations = []
for d in docs:
    if not d:
        continue
    if d.get("kind") not in {"Role", "ClusterRole"}:
        continue
    name = d.get("metadata", {}).get("name", "<unnamed>")
    for i, rule in enumerate(d.get("rules", []) or []):
        verbs = rule.get("verbs", []) or []
        rns = rule.get("resourceNames", []) or []
        if "create" in verbs and rns:
            violations.append(f"{name} rule[{i}]: verbs={verbs} resourceNames={rns}")
if violations:
    print("FAIL: RBAC rule combines create with resourceNames (forbidden per feedback_rbac_create_no_resourcenames.md):", file=sys.stderr)
    for v in violations:
        print(f"  - {v}", file=sys.stderr)
    sys.exit(1)
PY
echo "  PASS"

echo "[render-toggles] Case 4: ServiceMonitor defaults off"
if grep -qE "kind: (ServiceMonitor|PodMonitor|PrometheusRule)" "$TMP/default.yaml"; then
  echo "FAIL: default render contains a Prometheus operator resource — observability toggles must default off (BLUEPRINT-AUTHORING.md §11.2)." >&2
  exit 1
fi
echo "  PASS"

echo "[render-toggles] Case 5: networkPolicy.enabled=false suppresses NetworkPolicy"
if ! helm template smoke-openclaw . \
    --set "networkPolicy.enabled=false" \
    > "$TMP/np-off.yaml" 2> "$TMP/np-off.err"; then
  echo "FAIL: networkPolicy.enabled=false render failed:" >&2
  cat "$TMP/np-off.err" >&2
  exit 1
fi
if grep -qE "^kind: NetworkPolicy$" "$TMP/np-off.yaml"; then
  echo "FAIL: networkPolicy.enabled=false still renders a NetworkPolicy — toggle is broken." >&2
  exit 1
fi
echo "  PASS"

echo "[render-toggles] Case 6: per-user pod template ConfigMap is rendered"
if ! grep -q "pod-template.yaml: |" "$TMP/default.yaml"; then
  echo "FAIL: per-user pod-template ConfigMap was not rendered (controller would have no pod-spec template at runtime)." >&2
  exit 1
fi
# Assert the substitution placeholders the controller will fill at
# session-start are present in the rendered template.
for placeholder in '${USER_UUID}' '${SECRET_NAME}'; do
  if ! grep -qF "$placeholder" "$TMP/default.yaml"; then
    echo "FAIL: per-user pod-template missing controller substitution placeholder ${placeholder}" >&2
    exit 1
  fi
done
echo "  PASS"

echo "[render-toggles] Case 7: ingress carries cert-manager cluster-issuer annotation"
if ! grep -q 'cert-manager.io/cluster-issuer: "letsencrypt-prod"' "$TMP/default.yaml"; then
  echo "FAIL: ingress is missing cert-manager.io/cluster-issuer annotation — ACME auto-issue won't fire." >&2
  exit 1
fi
echo "  PASS"

echo "[render-toggles] All bp-openclaw render-toggle gates green."
