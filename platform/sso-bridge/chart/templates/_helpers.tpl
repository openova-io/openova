{{/*
bp-sso-bridge image reference composer (#2940 Pillar 5 anti-tether).

Composes the reconciler container image from:
  - .Values.global.registryMirror  (default "harbor.openova.io")
  - .Values.image.repository        (registry-RELATIVE path, e.g.
                                      "proxy-dockerhub/alpine/k8s")
  - .Values.image.tag

The registry host therefore has exactly ONE source of truth
(global.registryMirror), so a per-Sovereign overlay — or the
bp-self-sovereign-cutover Step-04 registry pivot (ADR-0002) — re-points
EVERY pull by setting that one value to the Sovereign's own Harbor
(e.g. "harbor.<sovereign-fqdn>").

Backward-compat: if .Values.image.repository ALREADY carries a registry
host (its first "/"-segment contains a "." or ":", e.g. the pre-#2940
literal "harbor.openova.io/proxy-dockerhub/alpine/k8s"), the helper uses
it verbatim and does NOT prepend the mirror — so existing per-Sovereign
overlays that set a full host-qualified repository keep working.
*/}}
{{- define "bp-sso-bridge.image" -}}
{{- $repo := .Values.image.repository -}}
{{- $tag := .Values.image.tag -}}
{{- if not $repo -}}
{{- fail "bp-sso-bridge: .Values.image.repository is empty" -}}
{{- end -}}
{{- if not $tag -}}
{{- fail "bp-sso-bridge: .Values.image.tag is empty — pin a tag (MIRROR-EVERYTHING)" -}}
{{- end -}}
{{- $firstSeg := first (splitList "/" $repo) -}}
{{- if or (contains "." $firstSeg) (contains ":" $firstSeg) -}}
{{- /* repository is already host-qualified — honour it verbatim */ -}}
{{- printf "%s:%s" $repo $tag -}}
{{- else -}}
{{- $mirror := .Values.global.registryMirror | default "harbor.openova.io" -}}
{{- if not $mirror -}}
{{- fail "bp-sso-bridge: .Values.global.registryMirror is empty and .Values.image.repository is host-less — set one or the other (#2940)" -}}
{{- end -}}
{{- printf "%s/%s:%s" $mirror $repo $tag -}}
{{- end -}}
{{- end }}
