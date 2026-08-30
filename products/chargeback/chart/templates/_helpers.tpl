{{/* workload name stem — "chargeback" (the bp- prefix is a chart/catalog concern) */}}
{{- define "chargeback.name" -}}
{{- default "chargeback" .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* fully-qualified name */}}
{{- define "chargeback.fullname" -}}
{{- $name := include "chargeback.name" . -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/* common labels */}}
{{- define "chargeback.labels" -}}
app.kubernetes.io/name: {{ include "chargeback.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-chargeback
{{- end -}}

{{/* selector labels */}}
{{- define "chargeback.selectorLabels" -}}
app.kubernetes.io/name: {{ include "chargeback.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- /*
Hook-resource labels: managed-by=flux (single key). The kyverno
flux-managed Enforce policy denies a hook Job without it → the hook fails
→ the release rolls back (the bp-newapi hookLabels idiom, hw133 #3374).
*/ -}}
{{- define "chargeback.hookLabels" -}}
app.kubernetes.io/name: {{ include "chargeback.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: flux
catalyst.openova.io/blueprint: bp-chargeback
{{- end -}}

{{- /*
chargeback.image — repository:tag honoring the cutover step-07
global.imageRegistry pivot seam (#4885/#4892): when set, strip the leading
registry host off the repository and re-prefix with the pivot registry so
the image pulls from the local Harbor under the deny-egress hold.
*/ -}}
{{- define "chargeback.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- include "chargeback.pivotImage" (dict "repo" .Values.image.repository "tag" $tag "root" .) -}}
{{- end -}}

{{- /* generic pivot: (dict "repo" <repo[:tag]> "tag" <tag|""> "root" $) */ -}}
{{- define "chargeback.pivotImage" -}}
{{- $repo := .repo -}}
{{- $tag := .tag -}}
{{- $registry := "" -}}
{{- if .root.Values.global -}}
{{- $registry = .root.Values.global.imageRegistry | default "" -}}
{{- end -}}
{{- if $registry -}}
{{- $parts := splitList "/" $repo -}}
{{- $rest := $repo -}}
{{- if gt (len $parts) 1 -}}
{{- $rest = rest $parts | join "/" -}}
{{- end -}}
{{- if $tag -}}
{{- printf "%s/%s:%s" (trimSuffix "/" $registry) $rest $tag -}}
{{- else -}}
{{- printf "%s/%s" (trimSuffix "/" $registry) $rest -}}
{{- end -}}
{{- else -}}
{{- if $tag -}}
{{- printf "%s:%s" $repo $tag -}}
{{- else -}}
{{- $repo -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* ServiceAccount name */}}
{{- define "chargeback.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "chargeback.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- /*
chargeback.hostname — the public HTTPRoute host. Explicit hostnames[0]
wins; else chargeback.<sovereignFqdn>; else "" (fail-closed — the route
renders nothing, Inviolable #4).
*/ -}}
{{- define "chargeback.hostname" -}}
{{- if .Values.httpRoute.hostnames -}}
{{- index .Values.httpRoute.hostnames 0 -}}
{{- else if .Values.sovereignFqdn -}}
{{- printf "chargeback.%s" .Values.sovereignFqdn -}}
{{- end -}}
{{- end -}}

{{- /* PUBLIC_URL: explicit value, else https://<hostname>, else "" */ -}}
{{- define "chargeback.publicUrl" -}}
{{- if .Values.config.publicUrl -}}
{{- .Values.config.publicUrl -}}
{{- else -}}
{{- $host := include "chargeback.hostname" . -}}
{{- if $host -}}
{{- printf "https://%s" $host -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* CNPG cluster + secret names */}}
{{- define "chargeback.cnpgClusterName" -}}
{{- printf "%s-pg" (include "chargeback.fullname" .) -}}
{{- end -}}

{{- define "chargeback.dsnSecretName" -}}
{{- default (printf "%s-db-dsn" (include "chargeback.fullname" .)) .Values.cnpg.dsnSecretName -}}
{{- end -}}

{{- define "chargeback.appKeySecretName" -}}
{{- default (printf "%s-app-key" (include "chargeback.fullname" .)) .Values.encryptionKey.secretName -}}
{{- end -}}
