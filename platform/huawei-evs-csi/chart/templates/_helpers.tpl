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

{{/*
CSI sidecar image ref with global.imageRegistry rewrite (#4973 Refs #3379).

The six sig-storage CSI sidecars (provisioner / attacher / resizer / snapshotter /
node-driver-registrar / livenessprobe) ship from harbor.openova.io/proxy-k8s/* —
the MOTHERSHIP Harbor's proxy-k8s cache. Post-cutover a Sovereign must pull
EXCLUSIVELY from its OWN local Harbor: the self-sovereign-cutover step-08
deny-egress hold blocks harbor.openova.io, so a controller/node roll that still
names harbor.openova.io cannot pull the sidecars → the EVS attach/provision path
breaks on any pod restart (#4973, found live on hw237 step-08). This mirrors the
exact host-swap .driverImage already performs for the openova-io driver image.

When .Values.global.imageRegistry is set (the step-07 image pivot patches this
HR — bp-huawei-evs-csi is already in imageRegistryPivot.additionalHRs), the
leading registry host of each sidecar repository is swapped for the override,
preserving the proxy-k8s/sig-storage/<name> path + tag verbatim
(harbor.openova.io/proxy-k8s/sig-storage/csi-provisioner →
registry.<sovereign-fqdn>/proxy-k8s/sig-storage/csi-provisioner — the local
Harbor's OWN proxy-k8s cache). When empty the ref is emitted byte-identical to
today, so a fresh pre-cutover install renders exactly as before.

Usage:
  {{ include "bp-huawei-evs-csi.sidecarImage" (dict "repository" $img.sidecars.provisioner.repository "tag" $img.sidecars.provisioner.tag "global" .Values.global) }}
*/}}
{{- define "bp-huawei-evs-csi.sidecarImage" -}}
{{- $repo := .repository -}}
{{- $tag := .tag -}}
{{- $globalRegistry := (.global).imageRegistry | default "" -}}
{{- if ne $globalRegistry "" -}}
{{- printf "%s/%s:%s" $globalRegistry (join "/" (slice (splitList "/" $repo) 1)) $tag -}}
{{- else -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}
{{- end -}}
