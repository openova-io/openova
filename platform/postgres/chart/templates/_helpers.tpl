{{/*
bp-postgres common helpers (ADR-0010, #3188).
*/}}

{{/* The data-instance (CNPG Cluster) name. */}}
{{- define "bp-postgres.instanceName" -}}
{{- default "postgres" .Values.instance.name -}}
{{- end -}}

{{/* The namespace the Cluster + CNPG Secrets live in. */}}
{{- define "bp-postgres.namespace" -}}
{{- default .Release.Namespace .Values.instance.namespace -}}
{{- end -}}

{{/* Full CNPG image ref. */}}
{{- define "bp-postgres.imageRef" -}}
{{- printf "%s:%s" .Values.instance.imageName (.Values.instance.pgVersion | toString) -}}
{{- end -}}

{{/*
Sanitise an arbitrary string into an RFC 1123 DNS-subdomain-safe name
fragment so it can be used in a k8s resource name. PostgreSQL identifiers
(role/database names) legally contain underscores (e.g. `openova_flow`),
but k8s object names may NOT — a Secret named `shared-pg-c-openova_flow`
is rejected with `metadata.name: Invalid value … must be lowercase RFC
1123`. This lowercases and replaces every run of non-[a-z0-9-] with a
single `-`, then trims leading/trailing `-`. (#3375 hw133 — bp-postgres-
shared-c was Stalled on exactly this, which blocked the whole
bp-catalyst-platform dependsOn chain.)
*/}}
{{- define "bp-postgres.k8sName" -}}
{{- regexReplaceAll "[^a-z0-9-]+" (lower (toString .)) "-" | trimAll "-" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
The Secret name CNPG reconciles a managed role's password FROM. When a
binding omits `passwordSecret`, default to `<instance>-<owner>` so a
CNPG-bootstrapped role Secret (or a same-named consumer Secret) lines up.
The owner segment is sanitised (k8sName) so an owner like `openova_flow`
yields the valid `…-openova-flow` instead of an underscore'd invalid name.
*/}}
{{- define "bp-postgres.roleSecretName" -}}
{{- $instance := include "bp-postgres.instanceName" .ctx -}}
{{- if .db.passwordSecret -}}
{{- .db.passwordSecret -}}
{{- else -}}
{{- printf "%s-%s" $instance (include "bp-postgres.k8sName" .db.owner) -}}
{{- end -}}
{{- end -}}

{{/* Common labels applied to every resource the chart emits. */}}
{{- define "bp-postgres.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: bp-postgres
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-postgres
catalyst.openova.io/data-instance: {{ include "bp-postgres.instanceName" . | quote }}
{{- end -}}
