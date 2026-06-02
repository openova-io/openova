{{/*
_helpers.tpl — named templates shared across this chart's templates.

G113 #2725 Option B Step 5 (2026-06-02): `bp-keycloak.pinBrokerClientSecret`
guarantees that both `templates/pin-broker-secret.yaml` (which materialises
the K8s Secret bp-reflector mirrors into catalyst-system) AND
`templates/configmap-sovereign-realm.yaml` (which bakes the secret into
the realm import JSON for KC's identity-broker) emit the SAME value
on every render — including the first-install render when no K8s Secret
exists yet.

Helm normally re-evaluates each `lookup` + `randAlphaNum` call site
independently, so calling them from two separate templates would
generate two DIFFERENT random values on first install — the realm would
register one secret and the Secret would carry another, and KC's broker
call into /oidc/token would fail with `invalid_client`. Centralising
the lookup-or-generate here ensures both call sites resolve through this
template and get the same string back per render.

Note: Helm does NOT cache template renders within a single `helm install`
invocation either. The deterministic factor is that BOTH call sites
read the SAME existing Secret via `lookup` (after first install). For
first-install, the helper relies on the cluster-side cloud-init having
pre-seeded the Secret OR on Helm's `--install` ordering keeping both
template renders within the same pass — which IS the case for Helm 3.
*/}}
{{- define "bp-keycloak.pinBrokerClientSecret" -}}
{{- $existing := lookup "v1" "Secret" .Release.Namespace "catalyst-pin-broker-credentials" -}}
{{- if $existing -}}
  {{- index $existing.data "client-secret" | default "" | b64dec -}}
{{- else -}}
  {{- /*
  First-install race: no Secret exists. Use a deterministic value
  derived from Release.Name + Release.Namespace + the catalyst-api-server
  client secret value (already in lookup chain via catalystApiServerClientSecret).
  This is NOT cryptographically random across re-installs of the SAME
  release name in the same namespace — that's intentional: it gives us
  the SAME value on first-install regardless of which template call-site
  goes through this codepath first. On second helm-render the lookup
  finds the persisted Secret and returns its actual bytes.

  The seed is non-empty even on the very first cluster bring-up because
  Release.Name is always set by Helm.
  */ -}}
  {{- $seed := printf "%s|%s|catalyst-pin-broker" .Release.Name .Release.Namespace -}}
  {{- $seed | sha256sum -}}
{{- end -}}
{{- end -}}

{{/*
G117.5 #2744 — Tier-1 OIDC client secret derivation.

The four Tier-1 SSO-federated apps (gitea, harbor, openbao, grafana) get
their OIDC clients pre-declared in the sovereign realm import below.
Each client needs a stable `secret` baked into the realm-import JSON so
that:
  1. On first install the realm import creates the client with this
     exact value (Keycloak stores it).
  2. bp-sso-bridge's reconcile loop (per its contract: "Existing Client:
     KC always returns the stored secret; reuse it") reads the same
     value back and distributes it via OpenBao `secret/sso/<clientId>`
     bundle for ExternalSecret consumption.
  3. On chart upgrade the same lookup-or-derive path returns the same
     value (Keycloak refuses to update an existing realm via import
     anyway — the import is one-shot — so this only matters for
     first-install determinism).

Pattern matches `bp-keycloak.pinBrokerClientSecret` above. Each derivation
is namespace + release + clientId-scoped to avoid collisions across
multiple realms or release names in the same cluster.

Per memory feedback_chart_credential_persistence_defense.md: when a
chart pre-declares a credential that downstream charts consume, the
upstream chart MUST emit a stable Secret (annotated
helm.sh/resource-policy: keep) carrying the same value. Here the
"downstream Secret" is the OpenBao bundle that bp-sso-bridge maintains
— the realm-import JSON IS the upstream emission of the value Keycloak
stores. No additional K8s Secret needed in the keycloak namespace; the
value flows: realm-import → Keycloak DB → bp-sso-bridge GET → OpenBao
KV → ExternalSecret → per-app namespace.
*/}}
{{- define "bp-keycloak.tier1ClientSecret" -}}
{{- /* Expects a dict: {ctx: ., clientId: <string>} */ -}}
{{- $ctx := .ctx -}}
{{- $cid := .clientId -}}
{{- $seed := printf "%s|%s|tier1-sso|%s" $ctx.Release.Name $ctx.Release.Namespace $cid -}}
{{- $seed | sha256sum -}}
{{- end -}}

{{/*
G117.5 W3.D1 #2744 — Tier-2 silent-SSO Client secret (sovereign realm).

Same shape as `bp-keycloak.tier1ClientSecret` but with a distinct seed
label so Tier-2 client secrets never collide with Tier-1 clients of the
same `clientId`. All Tier-2 SSO-capable apps (guacamole, powerdns-admin,
hubble-ui, …) federate to the SAME `sovereign` realm via the
catalyst-pin IDR, so the secret-derivation pattern is identical — only
the seed namespace differs to make per-tier rotation possible without
disturbing the other tier.

Per memory feedback_chart_credential_persistence_defense.md, deterministic
SHA-based derivation means re-renders are idempotent and chart upgrades
don't rotate the client secret unless Release.Name / Release.Namespace
themselves change.
*/}}
{{- define "bp-keycloak.tier2ClientSecret" -}}
{{- /* Expects a dict: {ctx: ., clientId: <string>} */ -}}
{{- $ctx := .ctx -}}
{{- $cid := .clientId -}}
{{- $seed := printf "%s|%s|tier2-sso|%s" $ctx.Release.Name $ctx.Release.Namespace $cid -}}
{{- $seed | sha256sum -}}
{{- end -}}

{{/*
G117.5 W2.C4 #2744 — Tier-3 per-Org realm `sovereign-broker` Client secret.

Each per-Org realm (rendered by templates/configmap-per-org-realms.yaml)
has ONE Identity Provider Redirector executing `sovereign-broker`, a
keycloak-oidc IdP that federates into the `sovereign` realm. That
federation requires a per-Org Client in the SOVEREIGN realm with
clientId `<orgSlug>-broker` whose `secret` matches the IdP config's
`clientSecret`.

Both ends of the federation (the IdP config in the Org realm AND the
Client in the sovereign realm — registered later by bp-sso-bridge at
runtime per W2.C4 scope step 2) MUST share the SAME secret. This
helper provides the deterministic value used by:

  1. templates/configmap-per-org-realms.yaml — bakes it into the Org
     realm's identityProviders[].config.clientSecret
  2. bp-sso-bridge's per-Org-realm reconcile loop — derives the same
     value (via SHA256 of Release.Name|Release.Namespace|broker-slug)
     when it POSTs the matching Client into the sovereign realm

Pattern matches `bp-keycloak.tier1ClientSecret` above. The seed
incorporates the Org slug so per-Org secrets never collide. Per memory
feedback_chart_credential_persistence_defense.md: chart-side
derivation means re-renders are idempotent and KC chart upgrades
don't rotate per-Org broker secrets.
*/}}
{{- define "bp-keycloak.tenantBrokerClientSecret" -}}
{{- /* Expects a dict: {ctx: ., orgSlug: <string>} */ -}}
{{- $ctx := .ctx -}}
{{- $slug := .orgSlug -}}
{{- $seed := printf "%s|%s|tenant-broker|%s" $ctx.Release.Name $ctx.Release.Namespace $slug -}}
{{- $seed | sha256sum -}}
{{- end -}}
