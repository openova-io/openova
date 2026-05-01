{{/*
Expand the name of the chart.
*/}}
{{- define "bp-newapi.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "bp-newapi.fullname" -}}
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
{{- define "bp-newapi.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "bp-newapi.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-newapi
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "bp-newapi.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-newapi.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "bp-newapi.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "bp-newapi.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
ConfigMap name (channel + policy config).
*/}}
{{- define "bp-newapi.configMapName" -}}
{{- printf "%s-config" (include "bp-newapi.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Channel attestation gate — refuses to render if any enabled channel
lacks attestation. Compliance posture defined in
platform/newapi/README.md and blueprint.yaml configSchema.
*/}}
{{- define "bp-newapi.assertChannelAttestation" -}}
{{- range $idx, $ch := .Values.channels }}
{{- if not $ch.attestation }}
{{- fail (printf "channel[%d] (%s): missing required attestation block — see platform/newapi/README.md compliance posture" $idx (default "<unnamed>" $ch.name)) }}
{{- end }}
{{- if not $ch.attestation.kind }}
{{- fail (printf "channel[%d] (%s): attestation.kind is required (one of: in-cluster, commercial-contract, byok)" $idx (default "<unnamed>" $ch.name)) }}
{{- end }}
{{- if eq $ch.attestation.kind "commercial-contract" }}
{{- if not $ch.attestation.accountId }}
{{- fail (printf "channel[%d] (%s): commercial-contract attestation requires accountId" $idx (default "<unnamed>" $ch.name)) }}
{{- end }}
{{- if not $ch.attestation.contractRef }}
{{- fail (printf "channel[%d] (%s): commercial-contract attestation requires contractRef" $idx (default "<unnamed>" $ch.name)) }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
