{{/*
Common labels
*/}}
{{- define "axon.labels" -}}
app.kubernetes.io/name: axon
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "axon.selectorLabels" -}}
app.kubernetes.io/name: axon
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Valkey selector labels
*/}}
{{- define "axon.valkey.selectorLabels" -}}
app.kubernetes.io/name: axon-valkey
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
