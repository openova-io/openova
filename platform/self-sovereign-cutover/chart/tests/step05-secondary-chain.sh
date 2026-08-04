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
# stub kubectl, under `set -eu`.
#
# 0.1.166 (#5359): the DEFAULT pivot target for a secondary is the Sovereign's
# OWN external Gitea door. The 0.1.151..0.1.165 default tried the in-cluster
# mesh URL first, and hw292 (dep 1c56518035a83e03) proved that candidate can
# NEVER converge from a secondary: both gitea-http Services are headless
# (Cilium global-services cannot span them) and the secondary's own empty
# bp-gitea answers the name — "pkt-line 3: EOF" on every fetch, generation=2 /
# observedGeneration=1 for 22h behind a result=success stamp. The suite pins:
#   * the default chain is the door, and the mesh URL is NOT in it;
#   * the retired mesh-alias machinery stays out of the step;
#   * a stale Ready=True (gen!=obs) is rejected — convergence predicate;
#   * secondaryRegions.giteaURL still pins an explicit single-URL chain;
#   * an empty chain and an exhausted chain both fail LOUD, never soft-skip.
# Each render-level check is mutation-proven against the verbatim pre-0.1.166
# shape, so a check that cannot fail cannot pass vacuously.
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
PIN_URL="https://gitea.pinned.example/openova/openova"

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

# The default candidate is read OUT OF THE RENDER, not hardcoded here. A suite
# that supplied its own SECONDARY_GITEA_FALLBACK_URL would keep passing after
# the chart stopped rendering one. Mutation-checked: setting
# secondaryRegions.giteaFallbackToPublicDoor=false must turn this red.
RENDERED_DOOR=$(grep -A1 'name: SECONDARY_GITEA_FALLBACK_URL' "${TMP}/render.yaml" \
  | grep 'value:' | head -1 | sed 's/.*value: *//' | tr -d '"')
if [ -z "${RENDERED_DOOR}" ]; then
  echo "FAIL — the chart renders no SECONDARY_GITEA_FALLBACK_URL, so the default"
  echo "       secondary chain is EMPTY and every 2-region cutover dies at the"
  echo "       no-candidate FATAL (secondaryRegions.giteaFallbackToPublicDoor /"
  echo "       giteaFallbackURL)."
  exit 1
fi
if [ "${RENDERED_DOOR}" != "${DOOR_URL}" ]; then
  echo "FAIL — SECONDARY_GITEA_FALLBACK_URL renders '${RENDERED_DOOR}',"
  echo "       expected the Sovereign's own door '${DOOR_URL}'."
  exit 1
fi
echo "[step05-secondary-chain] default candidate from the render: ${RENDERED_DOOR}"

# ── Render-level discriminators, each mutation-proven both directions ────────
# check_door_default: the default branch must seed the chain from the DOOR env
# and must never seed it from LOCAL_GITEA_URL (the pre-0.1.166 mesh-first
# shape that hw292 proved structurally unable to converge).
check_door_default() {
  grep -qF 'sec_url_chain="${SECONDARY_GITEA_FALLBACK_URL:-}"' "$1" || return 1
  grep -qF 'sec_url_chain="${LOCAL_GITEA_URL}"' "$1" && return 1
  return 0
}
# check_mesh_retired: the retired ClusterMesh machinery must stay out of the
# leg — the global annotate is inert on a headless Service and the alias apply
# fights the secondary's own bp-gitea for the Service name.
check_mesh_retired() {
  grep -q 'service.cilium.io/global' "$1" && return 1
  grep -q 'gitea-mesh-alias' "$1" && return 1
  return 0
}

if check_door_default "${TMP}/chain.sh"; then
  pass "default chain seeds from the Sovereign door, not LOCAL_GITEA_URL"
else
  fail "the default secondary chain is not door-first (LOCAL_GITEA_URL seeded, or the door assignment is gone)"
fi
if check_mesh_retired "${TMP}/chain.sh"; then
  pass "the retired mesh-alias machinery is absent from the secondary leg"
else
  fail "step-05 still carries service.cilium.io/global / gitea-mesh-alias machinery"
