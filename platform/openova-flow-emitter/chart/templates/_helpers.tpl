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

#4885 (Refs #3379 #4563): honour `global.imageRegistry`. The repository is
the full ghcr.io path (ghcr.io/openova-io/openova/openova-flow-adapter-flux).
The self-sovereign-cutover step-07 registry pivot patches this HR's
spec.values.global.imageRegistry = registry.<sovereign-fqdn>; when set, swap
ONLY the leading registry host so the image resolves to
registry.<fqdn>/openova-io/openova/openova-flow-adapter-flux — the path the
cutover step-03 harbor-prewarm natively pushes the image to. Without the
override the literal ghcr.io ref is unreachable under the step-08 600s
deny-egress hold (anonymous-token fetch blocked → 401 ImagePullBackOff) and
the fresh-pull proof FATALs (cutoverComplete stays false). Empty/unset =
byte-identical ghcr.io ref (pre-cutover default). Mirrors the sibling
bp-openova-flow-server.image helper.
*/}}
{{- define "bp-openova-flow-emitter.image" -}}
{{- $tag := .Values.flowEmitter.image.tag -}}
{{- if not $tag -}}
{{- fail "bp-openova-flow-emitter: .Values.flowEmitter.image.tag is empty — SHA-pinned image required (CI populates this)" -}}
{{- end -}}
{{- $repo := .Values.flowEmitter.image.repository -}}
{{- $registry := "" -}}
{{- if .Values.global -}}
{{- $registry = .Values.global.imageRegistry | default "" -}}
{{- end -}}
{{- if $registry -}}
{{/* Strip the leading registry host (first path segment, e.g. ghcr.io) and
     re-prefix with the pivot registry. A repository with no "/" (bare image
     name) is kept whole under the new registry. */}}
{{- $parts := splitList "/" $repo -}}
{{- $rest := $repo -}}
{{- if gt (len $parts) 1 -}}
{{- $rest = rest $parts | join "/" -}}
{{- end -}}
{{- printf "%s/%s:%s" (trimSuffix "/" $registry) $rest $tag -}}
{{- else -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}
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
