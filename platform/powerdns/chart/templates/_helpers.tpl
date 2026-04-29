{{/*
Catalyst-curated helpers for bp-powerdns. Mirrors the conventions used by
bp-cilium / bp-keycloak / bp-cert-manager.
*/}}

{{- define "bp-powerdns.fullname" -}}
{{- default "powerdns" .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "bp-powerdns.namespace" -}}
{{- default .Release.Namespace .Values.postgres.cluster.namespace -}}
{{- end -}}

{{- define "bp-powerdns.labels" -}}
app.kubernetes.io/name: {{ include "bp-powerdns.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-powerdns
catalyst.openova.io/component: powerdns
{{- end -}}

{{- define "bp-powerdns.dnsdistLabels" -}}
app.kubernetes.io/name: dnsdist
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: query-frontend
catalyst.openova.io/blueprint: bp-powerdns
{{- end -}}
