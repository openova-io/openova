{{/*
Expand the name of the chart.
*/}}
{{- define "bp-nemo-guardrails.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "bp-nemo-guardrails.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "bp-nemo-guardrails.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "bp-nemo-guardrails.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: ai-safety
catalyst.openova.io/blueprint: bp-nemo-guardrails
{{- end }}

{{/*
Selector labels
*/}}
{{- define "bp-nemo-guardrails.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-nemo-guardrails.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name
*/}}
{{- define "bp-nemo-guardrails.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "bp-nemo-guardrails.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Effective ConfigMap name — externalName when set (production), else
the chart-rendered stub ConfigMap.
*/}}
{{- define "bp-nemo-guardrails.configMapName" -}}
{{- if .Values.configMap.externalName }}
{{- .Values.configMap.externalName }}
{{- else }}
{{- printf "%s-config" (include "bp-nemo-guardrails.fullname" .) }}
{{- end }}
{{- end }}
