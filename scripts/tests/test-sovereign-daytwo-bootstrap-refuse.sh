#!/usr/bin/env bash
# test-sovereign-daytwo-bootstrap-refuse.sh — #5759 non-vacuity proof.
#
# Drives scripts/sovereign-daytwo-bootstrap.sh through a stub kubectl in TWO
# forms:
#   RED   the REAL pre-#5759 blob, read via `git show origin/main:...` — not
#         retyped, not a synthetic fixture (docs/PRINCIPLES.md A18: a guard
#         proven only against synthetic input can pass while missing the
#         actual incident shape). This is the literal script that reverted
#         62/69 region-a HelmRepositories on hw292 (dep 1c56518035a83e03) on
#         2026-08-06.
#   GREEN the FIXED script in the working tree.
#
# Both are run against an IDENTICAL stub kubectl reporting
# cutoverComplete=true. The assertion is that they DISAGREE: the historical
# script proceeds to stamp Job cutover-daytwo-bootstrap-01 (the force-push
# leg); the fixed script refuses before stamping anything.
#
# Two controls close the vacuity gaps an "always fail" fix would hide behind:
#   - cutoverComplete=false must still exit 0 cleanly (proves the fix is a
#     real condition, not an unconditional failure that happens to also
#     "pass" the RED/GREEN comparison above).
#   - An unreadable/absent cutoverComplete must ALSO refuse (fail-closed).
#
# Usage: bash scripts/tests/test-sovereign-daytwo-bootstrap-refuse.sh
#   Requires `origin` to have `main` fetched (git fetch origin main) so the
#   RED fixture can be read; CI checkouts always satisfy this.

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/../.." && pwd)"
CURRENT="${REPO_ROOT}/scripts/sovereign-daytwo-bootstrap.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "  ok — $*"; }

[ -f "${CURRENT}" ] || fail "working-tree script not found at ${CURRENT}"
chmod +x "${CURRENT}" 2>/dev/null || true

# ── RED fixture: the REAL pre-#5759 blob, from git ─────────────────────────
cd "${REPO_ROOT}"
PRE_FIX_REF="${PRE_FIX_REF:-origin/main}"
if ! git cat-file -e "${PRE_FIX_REF}:scripts/sovereign-daytwo-bootstrap.sh" 2>/dev/null; then
  git fetch origin main --quiet 2>/dev/null || true
fi
git cat-file -e "${PRE_FIX_REF}:scripts/sovereign-daytwo-bootstrap.sh" 2>/dev/null \
  || fail "cannot read ${PRE_FIX_REF}:scripts/sovereign-daytwo-bootstrap.sh — fetch origin/main first"
git show "${PRE_FIX_REF}:scripts/sovereign-daytwo-bootstrap.sh" > "${TMP}/pre-fix.sh"
chmod +x "${TMP}/pre-fix.sh"
grep -q "cutoverComplete is" "${TMP}/pre-fix.sh" \
  || fail "the ${PRE_FIX_REF} blob does not look like the pre-#5759 script (its REQUIRE-cutoverComplete=true die message is absent) — point PRE_FIX_REF at a commit before this fix"
grep -qi "REFUSING (#5759)" "${TMP}/pre-fix.sh" \
  && fail "VACUITY: ${PRE_FIX_REF} already carries the #5759 refusal — re-point PRE_FIX_REF at the commit immediately before this PR to get a genuine pre-fix fixture"

# ── Stub kubectl — answers every call BOTH script shapes make ──────────────
mkdir -p "${TMP}/bin"
cat > "${TMP}/bin/kubectl" <<'KSTUB'
#!/usr/bin/env bash
args="$*"
case "$args" in
  *"config current-context"*) echo "stub-context"; exit 0 ;;
  *"get cm self-sovereign-cutover-status"*) printf '%s' "${STUB_CC:-}"; exit "${STUB_CC_RC:-0}" ;;
  *"get hr bp-self-sovereign-cutover"*) printf '%s' "${STUB_CHART_VER:-0.1.159}"; exit 0 ;;
  *"get cm cutover-step-01-gitea-mirror"*) printf 'STUB_PODSPEC_01'; exit 0 ;;
  *"get cm cutover-step-03-harbor-prewarm"*) printf 'STUB_PODSPEC_03'; exit 0 ;;
  *"get gitrepo openova"*) printf 'aaaa1111'; exit 0 ;;
  *"delete job"*) exit 0 ;;
  *"apply -f -"*) echo "JOB_STAMPED $(date +%s)" >> "${STUB_ACTION_LOG}"; cat >/dev/null; exit 0 ;;
  *"wait --for=condition=complete"*) exit 0 ;;
  *) exit 0 ;;
