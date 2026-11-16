#!/bin/sh
# release-run.sh <stage|promote> - the release Job's own containers (#7922).
#
# The release Job of PUT /api/v1/sovereign/release runs in the primary region,
# in the namespace of the cutover chart, as its runner service account:
#
#   stage    (first container) release-region.sh stage in the primary region,
#            then in each follower region named by a file of SECONDARY_KUBECONFIGS,
#            as a Job in that region's Gitea namespace (its Gitea answers only
#            there; the same way cutover step 01 reaches it). All regions are
#            staged before anything moves; a conflict or a refusal in any region
#            fails this container, so the prewarm and the promote never run.
#   prewarm  (second container) cutover step 03 itself, reading the pins from
#            release/<candidate>. Not this script.
#   promote  (last container) release-region.sh check in every region, then
#            promote in every region, the followers first. A branch that moved
#            after staging stops the run before any region is pushed.
#
# Every region's result and report go to the ConfigMap RELEASE_STATUS_CM; the
# recorded commits go to STATE, the Pod's shared emptyDir.
set -eu
: "${RELEASE_NAMESPACE:?}" "${RELEASE_STATUS_CM:?}" "${CANDIDATE:?}"
: "${DRY_RUN:=false}" "${STATE:=/release-state}" "${SECONDARY_KUBECONFIGS:=/secondary-kubeconfigs}"
: "${FOLLOWER_WAIT_SECONDS:=3600}" "${FOLLOWER_POLL_SECONDS:=10}" "${RELEASE_SCRIPTS:=/release}"
: "${SECONDARY_GITEA_NAMESPACE:=gitea}"
: "${HARBOR_NAMESPACE:=harbor}" "${HARBOR_REGISTRY_CONFIGMAP:=harbor-registry}"
mkdir -p "${STATE}"

now() { date -u +%Y-%m-%dT%H:%M:%SZ; }
say() { echo "[release] $*"; }

# status_put --arg <key> <value> ...: merge the keys into the status ConfigMap.
status_put() {
  _j=$(jq -cn '$ARGS.named' "$@")
  kubectl -n "${RELEASE_NAMESPACE}" patch configmap "${RELEASE_STATUS_CM}" --type merge -p "{\"data\":${_j}}" >/dev/null \
    || say "WARN: could not write ${RELEASE_NAMESPACE}/${RELEASE_STATUS_CM}; this log keeps every line"
}

state_get() { cat "${STATE}/$1.$2" 2>/dev/null || true; }

# record <region> <phase> <file>: keep the region's last result line and report.
record() {
  _line=$(grep '^release-result ' "$3" | tail -n 1 || true)
  [ -n "${_line}" ] || _line="release-result region=$1 phase=failed"
  for _k in main staged candidate; do
    _v=$(printf '%s\n' "${_line}" | sed -n "s/.* ${_k}=\([^ ]*\).*/\1/p")
    [ -z "${_v}" ] || printf '%s' "${_v}" >"${STATE}/$1.${_k}"
  done
  printf '%s\n' "${_line}" | sed -n 's/.* phase=\([^ ]*\).*/\1/p' >"${STATE}/$1.$2.phase"
  _rep=$(grep '^release-report ' "$3" | sed 's/^release-report region=[^ ]* //' || true)
  status_put --arg "$1.$2" "${_line#release-result }" --arg "$1.$2.report" "${_rep}" --arg updatedAt "$(now)"
}

