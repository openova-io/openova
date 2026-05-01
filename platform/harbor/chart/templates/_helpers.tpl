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

{{/*
Object Storage credential secret name — the harbor-namespace Secret
that ships the operator-issued S3 keys to the upstream Harbor
registry deployment via persistence.imageChartStorage.s3.existingSecret.
The upstream chart consumes the Secret as an envFrom on the registry
pod, expecting two keys:
  - REGISTRY_STORAGE_S3_ACCESSKEY
  - REGISTRY_STORAGE_S3_SECRETKEY
(See charts/harbor/templates/registry/registry-dpl.yaml +
 charts/harbor/templates/registry/registry-secret.yaml.)

The chart is vendor-agnostic per #383 / #425 — the override key
`objectStorage.credentialsSecretName` carries any per-Sovereign
customisation without leaking the cloud-provider name into the
helper API.
*/}}
{{- define "bp-harbor.objectStorageCredentialsSecretName" -}}
{{- default "harbor-objectstorage-credentials" .Values.objectStorage.credentialsSecretName -}}
{{- end -}}
