{{- /*
═══════════════════════════════════════════════════════════════════════════
topology-lib — the openova.topology contract (ADR-0015 D2)
═══════════════════════════════════════════════════════════════════════════

Contract (written per region into Git by the orchestrator, never by a chart):

  openova:
    topology:
      mode:    singleton | active-hot-standby | active-passive | active-active
      self:    <region-id>      # the region THIS release is applied in
      primary: <region-id>      # the region holding the Primary
      peers:   [<region-id>]    # every OTHER region of the placement

Each region gets its own HelmRelease of the chart with `self` set to that
region; every other field is identical across regions. A chart renders ONLY
the `self` half: no resource is pinned (affinity / label / selector) to a
region other than `self`, and CI proves it
(scripts/check-chart-renders-self-only.sh).

Absent / empty contract ⇒ legacy single-region behaviour (`present=false`,
mode singleton, this release is the primary). That is the back-compat seam
for every install that predates the contract.

Calling convention: helpers that return structured data return JSON, decode
with `fromJson` / `fromJsonArray`:

  {{- $t := include "openova.topology.get" . | fromJson }}
  $t.present $t.mode $t.multiRegion $t.self $t.primary $t.peers $t.isPrimary

Region-id spelling: the cluster region id (`hw-me-east-215-a-rtz-prod`) —
ADR-0015 D3. Ids must be DNS-label safe (they end up in labels).
*/ -}}

{{- define "openova.topology.modes" -}}
{{- list "singleton" "active-hot-standby" "active-passive" "active-active" | toJson -}}
{{- end -}}

