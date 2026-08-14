#!/usr/bin/env bash
# #5439 — step-09 Phase-3 (per-Organization vCluster registry pivot) and the
# step-11 pre-hold lint that proves it held.
#
# THE DEFECT THIS LOCKS OUT
# ─────────────────────────
# Step-09 (vcluster-registry-pivot, cutover-order 9) pivoted exactly FOUR named
# HelmReleases in ONE namespace: bp-mgmt-vcluster, bp-rtz-vcluster,
# bp-dmz-vcluster, bp-sso-bridge, all in flux-system. The per-Organization
# vCluster HelmRelease is a fifth shape it structurally could not reach — it is
# named `vcluster`, it lives in the Org's own namespace, there is one PER
# ORGANIZATION, and its durable source is a third repo class,
# `<slug>/catalyst-tenant@main:vcluster/vcluster.yaml`.
#
# Measured live on hw292, cutoverComplete=true since 2026-08-03T08:12:04Z:
#
#   kubectl -n uatco get hr vcluster -o json | jq -r .spec.values
#     "image":{"registry":"harbor.openova.io"
#     "statefulSet":{"image":{"registry":"harbor.openova.io"
#     "coredns":{...:"harbor.openova.io/proxy-dockerhub/coredns/..."}
#
# WHY NOTHING WENT RED, which is the part that matters. The step-11 pod-spec
# ref-host lint EXEMPTS harbor.openova.io as a host step-04's containerd
# redirect covers, and the Flux source lint reads only HelmRepository /
# GitRepository / OCIRepository spec.url — never a HelmRelease's spec.values. So
# a live mothership tether and `cutoverComplete=true` coexisted. A green that
# overstates is worse than a red, and that is the class this suite exists for.
#
# WHAT IS REAL HERE AND WHAT IS STUBBED, AND WHY
# ──────────────────────────────────────────────
# REAL: git, and the shipped bytes. Every case executes the REAL rendered
# Phase-3 region against REAL local git repositories with real branches, and
# reads its verdict out of the BARE origin (`git show main:<path>`), never out
# of a working tree the script left behind — pushed-or-it-did-not-happen. Both
# the phase and the lint are sliced out of `helm template` output rather than
# copied into this file: #5646 showed a suite validating a hand-kept copy that
# had silently drifted from the shipped source and went on passing.
#
# STUBBED: kubectl and curl, because the defect classes here are "which
# Organizations did you enumerate", "did you tell a failed read apart from an
# empty estate" and "did you wait for the reverting reconciler" — all decided by
# how the script INTERPRETS the API's answers, not by the API. A stub can be
# made to FAIL on demand, which a live cluster cannot.
#
# `sleep` is stubbed to a fixed 0.2s so the bounded read-back's timing logic
# still runs (it is wall-clock driven off `date`, not off the sleep) without
# spending 15s of CI per poll.
#
# PROVEN ABLE TO FAIL: Section G re-runs the two verdict-bearing cases against
# MUTANTS of the shipped bytes — the vacuity witness deleted, the read-back
# deleted — and requires this suite to go RED on each. A guard that has only
# ever been observed passing is not yet known to be a guard.
set -uo pipefail

CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)"
FQDN="hw292.omani.works"
TARGET_HOST="harbor.${FQDN}"
MOTHERSHIP="harbor.openova.io"
REPO="catalyst-tenant"
TMP="$(mktemp -d)"
REAL_GIT="$(command -v git)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0

bad() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
ok()  { echo "  ok   — $*"; }

echo "== #5439 per-Org vCluster registry pivot (step-09 Phase-3) + step-11 lint =="

helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" >"${TMP}/render.yaml" 2>"${TMP}/render.err" || {
  echo "helm template failed:"; cat "${TMP}/render.err"; exit 1
}

# ═══════════════════════════════════════════════════════════════════════════
# A. Extraction — the shipped bytes, or nothing
# ═══════════════════════════════════════════════════════════════════════════
echo
echo "-- A. the phase and the lint exist in the render --"

awk '/# ---- perorg-vcluster-pivot BEGIN/,/# ---- perorg-vcluster-pivot END/' \
  "${TMP}/render.yaml" | sed 's/^            //' >"${TMP}/phase3.sh"

if ! grep -q 'perorg-vcluster-pivot BEGIN' "${TMP}/phase3.sh"; then
  echo "  FAIL — step-09 carries no per-Org catalyst-tenant pivot region."
  echo "         The four flux-system HelmReleases it pivots cannot reach the"
  echo "         per-Organization vCluster HelmRelease, whose durable source is"
  echo "         <slug>/${REPO}@main:vcluster/vcluster.yaml — so every Org keeps"
  echo "         image.registry=${MOTHERSHIP} through a cutover that reports"
  echo "         success (#5439, measured on hw292/uatco)."
  exit 1
fi
if ! grep -q 'perorg-vcluster-pivot END' "${TMP}/phase3.sh"; then
  echo "  FAIL — the Phase-3 region was truncated during extraction (no END marker)."
  exit 1
