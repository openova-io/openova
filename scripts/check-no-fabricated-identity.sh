#!/usr/bin/env bash
# check-no-fabricated-identity.sh — ADR-0015 D4 "references, never
# fabrication" guard for the catalyst-api handlers and the sovereign-admin
# console.
#
# An Organization, Environment or Region in any request/CR must NAME an
# existing object. Three source shapes manufactured one instead, and each
# produced an Application that pointed at nothing (hw307, 2026-11-08:
# Application sovereign/test → organizationRef "sovereign", environmentRef
# "sovereign-prod", Pending EnvironmentMissing, plus a stray `sovereign`
# namespace the API created on demand):
#
#   1. "sovereign" used as an Organization VALUE (assigned to / compared
#      with an org/slug/parent identifier, or as a `||` / `??` fallback).
#   2. an Environment name derived from an Org by appending "-prod"
#      (`+ "-prod"`, `"%s-prod"`, `${x}-prod`).
#   3. region-name string surgery: strings.TrimPrefix/TrimSuffix/Split/
#      Replace (Go) or .split/.replace/.slice/.substring (TS) applied to a
#      region value. Canonical region ids are carried verbatim
#      (internal/regions is the one registry).
#
# Scope: products/catalyst/bootstrap/api/internal/handler/*.go and
# products/catalyst/bootstrap/ui/src/**/*.{ts,tsx}; tests and comment lines
# are excluded. Exceptions live in ALLOW below — each names its file, the
# exact line text it permits, and why it is not an identity fabrication.
#
# Usage:
#   scripts/check-no-fabricated-identity.sh              # scan the tree
#   scripts/check-no-fabricated-identity.sh --self-test  # prove both directions

set -euo pipefail

ROOT="${ROOT:-.}"
API_DIR="products/catalyst/bootstrap/api/internal/handler"
UI_DIR="products/catalyst/bootstrap/ui/src"

# rule-id|PCRE — Go rules apply to *.go, TS rules to *.ts/*.tsx.
GO_RULES=(
  'sovereign-org|(?i:\b(org|orgs|orgref|organization|organizationref|parentorg|orgslug|slug|orgname)\w*)\s*(:=|=|==|!=)\s*"sovereign"'
  'sovereign-org|"sovereign"\s*(==|!=)\s*(?i:\w*(org|slug)\w*)\b'
  'env-prod-derivation|\+\s*"-prod"'
  'env-prod-derivation|"%s-prod"'
  'region-surgery|strings\.(TrimPrefix|TrimSuffix|Split|SplitN|Replace|ReplaceAll|Fields)\([^)]*(?i:region)'
  'region-surgery|(?i:region)\w*\s*(:=|=)\s*strings\.(TrimPrefix|TrimSuffix|Split|SplitN|Replace|ReplaceAll)\('
)
TS_RULES=(
  "sovereign-org|(?i:\\b(org|orgs|orgref|organization|organizationref|parentorg|orgslug|slug|orgname)\\w*)\\s*(:|=|===|==|!==|!=)\\s*['\"\`]sovereign['\"\`]"
  "sovereign-org|(\\|\\||\\?\\?)\\s*['\"\`]sovereign['\"\`]"
  "env-prod-derivation|\\+\\s*['\"\`]-prod['\"\`]"
  "env-prod-derivation|\\\$\\{[^}]+\\}-prod"
  "env-prod-derivation|\\{[A-Za-z_.?]+\\}-prod\\b"
  "region-surgery|(?i:region)\\w*\\??\\.(split|replace|replaceAll|slice|substring)\\("
)

# path|exact-substring-of-the-line|reason
ALLOW=(
  "${API_DIR}/k8s_resource_put_apply.go|org = \"sovereign\"|Gitea ORG name for the cluster-config repo (CATALYST_GITEA_SOVEREIGN_ORG default), not a Catalyst Organization reference"
  "${API_DIR}/cutover_secondary_kubeconfigs.go|strings.TrimSuffix(strings.TrimSuffix(key, \".yaml\"), \".yml\")|strips a kubeconfig FILE extension, the region key itself is untouched"
  "${API_DIR}/phase1_watch.go|strings.TrimSuffix(strings.TrimPrefix(n, prefix), \".yaml\")|parses a kubeconfig FILE name <depID>-<region>.yaml; registry migration is the ADR-0015 D3 slice"
)

is_allowed() { # file line
  local f="$1" l="$2" entry p s
  for entry in "${ALLOW[@]}"; do
    p="${entry%%|*}"; s="${entry#*|}"; s="${s%%|*}"
    if [[ "$f" == *"$p" ]] && [[ "$l" == *"$s"* ]]; then return 0; fi
  done
  return 1
}

