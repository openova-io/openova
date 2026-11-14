#!/usr/bin/env bash
# check-variables-documents.sh — guard G8 of contract C5, the variables document.
#
# Run from the repository root:
#   bash scripts/check-variables-documents.sh               # the real run
#   bash scripts/check-variables-documents.sh --self-test   # proves each rule can fail
#
# Rule A. The Go package core/controllers/pkg/upstream embeds a copy of each
#   published contract file. Each copy equals its file byte for byte:
#     docs/contracts/c5-upstream-variables.schema.json
#     docs/contracts/c5-kit-keys.txt
#   (under core/controllers/pkg/upstream/contract/). Change both together.
# Rule B. Every file named variables.yaml under clusters/ and tests/ (tracked,
#   or new and not ignored) is the v1 ConfigMap flux-system/sovereign-variables
#   and passes the schema and the cross-field rules 1 to 7 of contract C5
#   (upstream.FromMap and upstream.Validate). The Sovereign's own FQDN for
#   rule 7 is the folder after "clusters/" in the file's path.
#
# The judge of rule B is the Go command
# core/controllers/pkg/upstream/cmd/check-variables, built once per run.
# The self-test runs it on tests/fixtures/variables/: valid.yaml must pass,
# and each invalid-rule<N>.yaml must fail with rule <N> and no other rule.
#
# Prints one line per file and a last line "variables documents: <n>".
# Exit 0 pass, 1 a rule failed, 2 a setup error (no judgement).
set -euo pipefail

SELF_TEST=0
case "${1:-}" in
  --self-test) SELF_TEST=1 ;;
  "") ;;
  *) echo "usage: $0 [--self-test]" >&2; exit 2 ;;
esac

ROOT="$(pwd)"
PKG=core/controllers/pkg/upstream
FIXTURES=tests/fixtures/variables
OWN=alpha.example.org
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# copies_equal <published-dir> <copy-dir> — rule A; prints one FAIL line per differing file.
copies_equal() {
  local ok=0 name
  for name in c5-upstream-variables.schema.json c5-kit-keys.txt; do
    if ! cmp -s "$1/$name" "$2/$name"; then
      echo "FAIL rule A: $2/$name differs from $1/$name; copy the published file"
      ok=1
    fi
  done
  return "$ok"
}

if ! (cd core/controllers && go build -o "$TMP/check-variables" ./pkg/upstream/cmd/check-variables); then
  echo "check-variables-documents: the Go command does not build" >&2
  exit 2
fi
CHECK="$TMP/check-variables"

