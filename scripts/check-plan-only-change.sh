#!/usr/bin/env bash
# check-plan-only-change.sh - does a change touch only plan data, so conformance-local may skip the pack?
#
# Usage:
#   git diff --no-renames --name-only <base> <head> | bash scripts/check-plan-only-change.sh
#   bash scripts/check-plan-only-change.sh --self-test
#
# It reads the changed paths on stdin, one per line; blank lines are ignored. It prints one line.
#   exit 0  at least one path, and every path is docs/ledger/plan.yaml or a file under
#           docs/ledger/plan/ (tickets, done markers, spike records): only plan data changed, so the
#           job conformance-local skips the pack and the suite and ends green.
#   exit 1  the list is empty, or one path is anything else (code, charts, infra, scripts, tests,
#           .github, docs/ledger/gates.yaml, any other file): the job runs in full.
# Any exit code but 0 runs the job in full (.github/workflows/conformance-local.yaml), so an error
# here can never skip the pack. --self-test proves the cases on lists of its own; tree-guards.yaml
# runs it.
set -uo pipefail

# is_plan_path PATH: PATH is plan data.
is_plan_path() {
  case "$1" in
    docs/ledger/plan.yaml) return 0 ;;
    docs/ledger/plan/?*) return 0 ;;
  esac
  return 1
}

# decide: reads the paths on stdin, prints the reason, returns 0 for plan data only and 1 otherwise.
decide() {
  local path n=0
  while IFS= read -r path || [ -n "$path" ]; do
    [ -n "$path" ] || continue
    if ! is_plan_path "$path"; then
      echo "full run: $path is not plan data"
      return 1
    fi
    n=$((n + 1))
  done
  if [ "$n" -eq 0 ]; then
    echo "full run: no changed path was listed"
    return 1
  fi
  echo "plan data only: all $n changed path(s) are docs/ledger/plan.yaml or under docs/ledger/plan/"
  return 0
}

self_test() {
  local fails=0 got out
  # expect LABEL WANT LIST: decide on LIST (newline-separated) must return WANT.
  expect() {
    got=0
    out="$(printf '%s' "$3" | decide)" || got=$?
    if [ "$got" -eq "$2" ]; then
      echo "  ok   $1 -> exit $got ($out)"
    else
      echo "  FAIL $1: want exit $2, got $got ($out)"
      fails=1
    fi
  }
  local plan=$'docs/ledger/plan.yaml\ndocs/ledger/plan/tickets/FND-130.yaml\ndocs/ledger/plan/done/CLD-093.yaml\ndocs/ledger/plan/spikes/SPK-001.yaml\n'
  expect "plan-only list skips"                          0 "$plan"
  expect "plan.yaml alone skips"                         0 $'docs/ledger/plan.yaml\n'
  expect "one ticket alone skips"                        0 $'docs/ledger/plan/tickets/FND-130.yaml\n'
  expect "last line without a newline is read"           0 $'docs/ledger/plan.yaml\ndocs/ledger/plan/done/X.yaml'
  expect "blank lines between plan paths are ignored"    0 $'\ndocs/ledger/plan.yaml\n\n'
  expect "plan plus one other path runs (other last)"    1 "${plan}scripts/plan.py"$'\n'
  expect "plan plus one other path runs (other first)"   1 $'core/console/src/x.ts\n'"$plan"
  expect "plan plus the workflow itself runs"            1 "${plan}.github/workflows/conformance-local.yaml"$'\n'
  expect "gates.yaml alone runs"                         1 $'docs/ledger/gates.yaml\n'
  expect "gates.yaml with plan runs"                     1 "${plan}docs/ledger/gates.yaml"$'\n'
  expect "empty list runs"                               1 ""
  expect "blank lines only run"                          1 $'\n\n'
  expect "plan.yaml.orig is not plan data"               1 $'docs/ledger/plan.yaml.orig\n'
  expect "planning.md is not plan data"                  1 $'docs/ledger/planning.md\n'
  expect "a file named plan is not plan data"            1 $'docs/ledger/plan\n'
  expect "the bare folder docs/ledger/plan/ runs"        1 $'docs/ledger/plan/\n'
  expect "a plan path one level down runs"               1 $'x/docs/ledger/plan.yaml\n'
  expect "a path with a leading space runs"              1 $' docs/ledger/plan.yaml\n'
  # The command itself, as the workflow calls it: exit 0 only for plan data.
  got=0; printf '%s' "$plan" | bash "$0" > /dev/null || got=$?
  if [ "$got" -eq 0 ]; then echo "  ok   the command exits 0 on plan data"; else echo "  FAIL the command exits $got on plan data"; fails=1; fi
  got=0; printf 'docs/ledger/gates.yaml\n' | bash "$0" > /dev/null || got=$?
  if [ "$got" -eq 1 ]; then echo "  ok   the command exits 1 on gates.yaml"; else echo "  FAIL the command exits $got on gates.yaml"; fails=1; fi
  if [ "$fails" -ne 0 ]; then echo "check-plan-only-change: self-test FAILED"; return 1; fi
  echo "check-plan-only-change: self-test passed"
}

case "${1:-}" in
  --self-test) self_test; exit $? ;;
  "") decide; exit $? ;;
  *) echo "usage: $0 [--self-test]  (the changed paths on stdin)" >&2; exit 2 ;;
esac
