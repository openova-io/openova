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
# #4272: Ingress is NO LONGER a default-rendered kind — a Sovereign runs the
# Cilium Gateway, not traefik, so ingress.enabled defaults false and the
# traefik Ingress is INERT there. HTTPRoute is the Sovereign exposure (asserted
# in Case 8 with httpRoute.enabled=true).
for kind in Deployment Service Role RoleBinding ConfigMap NetworkPolicy ServiceAccount; do
  if ! grep -qE "^kind: ${kind}$" "$TMP/default.yaml"; then
    echo "FAIL: default render is missing kind=${kind}" >&2
    exit 1
  fi
done
# The traefik Ingress must NOT render by default (it is inert on a Sovereign).
if grep -qE "^kind: Ingress$" "$TMP/default.yaml"; then
  echo "FAIL: default render still emits a traefik Ingress — ingress.enabled must default false on a Cilium-Gateway Sovereign (#4272)." >&2
  exit 1
fi
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

echo "[render-toggles] Case 2b: assertNoPlaceholders=true with all real values (canonical oidc/llm blocks) renders successfully"
if ! helm template smoke-openclaw . \
    --set "assertNoPlaceholders=true" \
    --set "oidc.issuerURL=https://kc.acme.example/realms/sme-acme" \
    --set "oidc.clientSecret.name=openclaw-oidc-client-secret" \
    --set "llm.baseURL=https://api.acme.example/v1" \
    --set "llm.defaultModel=qwen3.6" \
    --set "tenant.namespace=sme-acme" \
    --set "controller.image.tag=sha-deadbeefdeadbeefdeadbeefdeadbeefdeadbeef" \
    --set "perUserPod.image.tag=sha-cafef00dcafef00dcafef00dcafef00dcafef00d" \
    --set "ingress.host=openclaw.acme.example" \
    > "$TMP/real.yaml" 2> "$TMP/real.err"; then
  echo "FAIL: assertNoPlaceholders=true with real values failed:" >&2
  cat "$TMP/real.err" >&2
  exit 1
fi
# Assert canonical envs are present on the controller.
# #6114: LLM_API_KEY / OPENAI_API_KEY are NOT in this list. They carried a
# secretKeyRef at `llm.apiKey.name` whose target Secret has no producer
# anywhere in the repo, and the controller binary reads neither env — the
# reference was removed rather than backed by a token nothing consumes.
for env in OIDC_ISSUER_URL OIDC_CLIENT_ID OIDC_CLIENT_SECRET \
           LLM_BASE_URL LLM_DEFAULT_MODEL \
           OPENAI_API_BASE \
           KEYCLOAK_REALM_URL NEWAPI_BASE_URL_DEFAULT; do
  if ! grep -q "name: ${env}" "$TMP/real.yaml"; then
    echo "FAIL: controller env ${env} missing from rendered manifests" >&2
    exit 1
  fi
done
# #6114 regression guard: the controller must not MOUNT a Secret this chart
# does not create. Match `name:` at a reference position only — a bare grep for
# the dead names also hits the explanatory comments the templates render, which
# would make this guard fail on its own documentation.
if grep -qE "^[[:space:]]*name:[[:space:]]*(openclaw-newapi-controller-token|openclaw-llm-apikey)[[:space:]]*$" "$TMP/real.yaml"; then
  echo "FAIL: rendered manifests reference a controller-side NewAPI token Secret," >&2
  echo "      but no template in this chart creates one (#6114)." >&2
  exit 1
fi
# Vacuity check for the guard above: the same matcher MUST fire on a known
# reference, otherwise a typo'd pattern would let the defect back in silently.
if ! printf '                  name: openclaw-newapi-controller-token\n' \
     | grep -qE "^[[:space:]]*name:[[:space:]]*(openclaw-newapi-controller-token|openclaw-llm-apikey)[[:space:]]*$"; then
  echo "FAIL: the #6114 reference matcher does not match a known-bad line — guard is vacuous." >&2
  exit 1
fi
# Assert the per-user pod-template ConfigMap carries OPENAI_API_BASE +
# LLM_DEFAULT_MODEL so OpenAI-SDK-based runtimes work without code change.
if ! grep -q "OPENAI_API_BASE" "$TMP/real.yaml"; then
  echo "FAIL: per-user pod template missing OPENAI_API_BASE env" >&2
  exit 1
fi
if ! grep -q "LLM_DEFAULT_MODEL" "$TMP/real.yaml"; then
  echo "FAIL: per-user pod template missing LLM_DEFAULT_MODEL env" >&2
  exit 1
fi
# Assert OIDC issuer + LLM baseURL values reach the rendered Deployment
# verbatim from --set.
if ! grep -q "https://kc.acme.example/realms/sme-acme" "$TMP/real.yaml"; then
  echo "FAIL: OIDC issuer URL not propagated to rendered Deployment" >&2
  exit 1
