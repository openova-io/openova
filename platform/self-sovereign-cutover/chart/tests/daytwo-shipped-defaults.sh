#!/usr/bin/env bash
# daytwo-shipped-defaults — the Day-2 reconciler must be ARMED as shipped.
#
# WHY THIS FILE EXISTS (#5640). Every other daytwo test renders with an explicit
# `--set daytwoReconciler...enabled=true`, so none of them ever exercises the
# value a real Sovereign actually gets. Flipping the shipped default back to
# false restores the exact gap #5640 describes — a knob nobody turns on, and a
# post-cutover Sovereign that silently cannot receive newly published images —
# while daytwo-source-host-lint.sh and daytwo-catalog-leg.sh both stay GREEN,
# because they overrode the very value that regressed.
#
# Measured before this file: setting `daytwoReconciler.enabled: false` in
# values.yaml left the whole existing daytwo suite passing.
#
# So this renders with NO --set at all. The only input is values.yaml.
set -euo pipefail

CHART_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FQDN="t99.omani.works"
fails=0

pass() { printf '  ok   — %s\n' "$1"; }
fail() { printf '  FAIL — %s\n' "$1"; fails=$((fails + 1)); }

echo "[daytwo-shipped-defaults] rendering with values.yaml ONLY (no --set overrides)"

# Deliberately only --set the sovereign identity the chart cannot infer. Any
# --set on daytwoReconciler.* here would defeat the entire point of this file.
if ! helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" \
  --show-only templates/12-daytwo-harbor-pin-reconciler.yaml \
  >"${TMP}/render.yaml" 2>"${TMP}/render.err"; then
  # helm exits non-zero with "could not find template" when --show-only names a
  # template that rendered to nothing. For THIS file that is not a tooling
  # error, it is the headline failure: the reconciler shipped disabled.
  if grep -q 'could not find template' "${TMP}/render.err"; then
    fail "daytwoReconciler renders to NOTHING by default — #5640 ships a knob nobody turns on, so a post-cutover Sovereign silently cannot receive newly published images"
    echo "[daytwo-shipped-defaults] 1 failure(s) — the Day-2 reconciler does not ship armed"
    exit 1
  fi
  echo "helm template failed for a reason unrelated to the default:"
  cat "${TMP}/render.err"
  exit 1
fi

# 1. The master switch. `enabled: false` renders the template to nothing, so a
#    non-empty render IS the assertion that it ships armed.
if [ -s "${TMP}/render.yaml" ] && grep -q 'kind:' "${TMP}/render.yaml"; then
  pass "daytwoReconciler ships ENABLED (template renders a real object by default)"
else
  fail "daytwoReconciler renders to nothing by default — #5640 ships a knob nobody turns on"
fi

# 2. The source-host lint must ship armed too. Asserted on the VALUE, not the
#    key: an env var present with value "false" is exactly the regression, and
#    a bare `grep -q SOURCE_HOST_LINT_ENABLED` would pass on it.
lint_val="$(awk '/name: SOURCE_HOST_LINT_ENABLED/{getline; print}' "${TMP}/render.yaml" \
  | sed -e 's/.*value:[[:space:]]*//' -e 's/"//g' -e 's/[[:space:]]*$//')"
if [ "${lint_val}" = "true" ]; then
  pass "SOURCE_HOST_LINT_ENABLED ships \"true\" (value asserted, not key presence)"
else
  fail "SOURCE_HOST_LINT_ENABLED ships \"${lint_val:-<absent>}\", want \"true\" — the post-cutover drift lint is off by default"
fi

# 3. The lint allow-list must ship EMPTY. A non-empty default would silently
#    permit whatever host it names — the fail-open shape, dressed as config.
allow_val="$(awk '/name: SOURCE_HOST_LINT_ALLOW/{getline; print}' "${TMP}/render.yaml" \
  | sed -e 's/.*value:[[:space:]]*//' -e 's/"//g' -e 's/[[:space:]]*$//')"
if [ -z "${allow_val}" ]; then
  pass "SOURCE_HOST_LINT_ALLOW ships empty (no host pre-permitted)"
else
  fail "SOURCE_HOST_LINT_ALLOW ships \"${allow_val}\" — that host is exempt from the drift lint by default"
fi

# 4. VACUITY CHECK. If the render were empty or the awk lookups silently matched
#    nothing, assertions 2 and 3 could both "pass" on emptiness. Prove the
#    render actually contains the reconciler we think it does.
if grep -q 'SOURCE_HOST_LINT_ENABLED' "${TMP}/render.yaml"; then
  pass "vacuity: the rendered manifest really carries the lint wiring"
else
  fail "vacuity: rendered manifest has no SOURCE_HOST_LINT_ENABLED at all — the checks above proved nothing"
fi

if [ "${fails}" -ne 0 ]; then
  echo "[daytwo-shipped-defaults] ${fails} failure(s) — the Day-2 reconciler does not ship armed"
  exit 1
fi
echo "[daytwo-shipped-defaults] all assertions passed — reconciler + drift lint ship ARMED, allow-list empty"
