#!/usr/bin/env bash
# #7922 — step-01 (gitea-mirror) pushes ONLY the upstream refs a Sovereign reads,
# and switches Gitea Actions off on the repository before it pushes.
#
# WHAT WAS MEASURED. hw307 (dep 9836870916c31984, 15 Nov 2026): step-01 took
# 33 min, 28 of them pushing the 2,733 upstream branches other than the default
# branch in 200-ref batches. Each batch took ~130 s, ~118 s of it between Gitea's
# pre-receive and post-receive calls: git runs the per-ref `update` hook, one
# `gitea hook update` process per ref. Every new branch also queued two
# notifications whose Gitea Actions handler parses the branch's workflow files
# (139 on main); 2,800+ such scans were still running 1h40m after the step ended.
# No consumer on a Sovereign reads any of those branches: Flux, steps 05-11, the
# secondary pivot and the day-two reconciler read the default branch
# (upstream.branch); org-tenants and catalog-sovereign are sovereign-local.
#
# WHAT THIS TEST DOES. It renders the chart (two-region, so both the primary
# step-01 script and the secondary-region script render), then EXECUTES both
# rendered scripts — not a token grep — against fixture git repositories: an
# "upstream" with the default branch, four other branches and a tag, and a
# "local Gitea" bare repository that already holds the sovereign-local branches.
# Only kubectl, curl and nslookup are stubbed; git is real (wrapped to log each
# push). It asserts, per script:
#   1. the script exits 0;
#   2. the local repository ends with EXACTLY the default branch plus the
#      sovereign-local branches — no other upstream branch arrived;
#   3. the default branch equals upstream, the tag arrived, and the
#      sovereign-local branches are byte-unchanged;
#   4. a PATCH {"has_actions":false} on the repository happened BEFORE the
#      first git push.
# Non-vacuity: the fixture upstream is checked to carry the extra branches, so
# assertion 2 can fail; on origin/main before #7922 assertions 2 and 4 FAIL
# (`bash step7922-gitea-mirror-refs.sh <origin/main chart dir>`).
#
# Usage: bash step7922-gitea-mirror-refs.sh [chart-dir]   (CI passes the chart dir)
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
FQDN="hw300.omani.works"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

REAL_GIT="$(command -v git)"
command -v helm >/dev/null 2>&1 || { echo "[step7922] helm not on PATH"; exit 1; }
SOV_LOCAL="catalog-sovereign org-tenants"
EXTRA_UPSTREAM="feat/unused-1 fix/unused-2 rc/20261115T144352Z-67402a1 feat/diverged"

echo "[step7922] render (two-region, so the secondary script renders too) from ${CHART_DIR}"
if ! helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" \
    --set secondaryRegions.mirrorToSecondaryGitea=true >"${TMP}/on.yaml" 2>"${TMP}/render.err"; then
  echo "helm template failed:"; cat "${TMP}/render.err"; exit 1
fi

# Slice both scripts and the env their containers set out of the RENDER (never a
# hand-kept copy). Only plain `value:` env is exported; the test sets the rest.
python3 - "${TMP}/on.yaml" "${TMP}" <<'PY'
import sys, yaml, shlex
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
out = sys.argv[2]
def envfile(env, path):
    with open(path, 'w') as f:
        for e in env:
            if 'value' in e:
                f.write('export %s=%s\n' % (e['name'], shlex.quote(str(e['value']))))
for d in docs:
    if d.get('kind') != 'ConfigMap':
        continue
    name = d['metadata']['name']
    if name == 'cutover-step-01-gitea-mirror':
        c = yaml.safe_load(d['data']['podSpec'])['containers'][0]
        open(out + '/step01.sh', 'w').write(c['args'][0])
        envfile(c.get('env', []), out + '/step01.env')
    elif name == 'cutover-secondary-mirror-script':
        open(out + '/secmirror.sh', 'w').write(d['data']['secondary-mirror.sh'])
        job = yaml.safe_load(d['data']['secondary-mirror-job.yaml'].replace('__REGION__', 'me-east-215-b-1').replace('__SOURCE_REF__', 'main'))
        c = job['spec']['template']['spec']['containers'][0]
        envfile(c.get('env', []), out + '/secmirror.env')
PY
for f in step01.sh step01.env secmirror.sh secmirror.env; do
  [ -s "${TMP}/${f}" ] || { echo "[step7922] could not slice ${f} out of the render"; exit 1; }
done

# ── stubs: git (real, logs pushes), curl (Gitea API), kubectl, nslookup ──────────
mkdir -p "${TMP}/bin"
cat >"${TMP}/bin/git" <<'STUB'
#!/bin/sh
for a in "$@"; do
  case "$a" in push) echo "git-push" >>"${EVENTS}"; break ;; esac
