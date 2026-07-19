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
`{registry/}repository:tag@digest` (digest omitted when empty).

#4152 — host-qualified-repository skip:
  The chart's WordPress runtime image is now the custom
  `ghcr.io/openova-io/openova/wordpress-tenant-pg` build (official image +
  the pg4wp-required `pdo_pgsql`/`pgsql` extensions). That repository ALREADY
  carries a registry host. When `.Values.wordpress.image.repository` is
  host-qualified (its first "/"-segment contains a "." or ":", e.g.
  "ghcr.io/..."), this helper honours it VERBATIM and does NOT prepend
  `global.imageRegistry` — otherwise a harbor `global.imageRegistry` overlay
  would emit a double-registry ref like
  `harbor.openova.io/ghcr.io/openova-io/openova/wordpress-tenant-pg`, which is
  unpullable. On a Sovereign the containerd registry mirror already rewrites
  ghcr.io -> harbor.openova.io/proxy-ghcr transparently, so no chart-side
  prefix is needed for ghcr images (same model as build-bp-huawei-evs-csi).
  A BARE repository (the legacy dockerhub `wordpress`) is still prefixed by
  `global.imageRegistry` when set, byte-identical to the prior behaviour.
  Mirrors the canonical `bp-sso-bridge.image` host-qualified skip (#2940).
*/}}
{{- define "bp-wordpress-tenant.wordpressImage" -}}
{{- $reg := .Values.global.imageRegistry | default "" -}}
{{- $repo := .Values.wordpress.image.repository -}}
{{- $tag := .Values.wordpress.image.tag -}}
{{- $digest := .Values.wordpress.image.digest | default "" -}}
{{- $firstSeg := first (splitList "/" $repo) -}}
{{- $hostQualified := or (contains "." $firstSeg) (contains ":" $firstSeg) -}}
{{- $base := $repo -}}
{{- if and $reg (not $hostQualified) -}}
{{- $base = printf "%s/%s" $reg $repo -}}
{{- end -}}
{{- if $digest -}}
{{- printf "%s:%s@%s" $base $tag $digest -}}
{{- else -}}
{{- printf "%s:%s" $base $tag -}}
{{- end -}}
{{- end -}}

{{/*
Resolved ingress host. Templates `wordpress.<orgDomain>` when
`ingress.host` is empty; otherwise returns the operator-supplied host
verbatim. The `orgDomain` placeholder default in values.yaml ensures
the smoke-render pass succeeds; per-Sovereign overlays MUST override.
#4139: was `.Values.smeDomain` — a dead key after the #3985 SME→Organization
rename moved the values.yaml default to `orgDomain`, so the host-derivation
fallback rendered `wordpress.<no value>`. (Live HRs always set ingress.host
explicitly, so this only bit the chart-default render path.)
*/}}
{{- define "bp-wordpress-tenant.ingressHost" -}}
{{- if .Values.ingress.host -}}
{{- .Values.ingress.host -}}
{{- else -}}
{{- printf "wordpress.%s" .Values.orgDomain -}}
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
D31 / TBD-E8b: canonical operator-facing DB topology selector.

Resolves the chart's DB topology to one of:
  - "singleton"          (default — single in-vcluster CNPG Cluster CR)
  - "active-hot-standby" (D31 — primary + replica CNPG Cluster CRs,
                          WAL streaming over Cilium ClusterMesh; mirrors
                          bp-cnpg-pair shape — see platform/cnpg-pair/)

Precedence (operator-supplied `database.mode` wins; legacy
`pg.activeHotStandby.enabled` boolean folds as alias for clusters whose
orchestrator overlays haven't been re-rendered with the canonical
enum):

  1. `database.mode` is "active-hot-standby" → active-hot-standby
  2. `database.mode` is "singleton"          → singleton
  3. `database.mode` is empty/unset:
     - `pg.activeHotStandby.enabled=true`    → active-hot-standby
     - otherwise                              → singleton

The enum form (`database.mode`) is the canonical interface customers
see in the marketplace voucher → org wizard (D29). The boolean form
(`pg.activeHotStandby.enabled`) remains a back-compat alias and will
be removed in chart 0.4.0.
*/}}
{{- define "bp-wordpress-tenant.dbMode" -}}
{{- $mode := .Values.database.mode | default "" -}}
{{- if eq $mode "active-hot-standby" -}}
active-hot-standby
{{- else if eq $mode "singleton" -}}
singleton
{{- else if .Values.pg.activeHotStandby.enabled -}}
active-hot-standby
{{- else -}}
singleton
{{- end -}}
{{- end -}}

