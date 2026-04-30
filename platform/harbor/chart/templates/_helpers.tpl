{{/*
Catalyst-curated helpers for bp-harbor. Mirrors the conventions used by
bp-cilium / bp-cert-manager / bp-external-dns / bp-powerdns.
*/}}

{{- define "bp-harbor.fullname" -}}
{{- default "harbor" .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "bp-harbor.labels" -}}
app.kubernetes.io/name: {{ include "bp-harbor.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-harbor
catalyst.openova.io/component: harbor
{{- end -}}

{{- define "bp-harbor.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-harbor.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
