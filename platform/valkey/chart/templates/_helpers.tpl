{{/*
bp-valkey Catalyst overlay helpers (#3370 Contexts).
*/}}

{{/* The instance identity — the Helm release IS the instance. */}}
{{- define "bp-valkey.instanceName" -}}
{{- .Release.Name -}}
{{- end -}}

{{/*
The wire host consumers dial. The upstream bitnami valkey subchart
exposes the primary at `<fullname>-primary`; with releaseName `valkey`
the chart-name dedupe yields `valkey-primary` (the address bp-newapi's
REDIS_CONN_STRING default already uses). Operator-overridable via
.Values.contextsService.host per Inviolable Principle #4.
*/}}
{{- define "bp-valkey.serviceHost" -}}
{{- if .Values.contextsService.host -}}
{{- .Values.contextsService.host -}}
{{- else -}}
{{- printf "%s-primary.%s.svc.cluster.local" .Release.Name .Release.Namespace -}}
{{- end -}}
{{- end -}}

{{/* Common labels for Catalyst overlay resources. */}}
{{- define "bp-valkey.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: bp-valkey
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-valkey
{{- end -}}
