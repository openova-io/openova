#!/usr/bin/env bash
# #5759 — behavioural test for the Day-2 reconciler's CONTINUOUS source-host
# drift lint (run_daytwo_source_host_lint, 12-daytwo-harbor-pin-reconciler.yaml).
#
# WHY a behavioural test and not a render grep, and why a NEW file rather than
# extending flux-source-host-lint.sh. The host-classification shape is
# deliberately byte-similar to step-08's run_flux_source_host_lint (same
# lesson: assert on the VALUE not the KEY, per
# reference_assert_on_the_value_not_the_key_and_sample_every_role.md), but the
# GATING is different and is the actual point of this lint, so it needs its
# own cases:
#   - step-08's lint runs ONCE, inside the torn-down 600s deny-egress hold, at
#     the ORIGINAL cutover.
#   - this lint runs every ~300s cycle of a STANDING Deployment for the
#     Sovereign's entire post-cutover life (#5157: a Deployment, not a
#     CronJob, survives a region-kill) — interval-independent, per #5759 item
#     2 — and it must NOT fire pre-cutover, when an external host is the
#     NORMAL, correct state. A test that only proved "fails on tether, passes
#     on local" would miss the one property that makes this lint safe to run
#     continuously: cases 9-11 below.
#
# Extracted straight from the rendered chart (not a hand-kept copy — #5646:
# a copy can silently drift from the shipped source and keep passing after
# the real thing changed).
set -uo pipefail

CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)"
FQDN="hw292.omani.works"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0

fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" --set daytwoReconciler.enabled=true \
  --show-only templates/12-daytwo-harbor-pin-reconciler.yaml >"${TMP}/render.yaml" 2>"${TMP}/render.err" || {
  echo "helm template failed:"; cat "${TMP}/render.err"; exit 1
}

awk '/^ *run_daytwo_source_host_lint\(\) \{/,/^ *\}$/' "${TMP}/render.yaml" \
  | sed 's/^ *//' >"${TMP}/fn.sh"

if ! grep -q 'run_daytwo_source_host_lint()' "${TMP}/fn.sh"; then
  echo "FAIL — could not extract run_daytwo_source_host_lint from the rendered Deployment."
  echo "       The lint is missing from the chart, or the function name changed."
  exit 1
fi
# The awk range terminates on the first bare '}' line — pin the tail
# explicitly so a nested multi-line construct silently truncating the
# extraction is a test failure, not a fragment quietly passing.
grep -q '^return 0$' "${TMP}/fn.sh" \
  || { echo "FAIL — fn.sh was truncated during extraction (no trailing 'return 0')."; exit 1; }
# Vacuity on the extraction itself (#5652 shape): too short to be the real
# body would make every case below pass on nothing.
fn_lines=$(wc -l <"${TMP}/fn.sh")
[ "${fn_lines}" -ge 30 ] || { echo "FAIL — extracted function is only ${fn_lines} lines — looks truncated"; exit 1; }

# ── Harness: stub kubectl feeds the source inventory under test ────────────
mkdir -p "${TMP}/bin"
cat >"${TMP}/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
kind=""
want=0
for a in "$@"; do
  if [ "$want" = "1" ]; then kind="$a"; break; fi
  [ "$a" = "get" ] && want=1
done
if [ -n "${STUB_FAIL_KIND:-}" ] && [ "${kind}" = "${STUB_FAIL_KIND}" ]; then
  echo "${STUB_FAIL_MSG:-error: You must be logged in to the server (Unauthorized)}" >&2
  exit 1
fi
f="${STUB_DIR}/${kind}.txt"
[ -f "$f" ] && cat "$f"
exit 0
STUB
chmod +x "${TMP}/bin/kubectl"

