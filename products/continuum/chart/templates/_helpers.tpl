{{/*
Helpers for bp-continuum (slice K-Cont-1 of EPIC-6 #1101).

Same conventions as the 5 Group C controllers' chart helpers
(products/catalyst/chart/templates/controllers/_helpers.tpl):

  - Image refs come from `.Values.continuum.image.{repository,tag,pullPolicy}`
    with NO :latest fallback. If `tag` is empty the chart fails-fast at
    render time so the operator sees the misconfig BEFORE the Pod
    crashlooping. CI stamps the SHA on every push to main per
    docs/INVIOLABLE-PRINCIPLES.md #4a.
  - ServiceAccount / ClusterRole / ClusterRoleBinding / Deployment names
    follow `continuum-controller` (no `bp-` prefix on K8s resource names).
  - Standard pod-security shape: runAsNonRoot, runAsUser=65534,
    seccompProfile RuntimeDefault, drop ALL caps, readOnlyRootFilesystem.
  - Resources/requests + limits set per Inviolable Principle #4 (no
    unbounded pods).
*/}}

{{/* Canonical name for every K8s object the chart renders. */}}
{{- define "continuum.fullname" -}}
continuum-controller
{{- end -}}

{{/* Standard labels applied to every Continuum resource. */}}
{{- define "continuum.labels" -}}
app.kubernetes.io/name: continuum-controller
app.kubernetes.io/component: controller
app.kubernetes.io/part-of: continuum
app.kubernetes.io/managed-by: {{ .Release.Service | default "Helm" }}
catalyst.openova.io/controller: continuum
openova.io/product: continuum
{{- end -}}

{{/* Selector labels (subset that does not change between releases). */}}
{{- define "continuum.selectorLabels" -}}
app.kubernetes.io/name: continuum-controller
app.kubernetes.io/component: controller
catalyst.openova.io/controller: continuum
{{- end -}}

{{/*
Resolve the image reference for the continuum-controller. Fail-fast
when image.tag is empty (per Inviolable Principle #4a — no :latest in
production). Honours `.Values.global.imageRegistry` rewrite when set
(Sovereign-local Harbor proxy_cache; same pattern catalyst chart uses).

Param shape: `dict "root" .` — root context (so we can read both
`.Values.continuum.image.*` and `.Values.global.imageRegistry`).
*/}}
{{- define "continuum.image" -}}
{{- $root := .root -}}
{{- $cfg := $root.Values.continuum -}}
{{- $tag := $cfg.image.tag | default "" -}}
{{- if eq $tag "" -}}
{{- fail "continuum.image.tag is empty — CI must stamp a SHA before render. Per docs/INVIOLABLE-PRINCIPLES.md #4a no :latest is permitted." -}}
{{- end -}}
{{- $repo := $cfg.image.repository -}}
{{- $globalRegistry := $root.Values.global.imageRegistry | default "" -}}
{{- if ne $globalRegistry "" -}}
{{- /* Strip the leading registry from $repo and prepend the global override. */ -}}
{{- $parts := splitList "/" $repo -}}
{{- $rest := slice $parts 1 -}}
{{- printf "%s/%s:%s" $globalRegistry (join "/" $rest) $tag -}}
{{- else -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}
{{- end -}}
