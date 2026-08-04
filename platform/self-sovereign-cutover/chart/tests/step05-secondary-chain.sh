#!/usr/bin/env bash
# #5359 — behavioural test for step-05's SECONDARY-REGION candidate URL chain.
#
# WHY this exists, and why a render grep would not have been enough.
#
# While building the #5359 fix, a `helm template` render that was structurally
# perfect — every assertion in cutover-contract.sh Case 76 green, every step
# script clean under `sh -n`/`bash -n`/`dash -n` — still contained a log line
# referencing ${sec_url} from BEFORE the candidate loop that defines it. The
# Job runs under `set -eu`, so that unset reference did not print a wrong URL:
# it ABORTED the secondary leg on the first region, before any pivot, on every
# 2-region Sovereign. Syntax checks cannot see it (the syntax is valid) and
# render greps cannot see it (the string is present, just in the wrong scope).
# Only EXECUTING the extracted script under the same shell options exposes it.
#
# So this suite drives the real chain, extracted from the render, against a
# stub kubectl, under `set -eu` — and asserts the three outcomes that matter:
# converge on the first candidate, fall through to the second, and fail loud
# when the chain is exhausted.
#
# The scenario is the live hw292 one (dep 1c56518035a83e03): the in-cluster
# Gitea URL never converges from region-b (generation=2, observedGeneration=1,
# "pkt-line 3: EOF" — its own empty Gitea answers that name), so the leg must
# fall through to the Sovereign's own external Gitea door.
set -uo pipefail

CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)"
FQDN="hw292.omani.works"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0

fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

MESH_URL="http://gitea-http.gitea.svc.cluster.local:3000/openova/openova"
DOOR_URL="https://gitea.${FQDN}/openova/openova"

helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" >"${TMP}/render.yaml" 2>"${TMP}/render.err" || {
  echo "helm template failed:"; cat "${TMP}/render.err"; exit 1
}

# Extract the secondary leg from the RENDER (never a copy pasted here — #5646
# showed a suite validating a hand-kept copy that had drifted from the source).
awk '/#5359 CANDIDATE URL CHAIN/,/echo "\[flux-gitrepository-patch\] done"/' "${TMP}/render.yaml" \
  | sed 's/^            //' >"${TMP}/chain.sh"

if ! grep -q 'for skc in /secondary-kubeconfigs/\*.yaml; do' "${TMP}/chain.sh"; then
  echo "FAIL — could not extract the step-05 secondary leg from the render."
  echo "       The leg is missing, or the candidate-chain marker comment changed."
  exit 1
fi
if ! grep -q 'flux-gitrepository-patch] done' "${TMP}/chain.sh"; then
  echo "FAIL — the extracted leg is truncated (no trailing done marker); every"
  echo "       case below would be driving a fragment."
  exit 1
fi

mkdir -p "${TMP}/skc"
: >"${TMP}/skc/me-east-215-b-1.yaml"
sed -i "s#/secondary-kubeconfigs#${TMP}/skc#g" "${TMP}/chain.sh"

# The fallback URL is read OUT OF THE RENDER, not hardcoded here. A suite that
# supplied its own SECONDARY_GITEA_FALLBACK_URL would keep passing after the
# chart stopped rendering one — it would be proving that a two-element chain
# behaves correctly while shipping a one-element chain. Mutation-checked:
# setting secondaryRegions.giteaFallbackToPublicDoor=false must turn this red.
RENDERED_FALLBACK=$(grep -A1 'name: SECONDARY_GITEA_FALLBACK_URL' "${TMP}/render.yaml" \
  | grep 'value:' | head -1 | sed 's/.*value: *//' | tr -d '"')
if [ -z "${RENDERED_FALLBACK}" ]; then
  echo "FAIL — the chart renders no SECONDARY_GITEA_FALLBACK_URL, so a secondary"
  echo "       that cannot reach the in-cluster Gitea name has no second candidate"
  echo "       and the #5359 pivot stays cosmetic (secondaryRegions."
  echo "       giteaFallbackToPublicDoor / giteaFallbackURL)."
  exit 1