# run_case <desc> <cutoverComplete> <enabled> <allow> <kind|row> ...
run_case() {
  local desc="$1" cc="$2" enabled="$3" allow="$4"; shift 4
  local case_dir="${TMP}/case"; rm -rf "${case_dir}"; mkdir -p "${case_dir}"
  : >"${case_dir}/helmrepositories.txt"
  : >"${case_dir}/gitrepositories.txt"
  : >"${case_dir}/ocirepositories.txt"
  for spec in "$@"; do
    local kind="${spec%%|*}" row="${spec#*|}"
    printf '%s\n' "${row}" >>"${case_dir}/${kind}.txt"
  done
  mkdir -p "${TMP}/work"; : >"${TMP}/drift-status.txt"
  (
    export PATH="${TMP}/bin:${PATH}"
    export STUB_DIR="${case_dir}"
    export SOVEREIGN_FQDN="${FQDN}"
    export LOCAL_REGISTRY_HOST="registry.${FQDN}"
    export SOURCE_HOST_LINT_ALLOW="${allow}"
    export SOURCE_HOST_LINT_ENABLED="${enabled}"
    export CUTOVER_COMPLETE="${cc}"
    export WORK="${TMP}/work"
    cd "${TMP}"
    # shellcheck disable=SC1090
    . "${TMP}/fn.sh"
    rc=0
    run_daytwo_source_host_lint >"${TMP}/out.txt" 2>&1 || rc=1
    printf 'DRIFT_STATUS=%s DRIFT_COUNT=%s\n' "${DRIFT_STATUS:-}" "${DRIFT_COUNT:-}" >"${TMP}/drift-status.txt"
    exit "${rc}"
  )
  if [ $? -eq 0 ]; then echo PASS; else echo FAIL; fi
}

expect() {
  local desc="$1" want="$2" got="$3"
  if [ "${got}" = "${want}" ]; then pass "${desc} (${got})"
  else fail "${desc}: expected ${want}, got ${got}"; sed 's/^/        /' "${TMP}/out.txt" 2>/dev/null; fi
}

echo "[daytwo-source-host-lint] #5759 — base shape: fail on a drifted source, pass on a local one (cutoverComplete=true)"

# 1. POSITIVE CONTROL — all local, post-cutover. Must PASS.
got=$(run_case "all-local" "true" "true" "" \
  "helmrepositories|flux-system bp-cilium oci://registry.${FQDN}/openova-io " \
  "helmrepositories|flux-system bp-agenity oci://registry.${FQDN}/openova-io " \
  "gitrepositories|flux-system catalog https://gitea.${FQDN}/catalog/openova ")
expect "all-local sources, cutoverComplete=true" PASS "${got}"

# 2. THE #5759 SHAPE — hw292 post-force-push: 62 HelmRepositories reverted to
#    ghcr.io. Must FAIL.
got=$(run_case "reverted-to-ghcr" "true" "true" "" \
  "helmrepositories|flux-system bp-cilium oci://registry.${FQDN}/openova-io " \
  "helmrepositories|flux-system bp-keycloak oci://ghcr.io/openova-io ")
expect "HelmRepository reverted to ghcr.io post-cutover (the #5759 shape)" FAIL "${got}"
grep -q 'DRIFT_STATUS=drifted DRIFT_COUNT=1' "${TMP}/drift-status.txt" \
  && pass "DRIFT_STATUS/DRIFT_COUNT feed publish_health() a countable reason" \
  || fail "drift status/count not set correctly: $(cat "${TMP}/drift-status.txt")"

# 3. VACUITY — same inventory, reviewed exception declared. Must PASS,
#    proving case 2 failed on the HOST.
got=$(run_case "allowed-exception" "true" "true" "ghcr.io" \
  "helmrepositories|flux-system bp-cilium oci://registry.${FQDN}/openova-io " \
  "helmrepositories|flux-system bp-keycloak oci://ghcr.io/openova-io ")
expect "same drift with an explicit SOURCE_HOST_LINT_ALLOW entry" PASS "${got}"

# 4. A suspended source cannot reconcile — reported, must not fail.
got=$(run_case "suspended" "true" "true" "" \
  "helmrepositories|flux-system bp-keycloak oci://ghcr.io/openova-io true")
expect "suspended external source" PASS "${got}"

