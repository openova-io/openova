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
