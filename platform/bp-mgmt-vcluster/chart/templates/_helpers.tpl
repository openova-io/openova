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
:latest shipping into production.

#2940 Pillar 5 anti-tether: composes the image registry host from
`.Values.global.registryMirror` (default "harbor.openova.io", the single
source of truth) + the registry-relative `.Values.mgmtVcluster.image.repository`.
Backward-compat: if repository is ALREADY host-qualified (its first
"/"-segment contains "." or ":", e.g. the pre-#2940 literal
"harbor.openova.io/proxy-ghcr/loft-sh/vcluster"), it is used verbatim and
the mirror is NOT prepended — so existing per-Sovereign overlays that set
a full host-qualified repository keep working.
*/}}
{{- define "bp-mgmt-vcluster.image" -}}
{{- $tag := .Values.mgmtVcluster.image.tag -}}
{{- if not $tag -}}
{{- fail "bp-mgmt-vcluster: .Values.mgmtVcluster.image.tag is empty — SHA-pinned image required (CI populates this) per docs/INVIOLABLE-PRINCIPLES.md #4a" -}}
{{- end -}}
{{- $repo := .Values.mgmtVcluster.image.repository -}}
{{- if not $repo -}}
{{- fail "bp-mgmt-vcluster: .Values.mgmtVcluster.image.repository is empty — must point at proxy-ghcr/loft-sh/vcluster (registry-relative; global.registryMirror supplies the host) per CLAUDE.md MIRROR-EVERYTHING" -}}
{{- end -}}
{{- $firstSeg := first (splitList "/" $repo) -}}
{{- if or (contains "." $firstSeg) (contains ":" $firstSeg) -}}
{{- /* repository is already host-qualified — honour it verbatim */ -}}
{{- printf "%s:%s" $repo $tag -}}
{{- else -}}
{{- $mirror := .Values.global.registryMirror | default "harbor.openova.io" -}}
{{- if not $mirror -}}
{{- fail "bp-mgmt-vcluster: .Values.global.registryMirror is empty and .Values.mgmtVcluster.image.repository is host-less — set one or the other (#2940)" -}}
{{- end -}}
{{- printf "%s/%s:%s" $mirror $repo $tag -}}
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
