#!/usr/bin/env bash
# The #3526 HelmRepository pivot check of step egress-block-test re-reconciles the
# org-tenants GitRepository and Kustomization within its bound of 300 s, and when it
# fails each offender names the Flux Kustomization that owns it.
#
# Why: on 21 Nov 2026 (two-region install) step 08 failed three Pods on
# OFFENDER flux-system/bp-agenity, bp-openclaw, bp-stalwart-tenant and
# bp-wordpress-tenant=oci://ghcr.io/openova-io. The Kustomization org-tenants applied
# them from clusters/<fqdn>/org-tenants/helmrepositories.yaml, which catalyst-api had
# written with the public catalog for an Organization created during the cutover. The
# check poked only gitrepository/openova and kustomization/bootstrap-kit, and its
# failure did not say who writes the objects back. catalyst-api now re-renders that
# file before this step; the check gives Flux the chance to apply it within the
# bound, and says who owns whatever is left.
#
# Evidence, each part able to fail: the check is cut out of the RENDERED step and run
# under sh with set -eu against a stand-in kubectl and a stand-in clock:
#   (a) the owner's source is fixed: the offenders turn local only after
#       gitrepository/openova-org-tenants and kustomization/org-tenants were
#       re-reconciled; the check passes within the bound;
#   (b) never fixed: fails after the bound (300 s, at most one poll more), each
#       OFFENDER line carries owner=flux-system/org-tenants, the FAIL line names
#       the writer of the org-tenants tree, each wait prints the time left;
#   (c) an offender no Flux Kustomization applies still fails the check;
#   (d) the rendered check keeps its bound, its exclusions and its exit 1;
#   (e) the pair it pokes is the one the values name: rendered with other names, step
#       08's Job sets ORG_TENANTS_GITREPO and ORG_TENANTS_KUSTOMIZATION from
#       helmRepositories.orgTenantsSource (the GitRepository as step 06 does), and the
#       check converges only by poking those. Before the repair the Job set neither,
#       the script took its defaults, and (e) fails.
#
# Usage: bash tests/step08-pivot-names-owner.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

helm template ssc "${CHART_DIR}" --set sovereign.fqdn=t01.omani.works >"${TMP}/render.yaml" 2>"${TMP}/render.err" || {
  echo "helm template failed:"; cat "${TMP}/render.err"; exit 1
}

