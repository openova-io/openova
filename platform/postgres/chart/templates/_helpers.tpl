{{/*
bp-postgres common helpers (ADR-0010, #3188).
*/}}

{{/* The data-instance (CNPG Cluster) name. */}}
{{- define "bp-postgres.instanceName" -}}
{{- default "postgres" .Values.instance.name -}}
{{- end -}}

{{/* The namespace the Cluster + CNPG Secrets live in. */}}
{{- define "bp-postgres.namespace" -}}
{{- default .Release.Namespace .Values.instance.namespace -}}
{{- end -}}

{{/* Full CNPG image ref. */}}
{{- define "bp-postgres.imageRef" -}}
{{- printf "%s:%s" .Values.instance.imageName (.Values.instance.pgVersion | toString) -}}
{{- end -}}

{{/*
active-hot-standby cross-region split-side resolution (#3375 / #3571).

A 2-region Sovereign is two SEPARATE k3s clusters joined by Cilium
ClusterMesh — NOT one stretched cluster. In `active-hot-standby` mode the
data instance therefore renders the bp-cnpg-pair split-side shape: the
PRIMARY half lands on region-A's control plane, the REPLICA half on
region-B's. Each cluster applies the SAME HelmRelease and `topology.side`
(stamped from the per-region SOVEREIGN_REGION_ROLE substitute) selects
which half it renders.

`topology.side` value domain: primary|replica|secondary (secondary aliases
replica so the bootstrap-kit substitutes the cloud-init role verbatim).
Empty/unset → primary, so a singleton install (where the split never
applies) keeps the historical single-cluster behaviour. Any other value
fails the render. Mirrors bp-cnpg-pair's `cnpg-pair.side` helper exactly.
*/}}
{{- define "bp-postgres.side" -}}
{{- $side := default "primary" .Values.topology.side -}}
{{- if eq $side "secondary" -}}
{{- $side = "replica" -}}
{{- end -}}
{{- if and (ne $side "primary") (ne $side "replica") -}}
{{- fail (printf "topology.side must be one of primary|replica|secondary — got %q" .Values.topology.side) -}}
{{- end -}}
{{- $side -}}
{{- end -}}

{{- define "bp-postgres.isReplicaSide" -}}
{{- if eq (include "bp-postgres.side" .) "replica" -}}true{{- end -}}
{{- end -}}

{{/*
Whether this install renders the active-hot-standby (cnpg-pair) shape.

TRUE when EITHER `topology.mode` is the literal `active-hot-standby` OR the
boolean `topology.crossRegion` is set — the bootstrap-kit slots wire
`topology.crossRegion` to ${SOVEREIGN_ENABLE_CNPG_PAIR:-false}, the SAME
post-mesh-confirm signal bp-cnpg-pair's `cnpgPair.enabled` rides (catalyst-api
patches it onto every region's Kustomization only after ClusterMesh is
established + the primary's replica-auth Secrets sync). This boolean seam lets
the slots flip the shared instances into the pair shape WITHOUT a second
catalyst-api flip-key (envsubst can substitute a plain true/false, but can't
turn the boolean into the `active-hot-standby` mode STRING — that needs tofu
template logic only available in cloud-init, and the cnpg-pair signal is a
RUNTIME catalyst-api patch, not a cloud-init substitute). Sprig-safe
`ne (toString …) "false"` so a literal `false` is honoured. Default false →
singleton, byte-identical to pre-0.2.0.
*/}}
{{- define "bp-postgres.activeHotStandby" -}}
{{- if or (eq .Values.topology.mode "active-hot-standby") (and (hasKey .Values.topology "crossRegion") (ne (toString .Values.topology.crossRegion) "false")) -}}true{{- end -}}
{{- end -}}

