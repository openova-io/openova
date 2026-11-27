{{/*
Common labels
*/}}
{{- define "axon.labels" -}}
app.kubernetes.io/name: axon
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-axon
{{- end }}

{{/*
Selector labels
*/}}
{{- define "axon.selectorLabels" -}}
app.kubernetes.io/name: axon
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
The one home of the credential in OpenBao (tier B, mount mounted/): the
SecretProviderClass mounts its key document, and Axon writes each refreshed
document back to it (AXON_CREDENTIAL_KV_PATH) as service account axon, whose
OpenBao role axon-credential (bp-openbao) grants create, update and read there.
*/}}
{{- define "axon.credentialPath" -}}
mounted/data/{{ .Release.Namespace }}/sa/axon/model-credential/e1
{{- end }}

{{/*
The memory limit in MiB: 512 + rssActiveMi x maxActive + rssParkedMi x maxParked,
rounded up. Each run, active or parked, holds one child process; rssActiveMi and
rssParkedMi are the figures of spike AIP-020 (docs/ledger/plan/spikes/AIP-020.md).
*/}}
{{- define "axon.memoryMi" -}}
{{- $a := .Values.axon -}}
{{- int (ceil (addf 512 (mulf $a.rssActiveMi $a.maxActive) (mulf $a.rssParkedMi $a.maxParked))) -}}
{{- end }}

{{/*
Every value the chart reads is checked here; a wrong one fails the render with
the name of the value.
*/}}
{{- define "axon.validate" -}}
{{- if not (kindIs "bool" .Values.secondary) }}
{{- fail (printf "secondary must be true or false, not %q" (toString .Values.secondary)) }}
{{- end }}
{{- if not .Values.image.repository }}
{{- fail "image.repository is required" }}
{{- end }}
{{- if not (regexMatch "^sha256:[0-9a-f]{64}$" (toString .Values.image.digest)) }}
{{- fail (printf "image.digest must be sha256:<64 hex digits>, not %q" (toString .Values.image.digest)) }}
{{- end }}
{{- if or (not (kindIs "slice" .Values.imagePullSecrets)) (not .Values.imagePullSecrets) }}
{{- fail "imagePullSecrets must name at least one pull secret: the image is on a registry the node has no login for" }}
{{- end }}
{{- range .Values.imagePullSecrets }}
{{- if or (not (kindIs "map" .)) (not .name) }}
{{- fail "imagePullSecrets: every entry must be {name: <pull secret>}" }}
{{- end }}
{{- end }}
{{- range $k := list "maxActive" "maxParked" }}
{{- if not (regexMatch "^[1-9][0-9]*$" (toString (index $.Values.axon $k))) }}
{{- fail (printf "axon.%s must be a positive whole number, not %q" $k (toString (index $.Values.axon $k))) }}
{{- end }}
{{- end }}
{{- range $k := list "rssActiveMi" "rssParkedMi" }}
{{- if not (regexMatch "^[0-9]+([.][0-9]+)?$" (toString (index $.Values.axon $k))) }}
{{- fail (printf "axon.%s must be a number of MiB, not %q" $k (toString (index $.Values.axon $k))) }}
{{- end }}
{{- end }}
{{- if or (not (kindIs "map" .Values.axon.modelMap)) (not .Values.axon.modelMap) }}
{{- fail "axon.modelMap must map at least one model name to a model" }}
{{- end }}
{{- range $name, $model := .Values.axon.modelMap }}
{{- if or (not (kindIs "string" $model)) (eq $model "") }}
{{- fail (printf "axon.modelMap.%s must be a model name" $name) }}
{{- end }}
{{- end }}
{{- if not .Values.openbao.address }}
{{- fail "openbao.address is required" }}
{{- end }}
{{- $hosts := .Values.egress.hosts }}
{{- if or (not (kindIs "slice" $hosts)) (ne (len $hosts) 2) }}
{{- fail "egress.hosts must list exactly two host names: the provider's API host and its token host" }}
{{- end }}
{{- range $hosts }}
{{- if not (regexMatch "^([a-z0-9]([-a-z0-9]*[a-z0-9])?[.])+[a-z]{2,}$" (toString .)) }}
{{- fail (printf "egress.hosts: %q is not a DNS name" (toString .)) }}
{{- end }}
{{- end }}
{{- end }}
