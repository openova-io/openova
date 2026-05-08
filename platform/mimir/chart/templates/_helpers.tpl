{{- /*
_helpers.tpl — template name + label helpers for Catalyst overlay
templates. Mirrors the patterns used by sibling bp-* charts so an
operator browsing the catalog sees uniform naming.

Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode) every name is
derived from .Release / .Chart so a re-named release rolls through
the manifests without per-template edits.
*/}}

{{/* Truncated, lowercased name for resources. */}}
{{- define "bp-mimir.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Fully-qualified name = release-name + chart-name. Capped at 63 chars
     per K8s DNS-1123 label rules. */}}
{{- define "bp-mimir.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* Standard label set. Includes selector labels + chart metadata. */}}
{{- define "bp-mimir.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "bp-mimir.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/family: observability
{{- end -}}

{{/* Selector subset — kept stable across upgrades because Service
     selectors and Deployment label selectors are immutable. */}}
{{- define "bp-mimir.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-mimir.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
