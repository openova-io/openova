{{/*
Catalyst-curated helpers for bp-seaweedfs. Mirrors the conventions used
by bp-cilium / bp-cert-manager / bp-external-dns / bp-powerdns.
*/}}

{{- define "bp-seaweedfs.fullname" -}}
{{- default "seaweedfs" .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "bp-seaweedfs.labels" -}}
app.kubernetes.io/name: {{ include "bp-seaweedfs.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-seaweedfs
catalyst.openova.io/component: seaweedfs
{{- end -}}

{{- define "bp-seaweedfs.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-seaweedfs.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
