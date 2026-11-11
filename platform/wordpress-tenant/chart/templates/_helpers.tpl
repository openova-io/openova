{{/*
CNPG base name (ADR-0015 D2, Refs #7067). Resolution:
  1. `database.cnpgClusterName` when set explicitly (operator pin, legacy installs);
  2. `<openova.application.name>-db` when the application-controller renders the
     Application identity — region-independent and unique per app, so two
     WordPress Applications in one namespace never share a CNPG Cluster,
     mesh Service or shared-secret path;
  3. `wordpress-db`, the historical default, for a bare install without the contract.
*/}}
{{- define "bp-wordpress-tenant.cnpgBase" -}}
{{- $explicit := .Values.database.cnpgClusterName | default "" -}}
{{- $app := dig "openova" "application" "name" "" (.Values | toJson | fromJson) -}}
{{- if $explicit -}}{{ $explicit }}{{- else if $app -}}{{ printf "%s-db" $app }}{{- else -}}wordpress-db{{- end -}}
{{- end -}}

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
{{- include "openova.topology.memberNames" (dict "ctx" . "base" (include "bp-wordpress-tenant.cnpgBase" .) "overrides" (dig "topology" "clusterNames" dict (.Values.database | toJson | fromJson))) -}}
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
{{- dig "topology" "sharedSecrets" "replicationCA" "" (.Values.database | toJson | fromJson) | default (printf "%s-replication-ca" (include "bp-wordpress-tenant.cnpgBase" .)) -}}
{{- end -}}

{{/*
Secret-store path prefix of the shared material (0.5.1, Refs #7067). Keyed on
the PAIR identity `<namespace>/<cnpgClusterName>` — identical in every region.
NOT the release name: each region's HelmRelease is `<app>-<cluster>`, so a
release-keyed path differs per region and no peer ever finds it (the 0.5.0
default had exactly that defect).
*/}}
{{- define "bp-wordpress-tenant.sharedSecretsPrefix" -}}
{{- dig "topology" "sharedSecrets" "externalSecret" "remoteKeyPrefix" "" (.Values.database | toJson | fromJson) | default (printf "openova/apps/%s/%s" (include "bp-wordpress-tenant.cnpgNamespace" .) (include "bp-wordpress-tenant.cnpgBase" .)) -}}
{{- end -}}

{{- define "bp-wordpress-tenant.appCredentialsSecret" -}}
{{- dig "topology" "sharedSecrets" "appCredentials" "" (.Values.database | toJson | fromJson) | default (printf "%s-app-credentials" (include "bp-wordpress-tenant.cnpgBase" .)) -}}
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
for a planned move, `database.topology.promotionToken`) to the region files.
Two mechanisms make that commit; this chart itself makes none:

  continuum - ADR-0015 D8. The Continuum the application-controller renders
              into every region file of this Application (bp-continuum, slot
              62, running in EVERY region) commits the move phase by phase:
              the old primary's file first (CNPG demotes it and publishes
              status.demotionToken), then the new primary's file with
              `primary` + `promotionToken`, then the peers. On an UNPLANNED
              region loss the survivor commits `primary` without a token only
              when the Continuum's spec.transition.autoPromoteOnRegionLoss is
              true (default false: the loss is reported, never acted on).
  manual    - the sovereign-admin commits `openova.topology.primary=<survivor>`.

The chart must render NOTHING that fights those commits: no promoter, no
Continuum or Lease of its own, no hook that patches a Cluster. The render is a
pure function of the two committed fields, so the declaration changes only the
stamp below (scripts/check-dr-pairs-declare-promotion.sh diffs the renders).
Under `continuum` the CNPG Cluster must also stay where Continuum observes it:
the release namespace, labelled app.kubernetes.io/instance=<release>
(core/controllers/continuum/internal/observe). Empty FAILS; `dr-promoter`
fails closed (this chart ships no promoter Deployment). The value is stamped on
every non-primary Cluster CR (`catalyst.openova.io/dr-promotion-mechanism`).
*/}}
{{- define "bp-wordpress-tenant.drPromotionMechanism" -}}
{{- dig "topology" "promotion" "mechanism" "" (.Values.database | toJson | fromJson) | toString | trim -}}
{{- end -}}

