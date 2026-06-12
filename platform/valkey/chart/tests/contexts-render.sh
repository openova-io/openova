#!/usr/bin/env bash
# bp-valkey Contexts render gate (#3370 — the generality proof).
#
# Locks the platform-wide reuse mechanism on a NON-database blueprint:
#   1. Default render → ZERO Catalyst overlay resources (no Context
#      Secrets, no Application CR) — byte-compatible with 1.0.x.
#   2. keyspaces[] declared → ONE reflected credential Secret per
#      Context, in the RELEASE namespace (the #3283 push-source
#      pattern), carrying host/port/index/uri + the reflector push
#      annotations naming the consumer namespace. Kind labels stamped
#      for discovery.
#   3. auth disabled (default) → uri carries no password; auth enabled
#      → password minted + carried in the uri.
#   4. bootstrapOwned + Application CRD registered → exactly ONE
#      apps.openova.io/v1 Application CR, spec.bootstrap=true, with the
#      keyspaces[] Context declarations riding spec.parameters.
#   5. bootstrapOwned WITHOUT the CRD → no Application CR, no error
#      (the Capabilities first-bootstrap gate).
#
# Usage: bash tests/contexts-render.sh [CHART_DIR]
# CI consumes this via blueprint-release.yaml's `tests/*.sh` gate.

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

fail() { echo "FAIL: $1" >&2; exit 1; }

# Same dep handling as observability-toggle.sh.
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

echo "[contexts] Case 1: default render → no Context Secrets, no Application CR"
helm template valkey . > "$TMP/default.yaml"
if grep -q 'catalyst.openova.io/context-kind' "$TMP/default.yaml"; then
  fail "default render emitted a Context Secret with no keyspaces declared"
fi
if grep -qE '^kind: Application$' "$TMP/default.yaml"; then
  fail "default render emitted an Application CR with bootstrapOwned off"
fi

echo "[contexts] Case 2: keyspaces[] → one reflected credential Secret per Context"
cat > "$TMP/keyspaces.values.yaml" <<'YAML'
keyspaces:
  - name: sessions
    index: 2
    consumer: { blueprint: bp-newapi, mode: shared }
    reflect: { secretName: newapi-keyspace-credential, namespaces: [newapi] }
  - name: queues
    index: 3
    consumer: { blueprint: bp-openova-flow-server, mode: shared }
    reflect: { secretName: flow-keyspace-credential, namespaces: [catalyst-system] }
YAML
helm template valkey . -f "$TMP/keyspaces.values.yaml" --namespace valkey > "$TMP/ks.yaml" 2> "$TMP/ks.err" || {
  cat "$TMP/ks.err" >&2; fail "keyspaces render errored"; }
ctx_count=$(grep -c 'catalyst.openova.io/context-kind: keyspace' "$TMP/ks.yaml" || true)
[ "$ctx_count" -eq 2 ] || fail "expected 2 Context Secrets, got $ctx_count"
grep -q 'name: "newapi-keyspace-credential"' "$TMP/ks.yaml" \
  || fail "newapi Context Secret missing"
# Hub lives in the RELEASE namespace (push-source, #3283 lesson).
if grep -qE '^  namespace: "?(newapi|catalyst-system)"?$' "$TMP/ks.yaml"; then
  fail "a Context Secret targets the consumer namespace directly — re-introduces the #3283 deadlock"
fi
grep -q 'reflector.v1.k8s.emberstack.com/reflection-auto-namespaces: "newapi"' "$TMP/ks.yaml" \
  || fail "newapi Context Secret missing the reflector push annotation"
grep -q 'catalyst.openova.io/context-consumer: "bp-newapi"' "$TMP/ks.yaml" \
  || fail "Context Secret missing the consumer label"
# Wire contract: host/port/index/uri; passwordless uri when auth off.
grep -q 'host: "valkey-primary.valkey.svc.cluster.local"' "$TMP/ks.yaml" \
  || fail "Context Secret missing the derived primary host"
grep -q 'index: "2"' "$TMP/ks.yaml" || fail "Context Secret missing the keyspace index"
grep -q 'uri: "redis://valkey-primary.valkey.svc.cluster.local:6379/2"' "$TMP/ks.yaml" \
  || fail "Context Secret missing the passwordless keyspace uri"

echo "[contexts] Case 3: auth enabled → password minted + carried in the uri"
helm template valkey . -f "$TMP/keyspaces.values.yaml" --namespace valkey \
  --set valkey.auth.enabled=true > "$TMP/ks-auth.yaml" 2>&1 || fail "auth render errored"
grep -qE '^  password: ' "$TMP/ks-auth.yaml" || fail "auth render missing the minted password"
grep -qE 'uri: "redis://:[A-Za-z0-9]+@valkey-primary' "$TMP/ks-auth.yaml" \
  || fail "auth render uri missing the password"

echo "[contexts] Case 4: bootstrapOwned + CRD → ONE Application CR with the Context declarations"
helm template valkey . -f "$TMP/keyspaces.values.yaml" --namespace valkey \
  --set bootstrapOwned.enabled=true \
  --set bootstrapOwned.helmRelease.name=bp-valkey \
  --api-versions apps.openova.io/v1 > "$TMP/appcr.yaml" 2> "$TMP/appcr.err" || {
  cat "$TMP/appcr.err" >&2; fail "bootstrapOwned render errored"; }
appcr_count=$(grep -cE '^kind: Application$' "$TMP/appcr.yaml" || true)
[ "$appcr_count" -eq 1 ] || fail "expected EXACTLY 1 Application CR, got $appcr_count"
grep -q '^  bootstrap: true$' "$TMP/appcr.yaml" || fail "Application CR missing spec.bootstrap=true"
grep -q 'name: "bp-valkey"' "$TMP/appcr.yaml" || fail "Application CR missing spec.helmRelease.name"
awk '/^kind: Application$/{s=1} s' "$TMP/appcr.yaml" > "$TMP/appcr-only.yaml"
grep -q 'name: sessions' "$TMP/appcr-only.yaml" \
  || fail "Application CR parameters missing the sessions Context"
grep -q 'blueprint: bp-newapi' "$TMP/appcr-only.yaml" \
  || fail "Application CR parameters missing the newapi consumer"

echo "[contexts] Case 5: bootstrapOwned WITHOUT the CRD → benign skip"
helm template valkey . -f "$TMP/keyspaces.values.yaml" --namespace valkey \
  --set bootstrapOwned.enabled=true > "$TMP/appcr-nocrd.yaml" 2>&1 || fail "no-CRD render errored"
if grep -qE '^kind: Application$' "$TMP/appcr-nocrd.yaml"; then
  fail "Application CR rendered WITHOUT the apps.openova.io/v1 CRD registered"
fi

echo "[contexts] PASS — bp-valkey Contexts render gate green (#3370 generality proof: keyspace Contexts via the same declaration-driven machinery as bp-postgres databases[])"
