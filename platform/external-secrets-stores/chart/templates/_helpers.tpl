{{- /*
bp-external-secrets-stores.peers — the peer regions of this region
(ADR-0015, Refs #7067).

Reads the region registry as bootstrap-kit threads it: peerStores.selfRegion
(SOVEREIGN_REGION_CANONICAL_LABEL) and peerStores.regions
(SOVEREIGN_REGIONS_JSON: a JSON string `[{"cloudRegion":"<id>",...}]` or
`["<id>",...]`, or a YAML list of either shape — the wire forms
core/controllers/pkg/regions.Parse accepts). Same derivation as bp-openbao's
openbao.peerTrust, so a region's stores and its OpenBao always agree on the
peer set.

Returns JSON {self, peers[]}. peers is EMPTY (every peer object skip-renders,
never `{{ fail }}`, #402) for a single region, or when selfRegion is unset or
not in the registry. A region id that is not DNS-label safe fails the render.
*/ -}}
{{- define "bp-external-secrets-stores.peers" -}}
{{- $ps := .Values.peerStores | default dict -}}
{{- $self := $ps.selfRegion | default "" | toString | trim -}}
{{- $raw := $ps.regions | default "" -}}
{{- $items := list -}}
{{- if kindIs "string" $raw -}}
{{- if ne (trim $raw) "" -}}{{- $items = fromJsonArray $raw -}}{{- end -}}
{{- else if kindIs "slice" $raw -}}
{{- $items = $raw -}}
{{- end -}}
{{- $ids := list -}}
{{- range $items -}}
{{- $id := "" -}}
{{- if kindIs "map" . -}}{{- $id = .cloudRegion | default "" | toString | trim -}}{{- else -}}{{- $id = . | toString | trim -}}{{- end -}}
{{- if and (ne $id "") (not (has $id $ids)) -}}
{{- if not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$" $id) -}}
{{- fail (printf "peerStores.regions: region id %q is not DNS-label safe (ADR-0015 D3)" $id) -}}
{{- end -}}
{{- $ids = append $ids $id -}}
{{- end -}}
{{- end -}}
{{- $peers := list -}}
{{- if and (ne $self "") (has $self $ids) -}}
{{- range $ids -}}{{- if ne . $self -}}{{- $peers = append $peers . -}}{{- end -}}{{- end -}}
{{- end -}}
{{- dict "self" $self "peers" $peers | toJson -}}
{{- end -}}
