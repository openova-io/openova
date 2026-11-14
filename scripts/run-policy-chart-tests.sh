#!/usr/bin/env bash
# run-policy-chart-tests.sh - runs every test of the policy chart (CMP-014).
#
# Order: one chart render, then every policy suite in name order, then the
# shell tests listed in SHELL_TESTS. It stops at the first failure.
#
# A policy suite is a directory under <chart>/tests/ that holds a file named
# kyverno-test.yaml. A directory without that file (fixtures) is not a suite.
#
# This script installs nothing. `helm` and `kyverno` must be on the PATH.
#
# Usage:
#   bash scripts/run-policy-chart-tests.sh              run the tests
#   bash scripts/run-policy-chart-tests.sh --self-test  prove the runner can fail
#
# Exit code: 0 every test passed. 1 a test failed, or no suite was found.
# 2 wrong usage.
#
# POLICY_CHART_DIR  The chart to test. Default: platform/kyverno-policies/chart
#                   of this repository. The self-test points it at a fake chart.
set -euo pipefail
export LC_ALL=C

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART="${POLICY_CHART_DIR:-$ROOT/platform/kyverno-policies/chart}"
TAG="[policy-chart-tests]"

# Shell tests under <chart>/tests/, run in this order. One file name per line.
SHELL_TESTS=(
  nodeport-enforce-5491.sh
  apicall-rbac-correspondence.sh
  bootstrap-window.sh
)

# step <label> <command...>
# Runs the command. Prints one line. On failure prints the command's output
# first and stops the script.
step() {
  local label="$1" rc
  shift
  if "$@" >"$LOG" 2>&1; then
    echo "$TAG PASS $label"
  else
    rc=$?
    cat "$LOG"
    echo "$TAG FAIL $label (exit $rc)"
    exit 1
  fi
}

run_tests() {
  local test_file name suites=0
  LOG="$(mktemp)"
  trap 'rm -f "$LOG"' EXIT

  step "render" helm template "$CHART"

  shopt -s nullglob
  for test_file in "$CHART"/tests/*/kyverno-test.yaml; do
    name="$(basename "$(dirname "$test_file")")"
    step "suite $name" kyverno test "$CHART/tests/$name"
    suites=$((suites + 1))
  done
  shopt -u nullglob

  if [ "$suites" -eq 0 ]; then
    echo "$TAG FAIL no suite found under $CHART/tests"
    exit 1
  fi

  for name in "${SHELL_TESTS[@]}"; do
    step "shell $name" bash "$CHART/tests/$name"
  done

  echo "$TAG ALL PASS: 1 render, $suites suites, ${#SHELL_TESTS[@]} shell tests"
}

# st_case <name> <wanted exit code> <STUB_FAIL value> <wanted calls> [argument]
# Runs this script against the fake chart with the fake tools first on the
# PATH. Compares the exit code and the list of calls the fake tools recorded.
st_case() {
  local name="$1" want_rc="$2" fail="$3" want_calls="$4" rc=0 got_calls
  shift 4
  : >"$ST_TMP/calls"
  PATH="$ST_TMP/bin:$PATH" POLICY_CHART_DIR="$ST_TMP/chart" \
    STUB_CALLS="$ST_TMP/calls" STUB_FAIL="$fail" \
    bash "${BASH_SOURCE[0]}" "$@" >"$ST_TMP/out" 2>&1 || rc=$?
  got_calls="$(paste -s -d '|' "$ST_TMP/calls")"
  if [ "$rc" != "$want_rc" ] || [ "$got_calls" != "$want_calls" ]; then
    echo "self-test FAIL: $name"
    echo "  exit code: got $rc, want $want_rc"
    echo "  calls got:  $got_calls"
    echo "  calls want: $want_calls"
    cat "$ST_TMP/out"
    exit 1
  fi
  echo "self-test ok: $name"
}

self_test() {
  local name suites_calls shell_calls="" first_shell
  ST_TMP="$(mktemp -d)"
  trap 'rm -rf "$ST_TMP"' EXIT

  # A fake chart: two suites, one fixture directory that is not a suite.
  mkdir -p "$ST_TMP/bin" "$ST_TMP/chart/tests/a-suite" \
    "$ST_TMP/chart/tests/b-suite" "$ST_TMP/chart/tests/fixtures"
  : >"$ST_TMP/chart/tests/a-suite/kyverno-test.yaml"
  : >"$ST_TMP/chart/tests/b-suite/kyverno-test.yaml"
  : >"$ST_TMP/chart/tests/fixtures/resource.yaml"

  # Fake tools. Each records its call. Each fails when STUB_FAIL names it.
  # The single quotes are wanted: the fake tools expand the variables.
  # shellcheck disable=SC2016
  printf '%s\n' '#!/usr/bin/env bash' \
    'echo "helm $1" >>"$STUB_CALLS"' \
    'if [ "${STUB_FAIL:-}" = "render" ]; then exit 1; fi' \
    'exit 0' >"$ST_TMP/bin/helm"
  # shellcheck disable=SC2016
  printf '%s\n' '#!/usr/bin/env bash' \
    'echo "kyverno $1 ${2##*/}" >>"$STUB_CALLS"' \
    'if [ "${2##*/}" = "${STUB_FAIL:-}" ]; then exit 1; fi' \
    'exit 0' >"$ST_TMP/bin/kyverno"
  chmod +x "$ST_TMP/bin/helm" "$ST_TMP/bin/kyverno"
  for name in "${SHELL_TESTS[@]}"; do
    # shellcheck disable=SC2016
    printf '%s\n' '#!/usr/bin/env bash' \
      'echo "shell ${0##*/}" >>"$STUB_CALLS"' \
      'if [ "${0##*/}" = "${STUB_FAIL:-}" ]; then exit 1; fi' \
      'exit 0' >"$ST_TMP/chart/tests/$name"
    shell_calls="$shell_calls|shell $name"
  done
  suites_calls="helm template|kyverno test a-suite|kyverno test b-suite"
  first_shell="${SHELL_TESTS[0]}"

  st_case "1 every test passes, in order, and the fixture directory is skipped" \
    0 "" "$suites_calls$shell_calls"
  st_case "2 a failed render stops the run" \
    1 "render" "helm template"
  st_case "3 a failed suite stops the run before the shell tests" \
    1 "b-suite" "$suites_calls"
  st_case "4 a failed shell test stops the run" \
    1 "$first_shell" "$suites_calls|shell $first_shell"
  rm "$ST_TMP/chart/tests/a-suite/kyverno-test.yaml" \
    "$ST_TMP/chart/tests/b-suite/kyverno-test.yaml"
  st_case "5 a chart with no suite fails" \
    1 "" "helm template"
  st_case "6 an unknown argument is refused" \
    2 "" "" --no-such-option

  echo "self-test: 6 cases passed"
}

if [ "$#" -eq 0 ]; then
  run_tests
elif [ "$#" -eq 1 ] && [ "$1" = "--self-test" ]; then
  self_test
else
  echo "usage: bash scripts/run-policy-chart-tests.sh [--self-test]" >&2
  exit 2
fi
