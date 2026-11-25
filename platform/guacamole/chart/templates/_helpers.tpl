{{/*
Expand the name of the chart.
*/}}
{{- define "bp-guacamole.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name. Used as the K8s resource name root.
*/}}
{{- define "bp-guacamole.fullname" -}}
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
{{- define "bp-guacamole.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "bp-guacamole.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-guacamole
{{- end }}

{{/*
Selector labels (no chart/version — stable across upgrades).
*/}}
{{- define "bp-guacamole.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-guacamole.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Registry-rewrite helper (#4885). Given an openova-io "<host>/<path>" image
repository + a validated tag, prepend .Values.global.imageRegistry in place of
the leading registry host when it is set (self-sovereign-cutover step-07 pivot),
else emit the ghcr.io ref verbatim. Mirrors the continuum.image pattern so the
default byte-identically stays on ghcr.io pre-cutover. No template calls it
since ACC-102 removed the chart's images; the access controller's image
(ACC-103) is the next caller.
Usage: {{ include "bp-guacamole.registryImage" (dict "repo" <repo> "tag" <tag> "root" $) }}
*/}}
{{- define "bp-guacamole.registryImage" -}}
{{- $repo := .repo -}}
{{- $tag := .tag -}}
{{- $globalRegistry := .root.Values.global.imageRegistry | default "" -}}
{{- if ne $globalRegistry "" -}}
{{- printf "%s/%s:%s" $globalRegistry (join "/" (slice (splitList "/" $repo) 1)) $tag -}}
{{- else -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}
{{- end }}

{{/*
Cross-region role (#5358, ADR-0033 decision A21). `primary` (default) = this
region self-registers the Sovereign's one Application of the access plane;
`secondary` = it does not. Any other value fails the render.
*/}}
{{- define "bp-guacamole.crossRegionRole" -}}
{{- $cr := .Values.crossRegion | default dict -}}
{{- $role := $cr.role | default "primary" -}}
{{- if not (has $role (list "primary" "secondary")) -}}
{{- fail (printf "bp-guacamole: invalid crossRegion.role %q — must be \"primary\" (self-registers the Application) or \"secondary\" (does not)" $role) -}}
{{- end -}}
{{- $role -}}
{{- end }}

{{/*
Mesh-secondary predicate (#5358). Emits "true" when crossRegion is on and this
region is the ClusterMesh SECONDARY, which does not self-register the
Application (templates/application-cr.yaml), so the Sovereign has one instance
card. Empty string otherwise.
*/}}
{{- define "bp-guacamole.isMeshSecondary" -}}
{{- $cr := .Values.crossRegion | default dict -}}
{{- if and $cr.enabled (eq (include "bp-guacamole.crossRegionRole" .) "secondary") -}}
true
{{- end -}}
{{- end }}
