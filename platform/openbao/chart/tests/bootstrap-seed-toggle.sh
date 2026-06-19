#!/usr/bin/env bash
# bp-openbao bootstrap-seed-toggle render test (#3888, Refs #3847).
#
# Verifies the prov-time openbao KV-seed mechanism — the missing piece #3890
# flagged that blocks evicting inline cloud-init Secrets to Flux ExternalSecrets.
# The auth-bootstrap Job (which still holds the valid root token before revoking
# it) reads a one-shot cloud-init Secret + writes its keys into openbao KV at
# secret/<prefix>/* so ExternalSecrets can materialise them via vault-region1.
#
#   Case 1 — default render (autoUnseal.enabled=false): NO seed artefacts.
#   Case 2 — autoUnseal+kubernetesAuth on, bootstrapSeed OFF (current prod
#            posture): the seed script + env vars render but BOOTSTRAP_SEED_ENABLED
#            is "false" and NO seed volume/mount is emitted — behaviourally inert,
#            so every live Sovereign on this chart is unaffected.
#   Case 3 — bootstrapSeed.enabled=true: the optional seed volume + read-only
#            mount + BOOTSTRAP_SEED_ENABLED="true" all render.
#   Case 4 — ORDERING: the `bao kv put` seed write appears BEFORE the
#            `bao token revoke -self` line (after revoke there is no token with
#            write access to secret/*; seeding-after-revoke would 403).
#   Case 5 — custom kvPrefix + secretName flow through.
#   Case 6 — the rendered seed script parses clean under `dash -n` (POSIX),
#            matching the cloud-init lint discipline (#3129).
#
# Usage: bash tests/bootstrap-seed-toggle.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

GW="--set gateway.host=bao.test.example.com"

# ── Case 1 ──────────────────────────────────────────────────────────────────
echo "[bootstrap-seed-toggle] Case 1: default render produces no seed artefacts"
helm template smoke-bao . $GW > "$TMP/default.yaml"
if grep -qE "BOOTSTRAP_SEED|name: bootstrap-seed|prov-time KV-seed" "$TMP/default.yaml"; then
  echo "FAIL: default render contains seed artefacts — skip-render is broken." >&2
  grep -nE "BOOTSTRAP_SEED|name: bootstrap-seed|prov-time KV-seed" "$TMP/default.yaml" >&2
  exit 1
fi
echo "  PASS"

# ── Case 2 ──────────────────────────────────────────────────────────────────
echo "[bootstrap-seed-toggle] Case 2: prod posture (bootstrapSeed OFF) is inert"
helm template smoke-bao . $GW \
  --set autoUnseal.enabled=true \
  --set autoUnseal.kubernetesAuth.enabled=true \
  > "$TMP/off.yaml" 2> "$TMP/off.err" || { echo "FAIL: render failed:" >&2; cat "$TMP/off.err" >&2; exit 1; }
if ! grep -qE 'name: BOOTSTRAP_SEED_ENABLED' "$TMP/off.yaml"; then
  echo "FAIL: BOOTSTRAP_SEED_ENABLED env not rendered on auth-bootstrap Job." >&2
  exit 1
fi
# The value must be the literal "false" (the seed step never executes).
if ! awk '/name: BOOTSTRAP_SEED_ENABLED/{getline; print}' "$TMP/off.yaml" | grep -qE 'value:[[:space:]]*"false"'; then
  echo "FAIL: BOOTSTRAP_SEED_ENABLED is not \"false\" in prod posture." >&2
  exit 1
fi
if grep -qE '^[[:space:]]*- name: bootstrap-seed$' "$TMP/off.yaml"; then
  echo "FAIL: bootstrap-seed volume/mount rendered despite bootstrapSeed disabled." >&2
  exit 1
fi
echo "  PASS"

# ── Case 3 ──────────────────────────────────────────────────────────────────
echo "[bootstrap-seed-toggle] Case 3: bootstrapSeed.enabled=true emits volume + mount + env"
helm template smoke-bao . $GW \
  --set autoUnseal.enabled=true \
  --set autoUnseal.kubernetesAuth.enabled=true \
  --set autoUnseal.bootstrapSeed.enabled=true \
  > "$TMP/on.yaml" 2> "$TMP/on.err" || { echo "FAIL: render failed:" >&2; cat "$TMP/on.err" >&2; exit 1; }
if ! awk '/name: BOOTSTRAP_SEED_ENABLED/{getline; print}' "$TMP/on.yaml" | grep -qE 'value:[[:space:]]*"true"'; then
  echo "FAIL: BOOTSTRAP_SEED_ENABLED is not \"true\" when bootstrapSeed.enabled." >&2
  exit 1
fi
# Expect BOTH the volume entry and the volumeMount entry (2 occurrences).
VOL_COUNT=$(grep -cE '^[[:space:]]*- name: bootstrap-seed$' "$TMP/on.yaml" || true)
if [ "$VOL_COUNT" -lt 2 ]; then
  echo "FAIL: expected the bootstrap-seed volume AND mount (>=2), got $VOL_COUNT." >&2
  exit 1
