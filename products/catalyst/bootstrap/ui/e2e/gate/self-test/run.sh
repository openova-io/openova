#!/usr/bin/env bash
# e2e/gate/self-test/run.sh — the self-test of the browser harness (FND-161). It runs the harness for real:
# Chromium, the stand-in manifest of this folder and a local server for every host of the stand-in domain.
#
#   bash e2e/gate/self-test/run.sh      (from products/catalyst/bootstrap/ui, after npm ci and the Chromium install)
#
# It proves: the selector lists exactly the checks that are folders of the harness, expected-fail or required, in
# this place; a run writes one result file per check, PASS for the check whose tests all pass and FAIL (with the
# error of the failed test, a skipped test as SKIP) for the other; the run's user, a named identity and a further
# context are signed in; a second host opens under the same sign-on; keys reach a canvas; regionKubectl runs
# kubectl with each region's kubeconfig; a named check that the harness cannot run stops the run.
set -euo pipefail
export KUBECONFIG=/dev/null
here="$(cd "$(dirname "$0")" && pwd)"
cd "$here/../../.."
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
fail() { echo "SELF-TEST FAILED: $1"; exit 1; }

listed="$(node --experimental-strip-types e2e/gate/checks.ts e2e/gate/self-test/manifest.json cloud 2>/dev/null | tr '\n' ' ')"
[ "$listed" = "SELF-G01 SELF-G02 " ] || fail "checks.ts lists '$listed', not 'SELF-G01 SELF-G02 '"
echo "  PASS  the selector lists the two checks the harness runs, and none of the five it does not"

for who in run second viewer; do printf 'session-of-%s\n' "$who" > "$work/$who"; done
export GATE_SELF_TEST=1 GATE_FQDN=self-test.invalid GATE_SCHEME=http GATE_PORT="${GATE_PORT:-47961}" GATE_RUN_IN=cloud
export GATE_SESSION_RUN="$work/run" GATE_SESSION_SECOND="$work/second" GATE_SESSION_VIEWER="$work/viewer"
export GATE_KUBECONFIG_A="$here/kubeconfig-a.yaml" GATE_KUBECONFIG_B="$here/kubeconfig-b.yaml"
export GATE_RESULTS="$work/results" GATE_OUTPUT="$work/test-results"

set +e
bash e2e/gate/run.sh > "$work/log" 2>&1
rc=$?
set -e
sed 's/^/    | /' "$work/log"
[ "$rc" = 1 ] || fail "the run exited $rc, not 1 (SELF-G02 fails on purpose)"
[ "$(cd "$work/results" && ls | tr '\n' ' ')" = "SELF-G01.json SELF-G02.json " ] || fail "the results are not exactly SELF-G01.json and SELF-G02.json"
node -e '
  const fs = require("fs"), dir = process.argv[1];
  const r1 = JSON.parse(fs.readFileSync(dir + "/SELF-G01.json", "utf8"));
  const r2 = JSON.parse(fs.readFileSync(dir + "/SELF-G02.json", "utf8"));
  const bad = (m) => { console.error("SELF-TEST FAILED: " + m); process.exit(1); };
  const keys = (o) => Object.keys(o).sort().join(",");
  if (keys(r1) !== "id,status,steps" || r1.id !== "SELF-G01" || r1.status !== "PASS") bad("SELF-G01 is not {id, status PASS, steps}: " + JSON.stringify(r1));
  if (r1.steps.map((s) => s.id).join(" ") !== "S1 S2 S3 S4 S5 S6") bad("SELF-G01 steps are " + r1.steps.map((s) => s.id).join(" "));
  const time = /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z$/;
  for (const s of r1.steps) {
    if (s.journey !== "SELF-G01" || s.status !== "PASS" || !time.test(s.started_at) || !time.test(s.finished_at) || !Number.isInteger(s.duration_ms)) bad("SELF-G01 step " + JSON.stringify(s));
    if (s.evidence.file !== "products/catalyst/bootstrap/ui/e2e/gate/self-test/pass/harness.spec.ts") bad("SELF-G01 evidence " + JSON.stringify(s.evidence));
  }
  if (r2.id !== "SELF-G02" || r2.status !== "FAIL") bad("SELF-G02 is not FAIL: " + JSON.stringify(r2));
  const s7 = r2.steps.find((s) => s.id === "S7"), s8 = r2.steps.find((s) => s.id === "S8");
  if (!s7 || s7.status !== "FAIL" || !/on purpose/.test(s7.error || "")) bad("S7 is not a FAIL with its error: " + JSON.stringify(s7));
  if (!s8 || s8.status !== "SKIP") bad("S8 is not SKIP: " + JSON.stringify(s8));
' "$work/results"
echo "  PASS  one result file per check: SELF-G01 PASS with its six steps, SELF-G02 FAIL with the error of S7 and S8 as SKIP"

rm -rf "$work/results"
set +e
bash e2e/gate/run.sh SELF-G01 > "$work/log" 2>&1
rc=$?
set -e
[ "$rc" = 0 ] || { sed 's/^/    | /' "$work/log"; fail "the run of SELF-G01 alone exited $rc, not 0"; }
[ "$(cd "$work/results" && ls | tr '\n' ' ')" = "SELF-G01.json " ] || fail "the run of SELF-G01 alone wrote another result than SELF-G01.json"
echo "  PASS  a check named on the command line runs alone"

set +e
bash e2e/gate/run.sh SELF-G03 > "$work/log" 2>&1
rc=$?
set -e
[ "$rc" != 0 ] || fail "a run that names the not-built check SELF-G03 passed"
grep -q 'SELF-G03 is not a check the harness runs' "$work/log" || fail "the run that names SELF-G03 does not say why it stopped"
echo "  PASS  a named check that the harness cannot run stops the run"
echo "SELF-TEST OK"
