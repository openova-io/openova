#!/usr/bin/env bash
# bp-sso-bridge — #4458 egress-CNP openbao/keycloak/DNS contract guard.
#
# THE CILIUM TRAP this guards against:
#   The sibling K8s `NetworkPolicy/bp-sso-bridge` egress block names
#   openbao:8200 + keycloak:8080 + kube-dns. BUT the
#   `sso-bridge-reconciler-apiserver` CiliumNetworkPolicy attaches an
#   egress rule to the SAME reconciler endpoint — which flips the
#   endpoint to CNP-ENFORCED egress (Cilium default-deny), rendering the
#   ENTIRE K8s-NP egress allow-list INERT (reserved-identity supersession,
#   same class as 0.2.7/#2861 ipBlock-excludes-kube-apiserver + openclaw
#   #4401 JWKS-egress). So the openbao/keycloak/DNS allows MUST be
#   re-stated on the CNP itself, or the reconciler's
#   `openbao.openbao.svc:8200` + `keycloak.keycloak.svc:8080` dials are
#   silently policy-dropped → `no OpenBao token` every tick → grafana/
#   hubble 503 + gitea 500 (their SSO secret never seeded).
#
# This script asserts the rendered CNP egress (the cilium.io/v2 render,
# triggered via --api-versions) names:
#   (1) kube-apiserver  (pre-existing apiserver hop — must NOT regress)
#   (2) kube-dns        (name-resolution must survive CNP enforcement)
#   (3) the openbao namespace on the openbao port
#   (4) the keycloak namespace on the keycloak Pod target port 8080
# and that the openbao/keycloak namespace + port track the SAME
# networkPolicy.* knobs the K8s NetworkPolicy uses (overlay lockstep).
#
# Usage: bash tests/4458-egress-cnp-openbao.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

helm="${HELM_BIN:-helm}"

CNP_NAME="sso-bridge-reconciler-apiserver"

# Render WITH the cilium.io/v2 CRD capability so the CNP template fires.
render() {
  "$helm" template smoke "$CHART_DIR" \
    --api-versions "cilium.io/v2" \
    "$@" 2>/dev/null
}

# Extract the CiliumNetworkPolicy doc from a multi-doc render: print from
# the `kind: CiliumNetworkPolicy` line up to the next doc separator. The
# chart renders exactly one CNP, named $CNP_NAME (asserted in Case 1).
extract_cnp() {
  awk '/^kind: CiliumNetworkPolicy/{f=1} f&&/^---/{exit} f{print}' "$1"
}

# ── Case 1: default render — CNP egress names all four destinations ──────
render > "$TMP/default.yaml"
extract_cnp "$TMP/default.yaml" > "$TMP/cnp.yaml"

if ! grep -q "kind: CiliumNetworkPolicy" "$TMP/cnp.yaml"; then
  echo "FAIL: CiliumNetworkPolicy/$CNP_NAME did not render with --api-versions cilium.io/v2" >&2
  exit 1
fi

assert_in_cnp() {
  local needle="$1" why="$2"
  if ! grep -qF "$needle" "$TMP/cnp.yaml"; then
    echo "FAIL: CNP/$CNP_NAME egress missing: $needle" >&2
    echo "      ($why)" >&2
    echo "      Without it, the K8s-NP allow is INERT (Cilium CNP-enforced egress" >&2
    echo "      supersession, #4458) and the reconciler dial is policy-dropped." >&2
    echo "--- rendered CNP ---" >&2
    cat "$TMP/cnp.yaml" >&2
    exit 1
  fi
}

# kube-apiserver hop must NOT regress.
assert_in_cnp "kube-apiserver"  "apiserver egress — pre-existing, must not regress"
# DNS must survive CNP enforcement.
assert_in_cnp "k8s-app: kube-dns" "kube-dns egress — name-resolution under CNP-enforced egress"
# openbao + keycloak namespaces (default knobs).
assert_in_cnp "k8s:io.kubernetes.pod.namespace: openbao"  "openbao egress — the §6 gap (#4458)"
assert_in_cnp "k8s:io.kubernetes.pod.namespace: keycloak" "keycloak egress — same trap as openbao"
# openbao port (default 8200) + keycloak Pod target port 8080.
assert_in_cnp 'port: "8200"' "openbao port 8200"
assert_in_cnp 'port: "8080"' "keycloak Pod target port 8080 (Cilium enforces target port — 0.2.6/#2744)"

# ── Case 2: namespace knobs flow into the CNP (overlay lockstep) ─────────
# A per-Sovereign overlay re-homing openbao/keycloak (e.g. into a vCluster
# `mgmt` ns) must re-point the CNP too — not just the K8s NP.
render \
  --set networkPolicy.openbaoNamespace=mgmt \
  --set networkPolicy.keycloakNamespace=mgmt \
  > "$TMP/override.yaml"
extract_cnp "$TMP/override.yaml" > "$TMP/cnp-override.yaml"

if ! grep -qF "k8s:io.kubernetes.pod.namespace: mgmt" "$TMP/cnp-override.yaml"; then
  echo "FAIL: CNP egress does not track networkPolicy.{openbao,keycloak}Namespace override" >&2
  echo "      Overlay re-homed openbao/keycloak to 'mgmt' but the CNP still names the default ns." >&2
  echo "      The K8s NP and this CNP MUST move in lockstep, else the trap re-opens on the overlay." >&2
  exit 1
fi

echo "PASS: #4458 egress-CNP openbao/keycloak/dns contract"
echo "      CNP names kube-apiserver + kube-dns + openbao:8200 + keycloak:8080,"
echo "      and tracks the networkPolicy.* namespace knobs in lockstep with the K8s NP."
