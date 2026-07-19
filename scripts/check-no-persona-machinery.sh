#!/usr/bin/env bash
# check-no-persona-machinery.sh — CI guard that keeps the persona word
# `sme` (and the GLOSSARY-banned `tenant`, docs/GLOSSARY.md:18) out of
# Catalyst MACHINERY. Companion to scripts/check-no-banned-words.sh.
#
# #3383 eradicated the `sme`/`tenant`-named Organization subsystem into the
# canonical `org-services` / `/api/v1/organizations` machinery. This guard
# prevents the persona name from re-entering machinery: NEW route paths,
# namespace declarations, chart template dirs, and exported Go identifiers
# matching `sme`/`SMETenant` fail the build.
#
# A persona is a field VALUE (`tier: "sme"` on legacy records), NOT
# architecture — those are EXEMPT. Ecosystem/vendored upstream values are
# EXEMPT. The one-release deprecation aliases (annotated `naming-guard:
# alias`) are EXEMPT.
#
# Usage:
#   scripts/check-no-persona-machinery.sh            # scan working tree
#   scripts/check-no-persona-machinery.sh --self-test  # prove BOTH directions
#
# The guard proves BOTH directions (DoD §9.6 / #3383):
#   - a NEW `rg.Post("/api/v1/sme/widgets", ...)` route FAILS (RED)
#   - a legitimate `Tier: "sme"` data value PASSES (the data exemption)

set -euo pipefail

ROOT="${ROOT:-.}"

# Machinery patterns the guard rejects (PCRE). Each is anchored to a
# MACHINERY context — a route literal, a YAML namespace decl, a chart
# template dir path, or an exported Go identifier. Bare `sme`/`tenant`
# words inside comments/values are intentionally NOT matched here; only
# the load-bearing machinery shapes are.
# Patterns checked in ALL scanned files (route literals, YAML namespace
# decls, chart template dir paths).
declare -a MACHINERY=(
  # NEW API route path literals that re-introduce the persona prefix.
  '"/api/v1/sme[/"]'
  '"/api/tenant/'
  # Kubernetes namespace declaration pinned to the persona name.
  'namespace:\s*sme\s*$'
  # Chart template directory named after the persona.
  'templates/sme-services'
)

# Patterns checked ONLY in Go files — exported Go identifiers naming
# machinery after the persona (§7). The UI's TypeScript mirror types under
# pages/sme/* are owned by #3378, not this guard, so the identifier rule is
# Go-scoped on purpose.
declare -a MACHINERY_GO=(
  '\bfunc\s.*SMETenant'
  '\btype\s+SMETenant'
  '\bfunc\s.*Handle.*SMETenant'
)

# Paths where the persona word is documented BY DEFINITION (this guard,
# the docs that ENUMERATE the rename, the cron ledgers that RECORD it),
# OR is a legacy deployment overlay the rename does not own, OR is a
# vendored/upstream value. Substring match, one pattern per line.
declare -a EXEMPT_PATHS=(
  'docs/'
  'scripts/check-no-persona-machinery.sh'
  'scripts/check-no-banned-words.sh'
  'docs/ledger/'
  # Legacy hand-rolled deployment overlays for Catalyst-Zero (contabo):
  # these predate the chart and are managed out-of-band; the rename ships
  # as a chart change. The per-cluster bootstrap-kit pins are deployment
  # config bumped at deploy time.
  'clusters/contabo-mkt/'
  '/vendor/'
  'node_modules/'
)

# Per-LINE exemptions: a line carrying this annotation is a deliberate
# one-release compatibility alias and is allowed to name the legacy path.
ALIAS_ANNOTATION='naming-guard: alias'

# A matched line is NOT machinery when it is PROSE — a comment (shell/YAML
# `#`, Go/JS `//`, or inside a Helm `{{- /* ... */}}` / C-style `/* ... */`
# block) that merely REFERENCES the legacy name (e.g. a file-lineage note
# "the byte-for-byte twin of templates/sme-services/…"), or a Go TEST
# function whose name describes the legacy funnel scenario
# (`func TestCreateSMETenant_…`). Machinery is load-bearing code/manifests,
# never a comment or a test name; flagging those is a false positive. The
# RED self-test still fails on a REAL resource path / route / exported
# identifier because those are not comments and not `func Test…`.
line_is_prose() {
  # $1 = matched line content; $2 = 1 if the line sits inside an open
  # block comment (tracked by the caller), else 0.
  local line="$1" in_block="${2:-0}"
  [[ "$in_block" == "1" ]] && return 0
  local trimmed="${line#"${line%%[![:space:]]*}"}"   # left-trim
  case "$trimmed" in
    '#'*) return 0 ;;          # shell / YAML line comment
    '//'*) return 0 ;;         # Go / JS line comment
    '*'*) return 0 ;;          # continuation line of a /* */ block
    '{{- /*'*|'{{/*'*) return 0 ;;  # Helm comment-block opener
  esac
  # A Go test function naming the legacy scenario is descriptive, not
  # machinery (the renamed handlers/types carry the canonical names; the
  # test that exercises the SME funnel keeps its scenario name).
  [[ "$trimmed" == func\ Test* ]] && return 0
  return 1
}