fi
ok "step-09 Phase-3 region extracted ($(grep -c . "${TMP}/phase3.sh") lines)"

awk '/^ *run_perorg_hr_values_registry_lint\(\) \{/,/^ *\}$/' \
  "${TMP}/render.yaml" | sed 's/^ *//' >"${TMP}/lint.sh"

if ! grep -q 'run_perorg_hr_values_registry_lint()' "${TMP}/lint.sh"; then
  echo "  FAIL — step-11 carries no per-Org HelmRelease values lint."
  echo "         Without it the pivot above has no gate: the pod-spec ref-host"
  echo "         lint EXEMPTS ${MOTHERSHIP}, and the Flux source lint reads only"
  echo "         source URLs, so a per-Org spec.values tether passes both and"
  echo "         cutoverComplete=true is set over a live dependency (#5439)."
  exit 1
fi
# The awk range stops at the first bare `}` line. If a nested multi-line
# function is ever added inside the lint the range truncates, and a FRAGMENT
# would still source cleanly and could still return 0 — i.e. the suite would
# quietly change subject. Pin the tail so truncation is a failure.
if ! grep -q '^return 0$' "${TMP}/lint.sh"; then
  echo "  FAIL — the lint was truncated during extraction (no trailing 'return 0')."
  exit 1
fi
ok "step-11 lint extracted ($(grep -c . "${TMP}/lint.sh") lines), tail intact"

sh -n "${TMP}/phase3.sh" 2>"${TMP}/syn.err" && ok "Phase-3 region is valid POSIX sh" \
  || bad "Phase-3 region is not valid POSIX sh: $(head -1 "${TMP}/syn.err")"
sh -n "${TMP}/lint.sh" 2>"${TMP}/syn2.err" && ok "lint is valid POSIX sh" \
  || bad "lint is not valid POSIX sh: $(head -1 "${TMP}/syn2.err")"

# ═══════════════════════════════════════════════════════════════════════════
# B. The ordering invariant the durability argument rests on
# ═══════════════════════════════════════════════════════════════════════════
# Phase-3 rewrites the per-Org repos. The org-controller REGENERATES those repos
# on every Organization reconcile, so the rewrite only survives if the generator
# has already been made cutover-aware — which is step-07 Phase 3e stamping
# CATALYST_LOCAL_IMAGE_REGISTRY_HOST. If step-09 ever sorted BEFORE step-07, any
# Organization reconcile in between would write ${MOTHERSHIP} straight back into
# the Flux-owned source and this whole leg would be a no-op with good logs.
# The orders are read from the RENDER's own labels, not from prose.
echo
echo "-- B. step-07 (generator stamp) must run BEFORE step-09 (repo rewrite) --"
python3 - "${TMP}/render.yaml" >"${TMP}/orders.txt" 2>"${TMP}/orders.err" <<'PY'
import sys, yaml
orders = {}
for doc in yaml.safe_load_all(open(sys.argv[1], encoding="utf-8")):
    if not isinstance(doc, dict):
        continue
    md = doc.get("metadata") or {}
    labels = md.get("labels") or {}
    o = labels.get("bp.openova.io/cutover-order")
    step = (doc.get("data") or {}).get("stepName") if doc.get("kind") == "ConfigMap" else None
    if o is not None and step:
        orders[step] = int(o)
for k, v in sorted(orders.items(), key=lambda kv: kv[1]):
    print(f"{v} {k}")
PY
if [ ! -s "${TMP}/orders.txt" ]; then
  bad "could not read cutover-order labels from the render: $(head -1 "${TMP}/orders.err")"
else
  env_order=$(awk '$2=="catalyst-api-env-patch"{print $1}' "${TMP}/orders.txt")
  piv_order=$(awk '$2=="vcluster-registry-pivot"{print $1}' "${TMP}/orders.txt")
  egr_order=$(awk '$2=="egress-block-test"{print $1}' "${TMP}/orders.txt")
  if [ -z "${env_order}" ] || [ -z "${piv_order}" ] || [ -z "${egr_order}" ]; then
    bad "missing a cutover-order (env=${env_order:-?} pivot=${piv_order:-?} egress=${egr_order:-?})"
  elif [ "${env_order}" -lt "${piv_order}" ]; then
    ok "catalyst-api-env-patch=${env_order} < vcluster-registry-pivot=${piv_order} — the generator carries the local-registry stamp before any repo is rewritten"
  else
    bad "catalyst-api-env-patch=${env_order} is NOT before vcluster-registry-pivot=${piv_order}: the org-controller would re-render ${MOTHERSHIP} back into the per-Org source after Phase-3 rewrote it"
  fi
  if [ -n "${piv_order}" ] && [ -n "${egr_order}" ] && [ "${piv_order}" -lt "${egr_order}" ]; then
    ok "vcluster-registry-pivot=${piv_order} < egress-block-test=${egr_order} — the lint observes the estate after the pivot ran"
  else
    bad "vcluster-registry-pivot=${piv_order:-?} must run before egress-block-test=${egr_order:-?}, or the lint asserts over an un-pivoted estate"
  fi
