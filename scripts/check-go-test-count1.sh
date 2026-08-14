#!/usr/bin/env bash
# check-go-test-count1.sh — every `go test` in a workflow must pass `-count=1`.
#
# WHY THIS EXISTS — Refs #6235
# ----------------------------
# `go test` caches a package's result under a key computed from that package's
# own inputs: its source files, its build flags, its environment, and files it
# opens through the test binary's own tracked paths. This repo has no root
# `go.mod` — every component is its own module — and ~25 test files deliberately
# read files OUTSIDE their module: chart templates, `blueprint.yaml`s, `.tf`
# modules, `clusters/_template/**`, even other trees' TypeScript. Those reads are
# INVISIBLE to the cache key. Mutate the external file and the cached verdict
# still stands.
#
# Measured on this repo, 2026-08-14, against
# core/services/provisioning/gitops/openclaw_secret_producer_6114_test.go, which
# reads ../../../../platform/openclaw/chart/templates/ :
#
#   $ python3 -c "...s.replace('kind: Secret','kind: ConfigMap',1)..."   # producer gone
#   $ go test ./gitops/
#   ok  github.com/openova-io/openova/core/services/provisioning/gitops  (cached)
#   $ go test -count=1 ./gitops/
#   FAIL github.com/openova-io/openova/core/services/provisioning/gitops 0.028s
#
# A clean pass that never read the mutated file. This is the purest form of the
# fail-open family in #6235: not a gate that fires for the wrong reason, but one
# that can report a stale green indefinitely and never execute at all.
#
# `-count=1` is the documented idiom for disabling test caching. It costs one
# re-run of an already-fast suite; a stale green costs a released artifact.
#
# WHY A BLANKET RULE RATHER THAN "ONLY THE CROSS-MODULE ONES"
# -----------------------------------------------------------
# Scoping this to the modules that read outside themselves today would need a
# list, and the list rots the moment a new test adds an `os.ReadFile("../../..")`
# — silently, because the symptom is a PASS. Every compliant step in this repo
# already passes `-count=1` (24 of 27 when this guard was written), so the rule
# is not a new burden; it is the existing convention, enforced.
#
# Usage:  scripts/check-go-test-count1.sh [--root <repo-root>] [--self-test]
# Exit:   0 = every `go test` carries -count=1
#         1 = at least one does not
#         2 = usage / setup error

set -euo pipefail

ROOT="${ROOT:-.}"
SELF_TEST=0
while [ $# -gt 0 ]; do
  case "$1" in
    --root)      ROOT="$2"; shift 2 ;;
    --self-test) SELF_TEST=1; shift ;;
    *) echo "Unknown arg: $1" >&2
       echo "Usage: $0 [--root <repo-root>] [--self-test]" >&2; exit 2 ;;
  esac
done

# ── The predicate, factored out so --self-test exercises the SAME code the real
#    run uses. A self-test over a re-implementation proves nothing.
#
# offenders <workflow-dir> — prints `file:line: <command>` for every `go test`
# invocation that does not carry -count=1.
#
# Shapes that must NOT be reported:
#   * a YAML comment mentioning `go test` (openclaw-controller.yaml has one in
#     its header, and test-ungated-trees.yaml has several in `echo "::error"`
#     strings) — comments run nothing;
#   * a step name / echo string containing the words "go test";
#   * `go test` split across a backslash continuation with `-count=1` on a later
#     physical line (pool-domain-manager's suite list is written that way).
offenders() {
  local wfdir="$1" f
  [ -d "$wfdir" ] || return 0
  local files=()
  for f in "$wfdir"/*.yml "$wfdir"/*.yaml; do [ -f "$f" ] && files+=("$f"); done
  [ "${#files[@]}" -gt 0 ] || return 0
  for f in "${files[@]}"; do
    awk -v FNAME="$f" '
      # Join backslash continuations so a multi-line invocation is one record,
      # remembering the line number the invocation STARTED on.
      {
        line = $0
        sub(/^[ \t]*/, "", line)
        if (pending != "") { buf = buf " " line } else { buf = line; start = NR }
        if (buf ~ /\\$/) { sub(/\\$/, "", buf); pending = 1; next }
        pending = ""
        rec = buf; buf = ""

        # Drop YAML comments — they execute nothing.
        if (rec ~ /^#/) next
        # Drop step names / keys — `- name: go test (…)`, `name: go test`.
        if (rec ~ /^-?[ \t]*name:[ \t]/) next
        # Drop echo/printf lines that merely mention it.
        if (rec ~ /^(echo|printf)[ \t]/) next

        # A real invocation: `go test` at the start of a command position.
        if (rec !~ /(^|[ \t;&|(])go[ \t]+test([ \t]|$)/) next
        if (rec ~ /-count[= \t]+?1([ \t]|$)/) next
        if (rec ~ /-count=1([ \t]|$)/) next
        printf "%s:%d: %s\n", FNAME, start, rec
      }
    ' "$f"
  done
}