# Data-value exemptions (the legitimate `sme`): a line matching one of
# these is the `Tier` commercial-class VALUE (e.g. on legacy persisted
# records), never machinery. Checked per-line so a `Tier: "sme"` passes
# even in a non-exempt file.
# #3985 tightening: `TenantKindSME` and the `"sme","corporate"` enum
# pairing were eradicated from the tree (the live Organization CRD tier
# enum is `[org, corporate]`), so those exemptions are removed — a new
# occurrence now fails the guard instead of being grandfathered.
declare -a DATA_VALUE_OK=(
  'tier:\s*"?sme"?'
  'Tier\s*[:=]'
)

is_exempt_path() {
  local path="$1"
  for ex in "${EXEMPT_PATHS[@]}"; do
    [[ "$path" == *"$ex"* ]] && return 0
  done
  return 1
}

line_is_allowed() {
  # $1 = the matched line. Allowed if it is an annotated alias OR a
  # legitimate Tier/registry data value.
  local line="$1"
  [[ "$line" == *"$ALIAS_ANNOTATION"* ]] && return 0
  for ok in "${DATA_VALUE_OK[@]}"; do
    if printf '%s' "$line" | grep -qP "$ok"; then
      return 0
    fi
  done
  return 1
}

scan_tree() {
  local root="$1"
  mapfile -t FILES < <(git -C "$root" ls-files \
    '*.go' '*.ts' '*.tsx' '*.yaml' '*.yml' '*.svelte' '*.astro' 2>/dev/null \
    | grep -v '^node_modules/' || true)
  local violations=0
  for f in "${FILES[@]}"; do
    is_exempt_path "$f" && continue
    # Read the file once so we can inspect the PRECEDING line for the
    # alias annotation (the real aliases annotate the comment above the
    # route block, not the route line itself).
    mapfile -t LINES < "$root/$f"
    # Go _test.go files EXERCISE machinery (they POST to the live deprecated
    # alias route, and name helper/scenario funcs after the SME funnel) but
    # never DEFINE production machinery — that lives in non-test files, which
    # are still fully scanned. So test files get the namespace/template-dir
    # patterns only, not the route-literal or exported-identifier patterns.
    local is_test=0
    [[ "$f" == *_test.go ]] && is_test=1
    # Go files also get the exported-identifier machinery patterns.
    local patterns
    if [[ "$is_test" == "1" ]]; then
      # Drop the two route-literal patterns; keep namespace + template-dir.
      patterns=('namespace:\s*sme\s*$' 'templates/sme-services')
    else
      patterns=("${MACHINERY[@]}")
      if [[ "$f" == *.go ]]; then
        patterns+=("${MACHINERY_GO[@]}")
      fi
    fi
    for pat in "${patterns[@]}"; do
      while IFS= read -r match; do
        [ -z "$match" ] && continue
        local lineno="${match%%:*}"
        local content="${match#*:}"
        # Allowed if the matched line itself is a Tier value / annotated
        # alias, OR a nearby PRECEDING line carries the alias annotation.
        # The real aliases annotate the comment that heads a contiguous
        # block of alias registrations, so walk back up to a small window
        # (stopping at a blank line) looking for the annotation.
        if line_is_allowed "$content"; then
          continue
        fi
        # Allowed if the matched line is PROSE (a comment or a test-function
        # name) rather than machinery. Determine block-comment membership by
        # counting unmatched `/*` openers above this line (covers Helm
        # `{{- /* … */}}` and C-style `/* … */` blocks).
        local in_block=0 j opens=0 closes=0
        for ((j=0; j<lineno-1; j++)); do
          local l="${LINES[$j]:-}"
          [[ "$l" == *'/*'* ]] && opens=$((opens+1))
          [[ "$l" == *'*/'* ]] && closes=$((closes+1))
        done
        [ "$opens" -gt "$closes" ] && in_block=1
        if line_is_prose "$content" "$in_block"; then
          continue
        fi
        local exempt_by_block=0
        local k
        for k in 2 3 4 5 6 7; do
          local idx=$((lineno - k))
          [ "$idx" -lt 0 ] && break
          local back="${LINES[$idx]:-}"
          # Stop the look-back at a blank line — annotations don't carry
          # across an empty line (separate block).
          if [[ -z "${back// }" ]]; then
            break
          fi
          if [[ "$back" == *"$ALIAS_ANNOTATION"* ]]; then
            exempt_by_block=1
            break
          fi
        done
        if [ "$exempt_by_block" -eq 1 ]; then
          continue
        fi
        echo "VIOLATION: $f:$lineno"
        echo "         $content" | sed 's/^[[:space:]]*/         /'
        violations=$((violations + 1))
      done < <(grep -nP "$pat" "$root/$f" 2>/dev/null || true)
    done
  done
  return "$violations"
}