esac
KSTUB
chmod +x "${TMP}/bin/kubectl"
touch "${TMP}/kubeconfig"

run_script() {   # run_script <script-path>  -> sets RC, OUT; logs to $TMP/action.log
  local script="$1"
  STUB_ACTION_LOG="${TMP}/action.log"; : > "${STUB_ACTION_LOG}"
  OUT=$(STUB_CC="${STUB_CC:-}" STUB_CC_RC="${STUB_CC_RC:-0}" STUB_CHART_VER="${STUB_CHART_VER:-0.1.159}" \
    STUB_ACTION_LOG="${STUB_ACTION_LOG}" PATH="${TMP}/bin:${PATH}" \
    bash "${script}" --kubeconfig "${TMP}/kubeconfig" --apply 2>&1)
  RC=$?
}

echo "[5759-refuse] RED: the REAL pre-fix script (${PRE_FIX_REF}) on a cutoverComplete=true Sovereign"
STUB_CC="true"; run_script "${TMP}/pre-fix.sh"
echo "${OUT}" | sed 's/^/    /'
echo "    exit=${RC}"
grep -q "JOB_STAMPED" "${TMP}/action.log" \
  || fail "RED did not reproduce: the pre-fix script did not attempt to stamp a Job on cutoverComplete=true — either the stub is wrong or ${PRE_FIX_REF} is not the pre-#5759 blob"
pass "RED confirmed — the pre-#5759 script stamps cutover-daytwo-bootstrap-01 on a cutoverComplete=true Sovereign, reproducing the exact hw292 defect shape (62/69 HelmRepositories reverted, #5759)"

echo "[5759-refuse] GREEN: the FIXED script (working tree) on the SAME cutoverComplete=true Sovereign"
STUB_CC="true"; run_script "${CURRENT}"
echo "${OUT}" | sed 's/^/    /'
echo "    exit=${RC}"
[ "${RC}" = "1" ] || fail "GREEN: expected exit 1 (refuse), got ${RC}"
grep -qi "#5759" <<<"${OUT}" || fail "GREEN: refusal message does not cite #5759"
grep -qi "REFUS" <<<"${OUT}" || fail "GREEN: no explicit refusal language in the output"
grep -q "JOB_STAMPED" "${TMP}/action.log" \
  && fail "GREEN: the fixed script stamped a Job anyway — the refusal did not actually stop the mutating path"
pass "GREEN confirmed — the fixed script refuses (exit 1), cites #5759, and never reaches the Job-stamping path"

echo "[5759-refuse] CONTROL 1 (vacuity): cutoverComplete=false must still be a clean no-op"
STUB_CC="false"; run_script "${CURRENT}"
echo "${OUT}" | sed 's/^/    /'
echo "    exit=${RC}"
[ "${RC}" = "0" ] \
  || fail "CONTROL 1: a genuinely pre-cutover Sovereign (cutoverComplete=false) must exit 0, got ${RC} — an 'always fail' fix would also pass the GREEN assertion above, which is exactly what this control exists to catch"
grep -q "JOB_STAMPED" "${TMP}/action.log" \
  && fail "CONTROL 1: stamped a Job on a pre-cutover Sovereign — that was never in scope for this script either"
pass "CONTROL 1 confirmed — cutoverComplete=false exits 0 cleanly; the refusal is a real condition, not a vacuous always-fail"

echo "[5759-refuse] CONTROL 2 (fail-closed): an unreadable/absent cutoverComplete must ALSO refuse"
STUB_CC=""; STUB_CC_RC=1; run_script "${CURRENT}"
STUB_CC_RC=0
echo "${OUT}" | sed 's/^/    /'
echo "    exit=${RC}"
[ "${RC}" = "1" ] || fail "CONTROL 2: an unreadable status ConfigMap must refuse (exit 1), got ${RC} — 'could not confirm this is safe' must never default to proceeding"
pass "CONTROL 2 confirmed — an unreadable cutoverComplete refuses rather than defaulting to safe"

echo "[5759-refuse] ALL PASS — RED (real pre-fix blob stamps a Job) -> GREEN (fixed script refuses) -> 2 controls"
