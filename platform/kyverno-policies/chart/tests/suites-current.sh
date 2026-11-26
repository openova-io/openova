#!/usr/bin/env bash
# suites-current.sh - each ValidatingPolicy and MutatingPolicy suite of the policy chart judges
# the policy as the chart renders it, not a stale copy of it (CMP-062).
#
# A policy suite tests/<name>/ holds a COPY of its policy (policy.yaml):
# `kyverno test` reads a file, never the chart. A copy that no longer equals
# its template would keep the suite green while the real admission changed.
# For every suite of the list SUITES below this test fails when
#   C1 policy.yaml is missing, or the template does not render;
#   C2 the render holds no ValidatingPolicy or MutatingPolicy named like the suite;
#   C3 policy.yaml is not the render of the template with the chart defaults;
# and it fails when
#   C4 a suite under tests/ holds a ValidatingPolicy or a MutatingPolicy and is not in SUITES,
#      unless it is in ELSEWHERE and the test named there exists and names it.
# A policy rewritten to the new kind adds its suite to SUITES.
#
# Usage:
#   bash suites-current.sh              run the test on the chart above this file
#   bash suites-current.sh <chart>      run the test on that chart directory
#                                       (the workflow Chart tests (PR) passes it)
#   bash suites-current.sh --self-test  prove each check can fail
#
# Re-render a suite's copy with:
#   helm template platform/kyverno-policies/chart --show-only templates/<template> \
#     | grep -v '^# Source:' | sed '1{/^---$/d}' > platform/kyverno-policies/chart/tests/<name>/policy.yaml
#
# Exit code: 0 pass. 1 a check failed. 2 wrong usage.
#
# HELM_BIN  The helm command. Default: helm. The self-test sets a fake one.
set -euo pipefail
export LC_ALL=C

CHART="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HELM="${HELM_BIN:-helm}"
TAG="[suites-current]"

# One suite per line: <suite directory, the policy's name> <template under templates/>.
SUITES=(
  "multi-replica-drainability baseline/01-multi-replica.yaml"
  "topology-spread baseline/03-topology-spread.yaml"
  "resource-requests baseline/05-resource-requests.yaml"
  "resource-limits baseline/06-resource-limits.yaml"
  "hpa-effective baseline/08-hpa-effective.yaml"
  "runasnonroot-readonlyrootfs baseline/17-runasnonroot-readonlyrootfs.yaml"
  "substrate-stays-on-host baseline/22-substrate-on-host.yaml"
  "probes-present baseline/04-probes.yaml"
  "harbor-proxy-pull baseline/11-harbor-proxy.yaml"
  "image-tag-pinned baseline/12-image-tag-pinned.yaml"
  "secret-not-in-env baseline/19-secret-not-in-env.yaml"
  "forbid-local-path-storage baseline/23-forbid-local-path.yaml"
  "pdb-permits-eviction baseline/02-pdb.yaml"
  "pvc-volume-expansion baseline/07-pvc-expansion.yaml"
  "cilium-l7-mtls baseline/09-cilium-l7-mtls.yaml"
  "prometheus-scrape baseline/13-prometheus-scrape.yaml"
  "networkpolicy-present baseline/14-networkpolicy-present.yaml"
  "otel-injected baseline/15-otel-injected.yaml"
  "backup-configured baseline/20-backup-configured.yaml"
  "stamp-cilium-enforced-label mutate/00-stamp-cilium-enforced-label.yaml"
  "inject-gitea-source-secretref mutate/01-inject-gitea-source-secretref.yaml"
  "app-label-mutate baseline/21-app-label-mutate.yaml"
)

# A suite whose copy holds more than one policy is kept current by its own
# test: <suite directory> <that test under tests/>.
ELSEWHERE=(
  "crossplane-package-runtime-admitted crossplane-runtime-suite-current.sh"
)

