{{/*
Catalyst-curated helpers for bp-hcloud-ccm. Mirrors the conventions used
by bp-cluster-autoscaler-hcloud / bp-hcloud-csi.
*/}}

{{- define "bp-hcloud-ccm.fullname" -}}
{{- default "hcloud-ccm" .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "bp-hcloud-ccm.labels" -}}
app.kubernetes.io/name: {{ include "bp-hcloud-ccm.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-hcloud-ccm
catalyst.openova.io/component: hcloud-cloud-controller-manager
{{- end -}}

{{- define "bp-hcloud-ccm.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-hcloud-ccm.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
