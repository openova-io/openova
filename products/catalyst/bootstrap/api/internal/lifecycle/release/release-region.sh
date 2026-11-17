#!/bin/sh
# release-region.sh - one region's part of delivering a release candidate to a
# Sovereign that has cut over (#7922).
#
# It runs inside the region whose Gitea it writes (a region's gitea-http answers
# only inside that region), makes no Kubernetes API call and does one phase:
#
#   stage    Clone this region's UPSTREAM_BRANCH, the branch Flux follows: the
#            release the region runs plus this Sovereign's own commits (cutover
#            steps 06, 07, 10, 11 and anything the sovereign-admin committed).
#            Fetch rc/CANDIDATE from UPSTREAM_REPO_URL and merge it. Every commit
#            of the branch stays: this is a merge, never a reset. Apply this
#            region's pivot again to what the candidate added (rewrite_kit_tree
#            of cutover step 06, KIT_REWRITE; for a follower region also the
#            folders and image-registry seams of the follower pivot), then push
#            the result as release/CANDIDATE. The candidate decides which files
#            exist and what they hold; this region's own change to a file it
#            pivots is its registry rewrite, which the stage applies again. So
#            a conflicted file whose region side is exactly the rewrite of the
#            merge base (step 06's rewrite_kit_tree, with or without the
#            imageRegistry seam of step 07) is taken from the candidate:
#            deleted when the candidate deletes it, else the candidate's content
#            with the rewrite applied again. Any other conflict moves nothing
#            and names each file. DRY_RUN=true reports and pushes nothing.
#   check    release/CANDIDATE is still STAGED_SHA and UPSTREAM_BRANCH is still
#            MAIN_SHA, the commit the staged one was merged onto: a promote is a
#            fast-forward.
#   promote  Push STAGED_SHA as UPSTREAM_BRANCH with a plain push, which the
#            server refuses unless it is a fast-forward. Never --force.
#
# Output: log lines "[release <region>] ...", report lines
# "release-report region=<r> <kind> <text>" and one last line
# "release-result region=<r> phase=<phase> [main=<sha>] [candidate=<sha>] [staged=<sha>]".
# Phases: staged, up-to-date, dry-run, checked, promoted, conflict, moved, refused, failed.
# Exit: 0 done; 2 refused; 3 conflict; 4 the branch moved; 1 a tool failed.
#
# Environment: PHASE CANDIDATE REGION DRY_RUN MAIN_SHA STAGED_SHA UPSTREAM_REPO_URL
# UPSTREAM_BRANCH UPSTREAM_PREFIX GITEA_INTERNAL_URL GITEA_ORG GITEA_REPO
# GITEA_USERNAME GITEA_PAT KIT_REWRITE PIN_EXTRACTOR FOLLOWER_SOURCE_DIRS
# IMAGE_REGISTRY_SLOT_FILES REPORT_MAX WORK MECHANISM_OUT CUTOVER_CHART_PATH CUTOVER_SLOT.
set -eu
export HOME="${HOME:-/tmp}"
: "${REGION:=primary}" "${DRY_RUN:=false}" "${REPORT_MAX:=40}"
: "${FOLLOWER_SOURCE_DIRS:=}" "${IMAGE_REGISTRY_SLOT_FILES:=}" "${PIN_EXTRACTOR:=}"
: "${MAIN_SHA:=}" "${STAGED_SHA:=}"
: "${CUTOVER_CHART_PATH:=platform/self-sovereign-cutover/chart}"
: "${CUTOVER_SLOT:=clusters/_template/bootstrap-kit/06a-bp-self-sovereign-cutover.yaml}"
export GIT_AUTHOR_NAME=sovereign-release GIT_COMMITTER_NAME=sovereign-release
export GIT_AUTHOR_EMAIL=release@sovereign.invalid GIT_COMMITTER_EMAIL=release@sovereign.invalid
export GIT_TERMINAL_PROMPT=0

say() { echo "[release ${REGION}] $*"; }
report() { echo "release-report region=${REGION} $*"; }
result() { echo "release-result region=${REGION} $*"; }
refuse() { report "reason $*"; say "REFUSED: $*"; result "phase=refused"; exit 2; }
fail() { report "reason $*"; say "FAILED: $*"; result "phase=failed"; exit 1; }

