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
{{- $side := default "primary" .Values.cnpgPair.side -}}
{{- if eq $side "secondary" -}}
{{- $side = "replica" -}}
{{- end -}}
{{- if and (ne $side "primary") (ne $side "replica") -}}
{{- fail (printf "cnpgPair.side must be one of primary|replica|secondary — got %q" .Values.cnpgPair.side) -}}
{{- end -}}
{{- $side -}}
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
{{- if and .Values.cnpgPair.enabled (include "cnpg-pair.isReplicaSide" .) $apEnabled (eq (.Values.cnpgPair.replication.mode | default "sync") "sync") -}}true{{- end -}}
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