{{- /*
openova.topology.get — validate + normalise the contract. FAILS the render on
any contradiction; a mis-rendered topology is a write outage or a split brain,
never a warning.
*/ -}}
{{- define "openova.topology.get" -}}
{{- $raw := dig "openova" "topology" dict (.Values | toJson | fromJson) -}}
{{- $mode := $raw.mode | default "" | toString -}}
{{- $out := dict "present" false "mode" "singleton" "multiRegion" false "self" "" "primary" "" "peers" (list) "isPrimary" true -}}
{{- if ne $mode "" -}}
{{- if not (has $mode (include "openova.topology.modes" . | fromJsonArray)) -}}
{{- fail (printf "openova.topology.mode=%q is not one of singleton | active-hot-standby | active-passive | active-active (ADR-0015 D2)" $mode) -}}
{{- end -}}
{{- $self := $raw.self | default "" | toString -}}
{{- $primary := $raw.primary | default "" | toString -}}
{{- $peers := list -}}
{{- range ($raw.peers | default list) -}}
{{- $p := . | toString -}}
{{- if has $p $peers -}}
{{- fail (printf "openova.topology.peers lists %q twice (ADR-0015 D2)" $p) -}}
{{- end -}}
{{- $peers = append $peers $p -}}
{{- end -}}
{{- range (concat (list $self $primary) $peers) -}}
{{- if and (ne . "") (not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$" .)) -}}
{{- fail (printf "openova.topology region id %q is not DNS-label safe ([a-z0-9-], ADR-0015 D3)" .) -}}
{{- end -}}
{{- end -}}
{{- if eq $mode "singleton" -}}
{{- if gt (len $peers) 0 -}}
{{- fail (printf "openova.topology.mode=singleton declares peers %v — a singleton has no peers; use a multi-region mode (ADR-0015 D2)" $peers) -}}
{{- end -}}
{{- if and (ne $primary "") (ne $self "") (ne $primary $self) -}}
{{- fail (printf "openova.topology.mode=singleton with primary=%q != self=%q — a singleton is always its own primary" $primary $self) -}}
{{- end -}}
{{- $_ := set $out "self" $self -}}
{{- $_ := set $out "primary" (default $self $primary) -}}
{{- else -}}
{{- if eq $self "" -}}
{{- fail (printf "openova.topology.self is REQUIRED for mode=%s: each region's HelmRelease must state which region it renders, or the chart cannot render only its own half (ADR-0015 D2)" $mode) -}}
{{- end -}}
{{- if eq $primary "" -}}
{{- fail (printf "openova.topology.primary is REQUIRED for mode=%s (ADR-0015 D2)" $mode) -}}
{{- end -}}
{{- if eq (len $peers) 0 -}}
{{- fail (printf "openova.topology.peers is EMPTY for mode=%s — a multi-region placement needs at least one peer region (ADR-0015 D2)" $mode) -}}
{{- end -}}
{{- if has $self $peers -}}
{{- fail (printf "openova.topology.peers must not contain self (%q) (ADR-0015 D2)" $self) -}}
{{- end -}}
{{- if and (ne $primary $self) (not (has $primary $peers)) -}}
{{- fail (printf "openova.topology.primary=%q is neither self (%q) nor a declared peer %v — every region would render as a replica (write outage) (ADR-0015 D2)" $primary $self $peers) -}}
{{- end -}}
{{- $_ := set $out "self" $self -}}
{{- $_ := set $out "primary" $primary -}}
{{- $_ := set $out "peers" $peers -}}
{{- $_ := set $out "multiRegion" true -}}
{{- $_ := set $out "isPrimary" (eq $self $primary) -}}
{{- end -}}
{{- $_ := set $out "present" true -}}
{{- $_ := set $out "mode" $mode -}}
{{- end -}}
{{- $out | toJson -}}
{{- end -}}

{{- /*
openova.topology.assertMode — a chart states which modes it supports.
  include "openova.topology.assertMode" (dict "ctx" . "supported" (list "singleton" "active-passive"))
*/ -}}
{{- define "openova.topology.assertMode" -}}
{{- $t := include "openova.topology.get" .ctx | fromJson -}}
{{- if not (has $t.mode .supported) -}}
{{- fail (printf "openova.topology.mode=%s is not supported by %s (supported: %v)" $t.mode .ctx.Chart.Name .supported) -}}
{{- end -}}
{{- end -}}

{{- /* "true" when this release renders the Primary (singleton / absent ⇒ true). */ -}}
{{- define "openova.topology.isPrimary" -}}
{{- if (include "openova.topology.get" . | fromJson).isPrimary -}}true{{- end -}}
{{- end -}}

{{- /* "true" when the contract declares a multi-region mode. */ -}}
{{- define "openova.topology.multiRegion" -}}
{{- if (include "openova.topology.get" . | fromJson).multiRegion -}}true{{- end -}}
{{- end -}}

{{- /*
openova.topology.regionHash — the stable, DNS-safe per-region suffix:
the first 8 hex chars of sha256(<region-id>). Any orchestrator can compute it
(`printf %s <region-id> | sha256sum | cut -c1-8`); it never changes for a
region; it does not depend on the substrate's id spelling length.
*/ -}}
{{- define "openova.topology.regionHash" -}}
{{- . | toString | sha256sum | trunc 8 -}}
{{- end -}}

{{- /*
openova.topology.nameFor — the per-region resource name of a member.
  include "openova.topology.nameFor" (dict "base" "wordpress-db" "region" "<id>" "overrides" (dict))
⇒ `<base>-<regionHash>` (base truncated to 41 so the result is ≤ 50 chars —
the CNPG Cluster-name ceiling). `overrides` maps region-id → explicit name
(migration seam: keep an existing Cluster's name instead of re-creating it).
*/ -}}
{{- define "openova.topology.nameFor" -}}
{{- $ov := .overrides | default dict -}}
{{- if hasKey $ov .region -}}
{{- index $ov .region -}}
{{- else -}}
{{- printf "%s-%s" (.base | trunc 41 | trimSuffix "-") (include "openova.topology.regionHash" .region) -}}
{{- end -}}
{{- end -}}

{{- /*
openova.topology.memberNames — member resource names keyed by role.
  include "openova.topology.memberNames" (dict "ctx" . "base" "wordpress-db" "overrides" (dict))
⇒ JSON {"self": n, "primary": n, "peers": [n...], "source": n}
  - contract absent            → every name = base (legacy)
  - singleton without `self`   → base
  - otherwise                  → nameFor(region)
`source` = whom THIS member streams from: the primary when self is a replica,
the first peer when self is the primary (used only once CNPG demotes it).
*/ -}}
{{- define "openova.topology.memberNames" -}}
{{- $t := include "openova.topology.get" .ctx | fromJson -}}
{{- $base := .base -}}
{{- $ov := .overrides | default dict -}}
{{- $out := dict "self" $base "primary" $base "peers" (list) "source" "" -}}
{{- if and $t.present (ne $t.self "") -}}
{{- $self := include "openova.topology.nameFor" (dict "base" $base "region" $t.self "overrides" $ov) -}}
{{- $primary := include "openova.topology.nameFor" (dict "base" $base "region" $t.primary "overrides" $ov) -}}
{{- $peers := list -}}
{{- range $t.peers -}}
{{- $peers = append $peers (include "openova.topology.nameFor" (dict "base" $base "region" . "overrides" $ov)) -}}
{{- end -}}
{{- $all := concat (list $self) $peers -}}
{{- if ne (len ($all | uniq)) (len $all) -}}
{{- fail (printf "openova.topology: member names collide %v (check overrides) — CNPG distributed topology needs unique Cluster names" $all) -}}
{{- end -}}
{{- $_ := set $out "self" $self -}}
{{- $_ := set $out "primary" $primary -}}
{{- $_ := set $out "peers" $peers -}}
{{- if $t.multiRegion -}}
{{- $_ := set $out "source" (ternary (first $peers) $primary $t.isPrimary) -}}
{{- end -}}
{{- end -}}
{{- $out | toJson -}}
{{- end -}}

{{- /* ClusterMesh global alias of a member's CNPG `rw` endpoint. */ -}}
{{- define "openova.topology.cnpg.meshServiceName" -}}
{{- printf "%s-mesh" . | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- /* Cilium ClusterMesh global-service annotations (the #5289 / #5635 idiom). */ -}}
{{- define "openova.topology.meshAnnotations" -}}
service.cilium.io/global: "true"
service.cilium.io/shared: "true"
service.cilium.io/affinity: {{ . | default "local" | quote }}
{{- end -}}

{{- /*
openova.topology.cnpg.replica — the CNPG ≥1.24 distributed-topology `replica:`
block body (indent it under `spec.replica:`). Empty unless multi-region.
  include "openova.topology.cnpg.replica" (dict "ctx" . "base" B "overrides" O "promotionToken" T)
`promotionToken` is rendered ONLY on the member the contract names primary:
it is the demotion token the former primary published, and CNPG promotes this
cluster only after replaying up to it (lossless planned move).
*/ -}}
{{- define "openova.topology.cnpg.replica" -}}
{{- $t := include "openova.topology.get" .ctx | fromJson -}}
{{- if $t.multiRegion -}}
{{- $n := include "openova.topology.memberNames" (dict "ctx" .ctx "base" .base "overrides" .overrides) | fromJson -}}
self: {{ $n.self | quote }}
primary: {{ $n.primary | quote }}
source: {{ $n.source | quote }}
{{- if and $t.isPrimary .promotionToken }}
promotionToken: {{ .promotionToken | quote }}
{{- end }}
{{- end -}}
{{- end -}}

{{- /*
openova.topology.cnpg.externalClusters — one entry per topology member (self
included — CNPG's distributed topology lists every member). Each reaches the
member's `rw` over its `<member>-mesh` ClusterMesh global Service, presenting
THIS member's streaming_replica client cert (`<self>-replication`, issued from
the shared client CA every member pins — see the consuming chart). No
sslRootCert: each member's SERVER chain is signed by its own per-cluster CA,
which the other regions do not hold (cnpg-pair 0.2.20 hw278 lesson: a root
pin upgrades sslmode=require to verify-ca and the leg never connects).
  include "openova.topology.cnpg.externalClusters" (dict "ctx" . "base" B "overrides" O "dbname" D)
*/ -}}
{{- define "openova.topology.cnpg.externalClusters" -}}
{{- $t := include "openova.topology.get" .ctx | fromJson -}}
{{- if $t.multiRegion -}}
{{- $n := include "openova.topology.memberNames" (dict "ctx" .ctx "base" .base "overrides" .overrides) | fromJson -}}
{{- $dbname := .dbname -}}
{{- range (concat (list $n.self) $n.peers) }}
- name: {{ . | quote }}
  connectionParameters:
    host: {{ include "openova.topology.cnpg.meshServiceName" . | quote }}
    port: "5432"
    user: streaming_replica
    sslmode: require
    dbname: {{ $dbname | quote }}
  sslKey:
    name: {{ printf "%s-replication" $n.self | quote }}
    key: tls.key
  sslCert:
    name: {{ printf "%s-replication" $n.self | quote }}
    key: tls.crt
{{- end }}
{{- end -}}
{{- end -}}

{{- /*
openova.topology.cnpg.syncStandbyNames — JSON list of the peer Cluster names
the Primary must wait for (ADR-0004: FIRST n, dataDurability required). Empty
on a replica member and when no peer is declared: a synchronous standby name
is rendered ONLY for a peer declared in `peers[]` (ADR-0015 D2).
*/ -}}
{{- define "openova.topology.cnpg.syncStandbyNames" -}}
{{- $t := include "openova.topology.get" .ctx | fromJson -}}
{{- if and $t.multiRegion $t.isPrimary -}}
{{- (include "openova.topology.memberNames" (dict "ctx" .ctx "base" .base "overrides" .overrides) | fromJson).peers | toJson -}}
{{- else -}}
[]
{{- end -}}
{{- end -}}

{{- /*
openova.topology.cnpg.managedMeshService — this member's own `<self>-mesh`
entry for `spec.managed.services.additional` (CNPG owns the Service).
selectorType `rw`: the member's (designated) primary instance. NOT `r`: an `r`
selector also picks local standbys, so a peer could cascade off a standby and
the synchronous target could never be satisfied (#3740, cnpg-pair).
  include "openova.topology.cnpg.managedMeshService" (dict "name" <self> "labels" <yaml> "affinity" "local")
*/ -}}
{{- define "openova.topology.cnpg.managedMeshService" -}}
- selectorType: rw
  serviceTemplate:
    metadata:
      name: {{ include "openova.topology.cnpg.meshServiceName" .name | quote }}
      {{- with .labels }}
      labels:
        {{- . | nindent 8 }}
      {{- end }}
      annotations:
        {{- include "openova.topology.meshAnnotations" .affinity | nindent 8 }}
    spec:
      type: ClusterIP
      ports:
        - name: postgres
          port: 5432
          targetPort: 5432
          protocol: TCP
{{- end -}}

{{- /*
openova.topology.cnpg.peerMeshStubs — one zero-backend Service per PEER member
named `<peer>-mesh`. Cilium merges global Services by name+namespace, so the
name must exist locally or it is NXDOMAIN here; its selector
(`cnpg.io/cluster: <peer>`) matches no local Pod, so every connection crosses
the mesh to the peer region. Selects nothing in this region ⇒ pins nothing to
another region.
  include "openova.topology.cnpg.peerMeshStubs" (dict "ctx" . "base" B "overrides" O "labels" <yaml> "affinity" "local")
*/ -}}
{{- define "openova.topology.cnpg.peerMeshStubs" -}}
{{- $t := include "openova.topology.get" .ctx | fromJson -}}
{{- if $t.multiRegion -}}
{{- $n := include "openova.topology.memberNames" (dict "ctx" .ctx "base" .base "overrides" .overrides) | fromJson -}}
{{- $labels := .labels -}}
{{- $affinity := .affinity -}}
{{- $ns := .ctx.Release.Namespace -}}
{{- range $n.peers }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ include "openova.topology.cnpg.meshServiceName" . | quote }}
  namespace: {{ $ns | quote }}
  labels:
    {{- with $labels }}
    {{- . | nindent 4 }}
    {{- end }}
    catalyst.openova.io/role: peer-mesh-stub
  annotations:
    {{- include "openova.topology.meshAnnotations" $affinity | nindent 4 }}
spec:
  type: ClusterIP
  selector:
    cnpg.io/cluster: {{ . | quote }}
  ports:
    - name: postgres
      port: 5432
      targetPort: 5432
      protocol: TCP
{{- end }}
{{- end -}}
{{- end -}}

{{- /*
openova.topology.labels — the identity labels a per-region resource carries.
Region label = `self` ONLY (never a peer): the self-only CI gate rejects any
other value. Role is deliberately NOT a common label (it changes on every
primary move); put `openova.topology.role` only on the objects whose role it
describes.
*/ -}}
{{- define "openova.topology.labels" -}}
{{- $t := include "openova.topology.get" . | fromJson -}}
{{- if and $t.present (ne $t.self "") -}}
openova.io/region: {{ $t.self | quote }}
openova.io/topology-mode: {{ $t.mode | quote }}
{{- end -}}
{{- end -}}

{{- /* "primary" | "replica" for this release. */ -}}
{{- define "openova.topology.role" -}}
{{- ternary "primary" "replica" (include "openova.topology.get" . | fromJson).isPrimary -}}
{{- end -}}
