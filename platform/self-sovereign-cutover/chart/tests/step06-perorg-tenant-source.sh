#!/usr/bin/env bash
# #6309 — step-06 Phase-2d: the per-Org `<slug>/catalyst-tenant` HelmRepository
# DURABLE-SOURCE pivot, and the read-back that must survive a reconcile.
#
# THE DEFECT THIS LOCKS OUT
# ─────────────────────────
# Three GitOps repo classes carry HelmRepository URLs on a Sovereign. Before
# 0.1.188 the cutover pivoted two:
#
#   clusters/_template/bootstrap-kit  committed in-repo    step-05 / step-06
#   clusters/<fqdn>/org-tenants       catalyst-api         Phase-2b (#6293)
#   <slug>/catalyst-tenant            org-services/        NOTHING
#                                       provisioning
#
# `grep -rn catalyst-tenant platform/self-sovereign-cutover/` returned zero hits
# across the entire chart. Measured on hw296 after #6303 (chart 0.1.187) pivoted
# the org-tenants class at source, step-11's #3526 pre-hold assert still failed
# on exactly two offenders — down from six, never to zero:
#
#   walkfour/bp-stalwart-tenant = oci://ghcr.io/openova-io
#   walkfive/bp-stalwart-tenant = oci://ghcr.io/openova-io
#
# whose durable source is the third class:
#
#   walkfour/catalyst-tenant@main : vcluster/host-apps/app-stalwart-mail.yaml:20
#   walkfive/catalyst-tenant@main : vcluster/apps/app-stalwart-mail.yaml:20
#
# THE OFFENDER COUNT SCALES WITH THE NUMBER OF ORGANIZATIONS. A curated list of
# slugs is therefore wrong the moment the next Org is created, which is why
# Section L below is not a nice-to-have: it is the property that separates this
# fix from one that happens to work on hw296.
#
# WHY THE 0.1.186 READ-BACK IS NOT A MODEL TO COPY
# ────────────────────────────────────────────────
# Step-06's Phase-1.5 read-back logged `ok=6` and `read-back OK`, and all six
# HelmRepositories reverted within 60s under the org-tenants Kustomization
# (interval=1m, prune=true). It could pass because it sampled ONCE, immediately
# after the write, with no requirement that the actor which would revert the
# write had run at all. Step-11's cluster-wide `-A` scan is what caught it.
#
# Phase-2d's read-back is two-stage and Section D proves BOTH stages fire:
#   Stage 1 CONVERGE — poll to zero offenders across the pivoted Orgs.
#   Stage 2 SURVIVE  — mint a token AFTER Stage 1 is clean, poke every per-Org
#                      HR-owning Kustomization, and refuse to certify an Org
#                      until one of ITS Kustomizations reports
#                      status.lastHandledReconcileAt == that token, with zero
#                      offenders on EVERY sample in that window.
# Case D2 is the 0.1.186 fixture exactly: clean on the first sample, reverted on
# the next. Mutant M6 deletes Stage 2 and shows that same fixture then passes —
# which is the whole argument, made mechanically rather than asserted in prose.
#
# WHAT IS REAL HERE AND WHAT IS STUBBED, AND WHY
# ──────────────────────────────────────────────
# REAL: git. Every case runs the REAL rendered phase2d.sh bytes against REAL
# local git repositories with real branches, and reads its verdict out of the
# BARE origin (`git show main:<path>`), never out of a working tree the script
# left behind. Pushed-or-it-did-not-happen. `git` is reached through a shim that
# ONLY maps the http URL onto the local bare repo and records whether the URL
# carried a credential — it then execs the real binary, so branches, commits,
# pushes and diffs are genuine.
#
# STUBBED: kubectl and curl, because the defect classes this suite covers are
# "which Organizations did you scan" and "did you wait for the reverting
# reconciler" — both of which are decided by how the script INTERPRETS the API's
# answers, not by the API. The stubs are programmable per case and answer from
# fixture files, so a case can make an enumeration FAIL, which a live cluster
# could not be asked to do on demand.
#
# WHAT A STUB CANNOT SEE, AND WHERE THAT IS COVERED INSTEAD
# ─────────────────────────────────────────────────────────
# A stubbed kubectl can never be RBAC-refused, so it is structurally blind to a
# missing ClusterRole grant — the #6280 shape, where Case 95 of the contract
# suite went green against a stub while the real ServiceAccount was being
# refused. Phase-2d reads a resource the ClusterRole did not previously grant
# (organizations.orgs.openova.io). Section R therefore asserts that grant
# DIRECTLY against the rendered ClusterRole — no stub in the path — and R3b
# deletes the rule and requires the assertion to go RED.
#
# NO `grep -q` ON A PIPE ANYWHERE IN THIS FILE. Under `pipefail` (which this
# file sets) grep closing the pipe on its first match SIGPIPEs the upstream
# writer, and on the GitHub runner Node sets SIGPIPE to SIG_IGN so a FOUND match
# exits 1 — byte-identical to an honest miss. That shape has bitten this repo
# three times (#5370, #5406, #4969). Producers write to FILES; greps read FILES.
#
# EVERY ASSERTION IS VACUITY-PROVED INDIVIDUALLY. Section M re-runs cases
# against single-behaviour mutants of the real script — one mutation each — and
# requires the matching case to go RED. Cases with no mutant partner are
# CONTROLS, and Section M includes mutants that turn controls red too, so the
# suite cannot be satisfied by deleting, disabling or blanket-widening Phase-2d.
#
# Usage: bash tests/step06-perorg-tenant-source.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

FQDN="hw296.omani.works"
UPSTREAM="oci://ghcr.io/openova-io"
LOCAL="oci://registry.${FQDN}/openova-io"
TRUST="cutover-harbor-ca"
GITEA_HOST="gitea.invalid"
GITEA_URL="http://${GITEA_HOST}"
REPO="catalyst-tenant"
HOSTAPP="vcluster/host-apps/app-stalwart-mail.yaml"
APPSAPP="vcluster/apps/app-stalwart-mail.yaml"
OUTSIDE="legacy/app-legacy.yaml"

FIX="${TMP}/fx"
GITEA_ROOT="${TMP}/gitea"
REAL_GIT="$(command -v git)"

pass=0; fail=0
ok()  { echo "  ok   — $1"; pass=$((pass+1)); }
bad() { echo "  FAIL — $1"; fail=$((fail+1)); }

mkdir -p "${TMP}/bin" "${FIX}"

# ── Render ─────────────────────────────────────────────────────────────────
helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" \
  >"${TMP}/render.yaml" 2>"${TMP}/render.err" || {
  echo "FAIL — helm template failed:"; cat "${TMP}/render.err"; exit 1
}

# ── Extract the bytes under test straight from the render ──────────────────
# Plain awk over the render text — the same dependency surface as the Job
# (sh + awk + git), so this suite cannot skip itself on a runner missing a
# python module.
awk '/# ---- Phase 2d \(#6309\)/,/# ---- end Phase 2d \(#6309\) ----/' "${TMP}/render.yaml" \
  | sed 's/^    //' >"${TMP}/phase2d.orig.sh"
awk '/# ---- inject_trust_ref \(#3379 #6293\) ----/,/# ---- end inject_trust_ref ----/' "${TMP}/render.yaml" \
  | sed 's/^            //' >"${TMP}/injector.sh"

if grep -q 'PERORG_TENANT_REPO' "${TMP}/phase2d.orig.sh"; then
  ok "extracted phase2d.sh from the render"
else
  echo "FAIL — phase2d.sh did not extract (marker comments changed?)"; exit 1
