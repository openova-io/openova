{{/*
Expand the name of the chart.
*/}}
{{- define "bp-dmz-vcluster.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name. Used as the K8s resource name root.
*/}}
{{- define "bp-dmz-vcluster.fullname" -}}
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
Common labels — Catalyst convention.
*/}}
{{- define "bp-dmz-vcluster.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "bp-dmz-vcluster.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-dmz-vcluster
openova.io/product: dmz-vcluster
{{- end }}

{{/*
Selector labels — used by the host-namespace NetworkPolicy to target
all DMZ vCluster pods. The `vcluster.loft.sh/managed-by:
<vclusterName>` label is the upstream vCluster's canonical pod label.
*/}}
{{- define "bp-dmz-vcluster.podSelectorLabel" -}}
vcluster.loft.sh/managed-by: {{ .Values.dmz.vclusterName | quote }}
{{- end }}

{{/*
Image-tag fail-fast helper. Per docs/INVIOLABLE-PRINCIPLES.md #4a
empty `:tag` MUST fail the helm template render — we never want
floating `:latest` shipping into production. Honours
`.Values.global.imageRegistry` rewrite when set.
*/}}
{{- define "bp-dmz-vcluster.image" -}}
{{- $tag := .Values.dmz.vcluster.image.tag -}}
{{- if not $tag -}}
{{- fail "bp-dmz-vcluster: .Values.dmz.vcluster.image.tag is empty — SHA-pinned image required (CI populates this) per docs/INVIOLABLE-PRINCIPLES.md #4a" -}}
{{- end -}}
{{- $repo := .Values.dmz.vcluster.image.repository -}}
{{- $globalRegistry := .Values.global.imageRegistry | default "" -}}
{{- if ne $globalRegistry "" -}}
{{- $parts := splitList "/" $repo -}}
{{- $rest := slice $parts 1 -}}
{{- printf "%s/%s:%s" $globalRegistry (join "/" $rest) $tag -}}
{{- else -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}
{{- end }}

{{/*
Host namespace fail-fast — every resource the chart renders is
namespaced into this. Refusing to render at all when empty prevents
the chart from accidentally landing in the `default` namespace.
*/}}
{{- define "bp-dmz-vcluster.hostNamespace" -}}
{{- $ns := .Values.dmz.hostNamespace -}}
{{- if not $ns -}}
{{- fail "bp-dmz-vcluster: .Values.dmz.hostNamespace is empty — set per Sovereign overlay (canonical: 'dmz' for single-DMZ Sovereigns)" -}}
{{- end -}}
{{- $ns -}}
{{- end }}