# scan_files <rules-array-name> <file...> — prints violations, returns count via stdout tail
scan() {
  local -n rules="$1"; shift
  local f n=0 rule id re hits line num text
  for f in "$@"; do
    for rule in "${rules[@]}"; do
      id="${rule%%|*}"; re="${rule#*|}"
      hits="$(grep -nP -- "$re" "$f" 2>/dev/null || true)"
      [[ -z "$hits" ]] && continue
      while IFS= read -r line; do
        num="${line%%:*}"; text="${line#*:}"
        # comment lines are prose, not code
        if [[ "$text" =~ ^[[:space:]]*(//|\*|/\*) ]]; then continue; fi
        if is_allowed "$f" "$text"; then continue; fi
        echo "VIOLATION [$id] $f:$num: $(echo "$text" | sed 's/^[[:space:]]*//')"
        n=$((n + 1))
      done <<< "$hits"
    done
  done
  return $(( n > 0 ? 1 : 0 ))
}

list_go() { find "$1" -name '*.go' ! -name '*_test.go' -type f 2>/dev/null | sort; }
list_ts() {
  find "$1" \( -name '*.ts' -o -name '*.tsx' \) -type f \
    ! -name '*.test.ts' ! -name '*.test.tsx' ! -name '*.spec.ts' ! -name '*.spec.tsx' \
    ! -path '*/__tests__/*' ! -path '*/test/*' 2>/dev/null | sort
}

run_scan() { # root
  local root="$1" rc=0
  mapfile -t gofiles < <(list_go "$root/$API_DIR")
  mapfile -t tsfiles < <(list_ts "$root/$UI_DIR")
  if [[ ${#gofiles[@]} -eq 0 || ${#tsfiles[@]} -eq 0 ]]; then
    echo "check-no-fabricated-identity: scope is empty (go=${#gofiles[@]} ts=${#tsfiles[@]}) under $root — refusing to pass on nothing" >&2
    return 2
  fi
  ( cd "$root" && scan GO_RULES "${gofiles[@]#$root/}" ) || rc=1
  ( cd "$root" && scan TS_RULES "${tsfiles[@]#$root/}" ) || rc=1
  return $rc
}

self_test() {
  local tmp; tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' RETURN
  mkdir -p "$tmp/$API_DIR" "$tmp/$UI_DIR"
  # Clean baseline — must PASS.
  cat > "$tmp/$API_DIR/ok.go" <<'GO'
package handler
// a comment mentioning org = "sovereign" and + "-prod" is prose
func cutoverStateValue(complete bool) string { if complete { return "sovereign" }; return "tethered" }
var realm = "sovereign"
func envRef(req string) string { return req }
GO
  cat > "$tmp/$UI_DIR/ok.tsx" <<'TS'
export const SOVEREIGN_PARENT_SLUG = 'sovereign'
const label = region.label
TS
  if ! run_scan "$tmp" >/dev/null; then
    echo "SELF-TEST FAIL: clean fixture was flagged"; run_scan "$tmp" || true; return 1
  fi
  # Each forbidden shape — must FAIL.
  local -a bad_go=(
    'func f() string { org := ""; if org == "" { org = "sovereign" }; return org }'
    'func f(slug string) bool { return slug == "sovereign" }'
    'func f(o string) string { return o + "-prod" }'
    'func f(o string) string { return fmt.Sprintf("%s-prod", o) }'
    'func f(region string) string { return strings.TrimPrefix(region, "hw-") }'
  )
  local -a bad_ts=(
    "const org = t.subdomain || 'sovereign'"
    "const env = \`\${org}-prod\`"
    "const env = org + '-prod'"
    "const short = region.replace(/^hw-/, '')"
    "const orgSlug = 'sovereign'"
    "const el = <code>{org}-prod</code>"
  )
  local i b
  for i in "${!bad_go[@]}"; do
    b="${bad_go[$i]}"
    printf 'package handler\n%s\n' "$b" > "$tmp/$API_DIR/bad.go"
    if run_scan "$tmp" >/dev/null; then echo "SELF-TEST FAIL: Go shape not caught: $b"; return 1; fi
    rm -f "$tmp/$API_DIR/bad.go"
  done
  for i in "${!bad_ts[@]}"; do
    b="${bad_ts[$i]}"
    printf '%s\n' "$b" > "$tmp/$UI_DIR/bad.ts"
    if run_scan "$tmp" >/dev/null; then echo "SELF-TEST FAIL: TS shape not caught: $b"; return 1; fi
    rm -f "$tmp/$UI_DIR/bad.ts"
  done
  # Tests are out of scope — a forbidden shape in *_test.go must not fail.
  printf 'package handler\nvar e = "acme" + "-prod"\n' > "$tmp/$API_DIR/x_test.go"
  if ! run_scan "$tmp" >/dev/null; then echo "SELF-TEST FAIL: a _test.go file was scanned"; return 1; fi
  # Empty scope must not pass.
  local empty; empty="$(mktemp -d)"
  if run_scan "$empty" >/dev/null 2>&1; then echo "SELF-TEST FAIL: empty scope passed"; rm -rf "$empty"; return 1; fi
  rm -rf "$empty"
  echo "check-no-fabricated-identity self-test: OK (clean fixture passes; ${#bad_go[@]} Go + ${#bad_ts[@]} TS forbidden shapes caught; tests excluded; empty scope refused; no cluster contacted)"
}

if [[ "${1:-}" == "--self-test" ]]; then
  self_test
  exit $?
fi

if run_scan "$ROOT"; then
  echo "check-no-fabricated-identity: OK — no fabricated Organization / Environment / Region identity in $API_DIR or $UI_DIR"
else
  rc=$?
  if [[ $rc -eq 2 ]]; then exit 2; fi
  echo
  echo "check-no-fabricated-identity: FAIL — name an existing object instead (ADR-0015 D4)."
  echo "  Organizations/Environments: read them from the CRs (GET /api/v1/refs/...)."
  echo "  Regions: carry the canonical id from internal/regions verbatim."
  exit 1
fi
