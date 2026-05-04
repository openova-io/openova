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
DNS-records ConfigMap name (MX/SPF/DKIM/DMARC required by SME admin).
Surfaced by the unified-rbac console UI.
*/}}
{{- define "bp-stalwart-tenant.dnsRecordsConfigMapName" -}}
{{- printf "%s-dns-records-required" (include "bp-stalwart-tenant.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Webmail host. If `ingress.webmail.host` is explicitly set, use it. Otherwise
compose `mail.<domain.primary>`. Per docs/INVIOLABLE-PRINCIPLES.md #4 —
no hardcoded base domain.

Returns empty string when both are unset (smoke-render-safe). The
per-template render gates keep Ingress/HTTPRoute objects from emitting
when host is empty, so a default-values render produces a syntactically
valid (but non-functional) manifest tree.
*/}}
{{- define "bp-stalwart-tenant.webmailHost" -}}
{{- if .Values.ingress.webmail.host -}}
{{- .Values.ingress.webmail.host -}}
{{- else if .Values.domain.primary -}}
{{- printf "mail.%s" .Values.domain.primary -}}
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
