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
repository and its digest, prepend .Values.global.imageRegistry in place of
the leading registry host when it is set (self-sovereign-cutover step-07 pivot),
else emit the ghcr.io ref verbatim. The image is referenced by digest only; a
digest that is not sha256:<64 hex digits> fails the render.
Usage: {{ include "bp-guacamole.registryImage" (dict "repo" <repo> "digest" <digest> "what" <value path> "root" $) }}
*/}}
{{- define "bp-guacamole.registryImage" -}}
{{- $repo := .repo -}}
{{- $digest := .digest -}}
{{- if not (regexMatch "^sha256:[0-9a-f]{64}$" $digest) -}}
{{- fail (printf "bp-guacamole: %s is %q, not sha256:<64 hex digits>" .what $digest) -}}
{{- end -}}
{{- $globalRegistry := .root.Values.global.imageRegistry | default "" -}}
{{- if ne $globalRegistry "" -}}
{{- printf "%s/%s@%s" $globalRegistry (join "/" (slice (splitList "/" $repo) 1)) $digest -}}
{{- else -}}
{{- printf "%s@%s" $repo $digest -}}
{{- end -}}
{{- end }}

{{/*
Cross-region role (#5358, ADR-0033 decision A21), from access.crossRegion.
`primary` (default) = this region runs the access controller and
self-registers the Sovereign's one Application of the access plane;
`secondary` = it does neither. Any other value fails the render.
*/}}
{{- define "bp-guacamole.crossRegionRole" -}}
{{- $cr := (.Values.access | default dict).crossRegion | default dict -}}
{{- $role := $cr.role | default "primary" -}}
{{- if not (has $role (list "primary" "secondary")) -}}
{{- fail (printf "bp-guacamole: invalid access.crossRegion.role %q — must be \"primary\" (runs the access controller) or \"secondary\" (does not)" $role) -}}
{{- end -}}
{{- $role -}}
{{- end }}

{{/*
Mesh-secondary predicate (#5358). Emits "true" when access.crossRegion is on
and this region is the ClusterMesh SECONDARY, which does not self-register the
Application (templates/application-cr.yaml), so the Sovereign has one instance
card. Empty string otherwise.
*/}}
{{- define "bp-guacamole.isMeshSecondary" -}}
{{- $cr := (.Values.access | default dict).crossRegion | default dict -}}
{{- if and $cr.enabled (eq (include "bp-guacamole.crossRegionRole" .) "secondary") -}}
true
{{- end -}}
{{- end }}

{{/*
Region rule of the access plane (decision A21). Emits "true" when
access.enabled and this region runs the controller: access.crossRegion is off,
or this region is the primary. The controller's Deployment, service account and
roles render only then (bp-guacamole.controllerRendered); its Service and the
writer lock render with access.enabled in every region.
*/}}
{{- define "bp-guacamole.accessActive" -}}
{{- $a := .Values.access | default dict -}}
{{- if and $a.enabled (not (include "bp-guacamole.isMeshSecondary" .)) -}}
true
{{- end -}}
{{- end }}

{{/*
Labels of the access controller's objects. app.kubernetes.io/name is
access-controller, not the chart name, so it is a label of its own.
*/}}
{{- define "bp-guacamole.controllerSelectorLabels" -}}
app.kubernetes.io/name: access-controller
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "bp-guacamole.controllerLabels" -}}
{{ include "bp-guacamole.controllerSelectorLabels" . }}
app.kubernetes.io/part-of: bp-guacamole
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
catalyst.openova.io/blueprint: bp-guacamole
{{- end }}

{{/*
The controller renders where bp-guacamole.accessActive says, once CI has
written the digest of its image (access-controller-build.yaml, after the first
build of the image). Until then the release holds the Service and the writer
lock only, and no target becomes Ready, which check ACC-G13 reports.
*/}}
{{- define "bp-guacamole.controllerRendered" -}}
{{- if and (include "bp-guacamole.accessActive" .) .Values.access.controller.image.digest -}}
true
{{- end -}}
{{- end }}