{{/*
True ONLY when this install is an active-hot-standby REPLICA half — the
single condition under which the data instance renders a follower Cluster
instead of the primary/singleton Cluster + its Database/role/Secret
machinery. Used to gate every primary-only template (the Database CRs,
the managed roles, the hub Secrets, the Application CR placement) so the
replica side renders ONLY the follower Cluster + its mesh stub + netpol.
*/}}
{{- define "bp-postgres.renderReplicaHalf" -}}
{{- if and (include "bp-postgres.activeHotStandby" .) (eq (include "bp-postgres.side" .) "replica") -}}true{{- end -}}
{{- end -}}

{{/*
Region-B automatic DR promotion — active? (chart 0.2.18, #5623)

The shared-pg data instances render the EXACT bp-cnpg-pair split-side replica
shape but shipped NO region-local promoter, so on a region-A kill their region-B
replicas stayed `pg_is_in_recovery()=t` for the whole outage — keycloak and every
other platform database read-only, the auth path unrecoverable in region-B (hw292
G12, 2026-08-03). This gate renders the SAME proven bp-cnpg-pair dr-promoter shape
(templates/dr-promoter.yaml + the shared _dr-promoter-scripts.tpl partials) on this
side, so the survivor auto-promotes RPO=0 with the same false-promote guards.

TRUE only when ALL of (mirrors bp-cnpg-pair.autoPromoteActive exactly):
  - renderReplicaHalf            (active-hot-standby AND side=replica|secondary —
                                  the actor runs ON cluster-B, the half that
                                  survives a region-A kill)
  - autoPromote.enabled          (operator gate; default TRUE — absent key ⇒
                                  enabled, hasKey-guarded because sprig `default`
                                  swallows a literal `false`)
  - replication.mode == sync     (the anti-split-brain DATA FENCE: with
                                  synchronous_commit=remote_apply + FIRST 1 pinned
                                  to the cross-region replica, the old primary
                                  CANNOT durably commit while its sync standby is
                                  unreachable or diverged, so an automatic promote
                                  can never lose or fork committed data. async has
                                  no fence → the promoter does NOT render there.)
renderReplicaHalf is already false when the whole chart is disabled or singleton;
dr-promoter.yaml additionally guards on `ne (toString .Values.enabled) "false"`.
*/}}
{{- define "bp-postgres.autoPromoteActive" -}}
{{- $topology := .Values.topology | default dict -}}
{{- $ap := dig "autoPromote" dict $topology -}}
{{- $apEnabled := true -}}
{{- if hasKey $ap "enabled" }}{{- $apEnabled = $ap.enabled -}}{{- end -}}
{{- if and (include "bp-postgres.renderReplicaHalf" .) $apEnabled (eq (dig "replication" "mode" "sync" $topology) "sync") -}}true{{- end -}}
{{- end -}}

