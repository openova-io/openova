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
Required-config no-op for chart render — runtime check lives in the Go
adapter binary which fail-fasts on empty FLOW_SERVER_URL / FLOW_ID /
REGION_KEY (products/openova-flow/adapter-flux/internal/config/env.go).
Surfacing the same gate at chart render time blocked the Blueprint
Release smoke step which always renders with default values, so the
chart was unpublishable. The bootstrap-kit HR
(clusters/_template/bootstrap-kit/57-bp-openova-flow-emitter.yaml)
supplies the real values at install time; if it doesn't, the adapter
pod CrashLoops with a clear error in `kubectl logs`.
*/}}
{{- define "bp-openova-flow-emitter.requireConfig" -}}
{{- /* intentionally empty — binary validates env at startup */ -}}
{{- end -}}
