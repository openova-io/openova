{{/*
Expand the name of the chart.
*/}}
{{- define "bp-newapi.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "bp-newapi.fullname" -}}
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
{{- define "bp-newapi.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "bp-newapi.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
catalyst.openova.io/blueprint: bp-newapi
{{- end }}

{{/*
Hook-resource labels (#3374). Identical to bp-newapi.labels EXCEPT
`app.kubernetes.io/managed-by: flux` instead of Helm. Helm hook Jobs/CronJobs
are created by the helm-controller with no HelmRelease ownerReference, so the
kyverno `flux-managed` Enforce policy DENIES them unless they carry
managed-by=flux. We can't simply append a second `managed-by` line after
`bp-newapi.labels` — that yields a DUPLICATE map key that strict-YAML
post-render (`error while running post render ... mapping key ... already
defined`, caught live on hw133 with bp-newapi 1.4.71) rejects even though
`helm template` renders it last-wins. This single-source helper emits the key
ONCE with the flux value.
*/}}
{{- define "bp-newapi.hookLabels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "bp-newapi.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: flux
catalyst.openova.io/blueprint: bp-newapi
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "bp-newapi.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bp-newapi.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Placement-role render gates (#3831).

newapi is the ONLY NorthStar-#1 app that OWNS a dedicated CNPG cluster rather
than CONSUMING a reflected shared-pg secret. To re-home it INTO the mgmt
vCluster (every app in a vCluster) while its CNPG-owner seams stay host-side,
the chart renders in one of three roles, selected by `.Values.placement.role`:

  all          (DEFAULT) — render EVERYTHING. Every standalone install and
               every host-placed Sovereign keeps the historic single-HR
               behaviour; both gates below return "true" so they are no-ops.
  host-seams   — render ONLY the host-reconciled seams: the CNPG Cluster +
               DSN-placeholder Secret + database-secret-sync Job (+ their RBAC) +
               the newapi-admin AppRegistration ConfigMap + the oidc-sync
               ExternalSecret + the catalyst-newapi-admin-token ExternalSecret.
               NO Deployment / Service / HTTPRoute / Ingress / Application CR.
               NOT the admin-sso-seed Job/CronJob — those operate on the migrated
               schema + rollout-restart the app Deployment, so they render WITH
               the app (vcluster-app), reading the CNPG `-app` + OIDC Secrets
               mirrored host→vCluster (#3831 deadlock fix, Refs #3858).
  vcluster-app — render ONLY the app: Deployment + Service + (HTTPRoute via the
               host-bridge) + NetworkPolicy + the Application CR + the
               in-cluster placeholder Secrets the Pod reads (credentials,
               token-signing-key) + the admin-sso-seed Job/CronJob + the
               channel-seed Job (both operate on the app's migrated DB schema).
               NO CNPG / DSN-sync Job / ExternalSecrets / AppRegistration (those
               render host-side under host-seams; the host→vCluster secret mirror
               + replicateServices deliver the DSN/`-app`/OIDC Secrets + CNPG
               Service into the vCluster).

The two HRs share `releaseName: newapi` + chart `bp-newapi` (in DIFFERENT
clusters — host k3s vs vc-mgmt apiserver, so NO Helm-storage collision), so
`bp-newapi.fullname` resolves identically (`newapi-bp-newapi`) on both sides
and every cross-render object name (DSN Secret, CNPG Cluster) lines up.

Helm has no boolean return; these emit the strings "true"/"false". Test with
`{{- if eq (include "bp-newapi.renderSeams" .) "true" }}`.
*/}}
{{- define "bp-newapi.placementRole" -}}
{{- (.Values.placement | default dict).role | default "all" -}}
{{- end -}}
{{- define "bp-newapi.renderSeams" -}}
{{- $r := include "bp-newapi.placementRole" . -}}
{{- if or (eq $r "all") (eq $r "host-seams") -}}true{{- else -}}false{{- end -}}
{{- end -}}
{{- define "bp-newapi.renderApp" -}}
{{- $r := include "bp-newapi.placementRole" . -}}
{{- if or (eq $r "all") (eq $r "vcluster-app") -}}true{{- else -}}false{{- end -}}
{{- end -}}

{{/*
Cross-region singleton gates (#5599).

THE DEFECT. Bootstrap-kit slot 80 installs this chart on EVERY region's control
plane, so a 2-region Sovereign ran TWO complete newapi stacks — two Deployments
AND two INDEPENDENT CNPG `Cluster` objects (NOT a replicated pair) — behind ONE
`newapi.<fqdn>` hostname whose shared VIP fans a browser's parallel connections
across both gateways (#5459: a browser can never be assumed to pin one backend).

An OAuth **authorization code is single-use and stored SERVER-SIDE**. The leg
that mints the exchange state writes it into region-A's Postgres; the
`/api/oauth/sovereign` callback lands on region-B, whose database has never seen
that code, and region-B CORRECTLY rejects it — a *valid* Keycloak code answered
403, three times as the client retries and loses the coin flip again (#5599,
measured live on hw292 dep 1c56518035a83e03: region-a
`newapi-bp-newapi-newapi-pg` instances=2 AND region-b
`newapi-bp-newapi-newapi-pg` instances=2, both Ready, both fronted by
httproute `newapi-bp-newapi-public` → ["newapi.hw292.omani.works"]).

#5414 already fixed the SESSION_SECRET/CRYPTO_SECRET split at the SECRET seam
(#5466's credsBridgeSynced above). The DATASTORE split underneath it was never
addressed, so the component still could not hold a server-side OAuth exchange
across regions.

THE FIX — the bp-guacamole 0.2.36 (#5358) / bp-cilium 1.4.19 (#5602) idiom:
  role=primary   — keeps the Deployment + its CNPG store and EXPORTS its
                   Services as global ClusterMesh services.
  role=secondary — renders NO Deployment, NO CNPG Cluster, NO DSN-sync /
                   seed Jobs. Its Services still render (Cilium merges global
                   Services by name+namespace, the HTTPRoute backendRef must
                   resolve, and this region's envoy still has to answer for
                   `newapi.<fqdn>` because the shared VIP fans TCP across BOTH
                   gateways) but match ZERO local Pods, so
                   `service.cilium.io/affinity: local` falls through the mesh
                   to the primary's singleton.

crossRegion.enabled=false (the DEFAULT, and every single-region Sovereign)
renders byte-identically to 1.4.150 — both gates below are no-ops.

Two SEPARATE gates because the two placement axes are orthogonal: under the
#3831 host/vCluster split the workload and the datastore live in DIFFERENT
clusters, so the cross-region suppression has to compose with whichever axis
this install is on rather than replace it.
*/}}
{{- define "bp-newapi.crossRegionRole" -}}
{{- $cr := .Values.crossRegion | default dict -}}
{{- $role := $cr.role | default "primary" -}}
{{- if not (has $role (list "primary" "secondary")) -}}
{{- fail (printf "bp-newapi: invalid crossRegion.role %q — must be \"primary\" (owns the singleton newapi + its Postgres) or \"secondary\" (ClusterMesh Service stub only)" $role) -}}
{{- end -}}
{{- $role -}}
{{- end -}}

{{/*
Mesh-secondary predicate (#5599). "true" when this region must run NO newapi
workload and NO newapi Postgres. Empty string otherwise.

Deliberately independent of `.Values.newapi.enabled` and of placement.role so
each caller keeps composing its OWN existing gate set — this only ever REMOVES
render, never adds it.
*/}}
{{- define "bp-newapi.isMeshSecondary" -}}
{{- $cr := .Values.crossRegion | default dict -}}
{{- if and $cr.enabled (eq (include "bp-newapi.crossRegionRole" .) "secondary") -}}
true
{{- end -}}
{{- end -}}

{{/*
Workload render gate (#5599). Composes the #3831 placement gate
(`bp-newapi.renderApp`) with the cross-region singleton gate: "true" only when
this install renders the app AND this region is not the mesh secondary.

Consumers: the Deployment (whose own DSN/credentials precedence chain still
applies ON TOP of this — see deployment.yaml), the admin-sso-seed Job/CronJob
and the channel-seed Job (both are post-install/post-upgrade HOOKS that talk to
the migrated DB schema and rollout-restart the Deployment; on a secondary with
neither present they would fail the HelmRelease and the mesh-stub Services
would never install).

The Services, HTTPRoute, ConfigMap, ServiceAccount and the region-local
token-signing-key Secret + its #5375 admin-token PushSecret deliberately stay
on plain `renderApp` — the Services ARE the mesh stub, and #5375's DR contract
requires every region to keep seeding its OWN OpenBao with its OWN bridge
ADMIN_SECRET so a promote finds a bearer.
*/}}
{{- define "bp-newapi.renderWorkloads" -}}
{{- if and (eq (include "bp-newapi.renderApp" .) "true") (not (include "bp-newapi.isMeshSecondary" .)) -}}true{{- else -}}false{{- end -}}
{{- end -}}

{{/*
Datastore render gate (#5599). Composes the #3831 seams gate
(`bp-newapi.renderSeams`) with the cross-region singleton gate.

Consumers: cnpg-cluster.yaml (the `Cluster` CR **and** its DSN-placeholder
Secret) and database-secret-sync-job.yaml. The sync Job is a post-install/
post-upgrade hook that polls CNPG's `<cluster>-app` Secret until it exists —
on a secondary with no Cluster it would poll to its budget, exit non-zero and
wedge the release, so the Job must go wherever the Cluster goes.
*/}}
{{- define "bp-newapi.renderDataStore" -}}
{{- if and (eq (include "bp-newapi.renderSeams" .) "true") (not (include "bp-newapi.isMeshSecondary" .)) -}}true{{- else -}}false{{- end -}}
{{- end -}}

{{/*
ClusterMesh global-Service annotations (#5599). Emitted by BOTH Services in
service.yaml (the main one and the `-bridge` one the HTTPRoute's Exact `/` and
`/login` rules target — annotating only one would leave the other path split,
which is the same bug in a narrower window).

`shared` is "true" ONLY on the primary: the secondary must never export a
backend set of its own, or the primary could land a session in the secondary
region and the exchange splits again in the other direction.
`affinity: local` prefers local backends and falls through to the peer only
when there are none — so the primary never bounces a request to the secondary,
and the secondary (zero local backends by design) always reaches the primary.
Nothing renders when crossRegion.enabled=false.

The `-}}` on the `if` below is load-bearing: without it the returned block opens
with an empty line, and `nindent` turns that into a whitespace-only line under
`annotations:`.
*/}}
{{- define "bp-newapi.crossRegionServiceAnnotations" -}}
{{- $cr := .Values.crossRegion | default dict -}}
{{- if $cr.enabled -}}
service.cilium.io/global: "true"
service.cilium.io/shared: {{ eq (include "bp-newapi.crossRegionRole" .) "primary" | quote }}
service.cilium.io/affinity: {{ $cr.affinity | default "local" | quote }}
{{- end }}
{{- end -}}

{{/*
#5466 / #5480 (A16 class) — is the region-consistent SESSION_SECRET/
CRYPTO_SECRET bridge carrier ACTIVE for this install?

credentials-secret.yaml's `lookup`-or-`randAlphaNum 64` is PER-CLUSTER: on a
2-region Sovereign each region's own Flux/Helm hits its own apiserver, the
lookup misses in the second region, and each region mints a DIFFERENT
SESSION_SECRET/CRYPTO_SECRET pair. newapi.<fqdn> is fanned at BOTH regions'
pods by one shared VIP, so a session cookie set by one region is rejected by
the other (`[sessions] ERROR! securecookie: the value is not valid`, hw291,
UAT rows 37/38). The fix rides the SAME carrier the sibling OIDC client
secret already uses (#3374): bp-sso-bridge publishes derived, per-KC-client
`session_secret`/`crypto_secret` properties into the OpenBao
sso/sovereign/<clientId> bundle (0.2.27), and every region's ExternalSecret
resolves that ONE bundle — never generated twice. Mirrors the #5416 fix for
the oauth2-proxy cookie secret (bp-oidc-gate 0.1.8 / bp-cilium 1.4.18).

"true" only when EVERY leg of the carrier is present for THIS install:
  - placement.role == all — the sovereign-admin install (slot 80, #4291
    de-vclustered): seams + app render in the SAME host cluster, so the
    ExternalSecret lands next to the Pod. host-seams renders no Pod;
    vcluster-app renders the Pod where ESO does NOT exist (the in-vCluster
    placeholder path is unchanged — same scoping as keycloak-client-secret).
  - adminUI keycloak mode + ssoBridgeSync.enabled + sovereignFQDN + a
    sovereign-realm issuer — the EXACT gate set under which
    sso-app-registration.yaml registers the KC client, which is what makes
    bp-sso-bridge mint the client and publish the bundle this consumes.
    A per-Org overlay (issuer realm org-<sub>, #4169) is excluded.
  - no operator credentials.existingSecret override (Principle #4 — BYO
    bytes always win).
  - the ESO CRD capability — without it the ExternalSecret cannot render
    and pointing the Pod at the bridge Secret would wedge it forever.
When "false" everything renders exactly as pre-#5466.
*/}}
{{- define "bp-newapi.credsBridgeSynced" -}}
{{- $kcMode := eq .Values.auth.adminUI.mode "keycloak" -}}
{{- $sync := .Values.auth.adminUI.keycloak.ssoBridgeSync -}}
{{- $issuer := .Values.auth.adminUI.keycloak.issuer | default "" -}}
{{- $issuerRealm := "" -}}
{{- if $issuer -}}
{{- $issuerRealm = regexReplaceAll ".*/realms/([^/?#]+).*" $issuer "${1}" -}}
{{- end -}}
{{- $issuerIsSovereign := or (not $issuer) (eq $issuerRealm "sovereign") -}}
{{- if and (eq (include "bp-newapi.placementRole" .) "all") $kcMode $sync.enabled .Values.sovereignFQDN $issuerIsSovereign (not .Values.credentials.existingSecret) (.Capabilities.APIVersions.Has "external-secrets.io/v1beta1") -}}true{{- else -}}false{{- end -}}
{{- end -}}

{{/*
#5466 — name of the bridge-published app-credentials Secret (ExternalSecret
target, creationPolicy Owner). Distinct from the chart-generated
`<fullname>-app-creds` (helm.sh/resource-policy: keep) so ESO needs no
ownership takeover of a helm-kept object — the same reasoning #5416 used
for the oidc-gate `-oidc` Secret.
*/}}
{{- define "bp-newapi.credsBridgeSecretName" -}}
{{- printf "%s-app-creds-oidc" (include "bp-newapi.fullname" .) -}}
{{- end -}}

{{/*
ServiceAccount name.
*/}}
{{- define "bp-newapi.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "bp-newapi.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
ConfigMap name (channel + policy config).
*/}}
{{- define "bp-newapi.configMapName" -}}
{{- printf "%s-config" (include "bp-newapi.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Sandbox-bridge env block (#3374). Shared by BOTH the native-sidecar
initContainer (the default since 2026-06-18) and the legacy plain-container
sidecar in deployment.yaml, so the two code paths stay byte-identical. Resolves
the token-signing-key Secret name with the SAME precedence as the Deployment's
$effTokenSigningKeySecret (explicit existingSecret > chart auto-provisioned
`<release>-token-signing-key`). The #3374 zero-click SSO env (provider slug +
deterministic authorize URL / client_id / scopes) is injected here so the
landing page builds the Keycloak redirect directly (NewAPI v0.13.2's
/api/status omits custom_oauth_providers, so runtime discovery is impossible).
*/}}
{{- define "bp-newapi.sandboxBridgeEnv" -}}
{{- $tk := .Values.sandboxTokenSigningKey | default dict -}}
{{- $effTokenSigningKeySecret := $tk.existingSecret -}}
{{- if and (not $effTokenSigningKeySecret) (default true $tk.autoProvision) -}}
{{- $effTokenSigningKeySecret = default (printf "%s-token-signing-key" (include "bp-newapi.fullname" .)) $tk.autoSecretName -}}
{{- end -}}
- name: BRIDGE_LISTEN
  value: ":{{ .Values.sandboxBridge.port }}"
- name: NEWAPI_TOKEN_SIGNING_KEY
  valueFrom:
    secretKeyRef:
      name: {{ $effTokenSigningKeySecret }}
      key: SIGNING_KEY
- name: NEWAPI_ADMIN_SECRET
  valueFrom:
    secretKeyRef:
      name: {{ $effTokenSigningKeySecret }}
      key: ADMIN_SECRET
{{- /* #3374 — the OIDC provider slug the zero-click bare-URL landing page
   (served by this sidecar at `/`, routed by the HTTPRoute's Exact `/` rule)
   initiates. Matches the admin-sso-seed-job's custom_oauth_providers.slug.
   #3648 — standard sso.bootstrap contract first, then legacy adminSeed, then
   the literal default "sovereign". */}}
- name: NEWAPI_SSO_INIT_SLUG
  value: {{ ((.Values.sso | default dict).bootstrap | default dict).providerSlug | default (.Values.adminSeed | default dict).providerSlug | default "sovereign" | quote }}
{{- /*
   #3374 0.1.16 — the landing page builds the authorize redirect DIRECTLY from
   these values instead of discovering them at runtime from GET /api/status
   (NewAPI v0.13.2's /api/status has NO custom_oauth_providers field → the old
   discovery returned [] → the page fell through to /login → the SPA bounced
   the owner to /setup). These are the IDENTICAL values the admin-sso-seed-job
   seeds into custom_oauth_providers. When sovereignFQDN is unset the
   seed/landing are no-ops anyway (the page degrades to the SPA /login link). */}}
{{- $sk := .Values.adminSeed | default dict }}
{{- $ssoBoot := (.Values.sso | default dict).bootstrap | default dict }}
{{- if .Values.sovereignFQDN }}
{{- $slug := $ssoBoot.providerSlug | default $sk.providerSlug | default "sovereign" }}
- name: NEWAPI_SSO_AUTHORIZE_URL
  value: {{ printf "https://auth.%s/realms/%s/protocol/openid-connect/auth?kc_idp_hint=catalyst-pin" .Values.sovereignFQDN $slug | quote }}
- name: NEWAPI_SSO_CLIENT_ID
  value: {{ .Values.auth.adminUI.keycloak.clientId | default "newapi-admin" | quote }}
- name: NEWAPI_SSO_SCOPES
  value: {{ $sk.scopes | default "openid profile email groups" | quote }}
{{- end }}
{{- end -}}

{{/*
Effective channel list — composed default channels plus
`.Values.channels`. Composition order matters because NewAPI's
channel-router resolves `model` lookups in row-insertion order:
  1. defaultChannels.qwenPartner        (issue #915 — channel #1)
  2. .Values.channels                    (operator-supplied)
  3. defaultChannels.vllm                (in-cluster vLLM fallback)
A fresh customer landing on a fresh Sovereign with no
`.Values.channels` set hits qwenPartner first; this is the
documented "channel #1 = Qwen partner-hosted" contract from epic #915
(per-Org alice → NewAPI → partner-hosted Qwen end-to-end DoD).
Centralised so configmap.yaml + assertChannelAttestation +
channel-seed-job.yaml operate on the same materialised list.
*/}}
{{- define "bp-newapi.effectiveChannels" -}}
{{- $channels := list -}}
{{- $dc := .Values.defaultChannels | default dict -}}
{{/* ── Channel #1: Qwen partner-hosted (#915) ──────────────────
     Attestation gate (PR #1631 follow-up, 2026-05-18): franchised
     Sovereigns set `MARKETPLACE_ENABLED=true` to flip qp.enabled true,
     but cloud-init may not yet have `LLM_PARTNER_ACCOUNT_ID` /
     `LLM_PARTNER_CONTRACT_REF` set (no commercial contract signed
     yet for that Sovereign). The envsubst defaults leave accountId /
     contractRef as empty strings, which makes `assertChannelAttestation`
     fail the install with `commercial-contract attestation requires
     accountId`.

     Fix: SKIP composing the qwenPartner channel when
     attestation.kind=commercial-contract AND accountId/contractRef are
     empty. The Sovereign installs newapi with zero default channels
     (operator-supplied channels still compose). Once the commercial
     contract lands, the operator overlays the attestation values and
     the channel composes on the next reconcile. */}}
{{- $qp := $dc.qwenPartner | default dict -}}
{{- $qpAtt := $qp.attestation | default (dict "kind" "commercial-contract") -}}
{{- $qpAttReady := true -}}
{{- if and $qp.enabled (eq (default "" $qpAtt.kind) "commercial-contract") -}}
  {{- if or (not $qpAtt.accountId) (not $qpAtt.contractRef) -}}
    {{- $qpAttReady = false -}}
  {{- end -}}
{{- end -}}
{{- if and $qp.enabled $qpAttReady -}}
  {{- if not $qp.endpoint -}}
    {{- fail "defaultChannels.qwenPartner.enabled=true but defaultChannels.qwenPartner.endpoint is empty — supply the upstream relay URL in the per-Sovereign bootstrap-kit overlay (operator-supplied customer-managed LLM partner endpoint)" -}}
  {{- end -}}
  {{- $composed := dict
        "name"      (default "qwen" $qp.name)
        "type"      "openai-compatible"
        "endpoint"  $qp.endpoint
        "models"    (default (list "qwen3.6" "qwen3-coder") $qp.models)
        "attestation" $qpAtt -}}
  {{- if $qp.existingSecret -}}
    {{- $_ := set $composed "existingSecret" $qp.existingSecret -}}
  {{- end -}}
  {{- $channels = append $channels $composed -}}
{{- end -}}
{{/* ── Operator-supplied channels (in-order) ──────────────────── */}}
{{- range (default (list) .Values.channels) -}}
  {{- $channels = append $channels . -}}
{{- end -}}
{{/* ── Default vLLM (in-cluster fallback) ─────────────────────── */}}
{{- $vllm := $dc.vllm | default dict -}}
{{- if $vllm.enabled -}}
  {{- if not $vllm.endpoint -}}
    {{- fail "defaultChannels.vllm.enabled=true but defaultChannels.vllm.endpoint is empty — supply the upstream vLLM relay URL in the per-Sovereign bootstrap-kit overlay (operator-supplied)" -}}
  {{- end -}}
  {{- $composed := dict
        "name"      (default "qwen" $vllm.name)
        "type"      "vllm"
        "endpoint"  $vllm.endpoint
        "models"    (default (list "qwen3-coder") $vllm.models)
        "attestation" (default (dict "kind" "in-cluster") $vllm.attestation) -}}
  {{- if $vllm.existingSecret -}}
    {{- $_ := set $composed "existingSecret" $vllm.existingSecret -}}
  {{- end -}}
  {{- $channels = append $channels $composed -}}
{{- end -}}
{{- toYaml $channels -}}
{{- end -}}

{{/*
Channel-seed Job name. Reused by the Job + RBAC + ConfigMap manifests
emitted by templates/channel-seed-job.yaml.
*/}}
{{- define "bp-newapi.channelSeedJobName" -}}
{{- printf "%s-channel-seed" (include "bp-newapi.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "bp-newapi.adminSeedJobName" -}}
{{- printf "%s-admin-seed" (include "bp-newapi.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "bp-newapi.adminPromoteCronName" -}}
{{- printf "%s-admin-promote" (include "bp-newapi.fullname" .) | trunc 52 | trimSuffix "-" }}
{{- end }}

{{/*
Channel attestation gate — refuses to render if any enabled channel
lacks attestation. Compliance posture defined in
platform/newapi/README.md and blueprint.yaml configSchema. Operates on
the EFFECTIVE channel list (`.Values.channels` + composed defaults).
*/}}
{{- define "bp-newapi.assertChannelAttestation" -}}
{{- $effective := include "bp-newapi.effectiveChannels" . | fromYamlArray -}}
{{- range $idx, $ch := $effective }}
{{- if not $ch.attestation }}
{{- fail (printf "channel[%d] (%s): missing required attestation block — see platform/newapi/README.md compliance posture" $idx (default "<unnamed>" $ch.name)) }}
{{- end }}
{{- if not $ch.attestation.kind }}
{{- fail (printf "channel[%d] (%s): attestation.kind is required (one of: in-cluster, commercial-contract, byok)" $idx (default "<unnamed>" $ch.name)) }}
{{- end }}
{{- if eq $ch.attestation.kind "commercial-contract" }}
{{- if not $ch.attestation.accountId }}
{{- fail (printf "channel[%d] (%s): commercial-contract attestation requires accountId" $idx (default "<unnamed>" $ch.name)) }}
{{- end }}
{{- if not $ch.attestation.contractRef }}
{{- fail (printf "channel[%d] (%s): commercial-contract attestation requires contractRef" $idx (default "<unnamed>" $ch.name)) }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Registry-rewrite image helper (#4885). Given an openova-io "<host>/<path>"
repository + tag, prepend .Values.global.imageRegistry in place of the leading
registry host when it is set (self-sovereign-cutover step-07 pivot), else emit
the ghcr.io ref verbatim (default byte-identical pre-cutover). Mirrors the
continuum.image pattern. Apply ONLY to openova-io first-party images — the
3rd-party helper images (harbor.openova.io proxy-* curl / postgres) carry their
own registry and must NOT be routed through this.
Usage: {{ include "bp-newapi.image" (dict "repo" <repo> "tag" <tag> "root" $) }}
*/}}
{{- define "bp-newapi.image" -}}
{{- $repo := .repo -}}
{{- $tag := .tag -}}
{{- $globalRegistry := .root.Values.global.imageRegistry | default "" -}}
{{- if ne $globalRegistry "" -}}
{{- printf "%s/%s:%s" $globalRegistry (join "/" (slice (splitList "/" $repo) 1)) $tag -}}
{{- else -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}
{{- end -}}
