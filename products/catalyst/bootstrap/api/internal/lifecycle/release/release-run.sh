#!/bin/sh
# release-run.sh <stage|deliver|verify|compose|prewarm|promote|rollback> - the
# containers of a release delivery (#7922, #7938, SOV-521).
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
#            the installed cutover HelmRelease's values. A dry run then runs the
#            CANDIDATE's release-run.sh "verify" and stops. A delivery copies
#            the pull credential into every follower (credential_deliver below).
#   deliver  runs the CANDIDATE's release-run.sh (from that render) "compose".
#
# The candidate's own script then does the rest; the installed step 03 is never
# run, so a repair of the mechanism acts on the delivery that carries it:
#
#   verify   composes every object the delivery creates, in every region (the
#            plans below), and sends each to the API server as a server-side
#            dry run, as the account that will create it; every refusal is
#            named, not the first.
#   compose  builds the DELIVERY Job from the candidate's step 01 and step 03
#            and its own release scripts; runs verify's dry run, and creates
#            nothing unless the API server accepts every object; then creates
#            the Job and waits for it:
#   prewarm  (its init container) "prewarm <command...>": the candidate's step
#            03, reading the pins from release/<candidate>. When it ends,
#            whatever its outcome, the pull credential is removed from every
#            region; when it fails, the state says failed, with the step and the
#            line that names the cause.
#   promote  (its container) release-region.sh check in every region, then
#            promote in every region, the followers first. A branch that moved
#            after staging stops the run before any region is pushed. Each
#            region whose main it moves is recorded in the state:
#            <region>.promotedFrom (the main recorded at stage),
#            <region>.promotedTo (the commit pushed) and <region>.phase.
#
# One region (SOV-521). A request may name one region, RELEASE_REGION: primary
# or a follower's name (the basename of its kubeconfig). The stage and the
# prewarm run as above, in every region, so a conflict anywhere moves nothing;
# promote checks and pushes that region only. Without it every region is
# promoted, as above. The bootstrap's stage refuses a region request when the
# candidate's release-run.sh has no phase rollback (read as verify is read), and
# nothing moves: that mechanism promotes every region.
#
#   rollback (the bootstrap's only container of a rollback request) puts
#            RELEASE_REGION back to PROMOTED_FROM, the main recorded at the
#            stage of the request that promoted it, as one commit on top of
#            PROMOTED_TO, the commit that promote pushed (release-region.sh
#            rollback): a fast-forward, never a force push. No stage, no
#            prewarm, no credential. The route sends it only when the state
#            records that promote of this candidate, which only a
#            release-run.sh with this phase records.
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
: "${SERVER_DRY_RUN:=false}" "${DRY_RUN_PULL_SECRET:=release-pull-dry-run}"
: "${RELEASE_REGION:=}" "${PROMOTED_FROM:=}" "${PROMOTED_TO:=}"
mkdir -p "${STATE}"
# A delivery Job starts with what the bootstrap's stage recorded (STATE_SEED).
if [ -n "${STATE_SEED}" ] && [ -d "${STATE_SEED}" ]; then
  for _f in "${STATE_SEED}"/*; do [ -f "${_f}" ] && cp "${_f}" "${STATE}/"; done
fi
MECH="${STATE}/mechanism"

now() { date -u +%Y-%m-%dT%H:%M:%SZ; }
say() { echo "[release] $*"; }

# helm_local <args...>: helm with its home in STATE (the containers' root
# filesystem is read-only, and so may HOME be).
helm_local() {
  mkdir -p "${STATE}/helm-home"
  HOME="${STATE}/helm-home" HELM_CACHE_HOME="${STATE}/helm-home/cache" HELM_CONFIG_HOME="${STATE}/helm-home/config" HELM_DATA_HOME="${STATE}/helm-home/data" \
    helm "$@"
}

# k8s_json <file>: the YAML documents of <file> as JSON, one per line, read the
# way Kubernetes reads them. kubectl, Helm and the catalyst-api read YAML with
# sigs.k8s.io/yaml, which follows YAML 1.1: 0755 is the octal number 493, and
# yes, no, on and off are booleans. yq follows YAML 1.2, where 0755 is the
# decimal 755 and yes is a string. The sixth delivery to a Sovereign was refused
# for that: step 03 writes a volume's defaultMode 0755, the delivery read its
# pod spec with yq and sent 755, above 0777 (#7938). So every YAML this script
# turns into an object is read here, by Helm's fromYaml (sigs.k8s.io/yaml), the
# same reader that installs the chart; no template can then mean one thing to
# Kubernetes and another to the delivery. What it writes is JSON, which every
# reader reads alike. A document that does not parse fails, _k8s_why names it;
# an empty one is skipped.
_k8s_why=""
k8s_json() {
  _kj="${STATE}/k8s-json"
  mkdir -p "${_kj}/templates"
  printf 'apiVersion: v2\nname: k8s-json\nversion: 0.0.0\n' >"${_kj}/Chart.yaml"
  # shellcheck disable=SC2016 # a Helm template, not shell
  printf '%s\n' '{{- range regexSplit "(?m)^---[ \\t]*$" .Values.doc -1 }}{{- $d := fromYaml . }}{{- if hasKey $d "Error" }}{{ fail (printf "a YAML document does not parse: %s" (get $d "Error")) }}{{- end }}{{- if $d }}{{ toJson $d }}{{ "\n" }}{{- end }}{{- end }}' \
    >"${_kj}/templates/doc.yaml"
  if ! helm_local template k8s-json "${_kj}" --set-file "doc=$1" >"${_kj}/out" 2>"${_kj}/err"; then
    _k8s_why="$(basename "$1"): $(grep -v '^[[:space:]]*$' "${_kj}/err" | head -n 1 | cut -c1-400)"
    return 1
  fi
  sed -n '/^{/p' "${_kj}/out"
}

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

# selected <region>: whether promote acts on <region>: every region when the
# request names none, else the one it names (RELEASE_REGION).
selected() { [ -z "${RELEASE_REGION}" ] || [ "${RELEASE_REGION}" = "$1" ]; }

# region_check: the region a request names is primary or a follower of this
# Sovereign; otherwise _region_why says so and it fails.
_region_why=""
region_check() {
  if [ -z "${RELEASE_REGION}" ] || [ "${RELEASE_REGION}" = primary ] || [ -e "${SECONDARY_KUBECONFIGS}/${RELEASE_REGION}.yaml" ]; then
    return 0
  fi
  _region_why="no region ${RELEASE_REGION}: a region is primary or a follower of this Sovereign (followers:$(for _kc in $(followers); do printf ' %s' "$(basename "${_kc}" .yaml)"; done))"
  return 1
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

# secret_copy <namespace> <name>: the Secret on stdin as a copy named
# <namespace>/<name>: only type and data travel; the copy carries the release's
# labels.
secret_copy() {
  jq -c --arg c "${CANDIDATE}" --arg ns "$1" --arg n "$2" '{apiVersion: "v1", kind: "Secret", type: .type, data: .data,
    metadata: {name: $n, namespace: $ns, labels: {"app.kubernetes.io/component": "sovereign-release", "catalyst.openova.io/release-candidate": $c}}}'
}

# credential_deliver: copy PULL_SECRET into RELEASE_NAMESPACE of every follower,
# where step 03's follower leg reads it. On a failure _cred_why names it.
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
        | secret_copy "${RELEASE_NAMESPACE}" "${PULL_SECRET}" \
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
  rm -rf "${MECH}"; mkdir -p "${MECH}/cm"
  helm_local template "${_rel}" "${_ch}" --namespace "${_tns}" -f "${STATE}/cutover-values.json" >"${MECH}/render.yaml" 2>"${MECH}/render.err" \
    || { _mech_why="helm template of ${_cn} ${_cv} failed: $(tail -n 1 "${MECH}/render.err")"; return 1; }
  k8s_json "${MECH}/render.yaml" >"${MECH}/render.jsonl" \
    || { _mech_why="the render of ${_cn} ${_cv} does not parse: ${_k8s_why}"; return 1; }
  jq -c 'select(.kind == "ConfigMap")' "${MECH}/render.jsonl" >"${MECH}/cms.jsonl"
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

# mech_podspec <ConfigMap>: the podSpec of a rendered step ConfigMap, as JSON,
# read as Kubernetes reads it (k8s_json).
mech_podspec() {
  jq -r '.data.podSpec // empty' "${MECH}/cm/$1.json" >"${MECH}/$1.podspec.yaml" || return 1
  [ -s "${MECH}/$1.podspec.yaml" ] || return 1
  k8s_json "${MECH}/$1.podspec.yaml" | head -n 1
}

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
    --arg ns "${RELEASE_NAMESPACE}" --arg cand "${CANDIDATE}" --arg pull "${_pull:-${PULL_SECRET}}" --arg sfx "${MECHANISM_SUFFIX}" \
    --arg st "${RELEASE_STATUS_CM}" --arg kcs "${SECONDARY_KUBECONFIGS_SECRET}" --arg fns "${SECONDARY_GITEA_NAMESPACE}" --arg region "${RELEASE_REGION}" '
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
          plain("SECONDARY_KUBECONFIGS"; "/secondary-kubeconfigs"), plain("SECONDARY_GITEA_NAMESPACE"; $fns)]
          + (if $region == "" then [] else [plain("RELEASE_REGION"; $region)] end)))
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
# its stage), JSON on stdout (read by k8s_json; JSON, so that no YAML reader
# reads a value of it differently); nothing when the render has none.
follower_job_of_mechanism() {
  jq -r '.data["secondary-pivot-job.yaml"] // empty' "${MECH}/cm/cutover-secondary-pivot-script.json" >"${MECH}/follower-src.yaml" 2>/dev/null || true
  [ -s "${MECH}/follower-src.yaml" ] || return 0
  _urepo=$(jq -r '([.containers[0].env[]? | select(.name == "UPSTREAM_REPO_URL") | .value] | .[0]) // ""' "${MECH}/p01.json")
  k8s_json "${MECH}/follower-src.yaml" >"${MECH}/follower-src.json" || return 1
  head -n 1 "${MECH}/follower-src.json" | jq --arg cand "${CANDIDATE}" --arg urepo "${_urepo}" '
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
    | .spec.template.spec.volumes = [{name: "release", configMap: {name: "sovereign-release-scripts", defaultMode: 493}}]'
}

# follower_scripts_cm <release-region.sh> <kit-rewrite.sh> <extract-pins.sh>:
# the ConfigMap of the scripts a follower's release Job runs, JSON.
follower_scripts_cm() {
  jq -n --rawfile a "$1" --rawfile b "$2" --rawfile c "$3" --arg ns "${SECONDARY_GITEA_NAMESPACE}" \
    '{apiVersion: "v1", kind: "ConfigMap", metadata: {name: "sovereign-release-scripts", namespace: $ns},
      data: {"release-region.sh": $a, "kit-rewrite.sh": $b, "extract-pins.sh": $c}}'
}

# follower_job_name <region> <phase>; follower_job <manifest> <region> <phase>:
# the follower Job manifest with the names and the recorded commits of a phase.
follower_job_name() { printf 'sovereign-release-%s-%s' "$2" "$1" | cut -c1-63 | sed 's/-*$//'; }
follower_job() {
  sed -e "s|__JOB__|$(follower_job_name "$2" "$3")|g" -e "s|__REGION__|$2|g" -e "s|__PHASE__|$3|g" \
    -e "s|__MAIN_SHA__|$(state_get "$2" main)|g" -e "s|__STAGED_SHA__|$(state_get "$2" staged)|g" "$1"
}

# ── The objects of a delivery, and the API server's dry run of them ──
# A plan lists objects a delivery creates, one per line of a .tsv file: <region>
# <how> <file> <what>. <region> is "primary" or a follower's kubeconfig, whose
# account creates it there; <how> is how the step that makes it sends it
# (submit). plan_delivery is what compose creates in the primary region, as the
# account of the bootstrap Job; plan_later what the later steps create in each
# follower: the stage's copy of the pull credential, what the candidate's step
# 03 creates there (its #6764 leg, and cutover step 04 there, #7938), and
# promote's scripts and Jobs.

_obj() { mkdir -p "${MECH}/objects"; printf '%s/objects/%s.json' "${MECH}" "$1"; }
plan_add() { printf '%s\t%s\t%s\t%s\n' "$2" "$3" "$4" "$5" >>"$1"; }

# plan_delivery: the delivery Job (composed with the request's pull Secret, or
# DRY_RUN_PULL_SECRET in a dry run, which names none), the ConfigMaps it mounts,
# under <name>${MECHANISM_SUFFIX}, and its record of the staged commits.
plan_delivery() {
  _pd="${MECH}/plan-deliver.tsv"; : >"${_pd}"
  _pull="${PULL_SECRET}"
  [ -n "${_pull}" ] || [ "${DRY_RUN}" != true ] || _pull="${DRY_RUN_PULL_SECRET}"
  compose_delivery >"${MECH}/delivery-job.json" 2>"${MECH}/compose.err" || { _mech_why="cannot compose the delivery Job: ${_mech_why:-$(tail -n 1 "${MECH}/compose.err")}"; return 1; }
  for _c in $(jq -r --arg s "${MECHANISM_SUFFIX}" '.spec.template.spec.volumes[].configMap.name // empty | select(endswith($s)) | rtrimstr($s)' "${MECH}/delivery-job.json" | sort -u); do
    [ "${_c}" != sovereign-release-delivery ] || continue
    [ -s "${MECH}/cm/${_c}.json" ] || { _mech_why="the delivery mounts ConfigMap ${_c}, which the render lacks"; return 1; }
    _f=$(_obj "cm-${_c}")
    jq --arg n "${_c}${MECHANISM_SUFFIX}" --arg ns "${RELEASE_NAMESPACE}" --arg cand "${CANDIDATE}" \
      '{apiVersion: "v1", kind: "ConfigMap", metadata: {name: $n, namespace: $ns, labels: {"app.kubernetes.io/component": "sovereign-release", "catalyst.openova.io/release-candidate": $cand}}, data: .data}' \
      "${MECH}/cm/${_c}.json" >"${_f}"
    plan_add "${_pd}" primary ssa "${_f}" "ConfigMap ${RELEASE_NAMESPACE}/${_c}${MECHANISM_SUFFIX}"
  done
  follower_job_of_mechanism >"${MECH}/follower-job.yaml" || { _mech_why="cannot turn the rendered follower pivot Job into the release's: ${_k8s_why}"; return 1; }
  if [ -n "$(followers)" ] && [ ! -s "${MECH}/follower-job.yaml" ]; then _mech_why="this Sovereign has follower regions, and the render has no follower pivot Job (cutover-secondary-pivot-script)"; return 1; fi
  _f=$(_obj sovereign-release-delivery)
  jq -n --rawfile a "${MECH}/release-run.sh" --rawfile b "${MECH}/release-region.sh" --rawfile c "${MECH}/follower-job.yaml" \
    --arg n "sovereign-release-delivery${MECHANISM_SUFFIX}" --arg ns "${RELEASE_NAMESPACE}" \
    '{apiVersion: "v1", kind: "ConfigMap", metadata: {name: $n, namespace: $ns, labels: {"app.kubernetes.io/component": "sovereign-release"}},
      data: {"release-run.sh": $a, "release-region.sh": $b, "follower-job.yaml": $c}}' >"${_f}"
  plan_add "${_pd}" primary ssa "${_f}" "ConfigMap ${RELEASE_NAMESPACE}/sovereign-release-delivery${MECHANISM_SUFFIX} (the delivery's scripts)"
  _rec='{}'
  for _r in "${STATE}"/*.main "${STATE}"/*.staged "${STATE}"/*.candidate; do
    [ -f "${_r}" ] || continue
    _rec=$(printf '%s' "${_rec}" | jq -c --arg k "$(basename "${_r}")" --rawfile v "${_r}" '. + {($k): $v}')
  done
  _f=$(_obj sovereign-release-recorded)
  printf '%s' "${_rec}" | jq --arg ns "${RELEASE_NAMESPACE}" \
    '{apiVersion: "v1", kind: "ConfigMap", metadata: {name: "sovereign-release-recorded", namespace: $ns, labels: {"app.kubernetes.io/component": "sovereign-release"}}, data: .}' >"${_f}"
  plan_add "${_pd}" primary ssa "${_f}" "ConfigMap ${RELEASE_NAMESPACE}/sovereign-release-recorded (the staged commits)"
  # The Job is created, not applied: its name is new for every request, and the
  # runner may create Jobs but not patch them (a server-side apply is a PATCH).
  plan_add "${_pd}" primary create "${MECH}/delivery-job.json" "Job ${RELEASE_NAMESPACE}/${RELEASE_JOB}-delivery"
}

# plan_later: what the steps after compose create in each follower region.
plan_later() {
  _pl="${MECH}/plan-later.tsv"; : >"${_pl}"
  [ -n "$(followers)" ] || return 0
  _envof() { jq -r --arg n "$1" '([.containers[0].env[]? | select(.name == $n) | .value] | .[0]) // ""' "${MECH}/p03.json"; }
  _hns=$(_envof SECONDARY_HARBOR_NAMESPACE); _hsec=$(_envof SECONDARY_GHCR_DEST_SECRET)
  _hcm="${MECH}/cm/cutover-secondary-harbor-prewarm-script.json"
  _pull="${PULL_SECRET}"
  [ -n "${_pull}" ] || _pull="${DRY_RUN_PULL_SECRET}"
  # The copies carry the pull Secret's type and data; a dry run has none, and
  # an empty docker configuration stands for it.
  _dcfg=$(printf '{"auths":{}}' | base64 | tr -d '\n')
  _src=$(jq -cn --arg d "${_dcfg}" '{type: "kubernetes.io/dockerconfigjson", data: {".dockerconfigjson": $d}}')
  jq -r '.data["kit-rewrite.sh"] // ""' "${MECH}/cm/cutover-step-06-helmrepository-patches.json" >"${MECH}/kit-rewrite.sh"
  jq -r '.data["extract-pins.sh"] // ""' "${MECH}/cm/cutover-bootstrap-kit-pins.json" >"${MECH}/extract-pins.sh"
  for _kc in $(followers); do
    _r=$(basename "${_kc}" .yaml)
    _f=$(_obj "${_r}-pull-secret")
    printf '%s' "${_src}" | secret_copy "${RELEASE_NAMESPACE}" "${_pull}" >"${_f}"
    plan_add "${_pl}" "${_kc}" ssa "${_f}" "Secret ${RELEASE_NAMESPACE}/${_pull} in ${_r} (the stage's copy of the pull credential)"
    if [ -n "${_hns}" ] && [ -n "${_hsec}" ] && [ -s "${_hcm}" ]; then
      _f=$(_obj "${_r}-prewarm-secret")
      printf '%s' "${_src}" | secret_copy "${_hns}" "${_hsec}" >"${_f}"
      plan_add "${_pl}" "${_kc}" ssa "${_f}" "Secret ${_hns}/${_hsec} in ${_r} (step 03's copy of the pull credential)"
      _f=$(_obj "${_r}-prewarm-cm")
      jq --arg ns "${_hns}" '{apiVersion: "v1", kind: "ConfigMap", metadata: {name: "cutover-secondary-harbor-prewarm", namespace: $ns},
          data: {"secondary-harbor-prewarm.sh": (.data["secondary-harbor-prewarm.sh"] // ""), "images.list": "", "charts.list": ""}}' "${_hcm}" >"${_f}"
      plan_add "${_pl}" "${_kc}" ssa "${_f}" "ConfigMap ${_hns}/cutover-secondary-harbor-prewarm in ${_r} (step 03)"
      jq -r '.data["secondary-harbor-prewarm-job.yaml"] // ""' "${_hcm}" | sed "s/__REGION__/${_r}/g; s/__REGISTRY_PIN_IP__//g" >"${MECH}/objects/${_r}-prewarm-job.yaml"
      _f=$(_obj "${_r}-prewarm-job")
      k8s_json "${MECH}/objects/${_r}-prewarm-job.yaml" | head -n 1 >"${_f}" || { _mech_why="step 03's secondary-harbor-prewarm-job.yaml: ${_k8s_why}"; return 1; }
      plan_add "${_pl}" "${_kc}" ssa-recreate "${_f}" "Job $(jq -r '.metadata.namespace + "/" + .metadata.name' "${_f}") in ${_r} (step 03)"
    fi
    # The candidate's step 03 ends with cutover step 04 in this follower
    # (files/follower-registry-pivot.sh): the primary's pivot.sh, then the
    # objects of the render's cutover-follower-registry-pivot (#7938).
    if [ -s "${MECH}/cm/cutover-follower-registry-pivot.json" ]; then
      _f=$(_obj "${_r}-pivot-script")
      jq --arg ns "${RELEASE_NAMESPACE}" '{apiVersion: "v1", kind: "ConfigMap", metadata: {name: "registry-pivot-script", namespace: $ns}, data: {"pivot.sh": .data["pivot.sh"]}}' \
        "${MECH}/cm/registry-pivot-script.json" >"${_f}" 2>/dev/null || { _mech_why="the render has no registry-pivot-script for step 04 in ${_r}"; return 1; }
      plan_add "${_pl}" "${_kc}" ssa "${_f}" "ConfigMap ${RELEASE_NAMESPACE}/registry-pivot-script in ${_r} (step 04, run by step 03)"
      jq -r '.data["objects.yaml"] // ""' "${MECH}/cm/cutover-follower-registry-pivot.json" >"${MECH}/objects/${_r}-pivot.yaml"
      k8s_json "${MECH}/objects/${_r}-pivot.yaml" >"${MECH}/objects/${_r}-pivot.jsonl" || { _mech_why="step 04's objects.yaml: ${_k8s_why}"; return 1; }
      _i=0
      while IFS= read -r _l; do
        _i=$((_i + 1)); _f=$(_obj "${_r}-pivot-${_i}"); printf '%s\n' "${_l}" >"${_f}"
        plan_add "${_pl}" "${_kc}" ssa "${_f}" "$(jq -r '.kind + " " + (.metadata.namespace // "" | if . == "" then "" else . + "/" end) + .metadata.name' "${_f}") in ${_r} (step 04, run by step 03)"
      done <"${MECH}/objects/${_r}-pivot.jsonl"
    fi
    _f=$(_obj "${_r}-scripts")
    follower_scripts_cm "${MECH}/release-region.sh" "${MECH}/kit-rewrite.sh" "${MECH}/extract-pins.sh" >"${_f}"
    plan_add "${_pl}" "${_kc}" ssa "${_f}" "ConfigMap ${SECONDARY_GITEA_NAMESPACE}/sovereign-release-scripts in ${_r} (promote)"
    for _ph in check promote; do
      _f=$(_obj "${_r}-${_ph}")
      follower_job "${MECH}/follower-job.yaml" "${_r}" "${_ph}" >"${_f}"
      plan_add "${_pl}" "${_kc}" ssa-recreate "${_f}" "Job ${SECONDARY_GITEA_NAMESPACE}/$(follower_job_name "${_r}" "${_ph}") in ${_r} (promote, ${_ph})"
    done
  done
}

# submit <region> <how> <file>: send one object to the API server of <region>:
# ssa (server-side apply as the release), create, or ssa-recreate (a Job the
# step deletes by name first, then applies). Step 03 sends its follower
# ConfigMap and Job by a client-side apply; the dry run sends them by a
# server-side one, which validates the same object, as that step's account
# (the peer, cluster-admin), and this script makes no client-side apply (G13).
# With SERVER_DRY_RUN=true it is a server-side dry run: authorization,
# admission and validation run and nothing is stored. A Job the step recreates
# is then created under a name generated from its own: a dry run cannot delete
# the one an earlier delivery left, and an update of it would be refused as
# immutable. The answer is STATE/submit.out.
submit() {
  _sr="$1"; _sh="$2"; _sf="$3"
  set --
  [ "${_sr}" = primary ] || set -- --kubeconfig "${_sr}"
  if [ "${SERVER_DRY_RUN}" = true ]; then
    set -- "$@" --dry-run=server
    case "${_sh}" in
      *-recreate)
        k8s_json "${_sf}" | head -n 1 | jq -c '.metadata.generateName = (.metadata.name + "-") | del(.metadata.name)' >"${_sf}.dry"
        _sf="${_sf}.dry"; _sh=create ;;
    esac
  fi
  case "${_sh}" in
    ssa|ssa-recreate) kubectl "$@" apply --server-side --force-conflicts --field-manager=sovereign-release -f - <"${_sf}" ;;
    create) kubectl "$@" create -f - <"${_sf}" ;;
    *) echo "submit: no way ${_sh}" ;;
  esac >"${STATE}/submit.out" 2>&1
}

# whoami_in <region>: the user the API server of <region> takes this Pod's
# calls for (a SelfSubjectReview, which every authenticated user may create).
whoami_in() {
  _wr="$1"; set --
  [ "${_wr}" = primary ] || set -- --kubeconfig "${_wr}"
  kubectl "$@" auth whoami -o 'jsonpath={.status.userInfo.username}' 2>/dev/null || true
}

# plan_submit <plan...>: send every object of the plans, in order. In a dry run
# every object is sent and each refusal is a line of MECH/refused; otherwise
# the first refusal stops it (_mech_why names the object and the answer).
plan_submit() {
  _ok=0; _no=0; : >"${MECH}/refused"
  for _p in "$@"; do
    [ -s "${_p}" ] || continue
    while IFS="$(printf '\t')" read -r _pr _ph _pf _pw; do
      [ -n "${_pr}" ] || continue
      if submit "${_pr}" "${_ph}" "${_pf}" </dev/null; then _ok=$((_ok + 1)); continue; fi
      _no=$((_no + 1))
      _ans=$(grep -v '^[[:space:]]*$' "${STATE}/submit.out" | tr '\n' ' ' | sed 's/  */ /g' | cut -c1-900)
      printf '%s: %s\n' "${_pw}" "${_ans}" >>"${MECH}/refused"
      [ "${SERVER_DRY_RUN}" = true ] || { _mech_why="${_pw}: ${_ans}"; return 1; }
    done <"${_p}"
  done
  _plan_ok="${_ok}"; _plan_no="${_no}"
  [ "${_no}" -eq 0 ]
}

