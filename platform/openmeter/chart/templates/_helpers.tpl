{{/*
Catalyst-curated helpers for bp-openmeter. Mirrors the conventions used
by bp-harbor / bp-valkey / bp-cnpg / bp-librechat.
*/}}

{{- define "bp-openmeter.fullname" -}}
{{- default "openmeter" .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "bp-openmeter.labels" -}}
app.kubernetes.io/name: {{ include "bp-openmeter.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-openmeter
catalyst.openova.io/component: openmeter
catalyst.openova.io/backend: {{ .Values.catalystBlueprint.backend.kind | quote }}
{{- end -}}

{{- define "bp-openmeter.selectorLabels" -}}
app.kubernetes.io/name: openmeter
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
