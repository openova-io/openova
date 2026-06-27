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

#4563 (Refs #3379): honour `global.imageRegistry`. The repository is the
full ghcr.io path (ghcr.io/openova-io/openova/openova-flow-server). When
the post-cutover registry pivot sets global.imageRegistry =
registry.<sovereign-fqdn>, swap ONLY the leading registry host so the
image resolves to registry.<fqdn>/openova-io/openova/openova-flow-server
— the path the cutover step-03 harbor-prewarm natively pushes the image
to. Without the override the literal ghcr.io ref is unreachable under the
600s deny-egress hold (anonymous-token fetch blocked → 401 ImagePullBackOff)
and the step-08 fresh-pull proof FATALs. Mirrors the catalyst chart's
api-deployment.yaml / ui-deployment.yaml `global.imageRegistry` wrapper.
*/}}
{{- define "bp-openova-flow-server.image" -}}
{{- $tag := .Values.flowServer.image.tag -}}
{{- if not $tag -}}
{{- fail "bp-openova-flow-server: .Values.flowServer.image.tag is empty — SHA-pinned image required (CI populates this)" -}}
{{- end -}}
{{- $repo := .Values.flowServer.image.repository -}}
{{- $registry := "" -}}
{{- if .Values.global -}}
{{- $registry = .Values.global.imageRegistry | default "" -}}
{{- end -}}
{{- if $registry -}}
{{/* Strip the leading registry host (first path segment, e.g. ghcr.io)
     and re-prefix with the pivot registry. Splits on "/" and drops the
     first segment; the remainder is rejoined with "/". A repository with
     no "/" (bare image name) is kept whole under the new registry. */}}
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
