{{- /*
bp-openbao parent-chart helpers.
*/ -}}

{{- /*
openbao.peerTrust — this OpenBao's view of its peer regions (ADR-0015, #7067).

Reads the Sovereign region registry in the form cloud-init threads it into
bootstrap-kit: SOVEREIGN_REGIONS_JSON → peerTrust.regions and
SOVEREIGN_REGION_CANONICAL_LABEL → peerTrust.selfRegion. `regions` accepts the
wire forms core/controllers/pkg/regions.Parse accepts: a JSON string
`[{"cloudRegion":"<id>",...}]` or `["<id>",...]`, or a YAML list of either
shape. There is no other region vocabulary.

Returns JSON {self, peers[], hosts{<id>: <service fqdn>}}. peers is EMPTY, so
every peer-trust object skip-renders (never `{{ fail }}`, per #402), when the
registry has one region, or when selfRegion is unset or not in the registry
(a chart never guesses which region it is). A region id that is not
DNS-label safe fails the render: it becomes a Service name and an auth mount.
*/ -}}
{{- define "openbao.peerTrust" -}}
{{- $pt := .Values.peerTrust | default dict -}}
{{- $self := $pt.selfRegion | default "" | toString | trim -}}
{{- $raw := $pt.regions | default "" -}}
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
{{- fail (printf "peerTrust.regions: region id %q is not DNS-label safe (ADR-0015 D3)" $id) -}}
{{- end -}}
{{- $ids = append $ids $id -}}
{{- end -}}
{{- end -}}
{{- $peers := list -}}
{{- if and (ne $self "") (has $self $ids) -}}
{{- range $ids -}}{{- if ne . $self -}}{{- $peers = append $peers . -}}{{- end -}}{{- end -}}
{{- end -}}
{{- $svcPrefix := $pt.servicePrefix | default "openbao-" -}}
{{- $hosts := dict -}}
{{- if $peers -}}
{{- range (concat (list $self) $peers) -}}
{{- $_ := set $hosts . (printf "%s%s.%s.svc.cluster.local" $svcPrefix . $.Release.Namespace) -}}
{{- end -}}
{{- end -}}
{{- dict "self" $self "peers" $peers "hosts" $hosts | toJson -}}
{{- end -}}
