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
{{- default (include "bp-k8s-ws-proxy.workloadName" .) .Values.k8sWsProxy.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.k8sWsProxy.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Canonical workload name. Per qa-loop iter-7 Fix #39, the test matrix
(TC-236, TC-237) and the catalyst-api shells/issue handler reference
the DaemonSet + Service + ClusterRole(Binding) by the canonical short
name `k8s-ws-proxy`, independent of release name. Operator can
override via .Values.k8sWsProxy.workloadName when running multiple
proxies in the same namespace (rare; one per Sovereign per
ADR-0001 §11).
*/}}
{{- define "bp-k8s-ws-proxy.workloadName" -}}
{{- default "k8s-ws-proxy" .Values.k8sWsProxy.workloadName -}}
{{- end }}

{{/*
Image-tag fail-fast — INVIOLABLE-PRINCIPLES #4a.

#4885 (Refs #3379 #4563): honour `global.imageRegistry`. The repository is
the full ghcr.io path (ghcr.io/openova-io/openova/k8s-ws-proxy). The
self-sovereign-cutover step-07 registry pivot patches this HR's
spec.values.global.imageRegistry = registry.<sovereign-fqdn>; when set, swap
ONLY the leading registry host so the image resolves to
registry.<fqdn>/openova-io/openova/k8s-ws-proxy — the path the cutover
step-03 harbor-prewarm natively pushes the image to. Without the override the
literal ghcr.io ref is unreachable under the step-08 600s deny-egress hold
(anonymous-token fetch blocked → 401 ImagePullBackOff) and the fresh-pull
proof FATALs (cutoverComplete stays false). Empty/unset = byte-identical
ghcr.io ref (pre-cutover default). Mirrors the sibling
bp-openova-flow-server.image helper.
*/}}
{{- define "bp-k8s-ws-proxy.image" -}}
{{- $tag := .Values.k8sWsProxy.image.tag -}}
{{- if not $tag -}}
{{- fail "bp-k8s-ws-proxy: .Values.k8sWsProxy.image.tag is empty — SHA-pinned image required (CI populates this)" -}}
{{- end -}}
{{- $repo := .Values.k8sWsProxy.image.repository -}}
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