printf '%s\n' "${CANDIDATE:-}" | grep -Eqx '[0-9]{8}T[0-9]{6}Z-[0-9a-f]{7}' \
  || refuse "'${CANDIDATE:-}' is not a candidate id (<yyyymmddThhmmssZ>-<first 7 hex of the commit>)"
case "${PHASE:-}" in stage|check|promote) ;; *) refuse "unknown phase '${PHASE:-}'" ;; esac
[ -n "${GITEA_PAT:-}" ] || fail "no Gitea token in this region"
for v in UPSTREAM_REPO_URL UPSTREAM_BRANCH UPSTREAM_PREFIX GITEA_INTERNAL_URL GITEA_ORG GITEA_REPO GITEA_USERNAME; do
  eval "[ -n \"\${${v}:-}\" ]" || fail "${v} is empty"
done

rc_ref="rc/${CANDIDATE}"
staging="release/${CANDIDATE}"
branch="${UPSTREAM_BRANCH}"
local_url="${GITEA_INTERNAL_URL%/}/${GITEA_ORG}/${GITEA_REPO}.git"
token_auth=$(printf '%s' "${GITEA_USERNAME}:${GITEA_PAT}" | base64 | tr -d '\n')
work="${WORK:-$(mktemp -d)}"
mkdir -p "${work}"
redact() { sed -e "s#${GITEA_PAT}#REDACTED#g" -e "s#${token_auth}#REDACTED#g"; }
# g: git against this region's Gitea. The token rides a header, never the URL.
g() { git -c http.extraHeader="Authorization: Basic ${token_auth}" -c http.lowSpeedLimit=1000 -c http.lowSpeedTime=600 "$@"; }
remote_sha() { g ls-remote "${local_url}" "refs/heads/$1" 2>"${work}/ls.err" | cut -f1 | head -n 1; }
retry3() { _n=1; while :; do if "$@"; then return 0; fi; [ "${_n}" -ge 3 ] && return 1; _n=$((_n+1)); sleep 10; done; }

# pins DIR LOCAL_PREFIX: "<chart>\t<version>" per openova-io chart the kit folder
# pins, with the shared extractor of cutover step 03 (03b); empty without it.
pins() {
  if [ -n "${PIN_EXTRACTOR}" ] && [ -r "${PIN_EXTRACTOR}" ]; then
    ( . "${PIN_EXTRACTOR}" && bootstrapkit_pins "$1" "${UPSTREAM_PREFIX}" "$2" ) 2>/dev/null
  fi
}

kit="clusters/_template/bootstrap-kit"

# region_pivot: this region's pivot, read from the branch it runs (${before}):
# cutover step 06 wrote one local chart prefix (and, with a private CA, one
# certSecretRef) into the kit. Sets local_prefix, trust_secret and reg (the
# registry host the imageRegistry seams name), or pivot_why when the branch
# does not tell them.
region_pivot() {
  pivot_why="" local_prefix="" trust_secret="" reg=""
  prefixes=$(git grep -h -E '^[[:space:]]*url:[[:space:]]*oci://[^[:space:]]+[[:space:]]*$' "${before}" -- "${kit}" \
    | sed -E 's/^[[:space:]]*url:[[:space:]]*//; s/[[:space:]]*$//' | grep -vxF "${UPSTREAM_PREFIX}" | grep -E '/openova-io$' | sort -u || true)
  n_prefix=$(printf '%s\n' "${prefixes}" | grep -c . || true)
  if [ "${n_prefix}" -ne 1 ]; then
    pivot_why="cannot tell this region's chart registry: ${n_prefix} local chart prefixes in ${kit} of ${branch} ($(printf '%s\n' "${prefixes}" | tr '\n' ' '))"
    return 0
  fi
  local_prefix="${prefixes}"
  lp_re=$(printf '%s' "${local_prefix}" | sed 's/[].[*^$\\]/\\&/g')
  trusts=$(git grep -h -A2 -E "^[[:space:]]*url:[[:space:]]*${lp_re}[[:space:]]*\$" "${before}" -- "${kit}" \
    | awk '/^[[:space:]]*certSecretRef:[[:space:]]*$/ {c=1; next} c && /^[[:space:]]*name:/ {print $2} {c=0}' | sort -u || true)
  if [ "$(printf '%s\n' "${trusts}" | grep -c . || true)" -gt 1 ]; then
    pivot_why="more than one certSecretRef beside ${local_prefix} in ${kit} of ${branch}: $(printf '%s\n' "${trusts}" | tr '\n' ' ')"
    return 0
  fi
  trust_secret="${trusts}"
  reg="${local_prefix#oci://}"; reg="${reg%%/*}"
}