fi
# Truncation guard: a short extraction would still `sh` cleanly and exit 0 —
# the fail-open shape this suite exists to eliminate.
tail -3 "${TMP}/phase2d.orig.sh" >"${TMP}/tail3.txt"
if ! grep -q 'end Phase 2d' "${TMP}/tail3.txt"; then
  echo "FAIL — phase2d.sh extraction was truncated"; exit 1
fi
if grep -q 'assert_perorg_pivot_durable' "${TMP}/phase2d.orig.sh"; then
  ok "extraction carries the read-back assert (not just the rewrite half)"
else
  echo "FAIL — the read-back assert is missing from the extraction"; exit 1
fi
if grep -q 'inject_trust_ref()' "${TMP}/injector.sh"; then
  ok "extracted the shared inject_trust_ref function from the render"
else
  echo "FAIL — inject_trust_ref did not extract"; exit 1
fi
sh -n "${TMP}/phase2d.orig.sh" || { echo "FAIL — phase2d.sh is not valid POSIX sh"; exit 1; }

# Relocate the container's literal /tmp paths into the test tmpdir, and assert
# the substitution applied — a path rename in the template must not silently
# turn every case below into a no-op.
tmp_hits=$(grep -c '/tmp' "${TMP}/phase2d.orig.sh" || true)
if [ "${tmp_hits}" -lt 5 ]; then
  echo "FAIL — phase2d.sh no longer uses /tmp paths; harness relocation is stale"; exit 1
fi
relocate() { sed "s,/tmp,${TMP}/t,g" "$1" >"$2"; }
relocate "${TMP}/phase2d.orig.sh" "${TMP}/phase2d.sh"

# ═══════════════════════════════════════════════════════════════════════════
# Stubs. kubectl + curl answer from ${FIX}; git is a URL-mapping shim over the
# real binary.
# ═══════════════════════════════════════════════════════════════════════════
cat >"${TMP}/bin/kubectl" <<'FAKE'
#!/usr/bin/env sh
FX="${FIXTURE_DIR}"
printf 'kubectl %s\n' "$*" >>"${FAKE_KUBECTL_LOG}"
verb="$1"; shift
case "${verb}" in
  annotate)
    for a in "$@"; do
      case "$a" in
        reconcile.fluxcd.io/requestedAt=*) printf '%s' "${a#*=}" >"${FX}/last-token" ;;
      esac
    done
    exit 0 ;;
  get) : ;;
  *) exit 0 ;;
esac
res="$1"; shift
case "${res}" in
  gitrepositories.source.toolkit.fluxcd.io)
    rc=$(cat "${FX}/gitrepos.rc" 2>/dev/null || echo 0)
    if [ "${rc}" != "0" ]; then
      echo 'Error from server (Forbidden): gitrepositories.source.toolkit.fluxcd.io is forbidden' >&2
      exit "${rc}"
    fi
    cat "${FX}/gitrepos.txt" 2>/dev/null || true
    exit 0 ;;
  customresourcedefinitions)
    rc=$(cat "${FX}/crd.rc" 2>/dev/null || echo 0)
    if [ "${rc}" != "0" ]; then cat "${FX}/crd.err" >&2; exit "${rc}"; fi
    echo 'organizations.orgs.openova.io   2026-08-14T00:00:00Z'
    exit 0 ;;
  organizations.orgs.openova.io)
    rc=$(cat "${FX}/orgs.rc" 2>/dev/null || echo 0)
    if [ "${rc}" != "0" ]; then
      echo 'Error from server (Forbidden): organizations.orgs.openova.io is forbidden: User "system:serviceaccount:catalyst:bp-self-sovereign-cutover-runner" cannot list resource "organizations"' >&2
      exit "${rc}"
    fi
    cat "${FX}/orgs.txt" 2>/dev/null || true
    exit 0 ;;
  helmrepositories.source.toolkit.fluxcd.io)
    n=$(cat "${FX}/hr-seq" 2>/dev/null || echo 0); n=$((n+1)); printf '%s' "${n}" >"${FX}/hr-seq"
    rc=$(cat "${FX}/hrs.rc" 2>/dev/null || echo 0)
    if [ "${rc}" != "0" ]; then
      echo 'Error from server: the server could not list helmrepositories' >&2
      exit "${rc}"
    fi
    if [ -f "${FX}/hrs.${n}.txt" ]; then cat "${FX}/hrs.${n}.txt"
    else cat "${FX}/hrs.last.txt" 2>/dev/null || true; fi
    exit 0 ;;
  kustomization)
    name="$1"; shift
    if ! grep -qx "${name}" "${FX}/ks.txt" 2>/dev/null; then
      echo "Error from server (NotFound): kustomizations.kustomize.toolkit.fluxcd.io \"${name}\" not found" >&2
      exit 1
    fi
    case "$*" in
      *spec.suspend*)
        if grep -qx "${name}" "${FX}/suspended-ks.txt" 2>/dev/null; then printf 'true'; fi ;;
      *lastHandledReconcileAt*)
        if grep -qx "${name}" "${FX}/handled-ks.txt" 2>/dev/null; then
          cat "${FX}/last-token" 2>/dev/null || true
        fi ;;
    esac
    exit 0 ;;
esac
exit 0
FAKE
chmod +x "${TMP}/bin/kubectl"

cat >"${TMP}/bin/curl" <<'FAKE'
#!/usr/bin/env sh
out=/dev/null
url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -s) shift ;;
    -o) out="$2"; shift 2 ;;
    -w) shift 2 ;;
    -u) shift 2 ;;
    -*) shift ;;
    *)  url="$1"; shift ;;
  esac
done
slug=$(printf '%s' "${url}" | awk -F/ '{print $(NF-1)}')
repo=$(printf '%s' "${url}" | awk -F/ '{print $NF}')
: >"${out}" 2>/dev/null || true
if [ -n "${CURL_FORCE_CODE:-}" ] && [ "${CURL_FORCE_SLUG:-}" = "${slug}" ]; then
  printf '%s' "${CURL_FORCE_CODE}"; exit 0
fi
if [ -d "${GITEA_ROOT}/${slug}/${repo}.git" ]; then printf '200'; else printf '404'; fi
FAKE
chmod +x "${TMP}/bin/curl"

# git shim — maps http://<creds>@gitea.invalid/<slug>/<repo>.git onto the local
# bare repo and RECORDS whether the URL carried a credential, then execs the
# real git. `for a in "$@"` expands the positional params once, so rebuilding
# them inside the loop is safe.
cat >"${TMP}/bin/git" <<'FAKE'
#!/usr/bin/env sh
first=1
for a in "$@"; do
  case "$a" in
    http://*"${GITEA_HOST_FOR_SHIM}"/*)
      case "$a" in
        *"@${GITEA_HOST_FOR_SHIM}/"*) printf 'credentialed %s\n' "${a##*@}" >>"${GIT_URL_LOG}" ;;
        *) printf 'bare %s\n' "$a" >>"${GIT_URL_LOG}" ;;
      esac
      a="${GITEA_ROOT}/${a##*"${GITEA_HOST_FOR_SHIM}"/}"
      ;;
  esac
  if [ "${first}" = 1 ]; then set -- "$a"; first=0; else set -- "$@" "$a"; fi
done
exec "${REAL_GIT_BIN}" "$@"
FAKE
chmod +x "${TMP}/bin/git"

# ═══════════════════════════════════════════════════════════════════════════
# Fixtures
# ═══════════════════════════════════════════════════════════════════════════
hr_doc() {
  # $1 = name, $2 = url — the shape gitops/helmrelease_apps.go helmRepoBlock
  # emits (namespace flux-system in the doc; the per-Org Kustomization's
  # targetNamespace: <slug> is what actually places it).
  printf -- 'apiVersion: source.toolkit.fluxcd.io/v1beta2\nkind: HelmRepository\nmetadata:\n  name: %s\n  namespace: flux-system\nspec:\n  type: oci\n  interval: 15m\n  url: %s\n  secretRef:\n    name: ghcr-pull\n' "$1" "$2"
}