fi

# ═══════════════════════════════════════════════════════════════════════════
# C. Harness
# ═══════════════════════════════════════════════════════════════════════════
mkdir -p "${TMP}/bin"
GITEA_ROOT="${TMP}/gitea"
GITEA_HOST="gitea.invalid"

# kubectl stub. Dispatches on the token after `get`, so a leading
# `--kubeconfig=` (the lint's secondary-region shim) does not shift the index.
cat >"${TMP}/bin/kubectl" <<'STUB'
#!/usr/bin/env sh
verb=""; kind=""; want=0
for a in "$@"; do
  if [ "${want}" = "1" ]; then kind="$a"; want=0; continue; fi
  case "$a" in
    get)      verb=get; want=1 ;;
    annotate) verb=annotate ;;
  esac
done
[ "${verb}" = "annotate" ] && exit 0
short="${kind%%.*}"
if [ -n "${STUB_FAIL_KIND:-}" ] && [ "${short}" = "${STUB_FAIL_KIND}" ]; then
  echo "${STUB_FAIL_MSG:-error: You must be logged in to the server (Unauthorized)}" >&2
  exit 1
fi
if [ "${short}" = "customresourcedefinitions" ]; then
  case "${STUB_CRD_MODE:-ok}" in
    ok)       exit 0 ;;
    notfound) echo 'Error from server (NotFound): customresourcedefinitions.apiextensions.k8s.io "organizations.orgs.openova.io" not found' >&2; exit 1 ;;
    *)        echo 'Error from server (Forbidden): customresourcedefinitions.apiextensions.k8s.io is forbidden' >&2; exit 1 ;;
  esac
fi
# HelmRelease answers may be a SEQUENCE, so a case can make the estate converge
# on the Nth poll (or never).
if [ "${short}" = "helmreleases" ] && [ ! -f "${STUB_DIR}/helmreleases.txt" ]; then
  n=$(cat "${STUB_DIR}/.hrn" 2>/dev/null || echo 0); n=$((n+1)); echo "${n}" >"${STUB_DIR}/.hrn"
  f="${STUB_DIR}/helmreleases.${n}.txt"
  if [ ! -f "${f}" ]; then f=$(ls "${STUB_DIR}"/helmreleases.*.txt 2>/dev/null | sort -V | tail -1); fi
  [ -n "${f}" ] && [ -f "${f}" ] && cat "${f}"
  exit 0
fi
f="${STUB_DIR}/${short}.txt"
[ -f "${f}" ] && cat "${f}"
exit 0
STUB
chmod +x "${TMP}/bin/kubectl"

# curl stub — repo existence is an HTTP STATUS, forceable per slug.
cat >"${TMP}/bin/curl" <<'STUB'
#!/usr/bin/env sh
url=""; out="/dev/null"; nexto=0
for a in "$@"; do
  if [ "${nexto}" = "1" ]; then out="$a"; nexto=0; continue; fi
  case "$a" in
    -o) nexto=1 ;;
    http*) url="$a" ;;
  esac
done
slug=$(printf '%s' "${url}" | awk -F/ '{print $(NF-1)}')
repo=$(printf '%s' "${url}" | awk -F/ '{print $NF}')
: >"${out}" 2>/dev/null || true
if [ -n "${CURL_FORCE_CODE:-}" ] && [ "${CURL_FORCE_SLUG:-}" = "${slug}" ]; then
  printf '%s' "${CURL_FORCE_CODE}"; exit 0
fi
if [ -d "${GITEA_ROOT}/${slug}/${repo}.git" ]; then printf '200'; else printf '404'; fi
STUB
chmod +x "${TMP}/bin/curl"

# git shim — maps http://<creds>@gitea.invalid/<slug>/<repo>.git onto the local
# bare repo, then execs the REAL git. Branches, commits, pushes and diffs are
# genuine; only the transport is local.
cat >"${TMP}/bin/git" <<'STUB'
#!/usr/bin/env sh
first=1
for a in "$@"; do
  case "$a" in
    http://*"${GITEA_HOST_FOR_SHIM}"/*)
      a="${GITEA_ROOT}/${a##*"${GITEA_HOST_FOR_SHIM}"/}" ;;
  esac
  if [ "${first}" = 1 ]; then set -- "$a"; first=0; else set -- "$@" "$a"; fi
done
exec "${REAL_GIT_BIN}" "$@"
STUB
chmod +x "${TMP}/bin/git"

# sleep stub — the read-back's bound is wall-clock (date), so shortening the
# poll gap changes nothing it asserts and saves 15s per poll in CI.
cat >"${TMP}/bin/sleep" <<'STUB'
#!/usr/bin/env sh
exec /bin/sleep 0.2
STUB
chmod +x "${TMP}/bin/sleep"