fi
if ! grep -q "https://api.acme.example/v1" "$TMP/real.yaml"; then
  echo "FAIL: LLM baseURL not propagated to rendered Deployment" >&2
  exit 1
fi
echo "  PASS"

echo "[render-toggles] Case 2c: legacy keycloak.* / newapi.* keys still work as fallbacks"
if ! helm template smoke-openclaw . \
    --set "assertNoPlaceholders=true" \
    --set "keycloak.realmURL=https://kc.legacy.example/realms/legacy" \
    --set "keycloak.clientSecretName=openclaw-oidc-legacy" \
    --set "newapi.baseURL=https://newapi.legacy.example/v1" \
    --set "tenant.namespace=sme-legacy" \
    --set "controller.image.tag=sha-deadbeefdeadbeefdeadbeefdeadbeefdeadbeef" \
    --set "perUserPod.image.tag=sha-cafef00dcafef00dcafef00dcafef00dcafef00d" \
    --set "ingress.host=openclaw.legacy.example" \
    > "$TMP/legacy.yaml" 2> "$TMP/legacy.err"; then
  echo "FAIL: legacy-key render failed:" >&2
  cat "$TMP/legacy.err" >&2
  exit 1
fi
if ! grep -q "https://kc.legacy.example/realms/legacy" "$TMP/legacy.yaml"; then
  echo "FAIL: legacy keycloak.realmURL did not reach rendered Deployment" >&2
  exit 1
fi
if ! grep -q "https://newapi.legacy.example/v1" "$TMP/legacy.yaml"; then
  echo "FAIL: legacy newapi.baseURL did not reach rendered Deployment" >&2
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

echo "[render-toggles] Case 7: ingress (when explicitly enabled) carries cert-manager cluster-issuer annotation"
# #4272 — ingress.enabled now defaults false (inert traefik Ingress on a
# Sovereign), so enable it explicitly here for the NON-Sovereign portability
# path. #4246 — default issuer is the DNS-01 PowerDNS ClusterIssuer every
# Sovereign installs; `letsencrypt-prod` does not exist on a real Sovereign.
if ! helm template smoke-openclaw . \
    --set "ingress.enabled=true" \
    > "$TMP/ingress-on.yaml" 2> "$TMP/ingress-on.err"; then
  echo "FAIL: ingress.enabled=true render failed:" >&2
  cat "$TMP/ingress-on.err" >&2
  exit 1
fi
if ! grep -qE "^kind: Ingress$" "$TMP/ingress-on.yaml"; then
  echo "FAIL: ingress.enabled=true did not render an Ingress — toggle is broken." >&2
  exit 1
fi
if ! grep -q 'cert-manager.io/cluster-issuer: "letsencrypt-dns01-prod-powerdns"' "$TMP/ingress-on.yaml"; then
  echo "FAIL: ingress is missing cert-manager.io/cluster-issuer annotation — ACME auto-issue won't fire." >&2
  exit 1
fi
echo "  PASS"

echo "[render-toggles] Case 8: Cilium-Gateway exposure — CNP fromEntities + HTTPRoute (#4272)"
# A Sovereign needs the CiliumNetworkPolicy fromEntities:[ingress,host,
# remote-node] carve-out (gateway hop + kubelet probe) AND an HTTPRoute on the
# console gateway. The CNP renders by default (cilium.io/v2 present in the
# helm-template Capabilities set); the HTTPRoute requires enabled + hostnames.
if ! helm template smoke-openclaw . \
    --api-versions "cilium.io/v2" \
    --set "httpRoute.enabled=true" \
    --set "httpRoute.hostnames[0]=openclaw.acme.omani.homes" \
    > "$TMP/gw.yaml" 2> "$TMP/gw.err"; then
  echo "FAIL: Cilium-Gateway render failed:" >&2
  cat "$TMP/gw.err" >&2
  exit 1
fi
# CNP present + correct fromEntities + the bare traefik-namespace ingress rule
# is GONE from the K8s NetworkPolicy (fromNamespaceLabels defaults empty).
RENDER_OUT="$TMP/gw.yaml" python3 - <<'PY'
import os, sys, yaml
docs = [d for d in yaml.safe_load_all(open(os.environ["RENDER_OUT"])) if d]
cnp = [d for d in docs if d.get("kind") == "CiliumNetworkPolicy"]
if not cnp:
    print("FAIL: no CiliumNetworkPolicy rendered with cilium.io/v2 present (#4272 gateway hop would be default-denied).", file=sys.stderr); sys.exit(1)
ents = set()
for d in cnp:
    for rule in d.get("spec", {}).get("ingress", []) or []:
        ents.update(rule.get("fromEntities", []) or [])
missing = {"ingress", "host", "remote-node"} - ents
if missing:
    print(f"FAIL: CNP fromEntities missing {sorted(missing)} (gateway/probe hop denied).", file=sys.stderr); sys.exit(1)
