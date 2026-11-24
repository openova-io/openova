{{/* The name of the chart, and the name of every object of the release. */}}
{{- define "bp-bge.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "bp-bge.fullname" -}}
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

{{- define "bp-bge.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "bp-bge.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-bge
{{- end }}

{{- define "bp-bge.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-bge.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/* "true" when the release renders its workloads: home and enabled are both true. */}}
{{- define "bp-bge.serving" -}}
{{- if and (eq (toString .Values.home) "true") (eq (toString .Values.enabled) "true") }}true{{ end }}
{{- end }}

{{/* The models of the release, in serving order: a list of dicts with role, source, revision, servedName. */}}
{{- define "bp-bge.models" -}}
{{- $out := list (merge (dict "role" "embedding") (.Values.embedding | default dict)) }}
{{- if .Values.reranker }}
{{- $out = append $out (merge (dict "role" "reranker") .Values.reranker) }}
{{- end }}
{{- toJson $out }}
{{- end }}

{{/* "hf" when the models come from the model hub, "oci" when they come from model images. */}}
{{- define "bp-bge.kind" -}}
{{- if hasPrefix "hf://" (toString (.Values.embedding | default dict).source) }}hf{{ else }}oci{{ end }}
{{- end }}

{{/*
bp-bge.validate fails the render on what the configSchema of blueprint.yaml
refuses, and on the rules the schema language cannot say. It renders nothing.
*/}}
{{- define "bp-bge.validate" -}}
{{- $sourceRe := "^(oci://[a-z0-9./_-]+@sha256:[a-f0-9]{64}|hf://[A-Za-z0-9._-]+/[A-Za-z0-9._-]+)$" }}
{{- $nameRe := "^[a-z0-9][a-z0-9.-]{0,62}$" }}
{{- range $key := list "home" "enabled" }}
{{- if not (kindIs "bool" (get $.Values $key)) }}
{{- fail (printf "bp-bge: %s must be true or false" $key) }}
{{- end }}
{{- end }}
{{- if not (kindIs "map" .Values.embedding) }}
{{- fail "bp-bge: embedding is required" }}
{{- end }}
{{- $kinds := dict }}
{{- $names := dict }}
{{- range $m := list (list "embedding" .Values.embedding (list "source" "revision" "servedName" "dimensions")) (list "reranker" .Values.reranker (list "source" "revision" "servedName")) }}
{{- $role := index $m 0 }}
{{- $v := index $m 1 }}
{{- $allowed := index $m 2 }}
{{- if $v }}
{{- if not (kindIs "map" $v) }}
{{- fail (printf "bp-bge: %s must be an object" $role) }}
{{- end }}
{{- range $k, $_ := $v }}
{{- if not (has $k $allowed) }}
{{- fail (printf "bp-bge: %s.%s is not a parameter of this chart" $role $k) }}
{{- end }}
{{- end }}
{{- $source := toString $v.source }}
{{- if not (regexMatch $sourceRe $source) }}
{{- fail (printf "bp-bge: %s.source %q is neither oci://<host>/<path>@sha256:<64 hex> nor hf://<owner>/<name>" $role $source) }}
{{- end }}
{{- if hasPrefix "hf://" $source }}
{{- if not (regexMatch "^[a-f0-9]{40}$" (toString $v.revision)) }}
{{- fail (printf "bp-bge: %s.revision must be the 40-character commit of %s" $role $source) }}
{{- end }}
{{- $_ := set $kinds "hf" true }}
{{- else }}
{{- if $v.revision }}
{{- fail (printf "bp-bge: %s.revision is only for an hf:// source" $role) }}
{{- end }}
{{- $_ := set $kinds "oci" true }}
{{- end }}
{{- if not (regexMatch $nameRe (toString $v.servedName)) }}
{{- fail (printf "bp-bge: %s.servedName %q must match %s" $role (toString $v.servedName) $nameRe) }}
{{- end }}
{{- if hasKey $names (toString $v.servedName) }}
{{- fail (printf "bp-bge: embedding and reranker have the same servedName %q" (toString $v.servedName)) }}
{{- end }}
{{- $_ := set $names (toString $v.servedName) true }}
{{- end }}
{{- end }}
{{- $dims := .Values.embedding.dimensions }}
{{- if not (and (or (kindIs "float64" $dims) (kindIs "int64" $dims) (kindIs "int" $dims)) (eq (float64 $dims) (float64 (int $dims))) (ge (int $dims) 64) (le (int $dims) 4096)) }}
{{- fail "bp-bge: embedding.dimensions must be a whole number from 64 to 4096" }}
{{- end }}
{{- if gt (len $kinds) 1 }}
{{- fail "bp-bge: embedding and reranker must come from the same kind of source, both oci:// or both hf://" }}
{{- end }}
{{- with .Values.cache }}
{{- range $k, $_ := . }}
{{- if not (has $k (list "sizeGi" "storageClass")) }}
{{- fail (printf "bp-bge: cache.%s is not a parameter of this chart" $k) }}
{{- end }}
{{- end }}
{{- if not (and (or (kindIs "float64" .sizeGi) (kindIs "int64" .sizeGi) (kindIs "int" .sizeGi)) (eq (float64 .sizeGi) (float64 (int .sizeGi))) (ge (int .sizeGi) 10)) }}
{{- fail "bp-bge: cache.sizeGi must be a whole number of at least 10" }}
{{- end }}
{{- if not (kindIs "string" (.storageClass | default "")) }}
{{- fail "bp-bge: cache.storageClass must be a string" }}
{{- end }}
{{- end }}
{{- if and .Values.hubToken.secretProviderClass (not (hasKey $kinds "hf")) }}
{{- fail "bp-bge: hubToken.secretProviderClass is set, but no model comes from hf://" }}
{{- end }}
{{- if and (hasKey $kinds "hf") .Values.networkPolicy.enabled (not .Values.networkPolicy.hubEgress) (include "bp-bge.serving" .) }}
{{- fail "bp-bge: an hf:// model is fetched from the model hub, but the network policy allows no egress to it: set networkPolicy.hubEgress true, or use oci:// model images of the Sovereign's registry" }}
{{- end }}
{{- end }}
