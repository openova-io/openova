#!/usr/bin/env bash
# bp-newapi — sandbox-bridge readiness / 1st-hit-111 render audit (#3374).
#
# The hw159 walk showed the zero-click bare URL throwing
# `upstream-connect-error ... delayed connect error: 111` on the 1st hit, then
# landing on the /setup wizard. ROOT CAUSE: the bridge serves the Exact `/`
# landing page, but as a PLAIN container it was gated behind NewAPI's ~2-min
# warm-up TWICE — (1) the wait-for-sql-dsn initContainer runs to completion
# before any plain container starts, and (2) a Pod only joins a Service's
# EndpointSlice once ALL its containers are Ready (the `newapi` container is
# NotReady during GORM AutoMigrate), so the Service had ZERO ready endpoints on
# every port → the gateway's Exact `/` backendRef had no healthy upstream → 111.
#
# The fix renders THREE things this test asserts:
#   1. The bridge is a NATIVE sidecar (initContainers entry, restartPolicy:
#      Always) declared BEFORE wait-for-sql-dsn, carrying a readinessProbe on
#      the bridge port — and it is NOT duplicated in the plain containers list.
#   2. A dedicated bridge-only Service `<fullname>-bridge` with
#      publishNotReadyAddresses:true exists, so its endpoint publishes while the
#      Pod is NotReady.
#   3. The HTTPRoute's Exact `/` rule backendRefs the bridge-only Service (NOT
#      the main Service) on the bridge port.
#
# Plus the operator opt-out:
#   4. sandboxBridge.nativeSidecar=false falls back to the LEGACY plain
#      container (in containers:, no restartPolicy:Always, no native sidecar).

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

# Slot-overlay-equivalent values: keycloak mode + sovereignFQDN + httpRoute on
# (the full zero-click SSO path the bootstrap-kit slot 80 overlay drives).
common_values=$(mktemp)
trap 'rm -f "$common_values"' EXIT
cat > "$common_values" <<'EOF'
sovereignFQDN: hw159.omani.works
auth:
  adminUI:
    mode: keycloak
    keycloak:
      issuer: https://auth.hw159.omani.works/realms/sovereign
      clientId: newapi-admin
      existingSecret: newapi-oidc
ingress:
  host: api.hw159.omani.works
  httpRoute:
    enabled: true
    host: newapi.hw159.omani.works
EOF

out=$("$helm" template smoke "$chart_dir" -f "$common_values" 2>&1)

# Helper: extract the Deployment document.
deployment_block() { echo "$out" | awk '/^kind: Deployment$/{f=1} f&&/^kind: /&&!/^kind: Deployment$/{f=0} f'; }

dep=$(deployment_block)
if [ -z "$dep" ]; then
  echo "FAIL: Deployment did not render"
  exit 1
fi

# ── Case 1: bridge is a native sidecar (initContainers) with readinessProbe ──
echo "[bp-newapi] Case 1: bridge native sidecar in initContainers w/ readinessProbe"

init_block=$(echo "$dep" | awk '/initContainers:/{f=1} f&&/^      containers:/{f=0} f')
if [ -z "$init_block" ]; then
  echo "FAIL: no initContainers block (native-sidecar bridge + dsn gate expected)"
  exit 1
fi

