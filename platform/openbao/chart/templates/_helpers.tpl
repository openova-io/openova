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