fi
if ! grep -qE '/var/run/openbao/bootstrap-seed' "$TMP/on.yaml"; then
  echo "FAIL: bootstrap-seed mountPath not rendered." >&2
  exit 1
fi
# The seed Secret mount must be optional (a prov that seeds nothing must not
# block Job scheduling).
if ! awk '/- name: bootstrap-seed/{f=1} f&&/optional: true/{print; exit}' "$TMP/on.yaml" | grep -q 'optional: true'; then
  echo "FAIL: bootstrap-seed Secret volume is not optional:true." >&2
  exit 1
fi
echo "  PASS"

# ── Case 4 ──────────────────────────────────────────────────────────────────
echo "[bootstrap-seed-toggle] Case 4: KV-seed write precedes the root-token revoke"
KV_LINE=$(grep -nE 'bao kv put "\$KV_MOUNT_PATH/\$BOOTSTRAP_SEED_KV_PREFIX' "$TMP/on.yaml" | head -1 | cut -d: -f1)
REVOKE_LINE=$(grep -nE 'bao token revoke -self' "$TMP/on.yaml" | head -1 | cut -d: -f1)
if [ -z "$KV_LINE" ] || [ -z "$REVOKE_LINE" ]; then
  echo "FAIL: could not locate the kv-put ($KV_LINE) or revoke ($REVOKE_LINE) line." >&2
  exit 1
fi
if [ "$KV_LINE" -ge "$REVOKE_LINE" ]; then
  echo "FAIL: seed kv-put (line $KV_LINE) must precede root-token revoke (line $REVOKE_LINE)." >&2
  exit 1
fi
echo "  PASS (kv-put @${KV_LINE} < revoke @${REVOKE_LINE})"

# ── Case 5 ──────────────────────────────────────────────────────────────────
echo "[bootstrap-seed-toggle] Case 5: custom kvPrefix + secretName flow through"
helm template smoke-bao . $GW \
  --set autoUnseal.enabled=true \
  --set autoUnseal.kubernetesAuth.enabled=true \
  --set autoUnseal.bootstrapSeed.enabled=true \
  --set autoUnseal.bootstrapSeed.kvPrefix=custompref \
  --set autoUnseal.bootstrapSeed.secretName=my-seed \
  > "$TMP/custom.yaml" 2>/dev/null
if ! awk '/name: BOOTSTRAP_SEED_KV_PREFIX/{getline; print}' "$TMP/custom.yaml" | grep -qE 'value:[[:space:]]*"custompref"'; then
  echo "FAIL: custom kvPrefix not threaded into BOOTSTRAP_SEED_KV_PREFIX." >&2
  exit 1
fi
if ! grep -qE 'secretName: "my-seed"' "$TMP/custom.yaml"; then
  echo "FAIL: custom secretName not threaded into the seed volume." >&2
  exit 1
fi
# The single-use delete must target the custom Secret name.
if ! grep -qE 'secrets/my-seed' "$TMP/custom.yaml"; then
  echo "FAIL: single-use delete does not target the custom secretName." >&2
  exit 1
fi
echo "  PASS"

# ── Case 6 ──────────────────────────────────────────────────────────────────
echo "[bootstrap-seed-toggle] Case 6: rendered seed script parses under dash -n (POSIX)"
DASH_BIN="${DASH_BIN:-dash}"
if command -v "$DASH_BIN" >/dev/null 2>&1; then
  # Extract the auth-bootstrap Job's inline shell script (the `args: - |` block)
  # and parse-check it. We pull the whole rendered Job doc + isolate the script
  # body heuristically: everything indented under the `args:` literal block.
  awk '
    /name: openbao-auth-bootstrap/ { injob=1 }
    injob && /args:/ { inargs=1; next }
    inargs && /^[[:space:]]*- \|/ { inscript=1; next }
    inscript {
      # the script block ends when indentation drops back to the container key level
      if ($0 ~ /^          [a-zA-Z]/) { inscript=0; injob=0; inargs=0; next }
      sub(/^              /, "")
      print
    }
  ' "$TMP/on.yaml" > "$TMP/script.sh"
  if [ ! -s "$TMP/script.sh" ]; then
    echo "FAIL: could not extract the auth-bootstrap script for dash -n." >&2
    exit 1
  fi
  if ! "$DASH_BIN" -n "$TMP/script.sh" 2>"$TMP/dash.err"; then
    echo "FAIL: auth-bootstrap script has a POSIX syntax error:" >&2
    cat "$TMP/dash.err" >&2
    exit 1
  fi
  echo "  PASS (dash -n clean)"
else
  echo "  SKIP (dash not on PATH)"
fi

echo "[bootstrap-seed-toggle] All bp-openbao bootstrap-seed-toggle gates green."
