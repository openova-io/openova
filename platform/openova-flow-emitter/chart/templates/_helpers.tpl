{{- define "bp-openova-flow-emitter.name" -}}
{{- default (.Chart.Name | trimPrefix "bp-") .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "bp-openova-flow-emitter.workloadName" -}}
{{- default "openova-flow-emitter" .Values.flowEmitter.workloadName -}}
{{- end -}}

{{- define "bp-openova-flow-emitter.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "bp-openova-flow-emitter.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-openova-flow-emitter
{{- end -}}

{{- define "bp-openova-flow-emitter.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-openova-flow-emitter.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "bp-openova-flow-emitter.serviceAccountName" -}}
{{- if .Values.flowEmitter.serviceAccount.create -}}
{{- default (include "bp-openova-flow-emitter.workloadName" .) .Values.flowEmitter.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.flowEmitter.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Image-tag fail-fast — INVIOLABLE-PRINCIPLES #4a.
*/}}
{{- define "bp-openova-flow-emitter.image" -}}
{{- $tag := .Values.flowEmitter.image.tag -}}
{{- if not $tag -}}
{{- fail "bp-openova-flow-emitter: .Values.flowEmitter.image.tag is empty — SHA-pinned image required (CI populates this)" -}}
{{- end -}}
{{- printf "%s:%s" .Values.flowEmitter.image.repository $tag -}}
{{- end -}}

{{/*
Required-config fail-fast — INVIOLABLE-PRINCIPLES #1 (target-state).
The adapter must have all three of FLOW_SERVER_URL, FLOW_ID,
REGION_KEY at boot or it fails immediately. Failing at chart render
gives the operator a clear error instead of an ImagePullBackOff
silence.
*/}}
{{- define "bp-openova-flow-emitter.requireConfig" -}}
{{- if not .Values.flowEmitter.flowServerUrl -}}
{{- fail "bp-openova-flow-emitter: .Values.flowEmitter.flowServerUrl is required" -}}
{{- end -}}
{{- if not .Values.flowEmitter.flowId -}}
{{- fail "bp-openova-flow-emitter: .Values.flowEmitter.flowId is required" -}}
{{- end -}}
{{- if not .Values.flowEmitter.regionKey -}}
{{- fail "bp-openova-flow-emitter: .Values.flowEmitter.regionKey is required" -}}
{{- end -}}
{{- end -}}