# check_suite <chart> <name> <template>: C1 to C3 for one suite. Prints one
# line. Returns 1 when a check fails.
check_suite() {
  local chart="$1" name="$2" template="$3" copy render
  copy="$chart/tests/$name/policy.yaml"
  render="$TMP/render-$name.yaml"
  if [ ! -f "$copy" ]; then
    echo "$TAG FAIL C1 $name: $copy is missing"
    return 1
  fi
  if ! "$HELM" template "$chart" --show-only "templates/$template" > "$render.raw" 2> "$TMP/err"; then
    cat "$TMP/err"
    echo "$TAG FAIL C1 $name: templates/$template does not render"
    return 1
  fi
  grep -v '^# Source:' "$render.raw" | sed '1{/^---$/d}' > "$render"
  if ! grep -q -x -E 'kind: (ValidatingPolicy|MutatingPolicy)' "$render" || ! grep -q -x "  name: $name" "$render"; then
    echo "$TAG FAIL C2 $name: the render of templates/$template holds no ValidatingPolicy or MutatingPolicy $name"
    return 1
  fi
  if ! diff -u "$copy" "$render" > "$TMP/diff"; then
    cat "$TMP/diff"
    echo "$TAG FAIL C3 $name: policy.yaml is not the current render of templates/$template; re-render it (see the header of this file)"
    return 1
  fi
  echo "$TAG PASS $name"
}