{{/*
Whether this install publishes the ClusterMesh-global `-mesh` (read) +
`-mesh-rw` (write) Service aliases — gated on `topology.clusterMesh.enabled`
+ a MULTI-REGION signal, deliberately DECOUPLED from `activeHotStandby` (#4460).

WHY decoupled (the #4460 root cause): the secondary region's consumer host
(keycloak/gitea/harbor `externalDatabase.host`) is baked to
`<instance>-mesh-rw.shared-data…` UNCONDITIONALLY at cloud-init boot whenever
`enable_shared_pg=true` (cloudinit-control-plane.tftpl), AND #4439's
patchSecondaryCrossRegionPGHosts re-stamps the same. But the `-mesh-rw` global
Service used to render ONLY under `$ahs` (`crossRegion=true`), which flips true
LATE — after full ClusterMesh convergence AND the cnpg-pair replication flip
(`SOVEREIGN_ENABLE_CNPG_PAIR=true`, catalyst-api enableCNPGPairAfterFullMesh).
So during the entire pre-flip window — and PERMANENTLY if the mesh never
converges (`0 remote clusters ready`) — the baked host had no backing Service
→ NXDOMAIN → region-B CrashLoop (the §2/§6 UAT walkers' symptom; also gates the
#4275 region-kill walk).

Host RESOLUTION is a mesh property; WAL REPLICATION is a cnpg-pair property.
Publishing these `service.cilium.io/global` aliases the moment ClusterMesh is
enabled (not when replication flips) lets the secondary's host resolve
cross-mesh to the primary's `-rw`/`-r` endpoint as soon as the mesh peers.

MULTI-REGION SIGNAL (not just clusterMesh.enabled): a TRUE singleton
(single-region prov) must stay byte-identical — NO mesh aliases, NO
`service.cilium.io/global` annotation. The signal that distinguishes a pre-flip
SECONDARY-of-a-2-region-prov from a true singleton is that BOTH
`topology.primary.region` AND `topology.replica.region` are non-empty:
cloud-init stamps SOVEREIGN_PRIMARY_REGION + SOVEREIGN_REPLICA_REGION onto EVERY
region's bootstrap-kit substitute at boot (cloudinit-control-plane.tftpl,
unconditional), so both are populated on a 2-region prov from the very first
reconcile — BEFORE and independent of the cnpg-pair flip. A single-region prov
leaves replica.region empty, so the aliases never render there. This is the
exact same precondition the cnpg-pair flip itself `required`s (distinct
non-empty primary/replica regions), so the two gates can never disagree.

nil-safe: clusterMesh / primary / replica may be absent on a direct chart
consumer that --sets only `mode`. The values.yaml defaults them, but dig keeps
the helper panic-free even when an overlay nulls the sub-map. Sprig-safe
`ne (toString …) "false"` so a literal clusterMesh.enabled=false (operator
opt-out) is honoured.
*/}}
{{- define "bp-postgres.meshGlobalServices" -}}
{{- $topology := .Values.topology | default dict -}}
{{- $cmOn := ne (toString (dig "clusterMesh" "enabled" true $topology)) "false" -}}
{{- $primaryRegion := dig "primary" "region" "" $topology -}}
{{- $replicaRegion := dig "replica" "region" "" $topology -}}
{{- $multiRegion := and (ne (trim (toString $primaryRegion)) "") (ne (trim (toString $replicaRegion)) "") -}}
{{- if and $cmOn $multiRegion -}}true{{- end -}}
{{- end -}}

