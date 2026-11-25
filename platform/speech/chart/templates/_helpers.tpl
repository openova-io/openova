{{/* The name of the chart, and the name of every object of the release. */}}
{{- define "bp-speech.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "bp-speech.fullname" -}}
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

{{- define "bp-speech.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "bp-speech.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-speech
{{- end }}

{{- define "bp-speech.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-speech.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/* "true" when the release renders its workloads: home and enabled are both true. */}}
{{- define "bp-speech.serving" -}}
{{- if and (eq (toString .Values.home) "true") (eq (toString .Values.enabled) "true") }}true{{ end }}
{{- end }}

{{/* "hf" when the model comes from the model hub, "oci" when it comes from a model image. */}}
{{- define "bp-speech.kind" -}}
{{- if hasPrefix "hf://" (toString (.Values.model | default dict).source) }}hf{{ else }}oci{{ end }}
{{- end }}

{{/*
Where the model sits in the weights volume: a Hugging Face cache (HF_HUB_CACHE
/models/hf) with one model repository, whose refs/main names its one snapshot.
The server reads it with the hub switched off, so "main" always resolves to the
pinned snapshot. An hf:// model keeps its own repository id and its revision; a
model image is the repository oci/model, and its snapshot is the first 40
characters of the image digest.
*/}}
{{- define "bp-speech.repo" -}}
{{- $source := toString .Values.model.source }}
{{- if eq (include "bp-speech.kind" .) "hf" }}
{{- trimPrefix "hf://" $source }}
{{- else }}oci/model{{ end }}
{{- end }}

{{- define "bp-speech.repoDir" -}}
{{- printf "/models/hf/models--%s" (include "bp-speech.repo" . | replace "/" "--") }}
{{- end }}

{{- define "bp-speech.snapshot" -}}
{{- if eq (include "bp-speech.kind" .) "hf" }}
{{- toString .Values.model.revision }}
{{- else }}
{{- regexReplaceAll "^.*@sha256:([a-f0-9]{40}).*$" (toString .Values.model.source) "${1}" }}
{{- end }}
{{- end }}

{{/* The server's CPU threads: resources.limits.cpu in whole CPUs, at least 1. */}}
{{- define "bp-speech.threads" -}}
{{- $cpu := toString ((.Values.resources | default dict).limits | default dict).cpu }}
{{- $n := 1 }}
{{- if hasSuffix "m" $cpu }}
{{- $n = div (atoi (trimSuffix "m" $cpu)) 1000 }}
{{- else if $cpu }}
{{- $n = int (float64 $cpu) }}
{{- end }}
{{- max 1 $n }}
{{- end }}

{{/*
bp-speech.validate fails the render on what the configSchema of blueprint.yaml
refuses, and on the rules the schema language cannot say. It renders nothing.
*/}}
{{- define "bp-speech.validate" -}}
{{- $sourceRe := "^(oci://[a-z0-9./_-]+@sha256:[a-f0-9]{64}|hf://[A-Za-z0-9._-]+/[A-Za-z0-9._-]+)$" }}
{{- $nameRe := "^[a-z0-9][a-z0-9.-]{0,62}$" }}
{{- range $key := list "home" "enabled" }}
{{- if not (kindIs "bool" (get $.Values $key)) }}
{{- fail (printf "bp-speech: %s must be true or false" $key) }}
{{- end }}
{{- end }}
{{- $m := .Values.model }}
{{- if not (kindIs "map" $m) }}
{{- fail "bp-speech: model is required" }}
{{- end }}
{{- range $k, $_ := $m }}
{{- if not (has $k (list "source" "revision" "servedName")) }}
{{- fail (printf "bp-speech: model.%s is not a parameter of this chart" $k) }}
{{- end }}
{{- end }}
{{- $source := toString $m.source }}
{{- if not (regexMatch $sourceRe $source) }}
{{- fail (printf "bp-speech: model.source %q is neither oci://<host>/<path>@sha256:<64 hex> nor hf://<owner>/<name>" $source) }}
{{- end }}
{{- if hasPrefix "hf://" $source }}
{{- if or (contains "--" $source) (contains ".." $source) }}
{{- fail (printf "bp-speech: model.source %q: a model hub repository id contains neither -- nor .." $source) }}
{{- end }}
{{- if not (regexMatch "^[a-f0-9]{40}$" (toString $m.revision)) }}
{{- fail (printf "bp-speech: model.revision must be the 40-character commit of %s" $source) }}
{{- end }}
{{- else if $m.revision }}
{{- fail "bp-speech: model.revision is only for an hf:// source" }}
{{- end }}
{{- if not (regexMatch $nameRe (toString $m.servedName)) }}
{{- fail (printf "bp-speech: model.servedName %q must match %s" (toString $m.servedName) $nameRe) }}
{{- end }}
{{- with .Values.cache }}
{{- range $k, $_ := . }}
{{- if not (has $k (list "sizeGi" "storageClass")) }}
{{- fail (printf "bp-speech: cache.%s is not a parameter of this chart" $k) }}
{{- end }}
{{- end }}
{{- if not (and (or (kindIs "float64" .sizeGi) (kindIs "int64" .sizeGi) (kindIs "int" .sizeGi)) (eq (float64 .sizeGi) (float64 (int .sizeGi))) (ge (int .sizeGi) 10)) }}
{{- fail "bp-speech: cache.sizeGi must be a whole number of at least 10" }}
{{- end }}
{{- if not (kindIs "string" (.storageClass | default "")) }}
{{- fail "bp-speech: cache.storageClass must be a string" }}
{{- end }}
{{- end }}
{{- $s := .Values.startup }}
{{- if not (kindIs "map" $s) }}
{{- fail "bp-speech: startup is required" }}
{{- end }}
{{- range $k, $_ := $s }}
{{- if ne $k "loadSeconds" }}
{{- fail (printf "bp-speech: startup.%s is not a parameter of this chart" $k) }}
{{- end }}
{{- end }}
{{- if not (and (or (kindIs "float64" $s.loadSeconds) (kindIs "int64" $s.loadSeconds) (kindIs "int" $s.loadSeconds)) (eq (float64 $s.loadSeconds) (float64 (int $s.loadSeconds))) (ge (int $s.loadSeconds) 30)) }}
{{- fail "bp-speech: startup.loadSeconds must be a whole number of at least 30" }}
{{- end }}
{{- if and .Values.hubToken.secretProviderClass (not (hasPrefix "hf://" $source)) }}
{{- fail "bp-speech: hubToken.secretProviderClass is set, but the model does not come from hf://" }}
{{- end }}
{{- if and (hasPrefix "hf://" $source) .Values.networkPolicy.enabled (not .Values.networkPolicy.hubEgress) (include "bp-speech.serving" .) }}
{{- fail "bp-speech: an hf:// model is fetched from the model hub, but the network policy allows no egress to it: set networkPolicy.hubEgress true, or use an oci:// model image of the Sovereign's registry" }}
{{- end }}
{{- end }}