# The check: from "count_offenders() {" to the line that reports convergence,
# dedented by the indent of its first line.
awk '
  !on && $0 ~ /^ *count_offenders\(\) \{$/ { on = 1; match($0, /^ */); ind = RLENGTH }
  on { print substr($0, ind + 1) }
  on && index($0, "HelmRepository pivot converged") { exit }
' "${TMP}/render.yaml" >"${TMP}/check.sh"
if ! grep -q 'HelmRepository pivot converged' "${TMP}/check.sh"; then
  echo "  FAIL — the rendered step holds no #3526 pivot check (count_offenders .. converged)"; exit 1
fi

echo "[step08-pivot-owner] (d) the rendered check keeps its bound, exclusions and exit"
grep -q -F 'pivot_deadline=$(( $(date +%s) + 300 ))' "${TMP}/check.sh" && pass "the bound is 300 s" || fail "the bound is not 300 s"
grep -q -F "grep -vE '/(bp-newapi|bp-self-sovereign-cutover)='" "${TMP}/check.sh" \
  && pass "the exclusions are bp-newapi and bp-self-sovereign-cutover, as before" || fail "the exclusion list changed"
grep -q -F 'owner={.metadata.labels.kustomize\.toolkit\.fluxcd\.io/namespace}/{.metadata.labels.kustomize\.toolkit\.fluxcd\.io/name}' "${TMP}/check.sh" \
  && pass "each offender line carries its Kustomization's labels" || fail "the jsonpath does not read the kustomize.toolkit labels"
grep -q -x -F '  exit 1' "${TMP}/check.sh" && pass "a surviving offender still exits 1" || fail "the check no longer exits 1"

# The stand-ins. HelmRepositories follow FIXTURE and whether the org-tenants pair was poked.
cat >"${TMP}/harness.sh" <<'HARNESS'
set -eu
NOW=1000
date() { echo "${NOW}"; }
sleep() { NOW=$((NOW + $1)); }
trap 'echo "RESULT elapsed=$((NOW - 1000))"' EXIT
poked() { grep -q -x -F "$1" "${POKES}" 2>/dev/null; }
row() { printf 'flux-system/%s=%s owner=%s\n' "$1" "$2" "$3"; }
ghcr=oci://ghcr.io/openova-io
own=oci://registry.t01.omani.works/openova-io
kubectl() {
  case "$*" in
    "get helmrepositories.source.toolkit.fluxcd.io -A -o jsonpath="*)
      url="${ghcr}"
      if [ "${FIXTURE}" = fixed ] && poked "gitrepository/${GIT}" && poked "kustomization/${KS}"; then url="${own}"; fi
      for n in bp-agenity bp-openclaw bp-stalwart-tenant bp-wordpress-tenant; do row "${n}" "${url}" flux-system/org-tenants; done
      row bp-cilium "${own}" flux-system/bootstrap-kit
      row bp-newapi "${ghcr}" flux-system/bootstrap-kit
      if [ "${FIXTURE}" = unowned ]; then row bp-by-hand "${ghcr}" /; fi ;;
    "annotate --overwrite gitrepository openova -n flux-system "*|"annotate --overwrite kustomization bootstrap-kit -n flux-system "*) : ;;
    "annotate --overwrite "*/*" -n flux-system reconcile.fluxcd.io/requestedAt="*) echo "$3" >>"${POKES}" ;;
    *) echo "unexpected kubectl $*" >&2; return 1 ;;
  esac
}
. "${CHECK}"
echo "CONVERGED"
HARNESS

# run <case> [fixture]: GIT and KS are the pair whose poke fixes the source (default the
# chart's names); the environment is the caller's.
run() { : >"${TMP}/$1.pokes"; FIXTURE="${2:-$1}" GIT="${GIT:-openova-org-tenants}" KS="${KS:-org-tenants}" CHECK="${TMP}/check.sh" POKES="${TMP}/$1.pokes" sh "${TMP}/harness.sh" >"${TMP}/$1.out" 2>&1; echo $? >"${TMP}/$1.rc"; }
has() { grep -q -F -- "$2" "${TMP}/$1.out"; }
rc() { cat "${TMP}/$1.rc"; }
elapsed() { sed -n 's/^RESULT elapsed=\([0-9]*\)$/\1/p' "${TMP}/$1.out"; }

echo "[step08-pivot-owner] (a) the owner's source is fixed: the check pokes it and passes"
run fixed
[ "$(rc fixed)" = 0 ] && has fixed CONVERGED && pass "it converged" || fail "it did not converge (rc $(rc fixed)): $(tail -3 "${TMP}/fixed.out" | tr '\n' ' ')"
grep -q -x -F "kustomization/org-tenants" "${TMP}/fixed.pokes" && pass "kustomization/org-tenants was re-reconciled" || fail "the org-tenants Kustomization was never poked"
grep -q -x -F "gitrepository/openova-org-tenants" "${TMP}/fixed.pokes" && pass "gitrepository/openova-org-tenants was re-reconciled" || fail "the org-tenants GitRepository was never poked"
e="$(elapsed fixed)"; [ -n "${e}" ] && [ "${e}" -lt 300 ] && pass "within the bound (${e} s)" || fail "not within the bound (${e:-?} s)"

echo "[step08-pivot-owner] (b) never fixed: fails after the bound and names the owner"
run never
[ "$(rc never)" = 1 ] && pass "it failed with exit 1" || fail "it did not fail with exit 1 (rc $(rc never))"
e="$(elapsed never)"; [ -n "${e}" ] && [ "${e}" -ge 300 ] && [ "${e}" -le 315 ] && pass "after the bound and at most one poll more (${e} s)" || fail "not after the 300 s bound (${e:-?} s)"
for n in bp-agenity bp-openclaw bp-stalwart-tenant bp-wordpress-tenant; do
  has never "OFFENDER flux-system/${n}=oci://ghcr.io/openova-io owner=flux-system/org-tenants" || fail "the OFFENDER line of ${n} does not name its owner"
done
has never "OFFENDER flux-system/bp-agenity=oci://ghcr.io/openova-io owner=flux-system/org-tenants" && pass "each OFFENDER line names its owner" || true
has never "owner=flux-system/org-tenants: catalyst-api writes clusters/<fqdn>/org-tenants/helmrepositories.yaml" \
  && pass "the FAIL line names the writer of the org-tenants tree" || fail "the FAIL line does not name the org-tenants writer"
has never "(deadline in 285s)" && pass "each wait prints the time left, not the deadline's epoch" || fail "the wait does not print the time left"
if has never "OFFENDER flux-system/bp-newapi" || has never "OFFENDER flux-system/bp-cilium"; then fail "an excluded or local HelmRepository is an offender"; else pass "the excluded and the local ones are not offenders"; fi

echo "[step08-pivot-owner] (c) an offender no Kustomization applies still fails"
run unowned
[ "$(rc unowned)" = 1 ] && has unowned "OFFENDER flux-system/bp-by-hand=oci://ghcr.io/openova-io owner=/" && pass "the unowned offender fails the check" || fail "an unowned offender did not fail the check (rc $(rc unowned))"

echo "[step08-pivot-owner] (e) renamed in the values: step 08's Job carries the names and the check pokes them"
helm template ssc "${CHART_DIR}" --set sovereign.fqdn=t01.omani.works \
  --set helmRepositories.orgTenantsSource.gitRepositoryName=org-git-x --set helmRepositories.orgTenantsSource.kustomizationName=org-ks-x \
  >"${TMP}/renamed.yaml" 2>"${TMP}/render.err" || { echo "helm template failed:"; cat "${TMP}/render.err"; exit 1; }
envof() { yq "select(.kind == \"ConfigMap\" and .metadata.name == \"$1\") | .data[\"podSpec\"]" "${TMP}/renamed.yaml" \
  | yq ".containers[0].env[] | select(.name == \"$2\") | .value"; }
e08_git="$(envof cutover-step-08-egress-block-test ORG_TENANTS_GITREPO)"
e08_ks="$(envof cutover-step-08-egress-block-test ORG_TENANTS_KUSTOMIZATION)"
e06_git="$(envof cutover-step-06-helmrepository-patches ORG_TENANTS_GITREPO)"
[ "${e08_git}" = org-git-x ] && [ "${e08_ks}" = org-ks-x ] && [ "${e06_git}" = org-git-x ] \
  && pass "step 08 sets ORG_TENANTS_GITREPO=org-git-x and ORG_TENANTS_KUSTOMIZATION=org-ks-x, step 06 the same GitRepository" \
  || fail "step 08 sets ORG_TENANTS_GITREPO='${e08_git}' ORG_TENANTS_KUSTOMIZATION='${e08_ks}' (step 06: '${e06_git}'), want org-git-x and org-ks-x"
GIT=org-git-x KS=org-ks-x ORG_TENANTS_GITREPO="${e08_git}" ORG_TENANTS_KUSTOMIZATION="${e08_ks}" run renamed fixed
[ "$(rc renamed)" = 0 ] && has renamed CONVERGED && grep -q -x -F "kustomization/org-ks-x" "${TMP}/renamed.pokes" && grep -q -x -F "gitrepository/org-git-x" "${TMP}/renamed.pokes" \
  && pass "with the Job's environment the check pokes org-git-x and org-ks-x and converges" \
  || fail "with the Job's environment the check did not converge by poking org-git-x and org-ks-x (rc $(rc renamed))"

if [ "${FAILURES}" -gt 0 ]; then
  echo "step08-pivot-names-owner: ${FAILURES} check(s) failed"
  for f in fixed never unowned renamed; do echo "--- ${f}"; cat "${TMP}/${f}.out" 2>/dev/null; echo "pokes:"; cat "${TMP}/${f}.pokes" 2>/dev/null; done
  exit 1
fi
echo "step08-pivot-names-owner: all checks passed"
