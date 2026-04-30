{{/*
Catalyst-curated helpers for bp-knative. Mirrors the conventions used by
bp-cilium / bp-cert-manager / bp-seaweedfs / bp-vllm.
*/}}

{{- define "bp-knative.name" -}}
{{- default "knative" .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "bp-knative.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default "knative" .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "bp-knative.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "bp-knative.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: serverless
catalyst.openova.io/blueprint: bp-knative
{{- end -}}

{{- define "bp-knative.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-knative.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