{{/*
D31 / TBD-E8b: boolean convenience predicate — returns "true" when the
resolved `dbMode` is "active-hot-standby". Used by cnpg-cluster.yaml
in place of the legacy `.Values.pg.activeHotStandby.enabled` boolean
so the canonical enum drives template rendering. Empty (falsy) when
mode resolves to "singleton".
*/}}
{{- define "bp-wordpress-tenant.dbModeActiveHotStandby" -}}
{{- if eq (include "bp-wordpress-tenant.dbMode" .) "active-hot-standby" -}}
true
{{- end -}}
{{- end -}}

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
`{registry/}repository:tag@digest` (digest omitted when empty).

#4152 — host-qualified-repository skip (same rule as wordpressImage):
  honours a host-qualified `oidc.cliImage.repository` (first "/"-segment
  contains "." or ":") verbatim and does NOT prepend `global.imageRegistry`.
  The default cliImage is the bare dockerhub `wordpress` (cli-* tag), so the
  prefix behaviour is byte-identical to before; the skip only fires if an
  overlay points cliImage at a host-qualified path.
*/}}
{{- define "bp-wordpress-tenant.wpCliImage" -}}
{{- $reg := .Values.global.imageRegistry | default "" -}}
{{- $repo := .Values.oidc.cliImage.repository -}}
{{- $tag := .Values.oidc.cliImage.tag -}}
{{- $digest := .Values.oidc.cliImage.digest | default "" -}}
{{- $firstSeg := first (splitList "/" $repo) -}}
{{- $hostQualified := or (contains "." $firstSeg) (contains ":" $firstSeg) -}}
{{- $base := $repo -}}
{{- if and $reg (not $hostQualified) -}}
{{- $base = printf "%s/%s" $reg $repo -}}
{{- end -}}
{{- if $digest -}}
{{- printf "%s:%s@%s" $base $tag $digest -}}
{{- else -}}
{{- printf "%s:%s" $base $tag -}}
{{- end -}}
{{- end -}}

{{/*
Resolved OIDC issuer URL. Resolution precedence (G117.5 W3.D3 #2744):

  1. Operator-supplied `oidc.issuerURL` non-placeholder → use verbatim.
  2. `sovereign.fqdn` + `org.realm` populated (G117 per-Org realm
     topology) → derive `https://auth.<sovereign.fqdn>/realms/<org.realm>`.
  3. `sovereign.fqdn` + `smeSubdomain` populated (legacy SME-fleet
     topology) → derive `https://auth.<sovereign.fqdn>/realms/sme-<sub>`.
  4. `oidc.issuerURL` placeholder AND legacy `keycloak.realmURL` set →
     fold legacy alias.
  5. Fall back to whatever `oidc.issuerURL` is (the smoke-render
     placeholder) so `helm template` continues to render valid YAML.

The placeholder is `https://keycloak.org.local/realms/org`.
*/}}
{{- define "bp-wordpress-tenant.oidcIssuerURL" -}}
{{- $modern := .Values.oidc.issuerURL -}}
{{- $legacy := .Values.keycloak.realmURL -}}
{{- /* #4139: lockstep with the values.yaml oidc.issuerURL placeholder default.
       The #3985 SME→Organization rename moved the values.yaml default
       sme.local/realms/sme → org.local/realms/org but left this constant
       on the old string — so `ne $modern $placeholder` was ALWAYS true and
       the legacy keycloak.realmURL fold (case 4) became unreachable, failing
       chart test oidc-config.sh Case 2 + silently blocking every publish
       since 0.4.1 (only 0.4.0/0.4.1 are on ghcr; 0.4.2 never shipped). */}}
{{- $placeholder := "https://keycloak.org.local/realms/org" -}}
{{- $sovFQDN := .Values.sovereign.fqdn | default "" -}}
{{- $orgRealm := .Values.org.realm | default "" -}}
{{- $smeSub := .Values.smeSubdomain | default "" -}}
{{- if ne $modern $placeholder -}}
{{- $modern -}}
{{- else if and $sovFQDN $orgRealm -}}
{{- printf "https://auth.%s/realms/%s" $sovFQDN $orgRealm -}}
{{- else if and $sovFQDN $smeSub -}}
{{- printf "https://auth.%s/realms/sme-%s" $sovFQDN $smeSub -}}
{{- else if $legacy -}}
{{- $legacy -}}
{{- else -}}
{{- $modern -}}
{{- end -}}
{{- end -}}

{{/*
G117.5 W3.D3: resolved Org realm name — fed into the AppRegistration
ConfigMap's `data.realm` field for bp-sso-bridge consumption.
Precedence: org.realm > smeSubdomain → `sme-<sub>` > "default".
*/}}
{{- define "bp-wordpress-tenant.orgRealm" -}}
{{- if .Values.org.realm -}}
{{- .Values.org.realm -}}
{{- else if .Values.smeSubdomain -}}
{{- printf "sme-%s" .Values.smeSubdomain -}}
{{- else -}}
default
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
