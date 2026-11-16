#!/bin/sh
# release-run.sh <stage|deliver|compose|prewarm|promote> - the containers of a
# release delivery (#7922, #7938).
#
# A delivery runs the mechanism of the release it delivers. PUT
# /api/v1/sovereign/release starts the BOOTSTRAP Job, in the primary region, in
# the namespace of the cutover chart, as its runner service account. Its two
# containers run this script as the installed catalyst-api ships it:
#
#   stage    release-region.sh stage in the primary region, then in each
#            follower region named by a file of SECONDARY_KUBECONFIGS, as a Job
#            in that region's Gitea namespace (its Gitea answers only there; the
#            same way cutover step 01 reaches it). All regions are staged before
#            anything moves; a conflict or a refusal in any region fails this
#            container. Then mechanism_render: the cutover chart of the staged
#            commit, verified to be the one the candidate pins, rendered with
#            the installed cutover HelmRelease's values. A dry run stops here. A
#            delivery copies the pull credential into every follower
#            (credential_deliver below).
#   deliver  runs the CANDIDATE's release-run.sh (from that render) "compose".
#
# The candidate's own script then does the rest; the installed step 03 is never
# run, so a repair of the mechanism acts on the delivery that carries it:
#
#   compose  builds the DELIVERY Job from the candidate's step 01 and step 03
#            and its own release scripts, applies it and waits for it:
#   prewarm  (its init container) "prewarm <command...>": the candidate's step
#            03, reading the pins from release/<candidate>. When it ends,
#            whatever its outcome, the pull credential is removed from every
#            region; when it fails, the state says failed, with the step and the
#            line that names the cause.
#   promote  (its container) release-region.sh check in every region, then
#            promote in every region, the followers first. A branch that moved
#            after staging stops the run before any region is pushed.
#
# The contract between the bootstrap and a cutover chart is stated in
# platform/self-sovereign-cutover/chart/README.md, "Release mechanism".
#
# Every region's result and report go to the ConfigMap RELEASE_STATUS_CM; the
# recorded commits go to STATE, the Pod's shared emptyDir.
#
# The pull credential (#7938). PULL_SECRET is the request's pullSecret, a
# Secret of RELEASE_NAMESPACE in this region, and the only source of it. Step
# 03's follower leg reads it through each follower's kubeconfig, as
# RELEASE_NAMESPACE/PULL_SECRET of THAT region (SECONDARY_GHCR_SOURCE_*), and
# copies it on into SECONDARY_HARBOR_NAMESPACE/SECONDARY_GHCR_DEST_SECRET there.
# So the stage of a delivery copies it into every follower, and the end of the
# prewarm, or a failed stage, deletes it in every region: the copies in each
# follower and the source here. No region keeps an upstream credential after a
# delivery (cutover step 06 strips the parent's); the next delivery names one again.
set -eu
: "${RELEASE_NAMESPACE:?}" "${RELEASE_STATUS_CM:?}" "${CANDIDATE:?}"
: "${DRY_RUN:=false}" "${STATE:=/release-state}" "${SECONDARY_KUBECONFIGS:=/secondary-kubeconfigs}"
: "${FOLLOWER_WAIT_SECONDS:=3600}" "${FOLLOWER_POLL_SECONDS:=10}" "${RELEASE_SCRIPTS:=/release}"
: "${SECONDARY_GITEA_NAMESPACE:=gitea}" "${PULL_SECRET:=}"
: "${SECONDARY_HARBOR_NAMESPACE:=}" "${SECONDARY_GHCR_DEST_SECRET:=}"
: "${HARBOR_NAMESPACE:=harbor}" "${HARBOR_REGISTRY_CONFIGMAP:=harbor-registry}"
: "${CUTOVER_HELMRELEASE:=flux-system/bp-self-sovereign-cutover}" "${CUTOVER_CHART_PATH:=platform/self-sovereign-cutover/chart}"
: "${CUTOVER_SLOT:=clusters/_template/bootstrap-kit/06a-bp-self-sovereign-cutover.yaml}"
: "${MECHANISM_SUFFIX:=-release}" "${RELEASE_JOB:=sovereign-release}" "${STATE_SEED:=}"
: "${SECONDARY_KUBECONFIGS_SECRET:=cutover-secondary-kubeconfigs}"
mkdir -p "${STATE}"
# A delivery Job starts with what the bootstrap's stage recorded (STATE_SEED).
if [ -n "${STATE_SEED}" ] && [ -d "${STATE_SEED}" ]; then
  for _f in "${STATE_SEED}"/*; do [ -f "${_f}" ] && cp "${_f}" "${STATE}/"; done
fi
MECH="${STATE}/mechanism"

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

# follower_storage: when the step 03 that runs (the candidate's) does not mirror
# the release's pins into a follower's registry (FOLLOWER_PREWARM_COVERED=false),
# whether each follower's registry stores in the primary's object storage all
# the same; then it serves what the primary's prewarm copies. The verdict
# replaces the state's followerPrewarm, which the route wrote as unknown.
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
    _fp="not covered: the registry of${_own} does not store in the primary's (${_ps:-unknown}), and the candidate's step 03 mirrors a follower's running set only, so that registry may lack what the release pins"
  elif [ -n "${_unk}" ]; then
    _fp="unknown: the registry storage of the primary or of${_unk} cannot be read (${HARBOR_NAMESPACE}/${HARBOR_REGISTRY_CONFIGMAP}), and the candidate's step 03 mirrors a follower's running set only"
  else
    _fp="covered by shared storage: every follower's registry stores in the primary's (${_ps}), so it serves what the primary's prewarm copies"
  fi
  say "followerPrewarm: ${_fp}"
  status_put --arg followerPrewarm "${_fp}" --arg updatedAt "$(now)"
}

# credential_deliver: copy PULL_SECRET into RELEASE_NAMESPACE of every follower,
# where step 03's follower leg reads it. Only type and data travel; the copy
# carries the release's labels. On a failure _cred_why names it.
_cred_why=""
credential_deliver() {
  [ -n "${PULL_SECRET}" ] || return 0
  if ! kubectl -n "${RELEASE_NAMESPACE}" get secret "${PULL_SECRET}" -o name >/dev/null 2>&1; then
    _cred_why="the request's pull Secret ${RELEASE_NAMESPACE}/${PULL_SECRET} is absent in the primary region"
    return 1
  fi
  for _kc in $(followers); do
    _r=$(basename "${_kc}" .yaml)
    if ! kubectl -n "${RELEASE_NAMESPACE}" get secret "${PULL_SECRET}" -o json 2>/dev/null \
        | jq -c --arg c "${CANDIDATE}" '{apiVersion: "v1", kind: "Secret", type: .type, data: .data,
            metadata: {name: .metadata.name, namespace: .metadata.namespace,
              labels: {"app.kubernetes.io/component": "sovereign-release", "catalyst.openova.io/release-candidate": $c}}}' \
        | kubectl --kubeconfig "${_kc}" apply --server-side --force-conflicts --field-manager=sovereign-release -f - >/dev/null 2>&1; then
      _cred_why="could not copy the pull credential into ${RELEASE_NAMESPACE}/${PULL_SECRET} of follower ${_r}"
      return 1
    fi
    say "follower ${_r}: the pull credential is in ${RELEASE_NAMESPACE}/${PULL_SECRET} until the prewarm ends"
  done
}

# _cred_del <kubeconfig or ""> <region> <namespace> <name>: delete one Secret.
_cred_del() {
  [ -n "$3" ] && [ -n "$4" ] || return 0
  _cdrc=0
  if [ -n "$1" ]; then
    kubectl --kubeconfig "$1" -n "$3" delete secret "$4" --ignore-not-found=true >/dev/null 2>&1 || _cdrc=1
  else
    kubectl -n "$3" delete secret "$4" --ignore-not-found=true >/dev/null 2>&1 || _cdrc=1
  fi
  if [ "${_cdrc}" -eq 0 ]; then _cred_gone="${_cred_gone} $2/$3/$4"; else _cred_left="${_cred_left} $2/$3/$4"; fi
}

# credential_remove: delete the pull credential in every region (each
# follower's two copies, then the source here) and record the result as the
# status key "credential". Idempotent: what is absent counts as removed.
credential_remove() {
  [ -n "${PULL_SECRET}" ] || return 0
  _cred_gone=""; _cred_left=""
  for _kc in $(followers); do
    _r=$(basename "${_kc}" .yaml)
    _cred_del "${_kc}" "${_r}" "${RELEASE_NAMESPACE}" "${PULL_SECRET}"
    _cred_del "${_kc}" "${_r}" "${SECONDARY_HARBOR_NAMESPACE}" "${SECONDARY_GHCR_DEST_SECRET}"
  done
  _cred_del "" primary "${RELEASE_NAMESPACE}" "${PULL_SECRET}"
  if [ -n "${_cred_left}" ]; then
    say "WARN: the pull credential could not be deleted in:${_cred_left}; delete it there by hand"
    status_put --arg credential "left in:${_cred_left}; delete it there by hand" --arg updatedAt "$(now)"
    return 1
  fi
  say "the pull credential is deleted in every region:${_cred_gone}"
  status_put --arg credential "deleted in every region:${_cred_gone}" --arg updatedAt "$(now)"
}

# first_cause <log>: the last line of a step's log that names why it stopped.
first_cause() {
  _c=$(grep -E 'FATAL|FAILED|REFUSED' "$1" 2>/dev/null | tail -n 1 | sed -e 's/^\[[^]]*\] *//' | cut -c1-600 || true)
  [ -n "${_c}" ] || _c=$(grep -v '^[[:space:]]*$' "$1" 2>/dev/null | tail -n 1 | cut -c1-600 || true)
  printf '%s' "${_c}"
}