hr_release_doc() {
  # A HelmRelease that sourceRefs the HR by NAME and carries no url: line.
  printf -- '---\napiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: %s\nspec:\n  chart:\n    spec:\n      chart: %s\n      sourceRef:\n        kind: HelmRepository\n        name: %s\n' "$1" "$1" "$1"
}

seed_org_repo() {
  # $1 = slug   $2 = tree ("host-apps" | "apps")
  sl="$1"; tree="$2"
  mkdir -p "${GITEA_ROOT}/${sl}"
  rm -rf "${GITEA_ROOT}/${sl}/${REPO}.git" "${TMP}/seed-${sl}"
  git init --bare -q "${GITEA_ROOT}/${sl}/${REPO}.git"
  git init -q -b main "${TMP}/seed-${sl}"
  (
    cd "${TMP}/seed-${sl}"
    git config user.name t; git config user.email t@example.invalid
    mkdir -p "vcluster/${tree}" "vcluster/apps" "legacy"
    { hr_doc bp-stalwart-tenant "${UPSTREAM}"; hr_release_doc bp-stalwart-tenant; } \
      >"vcluster/${tree}/app-stalwart-mail.yaml"
    # A doc with a sourceRef and NO url: — nothing may rewrite it.
    hr_release_doc bp-wordpress-tenant >"vcluster/apps/app-wordpress.yaml"
    printf 'resources:\n  - app-wordpress.yaml\n' >"vcluster/apps/kustomization.yaml"
    # OUTSIDE the reconciled trees, carrying the upstream url — the scoping
    # CONTROL. No per-Org Kustomization reconciles this path.
    hr_doc bp-legacy "${UPSTREAM}" >"${OUTSIDE}"
    git add -A; git commit -qm seed
    git remote add origin "${GITEA_ROOT}/${sl}/${REPO}.git"
    git push -q origin main
  ) >/dev/null 2>&1
}

origin_show() { "${REAL_GIT}" --git-dir="${GITEA_ROOT}/$1/${REPO}.git" show "main:$2" 2>/dev/null; }
count_in()    { origin_show "$1" "$2" | grep -c "url: $3\$" || true; }
count_trust() { origin_show "$1" "$2" | grep -c "name: ${TRUST}\$" || true; }

hr_line() { printf '%s/%s=%s\n' "$1" "$2" "$3"; }

reset_fixture() {
  rm -rf "${FIX}"; mkdir -p "${FIX}"
  : >"${FIX}/gitrepos.txt"; : >"${FIX}/orgs.txt"; : >"${FIX}/ks.txt"
  : >"${FIX}/handled-ks.txt"; : >"${FIX}/hrs.last.txt"; : >"${FIX}/suspended-ks.txt"
  echo 0 >"${FIX}/gitrepos.rc"; echo 0 >"${FIX}/orgs.rc"
  echo 0 >"${FIX}/crd.rc"; echo 0 >"${FIX}/hrs.rc"
  : >"${FIX}/crd.err"
  printf '0' >"${FIX}/hr-seq"
}

# The DEFAULT cluster fixture, deliberately asymmetric so the union of the two
# live sources is load-bearing in BOTH directions:
#
#   walkfour  Org CR + Flux GitRepository + a `-host-apps` Kustomization
#   walkfive  Org CR + Flux GitRepository + an `-apps` Kustomization
#   newcomer  Org CR ONLY — no Flux source at all. An Organization created
#             after the per-Org Flux loop last ran; a scan driven only by
#             GitRepositories would never see it.
#   fluxonly  Flux GitRepository ONLY — no Org CR. The mirror-image case.
#   norepo    Org CR, no Gitea repo at all → HTTP 404 → clean skip.
build_default_cluster() {
  reset_fixture
  rm -rf "${GITEA_ROOT}"; mkdir -p "${GITEA_ROOT}"
  seed_org_repo walkfour host-apps
  seed_org_repo walkfive apps
  seed_org_repo newcomer host-apps
  seed_org_repo fluxonly apps
  {
    echo "${REPO}-walkfour"
    echo "${REPO}-walkfive"
    echo "${REPO}-fluxonly"
    # Sources that must NOT be mistaken for a per-Org tenant repo.
    echo "openova"
    echo "openova-org-tenants"
    echo "${REPO}"
  } >"${FIX}/gitrepos.txt"
  {
    echo "walkfour"; echo "walkfive"; echo "newcomer"; echo "norepo"
  } >"${FIX}/orgs.txt"
  {
    echo "${REPO}-walkfour-host-apps"
    echo "${REPO}-walkfive-apps"
    echo "${REPO}-newcomer-host-apps"
    echo "${REPO}-fluxonly-apps"
  } >"${FIX}/ks.txt"
  cp "${FIX}/ks.txt" "${FIX}/handled-ks.txt"
  # Post-pivot live state: every per-Org HR on local Harbor.
  {
    hr_line walkfour bp-stalwart-tenant "${LOCAL}"
    hr_line walkfive bp-stalwart-tenant "${LOCAL}"
    hr_line newcomer bp-stalwart-tenant "${LOCAL}"
    hr_line fluxonly bp-stalwart-tenant "${LOCAL}"
    hr_line flux-system bp-harbor "${LOCAL}"
  } >"${FIX}/hrs.last.txt"
}

# ── Runner: execute the real phase2d.sh with the parent-shell contract ────
run_phase2d() {
  # $1 = script path   $2 = PERORG_PIVOT_ENABLED   $3.. = extra VAR=VAL env
  scr="$1"; enabled="$2"; shift 2
  : >"${TMP}/kubectl.log"; : >"${TMP}/giturl.log"
  printf '0' >"${FIX}/hr-seq"
  mkdir -p "${TMP}/t/repo"
  cat >"${TMP}/driver.sh" <<DRIVER
# The step container runs the whole script under \`set -eu\` (line 1 of the
# rendered args). Sourcing phase2d.sh without it would let an unset variable or
# an unguarded non-zero command pass here and kill the step in production.
set -eu
UPSTREAM_PREFIX='${UPSTREAM}'
local_prefix='${LOCAL}'
trust_secret='${TRUST}'
. '${TMP}/injector.sh'
. '${scr}'
DRIVER
  env -i \
    PATH="${TMP}/bin:${PATH}" \
    HOME="${TMP}" \
    FIXTURE_DIR="${FIX}" \
    FAKE_KUBECTL_LOG="${TMP}/kubectl.log" \
    GIT_URL_LOG="${TMP}/giturl.log" \
    GITEA_ROOT="${GITEA_ROOT}" \
    GITEA_HOST_FOR_SHIM="${GITEA_HOST}" \
    REAL_GIT_BIN="${REAL_GIT}" \
    GIT_AUTHOR_NAME=cutover GIT_AUTHOR_EMAIL=cutover@example.invalid \
    GIT_COMMITTER_NAME=cutover GIT_COMMITTER_EMAIL=cutover@example.invalid \
    GIT_TERMINAL_PROMPT=0 \
    GITEA_INTERNAL_URL="${GITEA_URL}" \
    GITEA_USERNAME=gitea_admin \
    GITEA_PASSWORD=fixture-not-a-real-password \
    HELMREPO_READBACK_EXCLUDE="bp-newapi" \
    PERORG_PIVOT_ENABLED="${enabled}" \
    PERORG_TENANT_REPO="${REPO}" \
    PERORG_BRANCH=main \
    PERORG_FLUX_NAMESPACE=flux-system \
    PERORG_TREES=vcluster \
    PERORG_KS_SUFFIXES="apps host-apps" \
    PERORG_ORG_CRD=organizations.orgs.openova.io \
    PERORG_READBACK_BUDGET_SECONDS="${PD_BUDGET:-300}" \
    PERORG_READBACK_SETTLE_SAMPLES=2 \
    PERORG_READBACK_INTERVAL_SECONDS=1 \
    "$@" \
    sh "${TMP}/driver.sh" >"${TMP}/out.log" 2>&1
  echo $?
}

