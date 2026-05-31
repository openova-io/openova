#!/usr/bin/env bash
# check-tofu-flux-envsubst.sh — CI guard against the cross-language
# interpolation trap that broke PR #2675 + my G99 line 506.
#
# THE TRAP:
# .tftpl files are rendered by tofu's `templatefile()` FIRST, then their
# OUTPUT is rendered later by Flux's `envsubst`. Both engines use `${...}`
# syntax — but they DON'T agree on the legal characters inside the braces.
#
# Tofu: bare names + ternaries (e.g. `${var.name}`, `${x == "y" ? "a" : "b"}`).
# Flux: name with optional `:operator` (e.g. `${MY_VAR:=default}`,
#       `${MY_VAR:-fallback}`, `${MY_VAR:?required}`, `${MY_VAR:+set}`,
#       `${MY_VAR:default}` — bare colon also legal in POSIX shells).
#
# When an author writes a Flux-shape `${MY_VAR:...}` inside a .tftpl
# file WITHOUT escaping, tofu's parser chokes at the colon:
#
#   ./cloudinit-control-plane.tftpl:1189,46-47: Extra characters after
#   interpolation expression; Template interpolation doesn't expect a colon
#   at this location.
#
# CANONICAL FIX:
# Escape the `$` so tofu passes the literal `${VAR:...}` through to the
# rendered output, where Flux later substitutes it:
#
#   ${MY_VAR:=default}    →   $${MY_VAR:=default}
#
# This script greps every `.tftpl` for unescaped `${SCREAMING_SNAKE:...}`
# patterns (Flux envsubst convention — uppercase env-var names + colon)
# and fails CI with the offending file:line.
#
# Per docs/INVIOLABLE-PRINCIPLES.md #15 (real IaC validators): canonical
# tofu validate catches some of these but the existing path-filtered
# infra-{hetzner,huawei}-tofu.yaml guards only matched `:-` operator.
# This script catches ALL colon shapes (bare, :=, :-, :?, :+) and runs
# against EVERY .tftpl regardless of provider routing.

set -euo pipefail

ROOT="${1:-.}"
EXIT=0

# Find every .tftpl file under infra/. Skip .terraform/ caches.
mapfile -t FILES < <(find "$ROOT/infra" -name '*.tftpl' -not -path '*/.terraform/*' 2>/dev/null || true)

if [ ${#FILES[@]} -eq 0 ]; then
  echo "check-tofu-flux-envsubst: no .tftpl files found under $ROOT/infra/ — nothing to check."
  exit 0
fi

# Pattern: ${IDENTIFIER:...} where:
#   - IDENTIFIER is SCREAMING_SNAKE_CASE (Flux envsubst convention)
#   - colon follows (any shell-operator: bare, =, -, ?, +)
#   - NOT preceded by `$` (the escaped form `$${...}` is correct)
#
# Negative lookbehind `(?<!\$)` ensures we don't match the already-escaped
# `$${...}` form. Identifier `[A-Z_][A-Z_0-9]*` distinguishes from tofu
# variable refs which are conventionally lowercase (`${var.region}`,
# `${cluster_cidr}`). Tofu ternaries use mixed-case + dots + spaces, also
# excluded by this pattern.
PATTERN='(?<!\$)\$\{[A-Z_][A-Z_0-9]*:'

for f in "${FILES[@]}"; do
  rel="${f#$ROOT/}"
  if grep -nP "$PATTERN" "$f" >/dev/null 2>&1; then
    echo "ERROR: $rel contains unescaped \${VAR:...} (Flux envsubst syntax) inside a tofu .tftpl file"
    echo "       Tofu's templatefile() parser doesn't allow the colon. Escape with \$\${VAR:...}."
    echo "       Offending lines:"
    grep -nP "$PATTERN" "$f" | sed 's/^/         /'
    EXIT=1
  fi
done

if [ $EXIT -ne 0 ]; then
  echo ""
  echo "FAIL: tofu+Flux envsubst syntax violation(s) detected."
  echo "FIX:  prefix the \$ with another \$ so tofu passes the literal through to Flux."
  echo "      Before: \${MY_VAR:=default}    After: \$\${MY_VAR:=default}"
  exit 1
fi

echo "OK: all .tftpl files use correctly-escaped Flux envsubst syntax (\$\${VAR:...})."