if [ "$SELF_TEST" = 1 ]; then
  failures=0
  pass() { echo "ok   $1"; }
  fail() { echo "FAIL $1"; failures=$((failures + 1)); }
  # expect_fail <case> <text> <command>...: the command must exit non-zero and print <text>.
  expect_fail() {
    local name="$1" text="$2"; shift 2
    if "$@" > "$TMP/out" 2>&1; then fail "$name (it passed)"; return; fi
    if grep -q -F -- "$text" "$TMP/out"; then pass "$name"; else cat "$TMP/out"; fail "$name (it failed without: $text)"; fi
  }

  # Rule A can fail: a copy that differs by one byte.
  mkdir -p "$TMP/pub" "$TMP/copy"
  cp docs/contracts/c5-upstream-variables.schema.json docs/contracts/c5-kit-keys.txt "$TMP/pub/"
  cp "$TMP/pub/"* "$TMP/copy/"
  if copies_equal "$TMP/pub" "$TMP/copy" > /dev/null; then pass "rule A: equal copies pass"; else fail "rule A: equal copies pass"; fi
  printf 'EXTRA_KIT_KEY\n' >> "$TMP/copy/c5-kit-keys.txt"
  if copies_equal "$TMP/pub" "$TMP/copy" > /dev/null; then fail "rule A: a differing copy fails"; else pass "rule A: a differing copy fails"; fi

  # Rule B: the valid fixture passes.
  if "$CHECK" --own-fqdn "$OWN" "$FIXTURES/valid.yaml" > "$TMP/out" 2>&1; then pass "rule B: valid.yaml passes"; else cat "$TMP/out"; fail "rule B: valid.yaml passes"; fi

  # Rule B: each invalid fixture fails with its own rule and no other.
  count=0
  for f in "$FIXTURES"/invalid-rule*.yaml; do
    [ -e "$f" ] || continue
    count=$((count + 1))
    n="$(basename "$f" .yaml)"; n="${n#invalid-rule}"
    if "$CHECK" --own-fqdn "$OWN" "$f" > "$TMP/out" 2>&1; then
      fail "rule B: $f must fail"; continue
    fi
    rules="$(awk '{ while (match($0, /rule [0-9]/)) { print substr($0, RSTART, RLENGTH); $0 = substr($0, RSTART + RLENGTH) } }' "$TMP/out" | sort -u | tr '\n' ',')"
    if [ "$rules" = "rule $n," ]; then pass "rule B: $f fails with rule $n only"; else cat "$TMP/out"; fail "rule B: $f must fail with rule $n only, got: $rules"; fi
  done
  if [ "$count" = 7 ]; then pass "rule B: seven invalid fixtures, one per rule"; else fail "rule B: $count invalid fixtures, want 7"; fi

  # Rule B: the own FQDN comes from the path clusters/<fqdn>/.
  mkdir -p "$TMP/tree/clusters/$OWN/regions/r1" "$TMP/tree/clusters/other.example.org/regions/r1" "$TMP/tree/tests/x"
  cp "$FIXTURES/invalid-rule7.yaml" "$TMP/tree/clusters/$OWN/regions/r1/variables.yaml"
  cp "$FIXTURES/invalid-rule7.yaml" "$TMP/tree/clusters/other.example.org/regions/r1/variables.yaml"
  cp "$FIXTURES/valid.yaml" "$TMP/tree/tests/x/variables.yaml"
  expect_fail "rule B: rule 7 reads the own FQDN from the path" "rule 7:" "$CHECK" --root "$TMP/tree" "clusters/$OWN/regions/r1/variables.yaml"
  if "$CHECK" --root "$TMP/tree" "clusters/other.example.org/regions/r1/variables.yaml" > "$TMP/out" 2>&1; then pass "rule B: another Sovereign's path passes rule 7"; else cat "$TMP/out"; fail "rule B: another Sovereign's path passes rule 7"; fi
  expect_fail "rule B: a path without clusters/<fqdn>/ fails" "no own FQDN" "$CHECK" --root "$TMP/tree" "tests/x/variables.yaml"

  # Rule B: a document that is not the ConfigMap, or holds a value that is not a string, fails.
  sed 's/^  name: sovereign-variables$/  name: other-variables/' "$FIXTURES/valid.yaml" > "$TMP/name.yaml"
  expect_fail "rule B: another ConfigMap name fails" "must be the v1 ConfigMap" "$CHECK" --own-fqdn "$OWN" "$TMP/name.yaml"
  sed "s/^  HANDOVER_AUTO: 'true'$/  HANDOVER_AUTO: true/" "$FIXTURES/valid.yaml" > "$TMP/bool.yaml"
  expect_fail "rule B: a value that is not a string fails" "is not a string" "$CHECK" --own-fqdn "$OWN" "$TMP/bool.yaml"

  if [ "$failures" -ne 0 ]; then
    echo "check-variables-documents: self-test FAILED: $failures case(s)"
    exit 1
  fi
  echo "check-variables-documents: self-test passed"
  exit 0
fi

status=0
copies_equal docs/contracts "$PKG/contract" || status=1

git ls-files -co --exclude-standard -- clusters tests > "$TMP/all"
awk '/(^|\/)variables\.yaml$/' "$TMP/all" > "$TMP/files"
n="$(wc -l < "$TMP/files" | tr -d ' ')"
if [ "$n" -gt 0 ]; then
  mapfile -t files < "$TMP/files"
  "$CHECK" --root "$ROOT" "${files[@]}" || status=1
fi
echo "variables documents: $n"
exit "$status"
