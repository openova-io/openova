{{/*
Catalyst-curated helpers for bp-velero-hcs (HCS-native Velero with
Huawei OBS S3-compatible backend). Mirrors bp-velero one-for-one
except the helper / blueprint label values say `bp-velero-hcs` so
operator drill-down can distinguish the HCS variant.
*/}}

{{- define "bp-velero-hcs.fullname" -}}
{{- default "velero" .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "bp-velero-hcs.labels" -}}
app.kubernetes.io/name: {{ include "bp-velero-hcs.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-velero-hcs
catalyst.openova.io/component: velero
{{- end -}}

{{/*
Object Storage credential secret name — the velero-namespace Secret
that ships the operator-issued OBS S3 keys to Velero's deployment in
the AWS-CLI INI format that velero-plugin-for-aws expects at
/credentials/cloud (AWS_SHARED_CREDENTIALS_FILE).

The default name matches bp-velero (`velero-objectstorage-credentials`)
so per-Sovereign overlays migrating off the suspended Hetzner-flavored
chart don't need to rename the existingSecret reference. Override key:
`objectStorage.credentialsSecretName`.
*/}}
{{- define "bp-velero-hcs.objectStorageCredentialsSecretName" -}}
{{- default "velero-objectstorage-credentials" .Values.objectStorage.credentialsSecretName -}}
{{- end -}}