# mechanism_render: verify and render the cutover chart the primary's stage
# exported from the staged commit (STATE/mechanism-src). It must be the
# candidate's own folder, unchanged by a commit of this Sovereign, and the chart
# and version the staged kit slot pins, and carry a release mechanism of
# contract 1 (files/release/). Rendered with the spec.values of the installed
# cutover HelmRelease, under its release name and namespace; every ConfigMap of
# the render lands in MECH/cm/<name>.json, the chart's release scripts in MECH.
# A failure leaves the reason in _mech_why.
_mech_why=""
mechanism_render() {
  _mexp="${STATE}/mechanism-src"
  _ch="${_mexp}/${CUTOVER_CHART_PATH}"
  for _t in helm yq jq; do command -v "${_t}" >/dev/null 2>&1 || { _mech_why="${_t} is not in the release image"; return 1; }; done
  [ -f "${_ch}/Chart.yaml" ] || { _mech_why="the staged commit has no ${CUTOVER_CHART_PATH}/Chart.yaml"; return 1; }
  [ "$(cat "${_ch}/files/release/contract" 2>/dev/null)" = 1 ] \
    || { _mech_why="${CUTOVER_CHART_PATH} of rc/${CANDIDATE} carries no release mechanism of contract 1 (files/release/contract): it predates #7938; deliver a later candidate"; return 1; }
  [ "$(cat "${_mexp}/.candidate" 2>/dev/null)" = same ] \
    || { _mech_why="${CUTOVER_CHART_PATH} of the staged commit differs from the candidate's: a commit of this Sovereign changes the release mechanism"; return 1; }
  _cn=$(yq -r '.name' "${_ch}/Chart.yaml"); _cv=$(yq -r '.version' "${_ch}/Chart.yaml")
  _pn=$(yq -r 'select(.kind == "HelmRelease") | .spec.chart.spec.chart' "${_mexp}/${CUTOVER_SLOT}" 2>/dev/null | head -n 1)
  _pv=$(yq -r 'select(.kind == "HelmRelease") | .spec.chart.spec.version' "${_mexp}/${CUTOVER_SLOT}" 2>/dev/null | head -n 1)
  if [ "${_cn}" != "${_pn}" ] || [ "${_cv}" != "${_pv}" ]; then
    _mech_why="the staged ${CUTOVER_CHART_PATH} is ${_cn} ${_cv}, but ${CUTOVER_SLOT} pins ${_pn:-nothing} ${_pv}"; return 1
  fi
  kubectl -n "${CUTOVER_HELMRELEASE%%/*}" get helmreleases.helm.toolkit.fluxcd.io "${CUTOVER_HELMRELEASE#*/}" -o json >"${STATE}/cutover-hr.json" 2>/dev/null \
    || { _mech_why="cannot read the installed cutover HelmRelease ${CUTOVER_HELMRELEASE}, whose values the mechanism is rendered with"; return 1; }
  [ "$(jq '.spec.valuesFrom // [] | length' "${STATE}/cutover-hr.json")" = 0 ] \
    || { _mech_why="the installed cutover HelmRelease ${CUTOVER_HELMRELEASE} takes valuesFrom, which the render does not read"; return 1; }
  jq '.spec.values // {}' "${STATE}/cutover-hr.json" >"${STATE}/cutover-values.json"
  _rel=$(jq -r '.spec.releaseName // .metadata.name' "${STATE}/cutover-hr.json")
  _tns=$(jq -r '.spec.targetNamespace // .metadata.namespace' "${STATE}/cutover-hr.json")
  rm -rf "${MECH}"; mkdir -p "${MECH}/cm" "${STATE}/helm-home"
  HOME="${STATE}/helm-home" HELM_CACHE_HOME="${STATE}/helm-home/cache" HELM_CONFIG_HOME="${STATE}/helm-home/config" HELM_DATA_HOME="${STATE}/helm-home/data" \
    helm template "${_rel}" "${_ch}" --namespace "${_tns}" -f "${STATE}/cutover-values.json" >"${MECH}/render.yaml" 2>"${MECH}/render.err" \
    || { _mech_why="helm template of ${_cn} ${_cv} failed: $(tail -n 1 "${MECH}/render.err")"; return 1; }
  yq -o=json -I=0 'select(.kind == "ConfigMap")' "${MECH}/render.yaml" >"${MECH}/cms.jsonl" \
    || { _mech_why="the render of ${_cn} ${_cv} does not parse"; return 1; }
  while IFS= read -r _l; do
    [ -n "${_l}" ] || continue
    printf '%s\n' "${_l}" >"${MECH}/cm/$(printf '%s' "${_l}" | jq -r .metadata.name).json"
  done <"${MECH}/cms.jsonl"
  for _c in cutover-step-01-gitea-mirror cutover-step-03-harbor-prewarm cutover-step-06-helmrepository-patches cutover-bootstrap-kit-pins; do
    [ -s "${MECH}/cm/${_c}.json" ] || { _mech_why="${_cn} ${_cv} renders no ConfigMap ${_c}, which a delivery runs"; return 1; }
  done
  for _k in release-run.sh release-region.sh; do
    cp "${_ch}/files/release/${_k}" "${MECH}/${_k}" 2>/dev/null || true
    [ -s "${MECH}/${_k}" ] || { _mech_why="${_cn} ${_cv} carries no files/release/${_k}"; return 1; }
  done
  printf '%s %s (chart tree %s)' "${_cn}" "${_cv}" "$(cut -c1-12 "${_mexp}/.tree")" >"${MECH}/version"
}

