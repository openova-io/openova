{{/*
bp-cnpg-pair common helpers (slice C-DB-1, #1101).
*/}}

{{- define "cnpg-pair.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "cnpg-pair.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Common labels — applied to every resource the chart emits so the
Catalyst projector + Continuum K-Cont-2 reconciler can locate the
pair via label selectors. The `openova.io/cnpg-role` and
`openova.io/region` labels are also placed individually on the
primary + replica Cluster CRs so Continuum's switchover sequencer
can find each side without parsing names.
*/}}
{{- define "cnpg-pair.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "cnpg-pair.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-cnpg-pair
{{- end }}

{{- define "cnpg-pair.selectorLabels" -}}
app.kubernetes.io/name: {{ include "cnpg-pair.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Fail-fast image reference per Inviolable Principle #4a.

The `tag` is REQUIRED — empty triggers a template-time error so a
hollow Cluster CR with `:latest` (Helm's default behaviour for empty
tag concatenation) can never reach a Sovereign apiserver. Cluster
overlays SHOULD pin to a digest:
  cnpgPair.image.tag: "sha256:<…>"
*/}}
{{- define "cnpg-pair.imageRef" -}}
{{- $tag := required "cnpgPair.image.tag is REQUIRED — set the CNPG postgres image tag (digest preferred per Inviolable Principle #4a)" .Values.cnpgPair.image.tag -}}
{{- printf "%s:%s" .Values.cnpgPair.image.repository $tag -}}
{{- end }}

{{/*
Region validation — both regions MUST be set when enabled and they
MUST differ (an active-hotstandby pair where primary.region ==
replica.region is degenerate). Triggered only when enabled=true.
*/}}
{{- define "cnpg-pair.validateRegions" -}}
{{- $p := required "cnpgPair.primary.region is REQUIRED when cnpgPair.enabled=true" .Values.cnpgPair.primary.region -}}
{{- $r := required "cnpgPair.replica.region is REQUIRED when cnpgPair.enabled=true" .Values.cnpgPair.replica.region -}}
{{- if eq $p $r -}}
{{- fail (printf "cnpgPair.primary.region (%s) MUST NOT equal cnpgPair.replica.region (%s) — active-hotstandby requires two distinct regions" $p $r) -}}
{{- end -}}
{{- end }}

{{/*
Side resolution — WHICH HALF of the pair this install renders
(chart 0.2.0 split-side topology).

A 2-region Sovereign is two SEPARATE clusters joined by Cilium
ClusterMesh, so the pair's objects must be applied per-cluster:
primary-side objects on cluster-A, replica-side objects on cluster-B.
The bootstrap-kit slot 16b stamps `cnpgPair.side` from the per-region
SOVEREIGN_REGION_ROLE cloud-init substitute, whose value domain is
primary|secondary — normalize "secondary" to "replica" here so the
slot substitutes the role verbatim. Empty/unset defaults to "primary"
(standalone single-manifest installs keep the historical behaviour of
rendering the primary half). Any other value fails the render.
*/}}
{{- define "cnpg-pair.side" -}}
{{- if (include "cnpg-pair.contractActive" .) -}}
{{- include "cnpg-pair.contractSide" . -}}
{{- else -}}
{{- $side := default "primary" .Values.cnpgPair.side -}}
{{- if eq $side "secondary" -}}
{{- $side = "replica" -}}
{{- end -}}
{{- if and (ne $side "primary") (ne $side "replica") -}}
{{- fail (printf "cnpgPair.side must be one of primary|replica|secondary — got %q" .Values.cnpgPair.side) -}}
{{- end -}}
{{- $side -}}
{{- end -}}
{{- end }}

{{- define "cnpg-pair.isPrimarySide" -}}
{{- if eq (include "cnpg-pair.side" .) "primary" -}}true{{- end -}}
{{- end }}

{{- define "cnpg-pair.isReplicaSide" -}}
{{- if eq (include "cnpg-pair.side" .) "replica" -}}true{{- end -}}
{{- end }}

{{/*
Region-B automatic DR promotion — active? (chart 0.2.13, #5137)

TRUE only when ALL of:
  - cnpgPair.enabled            (multi-region pair is on)
  - side=replica                (the actor runs ON cluster-B, the half
                                 that survives a region-A kill)
  - replica.autoPromote.enabled (operator gate; default TRUE — absent
                                 key ⇒ enabled, hasKey-guarded because
                                 sprig `default` swallows a literal
                                 `false`)
  - replication.mode == sync    (the anti-split-brain DATA FENCE: with
                                 synchronous_commit=remote_apply +
                                 dataDurability=required pinned to the
                                 cross-region replica, the old primary
                                 CANNOT durably commit while its sync
                                 standby is unreachable or diverged —
                                 so an automatic promotion can never
                                 lose or fork committed data. async
                                 mode has NO such fence; the promoter
                                 fail-safes to NOT render there.)
*/}}
{{- define "cnpg-pair.autoPromoteActive" -}}
{{- $ap := .Values.cnpgPair.replica.autoPromote | default dict -}}
{{- $apEnabled := true -}}
{{- if hasKey $ap "enabled" }}{{- $apEnabled = $ap.enabled -}}{{- end -}}
{{- if and .Values.cnpgPair.enabled (not (include "cnpg-pair.contractActive" .)) (include "cnpg-pair.isReplicaSide" .) $apEnabled (eq (.Values.cnpgPair.replication.mode | default "sync") "sync") -}}true{{- end -}}
{{- end }}

{{/*
Canonical CR names — derived from fullname so Continuum + the
ClusterMesh global Service can compute them deterministically.
*/}}
{{- define "cnpg-pair.primaryName" -}}
{{- printf "%s-primary" (include "cnpg-pair.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{- define "cnpg-pair.replicaName" -}}
{{- printf "%s-replica" (include "cnpg-pair.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/*
CNPG distributed topology (chart 0.2.29) — planned cross-region
switchover seam. See values.yaml `cnpgPair.distributed`.

distributedActive: "true" when cnpgPair.distributed.enabled.
activePrimary: the Cluster CR name the topology designates primary —
`distributed.primary`, defaulting to the pair-role primary half; fails
the render when it names neither half (a typo there would otherwise
render BOTH halves as replicas = a write outage).
*/}}
{{- define "cnpg-pair.distributedActive" -}}
{{- $d := .Values.cnpgPair.distributed | default dict -}}
{{- if or $d.enabled (include "cnpg-pair.contractActive" .) -}}true{{- end -}}
{{- end }}

{{- define "cnpg-pair.activePrimary" -}}
{{- $d := .Values.cnpgPair.distributed | default dict -}}
{{- $p := $d.primary | default (include "cnpg-pair.primaryName" .) -}}
{{- if (include "cnpg-pair.contractActive" .) -}}
{{- $p = include "cnpg-pair.memberName" (dict "ctx" . "region" (include "openova.topology.get" . | fromJson).primary) -}}
{{- end -}}
{{- if and (ne $p (include "cnpg-pair.primaryName" .)) (ne $p (include "cnpg-pair.replicaName" .)) -}}
{{- fail (printf "cnpgPair.distributed.primary must name one of the pair halves (%s | %s) — got %q" (include "cnpg-pair.primaryName" .) (include "cnpg-pair.replicaName" .) $p) -}}
{{- end -}}
{{- $p -}}
{{- end }}

{{/*
Replication Service name — canonical alias the replica uses to reach
the primary's read-replica endpoint over the Cilium ClusterMesh.

Chart 0.1.0 named this `<name>-primary-r`, which COLLIDED with the
auto-created CNPG `<name>-r` Service (selectorType=r). CNPG refused
to take ownership ("not owned by the cluster") and the primary
Cluster CR stuck at phase "Unable to create required cluster objects".

Chart 0.1.1 switches to `-primary-mesh`. The Service is now declared
via the primary Cluster CR's `spec.managed.services.additional` block
(CNPG owns it; operator applies the `service.cilium.io/global` annotation
directly to the right object) rather than as a standalone template.
*/}}
{{- define "cnpg-pair.replicationServiceName" -}}
{{- printf "%s-primary-mesh" (include "cnpg-pair.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/*
ALL-INSTANCES liveness alias `<fullname>-primary-mesh-any` (#6874 D2).

`-primary-mesh` is selectorType rw — the PRIMARY instance only (#3740: the
cross-region replica must stream from the primary's walsender). Right for the
WAL stream, wrong as the promoter's only liveness signal: it goes dark whenever
the primary Cluster has NO ELECTED PRIMARY, i.e. during CNPG's own local
failover inside a healthy region (hw307, 2026-09-07, on bp-postgres's port of
this promoter: one Pod of three lost, all nodes Ready, and the 120s hold expired
before CNPG's failover finished). This alias selects `cnpg.io/cluster:
<fullname>-primary` with NO role term, so it answers while ANY instance is up;
the promoter probes both and runs the SLOW primaryMissingHoldSeconds clock when
the primary is dark but an instance answers, the FAST primaryDownHoldSeconds
clock only when nothing answers. Standalone Service on both sides
(any-mesh-service.yaml) — the name is not CNPG-reserved and the selector never
has to follow the primary.
*/}}
{{- define "cnpg-pair.anyServiceName" -}}
{{- printf "%s-primary-mesh-any" (include "cnpg-pair.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/*
Peer replication Service name (#5245) — the REVERSE-direction mesh
alias: region-A reaches region-B's writable (promoted) primary through
`<fullname>-replica-mesh`. Declared via the replica Cluster CR's
`spec.managed.services.additional` (selectorType rw — B's designated
primary) on side=replica, plus a zero-backend local stub on
side=primary (Cilium merges global services by name+namespace; without
the stub the name is NXDOMAIN on cluster-A). Consumed by the
dr-failback actor's peer probe and by the demoted primary Cluster's
externalCluster (the region-A rejoin stream).
*/}}
{{- define "cnpg-pair.peerReplicationServiceName" -}}
{{- printf "%s-replica-mesh" (include "cnpg-pair.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/*
Region-A automatic DR FAILBACK — active? (chart 0.2.18, #5245)

TRUE only when ALL of:
  - cnpgPair.enabled
  - side=primary                 (the failback actor runs ON cluster-A —
                                  by definition it only matters once
                                  region-A is back)
  - primary.failback.enabled     (operator gate; default TRUE, hasKey-
                                  guarded like autoPromote)
  - replication.mode == sync     (the SAME data fence that makes the
                                  forward promote safe is what PROVES
                                  region-B's line ⊇ region-A's acked
                                  history — without it, discarding
                                  region-A's timeline could lose
                                  acknowledged commits)
  - clusterMesh.enabled AND crossRegionPeerClusters non-empty
                                 (the peer probe — psql of region-B over
                                  `-replica-mesh` — is a HARD
                                  requirement: without a positive
                                  peer-writable-and-ahead proof the
                                  actor has no safe action at all, so
                                  it does not render. This mirrors the
                                  dr-promoter's #5178 fail-safe, but
                                  stricter: the promoter can observe
                                  without peers; the failback cannot
                                  even observe.)
*/}}
{{- define "cnpg-pair.failbackActive" -}}
{{- $fb := .Values.cnpgPair.primary.failback | default dict -}}
{{- $fbEnabled := true -}}
{{- if hasKey $fb "enabled" }}{{- $fbEnabled = $fb.enabled -}}{{- end -}}
{{- $peers := .Values.cnpgPair.networkPolicy.crossRegionPeerClusters | default list -}}
{{- if and .Values.cnpgPair.enabled (not (include "cnpg-pair.contractActive" .)) (include "cnpg-pair.isPrimarySide" .) $fbEnabled (eq (.Values.cnpgPair.replication.mode | default "sync") "sync") .Values.cnpgPair.clusterMesh.enabled (gt (len $peers) 0) -}}true{{- end -}}
{{- end }}

{{- /*
═══════════════════════════════════════════════════════════════════════════
ADR-0015 D2/D8 — the openova.topology contract (chart 0.2.32)
═══════════════════════════════════════════════════════════════════════════
The pair consumes the ONE contract (platform/topology-lib):

  openova.topology: {mode, self, primary, peers}      region ids
  database.topology.promotionToken                    CNPG demotion token

delivered per region from Git: the application-controller writes
clusters/<fqdn>/regions/<r>/platform/cnpg-pair.yaml in the Sovereign region
repo (a carrier ConfigMap); slot 16b's HelmRelease merges it through
`valuesFrom`. Continuum moves `primary` + `promotionToken` region by region.

MAPPING onto the pair's existing (proven, hw307-live) distributed form — no
second vocabulary:

  contract                       pair
  ───────────────────────────    ─────────────────────────────────────────
  self == primary.region         side=primary  (renders <fullname>-primary)
  self == replica.region         side=replica  (renders <fullname>-replica)
  primary  (region id)           distributed.primary = that region's member
  database.topology.promotionToken  distributed.promotionToken
  present (multi-region)         distributed.enabled = true

CLUSTER NAMES ARE PINNED per region: cnpgPair.primary.region keeps
`<fullname>-primary`, cnpgPair.replica.region keeps `<fullname>-replica`
(the clusterNames-pin mechanism: the two cloud-init region substitutes ARE
the pins). Adoption of a live pair therefore renames nothing; only which
member is primary is moved, natively by CNPG.

UNDER THE CONTRACT Continuum is the one mover, through Git: the imperative
seams stay OFF — dr-promoter / dr-failback actors (they patch the bootstrap-kit
Kustomization), the primary.demoted / replica.promoted shapes, and the chart's
own Continuum CR (the region file carries it). Contract absent ⇒ byte-identical
to 0.2.31.
*/ -}}

{{- define "cnpg-pair.contractActive" -}}
{{- if (include "openova.topology.get" . | fromJson).multiRegion -}}true{{- end -}}
{{- end }}

{{- /* The pair's member (Cluster CR name) for a region id — fails on a region
     that is neither pinned half (a typo would render both halves replicas). */ -}}
{{- define "cnpg-pair.memberName" -}}
{{- $v := .ctx.Values.cnpgPair -}}
{{- if eq .region $v.primary.region -}}
{{- include "cnpg-pair.primaryName" .ctx -}}
{{- else if eq .region $v.replica.region -}}
{{- include "cnpg-pair.replicaName" .ctx -}}
{{- else -}}
{{- fail (printf "openova.topology region %q is neither cnpgPair.primary.region (%q) nor cnpgPair.replica.region (%q) — the pair's members are pinned to those two regions" .region $v.primary.region $v.replica.region) -}}
{{- end -}}
{{- end }}

{{- define "cnpg-pair.contractSide" -}}
{{- $t := include "openova.topology.get" . | fromJson -}}
{{- include "openova.topology.assertMode" (dict "ctx" . "supported" (list "active-hot-standby" "active-passive")) -}}
{{- include "cnpg-pair.validateRegions" . -}}
{{- range (concat (list $t.self $t.primary) $t.peers) -}}
{{- $_ := include "cnpg-pair.memberName" (dict "ctx" $ "region" .) -}}
{{- end -}}
{{- if eq $t.self .Values.cnpgPair.primary.region -}}primary{{- else -}}replica{{- end -}}
{{- end }}

{{- /* The promotion token in force: the contract's under the contract. */ -}}
{{- define "cnpg-pair.promotionToken" -}}
{{- if (include "cnpg-pair.contractActive" .) -}}
{{- dig "database" "topology" "promotionToken" "" (.Values | toJson | fromJson) -}}
{{- else -}}
{{- (.Values.cnpgPair.distributed | default dict).promotionToken | default "" -}}
{{- end -}}
{{- end }}

{{- /* DR seams — never under the contract (Continuum moves the pair via Git). */ -}}
{{- define "cnpg-pair.primaryDemoted" -}}
{{- if and (not (include "cnpg-pair.contractActive" .)) (.Values.cnpgPair.primary.demoted | default false) -}}true{{- end -}}
{{- end }}

{{- define "cnpg-pair.replicaPromoted" -}}
{{- if and (not (include "cnpg-pair.contractActive" .)) (.Values.cnpgPair.replica.promoted | default false) -}}true{{- end -}}
{{- end }}

{{- /* openova.io/topology-primary — what Helm APPLIED, read by Continuum to
     know a commit reached this region (the slot HelmRelease's spec.values do
     not carry the contract: it arrives through valuesFrom). */ -}}
{{- define "cnpg-pair.topologyAnnotations" -}}
{{- if (include "cnpg-pair.contractActive" .) }}
openova.io/topology-primary: {{ (include "openova.topology.get" . | fromJson).primary | quote }}
{{- end }}
{{- end }}
