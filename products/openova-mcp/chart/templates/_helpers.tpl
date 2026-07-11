{{/*
Expand the name of the chart.
*/}}
{{- define "bp-openova-mcp.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "bp-openova-mcp.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Common labels — required by docs/RUNBOOKS.md (Blueprint authoring) and by the
Catalyst projector to track resources back to the Blueprint.
*/}}
{{- define "bp-openova-mcp.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "bp-openova-mcp.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-openova-mcp
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "bp-openova-mcp.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-openova-mcp.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "bp-openova-mcp.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "bp-openova-mcp.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
ConfigMap name (non-secret OPENOVA_MCP_* env).
*/}}
{{- define "bp-openova-mcp.configMapName" -}}
{{- printf "%s-config" (include "bp-openova-mcp.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