# The per-Org vcluster/vcluster.yaml as the org-controller renders it — the
# three image references all resolve off VClusterImageRegistry, in the two
# DIFFERENT shapes it emits (bare `registry:` scalar, and a fully-qualified
# host/path:tag for coredns). Both shapes must end up pivoted.
vcluster_doc() {
  printf 'apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: vcluster\n  namespace: %s\n  labels:\n    openova.io/organization: %s\nspec:\n  values:\n    controlPlane:\n      distro:\n        k8s:\n          image:\n            registry: %s\n            repository: proxy-ghcr/loft-sh/kubernetes\n      coredns:\n        deployment:\n          image: %s/proxy-dockerhub/coredns/coredns:1.11.3\n      statefulSet:\n        image:\n          registry: %s\n          repository: proxy-ghcr/loft-sh/vcluster-oss\n' "$1" "$1" "$2" "$2" "$2"
}

seed_org_repo() {
  # $1 = slug   $2 = image registry host to seed with
  sl="$1"; host="$2"
  mkdir -p "${GITEA_ROOT}/${sl}"
  rm -rf "${GITEA_ROOT}/${sl}/${REPO}.git" "${TMP}/seed-${sl}"
  "${REAL_GIT}" init --bare -q "${GITEA_ROOT}/${sl}/${REPO}.git"
  "${REAL_GIT}" init -q -b main "${TMP}/seed-${sl}"
  (
    cd "${TMP}/seed-${sl}"
    "${REAL_GIT}" config user.name t; "${REAL_GIT}" config user.email t@example.invalid
    mkdir -p vcluster/apps legacy
    vcluster_doc "${sl}" "${host}" >vcluster/vcluster.yaml
    printf 'image: %s/proxy-dockerhub/library/nginx:1.27\n' "${host}" >vcluster/apps/app-site.yaml
    # OUTSIDE the reconciled tree, carrying the mothership host — the SCOPING
    # control. No per-Org Kustomization reconciles this path, so rewriting it
    # would be divergence with nothing behind it.
    printf 'image: %s/proxy-dockerhub/library/redis:7\n' "${host}" >legacy/old.yaml
    "${REAL_GIT}" add -A; "${REAL_GIT}" commit -qm seed
    "${REAL_GIT}" remote add origin "${GITEA_ROOT}/${sl}/${REPO}.git"
    "${REAL_GIT}" push -q origin main
  ) >/dev/null 2>&1
}

origin_show() { "${REAL_GIT}" --git-dir="${GITEA_ROOT}/$1/${REPO}.git" show "main:$2" 2>/dev/null; }
origin_commits() { "${REAL_GIT}" --git-dir="${GITEA_ROOT}/$1/${REPO}.git" rev-list --count main 2>/dev/null || echo 0; }

# Runs the extracted Phase-3 region. Echoes its exit code; output in ${TMP}/out.txt
run_phase3() {
  local case_dir="$1" script="${2:-${TMP}/phase3.sh}"
  (
    set -eu
    export PATH="${TMP}/bin:${PATH}"
    export STUB_DIR="${case_dir}"
    export GITEA_ROOT="${GITEA_ROOT}"
    export GITEA_HOST_FOR_SHIM="${GITEA_HOST}"
    export REAL_GIT_BIN="${REAL_GIT}"
    export GITEA_INTERNAL_URL="http://${GITEA_HOST}"
    export GITEA_USERNAME="gitea_admin"
    export GITEA_PAT="stub-token-not-a-secret"
    export PERORG_PIVOT_ENABLED="${PERORG_PIVOT_ENABLED:-true}"
    export PERORG_TENANT_REPO="${REPO}"
    export PERORG_BRANCH="main"
    export PERORG_FLUX_NAMESPACE="flux-system"
    export PERORG_ORG_CRD="organizations.orgs.openova.io"
    export PERORG_TREES="vcluster"
    export PERORG_READBACK_DEADLINE="${PERORG_READBACK_DEADLINE:-30}"
    export PERORG_SCRATCH_DIR="${case_dir}/scratch"
    export MOTHERSHIP_HARBOR_HOST="${MOTHERSHIP}"
    # Phase-2 runs `git config --global user.{name,email}` unconditionally
    # before Phase-3 is reached, so in the Job a commit identity always exists.
    # Driving Phase-3 alone has to supply the same fact; env vars do it without
    # writing a gitconfig into the runner's HOME.
    export GIT_AUTHOR_NAME="self-sovereign-cutover" GIT_AUTHOR_EMAIL="cutover@${FQDN}"
    export GIT_COMMITTER_NAME="self-sovereign-cutover" GIT_COMMITTER_EMAIL="cutover@${FQDN}"
    mkdir -p "${PERORG_SCRATCH_DIR}"
    target_host="${TARGET_HOST}"
    export target_host
    # shellcheck disable=SC1090
    . "${script}"
  ) >"${TMP}/out.txt" 2>&1
  echo $?
}

