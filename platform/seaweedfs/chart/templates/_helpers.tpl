{{/*
Catalyst-curated helpers for bp-seaweedfs. Mirrors the conventions used
by bp-cilium / bp-cert-manager / bp-external-dns / bp-powerdns.
*/}}

{{- define "bp-seaweedfs.fullname" -}}
{{- default "seaweedfs" .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "bp-seaweedfs.labels" -}}
app.kubernetes.io/name: {{ include "bp-seaweedfs.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-seaweedfs
catalyst.openova.io/component: seaweedfs
{{- end -}}

{{- define "bp-seaweedfs.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-seaweedfs.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- /*
──────────────────────────────────────────────────────────────────────────
Cross-region filer sync helpers — ADR-0015 D6(a), issue #7067.
See values.yaml `crossRegion` for the contract.
──────────────────────────────────────────────────────────────────────────
*/ -}}

{{- /* Subchart fullname — mirrors the vendored `seaweedfs.fullname`
       (charts/seaweedfs/templates/shared/_helpers.tpl) evaluated in the
       subchart's own context, so every Service name we dial matches what
       the upstream chart rendered (release `seaweedfs` -> `seaweedfs`). */ -}}
{{- define "bp-seaweedfs.subFullname" -}}
{{- $sub := .Values.seaweedfs | default dict -}}
{{- if $sub.fullnameOverride -}}
{{- $sub.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default "seaweedfs" $sub.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- /* This region's id: ADR-0015 D2 `openova.topology.self` wins, else the
       bootstrap-kit seam `crossRegion.self`. */ -}}
{{- define "bp-seaweedfs.topologySelf" -}}
{{- $topo := (.Values.openova | default dict).topology | default dict -}}
{{- $cr := .Values.crossRegion | default dict -}}
{{- coalesce $topo.self $cr.self "" | toString | trim -}}
{{- end -}}

{{- /* Peer region ids, comma-joined (an include can only return a string;
       a comma split keeps us off fromJsonArray for old helm-controller SDKs).
       ADR-0015 D2 `openova.topology.peers` wins; else
       crossRegion.configuredRegions minus self. Empties / dupes / self dropped. */ -}}
{{- define "bp-seaweedfs.topologyPeers" -}}
{{- $topo := (.Values.openova | default dict).topology | default dict -}}
{{- $cr := .Values.crossRegion | default dict -}}
{{- $self := include "bp-seaweedfs.topologySelf" . -}}
{{- $src := $topo.peers | default list -}}
{{- if eq (len $src) 0 -}}{{- $src = $cr.configuredRegions | default list -}}{{- end -}}
{{- $out := list -}}
{{- range $src -}}
{{- $r := . | toString | trim -}}
{{- if and $r (ne $r $self) (not (has $r $out)) -}}{{- $out = append $out $r -}}{{- end -}}
{{- end -}}
{{- join "," $out -}}
{{- end -}}

{{- /* "true" when the cross-region filer sync renders at all:
       crossRegion.enabled AND filerSync.enabled AND self known AND >=1 peer. */ -}}
{{- define "bp-seaweedfs.filerSyncActive" -}}
{{- $cr := .Values.crossRegion | default dict -}}
{{- $fs := $cr.filerSync | default dict -}}
{{- $self := include "bp-seaweedfs.topologySelf" . -}}
{{- $peers := include "bp-seaweedfs.topologyPeers" . -}}
{{- if and $cr.enabled $fs.enabled $self $peers -}}true{{- end -}}
{{- end -}}

{{- /* Per-region ClusterMesh global Service name for the filer.
       Arg: list <ctx> <regionId>. */ -}}
{{- define "bp-seaweedfs.filerMeshName" -}}
{{- $ctx := index . 0 -}}
{{- $region := index . 1 -}}
{{- $prefix := (((($ctx.Values.crossRegion | default dict).filerMesh) | default dict).servicePrefix) | default "seaweedfs-filer-mesh" -}}
{{- $name := printf "%s-%s" $prefix $region -}}
{{- if not (regexMatch "^[a-z]([-a-z0-9]*[a-z0-9])?$" $name) -}}
{{- fail (printf "bp-seaweedfs: filer mesh Service name %q is not a DNS-1035 label (region id %q)" $name $region) -}}
{{- end -}}
{{- if gt (len $name) 63 -}}
{{- fail (printf "bp-seaweedfs: filer mesh Service name %q exceeds 63 chars — shorten crossRegion.filerMesh.servicePrefix" $name) -}}
{{- end -}}
{{- $name -}}
{{- end -}}

{{- /* Labels for cross-region objects. Deliberately NOT
       `app.kubernetes.io/name: seaweedfs`, so the existing
       bp-seaweedfs-catalyst ServiceMonitor never selects these Services. */ -}}
{{- define "bp-seaweedfs.crossRegionLabels" -}}
app.kubernetes.io/part-of: seaweedfs
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-seaweedfs
catalyst.openova.io/subcomponent: filer-cross-region
{{- end -}}

{{- /* Image for the filer.sync processes — the same Harbor-proxied image as
       every other seaweedfs workload (host-ns Kyverno harbor-proxy-pull, #4347). */ -}}
{{- define "bp-seaweedfs.filerSyncImage" -}}
{{- $fs := (.Values.crossRegion | default dict).filerSync | default dict -}}
{{- $img := $fs.image | default dict -}}
{{- $g := (.Values.seaweedfs | default dict).global | default dict -}}
{{- $repo := $img.repository | default (printf "%s/%s" $g.registry ($g.imageName | default "seaweedfs")) -}}
{{- $tag := $img.tag | default $g.imageTag -}}
{{- printf "%s:%s" $repo ($tag | toString) -}}
{{- end -}}
