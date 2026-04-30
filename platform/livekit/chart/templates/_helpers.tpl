{{/*
Catalyst-curated helpers for bp-livekit. Mirrors the conventions used
by bp-harbor / bp-valkey / bp-cnpg / bp-librechat.
*/}}

{{- define "bp-livekit.fullname" -}}
{{- default "livekit" .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "bp-livekit.labels" -}}
app.kubernetes.io/name: {{ include "bp-livekit.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-livekit
catalyst.openova.io/component: livekit
{{- end -}}

{{- define "bp-livekit.selectorLabels" -}}
app.kubernetes.io/name: livekit-server
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
