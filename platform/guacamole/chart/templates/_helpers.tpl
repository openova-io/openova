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
{{/*
Registry-rewrite helper (#4885). Given an openova-io "<host>/<path>" image
repository + a validated tag, prepend .Values.global.imageRegistry in place of
the leading registry host when it is set (self-sovereign-cutover step-07 pivot),
else emit the ghcr.io ref verbatim. Mirrors the continuum.image pattern so the
default byte-identically stays on ghcr.io pre-cutover.
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

{{- define "bp-guacamole.guacdImage" -}}
{{- $tag := .Values.guacamole.guacd.image.tag -}}
{{- if not $tag -}}
{{- fail "bp-guacamole: .Values.guacamole.guacd.image.tag is empty — SHA-pinned image required (CI populates this)" -}}
{{- end -}}
{{- include "bp-guacamole.registryImage" (dict "repo" .Values.guacamole.guacd.image.repository "tag" $tag "root" $) -}}
{{- end }}

{{- define "bp-guacamole.webappImage" -}}
{{- $tag := .Values.guacamole.webapp.image.tag -}}
{{- if not $tag -}}
{{- fail "bp-guacamole: .Values.guacamole.webapp.image.tag is empty — SHA-pinned image required (CI populates this)" -}}
{{- end -}}
{{- include "bp-guacamole.registryImage" (dict "repo" .Values.guacamole.webapp.image.repository "tag" $tag "root" $) -}}
{{- end }}

{{/*
SSO mode (#5358). `header` (default) = bp-oidc-gate authorization-code
flow in front + guacamole-auth-header behind; `openid` = LEGACY native
guacamole-auth-sso-openid implicit flow (fragment-replay-broken upstream
— see values.yaml `guacamole.sso`). Any other value fails the render.
*/}}
{{- define "bp-guacamole.ssoMode" -}}
{{- $sso := .Values.guacamole.sso | default dict -}}
{{- $mode := $sso.mode | default "header" -}}
{{- if not (has $mode (list "header" "openid")) -}}
{{- fail (printf "bp-guacamole: invalid guacamole.sso.mode %q — must be \"header\" (bp-oidc-gate front, default) or \"openid\" (legacy native implicit flow)" $mode) -}}
{{- end -}}
{{- $mode -}}
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
https://<httproute.hostname>/guacamole/.

#3150 (2026-06-10): the redirect_uri MUST carry the `/guacamole/`
context path, NOT the bare host root. The Apache Guacamole WAR is
`guacamole.war`, so Tomcat serves the webapp under the `/guacamole/`
context path — the container root `/` returns a Tomcat 404 ROOT-context
page. The guacamole-auth-sso-openid extension implements the OIDC
*implicit* flow: it sends `redirect_uri` to Keycloak, Keycloak issues the
id_token and redirects the browser back to that exact URI with the token
in the URL fragment (`#id_token=…`). With the prior bare-`/` default the
browser landed on `https://guacamole.<fqdn>/#id_token=…` → Tomcat 404
ROOT page → the SSO completed at Keycloak but the token never reached the
webapp (the extension never ran), so the operator never landed logged-in.
Appending `/guacamole/` lands the fragment on the actual webapp root so
the extension consumes the token and the operator lands authenticated.
The KC `guacamole` client's `redirectUris` is a wildcard
(`https://guacamole.<fqdn>/*`, configmap-sovereign-realm.yaml) so it
already accepts the `/guacamole/` path — only this chart-side default was
wrong. Operators can still override via `oidc.redirectURI` for a
non-default context path.
*/}}
{{- define "bp-guacamole.oidcRedirectURI" -}}
{{- if .Values.guacamole.oidc.redirectURI -}}
{{- .Values.guacamole.oidc.redirectURI -}}
{{- else -}}
{{- printf "https://%s/guacamole/" (include "bp-guacamole.host" .) -}}
{{- end -}}
{{- end }}
