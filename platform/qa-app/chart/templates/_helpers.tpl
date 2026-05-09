{{/*
Expand the name of the chart. Per docs/BLUEPRINT-AUTHORING.md the chart
name is `bp-<slug>` and the default release name strips the prefix.
*/}}
{{- define "qa-app.name" -}}
{{- default (.Chart.Name | trimPrefix "bp-") .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "qa-app.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "qa-app.labels" -}}
app.kubernetes.io/name: {{ include "qa-app.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
catalyst.openova.io/blueprint: bp-qa-app
{{- end -}}

{{- define "qa-app.selectorLabels" -}}
app.kubernetes.io/name: {{ include "qa-app.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
