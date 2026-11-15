#!/usr/bin/env bash
# #7922 — after cutover a Sovereign runs EXACTLY the release it was installed
# from, not upstream main at the moment step 01 ran.
#
# WHAT WAS MEASURED. hw307 (dep 9836870916c31984, 15 Nov 2026) was installed
# from the candidate branch rc/20261115T144352Z-67402a1 (67402a108): Flux's
# source followed that branch until step 05. Step 01 mirrored upstream `main`
# (as of 17:05) into the local Gitea's `main`, and step 05 points Flux at local
# `main`, so after cutover both regions ran upstream main of 17:05 plus their own
# commits, not the candidate of 14:43.
#
# THE RULE. Step 01 reads the release from the live Flux source (the
# installation's gitopsBranch, while the source still follows upstream), pushes
# THAT commit as the local branch step 05 points Flux at (upstream.branch), and
# hands the same ref to the follower region's leg. Once Flux follows the local
# Gitea (a re-run after step 05) the local branch is KEPT: it holds the release
# plus the Sovereign's own commits (steps 06/07/10/11). An unreadable source is
# fatal: the step never guesses a release.
#
# WHAT THIS TEST DOES. It renders the chart (two-region) and EXECUTES the
# rendered step-01 script and the follower-region script against fixture git
# repositories (real git; kubectl, curl and nslookup stubbed). The fixture's
# upstream `main` is one commit AHEAD of the candidate branch, so "local main ==
# candidate" and "local main == upstream main" cannot both hold. Cases:
#   1 fresh step 01, source = upstream @ candidate: local main == candidate;
#     only main + tags + the sovereign-local branches exist (no branch flood, #7928)
#   2 re-run after the pivot (source = local Gitea) with a Sovereign commit on
#     local main: the commit is kept
#   3 source unreadable: step 01 fails, local main is not created
#   4 follower leg with the ref step 01 hands it: its local main == candidate;
#     with an empty ref (re-run): its own commit is kept
#   5 render: the branch step 01 writes is the branch step 05 points Flux at,
#     and step 01 hands its resolved ref to the follower Job it stamps
# On origin/main before this change cases 1, 2, 3 and 4 FAIL.
#
# Usage: bash step7922-cutover-keeps-candidate.sh [chart-dir]   (CI passes the chart dir)
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
FQDN="hw300.omani.works"
CANDIDATE="rc/20261115T144352Z-67402a1"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

REAL_GIT="$(command -v git)"
command -v helm >/dev/null 2>&1 || { echo "[step7922-candidate] helm not on PATH"; exit 1; }
SOV_LOCAL="catalog-sovereign org-tenants"

if ! helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" \
    --set secondaryRegions.mirrorToSecondaryGitea=true >"${TMP}/on.yaml" 2>"${TMP}/render.err"; then
  echo "helm template failed:"; cat "${TMP}/render.err"; exit 1
fi

# Slice the two scripts, their env and the step-05 branch out of the RENDER.
python3 - "${TMP}/on.yaml" "${TMP}" <<'PY'
import sys, yaml, shlex
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
out = sys.argv[2]
def envfile(env, path, skip=()):
    with open(path, 'w') as f:
        for e in env:
            if 'value' in e and e['name'] not in skip:
                f.write('export %s=%s\n' % (e['name'], shlex.quote(str(e['value']))))
def env_of(c, name):
    return next((str(e.get('value', '')) for e in c.get('env', []) if e['name'] == name), '')
