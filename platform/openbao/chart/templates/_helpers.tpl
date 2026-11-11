{{- /*
bp-openbao parent-chart helpers.

openbao.crossRegionRaftConfig — the full `server.ha.raft.config` HCL for the
SECONDARY (region-B) side of a #3492 cross-region stretched raft cluster.

The upstream openbao chart renders `server.ha.raft.config` into the openbao
ConfigMap (templates/server-config-configmap.yaml → openbao.config helper) and
runs `tpl` over it. The default raft.config has a bare
`storage "raft" { path = "/openbao/data" }` with NO retry_join, so region-B
boots as its OWN independent single-node cluster (the hw139-observed reality).

For the real OSS cross-region mechanism (openbao discussion #1842) region-B
must instead `retry_join` region-A's cluster as a NON-VOTER so it receives the
live data replication stream (region-B then holds region-A's KV, RPO≈0, and
shares region-A's barrier key — so no Shamir-mismatch the way a cross-cluster
snapshot-restore would have). This helper emits exactly that raft.config.

Critical cross-cluster facts baked in (openbao issue #2275):
  - leader_api_addr points at the ClusterMesh-global Service
    (cross-region-mesh-service.yaml) whose name resolves cluster-locally on
    region-B and routes across the mesh to region-A's active openbao Pod.
  - retry_join_as_non_voter = true → region-B never joins region-A's voter
    quorum (a region-B blip can't destabilise region-A), but still streams data.
  - cluster_addr / api_addr stay POD_IP-based (the upstream StatefulSet env
    BAO_CLUSTER_ADDR="https://$(POD_IP):8201" / BAO_API_ADDR; DNS names don't
    cross clusters, so each member must advertise its routable POD_IP).

The 08-openbao.yaml per-Sovereign overlay sets
`openbao.server.ha.raft.config` to the output of this helper (with the leader
address filled), gated on SOVEREIGN_REGION_ROLE=secondary, so the single-region
render is byte-identical (this string is never injected by default).

Usage in a render context (e.g. the cross-region-raft-config ConfigMap that
proves it in `helm template`): `{{ include "openbao.crossRegionRaftConfig" . }}`.
*/ -}}
{{- define "openbao.crossRegionRaftConfig" -}}
{{- $cr := .Values.crossRegion | default dict -}}
{{- $mesh := $cr.meshService | default dict -}}
{{- $svcName := $mesh.name | default "openbao-active-mesh" -}}
{{- $port := $mesh.port | default 8200 -}}
{{- /* leaderApiAddr defaults to the in-namespace ClusterMesh global Service
       (http — the listener sets tls_disable=1; in-mesh traffic is Cilium
       WireGuard-encrypted). region-B resolves it locally; the mesh routes it
       to region-A's active Pod. */ -}}
{{- $leader := $cr.leaderApiAddr | default (printf "http://%s.%s.svc.cluster.local:%v" $svcName .Release.Namespace $port) -}}
{{- $nonVoter := $cr.joinAsNonVoter -}}
{{- if kindIs "invalid" $nonVoter }}{{- $nonVoter = true -}}{{- end -}}
ui = true

listener "tcp" {
  tls_disable = 1
  address = "[::]:8200"
  cluster_address = "[::]:8201"
}

storage "raft" {
  path = "/openbao/data"

  # #3492 cross-region: join region-A's existing raft cluster instead of
  # bootstrapping an independent one. leader_api_addr is the ClusterMesh-global
  # Service that routes to region-A's active openbao Pod (zero local backends
  # on region-B → the dial crosses the mesh).
  retry_join {
    leader_api_addr = "{{ $leader }}"
  }

  # Join as a NON-VOTER: region-B receives region-A's full data-replication
  # stream (holds region-A's live KV) but never participates in region-A's
  # quorum. Promotion to a writable single-node voter on a region-A kill is
  # bp-continuum's peers.json recovery (raft.go), NOT a quorum vote.
  retry_join_as_non_voter = {{ $nonVoter }}
}

service_registration "kubernetes" {}
{{- end -}}

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