fi
if [ "${RENDERED_FALLBACK}" != "${DOOR_URL}" ]; then
  echo "FAIL — SECONDARY_GITEA_FALLBACK_URL renders '${RENDERED_FALLBACK}',"
  echo "       expected the Sovereign's own door '${DOOR_URL}'."
  exit 1
fi
echo "[step05-secondary-chain] fallback candidate from the render: ${RENDERED_FALLBACK}"

# ── Harness ─────────────────────────────────────────────────────────────────
# The stub kubectl answers the two reads the leg makes. CONVERGE_ON names the
# URL whose fetch "succeeds": for that one it reports generation ==
# observedGeneration; for every other candidate it reports the hw292 shape
# (generation=2, observedGeneration=1) with Ready=True — deliberately Ready,
# because a stale Ready=True is exactly what the old predicate accepted and
# this suite must prove the new one is not fooled by it.
make_harness() {
  cat >"${TMP}/hdr.sh" <<HDR
set -eu
LOCAL_GITEA_URL="${MESH_URL}"
SECONDARY_GITEA_URL=""
SECONDARY_GITEA_FALLBACK_URL="${RENDERED_FALLBACK}"
SECONDARY_GITREPO_READY_SECONDS=1
GITREPO_NAME=openova
GITREPO_NAMESPACE=flux-system
BRANCH=main
SOVEREIGN_FQDN=${FQDN}
GIT_AUTH_SECRET_NAME=cutover-gitea-git-auth
CUTOVER_NAMESPACE=catalyst
STATUS_CONFIGMAP=self-sovereign-cutover-status
CONVERGE_ON="\$1"
CUR=""
STATUS_LOG="${TMP}/status.log"
: > "\${STATUS_LOG}"
# The leg re-checks every 10s inside each candidate window. This suite is
# testing the DECISION, not the timing, so sleep is a no-op — otherwise the
# non-converging cases would take minutes of real wall-clock and the suite
# would be too slow to keep in the per-PR gate. The bounded window itself is
# still honoured: SECONDARY_GITREPO_READY_SECONDS above closes it.
sleep() { return 0; }
kubectl() {
  case "\$*" in
    *"patch gitrepository"*)
      CUR=\$(grep -m1 '^  url:' /tmp/patch-secondary.yaml | sed 's/^  url: //')
      return 0 ;;
    *"patch configmap"*)
      printf '%s\n' "\$*" >> "\${STATUS_LOG}"; return 0 ;;
    *observedGeneration*)
      if [ "\${CUR}" = "\${CONVERGE_ON}" ]; then printf 'True|2|2|%s' "\${CUR}"
      else printf 'True|2|1|%s' "\${CUR}"; fi
      return 0 ;;
    *'.message}'*)
      echo "unable to list remote for '\${CUR}': pkt-line 3: EOF"; return 0 ;;
  esac
  return 0
}
HDR
  cat "${TMP}/hdr.sh" "${TMP}/chain.sh" >"${TMP}/run.sh"
}

# Runs the leg under a given shell; echoes "<exit> <chosen-url-or-none>".
run_chain() {
  local shell="$1" converge_on="$2"
  make_harness
  local out rc
  out=$("${shell}" "${TMP}/run.sh" "${converge_on}" 2>&1); rc=$?
  printf '%s\n' "${out}" >"${TMP}/out.txt"
  local chosen="none"
  case "${out}" in
    *"CONVERGED on ${DOOR_URL} "*) chosen="${DOOR_URL}" ;;
    *"CONVERGED on ${MESH_URL} "*) chosen="${MESH_URL}" ;;
  esac
  echo "${rc} ${chosen}"
}

echo "[step05-secondary-chain] #5359 — the secondary leg must execute under set -eu and walk its candidate chain"

