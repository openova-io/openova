#!/usr/bin/env bash
# #5596 (Refs #5359 #6490 #6754) — behavioural test for the step-06 SECONDARY-
# region DURABLE rewrite.
#
# WHAT #5596 FIXES. Since #6490 each secondary region's Flux consumes that
# region's OWN in-cluster Gitea, which the step-01 secondary mirror fills from
# UPSTREAM GitHub. Step-06 Phase 2 pushed the bootstrap-kit rewrite (ghcr ->
# local Harbor + certSecretRef) to region-A's Gitea ONLY, so region-B's
# bootstrap-kit Kustomization re-applied the 65 literal ghcr URLs on every
# reconcile and the live patch never survived. hw305 (dep b2b00ce4c833badf)
# looped for 10 days:
#   #5359 secondary me-east-215-b-1: pivot ok=65 skip=0 fail=0
#   #5596 ... poked 1 HR-owning Kustomization(s) + flux-system/openova ...
#   FATAL: #5596 ... 63 HelmRepository(ies) on oci://ghcr.io/openova-io after the pivot
# The fix factors the Phase-2 transform into ONE ConfigMap key (kit-rewrite.sh),
# and — BEFORE the live patch loop — stamps an in-region Job in each secondary
# that applies it to that region's OWN Gitea clone and pushes; region-A then
# converges the region's GitRepository onto that sha before the #5596 assert.
#
# Three kinds of evidence, each vacuity-proofed:
#   (a) RENDER GATING — every secondary-leg token is ABSENT from the single-region
#       render and PRESENT with mirrorToSecondaryGitea=true (paired, so neither
#       side can pass on an empty render); the shared kit-rewrite.sh is present
#       on BOTH (region-A sources it) and the injector awk exists exactly once
#       (#5646 one-copy).
#   (b) ORDER — in the region-A args the durable rewrite is called BEFORE the
#       secondary's live HelmRepository enumeration and BEFORE
#       assert_secondary_pivot_durable, and the function is defined before
#       pivot_secondary_regions; the in-region Job carries the kyverno
#       flux-managed escape (#6493), the shared-PAT secretKeyRef (#6754) and the
#       region-LOCAL gitea-http host.
#   (c) EXECUTION — the REAL rendered secondary-pivot.sh + kit-rewrite.sh bytes
#       run against a REAL bare git origin (file://) holding a bootstrap-kit
#       fixture; the verdict is read out of the bare origin's main, never out of
#       a working tree the script left behind. Idempotent re-run, the
#       existing-catalog-block path (the Phase-2.5 WARN class), and two mutants
#       (push dropped / rewrite dropped) that MUST go red.
#
# Usage: bash tests/step5596-secondary-durable-rewrite.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
FQDN="hw306.omani.works"
UP="oci://ghcr.io/openova-io"
LOCAL="oci://registry.${FQDN}/openova-io"
OLD_LOCAL="oci://registry.hw305.omantel.biz/openova-io"
TRUST="cutover-harbor-ca"
REGION="me-east-215-b-1"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

render() { # $1=extra --set args (space-sep), $2=outfile
  # shellcheck disable=SC2086
  helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" $1 >"$2" 2>"${TMP}/render.err" || {
    echo "helm template failed ($1):"; cat "${TMP}/render.err"; exit 1
  }
}
render "--set secondaryRegions.mirrorToSecondaryGitea=true" "${TMP}/on.yaml"
render "" "${TMP}/off.yaml"

echo "[step5596] (a) render gating — single-region carries NONE of the secondary leg, 2-region carries ALL of it"
for tok in \
  'cutover-secondary-pivot-script' \
  'secondary-pivot.sh' \
  'durable_rewrite_secondary' \
  'cutover-helmrepo-pivot-secondary' \
  'PIVOT_JOB_WAIT_SECONDS' \
  'secondary-helmrepo-pivot' \
  'main-sha='; do
  if grep -qF "${tok}" "${TMP}/off.yaml"; then
    fail "OFF render contains '${tok}' — the leg is not fully gated (single-region NOT a no-op)"
  else
    pass "OFF render is free of '${tok}'"
  fi
  if grep -qF "${tok}" "${TMP}/on.yaml"; then
    pass "ON render contains '${tok}' (the OFF-absence check is non-vacuous)"
  else
    fail "ON render is missing '${tok}' — the fix did not render with the flag on"
  fi
