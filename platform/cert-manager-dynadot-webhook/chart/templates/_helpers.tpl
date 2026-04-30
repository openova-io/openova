{{/*
Expand the name of the chart.
*/}}
{{- define "bp-cert-manager-dynadot-webhook.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name. Used as the K8s resource name root for the
Deployment, Service, ServiceAccount, etc.
*/}}
{{- define "bp-cert-manager-dynadot-webhook.fullname" -}}
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
{{- define "bp-cert-manager-dynadot-webhook.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "bp-cert-manager-dynadot-webhook.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-cert-manager-dynadot-webhook
catalyst.openova.io/component: cert-manager-webhook
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "bp-cert-manager-dynadot-webhook.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-cert-manager-dynadot-webhook.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "bp-cert-manager-dynadot-webhook.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "bp-cert-manager-dynadot-webhook.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Selfsigned Issuer name — used to bootstrap the CA cert used to sign the
webhook's serving cert. cert-manager owns the chain entirely; no
external CA touches the webhook traffic.
*/}}
{{- define "bp-cert-manager-dynadot-webhook.selfSignedIssuer" -}}
{{ printf "%s-selfsign" (include "bp-cert-manager-dynadot-webhook.fullname" .) }}
{{- end }}

{{/*
CA Issuer name — issues the actual leaf serving cert from the CA
secret. Two issuers are required because cert-manager's Certificate CR
cannot self-sign and chain in one step.
*/}}
{{- define "bp-cert-manager-dynadot-webhook.rootCAIssuer" -}}
{{ printf "%s-ca" (include "bp-cert-manager-dynadot-webhook.fullname" .) }}
{{- end }}

{{/*
CA Certificate name (and the secret it materializes).
*/}}
{{- define "bp-cert-manager-dynadot-webhook.rootCACertificate" -}}
{{ printf "%s-ca" (include "bp-cert-manager-dynadot-webhook.fullname" .) }}
{{- end }}

{{/*
Serving Certificate name (and the secret the Deployment mounts).
*/}}
{{- define "bp-cert-manager-dynadot-webhook.servingCertificate" -}}
{{ printf "%s-tls" (include "bp-cert-manager-dynadot-webhook.fullname" .) }}
{{- end }}
