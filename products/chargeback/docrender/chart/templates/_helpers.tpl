{{/* workload name stem — "docrender" */}}
{{- define "docrender.name" -}}
{{- default "docrender" .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
fully-qualified name. As a SUB-CHART of bp-chargeback the release name is
the parent's ("chargeback"), so this resolves to "chargeback-docrender" —
which is exactly the Service name the parent's DOCRENDER_URL helper composes.
The two must never drift; chart/tests/render-contract.sh pins them together.
*/}}
{{- define "docrender.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := include "docrender.name" . -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* common labels */}}
{{- define "docrender.labels" -}}
app.kubernetes.io/name: {{ include "docrender.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: document-renderer
app.kubernetes.io/part-of: chargeback
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-chargeback
{{- end -}}

{{/* selector labels */}}
{{- define "docrender.selectorLabels" -}}
app.kubernetes.io/name: {{ include "docrender.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- /*
docrender.image — repository:tag honouring the cutover step-07
global.imageRegistry pivot seam (#4885/#4892): when set, strip the leading
registry host off the repository and re-prefix with the pivot registry so the
image pulls from the local Harbor under the deny-egress hold.
*/ -}}
{{- define "docrender.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- $repo := .Values.image.repository -}}
{{- $registry := "" -}}
{{- if .Values.global -}}
{{- $registry = .Values.global.imageRegistry | default "" -}}
{{- end -}}
{{- if $registry -}}
{{- $parts := splitList "/" $repo -}}
{{- $rest := $repo -}}
{{- if gt (len $parts) 1 -}}
{{- $rest = rest $parts | join "/" -}}
{{- end -}}
{{- printf "%s/%s:%s" (trimSuffix "/" $registry) $rest $tag -}}
{{- else -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}
{{- end -}}

{{/* ServiceAccount name */}}
{{- define "docrender.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "docrender.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* the namespace the admitted callers run in — empty means this one */}}
{{- define "docrender.callerNamespace" -}}
{{- default .Release.Namespace .Values.networkPolicy.fromNamespace -}}
{{- end -}}