for d in docs:
    if d.get('kind') != 'ConfigMap':
        continue
    name = d['metadata']['name']
    if name == 'cutover-step-01-gitea-mirror':
        c = yaml.safe_load(d['data']['podSpec'])['containers'][0]
        open(out + '/step01.sh', 'w').write(c['args'][0])
        envfile(c.get('env', []), out + '/step01.env')
        open(out + '/step01.branch', 'w').write(env_of(c, 'UPSTREAM_BRANCH'))
    elif name == 'cutover-step-05-flux-gitrepository-patch':
        c = yaml.safe_load(d['data']['podSpec'])['containers'][0]
        open(out + '/step05.branch', 'w').write(env_of(c, 'BRANCH'))
    elif name == 'cutover-secondary-mirror-script':
        open(out + '/secmirror.sh', 'w').write(d['data']['secondary-mirror.sh'])
        raw = d['data']['secondary-mirror-job.yaml']
        open(out + '/secjob.raw', 'w').write(raw)
        job = yaml.safe_load(raw.replace('__REGION__', 'me-east-215-b-1'))
        c = job['spec']['template']['spec']['containers'][0]
        envfile(c.get('env', []), out + '/secmirror.env', skip=('SOURCE_REF',))
PY
for f in step01.sh step01.env secmirror.sh secmirror.env step01.branch secjob.raw; do
  [ -s "${TMP}/${f}" ] || { echo "[step7922-candidate] could not slice ${f} out of the render"; exit 1; }
done

mkdir -p "${TMP}/bin"
cat >"${TMP}/bin/curl" <<'STUB'
#!/bin/sh
out=""; while [ $# -gt 0 ]; do case "$1" in -o) out="$2"; shift 2 ;; -X|-d|-H|--max-time) shift 2 ;; *) shift ;; esac; done
body='{"id":1,"empty":false,"mirror":false,"has_actions":false}'
if [ -n "${out}" ]; then printf '%s' "${body}" >"${out}"; else printf '%s' "${body}"; fi
exit 0
STUB
# kubectl: the Flux source answers what SRC says (url and branch); SRC=unreadable fails.
cat >"${TMP}/bin/kubectl" <<'STUB'
#!/bin/sh
case "$*" in
  *"get secret"*) printf 'testpat' | base64; exit 0 ;;
  *"get cronjob"*) exit 1 ;;
  *"get gitrepository"*) [ "${SRC}" = unreadable ] && { echo 'Error from server (Forbidden)' >&2; exit 1; }; printf '%s' "${SRC}"; exit 0 ;;
esac
exit 0
STUB
printf '#!/bin/sh\nexit 0\n' >"${TMP}/bin/nslookup"
chmod +x "${TMP}/bin/"*

# Fixture: upstream main is one commit AHEAD of the candidate branch.
make_fixture() {
  local d="$1"
  "${REAL_GIT}" init -q -b main "${d}/src"
  (
    cd "${d}/src" || exit 1
    g() { "${REAL_GIT}" -c user.email=t@example.invalid -c user.name=t "$@"; }
    echo a >a; g add -A; g commit -q -m release; g tag v1.0.0
    g branch "${CANDIDATE}"; g branch feat/unused
    echo c >c; g add -A; g commit -q -m main-after-the-cut
  ) || return 1
  "${REAL_GIT}" init -q --bare -b main "${d}/gitea/openova/openova.git"
  "${REAL_GIT}" -C "${d}/src" push -q "${d}/gitea/openova/openova.git" \
    "${CANDIDATE}:refs/heads/org-tenants" "${CANDIDATE}:refs/heads/catalog-sovereign"
}
heads_of() { "${REAL_GIT}" -C "$1" for-each-ref --format='%(refname:strip=2)' refs/heads/ | sort | tr '\n' ' '; }
sha_of()   { "${REAL_GIT}" -C "$1" rev-parse --verify --quiet "$2" 2>/dev/null; }
# sovereign_commit <bare-repo>: one commit on top of its main, as steps 06/07 make; prints its sha
sovereign_commit() {
  local w="${TMP}/sc.$$.$RANDOM"
  "${REAL_GIT}" clone -q "$1" "${w}" && (cd "${w}" && echo s >s && "${REAL_GIT}" add -A \
    && "${REAL_GIT}" -c user.email=t@example.invalid -c user.name=t commit -q -m "step-06 pivot" \
    && "${REAL_GIT}" push -q origin HEAD:main) >/dev/null 2>&1
  sha_of "$1" refs/heads/main
}