done
for f in off on; do
  if grep -qF '. /kit-rewrite/kit-rewrite.sh' "${TMP}/${f}.yaml" && grep -qF 'rewrite_kit_tree "${target_dir}"' "${TMP}/${f}.yaml"; then
    pass "${f^^} render: region-A Phase 2 sources kit-rewrite.sh and calls rewrite_kit_tree (the shared transform is always-on)"
  else
    fail "${f^^} render: region-A Phase 2 no longer sources/calls the shared kit-rewrite.sh"
  fi
  n=$(grep -c 'inject_trust_ref() {' "${TMP}/${f}.yaml" || true)
  if [ "${n}" = "1" ]; then pass "${f^^} render: exactly one inject_trust_ref definition (#5646 one-copy)"; else fail "${f^^} render: ${n} inject_trust_ref definitions (want 1, #5646)"; fi
  n=$(grep -c 'awk -v ref=' "${TMP}/${f}.yaml" || true)
  if [ "${n}" = "1" ]; then pass "${f^^} render: exactly one trust-injector awk (#5646 one-copy)"; else fail "${f^^} render: ${n} trust-injector awk copies (want 1, #5646)"; fi
done

echo "[step5596] (b) region-A ordering + the in-region Job's contract"
python3 - "${TMP}/on.yaml" "${TMP}/off.yaml" "${TMP}" "${REGION}" "${LOCAL}" "${TRUST}" <<'PY' || FAILURES=$((FAILURES + 1))
import sys, yaml
on, off, tmp, region, local, trust = sys.argv[1:7]
bad = 0
def say(ok, msg):
    global bad
    print(("  ok   — " if ok else "  FAIL — ") + msg)
    if not ok: bad += 1
def step06(path):
    for d in yaml.safe_load_all(open(path)):
        if d and d.get("kind") == "ConfigMap" and d["metadata"]["name"] == "cutover-step-06-helmrepository-patches":
            return d
    sys.exit("step-06 ConfigMap not found in " + path)
def args_of(cm):
    pod = yaml.safe_load(cm["data"]["podSpec"])
    c = [x for x in pod["containers"] if x["name"] == "helmrepository-patches"][0]
    return c["args"][0], c, pod
cm_on = step06(on); a, c, pod = args_of(cm_on)
i_def  = a.find("durable_rewrite_secondary() {")
i_pvt  = a.find("pivot_secondary_regions() {")
i_call = a.find('if ! durable_rewrite_secondary "${pv_kc}" "${pv_region}"; then')
i_enum = a.find("> /tmp/sec_hrs.txt")
i_asrt = a.find('if ! assert_secondary_pivot_durable "${pv_kc}" "${pv_region}"; then')
say(0 < i_def < i_pvt, "durable_rewrite_secondary is defined before pivot_secondary_regions")
say(0 < i_call < i_enum, "the durable rewrite runs BEFORE the secondary's live HelmRepository enumeration")
say(0 < i_call < i_asrt, "the durable rewrite runs BEFORE assert_secondary_pivot_durable (#5596)")
say(i_pvt < i_call, "the call sits inside pivot_secondary_regions (per region)")
say("lastHandledReconcileAt" in a[i_def:i_pvt] and 'status.artifact.revision' in a[i_def:i_pvt],
    "region-A gates the secondary GitRepository on token-handled + artifact revision, not on Ready alone")
say("secondary_source_diagnosis" in a[i_def:i_pvt], "the convergence FATAL carries the GitRepository diagnosis")
say("/kit-rewrite/kit-rewrite.sh" in a[i_def:i_pvt] and "/secondary-pivot/secondary-pivot.sh" in a[i_def:i_pvt],
    "the region-A leg distributes BOTH the pivot script and the shared kit-rewrite.sh into the secondary")
say("kit-rewrite.sh" in cm_on["data"], "kit-rewrite.sh is a key of the step-06 ConfigMap")
say(any(m["mountPath"] == "/kit-rewrite" for m in c["volumeMounts"]) and any(m["mountPath"] == "/secondary-pivot" for m in c["volumeMounts"]),
    "step-06 mounts /kit-rewrite and /secondary-pivot")
env = {e["name"]: e for e in c["env"]}
for k in ("SECONDARY_GITEA_NAMESPACE", "SECONDARY_GITEA_PAT_SECRET", "PAT_SOURCE_NAMESPACE", "PIVOT_JOB_WAIT_SECONDS", "SECONDARY_GITREPO_READY_SECONDS"):
    say(k in env, f"step-06 env carries {k}")
