{{/*
Expand the name of the chart.
*/}}
{{- define "bp-netbird.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name. Used as the K8s resource name root.
*/}}
{{- define "bp-netbird.fullname" -}}
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
{{- define "bp-netbird.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "bp-netbird.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-netbird
{{- end }}

{{/*
Selector labels (no chart/version — stable across upgrades).
*/}}
{{- define "bp-netbird.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-netbird.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Per-component selector labels — management vs signal vs coturn pods are
distinct Deployments and need separate selector sets.
*/}}
{{- define "bp-netbird.managementSelectorLabels" -}}
{{ include "bp-netbird.selectorLabels" . }}
catalyst.openova.io/component: management
{{- end }}

{{- define "bp-netbird.signalSelectorLabels" -}}
{{ include "bp-netbird.selectorLabels" . }}
catalyst.openova.io/component: signal
{{- end }}

{{- define "bp-netbird.coturnSelectorLabels" -}}
{{ include "bp-netbird.selectorLabels" . }}
catalyst.openova.io/component: coturn
{{- end }}

{{/*
Image-tag fail-fast helpers. Per docs/INVIOLABLE-PRINCIPLES.md #4a
empty `:tag` MUST fail the helm template render — we never want
floating `:latest` shipping into production. Honours
`.Values.global.imageRegistry` rewrite when set (Sovereign-local Harbor
proxy_cache; same pattern catalyst chart uses).
*/}}
{{- define "bp-netbird.imageRef" -}}
{{- $repo := .repo -}}
{{- $tag := .tag -}}
{{- $globalRegistry := .globalRegistry | default "" -}}
{{- if not $tag -}}
{{- fail (printf "bp-netbird: %s image tag is empty — SHA-pinned image required (CI populates this) per docs/INVIOLABLE-PRINCIPLES.md #4a" .label) -}}
{{- end -}}
{{- if ne $globalRegistry "" -}}
{{- $parts := splitList "/" $repo -}}
{{- $rest := slice $parts 1 -}}
{{- printf "%s/%s:%s" $globalRegistry (join "/" $rest) $tag -}}
{{- else -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}
{{- end }}

{{- define "bp-netbird.managementImage" -}}
{{- include "bp-netbird.imageRef" (dict "label" "management" "repo" .Values.netbird.management.image.repository "tag" .Values.netbird.management.image.tag "globalRegistry" .Values.global.imageRegistry) -}}
{{- end }}

{{- define "bp-netbird.signalImage" -}}
{{- include "bp-netbird.imageRef" (dict "label" "signal" "repo" .Values.netbird.signal.image.repository "tag" .Values.netbird.signal.image.tag "globalRegistry" .Values.global.imageRegistry) -}}
{{- end }}

{{- define "bp-netbird.coturnImage" -}}
{{- include "bp-netbird.imageRef" (dict "label" "coturn" "repo" .Values.netbird.coturn.image.repository "tag" .Values.netbird.coturn.image.tag "globalRegistry" .Values.global.imageRegistry) -}}
{{- end }}

{{/*
OIDC issuer fail-fast — NetBird won't authenticate without it.
*/}}
{{- define "bp-netbird.oidcIssuer" -}}
{{- $iss := .Values.netbird.oidc.issuer -}}
{{- if not $iss -}}
{{- fail "bp-netbird: .Values.netbird.oidc.issuer is empty — set to https://keycloak.<sovereign-fqdn>/realms/<realm>" -}}
{{- end -}}
{{- $iss -}}
{{- end }}

{{/*
Management domain fail-fast — every NetBird endpoint URL derives from it.
*/}}
{{- define "bp-netbird.domain" -}}
{{- $d := .Values.netbird.management.domain -}}
{{- if not $d -}}
{{- fail "bp-netbird: .Values.netbird.management.domain is empty — set to netbird.<sovereign-fqdn>" -}}
{{- end -}}
{{- $d -}}
{{- end }}

{{/*
HTTPRoute hostname — falls back to management.domain when not set.
*/}}
{{- define "bp-netbird.host" -}}
{{- if .Values.netbird.httproute.hostname -}}
{{- .Values.netbird.httproute.hostname -}}
{{- else -}}
{{- include "bp-netbird.domain" . -}}
{{- end -}}
{{- end }}

{{/*
Default redirectURI — when operator left it blank we derive
https://<management.domain>/ since that is the canonical landing
page NetBird's web UI serves.
*/}}
{{- define "bp-netbird.oidcRedirectURI" -}}
{{- if .Values.netbird.oidc.redirectURI -}}
{{- .Values.netbird.oidc.redirectURI -}}
{{- else -}}
{{- printf "https://%s/" (include "bp-netbird.host" .) -}}
{{- end -}}
{{- end }}

{{/*
OIDC audience — defaults to clientId when not explicitly set.
*/}}
{{- define "bp-netbird.oidcAudience" -}}
{{- if .Values.netbird.oidc.audience -}}
{{- .Values.netbird.oidc.audience -}}
{{- else -}}
{{- .Values.netbird.oidc.clientId -}}
{{- end -}}
{{- end }}

{{/*
TURN realm — defaults to management.domain when not explicitly set.
*/}}
{{- define "bp-netbird.turnRealm" -}}
{{- if .Values.netbird.coturn.realm -}}
{{- .Values.netbird.coturn.realm -}}
{{- else -}}
{{- include "bp-netbird.domain" . -}}
{{- end -}}
{{- end }}