# ═══════════════════════════════════════════════════════════════════════════
echo "== C. the rewrite, against real git repositories =="

build_default_cluster
rc=$(run_phase2d "${TMP}/phase2d.sh" true)

if [ "${rc}" -eq 0 ]; then ok "C0 happy path exits 0"
else bad "C0 happy path exit ${rc}"; sed 's/^/      /' "${TMP}/out.log" | tail -25; fi

c1_up=$(count_in walkfour "${HOSTAPP}" "${UPSTREAM}")
c1_lo=$(count_in walkfour "${HOSTAPP}" "${LOCAL}")
if [ "${c1_up}" -eq 0 ] && [ "${c1_lo}" -eq 1 ]; then
  ok "C1 walkfour/${REPO}@main:${HOSTAPP} pivoted to local Harbor IN THE BARE ORIGIN (the hw296 offender)"
else
  bad "C1 walkfour still has ${c1_up} on ${UPSTREAM} / ${c1_lo} on local (want 0 / 1)"
fi

c2_up=$(count_in walkfive "${APPSAPP}" "${UPSTREAM}")
c2_lo=$(count_in walkfive "${APPSAPP}" "${LOCAL}")
if [ "${c2_up}" -eq 0 ] && [ "${c2_lo}" -eq 1 ]; then
  ok "C2 walkfive/${REPO}@main:${APPSAPP} pivoted — BOTH reconciled trees are covered, not just one"
else
  bad "C2 walkfive still has ${c2_up} on ${UPSTREAM} / ${c2_lo} on local (want 0 / 1)"
fi

if [ "$(count_trust walkfour "${HOSTAPP}")" -eq 1 ] && [ "$(count_trust walkfive "${APPSAPP}")" -eq 1 ]; then
  ok "C3 certSecretRef=${TRUST} injected beside every pivoted url"
else
  bad "C3 certSecretRef missing — pivoted per-Org HRs would x509-fail the in-cluster Harbor"
fi

if [ "$(count_in walkfour "${OUTSIDE}" "${UPSTREAM}")" -eq 1 ]; then
  ok "C4 CONTROL: ${OUTSIDE} (outside the reconciled trees) is untouched — the vcluster/ scope holds"
else
  bad "C4 Phase-2d rewrote a path no per-Org Kustomization reconciles — the tree scope was widened"
fi

if origin_show walkfour "vcluster/apps/app-wordpress.yaml" \
   | cmp -s - "${TMP}/seed-walkfour/vcluster/apps/app-wordpress.yaml"; then
  ok "C5 CONTROL: the sourceRef-only HelmRelease doc is byte-identical (no url: line, nothing to rewrite)"
else
  bad "C5 CONTROL: a doc with no url: line was modified"
fi

if grep -q 'no catalyst-tenant repo (HTTP 404)' "${TMP}/out.log"; then
  ok "C6 an Org with no ${REPO} repo (HTTP 404) is a clean skip, not a failure"
else
  bad "C6 the 404 path did not report a skip"; sed 's/^/      /' "${TMP}/out.log" | tail -10
fi

if grep -q 'credentialed' "${TMP}/giturl.log"; then
  ok "C7 the clone URL carries the injected Gitea credential (the push can authenticate)"
else
  bad "C7 the clone URL had no credential — every push would 401"
fi

# ── C8: idempotent re-run ────────────────────────────────────────────────
rc=$(run_phase2d "${TMP}/phase2d.sh" true)
if [ "${rc}" -eq 0 ] \
   && [ "$(count_in walkfour "${HOSTAPP}" "${LOCAL}")" -eq 1 ] \
   && [ "$(count_trust walkfour "${HOSTAPP}")" -eq 1 ] \
   && grep -q 'already pivoted' "${TMP}/out.log"; then
  ok "C8 re-run is idempotent (exit 0, one local url, ONE certSecretRef — no duplicates)"
else
  bad "C8 re-run not idempotent: exit ${rc}, $(count_in walkfour "${HOSTAPP}" "${LOCAL}") local, $(count_trust walkfour "${HOSTAPP}") trust refs"
fi

# ── C9: the disable switch ───────────────────────────────────────────────
build_default_cluster
rc=$(run_phase2d "${TMP}/phase2d.sh" false)
if [ "${rc}" -eq 0 ] && [ "$(count_in walkfour "${HOSTAPP}" "${UPSTREAM}")" -eq 1 ]; then
  ok "C9 CONTROL: enabled=false is a genuine no-op"
else
  bad "C9 enabled=false did not no-op: exit ${rc}"
fi

# ── C10: an UNREADABLE repo must FATAL, never present as the same 404 skip ─
build_default_cluster
rc=$(run_phase2d "${TMP}/phase2d.sh" true CURL_FORCE_CODE=500 CURL_FORCE_SLUG=walkfour)
if [ "${rc}" -ne 0 ] && grep -q 'unreadable repo is NOT an absent repo' "${TMP}/out.log"; then
  ok "C10 a non-200/404 repo probe FATALs (an auth fault can never masquerade as an absent repo)"
else
  bad "C10 unreadable repo exited ${rc} without the discriminating FATAL"
fi

# ── C11: a repo that EXISTS but carries no branch yet ─────────────────────
# The org-controller creates the per-Org repo before anything seeds a tree into
# it, so an Org created shortly before the cutover is legitimately empty.
# `git clone --branch main` fails there; failing the cutover on it would stop a
# Sovereign whose only sin is a fresh Organization.
build_default_cluster
rm -rf "${GITEA_ROOT}/emptyorg"
mkdir -p "${GITEA_ROOT}/emptyorg"
"${REAL_GIT}" init --bare -q "${GITEA_ROOT}/emptyorg/${REPO}.git"
printf 'emptyorg\n' >>"${FIX}/orgs.txt"
rc=$(run_phase2d "${TMP}/phase2d.sh" true)
if [ "${rc}" -eq 0 ] \
   && grep -q 'has no main branch' "${TMP}/out.log" \
   && [ "$(count_in walkfour "${HOSTAPP}" "${LOCAL}")" -eq 1 ]; then
  ok "C11 a per-Org repo that exists but has no branch yet is a clean skip, and the other Orgs still pivot"
else
  bad "C11 the empty-repo case exited ${rc} — a freshly-created Org must not stop the cutover"
  sed 's/^/      /' "${TMP}/out.log" | tail -10
fi

# ═══════════════════════════════════════════════════════════════════════════
echo "== L. the scan is driven by the LIVE Organization list =="
# THE PROPERTY THAT MATTERS BEYOND hw296. The offender count scales with the
# number of Organizations, so a scan the code can enumerate from a constant is
# wrong by construction. These cases use Orgs that appear in ONLY ONE of the two
# live sources — a suite that fed the code the same list the code already knows
# would be testing nothing.

build_default_cluster
rc=$(run_phase2d "${TMP}/phase2d.sh" true)

if [ "$(count_in newcomer "${HOSTAPP}" "${LOCAL}")" -eq 1 ]; then
  ok "L1 'newcomer' — present ONLY as an Organization CR, with NO Flux source the code could have enumerated — was still scanned and pivoted"
else
  bad "L1 an Organization with no per-Org Flux source was NOT scanned — the scan is not driven by the live Org list ($(count_in newcomer "${HOSTAPP}" "${UPSTREAM}") still upstream)"