# OFF: no call, no mount, but the shared key + mount stay.
cm_off = step06(off); a2, c2, _ = args_of(cm_off)
say("durable_rewrite_secondary" not in a2, "OFF render: region-A args carry no secondary durable-rewrite call")
say(not any(m["mountPath"] == "/secondary-pivot" for m in c2["volumeMounts"]), "OFF render: no /secondary-pivot mount")
say(any(m["mountPath"] == "/kit-rewrite" for m in c2["volumeMounts"]), "OFF render: /kit-rewrite mount is always-on")
# the in-region Job manifest, after the runtime substitutions the leg performs
cm = [d for d in yaml.safe_load_all(open(on)) if d and d.get("kind") == "ConfigMap" and d["metadata"]["name"] == "cutover-secondary-pivot-script"][0]
raw = cm["data"]["secondary-pivot-job.yaml"].replace("__REGION__", region).replace("__LOCAL_PREFIX__", local).replace("__TRUST_SECRET__", trust)
job = yaml.safe_load(raw)
say(job["kind"] == "Job" and job["metadata"]["name"] == f"cutover-helmrepo-pivot-secondary-{region}", "Job manifest parses after __REGION__/__LOCAL_PREFIX__/__TRUST_SECRET__ substitution")
say(job["metadata"]["labels"].get("app.kubernetes.io/managed-by") == "flux", "in-region Job carries app.kubernetes.io/managed-by: flux (kyverno flux-managed escape, #6493)")
say(job["metadata"]["namespace"] == "gitea", "in-region Job is stamped in the secondary's gitea namespace")
jc = job["spec"]["template"]["spec"]["containers"][0]
jenv = {e["name"]: e for e in jc["env"]}
say(jenv["GITEA_PAT"].get("valueFrom", {}).get("secretKeyRef", {}).get("name") == "cutover-secondary-gitea-pat", "in-region Job reads the SHARED PAT via secretKeyRef cutover-secondary-gitea-pat (#6754)")
say("gitea-http.gitea.svc" in jenv["GITEA_INTERNAL_URL"]["value"] and "gitea." + "hw306" not in jenv["GITEA_INTERNAL_URL"]["value"], "in-region Job pushes to the region-LOCAL gitea-http Service, not the cross-region door")
say(jenv["LOCAL_PREFIX"]["value"] == local and jenv["TRUST_SECRET"]["value"] == trust, "LOCAL_PREFIX / TRUST_SECRET are substituted at run time")
say(jc["command"] == ["/bin/sh", "/secondary-pivot/secondary-pivot.sh"], "in-region Job runs the distributed secondary-pivot.sh")
say(job["spec"]["activeDeadlineSeconds"] == 900 and job["spec"].get("backoffLimit") == 2, "in-region Job deadline/backoff render from values")
open(f"{tmp}/secondary-pivot.sh", "w").write(cm["data"]["secondary-pivot.sh"])
open(f"{tmp}/kit-rewrite.sh", "w").write(cm_on["data"]["kit-rewrite.sh"])
sys.exit(1 if bad else 0)
PY

echo "[step5596] (c) EXECUTE the real secondary-pivot.sh + kit-rewrite.sh against a real bare origin"
mkdir -p "${TMP}/sp" "${TMP}/home" "${TMP}/gitea/openova"
cp "${TMP}/secondary-pivot.sh" "${TMP}/kit-rewrite.sh" "${TMP}/sp/"

