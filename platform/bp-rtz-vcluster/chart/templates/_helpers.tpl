{{/*
Expand the name of the chart.
*/}}
{{- define "bp-rtz-vcluster.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "bp-rtz-vcluster.fullname" -}}
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

{{- define "bp-rtz-vcluster.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "bp-rtz-vcluster.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-rtz-vcluster
catalyst.openova.io/topology-role: {{ .Values.rtzVcluster.role | quote }}
{{- end }}

{{/*
Image fail-fast helper. Per docs/INVIOLABLE-PRINCIPLES.md #4a.

#2940 Pillar 5 anti-tether: composes the image registry host from
`.Values.global.registryMirror` (default "harbor.openova.io", single
source of truth) + the registry-relative `.Values.rtzVcluster.image.repository`.
Backward-compat: a repository already host-qualified (first "/"-segment
contains "." or ":") is used verbatim and the mirror is NOT prepended.
*/}}
{{- define "bp-rtz-vcluster.image" -}}
{{- $tag := .Values.rtzVcluster.image.tag -}}
{{- if not $tag -}}
{{- fail "bp-rtz-vcluster: .Values.rtzVcluster.image.tag is empty — SHA-pinned image required (CI populates this) per docs/INVIOLABLE-PRINCIPLES.md #4a" -}}
{{- end -}}
{{- $repo := .Values.rtzVcluster.image.repository -}}
{{- if not $repo -}}
{{- fail "bp-rtz-vcluster: .Values.rtzVcluster.image.repository is empty — must point at proxy-ghcr/loft-sh/vcluster (registry-relative; global.registryMirror supplies the host) per CLAUDE.md MIRROR-EVERYTHING" -}}
{{- end -}}
{{- $firstSeg := first (splitList "/" $repo) -}}
{{- if or (contains "." $firstSeg) (contains ":" $firstSeg) -}}
{{- printf "%s:%s" $repo $tag -}}
{{- else -}}
{{- $mirror := .Values.global.registryMirror | default "harbor.openova.io" -}}
{{- if not $mirror -}}
{{- fail "bp-rtz-vcluster: .Values.global.registryMirror is empty and .Values.rtzVcluster.image.repository is host-less — set one or the other (#2940)" -}}
{{- end -}}
{{- printf "%s/%s:%s" $mirror $repo $tag -}}
{{- end -}}
{{- end }}

{{- define "bp-rtz-vcluster.hostNamespace" -}}
{{- $ns := .Values.rtzVcluster.hostNamespace -}}
{{- if not $ns -}}
{{- fail "bp-rtz-vcluster: .Values.rtzVcluster.hostNamespace is empty — canonical 'rtz' (see docs/SOVEREIGN-MULTI-REGION-DOD.md A4)" -}}
{{- end -}}
{{- $ns -}}
{{- end }}