# 5. Other kinds covered too.
got=$(run_case "git-tether" "true" "true" "" \
  "gitrepositories|flux-system upstream https://github.com/openova-io/openova ")
expect "external GitRepository" FAIL "${got}"
got=$(run_case "oci-tether" "true" "true" "" \
  "ocirepositories|flux-system up oci://ghcr.io/openova-io/bp-x ")
expect "external OCIRepository" FAIL "${got}"

# 6. Mothership harbor.openova.io must still fail (no containerd redirect
#    applies to a source object).
got=$(run_case "mothership-harbor" "true" "true" "" \
  "helmrepositories|flux-system bp-x oci://harbor.openova.io/openova-io ")
expect "mothership harbor.openova.io source" FAIL "${got}"

# 7. Port/path-bearing local URL must parse to the bare host and PASS.
got=$(run_case "port-parse" "true" "true" "" \
  "helmrepositories|flux-system bp-x oci://registry.${FQDN}:5000/openova-io/deep/path ")
expect "local host with port and path" PASS "${got}"

# 8. FAIL-CLOSED — an unwritable scratch dir must not report PASS.
echo "  (fail-closed: an unwritable scratch dir must not be reported as PASS)"
got=$(
  case_dir="${TMP}/case"; rm -rf "${case_dir}"; mkdir -p "${case_dir}"
  : >"${case_dir}/helmrepositories.txt"; : >"${case_dir}/gitrepositories.txt"; : >"${case_dir}/ocirepositories.txt"
  export PATH="${TMP}/bin:${PATH}" STUB_DIR="${case_dir}"
  export SOVEREIGN_FQDN="${FQDN}" LOCAL_REGISTRY_HOST="registry.${FQDN}"
  export SOURCE_HOST_LINT_ALLOW="" SOURCE_HOST_LINT_ENABLED="true" CUTOVER_COMPLETE="true"
  export WORK="${TMP}/definitely-not-a-real-dir/nested/deeper"
  cd "${TMP}"; . "${TMP}/fn.sh"
  if run_daytwo_source_host_lint >"${TMP}/out.txt" 2>&1; then echo PASS; else echo FAIL; fi
)
expect "unwritable scratch dir" FAIL "${got}"

# 9. FAIL-CLOSED — an unmeasurable enumeration (auth/connection error, not a
#    missing CRD) must not report PASS.
got=$(
  case_dir="${TMP}/case"; rm -rf "${case_dir}"; mkdir -p "${case_dir}"
  : >"${case_dir}/helmrepositories.txt"; : >"${case_dir}/gitrepositories.txt"; : >"${case_dir}/ocirepositories.txt"
  export PATH="${TMP}/bin:${PATH}" STUB_DIR="${case_dir}"
  export STUB_FAIL_KIND="helmrepositories" STUB_FAIL_MSG="error: Unauthorized"
  export SOVEREIGN_FQDN="${FQDN}" LOCAL_REGISTRY_HOST="registry.${FQDN}"
  export SOURCE_HOST_LINT_ALLOW="" SOURCE_HOST_LINT_ENABLED="true" CUTOVER_COMPLETE="true"
  export WORK="${TMP}/work"
  cd "${TMP}"; . "${TMP}/fn.sh"
  if run_daytwo_source_host_lint >"${TMP}/out.txt" 2>&1; then echo PASS; else echo FAIL; fi
)
expect "unmeasurable enumeration (auth error)" FAIL "${got}"