seed_origin() { # $1 = mode: fresh | oldblock
  rm -rf "${TMP}/gitea/openova/openova.git" "${TMP}/seed"
  git init -q --bare "${TMP}/gitea/openova/openova.git"
  git init -q -b main "${TMP}/seed"
  mkdir -p "${TMP}/seed/clusters/_template/bootstrap-kit"
  cat >"${TMP}/seed/clusters/_template/bootstrap-kit/19-harbor.yaml" <<EOF
apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: bp-harbor
  namespace: flux-system
spec:
  type: oci
  interval: 10m
  url: ${UP}
  secretRef:
    name: ghcr-pull
EOF
  cat >"${TMP}/seed/clusters/_template/bootstrap-kit/03-flux.yaml" <<EOF
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: unrelated
spec:
  url: https://github.com/openova-io/openova
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: bp-flux
  namespace: flux-system
spec:
  type: oci
  url: ${UP}
EOF
  if [ "$1" = "oldblock" ]; then
    cat >"${TMP}/seed/clusters/_template/bootstrap-kit/13-bp-catalyst-platform.yaml" <<EOF
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-catalyst-platform
  labels:
    catalyst.openova.io/sovereign: x
spec:
  values:
    global:
      a: b
    catalog:
      helmRepository:
        url: ${OLD_LOCAL}
    sovereign:
      orgEmail: a@b
EOF
  else
    cat >"${TMP}/seed/clusters/_template/bootstrap-kit/13-bp-catalyst-platform.yaml" <<EOF
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-catalyst-platform
  labels:
    catalyst.openova.io/sovereign: x
spec:
  values:
    global:
      a: b
    sovereign:
      orgEmail: a@b
EOF
  fi
  ( cd "${TMP}/seed" && git add -A && git -c user.name=t -c user.email=t@t commit -q -m seed && git push -q "${TMP}/gitea/openova/openova.git" main:main )
}
run_pivot() { # $1 = script dir; prints rc; log at ${TMP}/run.log
  ( cd "${TMP}" && env -i PATH="${PATH}" HOME="${TMP}/home" TMPDIR="${TMP}" \
      REGION="${REGION}" UPSTREAM_BRANCH=main GITEA_INTERNAL_URL="file://${TMP}/gitea" \
      GITEA_ORG=openova GITEA_REPO=openova GITEA_USERNAME=gitea_admin GITEA_PAT=deadbeefcafe \
      UPSTREAM_PREFIX="${UP}" LOCAL_PREFIX="${LOCAL}" TRUST_SECRET="${TRUST}" SECONDARY_PIVOT_DIR="$1" \
      sh "$1/secondary-pivot.sh" >"${TMP}/run.log" 2>&1 )
  echo $?
}
origin_show() { git --git-dir="${TMP}/gitea/openova/openova.git" show "main:clusters/_template/bootstrap-kit/$1"; }
origin_main() { git --git-dir="${TMP}/gitea/openova/openova.git" rev-parse main; }

seed_origin fresh
before=$(origin_main)
rc=$(run_pivot "${TMP}/sp")
if [ "${rc}" = "0" ]; then pass "fresh origin: secondary-pivot.sh exits 0"; else fail "fresh origin: secondary-pivot.sh exit ${rc}"; sed 's/^/      /' "${TMP}/run.log" | tail -20; fi
after=$(origin_main)
[ "${after}" != "${before}" ] && pass "a pivot commit landed on the bare origin's main" || fail "origin main did not move — nothing was pushed"
if grep -q "^\[secondary-pivot\] main-sha=${after}$" "${TMP}/run.log"; then pass "the Job log reports main-sha=<origin main> (what region-A gates the GitRepository on)"; else fail "main-sha line missing or wrong"; grep 'main-sha' "${TMP}/run.log" || true; fi
h=$(origin_show 19-harbor.yaml)
grep -q "^  url: ${LOCAL}$" <<<"${h}" && pass "19-harbor.yaml url pivoted to ${LOCAL} in the origin" || fail "19-harbor.yaml still not pivoted in the origin"
grep -qF "${UP}" <<<"${h}" && fail "19-harbor.yaml still carries ${UP}" || pass "19-harbor.yaml carries no upstream prefix"
[ "$(grep -c '^  certSecretRef:$' <<<"${h}")" = "1" ] && grep -q "^    name: ${TRUST}$" <<<"${h}" && pass "certSecretRef ${TRUST} injected exactly once at the url indent" || fail "certSecretRef injection wrong: $(grep -c certSecretRef <<<"${h}") occurrence(s)"
f=$(origin_show 03-flux.yaml)
grep -q '^  url: https://github.com/openova-io/openova$' <<<"${f}" && pass "an unrelated GitRepository url in the same file is untouched" || fail "unrelated GitRepository url was rewritten"
[ "$(grep -c certSecretRef <<<"${f}")" = "1" ] && pass "only the pivoted HelmRepository in a multi-doc file received certSecretRef" || fail "certSecretRef count in 03-flux.yaml = $(grep -c certSecretRef <<<"${f}")"
c=$(origin_show 13-bp-catalyst-platform.yaml)
grep -q "^        url: ${LOCAL}$" <<<"${c}" && grep -q '^    catalog:$' <<<"${c}" && pass "Phase-2.5 injected the catalog.helmRepository.url block above sovereign:" || fail "Phase-2.5 block missing from 13-bp-catalyst-platform.yaml"
grep -q 'Phase-2.5 OK: injected' "${TMP}/run.log" && pass "log: Phase-2.5 took the inject path on a fresh tree" || fail "log lacks 'Phase-2.5 OK: injected'"

