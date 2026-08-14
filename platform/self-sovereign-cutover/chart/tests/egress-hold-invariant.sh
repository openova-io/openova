#!/usr/bin/env bash
# #6309 — the step-11 deny-egress hold may never produce a PASS it did not earn.
#
# NOTE ON THE FILENAME IT GUARDS. templates/08-egress-block-test-job.yaml is
# `bp.openova.io/cutover-order: "11"` — the "08-" prefix is historical, the step
# runs ELEVENTH and strictly last (tests/cutover-contract.sh #6214 pins that).
# Everything below says step 11 and means that file.
#
# WHY THIS EXISTS. Three facts composed into a false PASS on a live Sovereign:
#
#   1. rbac.yaml granted ciliumclusterwidenetworkpolicies {create,delete,get}
#      in the create rule, and the update/patch rule listed ciliumnetwork-
#      policies ONLY — no clusterwide. The hold IS a CiliumClusterwide-
#      NetworkPolicy.
#   2. The region-A leg applied it with a bare `kubectl apply`, with no
#      delete-before-apply — while the #5359 secondary-region leg in the SAME
#      file already deleted first, for exactly the leaked-policy reason.
#      `kubectl apply` on an existing object is a PATCH, so a re-run (or any
#      run after a SIGKILLed one leaked `cutover-egress-block`) hit the 403.
#   3. The apply failure logged `WARN — assertion-only fallback (fail-safe)`
#      and continued. POLICY_APPLIED stayed empty, which SKIPS the #3678
#      call-home assertion and the #3647 fresh-pull proof — and the survival
#      loop then printed "sovereignty proof PASSED" anyway.
#
# So "the hold held" and "I could not apply the hold" ended at the same verdict
# and the same cutoverComplete=true. ADR-0002 permits no cutover claim without
# the egress-block proof.
#
# WHY THE EXISTING GUARDS DID NOT CATCH IT. tests/rbac-coverage.sh compares the
# RESOURCES the step scripts read against the RESOURCES the ClusterRole grants —
# it never looks at a VERB, so a resource granted `create` but not `patch` reads
# as fully covered. And no suite executed the verdict tail at all, so the one
# line that decides whether the word PASSED is printed had never been observed
# under the condition that makes it wrong.
#
# WHAT THIS ASSERTS. Structure from the RENDER (never from a copy kept here —
# #5646), plus the part that matters: it EXECUTES the rendered apply block and
# the rendered verdict block under stubs, in both directions, and requires the
# pre-fix content of each to go RED.
set -uo pipefail

CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)"
FQDN="hw297.omani.works"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0

fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

echo "== #6309 step-11 deny-egress hold — no PASS without an applied hold =="

helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" >"${TMP}/render.yaml" 2>"${TMP}/render.err" || {
  echo "helm template failed:"; cat "${TMP}/render.err"; exit 1
}

# ── Extractor: pull a shell block out of the render by its opening line and the
#    `fi` at the SAME indent. Shared by the live run and the mutation controls,
#    so a control can never be checked by different code than the real case.
extract_block() {   # extract_block <render> <opening-line-substring> <out>
  python3 - "$1" "$2" "$3" <<'PYEOF'
import sys
src, needle, dst = sys.argv[1], sys.argv[2], sys.argv[3]
lines = open(src, encoding="utf-8", errors="replace").read().splitlines()
start = next((i for i, l in enumerate(lines) if needle in l), None)
if start is None:
    sys.exit("open-line not found: %s" % needle)
indent = len(lines[start]) - len(lines[start].lstrip())
end = None
for i in range(start + 1, len(lines)):
    l = lines[i]
    if l.strip() == "fi" and (len(l) - len(l.lstrip())) == indent:
        end = i
        break
if end is None:
    sys.exit("closing `fi` at indent %d not found after %s" % (indent, needle))
open(dst, "w", encoding="utf-8").write("\n".join(lines[start:end + 1]) + "\n")
print("extracted %d lines" % (end - start + 1))
PYEOF
}

# ── A stub kubectl. APPLY_RC controls what `kubectl apply` returns; every other
#    verb succeeds silently, so the block under test can only fail for the one
#    reason the case is about.
cat >"${TMP}/kubectl" <<'EOF'
#!/usr/bin/env bash
for a in "$@"; do
  if [ "$a" = "apply" ]; then
    if [ "${APPLY_RC:-0}" -ne 0 ]; then
      echo "Error from server (Forbidden): ciliumclusterwidenetworkpolicies.cilium.io \"cutover-egress-block\" is forbidden: User \"system:serviceaccount:catalyst:cutover-runner\" cannot patch resource \"ciliumclusterwidenetworkpolicies\" in API group \"cilium.io\" at the cluster scope" >&2
      exit "${APPLY_RC}"
    fi
    echo "ciliumclusterwidenetworkpolicy.cilium.io/cutover-egress-block configured"
    exit 0
  fi
