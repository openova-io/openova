{{/*
Catalyst-curated helpers for bp-external-dns. Mirrors the conventions used
by bp-cilium / bp-keycloak / bp-cert-manager / bp-powerdns.
*/}}

{{- define "bp-external-dns.fullname" -}}
{{- default "external-dns" .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "bp-external-dns.labels" -}}
app.kubernetes.io/name: {{ include "bp-external-dns.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-external-dns
catalyst.openova.io/component: external-dns
{{- end -}}

{{- define "bp-external-dns.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-external-dns.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