# check_listed <chart>: C4. Prints one line per suite that holds a
# ValidatingPolicy or a MutatingPolicy and is neither in SUITES nor in ELSEWHERE with a test that
# exists and names it. Returns 1 when there is one.
check_listed() {
  local chart="$1" copy name entry listed rc=0
  for copy in "$chart"/tests/*/policy.yaml; do
    [ -f "$copy" ] || continue
    grep -q -x -E 'kind: (ValidatingPolicy|MutatingPolicy)' "$copy" || continue
    name="$(basename "$(dirname "$copy")")"
    listed=0
    for entry in "${SUITES[@]}"; do
      if [ "${entry%% *}" = "$name" ]; then listed=1; fi
    done
    for entry in "${ELSEWHERE[@]}"; do
      if [ "${entry%% *}" = "$name" ] && [ -f "$chart/tests/${entry#* }" ] &&
        grep -q -F "tests/$name\"" "$chart/tests/${entry#* }"; then
        listed=1
      fi
    done
    if [ "$listed" -eq 0 ]; then
      echo "$TAG FAIL C4 $name: the suite holds a ValidatingPolicy or a MutatingPolicy and is not in SUITES of this file"
      rc=1
    fi
  done
  return "$rc"
}

run_test() {
  local entry failed=0
  for entry in "${SUITES[@]}"; do
    # shellcheck disable=SC2086 # the entry is two words on purpose
    check_suite "$CHART" $entry || failed=1
  done
  check_listed "$CHART" || failed=1
  if [ "$failed" -ne 0 ]; then
    echo "$TAG FAILED"
    return 1
  fi
  echo "$TAG ALL PASS: ${#SUITES[@]} suites current"
}

# st_case <name> <wanted exit code> <function> <arguments...>
st_case() {
  local label="$1" want="$2" rc=0
  shift 2
  "$@" > "$ST_TMP/out" 2>&1 || rc=$?
  if [ "$rc" != "$want" ]; then
    echo "self-test FAIL: $label: exit code $rc, want $want"
    cat "$ST_TMP/out"
    exit 1
  fi
  echo "self-test ok: $label"
}

self_test() {
  local doc legacy mutating
  ST_TMP="$(mktemp -d)"
  trap 'rm -rf "$ST_TMP" "$TMP"' EXIT
  mkdir -p "$ST_TMP/chart/tests/s1" "$ST_TMP/chart/tests/s2" "$ST_TMP/chart/tests/s3"
  doc=$'apiVersion: policies.kyverno.io/v1\nkind: ValidatingPolicy\nmetadata:\n  name: s1\nspec:\n  validationActions: [Audit]'
  legacy=$'apiVersion: kyverno.io/v1\nkind: ClusterPolicy\nmetadata:\n  name: s1\nspec:\n  validationFailureAction: Audit'

  # A fake helm: prints the file render with the lines helm adds, or fails
  # when the file says FAIL. The single quotes are wanted: the fake expands
  # the path itself.
  # shellcheck disable=SC2016
  printf '%s\n' '#!/usr/bin/env bash' \
    'if [ "$(cat "'"$ST_TMP"'/render")" = "FAIL" ]; then echo "render error" >&2; exit 1; fi' \
    'echo "---"; echo "# Source: x/templates/t.yaml"; cat "'"$ST_TMP"'/render"' > "$ST_TMP/helm"
  chmod +x "$ST_TMP/helm"
  HELM="$ST_TMP/helm"

  printf '%s\n' "$doc" > "$ST_TMP/render"
  printf '%s\n' "$doc" > "$ST_TMP/chart/tests/s1/policy.yaml"
  st_case "1 a copy equal to the render passes (the --- and Source lines are not part of it)" 0 check_suite "$ST_TMP/chart" s1 t.yaml
  printf '%s\n' "${doc/Audit/Deny}" > "$ST_TMP/chart/tests/s1/policy.yaml"
  st_case "2 C3 a copy that differs from the render fails" 1 check_suite "$ST_TMP/chart" s1 t.yaml
  st_case "3 C1 a missing policy.yaml fails" 1 check_suite "$ST_TMP/chart" s9 t.yaml
  printf 'FAIL\n' > "$ST_TMP/render"
  st_case "4 C1 a template that does not render fails" 1 check_suite "$ST_TMP/chart" s1 t.yaml
  printf '%s\n' "$legacy" > "$ST_TMP/render"
  printf '%s\n' "$legacy" > "$ST_TMP/chart/tests/s1/policy.yaml"
  st_case "5 C2 a render of the legacy kind fails, even when the copy equals it" 1 check_suite "$ST_TMP/chart" s1 t.yaml
  printf '%s\n' "$doc" > "$ST_TMP/render"
  printf '%s\n' "$doc" > "$ST_TMP/chart/tests/s2/policy.yaml"
  st_case "6 C2 a render of another policy's name fails, even when the copy equals it" 1 check_suite "$ST_TMP/chart" s2 t.yaml
  rm "$ST_TMP/chart/tests/s2/policy.yaml"

  SUITES=("s1 t.yaml")
  printf '%s\n' "$doc" > "$ST_TMP/chart/tests/s1/policy.yaml"
  printf '%s\n' "$legacy" > "$ST_TMP/chart/tests/s3/policy.yaml"
  st_case "7 C4 every suite with a ValidatingPolicy listed, a legacy suite not: passes" 0 check_listed "$ST_TMP/chart"
  printf '%s\n' "${doc/s1/s2}" > "$ST_TMP/chart/tests/s2/policy.yaml"
  st_case "8 C4 a suite with a ValidatingPolicy that is not listed fails" 1 check_listed "$ST_TMP/chart"
  # shellcheck disable=SC2016 # the line is the text a test names its suite by
  printf '%s\n' 'SUITE="$CHART/tests/s2"' > "$ST_TMP/chart/tests/s2-current.sh"
  ELSEWHERE=("s2 s2-current.sh")
  st_case "9 C4 a suite in ELSEWHERE whose test exists and names it passes" 0 check_listed "$ST_TMP/chart"
  rm "$ST_TMP/chart/tests/s2-current.sh"
  st_case "10 C4 a suite in ELSEWHERE whose test does not exist fails" 1 check_listed "$ST_TMP/chart"
  mutating=$'apiVersion: policies.kyverno.io/v1\nkind: MutatingPolicy\nmetadata:\n  name: s1\nspec:\n  mutations: []'
  printf '%s\n' "$mutating" > "$ST_TMP/render"
  printf '%s\n' "$mutating" > "$ST_TMP/chart/tests/s1/policy.yaml"
  st_case "11 C2 a copy equal to a MutatingPolicy render passes" 0 check_suite "$ST_TMP/chart" s1 t.yaml
  printf '%s\n' "${mutating/s1/s2}" > "$ST_TMP/chart/tests/s2/policy.yaml"
  st_case "12 C4 a suite with a MutatingPolicy that is not listed fails" 1 check_listed "$ST_TMP/chart"

  echo "self-test: 12 cases passed"
}

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
if [ "$#" -eq 0 ]; then
  run_test
elif [ "$#" -eq 1 ] && [ "$1" = "--self-test" ]; then
  self_test
elif [ "$#" -eq 1 ] && [ -d "$1" ]; then
  CHART="$(cd "$1" && pwd)"
  run_test
else
  echo "usage: bash suites-current.sh [<chart directory> | --self-test]" >&2
  exit 2
fi
