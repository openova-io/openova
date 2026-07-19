{{/*
Expand the name of the chart.
*/}}
{{- define "bp-stalwart-tenant.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "bp-stalwart-tenant.fullname" -}}
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
Common labels — required by docs/BLUEPRINT-AUTHORING.md §14 and by the
Catalyst projector to track resources back to the Blueprint.
*/}}
{{- define "bp-stalwart-tenant.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "bp-stalwart-tenant.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-stalwart-tenant
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "bp-stalwart-tenant.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-stalwart-tenant.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "bp-stalwart-tenant.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "bp-stalwart-tenant.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
ConfigMap name (Stalwart bootstrap config.toml).
*/}}
{{- define "bp-stalwart-tenant.configMapName" -}}
{{- printf "%s-config" (include "bp-stalwart-tenant.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
DNS-records ConfigMap name (MX/SPF/DKIM/DMARC required by the Org admin).
Surfaced by the unified-rbac console UI.
*/}}
{{- define "bp-stalwart-tenant.dnsRecordsConfigMapName" -}}
{{- printf "%s-dns-records-required" (include "bp-stalwart-tenant.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Tenant primary mail domain — resolves the Organization's mail FQDN regardless of
the values shape the orchestrator supplies. Three accepted shapes (in
priority order):

  1. `tenant.domain: <string>`     — newer per-tenant overlay schema
                                     (#898 forward-looking convention).
  2. `domain: { primary: <str>,    — current chart canonical schema
                mode: ... }`         (matches blueprint.yaml configSchema
                                     and post-#897 catalyst-api emission).
  3. `domain: <string>`            — LEGACY shape emitted by pre-#897
                                     catalyst-api builds (live on
                                     otech103: `values.domain: acme.omani.works`).
                                     Kept for back-compat so a tenant
                                     pinned to chart 0.1.1 reconciles
                                     cleanly even when the catalyst-api
                                     image upgrade lags behind.

Returns "" (empty) when none of the three resolves, which is the
smoke-render-safe default (CI's blueprint-release.yaml renders with
empty values; per-template gates downstream skip on empty host).

Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode) — no fallback
to a literal domain anywhere in this file.
*/}}
{{- define "bp-stalwart-tenant.tenantDomain" -}}
{{- $tenant := .Values.tenant | default dict -}}
{{- $d := .Values.domain -}}
{{- if $tenant.domain -}}
{{- $tenant.domain -}}
{{- else if kindIs "map" $d -}}
{{- if $d.primary -}}{{- $d.primary -}}{{- end -}}
{{- else if kindIs "string" $d -}}
{{- $d -}}
{{- end -}}
{{- end -}}

{{/*
Tenant domain mode — `free-subdomain` (default) or `byo`. Mirrors the
shape resolution of tenantDomain so legacy string-shape `domain` falls
back to the values-yaml `domain.mode` default of "free-subdomain".
*/}}
{{- define "bp-stalwart-tenant.tenantMode" -}}
{{- $tenant := .Values.tenant | default dict -}}
{{- $d := .Values.domain -}}
{{- if $tenant.mode -}}
{{- $tenant.mode -}}
{{- else if and (kindIs "map" $d) $d.mode -}}
{{- $d.mode -}}
{{- else -}}
free-subdomain
{{- end -}}
{{- end -}}

{{/*
Webmail host. If `ingress.webmail.host` is explicitly set, use it. Otherwise
compose `mail.<tenant-domain>`. Per docs/INVIOLABLE-PRINCIPLES.md #4 —
no hardcoded base domain.

Returns empty string when both are unset (smoke-render-safe). The
per-template render gates keep Ingress/HTTPRoute objects from emitting
when host is empty, so a default-values render produces a syntactically
valid (but non-functional) manifest tree.
*/}}
{{- define "bp-stalwart-tenant.webmailHost" -}}
{{- $domain := include "bp-stalwart-tenant.tenantDomain" . -}}
{{- if .Values.ingress.webmail.host -}}
{{- .Values.ingress.webmail.host -}}
{{- else if $domain -}}
{{- printf "mail.%s" $domain -}}
{{- end -}}
{{- end -}}

{{/*
Webmail (SnappyMail) Service name (#4307). Distinct from the Stalwart
`-web` Service so the `mail.<slug>` HTTPRoute can front the SPA while the
Stalwart server's admin/JMAP listener stays on `-web`.
*/}}
{{- define "bp-stalwart-tenant.webmailFullname" -}}
{{- printf "%s-webmail" (include "bp-stalwart-tenant.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Webmail HTTPRoute backend Service name (#4307). Resolution order:
  1. explicit `ingress.webmail.backendService` (overlay pin).
  2. the SnappyMail webmail Service when `webmail.enabled` (the default —
     so `mail.<slug>/` serves the webmail login UI, not the Stalwart 404).
  3. the Stalwart `-web` Service (back-compat when webmail is disabled).
*/}}
{{- define "bp-stalwart-tenant.webmailRouteBackend" -}}
{{- if .Values.ingress.webmail.backendService -}}
{{- .Values.ingress.webmail.backendService -}}
{{- else if .Values.webmail.enabled -}}
{{- include "bp-stalwart-tenant.webmailFullname" . -}}
{{- else -}}
{{- printf "%s-web" (include "bp-stalwart-tenant.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/*
Webmail HTTPRoute backend port (#4307). Mirrors webmailRouteBackend:
  1. explicit `ingress.webmail.backendPort` (>0) wins.
  2. the SnappyMail HTTP port when webmail.enabled.
  3. the Stalwart `-web` http port (back-compat).
*/}}
{{- define "bp-stalwart-tenant.webmailRoutePort" -}}
{{- if gt (int .Values.ingress.webmail.backendPort) 0 -}}
{{- .Values.ingress.webmail.backendPort -}}
{{- else if .Values.webmail.enabled -}}
{{- .Values.webmail.containerPort -}}
{{- else -}}
{{- .Values.service.web.ports.http -}}
{{- end -}}
{{- end -}}

{{/*
Stalwart webadmin route host (#4307). Composes `mailadmin.<tenant-domain>`
when `webmail.adminRoute.host` is unset; an explicit value wins. Empty when
the tenant domain is unresolved (smoke-render-safe).
*/}}
{{- define "bp-stalwart-tenant.adminHost" -}}
{{- $domain := include "bp-stalwart-tenant.tenantDomain" . -}}
{{- if .Values.webmail.adminRoute.host -}}
{{- .Values.webmail.adminRoute.host -}}
{{- else if $domain -}}
{{- printf "mailadmin.%s" $domain -}}
{{- end -}}
{{- end -}}

{{/*
Effective SnappyMail webmail image reference (#4307). Honours
`global.imageRegistry` rewrite; SHA-pinned via digest when present.
*/}}
{{- define "bp-stalwart-tenant.webmailImage" -}}
{{- $g := .Values.global | default dict -}}
{{- $img := .Values.webmail.image -}}
{{- $reg := default $img.registry $g.imageRegistry -}}
{{- if $img.digest -}}
{{- printf "%s/%s@%s" $reg $img.repository $img.digest -}}
{{- else -}}
{{- printf "%s/%s:%s" $reg $img.repository $img.tag -}}
{{- end -}}
{{- end -}}

{{/*
Effective image reference. Honours `global.imageRegistry` rewrite for
post-handover Sovereign Harbor proxy-cache (ADR-0001 §11.5). Always
SHA-pinned via digest when present (Inviolable Principle #4 / #4a).
*/}}
{{- define "bp-stalwart-tenant.image" -}}
{{- $g := .Values.global | default dict -}}
{{- $img := .Values.stalwart.image -}}
{{- $reg := default $img.registry $g.imageRegistry -}}
{{- if $img.digest -}}
{{- printf "%s/%s@%s" $reg $img.repository $img.digest -}}
{{- else -}}
{{- printf "%s/%s:%s" $reg $img.repository $img.tag -}}
{{- end -}}
{{- end -}}

{{/*
Render the Stalwart admin password env var reference. Always pulled from
the Secret named `.Values.admin.secretName`, key ADMIN_PASSWORD. Centralised
so deployment.yaml and the setup Job pin to the same key.
*/}}
{{- define "bp-stalwart-tenant.adminPasswordEnv" -}}
- name: ADMIN_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ .Values.admin.secretName | quote }}
      key: ADMIN_PASSWORD
{{- end -}}

{{/*
Required-values gate (runtime-only). Skipped during default-values smoke
render (CI blueprint-release smoke step) so the artifact still builds
without operator overlay; per-tenant overlays MUST set these for an
actual deploy and the runtime template paths fail closed via
`{{- if X }}` gates and Helm's resource-validation when manifests would
be unusable. The fail-fast pattern from bp-newapi is intentionally NOT
used here — it breaks the platform-wide blueprint-release.yaml smoke
gate (issue #181 follow-up; see CI failure on bp-openclaw 2026-05-04).

Operators wiring this in a per-tenant overlay should still see explicit
fail messages from per-template gates (admin-externalsecret.yaml,
oidc-externalsecret.yaml) when their own knobs are inconsistent.
*/}}
{{- define "bp-stalwart-tenant.assertRequired" -}}
{{- /* Smoke-render-safe: no fail() at template root. */ -}}
{{- end -}}