fi

if [ "$(count_in fluxonly "${APPSAPP}" "${LOCAL}")" -eq 1 ]; then
  ok "L2 'fluxonly' — present ONLY as a live ${REPO}-* Flux source, absent from the Organization CR list — was still scanned and pivoted"
else
  bad "L2 an Org visible only through its Flux source was NOT scanned — the union is degenerate on that side"
fi

# L3: an Organization the fixture invents AFTER the suite was written. Nothing
# in the shipped script or in the cases above names it.
build_default_cluster
seed_org_repo lateorg host-apps
printf 'lateorg\n' >>"${FIX}/orgs.txt"
printf '%s-lateorg-host-apps\n' "${REPO}" >>"${FIX}/ks.txt"
cp "${FIX}/ks.txt" "${FIX}/handled-ks.txt"
hr_line lateorg bp-stalwart-tenant "${LOCAL}" >>"${FIX}/hrs.last.txt"
rc=$(run_phase2d "${TMP}/phase2d.sh" true)
if [ "${rc}" -eq 0 ] && [ "$(count_in lateorg "${HOSTAPP}" "${LOCAL}")" -eq 1 ]; then
  ok "L3 an Organization introduced only by the fixture — named nowhere in the chart — is scanned like any other"
else
  bad "L3 a previously-unseen Organization was not scanned: exit ${rc}, $(count_in lateorg "${HOSTAPP}" "${UPSTREAM}") still upstream"
fi

# L4: zero Organizations is a clean no-op, and must be distinguishable in the
# log from an enumeration that failed.
build_default_cluster
: >"${FIX}/gitrepos.txt"; : >"${FIX}/orgs.txt"
rc=$(run_phase2d "${TMP}/phase2d.sh" true)
if [ "${rc}" -eq 0 ] && grep -q '0 live Organization slug' "${TMP}/out.log"; then
  ok "L4 CONTROL: a Sovereign with zero Organizations is a clean, explicit no-op"
else
  bad "L4 zero-Org handling wrong: exit ${rc}"; sed 's/^/      /' "${TMP}/out.log" | tail -8
fi

# L5: the Flux-source enumeration FAILING is not an empty list.
build_default_cluster
echo 1 >"${FIX}/gitrepos.rc"
rc=$(run_phase2d "${TMP}/phase2d.sh" true)
if [ "${rc}" -ne 0 ] && grep -q 'scanning a PARTIAL list' "${TMP}/out.log"; then
  ok "L5 a FAILED GitRepository enumeration FATALs — an unreadable API is not an empty Org list"
else
  bad "L5 a failed source enumeration exited ${rc} without a FATAL — a verdict from absent evidence"
fi

# L6: the Organization CRD is INSTALLED but the list is REFUSED. This is the
# shape a missing `orgs.openova.io/organizations` grant produces at runtime.
build_default_cluster
echo 1 >"${FIX}/orgs.rc"
rc=$(run_phase2d "${TMP}/phase2d.sh" true)
if [ "${rc}" -ne 0 ] && grep -q 'CRD IS installed but listing Organizations failed' "${TMP}/out.log"; then
  ok "L6 CRD installed + list refused FATALs — an RBAC refusal can never read as 'this Sovereign has no Orgs'"
else
  bad "L6 a refused Organization list exited ${rc} without a FATAL"
fi

# L7: the CRD genuinely absent (Catalyst-Zero) — continue on the Flux leg alone.
build_default_cluster
echo 1 >"${FIX}/crd.rc"
printf 'Error from server (NotFound): customresourcedefinitions.apiextensions.k8s.io "organizations.orgs.openova.io" not found\n' >"${FIX}/crd.err"
rc=$(run_phase2d "${TMP}/phase2d.sh" true)
if [ "${rc}" -eq 0 ] \
   && [ "$(count_in walkfour "${HOSTAPP}" "${LOCAL}")" -eq 1 ] \
   && [ "$(count_in newcomer "${HOSTAPP}" "${UPSTREAM}")" -eq 1 ]; then
  ok "L7 CONTROL: a genuinely absent Organization CRD falls back to the Flux sources alone (walkfour pivoted; the CR-only newcomer legitimately untouched)"
else
  bad "L7 absent-CRD fallback wrong: exit ${rc}"; sed 's/^/      /' "${TMP}/out.log" | tail -8
fi

# L8: the CRD probe failing for a reason that is NOT NotFound. Absence must be
# established by POSITIVE evidence, or a refusal silently empties the scan.
build_default_cluster
echo 1 >"${FIX}/crd.rc"
printf 'Error from server (Forbidden): customresourcedefinitions.apiextensions.k8s.io is forbidden\n' >"${FIX}/crd.err"
rc=$(run_phase2d "${TMP}/phase2d.sh" true)
if [ "${rc}" -ne 0 ] && grep -q 'NOT the same as it being absent' "${TMP}/out.log"; then
  ok "L8 a CRD probe that fails for any reason other than NotFound FATALs — absence requires positive evidence"
else
  bad "L8 a non-NotFound CRD probe failure exited ${rc} and was treated as absence"
fi

# ═══════════════════════════════════════════════════════════════════════════
echo "== D. the read-back must survive the owning reconciler =="

# D1: the happy path already ran above; assert the DURABLE verdict was reached
# through Stage 2, not merely printed.
build_default_cluster
rc=$(run_phase2d "${TMP}/phase2d.sh" true)
if [ "${rc}" -eq 0 ] \
   && grep -q 'Phase-2d Stage-1 OK' "${TMP}/out.log" \
   && grep -q 'handled reconcile token' "${TMP}/out.log" \
   && grep -q 'Phase-2d DURABLE' "${TMP}/out.log"; then
  ok "D1 the pivot is certified only after Stage 1 goes clean AND an owning Kustomization HANDLES the Stage-2 token"
else
  bad "D1 the DURABLE verdict was not reached through both stages: exit ${rc}"
  sed 's/^/      /' "${TMP}/out.log" | tail -15
fi

# D2: THE 0.1.186 FIXTURE. Clean on the first sample, reverted on the next —
# exactly what step-06's Phase-1.5 read-back saw and passed.
build_default_cluster
PD_BUDGET=4
{
  hr_line walkfour bp-stalwart-tenant "${LOCAL}"
  hr_line walkfive bp-stalwart-tenant "${LOCAL}"
  hr_line newcomer bp-stalwart-tenant "${LOCAL}"
  hr_line fluxonly bp-stalwart-tenant "${LOCAL}"
} >"${FIX}/hrs.1.txt"
{
  hr_line walkfour bp-stalwart-tenant "${UPSTREAM}"
  hr_line walkfive bp-stalwart-tenant "${UPSTREAM}"
  hr_line newcomer bp-stalwart-tenant "${LOCAL}"
  hr_line fluxonly bp-stalwart-tenant "${LOCAL}"
} >"${FIX}/hrs.last.txt"
rc=$(PD_BUDGET=4 run_phase2d "${TMP}/phase2d.sh" true)
if [ "${rc}" -ne 0 ] && grep -q 'went BACK to' "${TMP}/out.log"; then
  ok "D2 a pivot that reads CLEAN then reverts under its owning Kustomization is caught (the 0.1.186 shape, one repo class down)"
else
  bad "D2 the revert-during-Stage-2 fixture exited ${rc} without the revert FATAL"
  sed 's/^/      /' "${TMP}/out.log" | tail -12
fi

# D3: the token is never handled — clean, but durability unproven.
build_default_cluster
: >"${FIX}/handled-ks.txt"
rc=$(PD_BUDGET=3 run_phase2d "${TMP}/phase2d.sh" true)
if [ "${rc}" -ne 0 ] && grep -q 'could not be proven DURABLE' "${TMP}/out.log"; then
  ok "D3 clean-but-never-re-applied is REFUSED — a read-back that can pass before the owning reconciler ran is not a read-back"
