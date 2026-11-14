#!/usr/bin/env bash
# bootstrap-window.sh - the bootstrap window of the policy chart (CMP-073).
#
# The window is open while compliancePolicies.bootstrapMode is true AND
# compliancePolicies.sovereignConsume is not "local". While it is open, every
# policy that uses the helper bp-kyverno-policies.action renders as Audit.
# Kit slot 27a sets sovereignConsume from the variable SOVEREIGN_CONSUME, and
# the hand-over sets that variable to "local", which closes the window.
#
# This test renders the chart three times and counts the enforcing policies:
# the documents of kind ClusterPolicy in which Enforce is the value of
# validationFailureAction or of a failureAction.
#
#   defaults                                        1 (forbid-local-path-storage)
#   --set compliancePolicies.sovereignConsume=local 9
#   --set compliancePolicies.bootstrapMode=false    9
#
# Usage:
#   bash bootstrap-window.sh              run the test on the chart above this file
#   bash bootstrap-window.sh <chart>      run the test on that chart directory
#                                         (the workflow Chart tests (PR) passes it)
#   bash bootstrap-window.sh --self-test  prove the test can fail
#
# Exit code: 0 pass. 1 a render failed or a count differs. 2 wrong usage.
#
# HELM_BIN  The helm command. Default: helm. The self-test sets a fake one.
set -euo pipefail
export LC_ALL=C

CHART="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HELM="${HELM_BIN:-helm}"
TAG="[bootstrap-window]"

# count_enforcing: reads a render on stdin. Prints the number of documents of
# kind ClusterPolicy in which Enforce is the value of validationFailureAction
# or of a failureAction. Documents are split at the lines that start with ---.
count_enforcing() {
  awk '
    function close_doc() {
      if (kind && enforce) n++
      kind = 0
      enforce = 0
    }
    /^---/ { close_doc(); next }
    /^kind:[[:space:]]*ClusterPolicy[[:space:]]*$/ { kind = 1 }
    /^[[:space:]]*(validationFailureAction|failureAction):[[:space:]]*"?Enforce"?[[:space:]]*$/ { enforce = 1 }
    END { close_doc(); print n + 0 }
  '
}

# check <label> <wanted count> [helm arguments...]
# Renders the chart with the arguments and compares the count. Prints one line.
# Returns 1 when the render fails or the count differs.
check() {
  local label="$1" want="$2" out err got
  shift 2
  err="$(mktemp)"
  if ! out="$("$HELM" template kyverno-policies "$CHART" "$@" 2>"$err")"; then
    cat "$err"
    rm -f "$err"
    echo "$TAG FAIL $label: the chart does not render"
    return 1
  fi
  rm -f "$err"
  got="$(printf '%s\n' "$out" | count_enforcing)"
  if [ "$got" != "$want" ]; then
    echo "$TAG FAIL $label: $got enforcing policies, want $want"
    return 1
  fi
  echo "$TAG PASS $label: $got enforcing policies"
}

run_test() {
  check "defaults" 1
  check "sovereignConsume=local" 9 --set compliancePolicies.sovereignConsume=local
  check "bootstrapMode=false" 9 --set compliancePolicies.bootstrapMode=false
  echo "$TAG ALL PASS"
}

# st_count <name> <wanted count> <render text>
st_count() {
  local got
  got="$(printf '%s' "$3" | count_enforcing)"
  if [ "$got" != "$2" ]; then
    echo "self-test FAIL: $1: counted $got, want $2"
    exit 1
  fi
  echo "self-test ok: $1"
}

# st_check <name> <wanted exit code> <wanted count> <fake helm output file or FAIL>
st_check() {
  local rc=0
  printf '%s\n' "$4" >"$ST_TMP/render"
  HELM="$ST_TMP/helm" check "case" "$3" >"$ST_TMP/out" 2>&1 || rc=$?
  if [ "$rc" != "$2" ]; then
    echo "self-test FAIL: $1: exit code $rc, want $2"
    cat "$ST_TMP/out"
    exit 1
  fi
  echo "self-test ok: $1"
}

self_test() {
  local enforce audit rule other
  ST_TMP="$(mktemp -d)"
  trap 'rm -rf "$ST_TMP"' EXIT

  enforce=$'---\napiVersion: kyverno.io/v1\nkind: ClusterPolicy\nmetadata:\n  name: a\nspec:\n  validationFailureAction: Enforce\n'
  audit=$'---\napiVersion: kyverno.io/v1\nkind: ClusterPolicy\nmetadata:\n  name: b\nspec:\n  validationFailureAction: Audit\n'
  rule=$'---\napiVersion: kyverno.io/v1\nkind: ClusterPolicy\nmetadata:\n  name: c\nspec:\n  rules:\n    - name: r\n      validate:\n        failureAction: Enforce\n'
  other=$'---\napiVersion: kyverno.io/v1\nkind: Policy\nmetadata:\n  name: d\nspec:\n  validationFailureAction: Enforce\n'

  st_count "1 a ClusterPolicy with validationFailureAction Enforce counts" 1 "$enforce"
  st_count "2 a ClusterPolicy with a rule failureAction Enforce counts" 1 "$rule"
  st_count "3 a ClusterPolicy in Audit does not count" 0 "$audit"
  st_count "4 another kind with Enforce does not count" 0 "$other"
  st_count "5 each document counts once, split at ---" 2 "$enforce$audit$rule$other$audit"

  # A fake helm: prints the file render, or fails when the file says FAIL.
  # The single quotes are wanted: the fake expands the variable itself.
  # shellcheck disable=SC2016
  printf '%s\n' '#!/usr/bin/env bash' \
    'if [ "$(cat "'"$ST_TMP"'/render")" = "FAIL" ]; then echo "render error" >&2; exit 1; fi' \
    'cat "'"$ST_TMP"'/render"' >"$ST_TMP/helm"
  chmod +x "$ST_TMP/helm"

  st_check "6 a matching count passes" 0 2 "$enforce$rule$audit"
  st_check "7 a different count fails" 1 1 "$enforce$rule$audit"
  st_check "8 a render that fails fails" 1 0 "FAIL"

  echo "self-test: 8 cases passed"
}

if [ "$#" -eq 0 ]; then
  run_test
elif [ "$#" -eq 1 ] && [ "$1" = "--self-test" ]; then
  self_test
elif [ "$#" -eq 1 ] && [ -d "$1" ]; then
  CHART="$(cd "$1" && pwd)"
  run_test
else
  echo "usage: bash bootstrap-window.sh [<chart directory> | --self-test]" >&2
  exit 2
fi