# seam FILE: the imageRegistry seam of cutover step 07 and of the follower
# pivot (a values.global.imageRegistry line at 6 spaces), set to ${reg}.
seam() {
  sed -i -E "s|^([[:space:]]{6}imageRegistry:[[:space:]]*)['\"]?[^'\"]*['\"]?[[:space:]]*\$|\1'${reg}'|" "$1"
}

# explained FILE: in the merge that stopped, this region's side of FILE is
# exactly its registry rewrite of the merge base: cutover step 06's
# rewrite_kit_tree (KIT_REWRITE, the same bytes the stage applies below), and
# for a file step 07 pivoted also the imageRegistry seam. Sets how to "kit" or
# "seam". Not explained: a file outside the folders the stage pivots, a file
# this Sovereign deleted or added, any other edit, or a region whose pivot the
# branch does not tell.
explained() {
  how=""
  [ -z "${pivot_why}" ] && [ -r "${KIT_REWRITE:-}" ] || return 1
  case "$1" in *.yaml) ;; *) return 1 ;; esac
  _in=""
  for _d in "${kit}" ${FOLLOWER_SOURCE_DIRS}; do [ "${1%/*}" != "${_d%/}" ] || _in=1; done
  [ -n "${_in}" ] || return 1
  _base=$(git rev-parse -q --verify ":1:$1") || return 1
  _ours=$(git rev-parse -q --verify ":2:$1") || return 1
  _x="${work}/explain"
  rm -rf "${_x}"; mkdir -p "${_x}"
  git cat-file blob "${_base}" >"${_x}/${1##*/}"
  ( . "${KIT_REWRITE}"; edited=0; rewrite_kit_tree "${_x}" ) >>"${work}/explain.log" 2>&1 || return 1
  if [ "$(git hash-object --no-filters "${_x}/${1##*/}")" = "${_ours}" ]; then how=kit; return 0; fi
  seam "${_x}/${1##*/}"
  if [ "$(git hash-object --no-filters "${_x}/${1##*/}")" = "${_ours}" ]; then how=seam; return 0; fi
  return 1
}

# take_candidate FILE...: resolve every explained conflict from the candidate
# (its deletion wins over this region's rewrite; its content is taken and the
# rewrite applied again after the merge, the seam through seam_files). Sets
# unexplained to the files it leaves and resolved to the report of the others.
take_candidate() {
  unexplained="" resolved=""
  for _c in "$@"; do
    if ! explained "${_c}"; then
      unexplained="${unexplained} ${_c}"
    elif git rev-parse -q --verify ":3:${_c}" >/dev/null; then
      git checkout --quiet --theirs -- "${_c}"
      git add -- "${_c}"
      [ "${how}" = kit ] || seam_files="${seam_files} ${_c}"
      resolved="${resolved}${_c}: the content of ${rc_ref}, this region's registry rewrite (${how}) applied again
"
    else
      git rm --quiet -- "${_c}"
      resolved="${resolved}${_c}: deleted by ${rc_ref}; this region changed it only by its registry rewrite (${how})
"
    fi
  done
}

stage() {
  say "stage ${rc_ref} onto ${branch} of ${local_url} (dry run: ${DRY_RUN})"
  up_rc=0
  up_line=$(git ls-remote --exit-code "${UPSTREAM_REPO_URL}" "refs/heads/${rc_ref}" 2>"${work}/up.err") || up_rc=$?
  case "${up_rc}" in
    0) ;;
    2) refuse "${rc_ref} does not exist at ${UPSTREAM_REPO_URL}" ;;
    *) fail "cannot read the refs of ${UPSTREAM_REPO_URL}: $(tail -n 1 "${work}/up.err")" ;;
  esac
  cand=$(printf '%s\n' "${up_line}" | head -n 1 | cut -f1)
  case "${cand}" in
    "${CANDIDATE##*-}"*) ;;
    *) refuse "${rc_ref} points at ${cand}, which does not end the id ${CANDIDATE}" ;;
  esac

  rm -rf "${work}/repo"
  if ! retry3 g clone --quiet --branch "${branch}" "${local_url}" "${work}/repo" 2>"${work}/clone.err"; then
    fail "cannot clone ${branch} of ${local_url}: $(redact <"${work}/clone.err" | tail -n 1)"
  fi
  cd "${work}/repo"
  before=$(git rev-parse HEAD)
  if ! retry3 git fetch --quiet --no-tags "${UPSTREAM_REPO_URL}" "+refs/heads/${rc_ref}:refs/remotes/upstream/${rc_ref}" 2>"${work}/fetch.err"; then
    fail "cannot fetch ${rc_ref} from ${UPSTREAM_REPO_URL}: $(tail -n 1 "${work}/fetch.err")"
  fi
  got=$(git rev-parse "refs/remotes/upstream/${rc_ref}^{commit}")
  [ "${got}" = "${cand}" ] || fail "fetched ${got} for ${rc_ref}, the ref list said ${cand}"

  if git merge-base --is-ancestor "${cand}" "${before}"; then
    report "nothing to deliver: ${branch} (${before}) already contains ${rc_ref} (${cand})"
    staged="${before}"
  else
    base=$(git merge-base "${before}" "${cand}") || refuse "${branch} and ${rc_ref} share no history"
    report "commits $(git rev-list --count "${base}..${cand}") from ${base} (the release this region runs) to ${rc_ref}; $(git rev-list --count "${base}..${before}") commit(s) of this Sovereign on ${branch} are kept"
    git log --format='%h %s' "${base}..${cand}" | head -n "${REPORT_MAX}" | while IFS= read -r l; do report "commit ${l}"; done

    git checkout --quiet -B "${staging}" "${before}"
    region_pivot
    seam_files=""
    merge_msg="release: deliver ${rc_ref} (${cand}) onto ${branch}"
    if ! git merge --no-ff --no-edit -m "${merge_msg}" "${cand}" >"${work}/merge.log" 2>&1; then
      conflicts=$(git diff --name-only --diff-filter=U)
      [ -n "${conflicts}" ] || fail "git merge failed without a conflict: $(tail -n 1 "${work}/merge.log")"
      take_candidate ${conflicts}
      if [ -n "${unexplained}" ]; then
        git merge --abort
        for f in ${unexplained}; do report "conflict ${f}"; done
        say "CONFLICT: ${rc_ref} and this Sovereign's commits on ${branch} change the same files, beyond this region's registry rewrite${pivot_why:+ (${pivot_why})}; nothing was pushed, ${branch} is still ${before}"
        result "phase=conflict main=${before} candidate=${cand}"
        exit 3
      fi
      [ -z "$(git diff --name-only --diff-filter=U)" ] || fail "files are still unmerged after the candidate was taken: $(git diff --name-only --diff-filter=U | tr '\n' ' ')"
      printf '%s' "${resolved}" | while IFS= read -r l; do report "resolved ${l}"; done
      git commit --quiet -m "${merge_msg}" -m "Taken from the candidate (this region's side was only its registry rewrite):
