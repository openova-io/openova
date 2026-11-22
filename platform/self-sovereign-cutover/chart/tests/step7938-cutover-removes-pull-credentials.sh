#!/usr/bin/env bash
# #7938 — the last step of the chain removes, in every region, the upstream
# registry pull credential the cutover copied, before it may report success.
#
# Step 03's follower leg (#6764, its step (2)) copies each follower's ghcr-pull
# into <secondaryHarborNamespace>/<secondaryGhcrSecretName>, so that the prewarm
# Job it starts in that region can read a ghcr.io credential. Step 06 strips the
# parent's registry auth from ghcr-pull (and Reflector carries the strip to every
# mirror), but the copy is no mirror and nothing stripped or deleted it: on
# hw307, region b still held harbor/cutover-secondary-ghcr after
# cutoverComplete=true. Only a later release delivery (#7942) deleted it.
#
# Evidence, each part able to fail:
#   (a) the Secret step 03 copies is the one the follower Job reads (both read
#       from the render); the step that removes it is the last of the chain and
#       runs after step 03, the only step that starts that Job;
#   (b) EXECUTION of the rendered tail of the last step (from its new-failures
#       gate to its end) for a primary and two followers, against a kubectl stub
#       that keeps each region's Secrets as files: on the PASSED path and on the
#       enforceCIDRBlock=false path the step ends with status 0 and no region
#       holds the copy, while every credential for the Sovereign's own registry
#       (ghcr-pull and its mirrors, step 11's package-pull Secret, harbor-admin)
#       is still there; a second run on the clean state passes too;
#   (c) a follower whose API does not answer: the step ends non-zero, names that
#       region, prints no PASSED, releases no reboot hold, and still deletes the
#       copy in every other region;
#   (d) the runner's ClusterRole grants the delete the primary's removal needs.
# On the tree before the removal, (b) and (c) fail.
#
# Usage: bash tests/step7938-cutover-removes-pull-credentials.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

helm template ssc "${CHART_DIR}" --set sovereign.fqdn=t01.omani.works \
  --set secondaryRegions.prewarmSecondaryHarbor=true >"${TMP}/render.yaml" 2>"${TMP}/render.err" || {
  echo "helm template failed:"; cat "${TMP}/render.err"; exit 1
}

# What the render says: the chain's order, the copy step 03 writes, what the
# follower Job reads, step 11's own-registry pull Secret, the last step's tail.
python3 - "${TMP}/render.yaml" "${TMP}" <<'PY' || { echo "  FAIL — the inputs cannot be read from the render"; exit 1; }
import sys, yaml
render, tmp = sys.argv[1], sys.argv[2]
docs = [d for d in yaml.safe_load_all(open(render)) if isinstance(d, dict)]
steps = {d["data"]["stepName"]: d for d in docs
         if d.get("kind") == "ConfigMap" and d["metadata"].get("labels", {}).get("app.kubernetes.io/component") == "cutover-step"}
order = {n: int(d["metadata"]["labels"]["bp.openova.io/cutover-order"]) for n, d in steps.items()}
last = max(order, key=order.get)
def env(step):
    spec = yaml.safe_load(steps[step]["data"]["podSpec"])
    return {e["name"]: e.get("value") for c in spec["containers"] for e in c.get("env", [])}
e03, e11 = env("harbor-prewarm"), env("crossplane-provider-pivot")
[script] = [d for d in docs if d.get("kind") == "ConfigMap" and d["metadata"]["name"] == "cutover-secondary-harbor-prewarm-script"]
job = yaml.safe_load(script["data"]["secondary-harbor-prewarm-job.yaml"])
reads = [e["valueFrom"]["secretKeyRef"]["name"] for e in job["spec"]["template"]["spec"]["containers"][0]["env"]
         if e["name"] == "GHCR_DOCKERCONFIG"]
spec = yaml.safe_load(steps[last]["data"]["podSpec"])
lines = spec["containers"][0]["args"][0].rstrip("\n").split("\n")
start = next(i for i, l in enumerate(lines) if l.strip() == 'if [ "${new_failures}" -gt 0 ]; then')
open(tmp + "/tail.sh", "w").write("\n".join(lines[start:]) + "\n")
with open(tmp + "/facts.txt", "w") as f:
    f.write("%s %d %d %d\n" % (last, order[last], order["harbor-prewarm"], len(order)))
    f.write("%s %s\n" % (e03["SECONDARY_HARBOR_NAMESPACE"], e03["SECONDARY_GHCR_DEST_SECRET"]))
    f.write("%s %s\n" % (job["metadata"]["namespace"], " ".join(reads) or "-"))
    f.write("%s %s\n" % (e11["CROSSPLANE_NAMESPACE"], e11["PULL_SECRET_NAME"]))
