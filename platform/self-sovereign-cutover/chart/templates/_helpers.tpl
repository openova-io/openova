{{/*
Expand the name of the chart.
*/}}
{{- define "bp-self-sovereign-cutover.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels — applied to every resource emitted by this chart.

Why these label keys are load-bearing
─────────────────────────────────────
The catalyst-api cutover endpoint (issue #792, products/catalyst/bootstrap/api/
internal/handler/cutover.go) discovers step ConfigMaps via:

    app.kubernetes.io/part-of=self-sovereign-cutover
    app.kubernetes.io/component=cutover-step

…and resolves order/mode/daemonsetRef via:

    bp.openova.io/cutover-order        integer 1..N
    bp.openova.io/cutover-mode         "job" | "daemonset-wait"
    bp.openova.io/cutover-daemonset    DaemonSet name (mode=daemonset-wait only)

Those labels are EMITTED PER-STEP-TEMPLATE because each step's order /
mode / daemonsetRef differ. The helpers below provide ONLY the common
ones; per-step templates inline their own.
*/}}
{{- define "bp-self-sovereign-cutover.labels" -}}
app.kubernetes.io/name: {{ include "bp-self-sovereign-cutover.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: self-sovereign-cutover
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
catalyst.openova.io/blueprint: bp-self-sovereign-cutover
catalyst.openova.io/sovereign-fqdn: {{ .Values.sovereign.fqdn | quote }}
{{- end -}}

{{/*
Image reference helper. Routes through .Values.global.imageRegistry IF
set AND the caller passes `cutoverPhase: post`. The first three steps
(01/02/03) and the registry-pivot DaemonSet MUST use upstream-public
images (cutoverPhase: pre) because they run BEFORE registries.yaml v2
takes effect on the node containerd. Post-pivot steps (05/06/07/08)
declare cutoverPhase: post so they pull from local Harbor when the
operator overlay sets global.imageRegistry.

Inputs (dict): .repository .tag .cutoverPhase (pre|post) .Values
*/}}
{{- define "bp-self-sovereign-cutover.image" -}}
{{- $repo := .repository -}}
{{- $tag := .tag -}}
{{- $registry := "" -}}
{{- if and (eq .cutoverPhase "post") .Values.global.imageRegistry -}}
{{- $registry = printf "%s/" .Values.global.imageRegistry -}}
{{- end -}}
{{- printf "%s%s:%s" $registry $repo $tag -}}
{{- end -}}

{{/*
ServiceAccount name — every step Job (stamped by catalyst-api from the
PodSpec ConfigMaps in this chart) AND the registry-pivot DaemonSet use
the same SA so RBAC is single-source.
*/}}
{{- define "bp-self-sovereign-cutover.serviceAccountName" -}}
{{- printf "%s-runner" (include "bp-self-sovereign-cutover.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
── #4975 offline-mirror coverage map (single source of truth) ───────────────
The bare host of the LOCAL Sovereign Harbor (registry.<fqdn>), derived from
sovereign.harborPublicURL. Steps 03/04/08 all resolve the local registry from
THIS one helper so "which host is local" is never hardcoded per-step (the
"harbor." vs "registry." drift that made step-08 exclude the mothership tether
and needlessly roll local refs).
*/}}
{{- define "bp-self-sovereign-cutover.localRegistryHost" -}}
{{- .Values.sovereign.harborPublicURL | trimPrefix "https://" | trimPrefix "http://" | trimSuffix "/" -}}
{{- end -}}

{{/*
The bare host of the MOTHERSHIP Harbor (harbor.openova.io), derived from
upstream.mothershipHarborURL. Post-cutover this host is a tether: its
`harbor.openova.io/proxy-<x>/PATH` refs host-swap to
`registry.<fqdn>/proxy-<x>/PATH` (path preserved).
*/}}
{{- define "bp-self-sovereign-cutover.mothershipHost" -}}
{{- .Values.upstream.mothershipHarborURL | trimPrefix "https://" | trimPrefix "http://" | trimSuffix "/" -}}
{{- end -}}

{{/*
The host→local-Harbor-project coverage map, rendered as a single-line,
env-safe, space-separated list of `host:project` tokens (empty project =
host-swap/path-preserve, used for the mothership host whose path already
carries the proxy-<x> project segment). Consumed VERBATIM by step-03
(skopeo push dest), step-04 (containerd certs.d rewrite target) and step-08
(pre-hold completeness HEAD) so all three derive identical local paths from
one declaration — this is the retirement of the three hand-maintained lists.
*/}}
{{- define "bp-self-sovereign-cutover.hostProjectMapInline" -}}
{{- $pairs := list -}}
{{- $hosts := list -}}
{{- range .Values.offlineMirror.hostProjects -}}
{{- $pairs = append $pairs (printf "%s:%s" .host (.project | default "")) -}}
{{- $hosts = append $hosts .host -}}
{{- end -}}
{{- /*
   #5026 (Refs #5010 #4977) — REGRESSION-PROOF mothership coverage. The
   mothership Harbor host (harbor.openova.io) is a sovereignty TETHER: its
   `harbor.openova.io/proxy-<x>/PATH` pod-spec refs MUST host-swap to
   `registry.<fqdn>/proxy-<x>/PATH` (path preserved) in the step-07 sweep +
   step-08 completeness gate, or velero-hcs/cnpg/etc. fail the pre-hold
   completeness gate (proven live hw243). offlineMirror.hostProjects already
   carries it, but an operator overlay that rewrites hostProjects could drop
   it — so GUARANTEE it here (empty project = host-swap) whenever it isn't
   already present. The map is the single source both step-03/04/07/08 read,
   so this one guard fixes the whole class at the source. */ -}}
{{- $mo := (include "bp-self-sovereign-cutover.mothershipHost" .) -}}
{{- if and $mo (not (has $mo $hosts)) -}}
{{- $pairs = append $pairs (printf "%s:" $mo) -}}
{{- end -}}
{{- /*
   The Sovereign's own Harbor answers on BOTH registry.<fqdn> and
   harbor.<fqdn>. Workloads pivoted onto harbor.<fqdn> (vcluster control
   planes, sso-bridge) are local, but step-08 counted them UNMAPPED and the
   sovereignty proof failed on hw307 (2026-11-06). Treat harbor.<fqdn> as a
   host-swap/path-preserve alias of the local registry. */ -}}
{{- $alias := include "bp-self-sovereign-cutover.localRegistryAliasesInline" . -}}
{{- if and $alias (not (has $alias $hosts)) -}}
{{- $pairs = append $pairs (printf "%s:" $alias) -}}
{{- end -}}
{{- join " " $pairs -}}
{{- end -}}

{{/*
The other name of the local registry: harbor.<fqdn>, onto which steps 07
(LOCAL_IMAGE_HOST), 10 and 11 pivot references. The coverage map above and the
resolver (LOCAL_REGISTRY_ALIASES) read this one declaration.
*/}}
{{- define "bp-self-sovereign-cutover.localRegistryAliasesInline" -}}
{{- $fq := .Values.sovereign.fqdn | default "" -}}
{{- if and $fq (ne $fq "example.local") -}}{{- printf "harbor.%s" $fq -}}{{- end -}}
{{- end -}}

{{/*
The local projects a cutover step pivots references into that the coverage map
does not name, as `host:project`: step 11 rewrites <upstreamHost>/<path> to
harbor.<fqdn>/<registryPath>/<path> (.Values.crossplaneProviderPivot, which
step 11 reads). offlinemirror_upstream_ref inverts it (PIVOT_PROJECT_MAP).
*/}}
{{- define "bp-self-sovereign-cutover.pivotProjectMapInline" -}}
{{- with .Values.crossplaneProviderPivot -}}
{{- if and .upstreamHost .registryPath -}}{{- printf "%s:%s" .upstreamHost .registryPath -}}{{- end -}}
{{- end -}}
{{- end -}}

{{/*
The local projects that are Harbor proxy caches (.Values.harbor.proxyProjects,
which step 02 creates): Harbor refuses a push into them, so step 03 fills one by
a read through it with the local credential (PROXY_CACHE_PROJECTS).
*/}}
{{- define "bp-self-sovereign-cutover.proxyCacheProjectsInline" -}}
{{- $n := list -}}
{{- range .Values.harbor.proxyProjects -}}{{- $n = append $n .name -}}{{- end -}}
{{- join " " $n -}}
{{- end -}}

{{/*
Hosts EXCLUDED from the offline mirror (handled elsewhere / un-mirrorable):
xpkg.upbound.io is pivoted by step-11 (crossplaneProviderPivot) and bypasses
containerd. Space-separated, env-safe.
*/}}
{{- define "bp-self-sovereign-cutover.excludedHostsInline" -}}
{{- .Values.offlineMirror.excludedHosts | default (list) | join " " -}}
{{- end -}}

{{/*
── #5214 gitea-admin-secret STEP-EXECUTION self-heal initContainer ──────────
The DURABLE close of the #4557→#4558→#4661 recurrence class.

WHY (verified live hw271). Every cutover step Job that reads the Gitea admin
credential (06-helmrepository-patches, 07-catalyst-api-env-patch,
09-gitea-token-mint, 10-vcluster-registry-pivot, 11-crossplane-provider-pivot,
11-mirror-resync) resolves it via a Kubernetes `secretKeyRef` on
`gitea-admin-secret`, which resolves ONLY in the Pod's OWN namespace
(`catalyst`). Two install-time mechanisms materialise that copy —
00-gitea-admin-secret-bridge.yaml (render-time Helm lookup+mirror, keep-policy)
and 00-gitea-admin-secret-bridge-job.yaml (#4661 one-shot runtime copy). But
the step Jobs run 45+ min after install (after harbor-prewarm), and on hw271 the
copy was DELETED from `catalyst` between install and step-06 by an as-yet
unidentified reaper (NOT helm-controller — release stayed in-sync so keep was
never consulted; NOT kustomize-controller — the secret is in no inventory; NOT
kyverno — no delete policy). Nothing re-asserted it (the mirror only re-renders
on a helm upgrade; the #4661 Job is one-shot Complete) →
`CreateContainerConfigError: secret "gitea-admin-secret" not found` → wedge.

THE FIX. Re-assert the secret AT STEP-EXECUTION TIME, immediately before the
main container needs it, regardless of who deleted it and when. This
initContainer copies
  <sourceNamespace>/<sourceSecretName>  ->  <Release.Namespace>/<adminSecretRef.name>
with the SAME jq sanitize the #4661 bridge Job uses (rebuild-from-scratch drops
resourceVersion/uid/managedFields/creationTimestamp/ownerReferences/
last-applied-configuration and any helm.sh/hook* annotations by construction),
BUT stamps `helm.sh/resource-policy: keep` on the applied copy so a subsequent
helm reconcile cannot prune it. `kubectl apply` create-or-updates (idempotent),
and the copy is fail-loud: if the password key is absent post-copy it exits 1
so the step surfaces the real fault instead of a downstream 401.

It runs under the cutover runner SA (secrets get cluster-wide + secrets create
in the release namespace are already granted in rbac.yaml — no new RBAC). The
kubectl image is images.kubectl (alpine/k8s carries kubectl + jq, exactly as
the #4661 bridge Job relies on); `harbor.openova.io/proxy-*` refs resolve to
local Harbor post-pivot via the step-04 containerd rewrite. HOME=/tmp with a
writable rootfs (matching the step containers) so kubectl's cache has a home;
no extra Pod volume is needed, keeping the per-step edit to `initContainers:`.

Inputs (dict): .ctx (root context) .phase ("pre"|"post" image phase for the
step this init is attached to). Emit at the pod's initContainers list level via
`include ... (dict "ctx" . "phase" "post") | nindent 6` (see each step template).
*/}}
{{- define "bp-self-sovereign-cutover.giteaSecretSelfHealInitContainer" -}}
{{- $ctx := .ctx -}}
{{- $phase := .phase | default "post" -}}
{{- $srcNs := $ctx.Values.giteaAdminSecretBridge.sourceNamespace | default "gitea" -}}
{{- $srcName := $ctx.Values.giteaAdminSecretBridge.sourceSecretName | default "gitea-admin-secret" -}}
{{- $destName := $ctx.Values.gitea.adminSecretRef.name -}}
{{- $destNs := $ctx.Release.Namespace -}}
- name: ensure-gitea-admin-secret
  image: {{ include "bp-self-sovereign-cutover.image" (dict "repository" $ctx.Values.images.kubectl.repository "tag" $ctx.Values.images.kubectl.tag "cutoverPhase" $phase "Values" $ctx.Values) }}
  imagePullPolicy: IfNotPresent
  env:
    - name: HOME
      value: /tmp
    - name: SRC_NAMESPACE
      value: {{ $srcNs | quote }}
    - name: SRC_SECRET_NAME
      value: {{ $srcName | quote }}
    - name: DEST_NAMESPACE
      value: {{ $destNs | quote }}
    - name: DEST_SECRET_NAME
      value: {{ $destName | quote }}
  command: ["/bin/sh", "-c"]
  args:
    - |
      set -eu
      echo "[ensure-gitea-admin-secret] source = ${SRC_NAMESPACE}/${SRC_SECRET_NAME}"
      echo "[ensure-gitea-admin-secret] dest   = ${DEST_NAMESPACE}/${DEST_SECRET_NAME}"

      # Fast path — if the destination already carries a usable password key we
      # still re-copy from source (so a Gitea password rotation propagates), but
      # if the source has vanished AND the dest is intact we leave it be.
      if ! kubectl -n "${SRC_NAMESPACE}" get secret "${SRC_SECRET_NAME}" >/dev/null 2>&1; then
        if kubectl -n "${DEST_NAMESPACE}" get secret "${DEST_SECRET_NAME}" -o jsonpath='{.data.password}' 2>/dev/null | grep -q .; then
          echo "[ensure-gitea-admin-secret] source absent but destination already intact — leaving untouched"
          exit 0
        fi
        echo "[ensure-gitea-admin-secret] FATAL: source ${SRC_NAMESPACE}/${SRC_SECRET_NAME} absent and no destination secret to fall back on" >&2
        exit 1
      fi

      # Copy source -> destination. jq REBUILDS the object from scratch (drops
      # resourceVersion/uid/managedFields/creationTimestamp/ownerReferences/
      # last-applied + any helm.sh/hook* annotations by construction), re-targets
      # name+namespace, keeps only provenance annotations, and stamps
      # helm.sh/resource-policy: keep so nothing prunes the applied copy. The
      # base64 password bytes flow kubectl->jq->kubectl and are NEVER printed.
      kubectl -n "${SRC_NAMESPACE}" get secret "${SRC_SECRET_NAME}" -o json \
        | jq \
            --arg ns "${DEST_NAMESPACE}" \
            --arg name "${DEST_SECRET_NAME}" \
            --arg src "${SRC_NAMESPACE}/${SRC_SECRET_NAME}" \
            '{apiVersion, kind, type,
              metadata: {
                name: $name,
                namespace: $ns,
                labels: {
                  "catalyst.openova.io/blueprint": "bp-self-sovereign-cutover",
                  "catalyst.openova.io/component": "gitea-admin-secret-bridge",
                  "app.kubernetes.io/part-of": "catalyst",
                  "app.kubernetes.io/managed-by": "Helm"
                },
                annotations: {
                  "catalyst.openova.io/mirrored-from": $src,
                  "catalyst.openova.io/bridge": "gitea-admin-secret-cutover-ns-selfheal-5214",
                  "helm.sh/resource-policy": "keep"
                }
              },
              data: (.data // {})}' \
        | kubectl -n "${DEST_NAMESPACE}" apply --server-side --force-conflicts --field-manager=self-sovereign-cutover -f - >/dev/null

      # Fail loud: the step must not proceed on a keyless secret.
      if ! kubectl -n "${DEST_NAMESPACE}" get secret "${DEST_SECRET_NAME}" \
           -o jsonpath='{.data.password}' 2>/dev/null | grep -q .; then
        echo "[ensure-gitea-admin-secret] FATAL: destination secret missing the password key after copy" >&2
        exit 1
      fi
      echo "[ensure-gitea-admin-secret] ${DEST_NAMESPACE}/${DEST_SECRET_NAME} present with password key — step secretKeyRef will resolve"
  resources:
    requests:
      cpu: 25m
      memory: 32Mi
    limits:
      memory: 64Mi
  securityContext:
    runAsNonRoot: true
    runAsUser: 1001
    allowPrivilegeEscalation: false
    readOnlyRootFilesystem: false
    capabilities:
      drop: ["ALL"]
{{- end -}}

{{/*
Image-path substrings EXCLUDED from the offline mirror + the step-08 roll-set
(rancher/k3s = per-Org vcluster distro, un-mirrorable on the host plane;
loft-sh/vcluster = handled by step-10 vcluster-registry-pivot). Space-separated.
*/}}
{{- define "bp-self-sovereign-cutover.excludedSubstringsInline" -}}
{{- .Values.offlineMirror.excludedImageSubstrings | default (list) | join " " -}}
{{- end -}}

{{/*
Step-03's exclusion list: excludedImageSubstrings MINUS mirrorAnyway. An
excluded substring is skipped by BOTH the mirror and the step-08 roll-set;
mirrorAnyway entries stay out of the roll-set but ARE copied into local
Harbor. loft-sh/vcluster is the case: step-10 only rewrites the control-plane
image reference to harbor.<fqdn>/proxy-ghcr/loft-sh/vcluster-oss — it copies
nothing — so excluding it from the mirror left every vcluster-0 in
ImagePullBackOff during the hw307 deny-egress hold (2026-11-06).
*/}}
{{- define "bp-self-sovereign-cutover.mirrorExcludedSubstringsInline" -}}
{{- $keep := .Values.offlineMirror.mirrorAnyway | default (list) -}}
{{- $out := list -}}
{{- range (.Values.offlineMirror.excludedImageSubstrings | default (list)) -}}
{{- if not (has . $keep) -}}{{- $out = append $out . -}}{{- end -}}
{{- end -}}
{{- join " " $out -}}
{{- end -}}

{{/*
#5468 — image-path substrings copied SINGLE-PLATFORM by step-03's skopeo
mirror instead of the #4975 `--multi-arch all` default. Space-separated for
the POSIX-sh `for` loop in prewarm_multiarch_flags. An empty list renders an
empty string, which that loop iterates zero times — i.e. no exception at all.
*/}}
{{- define "bp-self-sovereign-cutover.singlePlatformSubstringsInline" -}}
{{- .Values.prewarm.singlePlatform.imageSubstrings | default (list) | join " " -}}
{{- end -}}

{{- /* offlinemirrorCopy: the rules are above offlineMirror in values.yaml (#6004). */}}
{{- define "bp-self-sovereign-cutover.offlinemirrorCopy" -}}
offlinemirror_skopeo_copy() {
  _os_s="$1"; _os_c="$2"; _os_d="$3"; shift 3
  [ -z "${_os_c}" ] || set -- "$@" --src-creds="${_os_c}"
  skopeo --command-timeout "${PREWARM_SKOPEO_TIMEOUT:-1200s}" copy --insecure-policy --preserve-digests "$@" --retry-times 5 \
    --dest-creds="${HARBOR_USERNAME}:${HARBOR_PASSWORD}" --dest-tls-verify="${PREWARM_DEST_TLS_VERIFY:-false}" \
    --src-tls-verify=true "docker://${_os_s}" "docker://${_os_d}"
}
offlinemirror_copy() {
  _oc_s="$1"; _oc_c="$2"; _oc_d="$3"; shift 3; _oc_k=""; _oc_p=""
  if [ "$*" = "--multi-arch all" ]; then
    set -- --command-timeout "${PREWARM_SKOPEO_TIMEOUT:-1200s}" inspect --raw --tls-verify=true
    [ -z "${_oc_c}" ] || set -- "$@" --creds="${_oc_c}"
    _oc_k=$(skopeo "$@" "docker://${_oc_s}" 2>/dev/null) || _oc_k=""
    _oc_p=$(printf '%s' "${_oc_k}" | jq -r 'select(.manifests and (.artifactType or .subject or .mediaType == null)) | 1' 2>/dev/null) || _oc_p=""
    _oc_k=$(printf '%s' "${_oc_k}" | jq -r '.manifests[]?.digest' 2>/dev/null) || _oc_k=""
    set -- --multi-arch all
  fi
  [ -n "${_oc_k}" ] || { offlinemirror_skopeo_copy "${_oc_s}" "${_oc_c}" "${_oc_d}" "$@"; return $?; }
  _oc_sr="${_oc_s%%@*}"; case "${_oc_sr##*/}" in *:*) _oc_sr="${_oc_sr%:*}" ;; esac
  _oc_dr="${_oc_d%%@*}"; case "${_oc_dr##*/}" in *:*) _oc_dr="${_oc_dr%:*}" ;; esac
  for _oc_i in ${_oc_k}; do
    offlinemirror_skopeo_copy "${_oc_sr}@${_oc_i}" "${_oc_c}" "${_oc_dr}@${_oc_i}" --multi-arch all || return $?
  done
  [ -n "${_oc_p}" ] || { offlinemirror_skopeo_copy "${_oc_s}" "${_oc_c}" "${_oc_d}" --multi-arch index-only; return $?; }
  offlinemirror_put_list "${_oc_s}" "${_oc_c}" "${_oc_d}" "${_oc_dr}"
}
offlinemirror_put_list() {
  _op_s="$1"; _op_c="$2"; _op_d="$3"; _op_dr="$4"; _op_t="${3#"$4"}"; _op_t="${_op_t#[:@]}"; _op_f=$(mktemp) || return 1
  set -- --command-timeout "${PREWARM_SKOPEO_TIMEOUT:-1200s}" inspect --raw --tls-verify=true; [ -z "${_op_c}" ] || set -- "$@" --creds="${_op_c}"
  skopeo "$@" "docker://${_op_s}" >"${_op_f}" 2>/dev/null || { echo "[offlinemirror] cannot read ${_op_s}"; rm -f "${_op_f}"; return 1; }
  _op_g="sha256:$(sha256sum <"${_op_f}" | awk '{print $1}')"; _op_w="${_op_g}"
  case "${_op_s}" in *@sha256:*) _op_w="${_op_s##*@}" ;; esac
  [ "${_op_g}" = "${_op_w}" ] || { echo "[offlinemirror] ${_op_s} served ${_op_g}"; rm -f "${_op_f}"; return 1; }
  _op_m=$(jq -r '.mediaType // "application/vnd.oci.image.index.v1+json"' <"${_op_f}")
  _op_x=-k; [ "${PREWARM_DEST_TLS_VERIFY:-false}" != true ] || _op_x=""
  echo "[offlinemirror] PUT list ${_op_w} to ${_op_d} (skopeo would rewrite it)"
  _op_r=$(curl -sS ${_op_x} --max-time 120 -u "${HARBOR_USERNAME}:${HARBOR_PASSWORD}" -X PUT -H "Content-Type: ${_op_m}" \
    --data-binary "@${_op_f}" -D "${_op_f}.h" -o "${_op_f}.b" -w '%{http_code}' "https://${_op_dr%%/*}/v2/${_op_dr#*/}/manifests/${_op_t:-latest}" 2>&1) || true
  _op_a=$(sed -n 's/^[Dd]ocker-[Cc]ontent-[Dd]igest:[[:space:]]*\([^[:space:]]*\).*/\1/p' "${_op_f}.h" 2>/dev/null)
  _op_b=$(head -c 300 "${_op_f}.b" 2>/dev/null); rm -f "${_op_f}" "${_op_f}.h" "${_op_f}.b"
  [ "${_op_r}" = 201 ] && [ "${_op_a}" = "${_op_w}" ] && return 0
  echo "level=fatal msg=\"writing manifest list ${_op_d}: HTTP ${_op_r}, digest ${_op_a:-none}, want ${_op_w}: ${_op_b}\""; return 1
}
{{- end -}}

{{- /* skopeoFetch: skopeo_fetch DIR PREFIX; the rules are above the key skopeo in values.yaml (#6004). */}}
{{- define "bp-self-sovereign-cutover.skopeoFetch" -}}
skopeo_fetch() {
  _sf_u={{ printf .Values.skopeo.url .Values.skopeo.version | quote }}; _sf_w={{ .Values.skopeo.sha256 | quote }}; _sf_i=1
  while [ "${_sf_i}" -le 3 ]; do
    if curl -fsSL --max-time 600 --retry 3 --retry-delay 5 -C - "${_sf_u}" -o "$1/skopeo" 2>&1; then
      _sf_h=$(sha256sum <"$1/skopeo" | cut -c1-64)
      [ "${_sf_h}" = "${_sf_w}" ] && chmod +x "$1/skopeo" && "$1/skopeo" --version >/dev/null 2>&1 && export PATH="$1:${PATH}" && return 0
      echo "$2 ${_sf_u} has sha256 ${_sf_h}, want ${_sf_w} (or does not run); deleted"; rm -f "$1/skopeo"
    fi
    echo "$2 skopeo download attempt ${_sf_i}/3 failed; retrying in 5s"; _sf_i=$((_sf_i + 1)); sleep 5
  done
  rm -f "$1/skopeo"; return 1
}
{{- end -}}

{{/*
curl_no_answer <curl exit code> names a curl that ended without an HTTP answer; the registry's
readiness probes (step 03, secondary prewarm) count it as one not-ready probe, not a pod's end.
*/}}
{{- define "bp-self-sovereign-cutover.curlNoAnswer" -}}
curl_no_answer() {
  case "$1" in
    6) echo "could not resolve host, curl exit 6" ;;
    7) echo "could not connect, curl exit 7" ;;
    28) echo "timed out, curl exit 28" ;;
    35) echo "TLS not ready, curl exit 35" ;;
    *) echo "no HTTP answer, curl exit $1" ;;
  esac
}
{{- end -}}

