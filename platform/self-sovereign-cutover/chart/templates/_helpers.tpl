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
