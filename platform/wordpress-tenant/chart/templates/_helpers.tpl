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
{{- with (include "openova.topology.labels" .) }}
{{ . }}
{{- end }}
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
─── Topology (ADR-0015 D2) ──────────────────────────────────────────────
The chart consumes ONLY the shared contract `openova.topology.{mode,self,
primary,peers}` through the topology-lib library (platform/topology-lib).
Each region's HelmRelease sets `self` to that region and the release
renders ONLY its own half: one CNPG Cluster, named per region.

Supported modes: singleton | active-passive | active-hot-standby. For
WordPress the two multi-region modes render the SAME shape (see
cnpg-cluster.yaml + deployment.yaml): a synchronous CNPG distributed
topology, WordPress serving only in the primary region. active-active is
refused — WordPress is a single-writer application.

Contract ABSENT ⇒ the pre-contract single-region render (one Cluster named
`database.cnpgClusterName`, no replication fields), byte-for-byte the
singleton shape every existing install has.

REMOVED in 0.5.0: `database.mode` and `pg.activeHotStandby.*`. They rendered
both CNPG halves into ONE release/cluster separated only by node affinity —
the replica was unschedulable in region A for 58 days on hw307 and, with
`dataDurability: required`, every write blocked. Setting either now FAILS the
render with the migration instruction instead of silently dropping HA.
*/}}
{{- define "bp-wordpress-tenant.topology" -}}
{{- $pg := .Values.pg | default dict -}}
{{- $ahs := $pg.activeHotStandby | default dict -}}
{{- if or $ahs.enabled (ne (dig "mode" "" (.Values.database | default dict) | toString) "") -}}
{{- fail "bp-wordpress-tenant 0.5.0 (ADR-0015 D2): `database.mode` and `pg.activeHotStandby.*` are REMOVED. Express placement through the shared contract instead — one HelmRelease per region with `openova.topology: {mode: active-passive|active-hot-standby, self: <this region-id>, primary: <primary region-id>, peers: [<other region-ids>]}` (see platform/wordpress-tenant/chart/README.md §Migration). Remove database.mode / pg.activeHotStandby from the values." -}}
{{- end -}}
{{- include "openova.topology.assertMode" (dict "ctx" . "supported" (list "singleton" "active-passive" "active-hot-standby")) -}}
{{- include "openova.topology.get" . -}}
{{- end -}}

{{/* "true" when this release renders the Primary / serving half. */}}
{{- define "bp-wordpress-tenant.isPrimary" -}}
{{- if (include "bp-wordpress-tenant.topology" . | fromJson).isPrimary -}}true{{- end -}}
{{- end -}}

{{/* "true" when the contract declares a multi-region mode. */}}
{{- define "bp-wordpress-tenant.multiRegion" -}}
{{- if (include "bp-wordpress-tenant.topology" . | fromJson).multiRegion -}}true{{- end -}}
{{- end -}}

{{/*
Per-region CNPG Cluster names — JSON {self, primary, peers[], source}.

NAMING SCHEME: `<database.cnpgClusterName>-<sha256(region-id)[0:8]>`, e.g.
`wordpress-db-` + first 8 hex of sha256("hw-me-east-215-a-rtz-prod"). Stable
(a pure function of the region id), DNS-safe, ≤ 50 chars (the CNPG Cluster
name ceiling; the base is truncated to 41), unique per region, and computable
by any orchestrator without the chart. The contract-absent render keeps the
bare `database.cnpgClusterName`.

`database.topology.clusterNames` (region-id → name) overrides the derived
name — the migration seam that lets an existing Cluster keep its name (and
its data) when its release adopts the contract.
*/}}
{{- define "bp-wordpress-tenant.dbNames" -}}
{{- $_ := include "bp-wordpress-tenant.topology" . -}}
{{- include "openova.topology.memberNames" (dict "ctx" . "base" .Values.database.cnpgClusterName "overrides" (dig "topology" "clusterNames" dict (.Values.database | toJson | fromJson))) -}}
{{- end -}}

{{/* THIS region's CNPG Cluster name. */}}
{{- define "bp-wordpress-tenant.cnpgSelfName" -}}
{{- (include "bp-wordpress-tenant.dbNames" . | fromJson).self -}}
{{- end -}}

