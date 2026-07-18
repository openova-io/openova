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
{{- join " " $pairs -}}
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
        | kubectl -n "${DEST_NAMESPACE}" apply -f - >/dev/null

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
