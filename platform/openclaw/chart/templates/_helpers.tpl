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
Render-time assertions for required values. Fail loudly when the
controller or per-user pod template is enabled but missing a value the
operator MUST supply (Inviolable Principle 4 — never hardcode, but ALSO
never silently default to a placeholder that would make the runtime
malfunction).
*/}}
{{- define "bp-openclaw.assertRequired" -}}
{{- if .Values.controller.enabled }}
{{- if not .Values.keycloak.realmURL }}
{{- fail "keycloak.realmURL is required when controller.enabled=true (full SME-vcluster Keycloak realm issuer URL, e.g. https://keycloak.<sme-domain>/realms/<realm>)" }}
{{- end }}
{{- if not .Values.keycloak.clientSecretName }}
{{- fail "keycloak.clientSecretName is required when controller.enabled=true (ExternalSecret name carrying OIDC_CLIENT_SECRET)" }}
{{- end }}
{{- if not .Values.tenant.namespace }}
{{- fail "tenant.namespace is required when controller.enabled=true (SME tenant namespace where per-user pods are spawned and newapi-key-{uuid} Secrets are read)" }}
{{- end }}
{{- if not .Values.newapi.baseURL }}
{{- fail "newapi.baseURL is required when controller.enabled=true (NewAPI customer-facing hostname, e.g. https://newapi.<otech-fqdn>)" }}
{{- end }}
{{- if not .Values.controller.image.tag }}
{{- fail "controller.image.tag is required when controller.enabled=true (SHA-pinned tag — never use floating tags per Inviolable Principle 4)" }}
{{- end }}
{{- if not .Values.perUserPod.image.tag }}
{{- fail "perUserPod.image.tag is required when controller.enabled=true (SHA-pinned tag — never use floating tags per Inviolable Principle 4)" }}
{{- end }}
{{- if .Values.ingress.enabled }}
{{- if not .Values.ingress.host }}
{{- fail "ingress.host is required when ingress.enabled=true (controller public hostname, e.g. openclaw.<sme-domain>)" }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
