#!/usr/bin/env bash
# Stalwart expression-syntax audit.
#
# Per stalwart_expression_syntax.md memory (incident 2026-04-14): in
# Stalwart's expression language, equality matchers MUST use `==` not
# `=`. A single `=` is assignment and silently never matches — the rule
# just doesn't fire, no error.
#
# This script renders the chart's bootstrap config.toml with default
# values and audits every line that introduces a Stalwart expression
# string for the `==` form. It does NOT flag TOML-level `key = value`
# pairs (which legitimately use a single `=`).

set -euo pipefail

chart_dir="${1:-$(dirname "$0")/..}"

cd "$(dirname "$0")/.."
chart_dir="$(pwd)"

render="$(mktemp)"
trap "rm -f '${render}'" EXIT

# Render with default values; the bootstrap configmap is rendered even
# when domain.primary is empty (smoke-render-safe).
helm template smoke-bp-stalwart-tenant "${chart_dir}" \
    --namespace smoke-bp-stalwart-tenant \
    > "${render}"

# Extract every line inside an expression-string context: lines whose
# value side starts with a quoted string and contains a Stalwart
# expression operator (rcpt_domain, is_local_domain, authenticated_as,
# sender, is_anonymous, key_exists, sender_match).
expr_lines=$(grep -nE '"(rcpt_domain|is_local_domain|authenticated_as|sender_match|is_anonymous|key_exists)' "${render}" || true)

if [ -z "${expr_lines}" ]; then
  echo "[stalwart-tenant/expression-syntax] No expression strings rendered (default values)."
  echo "[stalwart-tenant/expression-syntax] PASS — nothing to audit."
  exit 0
fi

# Any expression that uses `domain = '...'` (single `=` between the
# operand and the literal) is the bug. Look for ` = ` between an
# identifier-character and a single quote, INSIDE the expression
# string.
fail=0
while IFS= read -r line; do
  expr=$(printf '%s' "${line}" | sed -nE 's/.*"([^"]+)".*/\1/p')
  # If the expression contains an equality test, it MUST use `==`.
  if printf '%s' "${expr}" | grep -qE "[a-zA-Z_]+ = '"; then
    echo "::error title=Stalwart expression '=' bug::Line in rendered config uses single '=' instead of '==':"
    echo "  ${line}"
    echo "  expression: ${expr}"
    echo "  See ~/.claude/projects/-home-openova-repos-openova-private/memory/stalwart_expression_syntax.md"
    fail=1
  fi
done <<< "${expr_lines}"

if [ "${fail}" -eq 1 ]; then
  exit 1
fi

echo "[stalwart-tenant/expression-syntax] All Stalwart expression strings audited — every equality uses '=='."
echo "[stalwart-tenant/expression-syntax] PASS"
