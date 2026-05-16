{{/*
Expand the name of the chart.
*/}}
{{- define "bp-mgmt-vcluster.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name.
*/}}
{{- define "bp-mgmt-vcluster.fullname" -}}
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
{{- define "bp-mgmt-vcluster.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "bp-mgmt-vcluster.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-mgmt-vcluster
catalyst.openova.io/topology-role: {{ .Values.mgmtVcluster.role | quote }}
{{- end }}

{{/*
Image fail-fast helper. Per docs/INVIOLABLE-PRINCIPLES.md #4a empty
:tag MUST fail the helm template render — we never want floating
:latest shipping into production. Honours .Values.global.imageRegistry
rewrite when set.
*/}}
{{- define "bp-mgmt-vcluster.image" -}}
{{- $tag := .Values.mgmtVcluster.image.tag -}}
{{- if not $tag -}}
{{- fail "bp-mgmt-vcluster: .Values.mgmtVcluster.image.tag is empty — SHA-pinned image required (CI populates this) per docs/INVIOLABLE-PRINCIPLES.md #4a" -}}
{{- end -}}
{{- $repo := .Values.mgmtVcluster.image.repository -}}
{{- if not $repo -}}
{{- fail "bp-mgmt-vcluster: .Values.mgmtVcluster.image.repository is empty — must point at harbor.openova.io/proxy-ghcr/loft-sh/vcluster (or per-Sovereign Harbor) per CLAUDE.md MIRROR-EVERYTHING" -}}
{{- end -}}
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
Host-namespace fail-fast. Every resource the chart renders is
namespaced into this. Refusing to render when empty prevents the
chart from accidentally landing in `default`.
*/}}
{{- define "bp-mgmt-vcluster.hostNamespace" -}}
{{- $ns := .Values.mgmtVcluster.hostNamespace -}}
{{- if not $ns -}}
{{- fail "bp-mgmt-vcluster: .Values.mgmtVcluster.hostNamespace is empty — canonical 'mgmt' for single-MGMT Sovereigns (see docs/SOVEREIGN-MULTI-REGION-DOD.md A4)" -}}
{{- end -}}
{{- $ns -}}
{{- end }}

{{/*
NodeSelector — emits map<key,value> only when both regionLabelKey
and regionLabelValue are non-empty. Empty value = no pin (smoke
test path).
*/}}
{{- define "bp-mgmt-vcluster.nodeSelector" -}}
{{- $k := .Values.mgmtVcluster.nodeSelector.regionLabelKey -}}
{{- $v := .Values.mgmtVcluster.nodeSelector.regionLabelValue -}}
{{- if and $k $v -}}
{{ $k }}: {{ $v | quote }}
{{- end -}}
{{- end }}
