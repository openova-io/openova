{{/*
Expand the name of the chart.
*/}}
{{- define "bp-dmz-vcluster.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name.
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
catalyst.openova.io/topology-role: {{ .Values.dmzVcluster.role | quote }}
{{- end }}

{{/*
Image fail-fast helper. Per docs/INVIOLABLE-PRINCIPLES.md #4a.
*/}}
{{- define "bp-dmz-vcluster.image" -}}
{{- $tag := .Values.dmzVcluster.image.tag -}}
{{- if not $tag -}}
{{- fail "bp-dmz-vcluster: .Values.dmzVcluster.image.tag is empty — SHA-pinned image required (CI populates this) per docs/INVIOLABLE-PRINCIPLES.md #4a" -}}
{{- end -}}
{{- $repo := .Values.dmzVcluster.image.repository -}}
{{- if not $repo -}}
{{- fail "bp-dmz-vcluster: .Values.dmzVcluster.image.repository is empty — must point at harbor.openova.io/proxy-ghcr/loft-sh/vcluster (or per-Sovereign Harbor) per CLAUDE.md MIRROR-EVERYTHING" -}}
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
Host-namespace fail-fast.
*/}}
{{- define "bp-dmz-vcluster.hostNamespace" -}}
{{- $ns := .Values.dmzVcluster.hostNamespace -}}
{{- if not $ns -}}
{{- fail "bp-dmz-vcluster: .Values.dmzVcluster.hostNamespace is empty — canonical 'dmz' (see docs/SOVEREIGN-MULTI-REGION-DOD.md A4)" -}}
{{- end -}}
{{- $ns -}}
{{- end }}