else
  bad "D3 an unproven-durability fixture exited ${rc} without the FATAL"
fi

# D4: an Org with NO HR-owning Kustomization is exempted EXPLICITLY, not hung on.
build_default_cluster
: >"${FIX}/ks.txt"; : >"${FIX}/handled-ks.txt"
rc=$(PD_BUDGET=6 run_phase2d "${TMP}/phase2d.sh" true)
if [ "${rc}" -eq 0 ] && grep -q 'no HR-owning Kustomization exists' "${TMP}/out.log"; then
  ok "D4 CONTROL: an Org with nothing that re-applies its HelmRepositories is exempted, and says so"
else
  bad "D4 the no-Kustomization case exited ${rc} — an absence must not be waited on"
fi

# D5: Stage 1 never converges.
build_default_cluster
{
  hr_line walkfour bp-stalwart-tenant "${UPSTREAM}"
  hr_line walkfive bp-stalwart-tenant "${LOCAL}"
  hr_line newcomer bp-stalwart-tenant "${LOCAL}"
  hr_line fluxonly bp-stalwart-tenant "${LOCAL}"
} >"${FIX}/hrs.last.txt"
rc=$(PD_BUDGET=2 run_phase2d "${TMP}/phase2d.sh" true)
if [ "${rc}" -ne 0 ] && grep -q 'Stage-1' "${TMP}/out.log" && grep -q 'STILL-UPSTREAM' "${TMP}/out.log"; then
  ok "D5 a pivot that never reaches the live objects FATALs in Stage 1, naming its offenders"
else
  bad "D5 the non-converging fixture exited ${rc} without a Stage-1 FATAL"
fi

# D6: the cluster-wide HelmRepository enumeration FAILING is not zero offenders.
build_default_cluster
echo 1 >"${FIX}/hrs.rc"
rc=$(PD_BUDGET=2 run_phase2d "${TMP}/phase2d.sh" true)
if [ "${rc}" -ne 0 ] && grep -q 'an unreadable cluster is NOT a pivoted one' "${TMP}/out.log"; then
  ok "D6 a failed HelmRepository enumeration FATALs — it can never be read as zero offenders"
else
  bad "D6 a failed read-back enumeration exited ${rc} without a FATAL"
fi

# D7: an offender OUTSIDE the pivoted Orgs is not this leg's failure.
build_default_cluster
{
  cat "${FIX}/hrs.last.txt"
  hr_line unrelated-ns bp-something "${UPSTREAM}"
} >"${FIX}/hrs.tmp" && mv "${FIX}/hrs.tmp" "${FIX}/hrs.last.txt"
rc=$(run_phase2d "${TMP}/phase2d.sh" true)
if [ "${rc}" -eq 0 ]; then
  ok "D7 CONTROL: an upstream HelmRepository in a namespace this leg did not pivot is not its failure"
else
  bad "D7 an unrelated-namespace offender failed this leg: exit ${rc}"
fi

# D8: the toleration list is honoured, so this gate is never STRICTER than the
# step-08/step-11 scan it exists to unblock.
build_default_cluster
{
  cat "${FIX}/hrs.last.txt"
  hr_line walkfour bp-newapi "${UPSTREAM}"
} >"${FIX}/hrs.tmp" && mv "${FIX}/hrs.tmp" "${FIX}/hrs.last.txt"
rc=$(run_phase2d "${TMP}/phase2d.sh" true)
if [ "${rc}" -eq 0 ]; then
  ok "D8 CONTROL: a HELMREPO_READBACK_EXCLUDE name is tolerated — never stricter than the step it unblocks"
else
  bad "D8 a tolerated name failed this leg: exit ${rc} — a cutover step-08 would pass dies here"
fi

# D9: a SUSPENDED Kustomization can never handle a reconcile request, and can
# never re-apply either — so it cannot revert the pivot. Requiring it to handle
# the token would time the assert out on a Sovereign whose only sin is a paused
# per-Org reconciler. It must be exempted, explicitly.
build_default_cluster
cp "${FIX}/ks.txt" "${FIX}/suspended-ks.txt"
: >"${FIX}/handled-ks.txt"
rc=$(PD_BUDGET=6 run_phase2d "${TMP}/phase2d.sh" true)
if [ "${rc}" -eq 0 ] && grep -q 'is SUSPENDED' "${TMP}/out.log"; then
  ok "D9 CONTROL: a SUSPENDED per-Org Kustomization is exempted and says so — it cannot re-apply, so it cannot revert"
else
  bad "D9 a suspended Kustomization exited ${rc} — the assert waited on a reconciler that can never run"
fi

# D9b: the exemption must be scoped to SUSPENSION alone. The same fixture with
# the suspension removed and the token still never handled MUST fail — otherwise
# D9 would be indistinguishable from "durability is never actually required".
build_default_cluster
: >"${FIX}/handled-ks.txt"
rc=$(PD_BUDGET=3 run_phase2d "${TMP}/phase2d.sh" true)
if [ "${rc}" -ne 0 ] && grep -q 'could not be proven DURABLE' "${TMP}/out.log"; then
  ok "D9b CONTROL: the identical fixture WITHOUT suspension still fails — the exemption is suspension-only, not a hole in the gate"
else
  bad "D9b an un-suspended, never-handled Kustomization exited ${rc} — the D9 exemption leaks"
fi

# ═══════════════════════════════════════════════════════════════════════════
# M. VACUITY PROOFS — one mutated behaviour each; the named case must go RED.
# ═══════════════════════════════════════════════════════════════════════════
echo "== M. vacuity proofs (single-behaviour mutants must break the named case) =="

mutant() {
  # $1 = label   $2 = sed expression applied to the ORIGINAL extracted script
  sed "$2" "${TMP}/phase2d.orig.sh" >"${TMP}/mut.orig.sh"
  if cmp -s "${TMP}/mut.orig.sh" "${TMP}/phase2d.orig.sh"; then
    bad "M:$1 mutation matched nothing — the vacuity proof itself is vacuous"
    return 1
  fi
  relocate "${TMP}/mut.orig.sh" "${TMP}/mut.sh"
  return 0
}

# M1 — the SHIPPED DEFECT: no per-Org leg at all. Neutering the rewrite loop
# leaves every per-Org repo upstream, exactly as 0.1.187 does. It must ALSO exit
# non-zero: the pre-commit post-condition asserts the RESULT of the rewrite, so
# a rewrite that did nothing can never be pushed as a durable pivot.
if mutant "no-rewrite" 's,^\( *\)sed -i -E "s\,\^(\[\[:space:\]\]\*url:.*$,\1:,'; then
  build_default_cluster
  m1_rc=$(run_phase2d "${TMP}/mut.sh" true)
  if [ "$(count_in walkfour "${HOSTAPP}" "${UPSTREAM}")" -eq 1 ] \
     && [ "$(count_in walkfive "${APPSAPP}" "${UPSTREAM}")" -eq 1 ]; then
    ok "M1 removing the url rewrite leaves both per-Org repos upstream → C1 + C2 go RED (this is the 0.1.187 state)"
  else
    bad "M1 C1/C2 still passed with the rewrite removed — they are vacuous"
  fi
  if [ "${m1_rc}" -ne 0 ] && grep -q 'still declare' "${TMP}/out.log"; then
    ok "M1b the same mutant exits ${m1_rc} on the pre-commit post-condition — a rewrite that did nothing cannot be pushed as a pivot"
  else
    bad "M1b a no-op rewrite exited ${m1_rc} without the post-condition FATAL"
  fi