# K8s NetworkPolicy must NOT carry a traefik-namespace ingress rule by default.
for d in docs:
    if d.get("kind") != "NetworkPolicy":
        continue
    for rule in d.get("spec", {}).get("ingress", []) or []:
        for frm in rule.get("from", []) or []:
            lbls = (frm.get("namespaceSelector") or {}).get("matchLabels") or {}
            if lbls.get("kubernetes.io/metadata.name") == "traefik":
                print("FAIL: K8s NetworkPolicy still synthesises a dead traefik-namespace ingress rule (#4272).", file=sys.stderr); sys.exit(1)
PY
if ! grep -qE "^kind: HTTPRoute$" "$TMP/gw.yaml"; then
  echo "FAIL: httpRoute.enabled=true with a hostname did not render an HTTPRoute (#4272)." >&2
  exit 1
fi
if ! grep -q "cilium-gateway-console" "$TMP/gw.yaml"; then
  echo "FAIL: HTTPRoute does not parent cilium-gateway-console (would 404 on the console ELB, #4054/#4070)." >&2
  exit 1
fi
echo "  PASS"

echo "[render-toggles] Case 9: K8s NetworkPolicy egress permits the public-host :443 JWKS hairpin (#4272)"
# The /readyz handler fetches JWKS from the PUBLIC issuer host, which hairpins
# through the Cilium Gateway on :443. The egress rule must allow :443 to
# 0.0.0.0/0 (no Pod selector can name the external EIP), else readyz 503.
RENDER_OUT="$TMP/default.yaml" python3 - <<'PY'
import os, sys, yaml
docs = [d for d in yaml.safe_load_all(open(os.environ["RENDER_OUT"])) if d]
ok = False
for d in docs:
    if d.get("kind") != "NetworkPolicy":
        continue
    for rule in d.get("spec", {}).get("egress", []) or []:
        tos = rule.get("to", []) or []
        has_world = any((t.get("ipBlock") or {}).get("cidr") == "0.0.0.0/0" for t in tos)
        has_443 = any(p.get("port") == 443 for p in (rule.get("ports") or []))
        if has_world and has_443:
            ok = True
if not ok:
    print("FAIL: K8s NetworkPolicy egress is missing the :443 → 0.0.0.0/0 public-host JWKS hairpin rule — readyz would 503 (#4272).", file=sys.stderr)
    sys.exit(1)
PY
echo "  PASS"

echo "[render-toggles] Case 10: per-Org install (no tenant.namespace override) targets the release namespace, NOT a placeholder (#4952)"
# #4952: bp-openclaw 0.2.16 defaulted tenant.namespace to the truthy placeholder
# "org-example". The bp-openclaw.tenantNamespace helper is
# `default .Release.Namespace .Values.tenant.namespace` — Helm's `default`
# returns the SECOND arg when truthy, so the placeholder WON over
# .Release.Namespace and the controller Role + RoleBinding rendered with
# `metadata.namespace: org-example`. On a real Sovereign that namespace does not
# exist → Helm install failed `namespaces "org-example" not found` (×2: Role +
# RoleBinding). The fix defaults tenant.namespace to "" so the helper falls back
# to the release namespace. A per-Org install passes `--namespace <org-slug>`;
# assert the render targets THAT namespace and emits ZERO org-example literals.
if ! helm template smoke-openclaw . --namespace acme235 > "$TMP/org-ns.yaml" 2> "$TMP/org-ns.err"; then
  echo "FAIL: per-Org (--namespace acme235) render failed:" >&2
  cat "$TMP/org-ns.err" >&2
  exit 1
fi
# No placeholder namespace may survive anywhere in the render.
if grep -q "org-example" "$TMP/org-ns.yaml"; then
  echo "FAIL: render still contains the 'org-example' placeholder namespace — a per-Org install would fail 'namespaces \"org-example\" not found' (#4952)." >&2
  grep -n "org-example" "$TMP/org-ns.yaml" >&2
  exit 1
fi
# The controller Role + RoleBinding (the two resources that carry an explicit
# metadata.namespace = tenantNamespace) MUST land in the release namespace.
RENDER_OUT="$TMP/org-ns.yaml" python3 - <<'PY'
import os, sys, yaml
docs = [d for d in yaml.safe_load_all(open(os.environ["RENDER_OUT"])) if d]
want = "acme235"
checked = 0
for d in docs:
    if d.get("kind") not in {"Role", "RoleBinding"}:
        continue
    ns = d.get("metadata", {}).get("namespace")
    name = d.get("metadata", {}).get("name", "<unnamed>")
    checked += 1
    if ns != want:
        print(f"FAIL: {d['kind']} {name} metadata.namespace={ns!r}, expected {want!r} (the release namespace) — #4952 fallback broken.", file=sys.stderr)
        sys.exit(1)
if checked < 2:
    print(f"FAIL: expected the controller Role + RoleBinding (2 namespaced resources), found {checked}.", file=sys.stderr)
    sys.exit(1)
PY
echo "  PASS"

echo "[render-toggles] All bp-openclaw render-toggle gates green."
