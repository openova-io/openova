{{/*
Expand the name of the chart.
*/}}
{{- define "bp-wordpress-tenant.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "bp-wordpress-tenant.fullname" -}}
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
{{- define "bp-wordpress-tenant.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "bp-wordpress-tenant.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-wordpress-tenant
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "bp-wordpress-tenant.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-wordpress-tenant.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "bp-wordpress-tenant.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "bp-wordpress-tenant.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
WordPress image reference, with optional `global.imageRegistry` rewrite
for Sovereign Harbor proxy-cache. Returns
`{registry/}repository:tag@digest` so consumers SHA-pin to the manifest-
list digest published on Docker Hub.
*/}}
{{- define "bp-wordpress-tenant.wordpressImage" -}}
{{- $reg := .Values.global.imageRegistry | default "" -}}
{{- $repo := .Values.wordpress.image.repository -}}
{{- $tag := .Values.wordpress.image.tag -}}
{{- $digest := .Values.wordpress.image.digest -}}
{{- if $reg -}}
{{- printf "%s/%s:%s@%s" $reg $repo $tag $digest -}}
{{- else -}}
{{- printf "%s:%s@%s" $repo $tag $digest -}}
{{- end -}}
{{- end -}}

{{/*
Resolved ingress host. Templates `wordpress.<smeDomain>` when
`ingress.host` is empty; otherwise returns the operator-supplied host
verbatim. `smeDomain` is required when `ingress.host` is empty.
*/}}
{{- define "bp-wordpress-tenant.ingressHost" -}}
{{- if .Values.ingress.host -}}
{{- .Values.ingress.host -}}
{{- else -}}
{{- $sme := required ".Values.smeDomain or .Values.ingress.host MUST be set (no sensible default per INVIOLABLE-PRINCIPLES #4)." .Values.smeDomain -}}
{{- printf "wordpress.%s" $sme -}}
{{- end -}}
{{- end -}}

{{/*
CNPG cluster namespace — defaults to .Release.Namespace if the
operator left `database.cluster.namespace` empty.
*/}}
{{- define "bp-wordpress-tenant.cnpgNamespace" -}}
{{- default .Release.Namespace .Values.database.cluster.namespace -}}
{{- end -}}

{{/*
CNPG-emitted application Secret name (`<cluster>-app`). CNPG synthesises
this Secret from the `Cluster.spec.bootstrap.initdb.owner` field at
install time.
*/}}
{{- define "bp-wordpress-tenant.cnpgAppSecret" -}}
{{- printf "%s-app" .Values.database.cnpgClusterName -}}
{{- end -}}

{{/*
CNPG-emitted read-write Service hostname. CNPG synthesises this Service
from the Cluster CR; suffix is `-rw` per the CNPG operator convention.
*/}}
{{- define "bp-wordpress-tenant.cnpgRwHost" -}}
{{- printf "%s-rw.%s.svc.cluster.local" .Values.database.cnpgClusterName (include "bp-wordpress-tenant.cnpgNamespace" .) -}}
{{- end -}}
