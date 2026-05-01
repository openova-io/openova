{{/*
Catalyst-curated helpers for bp-velero. Mirrors the conventions used by
bp-powerdns / bp-cilium / bp-cert-manager.
*/}}

{{- define "bp-velero.fullname" -}}
{{- default "velero" .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "bp-velero.labels" -}}
app.kubernetes.io/name: {{ include "bp-velero.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-velero
catalyst.openova.io/component: velero
{{- end -}}

{{/*
Hetzner Object Storage credential secret name — the velero-namespace
Secret that ships the operator-issued S3 keys to Velero's deployment in
the AWS-CLI INI format that velero-plugin-for-aws expects at
/credentials/cloud (AWS_SHARED_CREDENTIALS_FILE).
*/}}
{{- define "bp-velero.hetznerCredentialsSecretName" -}}
{{- default "velero-hetzner-credentials" .Values.veleroOverlay.hetzner.credentialsSecretName -}}
{{- end -}}