rules = [r for d in docs if d.get("kind") == "ClusterRole" for r in d.get("rules") or []]
ok = any("" in (r.get("apiGroups") or []) and "secrets" in (r.get("resources") or []) and "delete" in (r.get("verbs") or [])
         and not r.get("resourceNames") for r in rules)
open(tmp + "/rbac.txt", "w").write("yes\n" if ok else "no\n")
PY
{ read -r LAST LAST_ORDER PREWARM_ORDER NSTEPS; read -r COPY_NS COPY_NAME; read -r JOB_NS JOB_READS; read -r XP_NS XP_NAME; } <"${TMP}/facts.txt"
COPY="${COPY_NS}/${COPY_NAME}"

echo "[step7938-creds] (a) the copy, its reader, and the step that removes it"
if [ "${JOB_NS}" = "${COPY_NS}" ] && [ "${JOB_READS}" = "${COPY_NAME}" ]; then
  pass "step 03 copies ${COPY} in each follower, and the follower prewarm Job reads exactly that Secret"
else
  fail "step 03 copies ${COPY}, the follower Job runs in ${JOB_NS} and reads ${JOB_READS}: the inventory below would test another Secret"
fi
if [ "${LAST}" = "egress-block-test" ] && [ "${PREWARM_ORDER}" -lt "${LAST_ORDER}" ]; then
  pass "egress-block-test is the last of the ${NSTEPS} steps (cutover-order ${LAST_ORDER}), after harbor-prewarm (${PREWARM_ORDER}), which waits for the follower Job to end"
else
  fail "the last step is ${LAST} (order ${LAST_ORDER}), harbor-prewarm has order ${PREWARM_ORDER}: this test runs the tail of the wrong step"
fi

# The stub: every call is recorded as <region>|<args>; a region's Secrets are
# files STATE/<region>/<namespace>/<name>; a region in UNREACHABLE answers nothing.
mkdir -p "${TMP}/bin" "${TMP}/kc"
cat >"${TMP}/bin/kubectl" <<'STUB'
#!/bin/sh
region=primary
[ "$1" = "--kubeconfig" ] && { region=$(basename "$2" .yaml); shift 2; }
printf '%s|%s\n' "${region}" "$*" >>"${RECORD}"
case " ${UNREACHABLE:-} " in *" ${region} "*) echo "Unable to connect to the server: dial tcp 10.0.9.1:6443: i/o timeout" >&2; exit 1;; esac
ns=default; name=""; prev=""; verb=""; ignore=no
for a in "$@"; do
  [ "${prev}" = "-n" ] && ns="$a"
  [ "${prev}" = "delete" ] && [ "$a" = "secret" ] && verb=delete-secret
  [ "${prev}" = "secret" ] && [ "${verb}" = delete-secret ] && [ -z "${name}" ] && name="$a"
  [ "$a" = "--ignore-not-found=true" ] && ignore=yes
  prev="$a"
done
if [ "${verb}" = delete-secret ]; then
  f="${STATE}/${region}/${ns}/${name}"
  if [ -e "${f}" ]; then rm -f "${f}"; echo "secret \"${name}\" deleted"; exit 0; fi
  [ "${ignore}" = yes ] && exit 0
  echo "Error from server (NotFound): secrets \"${name}\" not found" >&2; exit 1
fi
exit 0
STUB
chmod +x "${TMP}/bin/kubectl"
REGIONS="primary me-east-215-b-1 me-east-215-c-1"
OWN="flux-system/ghcr-pull vcluster-system/ghcr-pull harbor/ghcr-pull catalyst/ghcr-pull harbor/harbor-admin"
: >"${TMP}/kc/me-east-215-b-1.yaml"; : >"${TMP}/kc/me-east-215-c-1.yaml"
seed() { # every region: the own-registry Secrets and the copy (a region that once ran as a follower holds one too)
  rm -rf "${TMP}/state"
  for r in ${REGIONS}; do
    for s in ${OWN} "${COPY}"; do mkdir -p "${TMP}/state/${r}/${s%/*}"; : >"${TMP}/state/${r}/${s}"; done
  done
  mkdir -p "${TMP}/state/primary/${XP_NS}"; : >"${TMP}/state/primary/${XP_NS}/${XP_NAME}"
}
sed "s#/secondary-kubeconfigs/#${TMP}/kc/#g" "${TMP}/tail.sh" >"${TMP}/tail-local.sh"
run_tail() { # $1 POLICY_APPLIED, $2 ENFORCE_CIDR_BLOCK, $3 regions that do not answer
  : >"${TMP}/record.txt"
  RECORD="${TMP}/record.txt" STATE="${TMP}/state" UNREACHABLE="$3" PATH="${TMP}/bin:${PATH}" sh -c "set -eu
new_failures=0
POLICY_APPLIED='$1'
ENFORCE_CIDR_BLOCK='$2'
DURATION_SECONDS=600
secondary_kubeconfig_count=2
ws_tally='cloud Workspaces: 0 of 0 judged'
NAMESPACE=catalyst
STATUS_CONFIGMAP=self-sovereign-cutover-status
$(cat "${TMP}/tail-local.sh")" >"${TMP}/out.txt" 2>&1
}
holders() { for r in ${REGIONS}; do [ -e "${TMP}/state/${r}/${COPY}" ] && printf ' %s' "${r}"; done; }
own_missing() {
  for r in ${REGIONS}; do for s in ${OWN}; do [ -e "${TMP}/state/${r}/${s}" ] || printf ' %s/%s' "${r}" "${s}"; done; done
  [ -e "${TMP}/state/primary/${XP_NS}/${XP_NAME}" ] || printf ' primary/%s/%s' "${XP_NS}" "${XP_NAME}"
}

