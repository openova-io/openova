{{/*
Expand the name of the chart.
*/}}
{{- define "bp-wordpress-tenant.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "bp-wordpress-tenant.fullname" -}}
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
Common labels — required by docs/BLUEPRINT-AUTHORING.md §14 and by the
Catalyst projector to track resources back to the Blueprint.
*/}}
{{- define "bp-wordpress-tenant.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "bp-wordpress-tenant.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-wordpress-tenant
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "bp-wordpress-tenant.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-wordpress-tenant.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "bp-wordpress-tenant.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "bp-wordpress-tenant.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
WordPress image reference, with optional `global.imageRegistry` rewrite
for Sovereign Harbor proxy-cache. Returns
`{registry/}repository:tag@digest` so consumers SHA-pin to the manifest-
list digest published on Docker Hub.
*/}}
{{- define "bp-wordpress-tenant.wordpressImage" -}}
{{- $reg := .Values.global.imageRegistry | default "" -}}
{{- $repo := .Values.wordpress.image.repository -}}
{{- $tag := .Values.wordpress.image.tag -}}
{{- $digest := .Values.wordpress.image.digest -}}
{{- if $reg -}}
{{- printf "%s/%s:%s@%s" $reg $repo $tag $digest -}}
{{- else -}}
{{- printf "%s:%s@%s" $repo $tag $digest -}}
{{- end -}}
{{- end -}}

{{/*
Resolved ingress host. Templates `wordpress.<smeDomain>` when
`ingress.host` is empty; otherwise returns the operator-supplied host
verbatim. The `smeDomain` placeholder default in values.yaml ensures
the smoke-render pass succeeds; per-Sovereign overlays MUST override.
*/}}
{{- define "bp-wordpress-tenant.ingressHost" -}}
{{- if .Values.ingress.host -}}
{{- .Values.ingress.host -}}
{{- else -}}
{{- printf "wordpress.%s" .Values.smeDomain -}}
{{- end -}}
{{- end -}}

{{/*
CNPG cluster namespace — defaults to .Release.Namespace if the
operator left `database.cluster.namespace` empty.
*/}}
{{- define "bp-wordpress-tenant.cnpgNamespace" -}}
{{- default .Release.Namespace .Values.database.cluster.namespace -}}
{{- end -}}

{{/*
CNPG-emitted application Secret name (`<cluster>-app`). CNPG synthesises
this Secret from the `Cluster.spec.bootstrap.initdb.owner` field at
install time.
*/}}
{{- define "bp-wordpress-tenant.cnpgAppSecret" -}}
{{- printf "%s-app" .Values.database.cnpgClusterName -}}
{{- end -}}

{{/*
CNPG-emitted read-write Service hostname. CNPG synthesises this Service
from the Cluster CR; suffix is `-rw` per the CNPG operator convention.

D31 active-hot-standby: WordPress always connects to the PRIMARY's RW
endpoint (`<cnpgClusterName>-rw`). After a cross-region switchover
Continuum K-Cont-2 flips `replica.enabled` on both Cluster CRs so the
former replica becomes the new primary and its `-rw` Service is what
the renamed-primary publishes — the hostname is unchanged from the
client side because the cnpgClusterName itself does not move.
*/}}
{{- define "bp-wordpress-tenant.cnpgRwHost" -}}
{{- printf "%s-rw.%s.svc.cluster.local" .Values.database.cnpgClusterName (include "bp-wordpress-tenant.cnpgNamespace" .) -}}
{{- end -}}

{{/*
─── D31 active-hot-standby helpers ──────────────────────────────────────
Mirror bp-cnpg-pair's naming + validation pattern (see
platform/cnpg-pair/chart/templates/_helpers.tpl).
*/}}