{{/*
Region resolution — FAIL CLOSED (#5639).

WHY THIS EXISTS. In active-hot-standby BOTH Cluster halves pin themselves to a
region with a REQUIRED nodeAffinity on the `openova.io/region` node label
(cluster.yaml primary, replica-cluster.yaml follower). Until 0.2.17 both sites
read `.Values.topology.<side>.region` directly, so an install that never set the
key rendered

    values: [""]                 <- matchExpressions In [empty string]
    openova.io/region: ""        <- the CR label

which no node can ever satisfy. That is not a slow schedule; it is unschedulable
FOREVER, and every status surface above it reports success: Helm rendered valid
YAML, the apiserver accepted the Cluster, the HelmRelease went
`install succeeded`, and the Application card showed a green badge over a
database that never had a running primary.

Proven live on hw292 (2026-08-03, #5639): the per-Org Cluster
`hw292-omani-works/postgres` sat at phase="Setting up primary" for 7+ hours with

    nodeAffinity required: openova.io/region In [""]
    node label, all 4 nodes:  openova.io/region=hw-me-east-215-a-rtz-prod
    FailedScheduling: 0/4 nodes are available: 4 node(s) didn't match Pod's
                      node affinity/selector

against the CONTROL of a healthy cnpg/cnpg-pair primary on the same cluster
carrying `openova.io/region=[hw-me-east-215-a-rtz-prod]`. The per-Org
HelmRelease values were `{"topology":{"mode":"active-hot-standby"}}` — mode set,
region absent.

WHAT THESE HELPERS DO. They make an unresolvable region an INSTALL ERROR naming
the exact missing key, instead of an empty selector. `required` is Helm's
fail-closed primitive and it treats the empty string as missing, which is
precisely the shape values.yaml ships (`topology.primary.region: ""`). This is
the SAME guard bp-wordpress-tenant has had since its own D31 work
(`bp-wordpress-tenant.validateActiveHotStandbyRegions`, _helpers.tpl) — the
sibling chart already refused to render a half-declared pair; bp-postgres was
the one that drifted.

SCOPE. Only the active-hot-standby render consumes a region, so these helpers
are included ONLY from inside the `$ahs` branch of cluster.yaml and from
replica-cluster.yaml (which is itself gated on renderReplicaHalf = ahs AND
side=replica). A SINGLETON install has no nodeAffinity and needs no region — its
render is byte-identical to 0.2.16, and the bootstrap-kit slots (16a/16c/16d)
are unaffected because cloud-init stamps SOVEREIGN_PRIMARY_REGION /
SOVEREIGN_REPLICA_REGION into every region's substitutes unconditionally
(cloudinit-control-plane.tftpl; primary_region_canonical_label is non-empty on
both the Hetzner and Huawei providers).

nil-safe via `dig`, same as meshGlobalServices above: an overlay that nulls the
`topology` map or its `primary`/`replica` sub-maps reaches `required` with ""
rather than panicking on a nil dereference.
*/}}
{{- define "bp-postgres.primaryRegion" -}}
{{- $topology := .Values.topology | default dict -}}
{{- required "bp-postgres: topology.primary.region is REQUIRED in active-hot-standby mode (topology.mode=active-hot-standby or topology.crossRegion=true). It is the canonical openova.io/region NODE LABEL the primary Cluster's required nodeAffinity pins on — set it from this Sovereign's SOVEREIGN_PRIMARY_REGION (e.g. hz-fsn-rtz-prod). Rendering it empty emits 'openova.io/region In [\"\"]', which no node can ever satisfy, so the primary is unschedulable forever while the HelmRelease still reports install succeeded (#5639)." (dig "primary" "region" "" $topology) -}}
{{- end -}}

{{/*
Replica half of bp-postgres.primaryRegion — same fail-closed contract for the
follower Cluster's region pin. replica-cluster.yaml renders ONLY when
renderReplicaHalf is true (active-hot-standby AND side=replica), so every call
site here is already inside the cross-region shape; an empty replica.region at
that point is the identical #5639 defect on cluster-B.
*/}}
{{- define "bp-postgres.replicaRegion" -}}
{{- $topology := .Values.topology | default dict -}}
{{- required "bp-postgres: topology.replica.region is REQUIRED when rendering the active-hot-standby REPLICA half (topology.side=replica|secondary). It is the canonical openova.io/region NODE LABEL the follower Cluster's required nodeAffinity pins on — set it from this Sovereign's SOVEREIGN_REPLICA_REGION (e.g. hz-hel-rtz-prod). Rendering it empty emits 'openova.io/region In [\"\"]', which no node can ever satisfy, so the follower is unschedulable forever while the HelmRelease still reports install succeeded (#5639)." (dig "replica" "region" "" $topology) -}}
{{- end -}}