# Every case runs under all three shells the Job image might provide. The
# regression this suite was written for was shell-option-dependent, not
# syntax-dependent, so a single-shell run would not have caught it.
for SH in sh dash bash; do
  command -v "${SH}" >/dev/null 2>&1 || { echo "  (skip ${SH}: not installed)"; continue; }

  # 1. THE REGRESSION. Before the fix this aborted with "sec_url: parameter not
  #    set" the moment the leg entered its per-region loop — no pivot attempted,
  #    on every 2-region Sovereign. Any outcome other than a clean walk of the
  #    chain means an unset variable or an early -e exit crept back in.
  got=$(run_chain "${SH}" "${MESH_URL}")
  if [ "${got}" = "0 ${MESH_URL}" ]; then
    pass "${SH}: first candidate converges -> exit 0 on the mesh URL"
  else
    fail "${SH}: first-candidate case returned '${got}', expected '0 ${MESH_URL}'"
    sed 's/^/        /' "${TMP}/out.txt"
  fi

  # 2. THE LIVE hw292 PATH. The mesh URL reports a STALE Ready=True (gen 2 /
  #    obs 1) — the exact state the old predicate accepted — so the leg must
  #    reject it and fall through to the Sovereign's own door.
  got=$(run_chain "${SH}" "${DOOR_URL}")
  if [ "${got}" = "0 ${DOOR_URL}" ]; then
    pass "${SH}: stale Ready=True on candidate 1 is rejected, chain falls through to the Sovereign door"
  else
    fail "${SH}: fallthrough case returned '${got}', expected '0 ${DOOR_URL}'"
    sed 's/^/        /' "${TMP}/out.txt"
  fi

  # 3. FAIL-LOUD. No candidate converges -> non-zero, so the step fails and
  #    cutoverComplete stays false. A soft skip here IS the #5359 defect.
  got=$(run_chain "${SH}" "no-candidate-matches-this")
  case "${got}" in
    "1 none") pass "${SH}: exhausted chain exits 1 (cutoverComplete stays false)" ;;
    *) fail "${SH}: exhausted-chain case returned '${got}', expected '1 none'"
       sed 's/^/        /' "${TMP}/out.txt" ;;
  esac
done

# 4. The FATAL must name the diagnosis, not just the verdict — an operator
#    reading the Job log has to know region-b is serving pre-cutover content.
run_chain sh "no-candidate-matches-this" >/dev/null
if grep -q 'still serving its pre-cutover artifact' "${TMP}/out.txt"; then
  pass "the exhausted-chain FATAL explains the consequence, not just the failure"
else
  fail "the exhausted-chain FATAL does not explain what being unconverged means"
fi

# 5. The per-region status keys must record the FAILURE, not silently vanish.
if grep -q 'step.flux-gitrepository-patch.region.me-east-215-b-1.result.*failed' "${TMP}/status.log"; then
  pass "a failed region stamps result=failed on the status ConfigMap"
else
  fail "no result=failed status key was written for the failed region"
  sed 's/^/        /' "${TMP}/status.log"
fi

# 6. VACUITY CONTROL for case 5 — a SUCCEEDING run must stamp success plus the
#    URL that won, or case 5 would also pass for a leg that always writes
#    'failed'.
run_chain sh "${DOOR_URL}" >/dev/null
if grep -q 'result.*success' "${TMP}/status.log" && grep -qF "giteaURL" "${TMP}/status.log"; then
  pass "a converged region stamps result=success and records which candidate won"
else
  fail "a converged region did not stamp success + giteaURL"
  sed 's/^/        /' "${TMP}/status.log"
fi

echo
if [ "${FAILURES}" -ne 0 ]; then
  echo "[step05-secondary-chain] ${FAILURES} assertion(s) FAILED"
  exit 1
fi
echo "[step05-secondary-chain] all assertions passed — the secondary leg executes cleanly under set -eu, rejects a stale Ready=True, falls through to the Sovereign's own Gitea door, and fails loud when no candidate converges"