{{/*
D31: replica Cluster CR name. Suffix `-replica` matches bp-cnpg-pair.
Truncated to 63 chars per the K8s resource-name limit.
*/}}
{{- define "bp-wordpress-tenant.cnpgPairReplicaName" -}}
{{- printf "%s-replica" .Values.database.cnpgClusterName | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
D31: ClusterMesh global replication Service alias the replica uses to
reach the primary's read-replica endpoint over the mesh. Suffix `-mesh`
avoids collision with the auto-created `<cluster>-r` Service (lesson
from bp-cnpg-pair chart 0.1.0 -> 0.1.1, Phase-2 incident #3 in
qa-loop-state/incidents.md).
*/}}
{{- define "bp-wordpress-tenant.cnpgPairReplicationServiceName" -}}
{{- printf "%s-mesh" .Values.database.cnpgClusterName | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
D31: validate primary + replica regions when active-hot-standby is on.
Both MUST be set AND differ (a same-region pair is degenerate — no
failure-isolation gain). Triggered only when enabled=true.
*/}}
{{- define "bp-wordpress-tenant.validateActiveHotStandbyRegions" -}}
{{- $p := required "pg.activeHotStandby.primaryRegion is REQUIRED when pg.activeHotStandby.enabled=true" .Values.pg.activeHotStandby.primaryRegion -}}
{{- $r := required "pg.activeHotStandby.replicaRegion is REQUIRED when pg.activeHotStandby.enabled=true" .Values.pg.activeHotStandby.replicaRegion -}}
{{- if eq $p $r -}}
{{- fail (printf "pg.activeHotStandby.primaryRegion (%s) MUST NOT equal pg.activeHotStandby.replicaRegion (%s) — active-hot-standby requires two distinct regions" $p $r) -}}
{{- end -}}
{{- end -}}

{{/*
wp-cli image reference, with optional `global.imageRegistry` rewrite for
Sovereign Harbor proxy-cache. Mirrors `wordpressImage` so both runtime
and CLI images route through the same proxy. Returns
`{registry/}repository:tag@digest`.
*/}}
{{- define "bp-wordpress-tenant.wpCliImage" -}}
{{- $reg := .Values.global.imageRegistry | default "" -}}
{{- $repo := .Values.oidc.cliImage.repository -}}
{{- $tag := .Values.oidc.cliImage.tag -}}
{{- $digest := .Values.oidc.cliImage.digest -}}
{{- if $reg -}}
{{- printf "%s/%s:%s@%s" $reg $repo $tag $digest -}}
{{- else -}}
{{- printf "%s:%s@%s" $repo $tag $digest -}}
{{- end -}}
{{- end -}}

{{/*
Resolved OIDC issuer URL. Folds the legacy `keycloak.realmURL` into
`oidc.issuerURL` for clusters whose orchestrator overlays haven't been
re-rendered with the canonical `oidc.*` block. Operator-supplied
`oidc.issuerURL` always wins; only when it equals the values.yaml
placeholder ("https://keycloak.sme.local/realms/sme") AND a non-empty
`keycloak.realmURL` is present does the fallback take effect.
*/}}
{{- define "bp-wordpress-tenant.oidcIssuerURL" -}}
{{- $modern := .Values.oidc.issuerURL -}}
{{- $legacy := .Values.keycloak.realmURL -}}
{{- $placeholder := "https://keycloak.sme.local/realms/sme" -}}
{{- if and (eq $modern $placeholder) $legacy -}}
{{- $legacy -}}
{{- else -}}
{{- $modern -}}
{{- end -}}
{{- end -}}

{{/*
Resolved OIDC clientId. Same fold pattern as oidcIssuerURL.
*/}}
{{- define "bp-wordpress-tenant.oidcClientId" -}}
{{- $modern := .Values.oidc.clientId -}}
{{- $legacy := .Values.keycloak.clientID -}}
{{- if and (eq $modern "wordpress") $legacy -}}
{{- $legacy -}}
{{- else -}}
{{- $modern -}}
{{- end -}}
{{- end -}}

{{/*
Resolved OIDC clientSecretName. Same fold pattern as oidcIssuerURL.
*/}}
{{- define "bp-wordpress-tenant.oidcClientSecretName" -}}
{{- $modern := .Values.oidc.clientSecretName -}}
{{- $legacy := .Values.keycloak.clientSecretName -}}
{{- if and (eq $modern "wordpress-oidc-client-secret") $legacy -}}
{{- $legacy -}}
{{- else -}}
{{- $modern -}}
{{- end -}}
{{- end -}}
