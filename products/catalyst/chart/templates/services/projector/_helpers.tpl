{{/*
Helpers for the projector binary shipped by EPIC-4 P1 (#1099).

Mirrors the catalog-service pattern. Default-OFF gate
(`.Values.services.projector.enabled`); operator opts in once
bp-nats-jetstream + bp-valkey are reconciled.

Conventions:
  - Image refs from `.Values.services.projector.image.{repository,tag,pullPolicy}`,
    NO :latest fallback. Empty tag fails fast (INVIOLABLE-PRINCIPLES #4a).
  - Service + Deployment + SA + RBAC names follow `catalyst-projector`.
  - Pod-security shape: runAsNonRoot, runAsUser=65534 (matches the
    binary's USER 65534:65534), seccompProfile RuntimeDefault, drop
    ALL caps, readOnlyRootFilesystem.
*/}}

{{- define "projector.fullname" -}}
catalyst-projector
{{- end -}}

{{- define "projector.labels" -}}
app.kubernetes.io/name: catalyst-projector
app.kubernetes.io/component: service
app.kubernetes.io/part-of: catalyst
app.kubernetes.io/managed-by: {{ .root.Release.Service | default "Helm" }}
catalyst.openova.io/service: projector
{{- end -}}

{{- define "projector.selectorLabels" -}}
app.kubernetes.io/name: catalyst-projector
app.kubernetes.io/component: service
catalyst.openova.io/service: projector
{{- end -}}

{{/* Image-ref resolver. Param: dict "cfg" .Values.services.projector "root" . */}}
{{- define "projector.image" -}}
{{- $cfg := .cfg -}}
{{- $root := .root -}}
{{- $tag := $cfg.image.tag | default "" -}}
{{- if eq $tag "" -}}
{{- fail "services.projector.image.tag is empty — CI must stamp a SHA before render. Per docs/INVIOLABLE-PRINCIPLES.md #4a no :latest is permitted." -}}
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