{{/*
Shared Secrets of a multi-region topology — the SAME bytes in every region:
  replicationCA  — `ca.crt` + `ca.key`; every member pins it as
                   `spec.certificates.clientCASecret`, so CNPG issues each
                   member's `<cluster>-replication` streaming client cert from
                   ONE CA and every member accepts every other member's stream.
  appCredentials — `kubernetes.io/basic-auth` (`username`, `password`) for the
                   WordPress DB owner, used by `bootstrap.{initdb,pg_basebackup}.secret`.
                   The role password lives in the replicated database, so every
                   region must hold the same password or WordPress cannot log in
                   after a primary move.
*/}}
{{- define "bp-wordpress-tenant.replicationCASecret" -}}
{{- dig "topology" "sharedSecrets" "replicationCA" "" (.Values.database | toJson | fromJson) | default (printf "%s-replication-ca" .Values.database.cnpgClusterName) -}}
{{- end -}}

{{- define "bp-wordpress-tenant.appCredentialsSecret" -}}
{{- dig "topology" "sharedSecrets" "appCredentials" "" (.Values.database | toJson | fromJson) | default (printf "%s-app-credentials" .Values.database.cnpgClusterName) -}}
{{- end -}}

{{/*
CNPG namespace — defaults to .Release.Namespace if the
operator left `database.cluster.namespace` empty.
*/}}
{{- define "bp-wordpress-tenant.cnpgNamespace" -}}
{{- default .Release.Namespace .Values.database.cluster.namespace -}}
{{- end -}}

{{/*
DB password Secret the runtime + hook Jobs read (key `password`).
  - single-region: the CNPG-emitted `<cluster>-app` Secret (#4936 — bound
    directly, no reflector mirror).
  - multi-region:  the shared `appCredentials` Secret (identical in every
    region; CNPG uses it as the owner password at bootstrap).
*/}}
{{- define "bp-wordpress-tenant.cnpgAppSecret" -}}
{{- if include "bp-wordpress-tenant.multiRegion" . -}}
{{- include "bp-wordpress-tenant.appCredentialsSecret" . -}}
{{- else -}}
{{- printf "%s-app" (include "bp-wordpress-tenant.cnpgSelfName" .) -}}
{{- end -}}
{{- end -}}

{{/*
CNPG `-rw` Service of THIS region's Cluster. WordPress runs only where this
Cluster is the primary (deployment.yaml), so `-rw` is always writable when a
Pod uses it — no cross-region DB round-trips.
*/}}
{{- define "bp-wordpress-tenant.cnpgRwHost" -}}
{{- printf "%s-rw.%s.svc.cluster.local" (include "bp-wordpress-tenant.cnpgSelfName" .) (include "bp-wordpress-tenant.cnpgNamespace" .) -}}
{{- end -}}

{{/*
#5623 — a multi-region pair MUST DECLARE ITS PROMOTION MECHANISM.

Under ADR-0015 a promotion is a Git commit of `openova.topology.primary` (plus,
for a planned move, `database.topology.promotionToken`) to the surviving
region's values. Nothing in THIS chart makes that commit on an unplanned region
kill: the Continuum actor that will (ADR-0015 D8, running in every region) is
not delivered yet. So the only honest mechanism today is `manual` — the
sovereign-admin commits the new primary. Empty FAILS; `dr-promoter` /
`continuum` fail closed naming what is missing. The value is stamped on every
non-primary Cluster CR (`catalyst.openova.io/dr-promotion-mechanism`).
*/}}
{{- define "bp-wordpress-tenant.drPromotionMechanism" -}}
{{- dig "topology" "promotion" "mechanism" "" (.Values.database | toJson | fromJson) | toString | trim -}}
{{- end -}}

{{- define "bp-wordpress-tenant.validatePromotion" -}}
{{- $m := include "bp-wordpress-tenant.drPromotionMechanism" . -}}
{{- if eq $m "" -}}
{{- fail "database.topology.promotion.mechanism is REQUIRED for a multi-region openova.topology (#5623): this chart renders a cross-region CNPG standby, and an undeclared pair is one that silently never promotes on a region kill. Set it to \"manual\" (the sovereign-admin commits openova.topology.primary=<surviving region> — the only mechanism this chart delivers today)." -}}
{{- end -}}
{{- if eq $m "dr-promoter" -}}
{{- fail "database.topology.promotion.mechanism=\"dr-promoter\" is NOT DELIVERABLE by bp-wordpress-tenant (#5623): this chart ships no dr-promoter Deployment. Declaring it would assert a failover that cannot happen." -}}
{{- end -}}
{{- if eq $m "continuum" -}}
{{- fail "database.topology.promotion.mechanism=\"continuum\" is NOT DELIVERABLE yet (#5623, ADR-0015 D8): the every-region Continuum actor that commits the next primary on a region loss is not shipped." -}}
{{- end -}}
{{- if ne $m "manual" -}}
{{- fail (printf "database.topology.promotion.mechanism=%q is not a recognised mechanism (#5623). Valid: manual | dr-promoter | continuum." $m) -}}
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