# run_script <env-file> <script> <fixture-root> <SRC> [extra env line]
run_script() {
  local envf="$1" script="$2" fx="$3" src="$4" extra="${5:-}"
  mkdir -p "${fx}/home" "${fx}/work" "${fx}/skc" "${fx}/smir"
  {
    cat "${envf}"
    echo "export UPSTREAM_REPO_URL='${fx}/src'"
    echo "export GITEA_INTERNAL_URL='${fx}/gitea'"
    echo "export GITEA_PAT=testpat"
    [ -n "${extra}" ] && echo "${extra}"
    sed -e "s#export HOME=/tmp#export HOME='${fx}/home'#" \
        -e "s#/secondary-kubeconfigs/#${fx}/skc/#g" \
        -e "s#/secondary-mirror/#${fx}/smir/#g" "${script}"
  } >"${fx}/run.sh"
  ( cd "${fx}/work" && PATH="${TMP}/bin:${PATH}" TMPDIR="${fx}/work" SRC="${src}" sh "${fx}/run.sh" ) >"${fx}/out.log" 2>&1
  echo $? >"${fx}/rc"
}
rc_of() { cat "$1/rc"; }

echo "[step7922-candidate] (1) fresh step 01: the Flux source follows upstream @ ${CANDIDATE}"
fx="${TMP}/fx1"; make_fixture "${fx}" || exit 1
cand="$(sha_of "${fx}/src" "refs/heads/${CANDIDATE}")"; up="$(sha_of "${fx}/src" refs/heads/main)"
[ -n "${cand}" ] && [ "${cand}" != "${up}" ] && pass "fixture: upstream main is ahead of the candidate (the two outcomes differ)" || fail "fixture: candidate and upstream main are not distinct"
run_script "${TMP}/step01.env" "${TMP}/step01.sh" "${fx}" "${fx}/src ${CANDIDATE}"
[ "$(rc_of "${fx}")" = 0 ] && pass "step 01 exits 0" || { fail "step 01 exited $(rc_of "${fx}")"; tail -5 "${fx}/out.log" | sed 's/^/      /'; }
got="$(sha_of "${fx}/gitea/openova/openova.git" refs/heads/main)"
if [ "${got}" = "${cand}" ]; then pass "local main == the candidate ${CANDIDATE} (${cand:0:9})"
elif [ "${got}" = "${up}" ]; then fail "local main == upstream main (${up:0:9}), not the candidate ${CANDIDATE} (${cand:0:9}): after cutover the Sovereign would run main"
else fail "local main is '${got:-absent}', want the candidate ${cand:0:9}"; fi
want="$(printf '%s\n' main ${SOV_LOCAL} | sort | tr '\n' ' ')"
[ "$(heads_of "${fx}/gitea/openova/openova.git")" = "${want}" ] && pass "local heads are exactly [${want% }]: the candidate arrives AS main, no branch flood (#7928)" \
  || fail "local heads are [$(heads_of "${fx}/gitea/openova/openova.git")], want [${want% }]"
[ -n "$(sha_of "${fx}/gitea/openova/openova.git" refs/tags/v1.0.0)" ] && pass "the tags arrived" || fail "the tags did not arrive"

echo "[step7922-candidate] (2) re-run after the pivot: the Flux source follows the local Gitea, local main carries a Sovereign commit"
own="$(sovereign_commit "${fx}/gitea/openova/openova.git")"
run_script "${TMP}/step01.env" "${TMP}/step01.sh" "${fx}" "${fx}/gitea/openova/openova main"
[ "$(rc_of "${fx}")" = 0 ] && pass "the re-run exits 0" || { fail "the re-run exited $(rc_of "${fx}")"; tail -5 "${fx}/out.log" | sed 's/^/      /'; }
[ -n "${own}" ] && [ "$(sha_of "${fx}/gitea/openova/openova.git" refs/heads/main)" = "${own}" ] \
  && pass "local main keeps the Sovereign's own commit (${own:0:9}) on top of the release" \
  || fail "the re-run moved local main to $(sha_of "${fx}/gitea/openova/openova.git" refs/heads/main | cut -c1-9), discarding the Sovereign's commit ${own:0:9}"

