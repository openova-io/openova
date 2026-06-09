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
The Secret name CNPG reconciles a managed role's password FROM. When a
binding omits `passwordSecret`, default to `<instance>-<owner>` so a
CNPG-bootstrapped role Secret (or a same-named consumer Secret) lines up.
*/}}
{{- define "bp-postgres.roleSecretName" -}}
{{- $instance := include "bp-postgres.instanceName" .ctx -}}
{{- if .db.passwordSecret -}}
{{- .db.passwordSecret -}}
{{- else -}}
{{- printf "%s-%s" $instance .db.owner -}}
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
