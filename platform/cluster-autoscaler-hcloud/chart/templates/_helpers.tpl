{{/*
Catalyst-curated helpers for bp-cluster-autoscaler-hcloud. Mirrors the
conventions used by bp-vpa / bp-velero / bp-external-dns.
*/}}

{{- define "bp-cluster-autoscaler-hcloud.fullname" -}}
{{- default "cluster-autoscaler-hcloud" .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "bp-cluster-autoscaler-hcloud.labels" -}}
app.kubernetes.io/name: {{ include "bp-cluster-autoscaler-hcloud.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-cluster-autoscaler-hcloud
catalyst.openova.io/component: cluster-autoscaler-hcloud
{{- end -}}

{{- define "bp-cluster-autoscaler-hcloud.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-cluster-autoscaler-hcloud.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