{{/* SOV-343 — the settings tofuProviders of values.yaml, the environment of steps 03 and 11 and the day-two reconciler. */}}
{{- define "bp-self-sovereign-cutover.tofuProvidersEnv" -}}
- name: TOFU_NAMESPACE
  value: {{ .Values.tofuProviders.namespace | quote }}
- name: TOFU_LIST_CM
  value: {{ .Values.tofuProviders.listConfigMapName | quote }}
- name: TOFU_CLI_CM
  value: {{ .Values.tofuProviders.cliConfigMapName | quote }}
- name: TOFU_ORIGIN_PREFIX
  value: {{ .Values.tofuProviders.originPrefix | quote }}
- name: TOFU_LOCAL_PATH
  value: {{ .Values.tofuProviders.localPath | quote }}
- name: TOFU_CLI_FILE
  value: {{ .Values.tofuProviders.cliConfigFile | quote }}
{{- end -}}

{{/* SOV-343 — the one reading of the list of OpenTofu provider packages; described above the key tofuProviders of values.yaml. */}}
{{- define "bp-self-sovereign-cutover.tofuProviders" -}}
# tofu-providers-list BEGIN
tofu_list_read() {
  _tr=$(kubectl -n "${TOFU_NAMESPACE}" get configmap "${TOFU_LIST_CM}" --ignore-not-found \
    -o 'jsonpath={.metadata.name}{"\n"}{.data.providers}' 2>/dev/null) || return 1
  [ -n "${_tr}" ] || return 2
  printf '%s\n' "${_tr}" | sed '1d; /^[[:space:]]*$/d' >"$1"
}
tofu_list_line() {
  set -f; set -- $1; set +f
  [ "$#" -eq 3 ] || return 1
  _tl_repo="${1#*/}"; _tl_ver="$2"; _tl_dig="$3"
  case "${_tl_repo}" in */*/*|/*|*/) return 1 ;; */*) ;; *) return 1 ;; esac
  case "${_tl_dig#sha256:}" in "${_tl_dig}"|*[!0-9a-f]*) return 1 ;; esac
  [ "${#_tl_dig}" -eq 71 ]
}
tofu_local_digest() {
  curl -sSIk --max-time 30 -u "${HARBOR_USERNAME:-admin}:${HARBOR_PASSWORD:-}" \
    -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json' \
    "${HARBOR_INTERNAL_URL}/v2/${TOFU_LOCAL_PATH}/$1/manifests/$2" 2>/dev/null \
    | tr -d '\r' | sed -n 's/^[Dd]ocker-[Cc]ontent-[Dd]igest:[[:space:]]*//p' | tail -n 1
}
# tofu-providers-list END
{{- end -}}
