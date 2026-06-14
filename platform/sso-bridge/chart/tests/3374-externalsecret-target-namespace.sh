#!/usr/bin/env bash
# bp-sso-bridge — #3374 ExternalSecret target-namespace regression guard.
#
# The per-app OIDC ExternalSecret that bp-sso-bridge emits MUST land in the
# namespace where the app POD actually runs — the HelmRelease's
# `targetNamespace` when set, NOT the HR's own namespace.
#
# Every Tier-1 SSO slot installs its HR in `flux-system` but targets the
# app's own namespace (bp-grafana→grafana, bp-harbor→harbor,
# bp-openbao→openbao, bp-gitea→gitea, bp-powerdns-admin→powerdns-admin).
# Before #3374 the reconciler computed the ES namespace from
# `.metadata.namespace` (= flux-system), so the projected
# `<cid>-sso-oidc-credentials` Secret materialised in flux-system —
# invisible to the app Pod's `envFromSecret`. grafana broke wholesale (its
# chart-side ES is omitted by the install-time `.Capabilities` ESO-CRD-race
# guard, so the bp-sso-bridge ES was the ONLY grafana ES) → grafana Pod 2/3
# CreateContainerConfigError → route 503 "no healthy upstream".
#
# This is a shape-only test: it renders the reconcile.sh ConfigMap and
# asserts the HR-namespace resolution falls through `.spec.targetNamespace`
# FIRST. Actual end-to-end is verified on a live Sovereign (the #3374 walk).
#
# Asserts:
#   1. The `ns` resolution in reconcile_one prefers `.spec.targetNamespace`
#      with `.metadata.namespace` only as the fallback.
#   2. emit_external_secret stamps `namespace: ${hrns}` (the resolved value),
#      i.e. it does NOT hardcode flux-system or re-read .metadata.namespace.
#   3. bash -n syntax-clean.
#
# Usage: bash tests/3374-externalsecret-target-namespace.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

helm="${HELM_BIN:-helm}"

"$helm" template smoke "$CHART_DIR" --show-only templates/configmap-reconciler.yaml \
  2>/dev/null > "$TMP/cm.yaml"

# Extract the embedded reconcile.sh out of the ConfigMap literal block.
python3 - "$TMP/cm.yaml" "$TMP/reconcile.sh" <<'PY'
import sys, yaml
doc = yaml.safe_load(open(sys.argv[1]))
open(sys.argv[2], "w").write(doc["data"]["reconcile.sh"])
PY

SCRIPT="$TMP/reconcile.sh"

# ── Case 1: ns resolution prefers targetNamespace ────────────────────────
# The canonical line is:
#   ns="$(printf '%s' "${hr}" | jq -r '.spec.targetNamespace // .metadata.namespace')"
if ! grep -Eq '\.spec\.targetNamespace[[:space:]]*//[[:space:]]*\.metadata\.namespace' "$SCRIPT"; then
  echo "FAIL: reconcile_one no longer resolves the ExternalSecret namespace via" >&2
  echo "      '.spec.targetNamespace // .metadata.namespace'." >&2
  echo "      The per-app OIDC ExternalSecret MUST target the app's runtime" >&2
  echo "      namespace (HR targetNamespace), else it lands in flux-system and" >&2
  echo "      the app Pod's envFromSecret can't see it (#3374 grafana 503)." >&2
  exit 1
fi

# Negative guard: there must be NO bare `.metadata.namespace`-only resolution
# feeding the ES namespace (that was the pre-#3374 bug).
if grep -Eq "ns=\"\\\$\(printf '%s' \"\\\$\{hr\}\" \| jq -r '\.metadata\.namespace'\)\"" "$SCRIPT"; then
  echo "FAIL: reconcile_one resolves ns from .metadata.namespace ALONE — that is" >&2
  echo "      the pre-#3374 regression (ES lands in flux-system, not the app ns)." >&2
  exit 1
fi

# ── Case 2: emit_external_secret stamps the resolved namespace ────────────
# The emitter signature is `emit_external_secret <cid> <hrns>` and the body
# prints `namespace: ${hrns}`. Guard that it still uses the passed value and
# does not hardcode a namespace.
if ! grep -q '"  namespace: ${hrns}"' "$SCRIPT"; then
  echo "FAIL: emit_external_secret no longer stamps 'namespace: \${hrns}' for the" >&2
  echo "      ExternalSecret — the resolved target namespace must flow through." >&2
  exit 1
fi
if grep -Eq '"  namespace: flux-system"' "$SCRIPT"; then
  echo "FAIL: emit_external_secret hardcodes 'namespace: flux-system' for the ES." >&2
  exit 1
fi

# ── Case 3: bash -n ──────────────────────────────────────────────────────
if ! bash -n "$SCRIPT"; then
  echo "FAIL: rendered reconcile.sh is not bash -n clean" >&2
  exit 1
fi

echo "PASS: bp-sso-bridge emits the per-app OIDC ExternalSecret into the HR" \
     "targetNamespace (app runtime ns), fixing the #3374 grafana 503."