# 10. VACUITY the other way — a kind that is merely NOT INSTALLED is zero
#     rows, not a tether, and must PASS.
got=$(
  case_dir="${TMP}/case"; rm -rf "${case_dir}"; mkdir -p "${case_dir}"
  printf 'flux-system bp-cilium oci://registry.%s/openova-io \n' "${FQDN}" >"${case_dir}/helmrepositories.txt"
  : >"${case_dir}/gitrepositories.txt"; : >"${case_dir}/ocirepositories.txt"
  export PATH="${TMP}/bin:${PATH}" STUB_DIR="${case_dir}"
  export STUB_FAIL_KIND="ocirepositories" STUB_FAIL_MSG="error: the server doesn't have a resource type \"ocirepositories\""
  export SOVEREIGN_FQDN="${FQDN}" LOCAL_REGISTRY_HOST="registry.${FQDN}"
  export SOURCE_HOST_LINT_ALLOW="" SOURCE_HOST_LINT_ENABLED="true" CUTOVER_COMPLETE="true"
  export WORK="${TMP}/work"
  cd "${TMP}"; . "${TMP}/fn.sh"
  if run_daytwo_source_host_lint >"${TMP}/out.txt" 2>&1; then echo PASS; else echo FAIL; fi
)
expect "uninstalled source kind (not a tether)" PASS "${got}"

echo
echo "[daytwo-source-host-lint] #5759 — the property this lint exists to add: cutoverComplete gating"

# 11. THE DISCRIMINATOR. Pre-cutover, an external host is the NORMAL state
#     (nothing has been pivoted yet) — the SAME offending row that FAILED in
#     case 2 must PASS here purely because cutoverComplete != 'true'. Without
#     this case, enabling this lint by default would false-positive every
#     Sovereign for its entire pre-cutover life.
got=$(run_case "pre-cutover-external-is-normal" "false" "true" "" \
  "helmrepositories|flux-system bp-cilium oci://ghcr.io/openova-io ")
expect "external host, cutoverComplete=false (pre-cutover — normal, must not fire)" PASS "${got}"
grep -q 'DRIFT_STATUS=not-cutover' "${TMP}/drift-status.txt" \
  && pass "DRIFT_STATUS=not-cutover names WHY it did not assert (not merely 'lucky pass')" \
  || fail "expected DRIFT_STATUS=not-cutover, got: $(cat "${TMP}/drift-status.txt")"

# 12. FAIL-CLOSED THE OTHER WAY — an unreadable/empty cutoverComplete must
#     also stay silent (this lint's fail-closed direction is the OPPOSITE of
#     the catalog leg's refuse-by-default: an unmeasurable cutover state must
#     not manufacture a false drift report on a Sovereign that might still be
#     mid-cutover). Same offending row as case 2/11.
got=$(run_case "unreadable-status-cm" "" "true" "" \
  "helmrepositories|flux-system bp-cilium oci://ghcr.io/openova-io ")
expect "external host, cutoverComplete unreadable (must not fire — see #5759 asymmetry note)" PASS "${got}"

# 13. THE SAME OFFENDING ROW, cutoverComplete=true — must now FAIL. This is
#     the case that proves 11/12 are not simply "the lint never fires": one
#     flag flips the verdict on byte-identical input.
got=$(run_case "same-row-post-cutover" "true" "true" "" \
  "helmrepositories|flux-system bp-cilium oci://ghcr.io/openova-io ")
expect "the SAME external row, cutoverComplete=true (must now fire)" FAIL "${got}"

# 14. OPERATOR OPT-OUT — SOURCE_HOST_LINT_ENABLED=false must skip regardless
#     of cutover state or offenders present.
got=$(run_case "operator-disabled" "true" "false" "" \
  "helmrepositories|flux-system bp-cilium oci://ghcr.io/openova-io ")
expect "SOURCE_HOST_LINT_ENABLED=false (operator opt-out)" PASS "${got}"
grep -q 'DRIFT_STATUS=skipped' "${TMP}/drift-status.txt" \
  && pass "DRIFT_STATUS=skipped distinguishes an opt-out from a clean measurement" \
  || fail "expected DRIFT_STATUS=skipped, got: $(cat "${TMP}/drift-status.txt")"

echo
if [ "${FAILURES}" -ne 0 ]; then
  echo "[daytwo-source-host-lint] ${FAILURES} assertion(s) FAILED"
  exit 1
fi
echo "[daytwo-source-host-lint] all assertions passed — lint proven to fail on a post-cutover drift AND pass on a local source, AND (the property unique to this file) to stay silent on the identical drift pre-cutover / unmeasurable-status, flipping only on cutoverComplete=true"