$(printf '%s' "${resolved}" | sed 's/^/- /')"
    fi

    # This region's pivot (region_pivot, read from the branch it runs) applied
    # again to the merged tree.
    [ -d "${kit}" ] || refuse "${kit} is not in the merged tree"
    [ -z "${pivot_why}" ] || refuse "${pivot_why}"
    [ -r "${KIT_REWRITE:-}" ] || fail "the kit rewrite of cutover step 06 is not readable at '${KIT_REWRITE:-}'"
    edited=0
    . "${KIT_REWRITE}"
    rewrite_kit_tree "${kit}" >"${work}/rewrite.log"
    pivot_paths="${kit}"
    for d in ${FOLLOWER_SOURCE_DIRS}; do
      [ ! -d "${d}" ] || { rewrite_kit_tree "${d}" >>"${work}/rewrite.log"; pivot_paths="${pivot_paths} ${d}"; }
    done
    for s in ${IMAGE_REGISTRY_SLOT_FILES} ${seam_files}; do
      [ ! -f "${s}" ] || seam "${s}"
    done
    # Fail closed: a url line still naming the upstream prefix (quoted, or with
    # trailing text the rewrite does not match) would re-tether this region.
    up_re=$(printf '%s' "${UPSTREAM_PREFIX}" | sed 's/[].[*^$\\]/\\&/g')
    left=$(for d in ${pivot_paths}; do grep -nE "^[[:space:]]*url:[[:space:]]*[\"']?${up_re}" "${d}"/*.yaml 2>/dev/null || true; done)
    if [ -n "${left}" ]; then
      printf '%s\n' "${left}" | while IFS= read -r l; do report "unpivoted ${l}"; done
      refuse "the merged tree still names ${UPSTREAM_PREFIX} after this region's pivot; nothing was pushed"
    fi
    git add -A -- ${pivot_paths} ${IMAGE_REGISTRY_SLOT_FILES}
    # The count is of files the pivot commit changes: rewrite_kit_tree raises
    # ${edited} for every trust re-injection, also one that changes nothing.
    changed=$(git diff --cached --name-only | grep -c . || true)
    if [ "${changed}" -gt 0 ]; then
      git commit --quiet -m "release: this Sovereign's pivot, applied to ${rc_ref} (${changed} file(s))"
    fi
    staged=$(git rev-parse HEAD)
    git merge-base --is-ancestor "${before}" "${staged}" || fail "the staged commit does not contain ${branch}"
    git merge-base --is-ancestor "${cand}" "${staged}" || fail "the staged commit does not contain ${rc_ref}"
    report "pivot ${changed} file(s) changed, registry ${local_prefix}${trust_secret:+, certSecretRef ${trust_secret}}"

    mkdir -p "${work}/before"
    git archive "${before}" "${kit}" | tar -xf - -C "${work}/before"
    pins "${work}/before/${kit}" "${local_prefix}" >"${work}/pins.before"
    pins "${kit}" "${local_prefix}" >"${work}/pins.staged"
    awk -F '\t' 'NR == FNR {o[$1] = $2; next} {n[$1] = $2}
      END {
        for (c in n) if (!(c in o)) print "chart " c " new " n[c]; else if (o[c] != n[c]) print "chart " c " " o[c] " -> " n[c]
        for (c in o) if (!(c in n)) print "chart " c " removed " o[c]
      }' "${work}/pins.before" "${work}/pins.staged" | sort | head -n "${REPORT_MAX}" | while IFS= read -r l; do report "${l}"; done
    git diff --unified=0 --no-color "${before}" "${staged}" -- '*values.yaml' \
      | awk '/^\+\+\+ / {f = substr($2, 3); next} /^\+[ \t]*(image|repository|tag):/ {l = $0; sub(/^\+[ \t]*/, "", l); print "image " f ": " l}' \
      | head -n "${REPORT_MAX}" | while IFS= read -r l; do report "${l}"; done
  fi

  # The release mechanism (#7938): the primary's stage exports the cutover chart
  # and its kit slot from the staged commit, for release-run.sh to verify and
  # render. ".candidate" says whether a commit of this Sovereign changed the
  # chart folder: the mechanism must be the candidate's own.
  if [ -n "${MECHANISM_OUT:-}" ]; then
    rm -rf "${MECHANISM_OUT}"; mkdir -p "${MECHANISM_OUT}"
    { git archive -o "${work}/mech.tar" "${staged}" -- "${CUTOVER_CHART_PATH}" "${CUTOVER_SLOT}" && tar -xf "${work}/mech.tar" -C "${MECHANISM_OUT}"; } 2>"${work}/mech.err" \
      || fail "cannot export ${CUTOVER_CHART_PATH} and ${CUTOVER_SLOT} of the staged commit: $(tail -n 1 "${work}/mech.err")"
    if git diff --quiet "${cand}" "${staged}" -- "${CUTOVER_CHART_PATH}"; then echo same; else echo changed; fi >"${MECHANISM_OUT}/.candidate"
    git rev-parse "${staged}:${CUTOVER_CHART_PATH}" >"${MECHANISM_OUT}/.tree"
  fi

  if [ "${DRY_RUN}" = "true" ]; then
    report "dry run: nothing was pushed"
    result "phase=dry-run main=${before} candidate=${cand} staged=${staged}"
    return 0
  fi
  # The candidate ref is immutable: a different commit under its name is refused.
  # The staging branch is read by nothing but this release's prewarm; a new
  # staging replaces an earlier one. The branch Flux follows is not pushed here.
  if ! g push --quiet "${local_url}" "${cand}:refs/heads/${rc_ref}" "+${staged}:refs/heads/${staging}" 2>"${work}/push.err"; then
    fail "cannot push ${rc_ref} and ${staging} to ${local_url}: $(redact <"${work}/push.err" | tail -n 1)"
  fi
  [ "$(remote_sha "${staging}")" = "${staged}" ] || fail "${staging} at ${local_url} is not ${staged} after the push"
  if [ "${staged}" = "${before}" ]; then
    result "phase=up-to-date main=${before} candidate=${cand} staged=${staged}"
  else
    result "phase=staged main=${before} candidate=${cand} staged=${staged}"
  fi
}

