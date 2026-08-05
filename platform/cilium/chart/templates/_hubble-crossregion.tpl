{{/*
#5602 — Hubble UI cross-region singleton helpers.

Hubble UI's `/api/service-map-stream` + `/api/control-stream` are a long-poll
protocol whose channel state lives in an IN-PROCESS map inside the hubble-ui
BACKEND. Only the OPENING message carries the GetEventsRequest; every follow-up
poll has an EMPTY body. A poll that reaches a different backend process mints a
fresh channel from that empty body and hubble-ui answers HTTP 400
("Requested events are empty, terminating..."), which the SPA renders as a
permanent "Idle / Data streams are reconnecting…" surface.

A 2-region Sovereign therefore MUST run exactly ONE hubble-ui behind the one
`hubble.<fqdn>` hostname. These helpers implement the same ClusterMesh idiom
bp-guacamole 0.2.36 (#5358) and bp-harbor 1.2.45 (#5406) use.

See values.yaml `catalystOverlay.hubbleUI.crossRegion` for the full rationale
and the live evidence.
*/}}

{{/*
Cross-region role. `primary` (default) = this region owns the Sovereign's ONE
Hubble UI; `secondary` = this region renders the ClusterMesh Service stub only.
Any other value fails the render rather than silently degrading to primary —
two primaries is exactly the split this fix removes.
*/}}
{{- define "bp-cilium.hubbleUI.crossRegionRole" -}}
{{- $cr := (.Values.catalystOverlay.hubbleUI.crossRegion | default dict) -}}
{{- $role := ($cr.role | default "primary") -}}
{{- if not (has $role (list "primary" "secondary")) -}}
{{- fail (printf "bp-cilium: invalid catalystOverlay.hubbleUI.crossRegion.role %q — must be \"primary\" (owns the singleton Hubble UI) or \"secondary\" (ClusterMesh stub only)" $role) -}}
{{- end -}}
{{- $role -}}
{{- end }}

{{/*
Mesh-enabled predicate. Emits "true" only when the ClusterMesh singleton
treatment actually applies: crossRegion.enabled AND auth=oidc (under auth=none
the HTTPRoute targets the UPSTREAM hubble-ui Service, which this chart does not
own and cannot annotate — see values.yaml). Empty string otherwise, so the
default render is byte-identical to pre-1.4.19.
*/}}
{{- define "bp-cilium.hubbleUI.meshEnabled" -}}
{{- $ov := .Values.catalystOverlay.hubbleUI -}}
{{- $cr := ($ov.crossRegion | default dict) -}}
{{- if and $cr.enabled (eq $ov.auth "oidc") -}}
true
{{- end -}}
{{- end }}

{{/*
Mesh-secondary predicate. Emits "true" when this region must run NO
oauth2-proxy workload, so its `hubble-ui-oauth2-proxy` Service has ZERO local
backends and `service.cilium.io/affinity: local` falls through the mesh to the
primary's singleton. Empty string otherwise.
*/}}
{{- define "bp-cilium.hubbleUI.isMeshSecondary" -}}
{{- if and (include "bp-cilium.hubbleUI.meshEnabled" .) (eq (include "bp-cilium.hubbleUI.crossRegionRole" .) "secondary") -}}
true
{{- end -}}
{{- end }}
