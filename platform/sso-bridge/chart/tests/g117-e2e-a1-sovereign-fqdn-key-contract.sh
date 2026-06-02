#!/usr/bin/env bash
# bp-sso-bridge — G117.E2E-C1 #2816 sovereign-fqdn key contract guard.
#
# Asserts two contracts that, when broken, silently disable the
# bp-sso-bridge reconciler:
#
#   (1) The rendered Deployment env `SOVEREIGN_FQDN_SOVEREIGN_FQDN_KEY`
#       defaults to "fqdn" — matching the canonical key that
#       bp-catalyst-platform emits at
#       products/catalyst/chart/templates/sovereign-fqdn-configmap.yaml:48
#       (`fqdn: {{ .Values.global.sovereignFQDN | quote }}`).
#
#   (2) That env value can be overridden via --set
#       operator.sovereignFqdnConfigMap.sovereignFqdnKey=... (defense-
#       in-depth for operators that pin a per-Sovereign overlay).
#
#   (3) The reconcile.sh script still reads the live ConfigMap via the
#       `${SOVEREIGN_FQDN_SOVEREIGN_FQDN_KEY}` indirection — otherwise
#       (1) and (2) become dead code.
#
#   (4) The chart default for sovereignFqdnKey in values.yaml matches the
#       canonical key emitted by the catalyst-platform sovereign-fqdn
#       ConfigMap template — caught by comparing both side-by-side from
#       the source files.
#
# Why this guard exists:
#   G91.1 (PR #2683) shipped a default `sovereignFqdnKey: sovereignFqdn`
#   that does NOT exist in any sovereign-fqdn ConfigMap on any live
#   Sovereign. The reconcile loop's `sovereign_fqdn()` helper used
#   jsonpath `{.data.${SOVEREIGN_FQDN_SOVEREIGN_FQDN_KEY}}` and silently
#   returned empty on every tick, so the loop logged
#   `sovereign-fqdn ConfigMap missing; skipping tick` every 6 minutes
#   and skipped the Tier-1 OpenBao + per-Org-realm reconciler calls
#   entirely. Caught on hw86 2026-06-03 (G117.E2E-A6 4-hop SSO probe)
#   where grafana + gitea token-exchange returned `unauthorized_client`
#   because chart-side OIDC secrets (md5-derived 32-hex) never
#   converged with KC realm-imported secrets (sha256-derived 64-hex).
#
# Usage: bash tests/g117-e2e-a1-sovereign-fqdn-key-contract.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
REPO_ROOT="$(cd "$CHART_DIR/../../.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

helm="${HELM_BIN:-helm}"

CATALYST_CM="$REPO_ROOT/products/catalyst/chart/templates/sovereign-fqdn-configmap.yaml"

# ── Case 1: chart default renders SOVEREIGN_FQDN_SOVEREIGN_FQDN_KEY=fqdn ──
"$helm" template smoke "$CHART_DIR" 2>/dev/null > "$TMP/default.yaml"

# Extract the env block containing the key, then verify the next line's
# value is "fqdn". The shape is:
#     - name: SOVEREIGN_FQDN_SOVEREIGN_FQDN_KEY
#       value: "fqdn"
grep_ctx() { grep -A1 "name: SOVEREIGN_FQDN_SOVEREIGN_FQDN_KEY" "$1" | tail -n1; }

default_line="$(grep_ctx "$TMP/default.yaml" | tr -d '[:space:]')"
if [ "$default_line" != 'value:"fqdn"' ]; then
  echo "FAIL: default SOVEREIGN_FQDN_SOVEREIGN_FQDN_KEY env != \"fqdn\"" >&2
  echo "      got: $default_line" >&2
  echo "      (canonical key on every Sovereign is sovereign-fqdn data.fqdn —" >&2
  echo "       see products/catalyst/chart/templates/sovereign-fqdn-configmap.yaml:48)" >&2
  exit 1
fi

# ── Case 2: explicit override is honoured ────────────────────────────────
"$helm" template smoke "$CHART_DIR" \
  --set operator.sovereignFqdnConfigMap.sovereignFqdnKey=customKeyName \
  2>/dev/null > "$TMP/override.yaml"

override_line="$(grep_ctx "$TMP/override.yaml" | tr -d '[:space:]')"
if [ "$override_line" != 'value:"customKeyName"' ]; then
  echo "FAIL: --set override of sovereignFqdnKey not honoured" >&2
  echo "      got: $override_line" >&2
  exit 1
fi

# ── Case 3: the reconcile.sh script still reads the env var ──────────────
# Guard against accidental drop of the indirection — the bash function
# `sovereign_fqdn()` must dereference $SOVEREIGN_FQDN_SOVEREIGN_FQDN_KEY
# via jsonpath, otherwise overrides + the default become dead code.
if ! grep -q 'jsonpath="{.data.${SOVEREIGN_FQDN_SOVEREIGN_FQDN_KEY}}"' "$TMP/default.yaml"; then
  echo "FAIL: reconcile.sh no longer reads sovereign-fqdn via" >&2
  echo "      \${SOVEREIGN_FQDN_SOVEREIGN_FQDN_KEY} indirection" >&2
  exit 1
fi

# ── Case 4: chart-default key matches catalyst-platform emitter key ──────
# Compare what we ship as the default vs what the catalyst-platform
# chart actually writes into the live ConfigMap. Drift on EITHER side
# (rename the data key in catalyst, OR forget to retune the bridge
# default) breaks SSO silently on every Sovereign.
if [ ! -f "$CATALYST_CM" ]; then
  echo "SKIP: catalyst-platform sovereign-fqdn ConfigMap template not at" >&2
  echo "      $CATALYST_CM (repo layout changed?)" >&2
  echo "      Case 4 cross-chart guard cannot run." >&2
else
  sso_key="$(awk '/sovereignFqdnKey: [a-zA-Z]/{print $2; exit}' "$CHART_DIR/values.yaml")"
  catalyst_key="$(grep -E '^  [a-zA-Z]+: \{\{ \.Values\.global\.sovereignFQDN' "$CATALYST_CM" \
    | awk -F: '{print $1}' | tr -d ' ')"

  if [ -z "$sso_key" ] || [ -z "$catalyst_key" ]; then
    echo "FAIL: could not extract one of the keys" >&2
    echo "      sso_key='$sso_key' catalyst_key='$catalyst_key'" >&2
    exit 1
  fi

  if [ "$sso_key" != "$catalyst_key" ]; then
    echo "FAIL: cross-chart key drift detected." >&2
    echo "  bp-sso-bridge      values.yaml sovereignFqdnKey:   '$sso_key'" >&2
    echo "  bp-catalyst-platform sovereign-fqdn ConfigMap key: '$catalyst_key'" >&2
    echo "  These MUST match. Fix whichever side drifted." >&2
    exit 1
  fi
fi

echo "PASS: sovereign-fqdn key contract"
echo "      default='fqdn', override honoured, indirection preserved, cross-chart match"
