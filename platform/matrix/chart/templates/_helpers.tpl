{{/*
Catalyst-curated helpers for bp-matrix. Mirrors the conventions used
by bp-harbor / bp-valkey / bp-cnpg / bp-librechat.
*/}}

{{- define "bp-matrix.fullname" -}}
{{- default "matrix" .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "bp-matrix.labels" -}}
app.kubernetes.io/name: {{ include "bp-matrix.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-matrix
catalyst.openova.io/component: synapse
catalyst.openova.io/federation: {{ .Values.federation.enabled | quote }}
{{- end -}}

{{- define "bp-matrix.selectorLabels" -}}
app.kubernetes.io/name: matrix-synapse
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
