{{/*
Expand the name of the chart.
*/}}
{{- define "bp-vllm.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "bp-vllm.fullname" -}}
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
{{- define "bp-vllm.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "bp-vllm.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: ai-runtime
catalyst.openova.io/blueprint: bp-vllm
{{- end }}

{{/*
Selector labels
*/}}
{{- define "bp-vllm.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-vllm.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name
*/}}
{{- define "bp-vllm.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "bp-vllm.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Resources block — switches between CPU and GPU shapes based on
vllm.gpu.enabled. Adds nvidia.com/gpu to limits when GPU mode is on.
*/}}
{{- define "bp-vllm.resources" -}}
{{- if .Values.vllm.gpu.enabled }}
requests:
  cpu: {{ .Values.vllm.resources.gpu.requests.cpu | quote }}
  memory: {{ .Values.vllm.resources.gpu.requests.memory | quote }}
limits:
  cpu: {{ .Values.vllm.resources.gpu.limits.cpu | quote }}
  memory: {{ .Values.vllm.resources.gpu.limits.memory | quote }}
  nvidia.com/gpu: {{ .Values.vllm.gpu.count | quote }}
{{- else }}
requests:
  cpu: {{ .Values.vllm.resources.cpu.requests.cpu | quote }}
  memory: {{ .Values.vllm.resources.cpu.requests.memory | quote }}
limits:
  cpu: {{ .Values.vllm.resources.cpu.limits.cpu | quote }}
  memory: {{ .Values.vllm.resources.cpu.limits.memory | quote }}
{{- end }}
{{- end }}
