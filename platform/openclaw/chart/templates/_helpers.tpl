{{/*
Expand the name of the chart.
*/}}
{{- define "bp-openclaw.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "bp-openclaw.fullname" -}}
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
{{- define "bp-openclaw.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "bp-openclaw.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-openclaw
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "bp-openclaw.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-openclaw.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "bp-openclaw.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "bp-openclaw.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
ConfigMap name (per-user pod template).
*/}}
{{- define "bp-openclaw.podTemplateConfigMapName" -}}
{{- printf "%s-pod-template" (include "bp-openclaw.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Tenant namespace — falls back to .Release.Namespace if tenant.namespace
is unset, but we prefer an explicit value so the controller's RBAC scope
matches the operator's intent.
*/}}
{{- define "bp-openclaw.tenantNamespace" -}}
{{- default .Release.Namespace .Values.tenant.namespace }}
{{- end }}

{{/*
Placeholder-rejection assertions. The chart ships placeholder defaults
(see values.yaml) so `helm template` smoke-renders cleanly in CI
(mirroring bp-self-sovereign-cutover's pattern); these assertions fail
loudly only when the placeholder is left in *and* the chart is being
installed for real (detected by the `bp-openclaw.assertNoPlaceholders`
flag in values, which Flux overlays set to `true` after they've
provided real values). The smoke-render path leaves the flag at its
default `false` so CI passes.

Operators MUST set every value in the "Required at install time" block
of values.yaml; the deploy-time validation hook in
catalyst-platform's reconciler also checks these against the rendered
HelmRelease.
*/}}
{{- define "bp-openclaw.assertNoPlaceholders" -}}
{{- if .Values.assertNoPlaceholders }}
{{- if eq .Values.controller.image.tag "0.1.0-placeholder" }}
{{- fail "controller.image.tag is still the placeholder — overlay must supply a SHA-pinned tag (Inviolable Principle 4)" }}
{{- end }}
{{- if eq .Values.perUserPod.image.tag "0.1.0-placeholder" }}
{{- fail "perUserPod.image.tag is still the placeholder — overlay must supply a SHA-pinned tag (Inviolable Principle 4)" }}
{{- end }}
{{- if eq .Values.keycloak.realmURL "https://keycloak.example.local/realms/example" }}
{{- fail "keycloak.realmURL is still the placeholder — overlay must supply the SME-vcluster Keycloak realm URL" }}
{{- end }}
{{- if eq .Values.newapi.baseURL "https://newapi.example.local" }}
{{- fail "newapi.baseURL is still the placeholder — overlay must supply the NewAPI customer-facing hostname" }}
{{- end }}
{{- if eq .Values.tenant.namespace "sme-example" }}
{{- fail "tenant.namespace is still the placeholder — overlay must supply the SME tenant namespace" }}
{{- end }}
{{- if eq .Values.ingress.host "openclaw.example.local" }}
{{- fail "ingress.host is still the placeholder — overlay must supply the controller public hostname" }}
{{- end }}
{{- end }}
{{- end }}