done
exec "${REAL_GIT}" "$@"
STUB
cat >"${TMP}/bin/curl" <<'STUB'
#!/bin/sh
# Gitea API stub: org + repo exist, regular (non-mirror) repo; remembers whether
# a PATCH switched Actions off and reports it in every repo body.
method=GET; data=""; out=""; url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -X) method="$2"; shift 2 ;;
    -d) data="$2"; shift 2 ;;
    -o) out="$2"; shift 2 ;;
    -H|--max-time) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
echo "curl ${method} ${url} ${data}" >>"${EVENTS}"
if [ "${method}" = "PATCH" ]; then
  case "${data}" in *'"has_actions":false'*) : >"${STATE}/actions-off" ;; esac
fi
acts=true; [ -f "${STATE}/actions-off" ] && acts=false
body="{\"id\":1,\"empty\":false,\"mirror\":false,\"has_actions\":${acts}}"
if [ -n "${out}" ]; then printf '%s' "${body}" >"${out}"; else printf '%s' "${body}"; fi
exit 0
STUB
cat >"${TMP}/bin/kubectl" <<'STUB'
#!/bin/sh
case "$*" in
  *"get secret"*) printf 'testpat' | base64; exit 0 ;;
  *"get cronjob"*) exit 1 ;;
  # the Flux source before the pivot follows upstream main (#7922 candidate rule)
  *"get gitrepository"*) printf '%s main' "${UPSTREAM_REPO_URL}"; exit 0 ;;
esac
exit 0
STUB
printf '#!/bin/sh\nexit 0\n' >"${TMP}/bin/nslookup"
chmod +x "${TMP}/bin/"*

# ── fixtures ────────────────────────────────────────────────────────────────────
make_fixture() { # $1 = fixture root
  local d="$1"
  "${REAL_GIT}" init -q -b main "${d}/src"
  (
    cd "${d}/src" || exit 1
    g() { "${REAL_GIT}" -c user.email=t@example.invalid -c user.name=t "$@"; }
    mkdir -p .github/workflows
    printf 'on:\n  workflow_dispatch:\n    inputs:\n      x: {type: string}\njobs: {}\n' >.github/workflows/ci.yaml
    echo a >a; g add -A; g commit -q -m main-1
    g tag v1.0.0
    for b in feat/unused-1 fix/unused-2 rc/20261115T144352Z-67402a1; do g branch "${b}"; done
    g checkout -q -b feat/diverged; echo b >b; g add -A; g commit -q -m side; g checkout -q main
    echo c >c; g add -A; g commit -q -m main-2
  ) || return 1
  "${REAL_GIT}" init -q --bare -b main "${d}/gitea/openova/openova.git"
  # the local Gitea already holds the sovereign-local branches (never upstream)
  "${REAL_GIT}" -C "${d}/src" push -q "${d}/gitea/openova/openova.git" \
    "HEAD~1:refs/heads/org-tenants" "HEAD~1:refs/heads/catalog-sovereign"
}

heads_of() { "${REAL_GIT}" -C "$1" for-each-ref --format='%(refname:strip=2)' refs/heads/ | sort | tr '\n' ' '; }
sha_of()   { "${REAL_GIT}" -C "$1" rev-parse --verify --quiet "$2" 2>/dev/null; }

# run_script <label> <env-file> <script> <fixture-root> — prepare + execute
run_script() {
  local label="$1" envf="$2" script="$3" fx="$4"
  mkdir -p "${fx}/home" "${fx}/work" "${fx}/skc" "${fx}/smir" "${fx}/state"
  : >"${fx}/events.log"
  {
    cat "${envf}"
    echo "export UPSTREAM_REPO_URL='${fx}/src'"
    echo "export GITEA_INTERNAL_URL='${fx}/gitea'"
    echo "export GITEA_PAT=testpat"
    # rewrite the container paths so nothing writes outside the fixture
    sed -e "s#export HOME=/tmp#export HOME='${fx}/home'#" \
        -e "s#/secondary-kubeconfigs/#${fx}/skc/#g" \
        -e "s#/secondary-mirror/#${fx}/smir/#g" "${script}"
  } >"${fx}/run.sh"
  ( cd "${fx}/work" && \
    PATH="${TMP}/bin:${PATH}" TMPDIR="${fx}/work" EVENTS="${fx}/events.log" \
    STATE="${fx}/state" REAL_GIT="${REAL_GIT}" sh "${fx}/run.sh" ) >"${fx}/out.log" 2>&1
  echo $? >"${fx}/rc"
  echo "    [${label}] exit=$(cat "${fx}/rc"); last lines:"
  tail -4 "${fx}/out.log" | sed -E 's/testpat/REDACTED/g; s/^/      /'
}