{{- define "bp-wordpress-tenant.validatePromotion" -}}
{{- $m := include "bp-wordpress-tenant.drPromotionMechanism" . -}}
{{- if eq $m "" -}}
{{- fail "database.topology.promotion.mechanism is REQUIRED for a multi-region openova.topology (#5623): this chart renders a cross-region CNPG standby, and an undeclared pair is one that silently never promotes on a region kill. Set it to \"continuum\" (ADR-0015 D8: the Continuum in every region commits the move) or \"manual\" (the sovereign-admin commits openova.topology.primary=<surviving region>)." -}}
{{- end -}}
{{- if eq $m "dr-promoter" -}}
{{- fail "database.topology.promotion.mechanism=\"dr-promoter\" is NOT DELIVERABLE by bp-wordpress-tenant (#5623): this chart ships no dr-promoter Deployment. Declaring it would assert a failover that cannot happen. Use \"continuum\" (ADR-0015 D8)." -}}
{{- end -}}
{{- if eq $m "continuum" -}}
{{- $cnpgNs := include "bp-wordpress-tenant.cnpgNamespace" . -}}
{{- if ne $cnpgNs .Release.Namespace -}}
{{- fail (printf "database.topology.promotion.mechanism=\"continuum\" needs the CNPG Cluster in the release namespace %q (database.cluster.namespace=%q): Continuum observes status.demotionToken / lastPromotionToken on the Cluster labelled app.kubernetes.io/instance=<release> in the HelmRelease's target namespace, and would wait forever on a Cluster it cannot see (ADR-0015 D8)." .Release.Namespace $cnpgNs) -}}
{{- end -}}
{{- else if ne $m "manual" -}}
{{- fail (printf "database.topology.promotion.mechanism=%q is not a recognised mechanism (#5623). Valid: continuum | manual." $m) -}}
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

{{/*
ADR-0015 D6 — media store (Refs #7067). Returns "s3" or "pvc" and FAILS the
render on the two shapes D6 forbids:
  - media.store=pvc under a multi-region openova.topology: uploads would live
    on a region-local PVC that nothing replicates, so a primary move strands
    them in the old region;
  - an S3 bucket whose name bp-seaweedfs does not replicate (only `replicated`
    and `replicated-<suffix>` are synced cross-region).
*/}}
{{- define "bp-wordpress-tenant.mediaStore" -}}
{{- $m := .Values.media | default dict -}}
{{- $store := $m.store | default "s3" | toString | trim -}}
{{- if not (has $store (list "s3" "pvc")) -}}
{{- fail (printf "media.store=%q is not supported (ADR-0015 D6). Valid: s3 (replicated SeaweedFS bucket, default) | pvc (legacy single-region wp-content PVC)." $store) -}}
{{- end -}}
{{- if and (eq $store "pvc") (eq (include "bp-wordpress-tenant.multiRegion" .) "true") -}}
{{- fail "media.store=pvc is REFUSED for a multi-region openova.topology (ADR-0015 D6): uploads would live on the region-local `wp-content` PVC, which nothing replicates — after a primary move the new primary serves posts whose media exist only in the old region. Use media.store=s3 (the default: the replicated SeaweedFS bucket)." -}}
{{- end -}}
{{- if eq $store "s3" -}}
{{- $bucket := dig "s3" "bucket" "" $m | toString | trim -}}
{{- if not (regexMatch "^replicated(-[a-z0-9][a-z0-9-]*)?$" $bucket) -}}
{{- fail (printf "media.s3.bucket=%q is not a cross-region replicated bucket (ADR-0015 D6): bp-seaweedfs syncs only `replicated` and `replicated-<suffix>` between regions, so any other bucket is a region-local volume again." $bucket) -}}
{{- end -}}
{{- end -}}
{{- $store -}}
{{- end -}}

{{/* Object-key prefix of this Application's media: `<org>/<app>`. */}}
{{- define "bp-wordpress-tenant.mediaKeyPrefix" -}}
{{- $p := dig "s3" "keyPrefix" "" (.Values.media | default dict) | toString | trim | trimPrefix "/" | trimSuffix "/" -}}
{{- if $p -}}{{ $p }}{{- else -}}{{ printf "%s/%s" .Release.Namespace (include "bp-wordpress-tenant.fullname" .) }}{{- end -}}
{{- end -}}

{{/* Name of the reflected SeaweedFS S3 credentials Secret. */}}
{{- define "bp-wordpress-tenant.mediaS3SecretName" -}}
{{- $n := dig "s3" "credentials" "secretName" "" (.Values.media | default dict) | toString | trim -}}
{{- if $n -}}{{ $n }}{{- else -}}{{ printf "%s-media-s3" (include "bp-wordpress-tenant.fullname" .) | trunc 63 | trimSuffix "-" }}{{- end -}}
{{- end -}}

{{/* Name of the LEGACY region-local wp-content PVC. */}}
{{- define "bp-wordpress-tenant.wpContentPvcName" -}}
{{- printf "%s-wp-content" (include "bp-wordpress-tenant.fullname" .) -}}
{{- end -}}

{{/* "true" when the wp-content volume is the legacy PVC. */}}
{{- define "bp-wordpress-tenant.wpContentOnPvc" -}}
{{- if and (eq (include "bp-wordpress-tenant.mediaStore" .) "pvc") .Values.persistence.wpContent.enabled -}}true{{- end -}}
{{- end -}}