fi

# VACUITY CONTROL — both checks must FAIL on the verbatim pre-0.1.166 shape.
# A discriminator that passes on the defect it exists to catch proves nothing.
sed 's|sec_url_chain="${SECONDARY_GITEA_FALLBACK_URL:-}"|sec_url_chain="${LOCAL_GITEA_URL}"\n  [ -n "${SECONDARY_GITEA_FALLBACK_URL:-}" ] \&\& sec_url_chain="${sec_url_chain} ${SECONDARY_GITEA_FALLBACK_URL}"|' \
  "${TMP}/chain.sh" >"${TMP}/chain-prefix-shape.sh"
if check_door_default "${TMP}/chain-prefix-shape.sh"; then
  fail "vacuity: check_door_default PASSES on the pre-0.1.166 mesh-first chain — the discriminator cannot fail"
else
  pass "vacuity: check_door_default goes red on the pre-0.1.166 mesh-first chain"
fi
{ cat "${TMP}/chain.sh"; printf '%s\n' 'kubectl -n gitea annotate --overwrite service gitea-http "service.cilium.io/global=true" "service.cilium.io/shared=true"'; } \
  >"${TMP}/chain-mesh-shape.sh"
if check_mesh_retired "${TMP}/chain-mesh-shape.sh"; then
  fail "vacuity: check_mesh_retired PASSES with the pre-0.1.166 global-annotate present — the discriminator cannot fail"
else
  pass "vacuity: check_mesh_retired goes red when the global-annotate reappears"
fi

mkdir -p "${TMP}/skc"
: >"${TMP}/skc/me-east-215-b-1.yaml"
sed -i "s#/secondary-kubeconfigs#${TMP}/skc#g" "${TMP}/chain.sh"

# ── Harness ─────────────────────────────────────────────────────────────────
# The stub kubectl answers the reads the leg makes. CONVERGE_ON names the URL
# whose fetch "succeeds": for that one it reports generation ==
# observedGeneration; for every other candidate it reports the hw292 shape
# (generation=2, observedGeneration=1) with Ready=True — deliberately Ready,
# because a stale Ready=True is exactly what the old predicate accepted and
# this suite must prove the current one is not fooled by it.
make_harness() {
  local sec_url="$1" sec_fallback="$2"
  cat >"${TMP}/hdr.sh" <<HDR
set -eu
LOCAL_GITEA_URL="${MESH_URL}"
SECONDARY_GITEA_URL="${sec_url}"
SECONDARY_GITEA_FALLBACK_URL="${sec_fallback}"
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
  # ${4-...} (no colon): an EXPLICIT empty 4th argument must stay empty —
  # case 4 drives the no-candidate path with sec_fallback="" and a :- default
  # would silently re-substitute the door and test the wrong branch.
  local shell="$1" converge_on="$2" sec_url="${3:-}" sec_fallback="${4-${RENDERED_DOOR}}"
  make_harness "${sec_url}" "${sec_fallback}"
  local out rc
  out=$("${shell}" "${TMP}/run.sh" "${converge_on}" 2>&1); rc=$?
  printf '%s\n' "${out}" >"${TMP}/out.txt"
  local chosen="none"
  case "${out}" in
    *"CONVERGED on ${DOOR_URL} "*) chosen="${DOOR_URL}" ;;
    *"CONVERGED on ${PIN_URL} "*)  chosen="${PIN_URL}" ;;
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

  # 1. THE DEFAULT PATH. The chain is the Sovereign's own door and it
  #    converges -> exit 0. This also re-proves the set -eu regression: any
  #    unset variable or early -e exit aborts before the CONVERGED line.
  got=$(run_chain "${SH}" "${DOOR_URL}")
  if [ "${got}" = "0 ${DOOR_URL}" ]; then
    pass "${SH}: default chain converges on the Sovereign door -> exit 0"
  else
    fail "${SH}: default-door case returned '${got}', expected '0 ${DOOR_URL}'"
    sed 's/^/        /' "${TMP}/out.txt"
  fi

  # 2. THE hw292 PREDICATE PROOF. The door reports a STALE Ready=True (gen 2 /
  #    obs 1) — the exact state the pre-0.1.164 predicate accepted — so the leg
  #    must reject it, exhaust the chain, and fail LOUD (exit 1). Convergence,
  #    not Ready, is the predicate.
  got=$(run_chain "${SH}" "no-candidate-matches-this")
  case "${got}" in
    "1 none") pass "${SH}: stale Ready=True (gen!=obs) is rejected and the exhausted chain exits 1" ;;
    *) fail "${SH}: stale-Ready case returned '${got}', expected '1 none'"
       sed 's/^/        /' "${TMP}/out.txt" ;;
  esac

  # 3. OPERATOR PIN. secondaryRegions.giteaURL pins the chain to exactly one
  #    explicit URL (the qaTestEnabled LE-staging path) — the leg must pivot
  #    to it and converge there, not on the door.
  got=$(run_chain "${SH}" "${PIN_URL}" "${PIN_URL}")
  if [ "${got}" = "0 ${PIN_URL}" ]; then
    pass "${SH}: secondaryRegions.giteaURL pins the chain to the operator URL"
  else
    fail "${SH}: operator-pin case returned '${got}', expected '0 ${PIN_URL}'"
    sed 's/^/        /' "${TMP}/out.txt"
  fi

  # 4. EMPTY CHAIN. giteaURL and the door both disabled -> the leg must fail
  #    LOUD at the first region, not soft-skip an unpivoted secondary — a
  #    soft skip here IS the #5359 defect.
  got=$(run_chain "${SH}" "irrelevant" "" "")
  case "${got}" in
    "1 none")
      if grep -q 'has NO candidate pivot URL' "${TMP}/out.txt"; then
        pass "${SH}: an empty candidate chain fails loud naming the values keys"
      else
        fail "${SH}: empty-chain case exited 1 but without the no-candidate diagnosis"
        sed 's/^/        /' "${TMP}/out.txt"
      fi ;;
    *) fail "${SH}: empty-chain case returned '${got}', expected '1 none'"
       sed 's/^/        /' "${TMP}/out.txt" ;;
  esac
