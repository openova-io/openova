#!/usr/bin/env bash
# check-guards-are-wired.sh — the meta-guard: a guard nothing runs cannot fire.
#
# WHY THIS EXISTS
# ---------------
# This repo keeps rediscovering ONE class: a check script is written, reviewed,
# merged — and then never referenced by a workflow, so it never runs and never
# fires. `docs/PRINCIPLES.md` records the sibling classes (a guard that tested a
# surface which cannot fail; a fail-open `|| echo WARN` that exits 0). This is
# the third member of the family and the cheapest to detect: the guard exists,
# is correct, self-tests green — and is dead code.
#
# Concrete instance that motivated this file (#6021, 2026-08-11):
# `scripts/check-per-region-role-secrets.sh` shipped with a full cluster-free
# `--self-test`, was referenced by ZERO workflows, and so could not fire on the
# per-region role-Secret split it was written to catch. Seven siblings were in
# the same state when this guard was written.
#
# THE CONTRACT
# ------------
# A check script that advertises a cluster-free `--self-test` has, by its own
# construction, a mode CI can run with no cluster, no credential and no cloud
# call. There is therefore no reason for it not to be wired, and an unwired one
# is always an oversight. So:
#
#   every scripts/check-*.sh that advertises `--self-test`
#     MUST be INVOKED by at least one .github/workflows/*.yml|yaml
#
# Scripts WITHOUT a `--self-test` are deliberately out of scope: many require a
# live cluster to say anything at all, and forcing them into CI would only breed
# fail-open stubs. That exemption is the guard's own control — see --self-test.
#
# "INVOKED", NOT "MENTIONED"
# --------------------------
# The reference must look like a shell invocation (`bash scripts/x.sh`,
# `./scripts/x.sh`, `sh scripts/x.sh`, `source scripts/x.sh`). Two shapes
# deliberately DO NOT count, because neither runs anything:
#
#   * a `paths:` trigger filter — `      - 'scripts/x.sh'` — which only decides
#     WHEN a workflow fires, never WHAT it executes. Counting these would let a
#     script be "wired" by a path filter in a workflow that never calls it, and
#     this guard would then certify the exact theater it exists to prevent.
#   * a YAML comment mentioning the script.
#
# Exit codes:
#   0 — every self-test-capable guard is invoked by at least one workflow.
#   1 — at least one is orphaned.
#   2 — usage / setup error.

set -euo pipefail

SELF_TEST_ONLY=0
ROOT="${ROOT:-.}"

while [ $# -gt 0 ]; do
  case "$1" in
    --self-test) SELF_TEST_ONLY=1; shift ;;
    --root)      ROOT="$2"; shift 2 ;;
    *) echo "Unknown arg: $1" >&2
       echo "Usage: $0 [--root <repo-root>] [--self-test]" >&2
       exit 2 ;;
  esac
done

# ── The two predicates, factored out so --self-test exercises the SAME code
#    the real run uses (a self-test over a reimplementation proves nothing).

# advertises_self_test <script-path> — does this guard offer a cluster-free mode?
advertises_self_test() {
  grep -qE -- '--self-test' "$1" 2>/dev/null
}