# idempotent re-run: no new commit, exit 0, still exactly one certSecretRef
rc=$(run_pivot "${TMP}/sp")
[ "${rc}" = "0" ] && pass "re-run exits 0" || { fail "re-run exit ${rc}"; sed 's/^/      /' "${TMP}/run.log" | tail -20; }
[ "$(origin_main)" = "${after}" ] && pass "re-run pushed nothing (origin main unchanged)" || fail "re-run moved origin main — the transform is not idempotent"
grep -q 'already pivoted' "${TMP}/run.log" && pass "re-run logs the idempotent path" || fail "re-run did not report the idempotent path"
grep -q "^\[secondary-pivot\] main-sha=${after}$" "${TMP}/run.log" && pass "re-run still reports main-sha=<origin main>" || fail "re-run main-sha wrong"
[ "$(origin_show 19-harbor.yaml | grep -c certSecretRef)" = "1" ] && pass "re-run left exactly one certSecretRef" || fail "re-run duplicated certSecretRef"
grep -q 'Phase-2.5 WARN' "${TMP}/run.log" && fail "re-run hit the Phase-2.5 WARN path (the hw305 class)" || pass "re-run did NOT hit the Phase-2.5 WARN path"
grep -q 'Phase-2.5 OK: rewrote existing' "${TMP}/run.log" && pass "re-run rewrote the existing catalog block in place" || fail "re-run did not take the existing-block path"

# existing block at an OLD local prefix (a re-cutover / harbor host change)
seed_origin oldblock
rc=$(run_pivot "${TMP}/sp")
[ "${rc}" = "0" ] && pass "old-block origin: exits 0" || { fail "old-block origin: exit ${rc}"; sed 's/^/      /' "${TMP}/run.log" | tail -20; }
c=$(origin_show 13-bp-catalyst-platform.yaml)
grep -q "^        url: ${LOCAL}$" <<<"${c}" && pass "existing catalog block url rewritten ${OLD_LOCAL} -> ${LOCAL}" || fail "existing catalog block url NOT rewritten"
grep -qF "${OLD_LOCAL}" <<<"${c}" && fail "old prefix still present in 13-bp-catalyst-platform.yaml" || pass "old prefix gone"
[ "$(grep -c '^    catalog:$' <<<"${c}")" = "1" ] && pass "no duplicate catalog block" || fail "catalog block duplicated"

echo "[step5596] (M) mutants — each MUST turn a case red (vacuity proof)"
mkdir -p "${TMP}/m1" "${TMP}/m2"
cp "${TMP}/kit-rewrite.sh" "${TMP}/m1/"; cp "${TMP}/kit-rewrite.sh" "${TMP}/m2/"
# m1: the push is dropped — the self-verify (remote sha != local sha) must FATAL.
sed -E 's#^( *)if ! push_err=\$\(git .* push origin .*$#\1if ! push_err=$(true 2>\&1); then#' "${TMP}/secondary-pivot.sh" >"${TMP}/m1/secondary-pivot.sh"
grep -q 'push origin' "${TMP}/m1/secondary-pivot.sh" && fail "m1 mutant did not apply" || pass "m1 mutant applied (push dropped)"
seed_origin fresh
rc=$(run_pivot "${TMP}/m1")
[ "${rc}" != "0" ] && pass "m1: a dropped push is caught (exit ${rc}, self-verify)" || fail "m1: dropped push went unnoticed — the self-verify is vacuous"
# m2: the rewrite is dropped — the zero-upstream assert must FATAL before any push.
sed -E 's#^( *)rewrite_kit_tree "\$\{target_dir\}"$#\1:#' "${TMP}/secondary-pivot.sh" >"${TMP}/m2/secondary-pivot.sh"
grep -q 'rewrite_kit_tree "${target_dir}"' "${TMP}/m2/secondary-pivot.sh" && fail "m2 mutant did not apply" || pass "m2 mutant applied (rewrite dropped)"
seed_origin fresh
before=$(origin_main)
rc=$(run_pivot "${TMP}/m2")
[ "${rc}" != "0" ] && pass "m2: a dropped rewrite is caught (exit ${rc}, zero-upstream assert)" || fail "m2: dropped rewrite went unnoticed"
[ "$(origin_main)" = "${before}" ] && pass "m2: nothing was pushed to the origin" || fail "m2: an unpivoted tree was pushed"

echo
if [ "${FAILURES}" -eq 0 ]; then
  echo "[step5596] all assertions passed — single-region renders none of the leg; 2-region stamps an in-region Job that pushes the SAME kit rewrite into the secondary's own Gitea before the live patch + durable assert; the real script bytes pivot, inject trust, handle the existing catalog block, are idempotent, and both mutants go red."
  exit 0
fi
echo "[step5596] ${FAILURES} assertion(s) FAILED"
exit 1