# check_result <label> <fixture-root> — the four assertions
check_result() {
  local label="$1" fx="$2" target="$2/gitea/openova/openova.git"
  local rc; rc="$(cat "${fx}/rc")"
  [ "${rc}" = "0" ] && pass "${label}: script exits 0" || fail "${label}: script exited ${rc}"

  local want got
  want="$(printf '%s\n' main ${SOV_LOCAL} | sort | tr '\n' ' ')"
  got="$(heads_of "${target}")"
  if [ "${got}" = "${want}" ]; then
    pass "${label}: local repo holds exactly [${got% }] — no other upstream branch was pushed (#7922)"
  else
    fail "${label}: local repo holds [${got% }], want [${want% }] — upstream branches no consumer reads were pushed (#7922)"
  fi

  if [ "$(sha_of "${target}" refs/heads/main)" = "$(sha_of "${fx}/src" refs/heads/main)" ]; then
    pass "${label}: local main == upstream main"
  else
    fail "${label}: local main does not equal upstream main"
  fi
  if [ -n "$(sha_of "${target}" refs/tags/v1.0.0)" ]; then
    pass "${label}: the upstream tag arrived"
  else
    fail "${label}: the upstream tag did not arrive"
  fi
  local b
  for b in ${SOV_LOCAL}; do
    if [ "$(sha_of "${target}" "refs/heads/${b}")" = "$(cat "${fx}/sov-${b}")" ]; then
      pass "${label}: sovereign-local ${b} untouched"
    else
      fail "${label}: sovereign-local ${b} changed or vanished (#5011 class)"
    fi
  done

  local patch_at push_at
  patch_at="$(grep -n '^curl PATCH .*/repos/openova/openova .*"has_actions":false' "${fx}/events.log" | awk -F: 'NR==1{print $1}')"
  push_at="$(grep -n '^git-push' "${fx}/events.log" | awk -F: 'NR==1{print $1}')"
  if [ -n "${patch_at}" ] && [ -n "${push_at}" ] && [ "${patch_at}" -lt "${push_at}" ]; then
    pass "${label}: Gitea Actions switched off (PATCH has_actions=false, event ${patch_at}) before the first push (event ${push_at}) (#7922)"
  else
    fail "${label}: no PATCH has_actions=false before the first push (patch event '${patch_at:-none}', first push event '${push_at:-none}') — every pushed ref costs a workflow scan (#7922)"
  fi
}

prepare() { # $1 = fixture root
  make_fixture "$1" || { echo "[step7922] fixture build failed"; exit 1; }
  local b
  for b in ${SOV_LOCAL}; do sha_of "$1/gitea/openova/openova.git" "refs/heads/${b}" >"$1/sov-${b}"; done
  # non-vacuity: the upstream really carries branches beyond main
  local extra; extra=0
  for b in ${EXTRA_UPSTREAM}; do [ -n "$(sha_of "$1/src" "refs/heads/${b}")" ] && extra=$((extra + 1)); done
  if [ "${extra}" -eq 4 ]; then
    pass "fixture upstream carries main + 4 other branches + a tag (the exact-ref check can fail)"
  else
    fail "fixture upstream carries only ${extra}/4 extra branches — the exact-ref check would be vacuous"
  fi
}

echo "[step7922] (1) EXECUTE the rendered step-01 script (primary leg)"
prepare "${TMP}/fx1"
run_script step-01 "${TMP}/step01.env" "${TMP}/step01.sh" "${TMP}/fx1"
check_result step-01 "${TMP}/fx1"
if grep -q 'secondary mirror is a no-op' "${TMP}/fx1/out.log"; then
  pass "step-01: the secondary leg no-ops with no kubeconfig mounted (the run reached the end)"
else
  fail "step-01: the run did not reach the secondary-leg no-op"
fi

echo "[step7922] (2) re-run step-01 on the same repository (Job retry / re-trigger is idempotent)"
run_script step-01-rerun "${TMP}/step01.env" "${TMP}/step01.sh" "${TMP}/fx1"
check_result step-01-rerun "${TMP}/fx1"

echo "[step7922] (3) EXECUTE the rendered secondary-region mirror script"
prepare "${TMP}/fx2"
run_script secondary "${TMP}/secmirror.env" "${TMP}/secmirror.sh" "${TMP}/fx2"
check_result secondary "${TMP}/fx2"
if grep -q 'ls-remote OK' "${TMP}/fx2/out.log"; then
  pass "secondary: the ls-remote ordering gate (#6490) still passes"
else
  fail "secondary: the ls-remote ordering gate did not pass"
fi

echo
if [ "${FAILURES}" -eq 0 ]; then
  echo "[step7922] all assertions passed — both mirror scripts push only the default branch + tags, switch Gitea Actions off first, and leave sovereign-local branches untouched."
  exit 0
fi
echo "[step7922] ${FAILURES} assertion(s) FAILED"
exit 1