new_case() {
  # $1 = case name; seeds a stub dir with a believable flux-system estate.
  local d="${TMP}/case-$1"
  rm -rf "${d}"; mkdir -p "${d}/scratch"
  # A real Sovereign always has GitRepositories in flux-system — `openova` at
  # minimum, which this very step annotates. Included so the vacuity witness
  # has an honest baseline to pass on.
  printf 'openova\n' >"${d}/gitrepositories.txt"
  : >"${d}/organizations.txt"
  : >"${d}/namespaces.txt"
  echo "${d}"
}

# ═══════════════════════════════════════════════════════════════════════════
# D. Phase-3 — the pivot, proven out of the BARE origin
# ═══════════════════════════════════════════════════════════════════════════
echo
echo "-- D. the durable rewrite --"

D1="$(new_case d1)"
seed_org_repo walkone "${MOTHERSHIP}"
seed_org_repo walktwo "${MOTHERSHIP}"
{ printf 'openova\n'; printf '%s-walkone\n' "${REPO}"; printf '%s-walktwo\n' "${REPO}"; } >"${D1}/gitrepositories.txt"
# Converged estate: the read-back's first sample is already clean.
printf 'walkone/vcluster|vcluster|walkone|map[controlPlane:map[statefulSet:map[image:map[registry:%s]]]]\n' "${TARGET_HOST}" >"${D1}/helmreleases.txt"
rc="$(run_phase3 "${D1}")"
if [ "${rc}" != "0" ]; then
  bad "D1 shipped Phase-3 exited ${rc} on a healthy estate: $(tail -3 "${TMP}/out.txt" | tr '\n' ' ')"
else
  ok "D1 Phase-3 completed on a two-Organization estate"
fi
for sl in walkone walktwo; do
  body="$(origin_show "${sl}" vcluster/vcluster.yaml)"
  if printf '%s' "${body}" | grep -q "${MOTHERSHIP}"; then
    bad "D1 ${sl}: vcluster.yaml in the BARE origin still names ${MOTHERSHIP}"
  elif [ "$(printf '%s' "${body}" | grep -c "${TARGET_HOST}")" != "3" ]; then
    bad "D1 ${sl}: expected 3 ${TARGET_HOST} references in the pushed vcluster.yaml, got $(printf '%s' "${body}" | grep -c "${TARGET_HOST}")"
  else
    ok "D1 ${sl}: all 3 image references pivoted to ${TARGET_HOST} in the pushed tree"
  fi
  if origin_show "${sl}" vcluster/apps/app-site.yaml | grep -q "${TARGET_HOST}"; then
    ok "D1 ${sl}: the app manifest in the same reconciled tree pivoted too"
  else
    bad "D1 ${sl}: vcluster/apps/app-site.yaml was not pivoted"
  fi
  if origin_show "${sl}" legacy/old.yaml | grep -q "${MOTHERSHIP}"; then
    ok "D1 ${sl}: the file OUTSIDE the reconciled tree was left alone (scoping control)"
  else
    bad "D1 ${sl}: rewrote legacy/old.yaml — nothing reconciles that path, so this is divergence"
  fi
done

# Idempotence, on the SAME origin the previous case pushed to.
before="$(origin_commits walkone)"
rc="$(run_phase3 "${D1}")"
after="$(origin_commits walkone)"
if [ "${rc}" != "0" ]; then
  bad "D2 re-run exited ${rc} (a second run must be safe): $(tail -3 "${TMP}/out.txt" | tr '\n' ' ')"
elif [ "${before}" != "${after}" ]; then
  bad "D2 re-run added a commit (${before} -> ${after}) — the rewrite is not idempotent"
else
  ok "D2 re-run is a no-op: exit 0, commit count unchanged at ${after}"
fi

# The Organization CR union: an Org with a repo but no Flux source yet.
D3="$(new_case d3)"
seed_org_repo walkthree "${MOTHERSHIP}"
printf 'openova\n' >"${D3}/gitrepositories.txt"
printf 'walkthree\n' >"${D3}/organizations.txt"
printf 'walkthree/vcluster|vcluster|walkthree|map[image:map[registry:%s]]\n' "${TARGET_HOST}" >"${D3}/helmreleases.txt"
rc="$(run_phase3 "${D3}")"
if [ "${rc}" = "0" ] && ! origin_show walkthree vcluster/vcluster.yaml | grep -q "${MOTHERSHIP}"; then
  ok "D3 an Organization known only by its CR (no Flux source yet) was still pivoted"
else
  bad "D3 the Organization-CR union leg did not pivot walkthree (exit ${rc})"
fi

# ═══════════════════════════════════════════════════════════════════════════
# E. Fail-loud — every way the estate can be unknowable
# ═══════════════════════════════════════════════════════════════════════════
echo
echo "-- E. an unreadable estate is never an empty one --"