# The native-sidecar bridge must be in initContainers with restartPolicy:Always.
bridge_init=$(echo "$init_block" | awk '
  /^        - name: sandbox-bridge[[:space:]]*$/{f=1; print; next}
  f && /^        - name: /{f=0}
  f{print}
')
if [ -z "$bridge_init" ]; then
  echo "FAIL: sandbox-bridge not present as a native sidecar in initContainers"
  echo "--- initContainers ---"; echo "$init_block"
  exit 1
fi
if ! grep -q "restartPolicy: Always" <<<"$bridge_init"; then
  echo "FAIL: native-sidecar bridge missing restartPolicy: Always (KEP-753 native sidecar)"
  exit 1
fi
# readinessProbe on the bridge port (8080) — this is the explicit ask: the
# Service has no ready endpoint until the bridge answers /healthz.
bridge_ready=$(echo "$bridge_init" | awk '
  /^          readinessProbe:/{f=1; print; next}
  f && /^          [a-zA-Z]/{f=0}
  f{print}
')
if [ -z "$bridge_ready" ]; then
  echo "FAIL: native-sidecar bridge has no readinessProbe"
  exit 1
fi
if ! grep -q "path: /healthz" <<<"$bridge_ready"; then
  echo "FAIL: bridge readinessProbe path is not /healthz"
  exit 1
fi
if ! grep -q "port: 8080" <<<"$bridge_ready"; then
  echo "FAIL: bridge readinessProbe port is not the bridge port 8080"
  exit 1
fi
# Init-ordering: the bridge must come BEFORE wait-for-sql-dsn so it starts
# first (the DSN gate must not block the landing page).
bridge_line=$(echo "$init_block" | grep -n "name: sandbox-bridge" | head -1 | cut -d: -f1)
dsn_line=$(echo "$init_block" | grep -n "name: wait-for-sql-dsn" | head -1 | cut -d: -f1)
if [ -n "$dsn_line" ] && [ "$bridge_line" -ge "$dsn_line" ]; then
  echo "FAIL: native-sidecar bridge must be declared BEFORE wait-for-sql-dsn (got bridge@$bridge_line, dsn@$dsn_line)"
  exit 1
fi
# It must NOT also appear as a plain container (no duplication).
containers_only=$(echo "$dep" | awk '/^      containers:/{f=1} f' | awk '/^      volumes:/{exit} {print}')
if grep -q "name: sandbox-bridge" <<<"$containers_only"; then
  echo "FAIL: sandbox-bridge duplicated as a plain container while native sidecar is on"
  exit 1
fi
echo "[bp-newapi] Case 1: PASS"

# ── Case 2: bridge-only Service with publishNotReadyAddresses ────────────────
echo "[bp-newapi] Case 2: bridge-only Service w/ publishNotReadyAddresses"
bridge_svc=$(echo "$out" | awk 'BEGIN{RS="---\n"} /kind: Service/ && /name: smoke-bp-newapi-bridge/ {print}')
if [ -z "$bridge_svc" ]; then
  echo "FAIL: dedicated bridge-only Service <fullname>-bridge did not render"
  exit 1
fi
if ! grep -q "publishNotReadyAddresses: true" <<<"$bridge_svc"; then
  echo "FAIL: bridge-only Service missing publishNotReadyAddresses: true"
  echo "--- bridge service ---"; echo "$bridge_svc"
  exit 1
fi
if ! grep -q "port: 8080" <<<"$bridge_svc"; then
  echo "FAIL: bridge-only Service does not expose the bridge port 8080"
  exit 1
fi
echo "[bp-newapi] Case 2: PASS"

# ── Case 3: HTTPRoute Exact / → bridge-only Service ─────────────────────────
echo "[bp-newapi] Case 3: HTTPRoute Exact / backendRefs the bridge-only Service"
httproute=$(echo "$out" | awk 'BEGIN{RS="---\n"} /kind: HTTPRoute/ {print}')
if [ -z "$httproute" ]; then
  echo "FAIL: HTTPRoute did not render"
  exit 1
fi
# Extract the Exact `/` rule's backendRef name (the block following type: Exact).
exact_backend=$(echo "$httproute" | awk '
  /type: Exact/{e=1}
  e && /name: /{print; e=0}
')
if ! grep -q "smoke-bp-newapi-bridge" <<<"$exact_backend"; then
  echo "FAIL: Exact / rule does not backendRef the bridge-only Service smoke-bp-newapi-bridge"
  echo "--- Exact backendRef ---"; echo "$exact_backend"
  echo "--- httproute rules ---"; echo "$httproute" | awk '/rules:/{f=1} f'
  exit 1
fi
echo "[bp-newapi] Case 3: PASS"

# ── Case 4: operator opt-out → legacy plain-container bridge ─────────────────
echo "[bp-newapi] Case 4: nativeSidecar=false → legacy plain container"
optout_values=$(mktemp)
cat > "$optout_values" <<'EOF'
sovereignFQDN: hw159.omani.works
auth:
  adminUI:
    mode: keycloak
    keycloak:
      issuer: https://auth.hw159.omani.works/realms/sovereign
      clientId: newapi-admin
      existingSecret: newapi-oidc
sandboxBridge:
  nativeSidecar: false
EOF
out2=$("$helm" template smoke "$chart_dir" -f "$optout_values" 2>&1)
rm -f "$optout_values"
dep2=$(echo "$out2" | awk '/^kind: Deployment$/{f=1} f&&/^kind: /&&!/^kind: Deployment$/{f=0} f')

# In opt-out mode the bridge must be a PLAIN container, never a native sidecar.
init_block2=$(echo "$dep2" | awk '/initContainers:/{f=1} f&&/^      containers:/{f=0} f')
if grep -q "name: sandbox-bridge" <<<"$init_block2"; then
  echo "FAIL: nativeSidecar=false but bridge still rendered as a native sidecar (default-true-bool coercion bug?)"
  exit 1
fi
containers2=$(echo "$dep2" | awk '/^      containers:/{f=1} f' | awk '/^      volumes:/{exit} {print}')
if ! grep -q "name: sandbox-bridge" <<<"$containers2"; then
  echo "FAIL: nativeSidecar=false but bridge not present as a plain container"
  exit 1
fi
# Exactly one bridge container in either mode.
n=$(echo "$dep2" | grep -c "name: sandbox-bridge")
if [ "$n" != "1" ]; then
  echo "FAIL: expected exactly 1 sandbox-bridge container in opt-out mode, got $n"
  exit 1
fi
echo "[bp-newapi] Case 4: PASS"

echo "[bp-newapi] All sandbox-bridge readiness render cases PASS"