{{/*
The follower Cluster CR name on the replica side. Distinct from the
primary instance name (which the consumer host `<instance>-rw` resolves
to) so the two Cluster CRs never collide and bp-continuum's switchover
sequencer can find each side. Mirrors bp-cnpg-pair's `<fullname>-replica`.
*/}}
{{- define "bp-postgres.replicaName" -}}
{{- printf "%s-replica" (include "bp-postgres.instanceName" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
ClusterMesh-global replication Service alias the replica's externalCluster
dials to reach the primary's read endpoint over Cilium ClusterMesh.

Named `<instance>-mesh` (NOT `<instance>-r`, which would collide with the
CNPG-auto-created `-r` Service — the bp-cnpg-pair 0.1.0 "refusing to
reconcile service" incident). On the primary side it is declared via the
primary Cluster CR's `spec.managed.services.additional[]` (CNPG owns it,
annotated `service.cilium.io/global: "true"`); on the replica side a
same-named stub Service (zero local backends) lets Cilium merge the two
into one global service so the host resolves and traffic crosses the mesh.
*/}}
{{- define "bp-postgres.replicationServiceName" -}}
{{- printf "%s-mesh" (include "bp-postgres.instanceName" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
ClusterMesh-global WRITE Service alias the cross-region CONSUMER apps
(grafana/keycloak/powerdns-admin) dial so their DB host resolves identically
in BOTH regions and always routes to the CURRENT primary's write endpoint.

The design's repeated promise — "the consumer host `<instance>-rw` is
unchanged across regions" (16a/16c headers, cluster.yaml) — was never met:
CNPG's auto-created `<instance>-rw` Service is region-LOCAL (it exists only
where the named primary Cluster runs), and the existing `<instance>-mesh`
global Service is `selectorType: r` (READ-only), so a consumer in the replica
region that points at `<instance>-rw` gets NXDOMAIN (#3629: grafana/keycloak/
powerdns-admin crashlooped on `shared-pg-*-rw.shared-data` in region-B).

Named `<instance>-mesh-rw` (NOT `<instance>-rw`, which collides with CNPG's
auto-created `-rw` Service — the bp-cnpg-pair "refusing to reconcile service,
not owned by the cluster" incident). On the primary side it is declared via
the primary Cluster CR's `spec.managed.services.additional[]` with
`selectorType: rw` (CNPG ≥1.22), annotated `service.cilium.io/global: "true"`;
on the replica side a same-named stub (zero local backends) lets Cilium merge
the two into one global service so the host resolves and WRITES cross the mesh
to the primary's primary instance. Promotion (bp-continuum flips the CR sides)
re-homes the `rw` selector to whichever region is primary, so the consumer
host needs no change on failover.
*/}}
{{- define "bp-postgres.writeServiceName" -}}
{{- printf "%s-mesh-rw" (include "bp-postgres.instanceName" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Sanitise an arbitrary string into an RFC 1123 DNS-subdomain-safe name
fragment so it can be used in a k8s resource name. PostgreSQL identifiers
(role/database names) legally contain underscores (e.g. `openova_flow`),
but k8s object names may NOT — a Secret named `shared-pg-c-openova_flow`
is rejected with `metadata.name: Invalid value … must be lowercase RFC
1123`. This lowercases and replaces every run of non-[a-z0-9-] with a
single `-`, then trims leading/trailing `-`. (#3375 hw133 — bp-postgres-
shared-c was Stalled on exactly this, which blocked the whole
bp-catalyst-platform dependsOn chain.)
*/}}
{{- define "bp-postgres.k8sName" -}}
{{- regexReplaceAll "[^a-z0-9-]+" (lower (toString .)) "-" | trimAll "-" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
The Secret name CNPG reconciles a managed role's password FROM. When a
binding omits `passwordSecret`, default to `<instance>-<owner>` so a
CNPG-bootstrapped role Secret (or a same-named consumer Secret) lines up.
The owner segment is sanitised (k8sName) so an owner like `openova_flow`
yields the valid `…-openova-flow` instead of an underscore'd invalid name.
*/}}
{{- define "bp-postgres.roleSecretName" -}}
{{- $instance := include "bp-postgres.instanceName" .ctx -}}
{{- if .db.passwordSecret -}}
{{- .db.passwordSecret -}}
{{- else -}}
{{- printf "%s-%s" $instance (include "bp-postgres.k8sName" .db.owner) -}}
{{- end -}}
{{- end -}}

{{/* Common labels applied to every resource the chart emits. */}}
{{- define "bp-postgres.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: bp-postgres
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-postgres
catalyst.openova.io/data-instance: {{ include "bp-postgres.instanceName" . | quote }}
{{- end -}}