expect_fatal() {
  # $1 = label, $2 = case dir, $3 = substring the message must carry
  local label="$1" d="$2" needle="$3" rc
  rc="$(run_phase3 "${d}")"
  if [ "${rc}" = "0" ]; then
    bad "${label}: exited 0 — the step recorded success over an estate it could not read"
  elif ! grep -qi "${needle}" "${TMP}/out.txt"; then
    bad "${label}: failed (exit ${rc}) but the message never names '${needle}': $(grep -i fatal "${TMP}/out.txt" | head -1)"
  else
    ok "${label}: FATAL (exit ${rc}), message names what was missing"
  fi
}

E1="$(new_case e1)"; export STUB_FAIL_KIND=gitrepositories
expect_fatal "E1 GitRepository listing REFUSED" "${E1}" "listing GitRepositories"
unset STUB_FAIL_KIND

# The vacuity witness: the listing SUCCEEDS and returns nothing. On a Sovereign
# that cannot be true — this step annotates gitrepository/openova in the same
# namespace — so a "no Organizations, nothing to do" verdict drawn from it is a
# fabrication. This is the case a fail-closed-on-error guard alone does NOT
# cover, and the reason the witness exists.
E2="$(new_case e2)"; : >"${E2}/gitrepositories.txt"
expect_fatal "E2 GitRepository listing SUCCEEDS but returns zero objects" "${E2}" "ZERO objects"

E3="$(new_case e3)"; export STUB_CRD_MODE=forbidden
expect_fatal "E3 CRD probe fails with something other than NotFound" "${E3}" "NOT the same as it being absent"
unset STUB_CRD_MODE

# NotFound is a REAL state and must NOT be fatal.
E4="$(new_case e4)"; export STUB_CRD_MODE=notfound
rc="$(run_phase3 "${E4}")"
if [ "${rc}" = "0" ] && grep -q "not installed on this Sovereign" "${TMP}/out.txt"; then
  ok "E4 a confirmed-NotFound CRD is a real state, not a failure (exit 0)"
else
  bad "E4 a NotFound Organization CRD must not fail the step (exit ${rc})"
fi
unset STUB_CRD_MODE

E5="$(new_case e5)"
seed_org_repo walkfour "${MOTHERSHIP}"
{ printf 'openova\n'; printf '%s-walkfour\n' "${REPO}"; } >"${E5}/gitrepositories.txt"
export CURL_FORCE_CODE=500 CURL_FORCE_SLUG=walkfour
expect_fatal "E5 repo probe returns 500" "${E5}" "unreadable repo is NOT an absent repo"
unset CURL_FORCE_CODE CURL_FORCE_SLUG

# 404 is legitimately absent — an Org that never installed anything.
E6="$(new_case e6)"
{ printf 'openova\n'; printf '%s-ghost\n' "${REPO}"; } >"${E6}/gitrepositories.txt"
rc="$(run_phase3 "${E6}")"
if [ "${rc}" = "0" ] && grep -q "HTTP 404" "${TMP}/out.txt"; then
  ok "E6 an Organization with no per-Org repo (404) is skipped, not fatal (exit 0)"
else
  bad "E6 a 404 repo probe must be a benign skip (exit ${rc})"
fi

# The read-back: pushed is not pivoted until the owning reconciler re-applied it.
E7="$(new_case e7)"
seed_org_repo walkfive "${MOTHERSHIP}"
{ printf 'openova\n'; printf '%s-walkfive\n' "${REPO}"; } >"${E7}/gitrepositories.txt"
rm -f "${E7}/helmreleases.txt"
printf 'walkfive/vcluster|vcluster|walkfive|map[image:map[registry:%s]]\n' "${MOTHERSHIP}" >"${E7}/helmreleases.1.txt"
PERORG_READBACK_DEADLINE=1 expect_fatal "E7 read-back never converges" "${E7}" "has not re-applied it"
if origin_show walkfive vcluster/vcluster.yaml | grep -q "${TARGET_HOST}"; then
  ok "E7 the push still happened — the FATAL is about the reconciler, not the rewrite"
else
  bad "E7 expected the rewrite to be pushed before the read-back verdict"
fi

# ...and it converges once the reconciler catches up.
E8="$(new_case e8)"
seed_org_repo walksix "${MOTHERSHIP}"
{ printf 'openova\n'; printf '%s-walksix\n' "${REPO}"; } >"${E8}/gitrepositories.txt"
rm -f "${E8}/helmreleases.txt"
printf 'walksix/vcluster|vcluster|walksix|map[image:map[registry:%s]]\n' "${MOTHERSHIP}" >"${E8}/helmreleases.1.txt"
printf 'walksix/vcluster|vcluster|walksix|map[image:map[registry:%s]]\n' "${MOTHERSHIP}" >"${E8}/helmreleases.2.txt"
printf 'walksix/vcluster|vcluster|walksix|map[image:map[registry:%s]]\n' "${TARGET_HOST}" >"${E8}/helmreleases.3.txt"
rc="$(PERORG_READBACK_DEADLINE=30 run_phase3 "${E8}")"
if [ "${rc}" = "0" ] && grep -q "read-back PASS" "${TMP}/out.txt"; then
  ok "E8 the read-back waits out a slow reconciler and then passes (exit 0)"