done

# 5. The FATAL must name the diagnosis, not just the verdict — an operator
#    reading the Job log has to know region-b is serving pre-cutover content.
run_chain sh "no-candidate-matches-this" >/dev/null
if grep -q 'still serving its pre-cutover artifact' "${TMP}/out.txt"; then
  pass "the exhausted-chain FATAL explains the consequence, not just the failure"
else
  fail "the exhausted-chain FATAL does not explain what being unconverged means"
fi

# 6. The per-region status keys must record the FAILURE, not silently vanish.
if grep -q 'step.flux-gitrepository-patch.region.me-east-215-b-1.result.*failed' "${TMP}/status.log"; then
  pass "a failed region stamps result=failed on the status ConfigMap"
else
  fail "no result=failed status key was written for the failed region"
  sed 's/^/        /' "${TMP}/status.log"
fi

# 7. The empty-chain FATAL must also stamp result=failed + a lastError an
#    operator can act on from the status ConfigMap alone.
run_chain sh "irrelevant" "" "" >/dev/null
if grep -q 'result.*failed' "${TMP}/status.log" && grep -q 'no candidate pivot URL configured' "${TMP}/status.log"; then
  pass "the empty-chain FATAL stamps result=failed with an actionable lastError"
else
  fail "the empty-chain FATAL did not stamp result=failed + lastError on the status ConfigMap"
  sed 's/^/        /' "${TMP}/status.log"
fi

# 8. VACUITY CONTROL for cases 6/7 — a SUCCEEDING run must stamp success plus
#    the URL that won, or those cases would also pass for a leg that always
#    writes 'failed'.
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
echo "[step05-secondary-chain] all assertions passed — the secondary leg executes cleanly under set -eu, defaults to the Sovereign's own Gitea door (mesh machinery retired, mutation-proven), rejects a stale Ready=True, honours the operator pin, and fails loud on an exhausted or empty chain"