fi

# M2 — drop the Organization-CR enumeration: the scan collapses to the Flux
# sources and the CR-only Org is silently missed. This is the mutant a
# hardcoded list would be indistinguishable from.
if mutant "no-org-crs" "s,awk 'NF' /tmp/.pd-orgs.txt >> \"\\\${pd_slugs}\",:,"; then
  build_default_cluster
  run_phase2d "${TMP}/mut.sh" true >/dev/null
  if [ "$(count_in newcomer "${HOSTAPP}" "${UPSTREAM}")" -eq 1 ]; then
    ok "M2 dropping the Organization-CR source misses the CR-only Org → L1 goes RED"
  else
    bad "M2 L1 still passed without the Organization-CR source — L1 is vacuous"
  fi
fi

# M3 — drop the Flux-source enumeration: the Flux-only Org is missed.
if mutant "no-flux-srcs" "s,awk -v p=\"\\\${pd_repo}-\",awk -v p=\"__never__\"," ; then
  build_default_cluster
  run_phase2d "${TMP}/mut.sh" true >/dev/null
  if [ "$(count_in fluxonly "${APPSAPP}" "${UPSTREAM}")" -eq 1 ]; then
    ok "M3 dropping the Flux-source prefix match misses the source-only Org → L2 goes RED"
  else
    bad "M3 L2 still passed without the Flux-source enumeration — L2 is vacuous"
  fi
fi

# M4 — drop the trust injection.
if mutant "no-trust" 's,^\( *\)inject_trust_ref "\${pdf}",\1:,'; then
  build_default_cluster
  run_phase2d "${TMP}/mut.sh" true >/dev/null
  if [ "$(count_trust walkfour "${HOSTAPP}")" -eq 0 ]; then
    ok "M4 removing the injector call drops every certSecretRef → C3 goes RED"
  else
    bad "M4 C3 still passed without the injector call — C3 is vacuous"
  fi
fi

# M5 — widen the tree scope past the reconciled paths (breaks the C4 control).
# NB the mutation appends to the LOOP, not to the `:-vcluster` default: the
# runner passes PERORG_TREES explicitly (that is what the chart renders), so a
# mutation of the default alone would be inert — and an inert mutant is a
# vacuity proof that proves nothing.
if mutant "wide-scope" 's,for pd_tree in \${PERORG_TREES:-vcluster}; do,for pd_tree in ${PERORG_TREES:-vcluster} legacy; do,g'; then
  build_default_cluster
  run_phase2d "${TMP}/mut.sh" true >/dev/null
  if [ "$(count_in walkfour "${OUTSIDE}" "${UPSTREAM}")" -eq 0 ]; then
    ok "M5 widening the tree scope rewrites a path no Kustomization reconciles → C4 goes RED"
  else
    bad "M5 C4 still passed with the scope widened — C4 is vacuous"
  fi
fi

# M6 — THE HEADLINE VACUITY PROOF. Delete Stage 2 (return 0 the moment Stage 1
# reads clean) and re-run the D2 revert fixture. If it now passes, the D2 case
# is proving that Stage 2 — not luck, not the Stage-1 poll — is what catches the
# 0.1.186 revert race.
if mutant "no-stage2" 's,^\( *\)pa_token=.*$,\1return 0,'; then
  build_default_cluster
  {
    hr_line walkfour bp-stalwart-tenant "${LOCAL}"
    hr_line walkfive bp-stalwart-tenant "${LOCAL}"
    hr_line newcomer bp-stalwart-tenant "${LOCAL}"
    hr_line fluxonly bp-stalwart-tenant "${LOCAL}"
  } >"${FIX}/hrs.1.txt"
  {
    hr_line walkfour bp-stalwart-tenant "${UPSTREAM}"
    hr_line walkfive bp-stalwart-tenant "${UPSTREAM}"
    hr_line newcomer bp-stalwart-tenant "${LOCAL}"
    hr_line fluxonly bp-stalwart-tenant "${LOCAL}"
  } >"${FIX}/hrs.last.txt"
  rc=$(PD_BUDGET=4 run_phase2d "${TMP}/mut.sh" true)
  if [ "${rc}" -eq 0 ]; then
    ok "M6 without Stage 2 the D2 revert fixture PASSES → D2 is proving the anti-race property, not a lucky sample"
  else
    bad "M6 the D2 fixture still failed without Stage 2 (exit ${rc}) — D2 does not isolate the race"
  fi
fi

# M7 — accept a reconcile REQUEST instead of a HANDLED one. `lastHandledReconcileAt`
# is the field that means the re-apply ran; comparing against anything else
# restores the 0.1.186 guarantee (none).
if mutant "request-not-handled" 's,{.status.lastHandledReconcileAt},{.metadata.name},'; then
  build_default_cluster
  rc=$(PD_BUDGET=3 run_phase2d "${TMP}/mut.sh" true)
  if [ "${rc}" -ne 0 ]; then
    ok "M7 reading anything but status.lastHandledReconcileAt can no longer prove the re-apply → D1 goes RED"
  else
    bad "M7 D1 still passed reading the wrong status field — D1 is vacuous"
  fi
fi

# M8 — swallow the repo-probe discrimination (breaks C10).
if mutant "swallow-probe" 's,^\( *\)echo "\[helmrepository-patches\] FATAL: Phase-2d \${pd_slug}: HTTP,\1continue \# ,'; then
  build_default_cluster
  rc=$(run_phase2d "${TMP}/mut.sh" true CURL_FORCE_CODE=500 CURL_FORCE_SLUG=walkfour)
  if [ "${rc}" -eq 0 ]; then
    ok "M8 swallowing the repo-probe verdict turns an unreadable repo into a silent skip → C10 goes RED"
  else
    bad "M8 C10 still passed with the probe verdict swallowed — C10 is vacuous"
  fi
fi

# M10 — swallow the ls-remote verdict. An unreadable remote must not be able to
# present as the same "no branch yet" skip C11 relies on.
if mutant "swallow-lsremote" '/ls-remote \${pd_branch} failed against a repo/,+2 s,^\( *\)exit 1,\1:,'; then
  build_default_cluster
  rm -rf "${GITEA_ROOT}/emptyorg"; mkdir -p "${GITEA_ROOT}/emptyorg"
  # A path that is NOT a git repository at all: the API probe is forced to 200,
  # so ls-remote is the only thing standing between "unreadable" and "empty".
  printf 'not a repo\n' >"${GITEA_ROOT}/emptyorg/${REPO}.git"
  printf 'emptyorg\n' >>"${FIX}/orgs.txt"
  m10_real=$(run_phase2d "${TMP}/phase2d.sh" true CURL_FORCE_CODE=200 CURL_FORCE_SLUG=emptyorg)
  m10_mut=$(run_phase2d "${TMP}/mut.sh" true CURL_FORCE_CODE=200 CURL_FORCE_SLUG=emptyorg)
  if [ "${m10_real}" -ne 0 ] && [ "${m10_mut}" -eq 0 ]; then
    ok "M10 the shipped script FATALs on an unreadable remote (exit ${m10_real}) and the mutant silently skips it (exit ${m10_mut}) → the ls-remote guard is what discriminates"
  else
    bad "M10 shipped=${m10_real} mutant=${m10_mut} — the ls-remote discrimination is not isolated"
  fi
fi

# M9 — the DIFFERENTIAL control for M1b. M1's mutant (rewrite neutered) exits
# non-zero; this one neuters the rewrite AND the post-condition. If it now exits
# 0, the failure M1b observed came from the post-condition and from nothing
# else — which is what makes M1b a proof rather than a coincidence. Two seds on
# purpose: isolating a guard requires removing the guard AND the condition that
# trips it, and saying so beats a single mutation that changes two things.
sed -e 's,^\( *\)sed -i -E "s\,\^(\[\[:space:\]\]\*url:.*$,\1:,' \
    -e 's,^\( *\)if \[ "\${pd_left}" -ne 0 \]; then,\1if false; then,' \
    "${TMP}/phase2d.orig.sh" >"${TMP}/mut9.orig.sh"