else
  bad "E8 expected convergence on the 3rd sample (exit ${rc}): $(tail -2 "${TMP}/out.txt" | tr '\n' ' ')"
fi

# ═══════════════════════════════════════════════════════════════════════════
# F. The step-11 lint
# ═══════════════════════════════════════════════════════════════════════════
echo
echo "-- F. the pre-hold lint --"

run_lint() {
  # $1 = case dir, $2 = allowReleases
  (
    export PATH="${TMP}/bin:${PATH}"
    export STUB_DIR="$1"
    export WORK_DIR="$1/work"; mkdir -p "${WORK_DIR}"
    export PERORG_HR_MOTHERSHIP_HOSTS="${MOTHERSHIP}"
    export PERORG_HR_ALLOW_RELEASES="${2:-}"
    # shellcheck disable=SC1090
    . "${TMP}/lint.sh"
    if run_perorg_hr_values_registry_lint "primary" "" >"${TMP}/lintout.txt" 2>&1; then echo PASS; else echo FAIL; fi
  )
}

F1="$(new_case f1)"
printf 'uatco\n' >"${F1}/namespaces.txt"
printf 'uatco|vcluster|uatco|map[controlPlane:map[statefulSet:map[image:map[registry:%s]]]]\n' "${MOTHERSHIP}" >"${F1}/helmreleases.txt"
[ "$(run_lint "${F1}")" = "FAIL" ] \
  && ok "F1 a per-Org HelmRelease naming ${MOTHERSHIP} in spec.values is caught (the exact hw292/uatco shape)" \
  || bad "F1 the lint PASSED over the live hw292 tether — it does not measure what it claims"

F2="$(new_case f2)"
printf 'uatco\n' >"${F2}/namespaces.txt"
printf 'uatco|vcluster|uatco|map[controlPlane:map[statefulSet:map[image:map[registry:%s]]]]\n' "${TARGET_HOST}" >"${F2}/helmreleases.txt"
[ "$(run_lint "${F2}")" = "PASS" ] \
  && ok "F2 a pivoted per-Org HelmRelease passes" \
  || bad "F2 the lint failed a clean estate: $(tail -1 "${TMP}/lintout.txt")"

# Namespace-derived membership: the HR carries no org label of its own.
F3="$(new_case f3)"
printf 'uatco\n' >"${F3}/namespaces.txt"
printf 'uatco|bp-wordpress-tenant||map[image:map[registry:%s]]\n' "${MOTHERSHIP}" >"${F3}/helmreleases.txt"
[ "$(run_lint "${F3}")" = "FAIL" ] \
  && ok "F3 an unlabelled HelmRelease in an Organization namespace is still in scope" \
  || bad "F3 membership by namespace label is not working — an app HR in an Org ns escaped"

# A PLATFORM HelmRelease is not this lint's business (step-09 Phase-1c owns it).
F4="$(new_case f4)"
: >"${F4}/namespaces.txt"
printf 'flux-system|bp-mgmt-vcluster||map[global:map[registryMirror:%s]]\n' "${MOTHERSHIP}" >"${F4}/helmreleases.txt"
[ "$(run_lint "${F4}")" = "PASS" ] \
  && ok "F4 a platform HelmRelease outside any Organization namespace is out of scope, not a false positive" \
  || bad "F4 the lint claimed a platform HR — scope creep into step-09 Phase-1c's assertion"

F5="$(new_case f5)"
printf 'uatco\n' >"${F5}/namespaces.txt"
printf 'uatco|vcluster|uatco|map[image:map[registry:%s]]\n' "${MOTHERSHIP}" >"${F5}/helmreleases.txt"
[ "$(run_lint "${F5}" "uatco/vcluster")" = "PASS" ] \
  && ok "F5 a reviewed allowReleases exception is honoured" \
  || bad "F5 the allowReleases exception was ignored"

F6="$(new_case f6)"; printf 'uatco\n' >"${F6}/namespaces.txt"
[ "$(STUB_FAIL_KIND=helmreleases run_lint "${F6}")" = "FAIL" ] \
  && ok "F6 an unreadable HelmRelease list is a FAIL, never a silent PASS" \
  || bad "F6 the lint reported PASS for a region it could not measure"

F7="$(new_case f7)"; printf 'uatco\n' >"${F7}/namespaces.txt"
[ "$(STUB_FAIL_KIND=namespaces run_lint "${F7}")" = "FAIL" ] \
  && ok "F7 an unreadable Organization-namespace list is a FAIL" \
  || bad "F7 the lint proceeded with a shrunken per-Org predicate"

