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
Per-component selector labels — guacd vs webapp pods are distinct
Deployments and need separate selector sets.
*/}}
{{- define "bp-guacamole.guacdSelectorLabels" -}}
{{ include "bp-guacamole.selectorLabels" . }}
catalyst.openova.io/component: guacd
{{- end }}

{{- define "bp-guacamole.webappSelectorLabels" -}}
{{ include "bp-guacamole.selectorLabels" . }}
catalyst.openova.io/component: webapp
{{- end }}

{{/*
Per-component resource names. Per qa-loop iter-7 Fix #39, the test
matrix (TC-228 / TC-230 / TC-245 / TC-246) and the catalyst-api's
shells/issue handler reference these resources by their canonical
short names — `guacd` and `guacamole-server` — independent of the
release name. Operator can override via .Values.guacamole.guacd.name
and .Values.guacamole.webapp.name when running multiple Guacamole
deployments in the same namespace (rare; ADR-0001 §11 caps at one
Guacamole per Sovereign).
*/}}
{{- define "bp-guacamole.guacdName" -}}
{{- default "guacd" .Values.guacamole.guacd.name -}}
{{- end }}

{{- define "bp-guacamole.webappName" -}}
{{- default "guacamole-server" .Values.guacamole.webapp.name -}}
{{- end }}

{{- define "bp-guacamole.recordingsName" -}}
{{- default "guacamole-recordings" .Values.guacamole.recordings.pvcName -}}
{{- end }}

{{/*
Image-tag fail-fast helpers. Per docs/INVIOLABLE-PRINCIPLES.md #4a
empty `:tag` MUST fail the helm template render — we never want
floating `:latest` shipping into production.
*/}}
{{- define "bp-guacamole.guacdImage" -}}
{{- $tag := .Values.guacamole.guacd.image.tag -}}
{{- if not $tag -}}
{{- fail "bp-guacamole: .Values.guacamole.guacd.image.tag is empty — SHA-pinned image required (CI populates this)" -}}
{{- end -}}
{{- printf "%s:%s" .Values.guacamole.guacd.image.repository $tag -}}
{{- end }}

{{- define "bp-guacamole.webappImage" -}}
{{- $tag := .Values.guacamole.webapp.image.tag -}}
{{- if not $tag -}}
{{- fail "bp-guacamole: .Values.guacamole.webapp.image.tag is empty — SHA-pinned image required (CI populates this)" -}}
{{- end -}}
{{- printf "%s:%s" .Values.guacamole.webapp.image.repository $tag -}}
{{- end }}

{{/*
OIDC issuer fail-fast — Guacamole won't authenticate without it.
*/}}
{{- define "bp-guacamole.oidcIssuer" -}}
{{- $iss := .Values.guacamole.oidc.issuer -}}
{{- if not $iss -}}
{{- fail "bp-guacamole: .Values.guacamole.oidc.issuer is empty — set to https://keycloak.<sovereign-fqdn>/realms/<realm>" -}}
{{- end -}}
{{- $iss -}}
{{- end }}

{{/*
HTTPRoute hostname fail-fast — Cilium Gateway needs a concrete host.
*/}}
{{- define "bp-guacamole.host" -}}
{{- $h := .Values.guacamole.httproute.hostname -}}
{{- if not $h -}}
{{- fail "bp-guacamole: .Values.guacamole.httproute.hostname is empty — set to guacamole.<sovereign-fqdn>" -}}
{{- end -}}
{{- $h -}}
{{- end }}

{{/*
Default redirectURI — when operator left it blank we derive
https://<httproute.hostname>/ since that is the canonical landing
page Guacamole serves.
*/}}
{{- define "bp-guacamole.oidcRedirectURI" -}}
{{- if .Values.guacamole.oidc.redirectURI -}}
{{- .Values.guacamole.oidc.redirectURI -}}
{{- else -}}
{{- printf "https://%s/" (include "bp-guacamole.host" .) -}}
{{- end -}}
{{- end }}