# server_dry_run: every object of plan_delivery and plan_later, sent to the API
# server as a server-side dry run by the account that will create it: this
# Pod's in the primary region (the bootstrap's, which deliver creates the
# delivery's objects with) and each follower's peer kubeconfig. MECH/
# server-dry-run and the state key serverDryRun say what it answered. 0: every
# object accepted; 1: refusals, each named; 2: the delivery cannot be composed
# (_mech_why).
server_dry_run() {
  plan_delivery || return 2
  plan_later || return 2
  _who="primary as $(whoami_in primary)"
  for _kc in $(followers); do _who="${_who}, $(basename "${_kc}" .yaml) as $(whoami_in "${_kc}")"; done
  SERVER_DRY_RUN=true
  _sdrc=0; plan_submit "${MECH}/plan-deliver.tsv" "${MECH}/plan-later.tsv" || _sdrc=1
  SERVER_DRY_RUN=false
  _n=$((_plan_ok + _plan_no))
  if [ "${_sdrc}" -eq 0 ]; then
    printf 'passed: the API server accepted all %s objects the delivery creates, each as a server-side dry run by the account that creates it (%s); nothing was stored' "${_n}" "${_who}" >"${MECH}/server-dry-run"
  else
    printf 'refused: the API server refuses %s of the %s objects the delivery creates (server-side dry run; %s): %s' "${_plan_no}" "${_n}" "${_who}" "$(tr '\n' ';' <"${MECH}/refused" | sed 's/;$//; s/;/; /g')" >"${MECH}/server-dry-run"
  fi
  status_put --arg serverDryRun "$(cut -c1-6000 "${MECH}/server-dry-run")" --arg updatedAt "$(now)"
  say "server dry run: $(cat "${MECH}/server-dry-run")"
  sed 's/^/[release]   REFUSED /' "${MECH}/refused"
  return "${_sdrc}"
}

