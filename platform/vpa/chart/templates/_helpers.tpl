{{/*
Catalyst-curated helpers for bp-vpa. Mirrors the conventions used by
bp-cilium / bp-cert-manager / bp-external-dns / bp-powerdns.
*/}}

{{- define "bp-vpa.fullname" -}}
{{- default "vpa" .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "bp-vpa.labels" -}}
app.kubernetes.io/name: {{ include "bp-vpa.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-vpa
catalyst.openova.io/component: vertical-pod-autoscaler
{{- end -}}

{{- define "bp-vpa.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-vpa.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