followers() {
  for _kc in "${SECONDARY_KUBECONFIGS}"/*.yaml; do [ -e "${_kc}" ] && echo "${_kc}"; done
}

# registry_storage [<kubeconfig>]: where a region's registry stores, from the
# storage: section of HARBOR_NAMESPACE/HARBOR_REGISTRY_CONFIGMAP (config.yml):
# "<driver> bucket=<b> endpoint=<e> root=<r>"; empty when it cannot be read.
registry_storage() {
  if [ -n "${1:-}" ]; then set -- --kubeconfig "$1"; fi
  kubectl "$@" -n "${HARBOR_NAMESPACE}" get configmap "${HARBOR_REGISTRY_CONFIGMAP}" -o 'jsonpath={.data.config\.yml}' 2>/dev/null \
    | awk '/^storage:/ {s = 1; next} s && /^[^ ]/ {s = 0}
        s && /^  (s3|gcs|azure|swift|oss|filesystem|inmemory):/ {d = $1; sub(/:$/, "", d); in_d = 1; next}
        s && /^  [^ ]/ {in_d = 0}
        in_d && /^    (bucket|container|regionendpoint|accountname|rootdirectory):/ {k = $1; sub(/:$/, "", k); v[k] = $2}
        END {if (d != "") printf "%s bucket=%s%s endpoint=%s%s root=%s\n", d, v["bucket"], v["container"], v["regionendpoint"], v["accountname"], v["rootdirectory"]}' || true
}

# follower_storage: when the installed step 03 does not mirror the release's
# pins into a follower's registry (FOLLOWER_PREWARM_COVERED=false), whether each
# follower's registry stores in the primary's object storage all the same; then
# it serves what the primary's prewarm copies. The verdict replaces the state's
# followerPrewarm, which the route wrote as unknown.
follower_storage() {
  [ "${FOLLOWER_PREWARM_COVERED:-true}" = "true" ] && return 0
  [ -n "$(followers)" ] || return 0
  _ps=$(registry_storage); _own=""; _unk=""
  for _kc in $(followers); do
    _r=$(basename "${_kc}" .yaml); _fs=$(registry_storage "${_kc}")
    case "${_ps} ${_fs}" in
      " "*|*" ") _unk="${_unk} ${_r}" ;;
      filesystem*|inmemory*|*" filesystem"*|*" inmemory"*) _own="${_own} ${_r}" ;;
      *) [ "${_fs}" = "${_ps}" ] || _own="${_own} ${_r}" ;;
    esac
  done
  if [ -n "${_own}" ]; then
    _fp="not covered: the registry of${_own} does not store in the primary's (${_ps:-unknown}), and the installed step 03 mirrors a follower's running set only; request the same candidate again once the release has installed a cutover chart that mirrors the release's pins"
  elif [ -n "${_unk}" ]; then
    _fp="unknown: the registry storage of the primary or of${_unk} cannot be read (${HARBOR_NAMESPACE}/${HARBOR_REGISTRY_CONFIGMAP}), and the installed step 03 mirrors a follower's running set only"
  else
    _fp="covered by shared storage: every follower's registry stores in the primary's (${_ps}), so it serves what the primary's prewarm copies"
  fi
  say "followerPrewarm: ${_fp}"
  status_put --arg followerPrewarm "${_fp}" --arg updatedAt "$(now)"
}

primary_pat=""
# run_primary <phase>: release-region.sh in this region, with the Gitea token
# cutover step 01 reads (the catalyst-gitea-token PAT).
run_primary() {
  if [ -z "${primary_pat}" ]; then
    primary_pat=$(kubectl -n "${PAT_SOURCE_NAMESPACE}" get secret "${PAT_SOURCE_SECRET_NAME}" \
      -o "jsonpath={.data.${PAT_SOURCE_KEY}}" 2>/dev/null | base64 -d 2>/dev/null || true)
  fi
  _out="${STATE}/primary.$1.out"
  _rc=0
  GITEA_PAT="${primary_pat}" REGION=primary PHASE="$1" MAIN_SHA="$(state_get primary main)" \
    STAGED_SHA="$(state_get primary staged)" WORK="${STATE}/work-primary-$1" \
    sh "${RELEASE_SCRIPTS}/release-region.sh" >"${_out}" 2>&1 || _rc=$?
  cat "${_out}"
  record primary "$1" "${_out}"
  rm -rf "${STATE}/work-primary-$1"
  return "${_rc}"
}

# run_follower <kubeconfig> <phase>: release-region.sh as a Job in that region.
run_follower() {
  _kc="$1"; _ph="$2"
  _region=$(basename "${_kc}" .yaml)
  [ -r "${RELEASE_SCRIPTS}/follower-job.yaml" ] || { say "FAILED: follower ${_region} has no Job manifest in ${RELEASE_SCRIPTS}"; return 1; }
  _job=$(printf 'sovereign-release-%s-%s' "${_ph}" "${_region}" | cut -c1-63 | sed 's/-*$//')
  _out="${STATE}/${_region}.${_ph}.out"
  kubectl --kubeconfig "${_kc}" -n "${SECONDARY_GITEA_NAMESPACE}" create configmap sovereign-release-scripts \
    --from-file=release-region.sh="${RELEASE_SCRIPTS}/release-region.sh" \
    --from-file=kit-rewrite.sh="${KIT_REWRITE}" --from-file=extract-pins.sh="${PIN_EXTRACTOR}" \
    --dry-run=client -o yaml | kubectl --kubeconfig "${_kc}" apply --server-side --force-conflicts --field-manager=sovereign-release -f - >/dev/null
  kubectl --kubeconfig "${_kc}" -n "${SECONDARY_GITEA_NAMESPACE}" delete job "${_job}" --ignore-not-found=true --wait=true >/dev/null
  sed -e "s|__JOB__|${_job}|g" -e "s|__REGION__|${_region}|g" -e "s|__PHASE__|${_ph}|g" \
    -e "s|__MAIN_SHA__|$(state_get "${_region}" main)|g" -e "s|__STAGED_SHA__|$(state_get "${_region}" staged)|g" \
    "${RELEASE_SCRIPTS}/follower-job.yaml" | kubectl --kubeconfig "${_kc}" apply --server-side --force-conflicts --field-manager=sovereign-release -f - >/dev/null
  say "follower ${_region}: ${_ph} runs as Job ${SECONDARY_GITEA_NAMESPACE}/${_job}"
  _deadline=$(( $(date +%s) + FOLLOWER_WAIT_SECONDS ))
  _end=""
  while [ "$(date +%s)" -lt "${_deadline}" ]; do
    _ok=$(kubectl --kubeconfig "${_kc}" -n "${SECONDARY_GITEA_NAMESPACE}" get job "${_job}" -o jsonpath='{.status.succeeded}' 2>/dev/null || true)
    _bad=$(kubectl --kubeconfig "${_kc}" -n "${SECONDARY_GITEA_NAMESPACE}" get job "${_job}" -o jsonpath='{.status.failed}' 2>/dev/null || true)
    if [ "${_ok:-0}" -ge 1 ] 2>/dev/null; then _end=ok; break; fi
    if [ "${_bad:-0}" -ge 1 ] 2>/dev/null; then _end=failed; break; fi
    sleep "${FOLLOWER_POLL_SECONDS}"
  done
  kubectl --kubeconfig "${_kc}" -n "${SECONDARY_GITEA_NAMESPACE}" logs "job/${_job}" --tail=-1 >"${_out}" 2>&1 || true
  [ -n "${_end}" ] || echo "release-report region=${_region} reason the Job did not end within ${FOLLOWER_WAIT_SECONDS}s" >>"${_out}"
  cat "${_out}"
  record "${_region}" "${_ph}" "${_out}"
  [ "${_end}" = ok ]
}

# verdict <phase>: the worst phase any region recorded for <phase>.
verdict() {
  _worst=""
  for _f in "${STATE}"/*."$1".phase; do
    [ -e "${_f}" ] || continue
    case "$(cat "${_f}")" in
      conflict) _worst=conflict ;;
      moved) [ "${_worst}" = conflict ] || _worst=moved ;;
      refused) case "${_worst}" in conflict|moved) ;; *) _worst=refused ;; esac ;;
      failed) [ -n "${_worst}" ] || _worst=failed ;;
    esac
  done
  echo "${_worst}"
}

case "${1:-}" in
  stage)
    status_put --arg phase staging --arg updatedAt "$(now)"
    follower_storage
    _rc=0
    run_primary stage || _rc=1
    for kc in $(followers); do run_follower "${kc}" stage || _rc=1; done
    if [ "${_rc}" -ne 0 ]; then
      _v=$(verdict stage); _v="${_v:-failed}"
      status_put --arg phase "${_v}" --arg message "stage ${_v} in a region: nothing moved in any region; see <region>.stage.report" --arg updatedAt "$(now)"
      say "stage ${_v}: nothing moved in any region"
      exit 1
    fi
    if [ "${DRY_RUN}" = "true" ]; then
      status_put --arg phase dry-run --arg message "dry run: every region merged rc/${CANDIDATE} cleanly in a scratch copy; nothing was pushed" --arg updatedAt "$(now)"
      say "dry run complete: nothing was pushed"
      exit 0
    fi
    status_put --arg phase staged --arg message "release/${CANDIDATE} is staged in every region; the prewarm copies what it pins" --arg updatedAt "$(now)"
    ;;
  promote)
    status_put --arg phase promoting --arg updatedAt "$(now)"
    _rc=0
    run_primary check || _rc=1
    for kc in $(followers); do run_follower "${kc}" check || _rc=1; done
    if [ "${_rc}" -ne 0 ]; then
      _v=$(verdict check); _v="${_v:-failed}"
      status_put --arg phase "${_v}" --arg message "a region changed after staging: no region was promoted; request the release again" --arg updatedAt "$(now)"
      say "check ${_v}: no region was promoted"
      exit 1
    fi
    for kc in $(followers); do run_follower "${kc}" promote || _rc=1; done
    [ "${_rc}" -ne 0 ] || run_primary promote || _rc=1
    if [ "${_rc}" -ne 0 ]; then
      status_put --arg phase partial --arg message "a promote failed after every region checked clean; <region>.promote names which regions moved" --arg updatedAt "$(now)"
      exit 1
    fi
    status_put --arg phase delivered --arg message "every region follows rc/${CANDIDATE} merged with its own commits" --arg deliveredAt "$(now)" --arg updatedAt "$(now)"
    say "delivered rc/${CANDIDATE}"
    ;;
  *) say "usage: release-run.sh stage|promote"; exit 2 ;;
esac
