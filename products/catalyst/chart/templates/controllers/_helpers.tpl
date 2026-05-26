{{/*
Helpers for the 5 Group C controllers (organization, environment, blueprint,
application, useraccess) wired by slice CC3 of EPIC-0 (#1095).

Each controller is opt-in via `.Values.controllers.<name>.enabled` (default
false). Operators flip the flag to true in a per-Sovereign overlay once
the controller is ready to take over from the legacy reconciliation path
(catalyst-api-driven for now; controller-driven post-cutover).

Conventions enforced here:

  - Image refs come from `.Values.controllers.<name>.image.{repository,tag,pullPolicy}`
    with NO :latest fallback. If `tag` is empty the chart fails-fast at
    render time so the operator can see the misconfig BEFORE the Pod
    crashlooping. CI stamps the SHA on every push to main per
    docs/INVIOLABLE-PRINCIPLES.md #4a.
  - ServiceAccount names follow `catalyst-<name>-controller` to match
    the existing `catalyst-api-cutover-driver` ServiceAccount pattern.
  - ClusterRole / ClusterRoleBinding names match the SA name (no
    `-controller` suffix collision with anything else in the chart).
  - Standard pod-security shape: runAsNonRoot, runAsUser=65534,
    seccompProfile RuntimeDefault, drop ALL caps, readOnlyRootFilesystem.
  - Resources/requests + limits set per Inviolable Principle #4 (no
    unbounded pods).

*/}}

{{/* Compose the canonical controller name (used as SA / Deployment /
     ClusterRole / ClusterRoleBinding name). */}}
{{- define "controllers.fullname" -}}
{{- printf "catalyst-%s-controller" . -}}
{{- end -}}

{{/* Standard labels applied to every controller's resources. */}}
{{- define "controllers.labels" -}}
app.kubernetes.io/name: {{ printf "%s-controller" .name }}
app.kubernetes.io/component: controller
app.kubernetes.io/part-of: catalyst
app.kubernetes.io/managed-by: {{ .root.Release.Service | default "Helm" }}
catalyst.openova.io/controller: {{ .name }}
policy.cilium.io/enforced: "true"
{{- end -}}

{{/* Selector labels (subset that does not change between releases). */}}
{{- define "controllers.selectorLabels" -}}
app.kubernetes.io/name: {{ printf "%s-controller" .name }}
app.kubernetes.io/component: controller
catalyst.openova.io/controller: {{ .name }}
{{- end -}}

{{/* Resolve the image reference for a controller, fail-fast if tag empty.
     Param shape: dict "name" <controller-name> "cfg" <.Values.controllers.<name>>
     Honours `.Values.global.imageRegistry` rewrite when set (Sovereign-local
     Harbor proxy_cache; same pattern api-deployment.yaml uses). */}}
{{- define "controllers.image" -}}
{{- $name := .name -}}
{{- $cfg := .cfg -}}
{{- $root := .root -}}
{{- $tag := $cfg.image.tag | default "" -}}
{{- if eq $tag "" -}}
{{- fail (printf "controllers.%s.image.tag is empty — CI must stamp a SHA before render. Per docs/INVIOLABLE-PRINCIPLES.md #4a no :latest is permitted." $name) -}}
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