# verify sets now_main. It exits when the staged commit is not the recorded one
# or when the branch moved after the release was merged onto it.
verify() {
  if [ -z "${MAIN_SHA}" ] || [ -z "${STAGED_SHA}" ]; then refuse "no staged commit is recorded for this region"; fi
  now_staged=$(remote_sha "${staging}")
  [ "${now_staged}" = "${STAGED_SHA}" ] || refuse "${staging} is ${now_staged:-absent}, not the staged ${STAGED_SHA}"
  now_main=$(remote_sha "${branch}")
  [ -n "${now_main}" ] || fail "cannot read ${branch} of ${local_url}: $(redact <"${work}/ls.err" | tail -n 1)"
  [ "${now_main}" = "${STAGED_SHA}" ] && return 0
  if [ "${now_main}" != "${MAIN_SHA}" ]; then
    report "reason ${branch} moved from ${MAIN_SHA} to ${now_main} after the release was staged"
    say "MOVED: ${branch} is ${now_main}, the release was merged onto ${MAIN_SHA}; nothing was pushed"
    result "phase=moved main=${now_main} staged=${STAGED_SHA}"
    exit 4
  fi
}

check() {
  verify
  if [ "${now_main}" = "${STAGED_SHA}" ]; then
    result "phase=promoted main=${now_main} staged=${STAGED_SHA}"
  else
    result "phase=checked main=${now_main} staged=${STAGED_SHA}"
  fi
}

