{{/* chart name */}}
{{- define "chepherd.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* fully-qualified name */}}
{{- define "chepherd.fullname" -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/* common labels */}}
{{- define "chepherd.labels" -}}
app.kubernetes.io/name: {{ include "chepherd.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-chepherd
{{- end -}}

{{/* selector labels */}}
{{- define "chepherd.selectorLabels" -}}
app.kubernetes.io/name: {{ include "chepherd.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/* image ref */}}
{{- define "chepherd.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}
