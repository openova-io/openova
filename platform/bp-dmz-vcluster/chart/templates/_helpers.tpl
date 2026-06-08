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

#2940 Pillar 5 anti-tether: composes the image registry host from
`.Values.global.registryMirror` (default "harbor.openova.io", single
source of truth) + the registry-relative `.Values.dmzVcluster.image.repository`.
Backward-compat: a repository already host-qualified (first "/"-segment
contains "." or ":") is used verbatim and the mirror is NOT prepended.
*/}}
{{- define "bp-dmz-vcluster.image" -}}
{{- $tag := .Values.dmzVcluster.image.tag -}}
{{- if not $tag -}}
{{- fail "bp-dmz-vcluster: .Values.dmzVcluster.image.tag is empty — SHA-pinned image required (CI populates this) per docs/INVIOLABLE-PRINCIPLES.md #4a" -}}
{{- end -}}
{{- $repo := .Values.dmzVcluster.image.repository -}}
{{- if not $repo -}}
{{- fail "bp-dmz-vcluster: .Values.dmzVcluster.image.repository is empty — must point at proxy-ghcr/loft-sh/vcluster (registry-relative; global.registryMirror supplies the host) per CLAUDE.md MIRROR-EVERYTHING" -}}
{{- end -}}
{{- $firstSeg := first (splitList "/" $repo) -}}
{{- if or (contains "." $firstSeg) (contains ":" $firstSeg) -}}
{{- printf "%s:%s" $repo $tag -}}
{{- else -}}
{{- $mirror := .Values.global.registryMirror | default "harbor.openova.io" -}}
{{- if not $mirror -}}
{{- fail "bp-dmz-vcluster: .Values.global.registryMirror is empty and .Values.dmzVcluster.image.repository is host-less — set one or the other (#2940)" -}}
{{- end -}}
{{- printf "%s/%s:%s" $mirror $repo $tag -}}
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
