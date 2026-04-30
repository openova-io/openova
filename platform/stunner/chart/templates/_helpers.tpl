{{/*
Catalyst-curated helpers for bp-stunner. Mirrors the conventions used by
bp-cilium / bp-cert-manager / bp-seaweedfs / bp-vllm.
*/}}

{{- define "bp-stunner.name" -}}
{{- default "stunner" .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "bp-stunner.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default "stunner" .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "bp-stunner.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "bp-stunner.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: media-gateway
catalyst.openova.io/blueprint: bp-stunner
{{- end -}}

{{- define "bp-stunner.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-stunner.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
