{{- define "bp-openova-flow-server.name" -}}
{{- default (.Chart.Name | trimPrefix "bp-") .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "bp-openova-flow-server.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "bp-openova-flow-server.workloadName" -}}
{{- default "openova-flow-server" .Values.flowServer.workloadName -}}
{{- end -}}

{{- define "bp-openova-flow-server.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "bp-openova-flow-server.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-openova-flow-server
{{- end -}}

{{- define "bp-openova-flow-server.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-openova-flow-server.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "bp-openova-flow-server.serviceAccountName" -}}
{{- if .Values.flowServer.serviceAccount.create -}}
{{- default (include "bp-openova-flow-server.workloadName" .) .Values.flowServer.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.flowServer.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Image-tag fail-fast — INVIOLABLE-PRINCIPLES #4a.
*/}}
{{- define "bp-openova-flow-server.image" -}}
{{- $tag := .Values.flowServer.image.tag -}}
{{- if not $tag -}}
{{- fail "bp-openova-flow-server: .Values.flowServer.image.tag is empty — SHA-pinned image required (CI populates this)" -}}
{{- end -}}
{{- printf "%s:%s" .Values.flowServer.image.repository $tag -}}
{{- end -}}