echo "[step7938-creds] (b) the rendered tail of the last step, run for three regions"
for path in "yes true sovereignty proof PASSED" "_ false survival poll, NOT the ADR-0002 sovereignty proof"; do
  set -- ${path}; applied="$1"; enforce="$2"; shift 2; marker="$*"
  [ "${applied}" = "_" ] && applied=""
  seed
  if run_tail "${applied}" "${enforce}" ""; then
    pass "POLICY_APPLIED='${applied}' enforceCIDRBlock=${enforce}: the step ends with status 0"
  else
    fail "POLICY_APPLIED='${applied}' enforceCIDRBlock=${enforce}: the step ended non-zero: $(tail -n 3 "${TMP}/out.txt")"
  fi
  grep -qF "${marker}" "${TMP}/out.txt" || fail "the step did not reach its '${marker}' line: $(tail -n 3 "${TMP}/out.txt")"
  left="$(holders)"
  if [ -z "${left}" ]; then
    pass "after the last step no region holds ${COPY}"
  else
    for r in ${left}; do fail "after the last step, region ${r} still holds ${COPY}, the upstream pull credential step 03 copied"; done
  fi
  missing="$(own_missing)"
  if [ -z "${missing}" ]; then pass "every credential for the Sovereign's own registry is still there"; else fail "the step removed own-registry credentials:${missing}"; fi
  said="$(grep -c "region .*: holds no ${COPY} " "${TMP}/out.txt")"
  if [ "${said}" = 3 ]; then pass "the log says so once per region"; else fail "the log says so for ${said} of 3 regions: $(grep -c . "${TMP}/out.txt") lines of output"; fi
done
if [ "$(grep -c " delete secret ${COPY_NAME} " "${TMP}/record.txt")" = 3 ] && ! grep -q ' delete ' <<<"$(grep -v " delete secret ${COPY_NAME} " "${TMP}/record.txt")"; then
  pass "one delete of ${COPY_NAME} per region and no other delete"
else
  fail "the deletes differ from one ${COPY_NAME} per region: $(grep ' delete ' "${TMP}/record.txt" | tr '\n' ';')"
fi
if run_tail yes true ""; then rc=0; else rc=$?; fi
if [ "${rc}" = 0 ] && [ -z "$(holders)" ]; then pass "a second run, with nothing left to delete, ends with status 0"; else fail "a second run: status ${rc}, regions holding ${COPY}:$(holders)"; fi

echo "[step7938-creds] (c) a follower whose API does not answer"
seed
if run_tail yes true "me-east-215-b-1"; then
  fail "the step ended with status 0 while me-east-215-b-1 could not be reached: cutoverComplete would be set with ${COPY} left there"
else
  if grep -q "FATAL.*${COPY}.*me-east-215-b-1" "${TMP}/out.txt"; then pass "the step fails and its FATAL names ${COPY} and the region me-east-215-b-1"; else fail "the step failed without naming the credential and the region: $(tail -n 3 "${TMP}/out.txt")"; fi
fi
grep -q 'sovereignty proof PASSED' "${TMP}/out.txt" && fail "the step printed PASSED with a credential left" || pass "no PASSED line"
grep -q ' create configmap ' "${TMP}/record.txt" && fail "the reboot hold was released although the chain did not complete" || pass "no reboot hold released"
left="$(holders)"
if [ "${left}" = " me-east-215-b-1" ]; then pass "the copy is deleted in every region that answered"; else fail "regions still holding ${COPY}:${left:- none} (want only me-east-215-b-1)"; fi

echo "[step7938-creds] (d) the runner may delete the primary's copy"
if [ "$(cat "${TMP}/rbac.txt")" = yes ]; then pass "the ClusterRole grants delete on secrets"; else fail "no ClusterRole rule grants delete on secrets: the primary's removal would be refused"; fi

echo
if [ "${FAILURES}" -gt 0 ]; then echo "step7938-cutover-removes-pull-credentials: ${FAILURES} failure(s)"; exit 1; fi
echo "step7938-cutover-removes-pull-credentials: all cases passed"