# ── Self-test — fixtures only, no repo state, no cluster ────────────────────
if [ "$SELF_TEST" -eq 1 ]; then
  T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
  W="$T/.github/workflows"; mkdir -p "$W"
  RC=0
  chk() { if [ "$2" = "$3" ]; then echo "  ok   $1"; else echo "  FAIL $1 (want '$2', got '$3')"; RC=1; fi; }

  cat > "$W/compliant.yaml" <<'YAML'
      - name: go test
        run: go test -count=1 -race ./...
YAML
  cat > "$W/offender.yaml" <<'YAML'
      - name: unit
        run: go test ./...
YAML
  cat > "$W/comment.yaml" <<'YAML'
# This header explains that `go test ./...` exits 0 on a module with no tests.
      - name: unit
        run: go test -count=1 ./...
YAML
  cat > "$W/stepname.yaml" <<'YAML'
      - name: go test (race + count=1)
        run: go test -count=1 -race ./...
YAML
  cat > "$W/echoed.yaml" <<'YAML'
      - name: unit
        run: |
          echo "go test ./... exits 0 with no test files, so this check exists"
          go test -count=1 ./...
YAML
  cat > "$W/continuation.yaml" <<'YAML'
      - name: unit
        run: |
          go test -count=1 ./internal/a/... \
                  ./internal/b/...
YAML
  cat > "$W/continuation-bad.yaml" <<'YAML'
      - name: unit
        run: |
          go test ./internal/a/... \
                  ./internal/b/...
YAML
  cat > "$W/spaced.yaml" <<'YAML'
      - name: unit
        run: go test -count 1 ./...
YAML

  got="$(offenders "$W" | sed "s#$W/##" | cut -d: -f1 | sort -u | tr '\n' ' ' | sed 's/ $//')"
  echo "== check-go-test-count1 self-test (fixtures only, no cluster) =="
  chk "flags a bare 'go test ./...' and nothing else" \
      "continuation-bad.yaml offender.yaml" "$got"

  # Each control on its own, so a broadened matcher cannot hide behind the set.
  one() { offenders "$W" | grep -c "/$1:" || true; }
  chk "compliant -count=1 not flagged"          0 "$(one compliant.yaml)"
  chk "comment mentioning go test not flagged"  0 "$(one comment.yaml)"
  chk "step NAME containing 'go test' not flagged" 0 "$(one stepname.yaml)"
  chk "echoed 'go test' string not flagged"     0 "$(one echoed.yaml)"
  chk "backslash continuation with -count=1 not flagged" 0 "$(one continuation.yaml)"
  chk "'-count 1' (space form) not flagged"     0 "$(one spaced.yaml)"
  chk "bare invocation IS flagged"              1 "$(one offender.yaml)"
  chk "bare multi-line invocation IS flagged"   1 "$(one continuation-bad.yaml)"

  # VACUITY: an empty workflow dir must yield nothing, and the guard must not
  # mistake "found nothing because I read nothing" for a pass.
  E="$T/empty/.github/workflows"; mkdir -p "$E"
  chk "empty workflow dir yields no offenders" "" "$(offenders "$E")"

  if [ "$RC" -eq 0 ]; then echo "SELF-TEST PASS (10 cases, fixtures only — no cluster, no network)"
  else echo "SELF-TEST FAIL"; fi
  exit "$RC"
fi

WFDIR="${ROOT}/.github/workflows"
[ -d "$WFDIR" ] || { echo "FATAL: $WFDIR not found" >&2; exit 2; }

# Refuse to report a pass from an empty read — the guard must have seen work.
seen="$(ls -1 "$WFDIR"/*.yml "$WFDIR"/*.yaml 2>/dev/null | wc -l)"
if [ "$seen" -eq 0 ]; then
  echo "FATAL: no workflow files under $WFDIR — refusing to report a pass on an empty scan." >&2
  exit 2
fi

FOUND="$(offenders "$WFDIR" || true)"
if [ -n "$FOUND" ]; then
  echo "FAIL: a workflow runs 'go test' without -count=1 (#6235)." >&2
  echo "" >&2
  printf '%s\n' "$FOUND" >&2
  echo "" >&2
  echo "  This repo has no root go.mod and ~25 test files read chart templates," >&2
  echo "  blueprint.yaml, .tf modules and clusters/_template/** from OUTSIDE their" >&2
  echo "  own module. Those reads are invisible to the go test cache key, so a" >&2
  echo "  cached 'ok' can stand over a mutated external file indefinitely." >&2
  echo "  Add -count=1 to the invocation." >&2
  exit 1
fi
echo "OK: all ${seen} workflow file(s) scanned; every 'go test' carries -count=1 (#6235)."