promote() {
  verify
  if [ "${now_main}" = "${STAGED_SHA}" ]; then
    result "phase=promoted main=${now_main} staged=${STAGED_SHA}"
    return 0
  fi
  git init --quiet --bare "${work}/promote.git"
  # Enough history to hold MAIN_SHA as an ancestor of the staged commit (it is
  # two or three parents down), so git can see the push is a fast-forward.
  if ! retry3 g --git-dir="${work}/promote.git" fetch --quiet --depth=5 "${local_url}" \
      "+refs/heads/${staging}:refs/heads/staged" "+refs/heads/${branch}:refs/heads/main-now" 2>"${work}/pfetch.err"; then
    fail "cannot fetch ${staging} from ${local_url}: $(redact <"${work}/pfetch.err" | tail -n 1)"
  fi
  [ "$(git --git-dir="${work}/promote.git" rev-parse refs/heads/staged)" = "${STAGED_SHA}" ] || refuse "${staging} changed while it was fetched"
  if ! g --git-dir="${work}/promote.git" push --quiet "${local_url}" "${STAGED_SHA}:refs/heads/${branch}" 2>"${work}/ppush.err"; then
    report "reason the push of ${STAGED_SHA} as ${branch} was refused: $(redact <"${work}/ppush.err" | tail -n 1)"
    result "phase=moved main=$(remote_sha "${branch}") staged=${STAGED_SHA}"
    exit 4
  fi
  [ "$(remote_sha "${branch}")" = "${STAGED_SHA}" ] || fail "${branch} at ${local_url} is not ${STAGED_SHA} after the push"
  say "${branch} moved ${MAIN_SHA} -> ${STAGED_SHA}; Flux applies it on its next interval"
  result "phase=promoted main=${STAGED_SHA} staged=${STAGED_SHA}"
}

case "${PHASE}" in
  stage) stage ;;
  check) check ;;
  promote) promote ;;
esac
