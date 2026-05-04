#!/usr/bin/env bash
# bp-openclaw render-toggle integration test.
#
# Drives the helm-template gate run by .github/workflows/blueprint-release.yaml.
# Verifies:
#   1. Default values render FAILS — required values must be enforced.
#   2. Minimum-required values render SUCCEEDS and produces expected resources.
#   3. RBAC: `create` verbs are NOT combined with `resourceNames`
#      (per feedback_rbac_create_no_resourcenames.md).
#   4. ServiceMonitor toggle defaults to off (per BLUEPRINT-AUTHORING §11.2).
#   5. networkPolicy toggle suppresses NetworkPolicy when off.
#   6. Per-user pod template ConfigMap is rendered.
#
# Usage: bash tests/render-toggles.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

# Common args every successful render needs (the operator-supplied
# values that the chart's assertRequired helper guards on).
COMMON_ARGS=(
  --set "keycloak.realmURL=https://kc.acme.example/realms/acme"
  --set "keycloak.clientSecretName=openclaw-oidc"
  --set "tenant.namespace=sme-acme"
  --set "newapi.baseURL=https://newapi.example"
  --set "controller.image.tag=sha-deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
  --set "perUserPod.image.tag=sha-cafef00dcafef00dcafef00dcafef00dcafef00d"
  --set "ingress.host=openclaw.acme.example"
)

echo "[render-toggles] Case 1: default render FAILS without required values"
if helm template smoke-openclaw . > "$TMP/default.yaml" 2> "$TMP/default.err"; then
  echo "FAIL: default render succeeded — assertRequired in _helpers.tpl is broken." >&2
  echo "      Expected the chart to fail with a helpful error when keycloak.realmURL et al are unset." >&2
  exit 1
fi
if ! grep -q "is required" "$TMP/default.err"; then
  echo "FAIL: default render failure did not include the expected 'is required' helper-message." >&2
  cat "$TMP/default.err" >&2
  exit 1
fi
echo "  PASS"

echo "[render-toggles] Case 2: minimum-required values render succeeds"
if ! helm template smoke-openclaw . "${COMMON_ARGS[@]}" > "$TMP/ok.yaml" 2> "$TMP/ok.err"; then
  echo "FAIL: minimum-required render failed:" >&2
  cat "$TMP/ok.err" >&2
  exit 1
fi
for kind in Deployment Service Ingress Role RoleBinding ConfigMap NetworkPolicy ServiceAccount; do
  if ! grep -qE "^kind: ${kind}$" "$TMP/ok.yaml"; then
    echo "FAIL: minimum-required render is missing kind=${kind}" >&2
    exit 1
  fi
done
echo "  PASS"

echo "[render-toggles] Case 3: RBAC — 'create' verb is NOT combined with resourceNames"
# Extract all rules blocks from any Role/ClusterRole rendered by the
# chart and assert no rule contains both `verbs: [..., create, ...]`
# AND a `resourceNames:` selector. This is the canonical defense
# against the bp-openbao 6+ provisioning loop incident
# (feedback_rbac_create_no_resourcenames.md, 2026-05-03).
RENDER_OUT="$TMP/ok.yaml" python3 - <<'PY'
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
if grep -qE "kind: (ServiceMonitor|PodMonitor|PrometheusRule)" "$TMP/ok.yaml"; then
  echo "FAIL: default render contains a Prometheus operator resource — observability toggles must default off (BLUEPRINT-AUTHORING.md §11.2)." >&2
  exit 1
fi
echo "  PASS"

echo "[render-toggles] Case 5: networkPolicy.enabled=false suppresses NetworkPolicy"
if ! helm template smoke-openclaw . "${COMMON_ARGS[@]}" \
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
if ! grep -q "pod-template.yaml: |" "$TMP/ok.yaml"; then
  echo "FAIL: per-user pod-template ConfigMap was not rendered (controller would have no pod-spec template at runtime)." >&2
  exit 1
fi
# Assert the substitution placeholders the controller will fill at
# session-start are present in the rendered template.
for placeholder in '${USER_UUID}' '${SECRET_NAME}'; do
  if ! grep -qF "$placeholder" "$TMP/ok.yaml"; then
    echo "FAIL: per-user pod-template missing controller substitution placeholder ${placeholder}" >&2
    exit 1
  fi
done
echo "  PASS"

echo "[render-toggles] Case 7: ingress carries cert-manager cluster-issuer annotation"
if ! grep -q 'cert-manager.io/cluster-issuer: "letsencrypt-prod"' "$TMP/ok.yaml"; then
  echo "FAIL: ingress is missing cert-manager.io/cluster-issuer annotation — ACME auto-issue won't fire." >&2
  exit 1
fi
echo "  PASS"

echo "[render-toggles] All bp-openclaw render-toggle gates green."