done
exit 0
EOF
chmod +x "${TMP}/kubectl"

# ── The executed blocks run under `set -eu`, the SAME options the shipped Job
#    sets at 08-egress-block-test-job.yaml line 390. This is not decoration: a
#    bare `_x=$(cmd)` that fails is a simple command with a non-zero status, so
#    under errexit the script dies THERE and any diagnosis written below it never
#    runs. Executing the block under laxer options than the container uses would
#    be a harness that cannot reproduce the container's control flow.
SHELL_OPTS='set -eu'

# ── run_apply_block <block-file> <APPLY_RC> — execute the region-A apply block,
#    echo the resulting POLICY_APPLIED, and return the block's exit code.
run_apply_block() {
  local block="$1" rc="$2"
  ( export PATH="${TMP}:${PATH}" APPLY_RC="${rc}"
    mkdir -p "${TMP}/work" && : >"${TMP}/work/egress-deny.yaml"
    cd "${TMP}"
    sh -c "${SHELL_OPTS}
DEFAULT_DENY_EGRESS=true
POLICY_APPLIED=\"\"
$(sed 's|/work/egress-deny.yaml|'"${TMP}"'/work/egress-deny.yaml|g' "${block}")
echo \"POLICY_APPLIED=[\${POLICY_APPLIED}]\"" ) 2>&1
}

# ── run_verdict <block-file> <POLICY_APPLIED> <ENFORCE_CIDR_BLOCK> ───────────
run_verdict() {
  local block="$1" applied="$2" enforce="$3"
  ( sh -c "${SHELL_OPTS}
new_failures=0
POLICY_APPLIED='${applied}'
ENFORCE_CIDR_BLOCK='${enforce}'
DURATION_SECONDS=600
secondary_kubeconfig_count=1
$(cat "${block}")" ) 2>&1
}

# ── Case 1 — vacuity control: the render must carry the step-11 script ───────
if grep -q 'cutover-egress-block' "${TMP}/render.yaml"; then
  pass "render carries the step-11 egress-block script"
else
  fail "render carries no cutover-egress-block — every case below would pass on nothing"
  echo "RESULT: FAIL (${FAILURES})"; exit 1
fi

# ── Case 2 — RBAC: the hold's own kind must be granted update/patch ──────────
# rbac-coverage.sh cannot see this: it asserts resources, never verbs.
python3 - "${TMP}/render.yaml" >"${TMP}/verbs.txt" <<'PYEOF'
import sys, yaml
for doc in yaml.safe_load_all(open(sys.argv[1], encoding="utf-8", errors="replace")):
    if not doc or doc.get("kind") not in ("ClusterRole", "Role"):
        continue
    for rule in doc.get("rules") or []:
        if "ciliumclusterwidenetworkpolicies" in (rule.get("resources") or []):
            for v in rule.get("verbs") or []:
                print(v)
PYEOF
CCNP_VERBS="$(sort -u "${TMP}/verbs.txt" | tr '\n' ' ')"
MISSING_VERBS=""
for v in create delete get patch update; do
  case " ${CCNP_VERBS} " in *" ${v} "*) : ;; *) MISSING_VERBS="${MISSING_VERBS} ${v}" ;; esac
done
if [ -z "${MISSING_VERBS}" ]; then
  pass "ClusterRole grants ciliumclusterwidenetworkpolicies: ${CCNP_VERBS}"
else
  fail "ClusterRole does NOT grant ciliumclusterwidenetworkpolicies verb(s):${MISSING_VERBS} (granted: ${CCNP_VERBS:-none}) — an apply over an existing hold is a PATCH and would 403 (#6309)"
fi

# ── Case 3 — region A must delete before it applies, like the secondary leg ──
extract_block "${TMP}/render.yaml" 'if [ -f /work/egress-deny.yaml ]; then' "${TMP}/apply.sh" >/dev/null || {
  fail "could not extract the region-A apply block from the render"; echo "RESULT: FAIL (${FAILURES})"; exit 1
}
if grep -q 'kubectl delete ciliumclusterwidenetworkpolicy cutover-egress-block --ignore-not-found=true' "${TMP}/apply.sh" \
   && [ "$(grep -n 'kubectl delete ciliumclusterwidenetworkpolicy' "${TMP}/apply.sh" | head -1 | cut -d: -f1)" \
        -lt "$(grep -n 'kubectl apply -f /work/egress-deny.yaml' "${TMP}/apply.sh" | head -1 | cut -d: -f1)" ]; then
  pass "region-A leg deletes a stale cutover-egress-block BEFORE applying"
