{{/*
Helpers for the sandbox-controller chart (Wave 1).

Conventions match products/catalyst/chart/templates/controllers/_helpers.tpl
so future maintainers can move the chart into the catalyst umbrella without
renaming a single resource.
*/}}

{{/* Canonical chart name (used as Deployment / SA / ClusterRole /
     ClusterRoleBinding name). Honors nameOverride / fullnameOverride
     per the upstream Helm convention. */}}
{{- define "sandbox.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default "sandbox-controller" .Values.nameOverride -}}
{{- printf "%s" $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/* Common labels applied to every resource. */}}
{{- define "sandbox.labels" -}}
app.kubernetes.io/name: sandbox-controller
app.kubernetes.io/component: controller
app.kubernetes.io/part-of: catalyst
app.kubernetes.io/managed-by: {{ .Release.Service | default "Helm" }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
catalyst.openova.io/controller: sandbox
{{- end -}}

{{/* Selector labels (subset that does not change between releases). */}}
{{- define "sandbox.selectorLabels" -}}
app.kubernetes.io/name: sandbox-controller
app.kubernetes.io/component: controller
catalyst.openova.io/controller: sandbox
{{- end -}}

{{/* Resolve the controller image reference. Fails-fast when tag is
     empty so the operator catches the misconfig at `helm template`
     time rather than from a CrashLoopBackoff. */}}
{{- define "sandbox.image" -}}
{{- $tag := .Values.image.tag | default "" -}}
{{- if eq $tag "" -}}
{{- fail "platform/sandbox/chart values.image.tag is empty — CI must stamp a SHA before render. Per docs/INVIOLABLE-PRINCIPLES.md #4a no :latest is permitted." -}}
{{- end -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}
