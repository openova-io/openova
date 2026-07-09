{{/*
bp-huawei-evs-csi template helpers.
*/}}

{{/*
Driver image ref with global.imageRegistry rewrite (#4885).

The EVS driver image (image.driver) is the only openova-io first-party image in
this chart — the six CSI sidecars are public harbor.openova.io proxy-k8s mirrors
and MUST NOT be rewritten. When .Values.global.imageRegistry is set (the
self-sovereign-cutover step-07 pivot), the leading registry host of the driver
repository is swapped for the override (so ghcr.io/openova-io/openova/evs-csi-plugin
→ registry.<sovereign-fqdn>/openova-io/openova/evs-csi-plugin); when empty the
ghcr.io ref is emitted verbatim (default byte-identical pre-cutover).

Usage: {{ include "bp-huawei-evs-csi.driverImage" . }}
*/}}
{{- define "bp-huawei-evs-csi.driverImage" -}}
{{- $repo := .Values.image.driver.repository -}}
{{- $tag := .Values.image.driver.tag -}}
{{- $globalRegistry := .Values.global.imageRegistry | default "" -}}
{{- if ne $globalRegistry "" -}}
{{- printf "%s/%s:%s" $globalRegistry (join "/" (slice (splitList "/" $repo) 1)) $tag -}}
{{- else -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}
{{- end -}}