# invoked_set <workflow-dir> — every guard basename any workflow EXECUTES.
#
# ONE pass over the workflow tree (per-script rescanning was O(scripts x lines)
# and took minutes on this repo). Comment-only lines are dropped first, then a
# shell invocation token is REQUIRED immediately before the path — `bash x.sh`,
# `sh x.sh`, `source x.sh`, `./x.sh`. A `paths:` list item (`- 'scripts/x.sh'`)
# carries no such token and is therefore correctly not counted.
INVOKE_RE='(^|[[:space:]|;&(])((bash|sh|source)[[:space:]]+|\./)[A-Za-z0-9_./-]*check-[A-Za-z0-9_.-]+\.sh'
invoked_set() {
  local wfdir="$1" f
  [ -d "$wfdir" ] || return 0
  # Collect only files that EXIST. A bare `cat dir/*.yml dir/*.yaml` breaks when
  # one glob matches nothing: bash passes the literal pattern, cat exits 1, and
  # `set -o pipefail` turns the whole pipeline non-zero — which under `set -e`
  # aborted this script silently and made an EMPTY invoked-set look like "wired
  # by nothing". That failure mode passed the vacuity check for the wrong
  # reason, so it is guarded here rather than with `|| true`.
  local files=()
  for f in "$wfdir"/*.yml "$wfdir"/*.yaml; do
    [ -f "$f" ] && files+=("$f")
  done
  [ "${#files[@]}" -gt 0 ] || return 0
  grep -hvE '^[[:space:]]*#' "${files[@]}" \
    | grep -oE "$INVOKE_RE" \
    | grep -oE 'check-[A-Za-z0-9_.-]+\.sh' \
    | sort -u || true
}

# in_set <name> <list> — is <name> one whole line of the newline-separated
# <list>? Pure bash: no pipe, no child process. The former test piped the list
# into `grep -q` under `set -o pipefail`. grep -q exits at its first match; the
# writer then wrote into a closed pipe, died of SIGPIPE, and pipefail turned a
# found name into "not found". So a guard that a workflow runs was now and then
# reported as orphaned. A test without a pipe cannot fail that way.
in_set() {
  case $'\n'"$2"$'\n' in
    *$'\n'"$1"$'\n'*) return 0 ;;
  esac
  return 1
}

# contains <text> <needle> — does <text> contain <needle> anywhere? Pure bash,
# for the same reason as in_set.
contains() {
  case "$1" in
    *"$2"*) return 0 ;;
  esac
  return 1
}

# is_invoked <basename> <workflow-dir> — thin wrapper, used by the self-test so
# it exercises the SAME matcher and the SAME membership test the real run uses.
is_invoked() {
  in_set "$1" "$(invoked_set "$2")"
}

# ── Self-test — fixtures only, no repo state, no cluster ────────────────
if [ "$SELF_TEST_ONLY" -eq 1 ]; then
  T="$(mktemp -d)"
  trap 'rm -rf "$T"' EXIT
  mkdir -p "$T/scripts" "$T/.github/workflows"

  # (a) self-test-capable AND invoked  → must NOT be flagged
  printf '#!/usr/bin/env bash\ncase "$1" in --self-test) exit 0;; esac\n' > "$T/scripts/check-wired.sh"
  # (b) self-test-capable, NOT invoked → MUST be flagged  (the #6021 shape)
  printf '#!/usr/bin/env bash\ncase "$1" in --self-test) exit 0;; esac\n' > "$T/scripts/check-orphan.sh"
  # (c) CONTROL — shares the suspect property (unwired) but has NO --self-test.
  #     A cluster-only guard must stay green, or this gate would force fail-open
  #     stubs into CI. This is the control that keeps the rule honest.
  printf '#!/usr/bin/env bash\necho needs a cluster\n' > "$T/scripts/check-cluster-only.sh"
  # (d) self-test-capable, mentioned ONLY by a paths: filter and a comment
  #     → MUST be flagged (a path filter executes nothing).
  printf '#!/usr/bin/env bash\ncase "$1" in --self-test) exit 0;; esac\n' > "$T/scripts/check-paths-only.sh"

  cat > "$T/.github/workflows/w.yaml" <<'YAML'
on:
  pull_request:
    paths:
      - 'scripts/check-paths-only.sh'
jobs:
  g:
    steps:
      # scripts/check-orphan.sh is discussed here but never run
      - run: bash scripts/check-wired.sh
YAML

  fails=0
  # `set -e` would abort on the deliberately-false assertions below, so every
  # predicate call captures its status instead of letting it propagate.
  expect() { # expect <label> <expected 0|1> <cmd...>
    local label="$1" want="$2"; shift 2
    local got=0
    "$@" >/dev/null 2>&1 || got=$?
    [ "$got" -gt 1 ] && got=1
    if [ "$want" -ne "$got" ]; then echo "  FAIL: $label (want $want, got $got)" >&2; fails=1
    else echo "  ok  $label"; fi
  }

  WF="$T/.github/workflows"
  expect "invoked script is seen as wired"               0 is_invoked "check-wired.sh"      "$WF"
  expect "comment-only mention is NOT wired"             1 is_invoked "check-orphan.sh"     "$WF"
  expect "paths:-filter-only mention is NOT wired"       1 is_invoked "check-paths-only.sh" "$WF"
  expect "--self-test advertised is detected"            0 advertises_self_test "$T/scripts/check-orphan.sh"
  expect "CONTROL: cluster-only guard has no --self-test" 1 advertises_self_test "$T/scripts/check-cluster-only.sh"

  # (e) MEMBERSHIP GIVES THE SAME ANSWER ON EVERY RUN. The list is far larger
  #     than a pipe's buffer (64 KiB), so a membership test that pipes the list
  #     into a reader which exits at its first match fails here on every run,
  #     not only now and then as it did on the real invoked set.
  BIG="$(printf 'check-big-%05d.sh\n' $(seq 1 20000))"
  stable=1
  for _ in $(seq 1 20); do
    in_set "check-big-00001.sh" "$BIG" || stable=0
  done
  if [ "$stable" -eq 1 ]; then
    echo "  ok  the first name of a 20000-name list is a member on 20 runs in a row"
  else
    echo "  FAIL: the first name of a 20000-name list was not a member on every run" >&2; fails=1
  fi
  expect "the last name of a large list is a member"     0 in_set "check-big-20000.sh" "$BIG"
  expect "a name not in a large list is not a member"    1 in_set "check-big-20001.sh" "$BIG"
  expect "part of a line is not a member"                1 in_set "big-00001.sh"       "$BIG"
  expect "a line plus more is not a member"              1 in_set "check-big-00001.sh.x" "$BIG"

  # End-to-end over the fixture tree: exactly the two orphans, never the control.
  out="$(ROOT="$T" "$0" --root "$T" 2>&1 || true)"
  contains "$out" 'check-orphan.sh'      && echo "  ok  end-to-end flags the orphan"          || { echo "  FAIL: orphan not reported" >&2; fails=1; }
  contains "$out" 'check-paths-only.sh'  && echo "  ok  end-to-end flags the paths-only shape" || { echo "  FAIL: paths-only not reported" >&2; fails=1; }
  if contains "$out" 'check-cluster-only.sh'; then
    echo "  FAIL: CONTROL was flagged — the exemption is broken" >&2; fails=1
  else
    echo "  ok  CONTROL (cluster-only, unwired) stays green"
  fi

  # VACUITY — the gate must be able to FAIL. It just did, on the fixture tree.
  ( ROOT="$T" "$0" --root "$T" >/dev/null 2>&1 ) && { echo "  FAIL: vacuity — gate returned 0 on a tree with 2 orphans" >&2; fails=1; } \
                                                || echo "  ok  vacuity: gate exits non-zero when orphans exist"

  [ "$fails" -eq 0 ] || { echo "SELF-TEST FAILED" >&2; exit 1; }
  echo "OK — self-test passed (wired / comment-only / paths-only distinguished; cluster-only CONTROL exempt; gate provably fails)."
  echo "OK: --self-test only; no cluster was contacted."
  exit 0
fi

# ── Real run ────────────────────────────────────────────────────────────
cd "$ROOT" || { echo "cannot cd to $ROOT" >&2; exit 2; }
WFDIR=".github/workflows"
[ -d "$WFDIR" ] || { echo "no $WFDIR under $ROOT" >&2; exit 2; }

INVOKED="$(invoked_set "$WFDIR")"

orphans=0
checked=0
for s in scripts/check-*.sh; do
  [ -f "$s" ] || continue
  advertises_self_test "$s" || continue      # cluster-only guards are exempt
  checked=$((checked + 1))
  b="$(basename "$s")"
  if ! in_set "$b" "$INVOKED"; then
    echo "ORPHANED GUARD: $s advertises --self-test but no workflow INVOKES it"
    orphans=$((orphans + 1))
  fi
done

if [ "$orphans" -gt 0 ]; then
  echo ""
  echo "FAIL: $orphans of $checked self-test-capable guard(s) are never run by CI."
  echo "A guard nothing runs is a guard that cannot fire. Add a workflow step:"
  echo "    - run: bash scripts/<guard>.sh --self-test"
  echo "(a 'paths:' trigger entry does NOT count — it selects WHEN a workflow"
  echo " runs, never WHAT it executes.)"
  exit 1
fi

echo "OK — all $checked self-test-capable guard(s) are invoked by at least one workflow."
