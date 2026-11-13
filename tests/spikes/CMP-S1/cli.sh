#!/usr/bin/env bash
# CMP-S1 lab test, tool half (question 6). Runs only inside the
# spike-cmp-s1 workflow. Needs no cluster.
#
# Downloads the engine's command-line tool at KYVERNO_VERSION, checks its
# checksum, and runs one test suite against a ValidatingPolicy file and a
# MutatingPolicy file, once per candidate API version. Two negative suites
# (one wrong expectation each) must FAIL: that proves the tool evaluates the
# files instead of passing them unread.
#
# Prints one "RESULT key=value" line per measured value.
set -uo pipefail
export LC_ALL=C

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
OUT="${OUT:-$PWD/evidence-cli}"
VER="${KYVERNO_VERSION:-v1.19.2}"
mkdir -p "$OUT"
: > "$OUT/results.txt"

result() { printf 'RESULT %s=%s\n' "$1" "$2" | tee -a "$OUT/results.txt"; }

WORK="$(mktemp -d)"
ASSET="kyverno-cli_${VER}_linux_x86_64.tar.gz"
BASE="https://github.com/kyverno/kyverno/releases/download/${VER}"

echo "===== download $ASSET"
curl -fsSL -o "$WORK/$ASSET" "$BASE/$ASSET" || { echo "download failed"; exit 1; }
curl -fsSL -o "$WORK/checksums.txt" "$BASE/checksums.txt" || { echo "checksums download failed"; exit 1; }
if (cd "$WORK" && grep " ${ASSET}\$" checksums.txt | sha256sum -c -); then
  result cli_checksum_matches_release yes
else
  result cli_checksum_matches_release no
  exit 1
fi
result cli_tarball_sha256 "$(sha256sum "$WORK/$ASSET" | cut -d' ' -f1)"
tar -xzf "$WORK/$ASSET" -C "$WORK" kyverno
KY="$WORK/kyverno"
"$KY" version 2>&1 | tee "$OUT/cli-version.txt"
result cli_version "$("$KY" version 2>&1 | grep -i '^version' | head -1 | tr -d ' ')"

# run_suite <dir> <label> -> sets RC PASSED FAILED
run_suite() {
  local dir="$1" label="$2" log="$OUT/suite-$2.log"
  "$KY" test "$dir" --detailed-results >"$log" 2>&1
  RC=$?
  local summary
  summary="$(grep -E 'Test Summary: [0-9]+ tests? passed and [0-9]+ tests? failed' "$log" | tail -1)"
  PASSED="$(printf '%s' "$summary" | sed -nE 's/.*Test Summary: ([0-9]+) tests? passed.*/\1/p')"
  FAILED="$(printf '%s' "$summary" | sed -nE 's/.* and ([0-9]+) tests? failed.*/\1/p')"
  PASSED="${PASSED:-none}"
  FAILED="${FAILED:-none}"
  echo "----- suite $label (exit $RC)"
  cat "$log"
}

# make_suite <version> <test-file> <dest>
make_suite() {
  local v="$1" t="$2" d="$3"
  mkdir -p "$d"
  sed -e "s/__VP__/$v/g" "$HERE/cli/vpol.yaml" > "$d/vpol.yaml"
  sed -e "s/__MP__/$v/g" "$HERE/cli/mpol.yaml" > "$d/mpol.yaml"
  cp "$HERE/cli/resources.yaml" "$HERE/cli/patched.yaml" "$HERE/cli/patched-wrong.yaml" "$d/"
  cp "$HERE/cli/$t" "$d/kyverno-test.yaml"
}

for v in v1 v1beta1; do
  echo
  echo "===== API version policies.kyverno.io/$v"
  make_suite "$v" test-positive.yaml "$WORK/$v-positive"
  run_suite "$WORK/$v-positive" "$v-positive"
  result "Q6_${v}_positive" "exit=$RC passed=$PASSED failed=$FAILED"
  pos_ok=no
  if [ "$RC" -eq 0 ] && [ "$PASSED" = "3" ] && [ "$FAILED" = "0" ]; then pos_ok=yes; fi

  make_suite "$v" test-neg-vp.yaml "$WORK/$v-neg-vp"
  run_suite "$WORK/$v-neg-vp" "$v-neg-vp"
  result "Q6_${v}_negative_validating" "exit=$RC passed=$PASSED failed=$FAILED"
  negvp_ok=no
  if [ "$RC" -ne 0 ] && [ "$FAILED" != "none" ] && [ "$FAILED" -ge 1 ]; then negvp_ok=yes; fi

  make_suite "$v" test-neg-mp.yaml "$WORK/$v-neg-mp"
  run_suite "$WORK/$v-neg-mp" "$v-neg-mp"
  result "Q6_${v}_negative_mutating" "exit=$RC passed=$PASSED failed=$FAILED"
  negmp_ok=no
  if [ "$RC" -ne 0 ] && [ "$FAILED" != "none" ] && [ "$FAILED" -ge 1 ]; then negmp_ok=yes; fi

  result "Q6_${v}_validating_evaluated" "$([ "$pos_ok" = yes ] && [ "$negvp_ok" = yes ] && echo yes || echo no)"
  result "Q6_${v}_mutating_evaluated" "$([ "$pos_ok" = yes ] && [ "$negmp_ok" = yes ] && echo yes || echo no)"

  echo "----- cross-check: kyverno apply (validating, $v)"
  "$KY" apply "$WORK/$v-positive/vpol.yaml" --resource "$WORK/$v-positive/resources.yaml" 2>&1 | tail -15 | tee "$OUT/apply-vpol-$v.log"
  echo "----- cross-check: kyverno apply (mutating, $v)"
  "$KY" apply "$WORK/$v-positive/mpol.yaml" --resource "$WORK/$v-positive/resources.yaml" 2>&1 | tail -30 | tee "$OUT/apply-mpol-$v.log"
done

echo
echo "===== information only: the platform policy chart suites with this tool version"
if command -v helm >/dev/null 2>&1; then
  PATH="$WORK:$PATH" bash "$ROOT/scripts/run-policy-chart-tests.sh" 2>&1 | tee "$OUT/policy-chart-suites.log"
  rc=${PIPESTATUS[0]}
  result info_policy_chart_suites_exit "$rc"
  result info_policy_chart_suites_pass_lines "$(grep -c 'PASS' "$OUT/policy-chart-suites.log")"
  result info_policy_chart_suites_fail_lines "$(grep -c 'FAIL' "$OUT/policy-chart-suites.log")"
else
  result info_policy_chart_suites_exit "helm-missing"
fi

echo
echo "===== summary"
cat "$OUT/results.txt"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  { echo '### CMP-S1 tool half'; echo '```'; cat "$OUT/results.txt"; echo '```'; } >> "$GITHUB_STEP_SUMMARY"
fi
exit 0