self_test() {
  # Prove BOTH directions on synthetic fixtures in a throwaway git repo.
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  git -C "$tmp" init -q
  mkdir -p "$tmp/products/x"

  # FIXTURE A (must FAIL): a NEW persona-named machinery route.
  cat > "$tmp/products/x/bad_route.go" <<'EOF'
package x
func reg(rg Router) {
	rg.Post("/api/v1/sme/widgets", handleWidgets)
}
EOF
  # FIXTURE B (must PASS): a legitimate Tier data value + an annotated alias.
  cat > "$tmp/products/x/good_data.go" <<'EOF'
package x
type Org struct {
	Tier string // "sme" | "corporate" — a commercial class is DATA
}
func reg(rg Router) {
	// naming-guard: alias — legacy path, one-release deprecation.
	rg.Post("/api/v1/sme/tenants", deprecatedAlias("/api/v1/organizations", h.Create))
}
EOF
  git -C "$tmp" add -A >/dev/null 2>&1

  local fail_ok=0 pass_ok=0

  # Direction 1: the BAD file alone must trip the guard.
  git -C "$tmp" rm -q --cached products/x/good_data.go >/dev/null 2>&1
  if ROOT="$tmp" scan_tree "$tmp" >/dev/null 2>&1; then
    echo "SELF-TEST FAIL: a new /api/v1/sme/ route did NOT trip the guard"
  else
    echo "SELF-TEST OK: new /api/v1/sme/ machinery route is REJECTED (RED)"
    fail_ok=1
  fi

  # Direction 2: the GOOD file alone must PASS (Tier value + alias line).
  git -C "$tmp" reset -q >/dev/null 2>&1
  git -C "$tmp" add products/x/good_data.go >/dev/null 2>&1
  git -C "$tmp" rm -q --cached products/x/bad_route.go >/dev/null 2>&1
  if ROOT="$tmp" scan_tree "$tmp" >/dev/null 2>&1; then
    echo "SELF-TEST OK: Tier \"sme\" data value + annotated alias PASS (data exemption)"
    pass_ok=1
  else
    echo "SELF-TEST FAIL: a legitimate Tier \"sme\" value / alias was rejected"
  fi

  if [ "$fail_ok" -eq 1 ] && [ "$pass_ok" -eq 1 ]; then
    echo "SELF-TEST: BOTH directions proven."
    return 0
  fi
  return 1
}

MODE="scan"
case "${1:-}" in
  --self-test) MODE="self-test" ;;
  --root) ROOT="${2:?--root needs a path}" ;;
  "" ) ;;
  * ) echo "Usage: $0 [--self-test] [--root <path>]"; exit 2 ;;
esac

if [ "$MODE" = "self-test" ]; then
  if self_test; then
    exit 0
  fi
  exit 1
fi

if scan_tree "$ROOT"; then
  echo "OK: no persona-named machinery introduced."
  exit 0
else
  echo ""
  echo "FAIL: persona-named machinery detected (docs/GLOSSARY.md:18 — 'tenant'"
  echo "→ Organization; the persona 'sme' is a Tier VALUE, never machinery)."
  echo ""
  echo "Machinery (namespace, route path, chart template dir, exported Go"
  echo "identifier) must be Organization-named. If the line above is a"
  echo "deliberate one-release compatibility alias, annotate it with"
  echo "'naming-guard: alias'. A legitimate legacy Tier data value"
  echo "(tier: \"sme\") is already exempt."
  exit 1
fi