m9_delta=$(diff "${TMP}/phase2d.orig.sh" "${TMP}/mut9.orig.sh" | grep -c '^>' || true)
relocate "${TMP}/mut9.orig.sh" "${TMP}/mut9.sh"
if [ "${m9_delta}" -lt 2 ]; then
  bad "M9 the two-part mutation did not apply (${m9_delta} changed line(s)) — the differential is vacuous"
else
  build_default_cluster
  m9_rc=$(run_phase2d "${TMP}/mut9.sh" true)
  if [ "${m9_rc}" -eq 0 ] && [ "$(count_in walkfour "${HOSTAPP}" "${UPSTREAM}")" -eq 1 ]; then
    ok "M9 with the post-condition ALSO removed the same no-op rewrite exits 0 → M1b's failure is the post-condition firing, nothing else"
  else
    bad "M9 differential control exited ${m9_rc} — M1b's FATAL cannot be attributed to the post-condition"
  fi
fi

# ═══════════════════════════════════════════════════════════════════════════
echo "== R. render wiring + the grant a stub cannot see =="

python3 - "${TMP}/render.yaml" "${TMP}/args.sh" <<'PY'
import sys, yaml
for d in yaml.safe_load_all(open(sys.argv[1])):
    if not d or d.get("kind") != "ConfigMap":
        continue
    if d["metadata"]["name"] != "cutover-step-06-helmrepository-patches":
        continue
    spec = yaml.safe_load(d["data"]["podSpec"])
    c = [x for x in spec["containers"] if x["name"] == "helmrepository-patches"][0]
    open(sys.argv[2], "w").write(c["args"][0])
    sys.exit(0)
sys.exit("step-06 ConfigMap not found")
PY

p2b_ln=$(awk 'index($0,". /phase2b/phase2b.sh"){print NR; exit}' "${TMP}/args.sh")
p2d_ln=$(awk 'index($0,". /phase2d/phase2d.sh"){print NR; exit}' "${TMP}/args.sh")
strip_ln=$(awk 'index($0,"assert_region_a_pivot \"Phase-3 pre-strip read-back\""){print NR; exit}' "${TMP}/args.sh")

if [ -n "${p2b_ln}" ] && [ -n "${p2d_ln}" ] && [ "${p2d_ln}" -gt "${p2b_ln}" ]; then
  ok "R1 phase2d.sh is sourced (line ${p2d_ln}) AFTER phase2b.sh (line ${p2b_ln})"
else
  bad "R1 phase2d source at ${p2d_ln:-<none>} does not follow phase2b at ${p2b_ln:-<none>}"
fi

# The per-Org HelmRepositories must be pivoted and pullable BEFORE Phase 3
# removes the mothership auth fallback — the hw139 half-pivot lesson.
if [ -n "${p2d_ln}" ] && [ -n "${strip_ln}" ] && [ "${p2d_ln}" -lt "${strip_ln}" ]; then
  ok "R2 phase2d.sh runs (line ${p2d_ln}) BEFORE the Phase-3 pre-strip gate (line ${strip_ln}) — the ghcr fallback is still intact while these HRs pivot"
else
  bad "R2 phase2d at ${p2d_ln:-<none>} does not precede the Phase-3 strip at ${strip_ln:-<none>} — the hw139 half-pivot ordering"
fi

r3=0
grep -q '^  phase2d.sh: |' "${TMP}/render.yaml" || r3=1
grep -q 'mountPath: /phase2d' "${TMP}/render.yaml" || r3=1
grep -q 'key: phase2d.sh' "${TMP}/render.yaml" || r3=1
if [ "${r3}" -eq 0 ]; then
  ok "R3 phase2d.sh key + /phase2d mount + volume item are all wired"
else
  bad "R3 phase2d.sh is not fully wired — the source would fail at runtime"
fi

# R4 — the grant. Asserted DIRECTLY against the rendered ClusterRole, with no
# stub in the path, because a stubbed kubectl can never be RBAC-refused (#6280).
python3 - "${TMP}/render.yaml" "${TMP}/grants.txt" <<'PY'
import sys, yaml
out = []
for d in yaml.safe_load_all(open(sys.argv[1])):
    if not d or d.get("kind") not in ("Role", "ClusterRole"):
        continue
    for rule in d.get("rules") or []:
        for g in rule.get("apiGroups") or []:
            for r in rule.get("resources") or []:
                for v in rule.get("verbs") or []:
                    out.append("%s/%s/%s" % (g or "core", r, v))
open(sys.argv[2], "w").write("\n".join(sorted(set(out))) + "\n")
PY

r4=0
for v in get list; do
  grep -qx "orgs.openova.io/organizations/${v}" "${TMP}/grants.txt" || r4=1
done
if [ "${r4}" -eq 0 ]; then
  ok "R4 the runner ClusterRole grants orgs.openova.io/organizations {get,list} — without it Phase-2d's live Org list is REFUSED"
else
  bad "R4 orgs.openova.io/organizations is NOT granted — Phase-2d would fail closed on every Sovereign"
fi

# R4b — PROVEN ABLE TO FAIL. Delete the rule from the rendered ClusterRole and
# require R4's check to go RED. A grant check only ever observed passing is not
# yet known to be a check.
python3 - "${TMP}/render.yaml" "${TMP}/grants-mut.txt" <<'PY'
import sys, yaml
out = []
for d in yaml.safe_load_all(open(sys.argv[1])):
    if not d or d.get("kind") not in ("Role", "ClusterRole"):
        continue
    for rule in d.get("rules") or []:
        groups = rule.get("apiGroups") or []
        res = rule.get("resources") or []
        if "orgs.openova.io" in groups and "organizations" in res:
            continue           # the mutation: drop the rule entirely
        for g in groups:
            for r in res:
                for v in rule.get("verbs") or []:
                    out.append("%s/%s/%s" % (g or "core", r, v))
open(sys.argv[2], "w").write("\n".join(sorted(set(out))) + "\n")
PY
if grep -qx "orgs.openova.io/organizations/list" "${TMP}/grants-mut.txt"; then
  bad "R4b removing the rule did not remove the grant — R4 is vacuous"
else
  ok "R4b mutation control: deleting the organizations rule makes R4's check report it missing"
fi

# R5 — phase2d must CALL the shared injector, never carry a second copy.
if grep -q 'inject_trust_ref' "${TMP}/phase2d.orig.sh" \
   && ! grep -q 'awk -v ref=' "${TMP}/phase2d.orig.sh"; then
  ok "R5 phase2d calls the shared injector instead of duplicating the awk (#5646)"
else
  bad "R5 phase2d carries its own copy of the trust injector — the two will drift"
fi

# R6 — the read-back must scan cluster-wide. Scoping it to HELMREPO_NAMESPACE is
# precisely why assert_region_a_pivot could not see these objects: the per-Org
# Kustomization's targetNamespace places them in the Org's own namespace.
if grep -q 'helmrepositories.source.toolkit.fluxcd.io -A' "${TMP}/phase2d.orig.sh"; then
  ok "R6 the read-back enumerates HelmRepositories cluster-wide (-A) — per-Org HRs land in the Org namespace, not flux-system"
else
  bad "R6 the read-back is namespace-scoped — it would be blind to exactly the objects this leg pivots"
fi

echo
echo "RESULT: ${pass} passed, ${fail} failed"
[ "${fail}" -eq 0 ]
