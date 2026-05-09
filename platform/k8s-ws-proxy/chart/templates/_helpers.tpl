{{- define "bp-k8s-ws-proxy.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "bp-k8s-ws-proxy.fullname" -}}
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

{{- define "bp-k8s-ws-proxy.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "bp-k8s-ws-proxy.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-k8s-ws-proxy
{{- end }}

{{- define "bp-k8s-ws-proxy.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-k8s-ws-proxy.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "bp-k8s-ws-proxy.serviceAccountName" -}}
{{- if .Values.k8sWsProxy.serviceAccount.create }}
{{- default (include "bp-k8s-ws-proxy.fullname" .) .Values.k8sWsProxy.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.k8sWsProxy.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Image-tag fail-fast — INVIOLABLE-PRINCIPLES #4a.
*/}}
{{- define "bp-k8s-ws-proxy.image" -}}
{{- $tag := .Values.k8sWsProxy.image.tag -}}
{{- if not $tag -}}
{{- fail "bp-k8s-ws-proxy: .Values.k8sWsProxy.image.tag is empty — SHA-pinned image required (CI populates this)" -}}
{{- end -}}
{{- printf "%s:%s" .Values.k8sWsProxy.image.repository $tag -}}
{{- end }}

{{/*
HMAC secret name fail-fast — operator MUST pre-provision it.
*/}}
{{- define "bp-k8s-ws-proxy.hmacSecretName" -}}
{{- $n := .Values.k8sWsProxy.hmacSecret.name -}}
{{- if not $n -}}
{{- fail "bp-k8s-ws-proxy: .Values.k8sWsProxy.hmacSecret.name is empty — provision a Secret with the HMAC shared key first" -}}
{{- end -}}
{{- $n -}}
{{- end }}