echo "[step7922-candidate] (3) the Flux source cannot be read"
fx="${TMP}/fx3"; make_fixture "${fx}" || exit 1
run_script "${TMP}/step01.env" "${TMP}/step01.sh" "${fx}" unreadable
[ "$(rc_of "${fx}")" != 0 ] && grep -q 'FATAL (#7922)' "${fx}/out.log" && pass "step 01 fails, naming #7922, when the release cannot be read" \
  || fail "step 01 exited $(rc_of "${fx}") without the #7922 FATAL when the Flux source was unreadable: it guessed a release"
[ -z "$(sha_of "${fx}/gitea/openova/openova.git" refs/heads/main)" ] && pass "nothing was pushed as main" || fail "a main was pushed although the release was unknown"

echo "[step7922-candidate] (4) the follower region's leg with the ref step 01 hands it"
fx="${TMP}/fx4"; make_fixture "${fx}" || exit 1
cand="$(sha_of "${fx}/src" "refs/heads/${CANDIDATE}")"
run_script "${TMP}/secmirror.env" "${TMP}/secmirror.sh" "${fx}" "" "export SOURCE_REF='${CANDIDATE}'"
[ "$(rc_of "${fx}")" = 0 ] && pass "the follower leg exits 0" || { fail "the follower leg exited $(rc_of "${fx}")"; tail -5 "${fx}/out.log" | sed 's/^/      /'; }
[ "$(sha_of "${fx}/gitea/openova/openova.git" refs/heads/main)" = "${cand}" ] && pass "follower local main == the candidate" \
  || fail "follower local main is $(sha_of "${fx}/gitea/openova/openova.git" refs/heads/main | cut -c1-9), want the candidate ${cand:0:9}"
own="$(sovereign_commit "${fx}/gitea/openova/openova.git")"
run_script "${TMP}/secmirror.env" "${TMP}/secmirror.sh" "${fx}" "" "export SOURCE_REF=''"
[ "$(rc_of "${fx}")" = 0 ] && [ -n "${own}" ] && [ "$(sha_of "${fx}/gitea/openova/openova.git" refs/heads/main)" = "${own}" ] \
  && pass "with an empty ref (a re-run after the pivot) the follower keeps its own commit" \
  || fail "with an empty ref the follower leg exited $(rc_of "${fx}") and left main at $(sha_of "${fx}/gitea/openova/openova.git" refs/heads/main | cut -c1-9), want its own ${own:0:9}"

echo "[step7922-candidate] (5) render: one branch from step 01 to step 05, and the ref handed to the follower"
b01="$(cat "${TMP}/step01.branch")"; b05="$(cat "${TMP}/step05.branch" 2>/dev/null)"
[ -n "${b01}" ] && [ "${b01}" = "${b05}" ] && pass "step 01 writes local '${b01}', the branch step 05 points Flux at" \
  || fail "step 01 writes '${b01}' but step 05 points Flux at '${b05:-?}'"
grep -q 'value: "__SOURCE_REF__"' "${TMP}/secjob.raw" && grep -q 's#__SOURCE_REF__#${src_ref:-}#g' "${TMP}/step01.sh" \
  && pass "the follower Job carries SOURCE_REF, and step 01 substitutes its resolved ref into it" \
  || fail "the follower Job is not handed the ref step 01 resolved"

echo
if [ "${FAILURES}" -eq 0 ]; then echo "[step7922-candidate] all assertions passed"; exit 0; fi
echo "[step7922-candidate] ${FAILURES} assertion(s) FAILED"; exit 1
