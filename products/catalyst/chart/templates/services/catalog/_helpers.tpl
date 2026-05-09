{{/*
Helpers for the catalyst-catalog HTTP service shipped by EPIC-2 Slice L
of #1097.

The service is opt-in via `.Values.services.catalog.enabled` (default
false). Operators flip the flag to true once the catalog backend has
been validated against the Sovereign's Gitea (catalog + catalog-sovereign
+ <org>/shared-blueprints repos).

Conventions enforced here mirror the controllers' chart pattern (see
templates/controllers/_helpers.tpl):

  - Image refs come from `.Values.services.catalog.image.{repository,tag,pullPolicy}`
    with NO :latest fallback. Empty `tag` fails fast at render time per
    docs/INVIOLABLE-PRINCIPLES.md #4a. CI stamps the SHA.
  - Service / Deployment / SA / RBAC names follow `catalyst-catalog`.
  - Standard pod-security shape: runAsNonRoot, runAsUser=65532 (matches
    the distroless:nonroot UID), seccompProfile RuntimeDefault, drop
    ALL caps, readOnlyRootFilesystem.
  - Resources requests + limits set per Inviolable Principle #4.

*/}}

{{- define "catalog.fullname" -}}
catalyst-catalog
{{- end -}}

{{- define "catalog.labels" -}}
app.kubernetes.io/name: catalyst-catalog
app.kubernetes.io/component: service
app.kubernetes.io/part-of: catalyst
app.kubernetes.io/managed-by: {{ .root.Release.Service | default "Helm" }}
catalyst.openova.io/service: catalog
{{- end -}}

{{- define "catalog.selectorLabels" -}}
app.kubernetes.io/name: catalyst-catalog
app.kubernetes.io/component: service
catalyst.openova.io/service: catalog
{{- end -}}

{{/* Resolve the image reference for the catalog, fail-fast if tag empty.
     Param: dict "cfg" .Values.services.catalog "root" . */}}
{{- define "catalog.image" -}}
{{- $cfg := .cfg -}}
{{- $root := .root -}}
{{- $tag := $cfg.image.tag | default "" -}}
{{- if eq $tag "" -}}
{{- fail "services.catalog.image.tag is empty — CI must stamp a SHA before render. Per docs/INVIOLABLE-PRINCIPLES.md #4a no :latest is permitted." -}}
{{- end -}}
{{- $repo := $cfg.image.repository -}}
{{- $globalRegistry := $root.Values.global.imageRegistry | default "" -}}
{{- if ne $globalRegistry "" -}}
{{- $parts := splitList "/" $repo -}}
{{- $rest := slice $parts 1 -}}
{{- printf "%s/%s:%s" $globalRegistry (join "/" $rest) $tag -}}
{{- else -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}
{{- end -}}