else
  fail "region-A leg applies without deleting first — a leaked hold turns the apply into a PATCH (#6309)"
fi

# ── Case 4 — apply FAILURE must be fatal, not a WARN that keeps going ───────
OUT_FAIL="$(run_apply_block "${TMP}/apply.sh" 1)"; RC_FAIL=$?
if [ "${RC_FAIL}" -ne 0 ]; then
  pass "apply failure exits non-zero (rc=${RC_FAIL})"
else
  fail "apply failure exited 0 — the step continues toward a PASS it cannot earn (#6309)"
fi
case "${OUT_FAIL}" in
  *"FATAL"*"cutover-egress-block"*) pass "apply-failure message names the policy that could not be applied" ;;
  *) fail "apply-failure message does not name cutover-egress-block: ${OUT_FAIL}" ;;
esac
case "${OUT_FAIL}" in
  *"assertion-only fallback"*) fail "the silent assertion-only fallback is still present (#6309)" ;;
  *) pass "no assertion-only fallback on the apply-failure path" ;;
esac

# ── Case 5 — the SUCCESS direction still works (a guard that only ever ───────
#    reports red is as useless as one that only ever reports green).
OUT_OK="$(run_apply_block "${TMP}/apply.sh" 0)"; RC_OK=$?
if [ "${RC_OK}" -eq 0 ] && printf '%s' "${OUT_OK}" | grep -q 'POLICY_APPLIED=\[yes\]'; then
  pass "a successful apply still sets POLICY_APPLIED=yes and exits 0"
else
  fail "a SUCCESSFUL apply no longer sets POLICY_APPLIED=yes / exits 0 (rc=${RC_OK}): ${OUT_OK}"
fi

# ── Case 6 — THE INVARIANT: no PASS without an applied hold ─────────────────
extract_block "${TMP}/render.yaml" 'if [ "${new_failures}" -gt 0 ]; then' "${TMP}/verdict-head.sh" >/dev/null
# The verdict tail is the whole region from the new_failures gate to the final
# echo; take it verbatim so the executed text is the shipped text.
python3 - "${TMP}/render.yaml" "${TMP}/verdict.sh" <<'PYEOF'
import sys
src, dst = sys.argv[1], sys.argv[2]
lines = open(src, encoding="utf-8", errors="replace").read().splitlines()
start = next(i for i, l in enumerate(lines) if '"${new_failures}" -gt 0' in l)
end = next(i for i in range(start, len(lines)) if 'sovereignty proof PASSED' in lines[i])
open(dst, "w", encoding="utf-8").write("\n".join(lines[start:end + 1]) + "\n")
PYEOF

V_DENIED="$(run_verdict "${TMP}/verdict.sh" "" "true")"; RC_DENIED=$?
if printf '%s' "${V_DENIED}" | grep -q 'sovereignty proof PASSED'; then
  fail "verdict printed 'sovereignty proof PASSED' with POLICY_APPLIED empty — THIS IS the #6309 false PASS"
else
  pass "verdict refuses to print PASSED with POLICY_APPLIED empty"
fi
if [ "${RC_DENIED}" -ne 0 ]; then
  pass "verdict exits non-zero with POLICY_APPLIED empty and enforceCIDRBlock=true (rc=${RC_DENIED})"
else
  fail "verdict exited 0 with no hold applied under enforceCIDRBlock=true — cutoverComplete would flip on an unmeasured proof"
fi

V_OPTOUT="$(run_verdict "${TMP}/verdict.sh" "" "false")"; RC_OPTOUT=$?
if printf '%s' "${V_OPTOUT}" | grep -q 'sovereignty proof PASSED'; then
  fail "verdict called the enforceCIDRBlock=false survival poll a sovereignty proof"
elif [ "${RC_OPTOUT}" -eq 0 ]; then
  pass "explicit enforceCIDRBlock=false opt-out still exits 0, but is never called a proof"
else
  fail "the documented enforceCIDRBlock=false opt-out now fails the step (rc=${RC_OPTOUT}) — that is a different change than #6309 asked for"
fi

V_HELD="$(run_verdict "${TMP}/verdict.sh" "yes" "true")"; RC_HELD=$?
if [ "${RC_HELD}" -eq 0 ] && printf '%s' "${V_HELD}" | grep -q 'sovereignty proof PASSED'; then
  pass "a hold that WAS applied and survived still reports PASSED and exits 0"