# pod_stall <job>: why the Pod of a Job has started no container, or "": a Pod
# on a node that cannot start it (no sandbox image, say) waits until the Job's
# deadline, and only its waiting reason names the cause.
pod_stall() {
  kubectl -n "${RELEASE_NAMESPACE}" get pods -l "job-name=$1" -o json 2>/dev/null | jq -r '
    def ctrs: [(.status.initContainerStatuses // [])[], (.status.containerStatuses // [])[]];
    def why: ([ctrs[] | .state.waiting // empty | (.reason // "") + " " + (.message // "")]
      + [(.status.conditions // [])[] | select(.status != "True") | (.reason // .type) + " " + (.message // "")]) | first // "no container started";
    [.items[] | select(ctrs | any(.state.running or .state.terminated) | not)
      | "Pod \(.metadata.name) on node \(.spec.nodeName // "none") (\(.status.phase // "unknown")): \(why)"] | first // ""' 2>/dev/null || true
}

# run_delivery: the server's dry run of every object the delivery creates, in
# every region; then, only if it accepted all, what compose creates, in order
# (the ConfigMaps the Job mounts, then the Job); then wait for the Job.
run_delivery() {
  _dj="${RELEASE_JOB}-delivery"
  _sdr=0; server_dry_run || _sdr=$?
  case "${_sdr}" in
    0) ;;
    2) return 1 ;;
    *) _mech_why="the API server refuses what the delivery creates, so nothing was created: $(sed 's/^refused: //' "${MECH}/server-dry-run")"; return 1 ;;
  esac
  plan_submit "${MECH}/plan-deliver.tsv" || { _mech_why="cannot create ${_mech_why}"; return 1; }
  status_put --arg deliveryJob "${_dj}" --arg updatedAt "$(now)"
  say "the delivery runs as Job ${RELEASE_NAMESPACE}/${_dj}: prewarm and promote of $(cat "${MECH}/version")"
  _start=$(date +%s)
  _deadline=$(( _start + $(jq '.spec.activeDeadlineSeconds' "${MECH}/delivery-job.json") + 600 ))
  _end=""; _stall=""
  while [ "$(date +%s)" -lt "${_deadline}" ]; do
    # A Pod that has started no container after DELIVERY_STALL_SECONDS is
    # named in the state (deliveryPod) while the wait goes on.
    if [ "$(( $(date +%s) - _start ))" -ge "${DELIVERY_STALL_SECONDS:-600}" ]; then
      _s=$(pod_stall "${_dj}")
      if [ "${_s}" != "${_stall}" ]; then
        _stall="${_s}"
        status_put --arg deliveryPod "${_s:-started}" --arg updatedAt "$(now)"
        [ -z "${_s}" ] || say "WARN: the delivery Job has started no container: ${_s}"
      fi
    fi
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
    *) _mech_why="the delivery Job ${_dj} did not end within its deadline${_stall:+: ${_stall}}"; return 1 ;;
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
  _job=$(follower_job_name "${_region}" "${_ph}")
  _out="${STATE}/${_region}.${_ph}.out"
  follower_scripts_cm "${RELEASE_SCRIPTS}/release-region.sh" "${KIT_REWRITE}" "${PIN_EXTRACTOR}" >"${STATE}/${_region}.${_ph}.scripts.json"
  follower_job "${RELEASE_SCRIPTS}/follower-job.yaml" "${_region}" "${_ph}" >"${STATE}/${_region}.${_ph}.job"
  # What the API server refuses ends the phase at once, with its answer.
  _sub="${SECONDARY_GITEA_NAMESPACE}/sovereign-release-scripts"
  if submit "${_kc}" ssa "${STATE}/${_region}.${_ph}.scripts.json"; then
    kubectl --kubeconfig "${_kc}" -n "${SECONDARY_GITEA_NAMESPACE}" delete job "${_job}" --ignore-not-found=true --wait=true >/dev/null
    _sub="Job ${SECONDARY_GITEA_NAMESPACE}/${_job}"
    submit "${_kc}" ssa-recreate "${STATE}/${_region}.${_ph}.job" && _sub=""
  fi
  if [ -n "${_sub}" ]; then
    echo "release-report region=${_region} reason the API server refused ${_sub}: $(grep -v '^[[:space:]]*$' "${STATE}/submit.out" | tr '\n' ' ' | cut -c1-600)" >"${_out}"
    cat "${_out}"
    record "${_region}" "${_ph}" "${_out}"
    return 1
  fi
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
  if [ -z "${_end}" ]; then
    # Nothing waits for it any more, so it must not run on: a promote that
    # starts after the wait gave up would move this region unobserved.
    kubectl --kubeconfig "${_kc}" -n "${SECONDARY_GITEA_NAMESPACE}" delete job "${_job}" --ignore-not-found=true --wait=false >/dev/null 2>&1 || true
    echo "release-report region=${_region} reason the Job did not end within ${FOLLOWER_WAIT_SECONDS}s and was deleted" >>"${_out}"
  fi
  cat "${_out}"
  record "${_region}" "${_ph}" "${_out}"
  [ "${_end}" = ok ]
}

# promote_region <region> <command...>: "<command...> promote" for one region;
# when it succeeds the state keeps what moved, for a rollback (SOV-521):
# <region>.promotedFrom, the main recorded at stage, <region>.promotedTo, the
# commit pushed, and <region>.phase. A region whose main already held the
# candidate at stage (staged = main, up-to-date) moved nothing: the record of
# the request that promoted it, or of its rollback, stays as it is.
promote_region() {
  _pr="$1"; shift
  _from=$(state_get "${_pr}" main); _to=$(state_get "${_pr}" staged)
  "$@" promote || return 1
  [ "${_from}" != "${_to}" ] || return 0
  status_put --arg "${_pr}.promotedFrom" "${_from}" --arg "${_pr}.promotedTo" "${_to}" --arg "${_pr}.phase" promoted --arg updatedAt "$(now)"
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
    if ! region_check; then
      status_put --arg phase refused --arg failedStep stage --arg message "${_region_why}; nothing moved in any region" --arg updatedAt "$(now)"
      say "REFUSED: ${_region_why}"
      [ "${DRY_RUN}" = "true" ] || credential_remove || true
      exit 1
    fi
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
    # A request for one region is promoted by the candidate's mechanism, which
    # promotes one region only if its release-run.sh has the phase rollback
    # (SOV-521); an earlier one would promote every region.
    if [ -n "${RELEASE_REGION}" ] && ! grep -q '^  rollback)' "${MECH}/release-run.sh"; then
      _why="the release-run.sh of rc/${CANDIDATE} has no phase rollback, so it promotes every region and cannot promote region ${RELEASE_REGION} alone: deliver rc/${CANDIDATE} with no region"
      status_put --arg phase refused --arg failedStep stage --arg message "${_why}; nothing moved in any region" --arg updatedAt "$(now)"
      say "REFUSED: ${_why}; nothing moved in any region"
      [ "${DRY_RUN}" = "true" ] || credential_remove || true
      exit 1
    fi
    # Whether a follower's registry gets what the release pins depends on the
    # step 03 that runs: the candidate's.
    if jq -r '.data["run.sh"] // ""' "${MECH}/cm/cutover-step-03-harbor-prewarm.json" | grep -q "${FOLLOWER_PREWARM_MARKER:-also mirrors the pins of}"; then
      [ -z "$(followers)" ] || status_put --arg followerPrewarm "covered: the candidate's step 03 mirrors the release's pins into every follower registry" --arg updatedAt "$(now)"
    else
      FOLLOWER_PREWARM_COVERED=false follower_storage
    fi
    if [ "${DRY_RUN}" = "true" ]; then
      # The candidate's own verify sends every object the delivery would
      # create, in every region, to the API server as a server-side dry run.
      # A candidate whose release-run.sh predates it is said to, not passed.
      _dm="; the objects of the delivery were not sent to the API server: the release-run.sh of rc/${CANDIDATE} has no verify"
      if grep -q '^  verify)' "${MECH}/release-run.sh"; then
        _vrc=0; sh "${MECH}/release-run.sh" verify || _vrc=$?
        _sd=$(cat "${MECH}/server-dry-run" 2>/dev/null || echo "the candidate's verify exited ${_vrc}")
        if [ "${_vrc}" -ne 0 ]; then
          status_put --arg phase failed --arg failedStep stage \
            --arg message "dry run failed: every region merged rc/${CANDIDATE} cleanly, but the delivery would not run: ${_sd}; nothing was pushed" --arg updatedAt "$(now)"
          say "dry run FAILED: ${_sd}"
          exit 1
        fi
        _dm="; ${_sd}"
      else
        status_put --arg serverDryRun "not run: the release-run.sh of rc/${CANDIDATE} has no verify" --arg updatedAt "$(now)"
      fi
      status_put --arg phase dry-run --arg message "dry run: every region merged rc/${CANDIDATE} cleanly in a scratch copy${_dm}; nothing was pushed" --arg updatedAt "$(now)"
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
  verify)
    # The candidate's: compose every object the delivery creates and send each
    # to the API server as a server-side dry run (server_dry_run). Nothing is
    # created; MECH/server-dry-run is the verdict, for the stage to record.
    [ -s "${MECH}/version" ] || { say "FAILED: no rendered release mechanism in ${MECH}"; exit 1; }
    _vrc=0; server_dry_run || _vrc=$?
    if [ "${_vrc}" -eq 2 ]; then
      printf 'not run: the delivery cannot be composed: %s' "${_mech_why}" >"${MECH}/server-dry-run"
      status_put --arg serverDryRun "$(cat "${MECH}/server-dry-run")" --arg updatedAt "$(now)"
      say "FAILED: $(cat "${MECH}/server-dry-run")"
    fi
    [ "${_vrc}" -eq 0 ] || exit 1
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
    # A warning of step 03 (say, an installed ClusterRole that does not let it
    # read the nodes' sandbox image yet) is in the state, not only in the log.
    _wn=$(grep -c '\] WARNING' "${STATE}/prewarm.log" 2>/dev/null || true)
    if [ "${_wn:-0}" -gt 0 ] 2>/dev/null; then
      status_put --arg prewarmWarnings "${_wn} warning(s) of step 03: $(grep '\] WARNING' "${STATE}/prewarm.log" | sed 's/^\[[^]]*\] *WARNING:* *//' | head -n 3 | tr '\n' ';' | cut -c1-1200)" --arg updatedAt "$(now)"
    fi
    ;;
  promote)
    status_put --arg phase promoting --arg updatedAt "$(now)"
    # A deletion the prewarm could not finish is tried again; nothing is left
    # to delete on the usual path.
    credential_remove || true
    if ! region_check; then
      status_put --arg phase refused --arg message "${_region_why}; no region was promoted" --arg updatedAt "$(now)"
      say "REFUSED: ${_region_why}"
      exit 1
    fi
    # Only the regions promote acts on are checked: a request for one region
    # moves that region alone (selected).
    _rc=0
    if selected primary; then run_primary check || _rc=1; fi
    for kc in $(followers); do
      if selected "$(basename "${kc}" .yaml)"; then run_follower "${kc}" check || _rc=1; fi
    done
    if [ "${_rc}" -ne 0 ]; then
      _v=$(verdict check); _v="${_v:-failed}"
      case "${_v}" in
        moved|conflict) _m="a region changed after staging: no region was promoted; request the release again" ;;
        *) _m="the check did not run to its end in every region (<region>.check.report names why): no region was promoted" ;;
      esac
      status_put --arg phase "${_v}" --arg message "${_m}" --arg updatedAt "$(now)"
      say "check ${_v}: no region was promoted"
      exit 1
    fi
    for kc in $(followers); do
      _r=$(basename "${kc}" .yaml)
      if selected "${_r}"; then promote_region "${_r}" run_follower "${kc}" || _rc=1; fi
    done
    if [ "${_rc}" -eq 0 ] && selected primary; then promote_region primary run_primary || _rc=1; fi
    if [ "${_rc}" -ne 0 ]; then
      if [ -n "${RELEASE_REGION}" ]; then
        _v=$(verdict promote); _v="${_v:-failed}"
        status_put --arg phase "${_v}" --arg message "the promote of region ${RELEASE_REGION} ended ${_v} after it checked clean (${RELEASE_REGION}.promote names why); no other region was promoted" --arg updatedAt "$(now)"
      else
        status_put --arg phase partial --arg message "a promote failed after every region checked clean; <region>.promote names which regions moved" --arg updatedAt "$(now)"
      fi
      exit 1
    fi
    if [ -n "${RELEASE_REGION}" ]; then
      status_put --arg phase delivered --arg message "region ${RELEASE_REGION} follows rc/${CANDIDATE} merged with its own commits; no other region was promoted" --arg deliveredAt "$(now)" --arg updatedAt "$(now)"
      say "delivered rc/${CANDIDATE} to region ${RELEASE_REGION}"
    else
      status_put --arg phase delivered --arg message "every region follows rc/${CANDIDATE} merged with its own commits" --arg deliveredAt "$(now)" --arg updatedAt "$(now)"
      say "delivered rc/${CANDIDATE}"
    fi
    ;;
  rollback)
    # The bootstrap's only container of a rollback request (SOV-521). The
    # region's recorded commits come from the route, which read them from the
    # state of this candidate's promote.
    if [ -z "${RELEASE_REGION}" ] || [ -z "${PROMOTED_FROM}" ] || [ -z "${PROMOTED_TO}" ]; then
      _why="a rollback names a region (RELEASE_REGION) and the commits its promote recorded (PROMOTED_FROM, PROMOTED_TO)"
      status_put --arg phase refused --arg failedStep rollback --arg message "${_why}; nothing was pushed" --arg updatedAt "$(now)"
      say "REFUSED: ${_why}"
      exit 1
    fi
    if ! region_check; then
      status_put --arg phase refused --arg failedStep rollback --arg message "${_region_why}; nothing was pushed" --arg updatedAt "$(now)"
      say "REFUSED: ${_region_why}"
      exit 1
    fi
    status_put --arg phase rolling-back --arg updatedAt "$(now)"
    printf '%s' "${PROMOTED_FROM}" >"${STATE}/${RELEASE_REGION}.main"
    printf '%s' "${PROMOTED_TO}" >"${STATE}/${RELEASE_REGION}.staged"
    _rc=0
    if [ "${RELEASE_REGION}" = primary ]; then
      run_primary rollback || _rc=1
    else
      run_follower "${SECONDARY_KUBECONFIGS}/${RELEASE_REGION}.yaml" rollback || _rc=1
    fi
    if [ "${_rc}" -ne 0 ]; then
      _v=$(verdict rollback); _v="${_v:-failed}"
      status_put --arg phase "${_v}" --arg failedStep rollback \
        --arg message "the rollback of region ${RELEASE_REGION} ended ${_v} (${RELEASE_REGION}.rollback.report names why); no other region was touched" --arg updatedAt "$(now)"
      say "rollback ${_v}: region ${RELEASE_REGION}"
      exit 1
    fi
    status_put --arg phase rolled-back --arg "${RELEASE_REGION}.phase" rolled-back \
      --arg message "region ${RELEASE_REGION} runs the tree of ${PROMOTED_FROM} again, the main recorded at the stage of rc/${CANDIDATE}, as a commit on top of ${PROMOTED_TO}, the commit its promote pushed; Flux applies it on its next interval" \
      --arg updatedAt "$(now)"
    say "rolled back region ${RELEASE_REGION} to the tree of ${PROMOTED_FROM}"
    ;;
  *) say "usage: release-run.sh stage|deliver|verify|compose|prewarm <command...>|promote|rollback"; exit 2 ;;
esac