# mech_podspec <ConfigMap>: the podSpec of a rendered step ConfigMap, as JSON.
mech_podspec() { jq -r '.data.podSpec // empty' "${MECH}/cm/$1.json" | yq -o=json '.'; }

# compose_delivery: the delivery Job of THIS mechanism (JSON on stdout), from the
# rendered step 03 (prewarm, wrapped by "prewarm") and step 01 (promote). Every
# ConfigMap a container mounts is the render's, applied under
# <name>${MECHANISM_SUFFIX} so no installed object changes.
compose_delivery() {
  for _s in 03:cutover-step-03-harbor-prewarm 01:cutover-step-01-gitea-mirror; do
    mech_podspec "${_s#*:}" >"${MECH}/p${_s%%:*}.json" 2>/dev/null || true
    [ -s "${MECH}/p${_s%%:*}.json" ] || { _mech_why="the rendered ${_s#*:} has no podSpec"; return 1; }
  done
  jq -n --slurpfile p3 "${MECH}/p03.json" --slurpfile p1 "${MECH}/p01.json" --arg job "${RELEASE_JOB}-delivery" \
    --arg ns "${RELEASE_NAMESPACE}" --arg cand "${CANDIDATE}" --arg pull "${PULL_SECRET}" --arg sfx "${MECHANISM_SUFFIX}" \
    --arg st "${RELEASE_STATUS_CM}" --arg kcs "${SECONDARY_KUBECONFIGS_SECRET}" --arg fns "${SECONDARY_GITEA_NAMESPACE}" '
    def plain($n; $v): {name: $n, value: $v};
    def setenv($e): if any(.[]; .name == $e.name) then map(if .name == $e.name then $e else . end) else . + [$e] end;
    def setall($es): reduce $es[] as $e (.; setenv($e));
    def envof($c; $n): ([($c.env // [])[] | select(.name == $n) | .value] | .[0]) // "";
    $p3[0] as $s3 | $p1[0] as $s1 | $s3.containers[0] as $c3 |
    if ([($s3.volumes // [])[].name] | any(startswith("release-"))) then error("the rendered step 03 has a volume named release-*") else . end |
    ([plain("CANDIDATE"; $cand), plain("DRY_RUN"; "false"), plain("RELEASE_NAMESPACE"; $ns), plain("RELEASE_STATUS_CM"; $st),
      plain("RELEASE_SCRIPTS"; "/release"), plain("STATE"; "/release-state"), plain("STATE_SEED"; "/release-recorded"),
      plain("RELEASE_JOB"; $job), plain("PULL_SECRET"; $pull)]
     + ([("SECONDARY_HARBOR_NAMESPACE", "SECONDARY_GHCR_DEST_SECRET") as $n | select(envof($c3; $n) != "") | plain($n; envof($c3; $n))])) as $common |
    ($c3 | .name = "prewarm" | .args = ((.command // []) + (.args // [])) | .command = ["/bin/sh", "/release/release-run.sh", "prewarm"]
      | .env = ((.env // []) | setenv(plain("UPSTREAM_BRANCH"; "release/" + $cand))
          | setenv({name: "GHCR_DOCKERCONFIG", valueFrom: {secretKeyRef: {name: $pull, key: ".dockerconfigjson"}}})
          | setenv(plain("GHCR_DOCKERCONFIG_NAMESPACE"; $ns))
          | if any(.[]; .name == "SECONDARY_GHCR_SOURCE_SECRET") then setenv(plain("SECONDARY_GHCR_SOURCE_NAMESPACE"; $ns)) | setenv(plain("SECONDARY_GHCR_SOURCE_SECRET"; $pull)) else . end
          | setall($common + [plain("SECONDARY_KUBECONFIGS"; "/release-secondary-kubeconfigs")]))
      | .volumeMounts = ((.volumeMounts // []) + [{name: "release-scripts", mountPath: "/release", readOnly: true},
          {name: "release-state", mountPath: "/release-state"}, {name: "release-recorded", mountPath: "/release-recorded", readOnly: true},
          {name: "release-secondary-kubeconfigs", mountPath: "/release-secondary-kubeconfigs", readOnly: true}])) as $prewarm |
    ($s1.containers[0] | .name = "promote" | .command = ["/bin/sh", "/release/release-run.sh", "promote"] | del(.args)
      | .env = ((.env // []) | setall([plain("HOME"; "/tmp")] + $common + [plain("UPSTREAM_PREFIX"; envof($c3; "UPSTREAM_PREFIX")),
          plain("KIT_REWRITE"; "/kit-rewrite/kit-rewrite.sh"), plain("PIN_EXTRACTOR"; "/pin-extractor/extract-pins.sh"),
          plain("SECONDARY_KUBECONFIGS"; "/secondary-kubeconfigs"), plain("SECONDARY_GITEA_NAMESPACE"; $fns)]))
      | .volumeMounts = [{name: "release-scripts", mountPath: "/release", readOnly: true}, {name: "release-kit-rewrite", mountPath: "/kit-rewrite", readOnly: true},
          {name: "release-pins", mountPath: "/pin-extractor", readOnly: true}, {name: "release-state", mountPath: "/release-state"},
          {name: "release-recorded", mountPath: "/release-recorded", readOnly: true},
          {name: "release-secondary-kubeconfigs", mountPath: "/secondary-kubeconfigs", readOnly: true}]) as $promote |
    {app: "sovereign-release", cand: $cand} as $l |
    {apiVersion: "batch/v1", kind: "Job",
     metadata: {name: $job, namespace: $ns, labels: {"app.kubernetes.io/component": "sovereign-release", "catalyst.openova.io/release-candidate": $cand}},
     spec: {backoffLimit: 0, activeDeadlineSeconds: (($s3.activeDeadlineSeconds // 0) + 7200), ttlSecondsAfterFinished: 259200,
       template: {metadata: {labels: {"app.kubernetes.io/component": "sovereign-release"}},
         spec: ({serviceAccountName: $s3.serviceAccountName, restartPolicy: "Never", securityContext: $s3.securityContext,
           imagePullSecrets: $s3.imagePullSecrets, nodeSelector: $s3.nodeSelector, tolerations: $s3.tolerations, affinity: $s3.affinity,
           volumes: ((($s3.volumes // []) | map(if .configMap then .configMap.name += $sfx else . end)) + [
             {name: "release-scripts", configMap: {name: ("sovereign-release-delivery" + $sfx), defaultMode: 493}},
             {name: "release-kit-rewrite", configMap: {name: ("cutover-step-06-helmrepository-patches" + $sfx), items: [{key: "kit-rewrite.sh", path: "kit-rewrite.sh"}]}},
             {name: "release-pins", configMap: {name: ("cutover-bootstrap-kit-pins" + $sfx), items: [{key: "extract-pins.sh", path: "extract-pins.sh"}]}},
             {name: "release-state", emptyDir: {}},
             {name: "release-recorded", configMap: {name: "sovereign-release-recorded"}},
             {name: "release-secondary-kubeconfigs", secret: {secretName: $kcs, optional: true}}]),
           initContainers: [$prewarm], containers: [$promote]} | with_entries(select(.value != null)))}}}'
}

# follower_job_of_mechanism: the rendered follower pivot Job of step 06, turned
# into one phase of release-region.sh (the bootstrap's route does the same for
# its stage), YAML on stdout; nothing when the render has none.
follower_job_of_mechanism() {
  _fj=$(jq -r '.data["secondary-pivot-job.yaml"] // empty' "${MECH}/cm/cutover-secondary-pivot-script.json" 2>/dev/null || true)
  [ -n "${_fj}" ] || return 0
  _urepo=$(jq -r '([.containers[0].env[]? | select(.name == "UPSTREAM_REPO_URL") | .value] | .[0]) // ""' "${MECH}/p01.json")
  printf '%s\n' "${_fj}" | yq -o=json '.' | jq --arg cand "${CANDIDATE}" --arg urepo "${_urepo}" '
    def plain($n; $v): {name: $n, value: $v};
    def setenv($e): if any(.[]; .name == $e.name) then map(if .name == $e.name then $e else . end) else . + [$e] end;
    .metadata.name = "__JOB__" | del(.metadata.resourceVersion, .metadata.uid)
    | .metadata.labels = ((.metadata.labels // {}) + {"app.kubernetes.io/component": "sovereign-release", "catalyst.openova.io/release-candidate": $cand, "catalyst.openova.io/secondary-region": "__REGION__"})
    | .spec.backoffLimit = 0 | .spec.activeDeadlineSeconds = 7200 | .spec.ttlSecondsAfterFinished = 259200
    | .spec.template.metadata.labels = ((.spec.template.metadata.labels // {}) + {"app.kubernetes.io/component": "sovereign-release"})
    | .spec.template.spec.containers[0] |= (.name = "release" | .command = ["/bin/sh", "/release/release-region.sh"] | del(.args)
        | .env = ((.env // []) | map(select(.name != "LOCAL_PREFIX" and .name != "TRUST_SECRET"))
            | reduce ([plain("HOME"; "/tmp"), plain("REGION"; "__REGION__"), plain("PHASE"; "__PHASE__"), plain("CANDIDATE"; $cand),
                plain("DRY_RUN"; "false"), plain("MAIN_SHA"; "__MAIN_SHA__"), plain("STAGED_SHA"; "__STAGED_SHA__"),
                plain("UPSTREAM_REPO_URL"; $urepo), plain("KIT_REWRITE"; "/release/kit-rewrite.sh"), plain("PIN_EXTRACTOR"; "/release/extract-pins.sh")])[] as $e (.; setenv($e)))
        | .volumeMounts = [{name: "release", mountPath: "/release", readOnly: true}])
    | .spec.template.spec.volumes = [{name: "release", configMap: {name: "sovereign-release-scripts", defaultMode: 493}}]' | yq -p=json -o=yaml '.'
}

# apply_json: server-side apply of the JSON object on stdin, as the release.
apply_json() { kubectl apply --server-side --force-conflicts --field-manager=sovereign-release -f - >/dev/null; }

# run_delivery: apply what the delivery Job mounts and the Job, then wait for it.
run_delivery() {
  _dj="${RELEASE_JOB}-delivery"
  compose_delivery >"${MECH}/delivery-job.json" 2>"${MECH}/compose.err" || { _mech_why="cannot compose the delivery Job: ${_mech_why:-$(tail -n 1 "${MECH}/compose.err")}"; return 1; }
  for _c in $(jq -r --arg s "${MECHANISM_SUFFIX}" '.spec.template.spec.volumes[].configMap.name // empty | select(endswith($s)) | rtrimstr($s)' "${MECH}/delivery-job.json" | sort -u); do
    [ "${_c}" != sovereign-release-delivery ] || continue
    [ -s "${MECH}/cm/${_c}.json" ] || { _mech_why="the delivery mounts ConfigMap ${_c}, which the render lacks"; return 1; }
    jq --arg n "${_c}${MECHANISM_SUFFIX}" --arg ns "${RELEASE_NAMESPACE}" --arg cand "${CANDIDATE}" \
      '{apiVersion: "v1", kind: "ConfigMap", metadata: {name: $n, namespace: $ns, labels: {"app.kubernetes.io/component": "sovereign-release", "catalyst.openova.io/release-candidate": $cand}}, data: .data}' \
      "${MECH}/cm/${_c}.json" | apply_json || { _mech_why="cannot apply ConfigMap ${_c}${MECHANISM_SUFFIX}"; return 1; }
  done
  follower_job_of_mechanism >"${MECH}/follower-job.yaml" || { _mech_why="cannot turn the rendered follower pivot Job into the release's"; return 1; }
  if [ -n "$(followers)" ] && [ ! -s "${MECH}/follower-job.yaml" ]; then _mech_why="this Sovereign has follower regions, and the render has no follower pivot Job (cutover-secondary-pivot-script)"; return 1; fi
  _scripts=$(jq -n --rawfile a "${MECH}/release-run.sh" --rawfile b "${MECH}/release-region.sh" --rawfile c "${MECH}/follower-job.yaml" '{"release-run.sh": $a, "release-region.sh": $b, "follower-job.yaml": $c}')
  printf '%s' "${_scripts}" | jq --arg n "sovereign-release-delivery${MECHANISM_SUFFIX}" --arg ns "${RELEASE_NAMESPACE}" \
    '{apiVersion: "v1", kind: "ConfigMap", metadata: {name: $n, namespace: $ns, labels: {"app.kubernetes.io/component": "sovereign-release"}}, data: .}' \
    | apply_json || { _mech_why="cannot apply the delivery's scripts"; return 1; }
  _rec='{}'
  for _f in "${STATE}"/*.main "${STATE}"/*.staged "${STATE}"/*.candidate; do
    [ -f "${_f}" ] || continue
    _rec=$(printf '%s' "${_rec}" | jq -c --arg k "$(basename "${_f}")" --rawfile v "${_f}" '. + {($k): $v}')
  done
  printf '%s' "${_rec}" | jq --arg ns "${RELEASE_NAMESPACE}" \
    '{apiVersion: "v1", kind: "ConfigMap", metadata: {name: "sovereign-release-recorded", namespace: $ns, labels: {"app.kubernetes.io/component": "sovereign-release"}}, data: .}' \
    | apply_json || { _mech_why="cannot record the staged commits for the delivery"; return 1; }
  apply_json <"${MECH}/delivery-job.json" || { _mech_why="cannot create the delivery Job ${_dj}"; return 1; }
  status_put --arg deliveryJob "${_dj}" --arg updatedAt "$(now)"
  say "the delivery runs as Job ${RELEASE_NAMESPACE}/${_dj}: prewarm and promote of $(cat "${MECH}/version")"
  _deadline=$(( $(date +%s) + $(jq '.spec.activeDeadlineSeconds' "${MECH}/delivery-job.json") + 600 ))
  _end=""
  while [ "$(date +%s)" -lt "${_deadline}" ]; do
    _ok=$(kubectl -n "${RELEASE_NAMESPACE}" get job "${_dj}" -o jsonpath='{.status.succeeded}' 2>/dev/null || true)
    _bad=$(kubectl -n "${RELEASE_NAMESPACE}" get job "${_dj}" -o jsonpath='{.status.failed}' 2>/dev/null || true)
    if [ "${_ok:-0}" -ge 1 ] 2>/dev/null; then _end=ok; break; fi
    if [ "${_bad:-0}" -ge 1 ] 2>/dev/null; then _end=failed; break; fi
    sleep "${FOLLOWER_POLL_SECONDS}"
  done
  kubectl -n "${RELEASE_NAMESPACE}" logs "job/${_dj}" --all-containers=true --tail=-1 2>&1 || true
  case "${_end}" in
    ok) return 0 ;;
    failed) return 3 ;;   # its own containers recorded the step and the cause
    *) _mech_why="the delivery Job ${_dj} did not end within its deadline"; return 1 ;;
  esac
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
  _mo=""; [ "$1" != stage ] || _mo="${STATE}/mechanism-src"
  GITEA_PAT="${primary_pat}" REGION=primary PHASE="$1" MAIN_SHA="$(state_get primary main)" \
    STAGED_SHA="$(state_get primary staged)" WORK="${STATE}/work-primary-$1" MECHANISM_OUT="${_mo}" \
    CUTOVER_CHART_PATH="${CUTOVER_CHART_PATH}" CUTOVER_SLOT="${CUTOVER_SLOT}" \
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
    _rc=0
    run_primary stage || _rc=1
    for kc in $(followers); do run_follower "${kc}" stage || _rc=1; done
    if [ "${_rc}" -ne 0 ]; then
      _v=$(verdict stage); _v="${_v:-failed}"
      status_put --arg phase "${_v}" --arg failedStep stage --arg message "stage ${_v} in a region: nothing moved in any region; see <region>.stage.report" --arg updatedAt "$(now)"
      say "stage ${_v}: nothing moved in any region"
      [ "${DRY_RUN}" = "true" ] || credential_remove || true
      exit 1
    fi
    if ! mechanism_render; then
      status_put --arg phase failed --arg failedStep stage --arg mechanism "none: ${_mech_why}" \
        --arg message "stage failed: the release mechanism of rc/${CANDIDATE} cannot run: ${_mech_why}; nothing moved in any region" --arg updatedAt "$(now)"
      say "FAILED: the release mechanism of rc/${CANDIDATE}: ${_mech_why}; nothing moved in any region"
      [ "${DRY_RUN}" = "true" ] || credential_remove || true
      exit 1
    fi
    status_put --arg mechanism "$(cat "${MECH}/version"), the candidate's, rendered from release/${CANDIDATE}; the installed step 03 does not run" --arg updatedAt "$(now)"
    say "the delivery runs the candidate's mechanism: $(cat "${MECH}/version")"
    # Whether a follower's registry gets what the release pins depends on the
    # step 03 that runs: the candidate's.
    if jq -r '.data["run.sh"] // ""' "${MECH}/cm/cutover-step-03-harbor-prewarm.json" | grep -q "${FOLLOWER_PREWARM_MARKER:-also mirrors the pins of}"; then
      [ -z "$(followers)" ] || status_put --arg followerPrewarm "covered: the candidate's step 03 mirrors the release's pins into every follower registry" --arg updatedAt "$(now)"
    else
      FOLLOWER_PREWARM_COVERED=false follower_storage
    fi
    if [ "${DRY_RUN}" = "true" ]; then
      status_put --arg phase dry-run --arg message "dry run: every region merged rc/${CANDIDATE} cleanly in a scratch copy; nothing was pushed" --arg updatedAt "$(now)"
      say "dry run complete: nothing was pushed"
      exit 0
    fi
    if ! credential_deliver; then
      status_put --arg phase failed --arg failedStep stage --arg message "stage failed: ${_cred_why}; nothing moved in any region" --arg updatedAt "$(now)"
      say "FAILED: ${_cred_why}; nothing moved in any region"
      credential_remove || true
      exit 1
    fi
    status_put --arg phase staged --arg message "release/${CANDIDATE} is staged in every region; the prewarm copies what it pins" --arg updatedAt "$(now)"
    ;;
  deliver)
    # The bootstrap's last container: the candidate's own release-run.sh runs
    # the delivery. What it does not get to record is recorded here.
    if [ ! -s "${MECH}/release-run.sh" ]; then
      status_put --arg phase failed --arg failedStep deliver --arg message "no rendered release mechanism in ${MECH}: nothing moved in any region" --arg updatedAt "$(now)"
      credential_remove || true
      exit 1
    fi
    _drc=0
    sh "${MECH}/release-run.sh" compose || _drc=$?
    if [ "${_drc}" -ne 0 ]; then
      credential_remove || true
      _ph=$(kubectl -n "${RELEASE_NAMESPACE}" get configmap "${RELEASE_STATUS_CM}" -o 'jsonpath={.data.phase}' 2>/dev/null || true)
      if [ "${_ph}" = staged ]; then
        status_put --arg phase failed --arg failedStep deliver --arg message "the candidate's release-run.sh compose exited ${_drc} before the delivery recorded a cause; nothing moved in any region" --arg updatedAt "$(now)"
      fi
      exit "${_drc}"
    fi
    ;;
  compose)
    # The candidate's: build the delivery Job of this mechanism, and run it.
    _crc=0; run_delivery || _crc=$?
    case "${_crc}" in
      0) ;;
      3) say "the delivery Job failed; its state names the step and the cause"; exit 1 ;;
      *)
        status_put --arg phase failed --arg failedStep deliver --arg message "the delivery did not run: ${_mech_why}; nothing moved in any region" --arg updatedAt "$(now)"
        say "FAILED: ${_mech_why}"
        exit 1 ;;
    esac
    ;;
  prewarm)
    # prewarm <command...>: step 03's own command. Its output goes to this
    # container's log as it runs and to STATE/prewarm.log, for the cause.
    shift
    [ "$#" -gt 0 ] || { say "usage: release-run.sh prewarm <command of cutover step 03...>"; exit 2; }
    rm -f "${STATE}/prewarm.rc"
    { _prc=0; "$@" || _prc=$?; echo "${_prc}" >"${STATE}/prewarm.rc"; } 2>&1 | tee "${STATE}/prewarm.log"
    _prc=$(cat "${STATE}/prewarm.rc" 2>/dev/null || echo 1)
    credential_remove || true
    if [ "${_prc}" -ne 0 ]; then
      _cause=$(first_cause "${STATE}/prewarm.log")
      status_put --arg phase failed --arg failedStep prewarm \
        --arg message "prewarm failed (cutover step 03 exited ${_prc}): ${_cause:-its log names no cause}; nothing moved in any region" --arg updatedAt "$(now)"
      say "prewarm failed (exit ${_prc}): nothing moved in any region"
      exit "${_prc}"
    fi
    ;;
  promote)
    status_put --arg phase promoting --arg updatedAt "$(now)"
    # A deletion the prewarm could not finish is tried again; nothing is left
    # to delete on the usual path.
    credential_remove || true
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
  *) say "usage: release-run.sh stage|deliver|compose|prewarm <command...>|promote"; exit 2 ;;
esac