else
  fail "an applied+survived hold no longer reports PASSED (rc=${RC_HELD}): ${V_HELD}"
fi

# ── Case 7 — MUTATION CONTROLS: the PRE-FIX text of each block must go RED ──
# A case that has only ever been observed passing is not yet known to be a case.
cat >"${TMP}/prefix-apply.sh" <<'EOF'
if [ -f /work/egress-deny.yaml ]; then
  if kubectl apply -f /work/egress-deny.yaml; then
    POLICY_APPLIED="yes"
    trap 'kubectl delete ciliumclusterwidenetworkpolicy cutover-egress-block --ignore-not-found=true 2>/dev/null || true' TERM EXIT
    echo "[egress-block-test] egressDeny applied"
  else
    echo "  WARN — egressDeny apply failed; assertion-only fallback (fail-safe)"
  fi
fi
EOF
P_OUT="$(run_apply_block "${TMP}/prefix-apply.sh" 1)"; P_RC=$?
if [ "${P_RC}" -eq 0 ] && printf '%s' "${P_OUT}" | grep -q 'assertion-only fallback'; then
  pass "mutation control: the PRE-FIX apply block does swallow the failure (rc=0) — Case 4 can fail"
else
  fail "mutation control did NOT reproduce the pre-fix swallow (rc=${P_RC}) — Case 4 may be untestable: ${P_OUT}"
fi

# ── Case 8 — ERREXIT control: the capture MUST sit in an `if` condition ─────
# The Job runs `set -eu`. Written as a bare `_x=$(kubectl apply …)` the script
# dies AT the assignment and the FATAL diagnosis below it never executes — the
# operator gets a raw kubectl error and no statement of what it means. This
# control runs exactly that shape and requires it to lose the message, which is
# what makes the `if _eb_apply_out=$(…); then` form in the shipped template a
# load-bearing choice rather than a stylistic one.
cat >"${TMP}/errexit-apply.sh" <<'EOF'
if [ -f /work/egress-deny.yaml ]; then
  kubectl delete ciliumclusterwidenetworkpolicy cutover-egress-block --ignore-not-found=true 2>/dev/null || true
  _eb_apply_out=$(kubectl apply -f /work/egress-deny.yaml 2>&1)
  _eb_apply_rc=$?
  printf '%s\n' "${_eb_apply_out}" | sed 's/^/[egress-block-test]   apply: /'
  if [ "${_eb_apply_rc}" -eq 0 ]; then
    POLICY_APPLIED="yes"
  else
    echo "[egress-block-test] FATAL: could not apply the deny-egress hold 'cutover-egress-block' …" >&2
    exit 1
  fi
fi
EOF
E_OUT="$(run_apply_block "${TMP}/errexit-apply.sh" 1)"; E_RC=$?
if [ "${E_RC}" -ne 0 ] && ! printf '%s' "${E_OUT}" | grep -q 'FATAL'; then
  pass "errexit control: the bare-assignment form dies before its own FATAL — the \`if\`-condition capture is load-bearing"
else
  fail "errexit control did not reproduce the set -eu abort (rc=${E_RC}) — Case 4's message assertion may be passing for the wrong reason: ${E_OUT}"
fi

cat >"${TMP}/prefix-verdict.sh" <<'EOF'
if [ "${new_failures}" -gt 0 ]; then
  echo "[egress-block-test] cluster developed NEW NotReady items during the window (any region) — sovereignty proof FAILED"
  exit 1
fi
echo "[egress-block-test] no new regressions during ${DURATION_SECONDS}s window (all $((secondary_kubeconfig_count + 1)) region(s)) — sovereignty proof PASSED"
EOF
PV_OUT="$(run_verdict "${TMP}/prefix-verdict.sh" "" "true")"; PV_RC=$?
if [ "${PV_RC}" -eq 0 ] && printf '%s' "${PV_OUT}" | grep -q 'sovereignty proof PASSED'; then
  pass "mutation control: the PRE-FIX verdict does print PASSED on an empty POLICY_APPLIED — Case 6 can fail"
else
  fail "mutation control did NOT reproduce the pre-fix false PASS (rc=${PV_RC}) — Case 6 may be untestable: ${PV_OUT}"
fi

echo
if [ "${FAILURES}" -eq 0 ]; then
  echo "RESULT: PASS"
else
  echo "RESULT: FAIL (${FAILURES})"
fi
exit $(( FAILURES > 0 ? 1 : 0 ))