# The lint's own vacuity witness.
F8="$(new_case f8)"; printf 'uatco\n' >"${F8}/namespaces.txt"; : >"${F8}/helmreleases.txt"
[ "$(run_lint "${F8}")" = "FAIL" ] \
  && ok "F8 zero HelmReleases from a SUCCEEDING listing is an unread estate, not a clean one" \
  || bad "F8 the lint certified 'no per-Org tether' from an estate with zero objects in it"

# ═══════════════════════════════════════════════════════════════════════════
# G. Proven able to fail — mutants of the shipped bytes
# ═══════════════════════════════════════════════════════════════════════════
echo
echo "-- G. the guards are guards (mutation controls) --"

# G1 — neuter the vacuity witness (make its condition unreachable rather than
# deleting the block, which would only prove sh rejects a hole). E2's fixture
# must then sail through.
sed 's/\[ "\${pv_grs_total}" -eq 0 \]/[ "${pv_grs_total}" -eq -1 ]/' "${TMP}/phase3.sh" >"${TMP}/mutant-witness.sh"
if ! grep -q -- '-eq -1' "${TMP}/mutant-witness.sh" || ! sh -n "${TMP}/mutant-witness.sh" 2>/dev/null; then
  bad "G1 could not build a valid no-witness mutant"
else
  G1="$(new_case g1)"; : >"${G1}/gitrepositories.txt"
  mrc="$(run_phase3 "${G1}" "${TMP}/mutant-witness.sh")"
  if [ "${mrc}" = "0" ]; then
    ok "G1 without the witness the SAME empty-read fixture exits 0 (silently 'nothing to do') — the witness is what discriminates"
  else
    bad "G1 the no-witness mutant still failed (exit ${mrc}); E2 is not isolating the witness"
  fi
fi

# G2 — delete the read-back. E7's never-converging fixture must then pass.
python3 - "${TMP}/phase3.sh" "${TMP}/mutant-readback.sh" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
lines = open(src, encoding="utf-8").read().split("\n")
out, skip = [], False
for ln in lines:
    if "pv_offenders() {" in ln:
        skip = True
        # The removed region is the whole body of an `else` branch; leave a
        # no-op behind so the mutant stays syntactically valid and the case
        # measures the ABSENCE OF THE READ-BACK, not a parse error.
        out.append(":")
    if skip and "read-back PASS" in ln:
        skip = False
        continue
    if not skip:
        out.append(ln)
open(dst, "w", encoding="utf-8").write("\n".join(out))
PY
if grep -q 'read-back PASS' "${TMP}/mutant-readback.sh" || ! sh -n "${TMP}/mutant-readback.sh" 2>/dev/null; then
  bad "G2 could not build a syntactically valid no-read-back mutant"
else
  G2="$(new_case g2)"
  seed_org_repo walkseven "${MOTHERSHIP}"
  { printf 'openova\n'; printf '%s-walkseven\n' "${REPO}"; } >"${G2}/gitrepositories.txt"
  rm -f "${G2}/helmreleases.txt"
  printf 'walkseven/vcluster|vcluster|walkseven|map[image:map[registry:%s]]\n' "${MOTHERSHIP}" >"${G2}/helmreleases.1.txt"
  mrc="$(PERORG_READBACK_DEADLINE=1 run_phase3 "${G2}" "${TMP}/mutant-readback.sh")"
  if [ "${mrc}" = "0" ]; then
    ok "G2 without the read-back the SAME never-converging fixture exits 0 — the read-back is what turns 'pushed' into 'pivoted'"
  else
    bad "G2 the no-read-back mutant still failed (exit ${mrc}); E7 is not isolating the read-back"
  fi
fi

# G3 — the lint with its offender test removed must pass F1's fixture.
sed 's/^                    \*"\${_pvh}"\*).*$/                    *"__never_matches__"*) : ;;/' "${TMP}/lint.sh" >"${TMP}/mutant-lint.sh"
if ! grep -q '__never_matches__' "${TMP}/mutant-lint.sh"; then
  # The indentation was stripped at extraction; match on content instead.
  sed 's/\*"\${_pvh}"\*)/*"__never_matches__")/' "${TMP}/lint.sh" >"${TMP}/mutant-lint.sh"
fi
if ! grep -q '__never_matches__' "${TMP}/mutant-lint.sh"; then
  bad "G3 could not build the blind-lint mutant"
else
  mv "${TMP}/lint.sh" "${TMP}/lint.real.sh"; cp "${TMP}/mutant-lint.sh" "${TMP}/lint.sh"
  res="$(run_lint "${F1}")"
  mv "${TMP}/lint.real.sh" "${TMP}/lint.sh"
  if [ "${res}" = "PASS" ]; then
    ok "G3 with the host test neutered the lint PASSES the hw292 fixture — F1 is isolating the assertion, not the plumbing"
  else
    bad "G3 the blind-lint mutant still failed; F1 may be passing for the wrong reason"
  fi
fi

echo
if [ "${FAILURES}" -eq 0 ]; then
  echo "== #5439 suite PASS =="
  exit 0
fi
echo "== #5439 suite FAILED (${FAILURES}) =="
exit 1
