# Tier-2 SSO audit — `bp-guacamole`

> Federates to: `sovereign` realm (single hop) — Catalyst-CP admin UI tier.
> Chart path: `platform/guacamole/chart/`
> Chart version on `main`: `0.1.31` (Chart.yaml:51)
> Topology row (audit doc): mgmt-A/A, mgmt-B/P (Catalyst-CP HA pair)

## Current SSO state — PARTIAL

Guacamole already ships the bulk of the OIDC chart wiring. What is in place vs. what Wave-2 must extend:

| Surface | Status | File:line |
|---|---|---|
| `oidc.*` values block (issuer / clientId / clientSecretRef / redirectURI / groupsClaim) | PRESENT | `platform/guacamole/chart/values.yaml:118-136` |
| Deployment env vars `OPENID_*` populated from values + secretKeyRef | PRESENT | `platform/guacamole/chart/templates/guacamole-deployment.yaml:42-65` |
| Per-app SSO Secret materialization | PARTIAL — uses **SealedSecret placeholder** (operator must `kubeseal` the value) | `platform/guacamole/chart/templates/keycloak-client-secret.yaml:25-42` |
| Realm-patch ConfigMap (`catalyst.openova.io/keycloak-config: realm-patch`) for KC client registration | PRESENT but DIVERGENT — targets a `catalyst` realm and a separate post-deploy Job (NOT the `sovereign-realm-config` ConfigMap that Wave-1.B6/PR #2730 codified the IDR pattern in) | `platform/guacamole/chart/templates/keycloak-realm-config.yaml:24-90` |
| `kc_idp_hint=catalyst-pin` injection on the auth URL | MISSING — `OPENID_AUTHORIZATION_ENDPOINT` lacks the query param | `platform/guacamole/chart/templates/guacamole-deployment.yaml:45-46` |
| `helm.sh/resource-policy: keep` on per-app SSO Secret | MISSING (SealedSecret only; no kept Secret with `lookup`-or-generate) | n/a |
| Default `oidc.issuer` derivation from `sovereign.fqdn` | MISSING — operator-supplied; empty default fails-fast via `_helpers.tpl bp-guacamole.oidcIssuer` | `platform/guacamole/chart/values.yaml:123` |

## Required Wave-2 patch plan

### 1. Per-app SSO Secret materialization (new template)

Add `platform/guacamole/chart/templates/secret-sso-oidc-credentials.yaml` following the pattern from `platform/openbao/chart/templates/recovery-seed-bootstrap.yaml` (`lookup`-or-generate + `helm.sh/resource-policy: keep`):

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: guacamole-sso-oidc-credentials
  namespace: {{ .Release.Namespace }}
  annotations:
    helm.sh/resource-policy: keep
  labels:
    catalyst.openova.io/blueprint: bp-guacamole
    catalyst.openova.io/component: sso-credentials
type: Opaque
stringData:
  client_id: {{ .Values.guacamole.oidc.clientId | quote }}
  # lookup-or-generate pattern
  client_secret: {{ (lookup "v1" "Secret" .Release.Namespace "guacamole-sso-oidc-credentials").data.client_secret | default (randAlphaNum 32 | b64enc) | b64dec | quote }}
  issuer_url: "https://auth.{{ required "sovereign.fqdn required for SSO" .Values.sovereign.fqdn }}/realms/sovereign"
```

### 2. Required KC client config (additions to `platform/keycloak/chart/templates/configmap-sovereign-realm.yaml` `clients[]` array — owned by Wave-2 SSO agent, NOT touched by this audit PR)

```jsonc
{
  "clientId": "guacamole",
  "name": "Apache Guacamole (Sovereign admin UI)",
  "enabled": true,
  "publicClient": false,
  "secret": "<materialized-via-lookup>",
  "standardFlowEnabled": true,
  "directAccessGrantsEnabled": false,
  "redirectUris": ["https://guac.{{ .Values.sovereignFQDN }}/*"],
  "webOrigins": ["https://guac.{{ .Values.sovereignFQDN }}"],
  "defaultClientScopes": ["web-origins","acr","profile","roles","email","groups","org"],
  "attributes": {
    "access.token.lifespan": "900",
    "post.logout.redirect.uris": "https://guac.{{ .Values.sovereignFQDN }}/*"
  }
}
```

### 3. `kc_idp_hint=catalyst-pin` injection point (chart edit)

Patch `platform/guacamole/chart/templates/guacamole-deployment.yaml:45-46`:

```yaml
- name: OPENID_AUTHORIZATION_ENDPOINT
  value: "{{ include "bp-guacamole.oidcIssuer" . }}/protocol/openid-connect/auth?kc_idp_hint=catalyst-pin"
```

> **Defense-in-depth note:** per `feedback_g113_sso_idr_defaultprovider_fix.md` the IDR `defaultProvider` config bound on the `sovereign` realm browser flow (PR #2730) makes per-app `kc_idp_hint` optional — but each app SHOULD still pass it for explicit hinting + post-cutover resilience (PINs the realm to one IdP even if the realm-import IDR config drifts).

### 4. Existing realm-patch ConfigMap — DECOMMISSION

`platform/guacamole/chart/templates/keycloak-realm-config.yaml` registers Guacamole against a `catalyst` realm via a separate post-deploy Job. That pattern PRECEDES the Wave-1 codification of the `sovereign` realm import in `configmap-sovereign-realm.yaml`. Wave-2 SSO agent will:

1. Delete `keycloak-realm-config.yaml` (it would race with the consolidated realm-import).
2. Move the `clientId: guacamole` registration into `configmap-sovereign-realm.yaml` `clients[]`.
3. Update `values.yaml` `oidc.issuer` default to derive from `sovereign.fqdn` (drop the operator-supplied requirement; add `sovereign.fqdn` to the values block).

## 4-hop curl probe (verification)

After Wave-2 patches land + fresh-prov reconciles:

```
HOP1: GET https://guac.<sov-fqdn>/guacamole/api/ext/openid/login
      → 302 → https://auth.<sov-fqdn>/realms/sovereign/protocol/openid-connect/auth?client_id=guacamole&kc_idp_hint=catalyst-pin&...
HOP2: GET HOP1 (with cookie jar holding catalyst_session)
      → 303 → https://auth.<sov-fqdn>/realms/sovereign/broker/catalyst-pin/login?session_code=...&client_id=guacamole&tab_id=...
HOP3: GET HOP2 (with jar)
      → 303 → https://api.<sov-fqdn>/oidc/auth?scope=openid+email+profile+groups&state=...
HOP4: GET HOP3 (with jar)
      → 302 → https://guac.<sov-fqdn>/guacamole/  (silent SSO success)
```

If HOP2 returns 200 (login form HTML) → IDR `defaultProvider` config did not take. Clear realm cache via `POST /admin/realms/sovereign/clear-realm-cache`. If HOP4 returns 401 → client_secret mismatch between rendered Secret and KC realm import (chart-credential-persistence Defense pattern from `feedback_chart_credential_persistence_defense.md`).

## Chart-patch size estimate

| File | Lines added | Lines removed |
|---|---|---|
| `platform/guacamole/chart/templates/secret-sso-oidc-credentials.yaml` (new) | ~25 | 0 |
| `platform/guacamole/chart/templates/guacamole-deployment.yaml` | ~2 | ~1 |
| `platform/guacamole/chart/values.yaml` | ~5 (add `sovereign.fqdn` block) | ~2 (drop required-operator-supplied default) |
| `platform/guacamole/chart/templates/keycloak-realm-config.yaml` | 0 | ~90 (delete entire file) |
| **Total per app** | **~32** | **~93** |

Chart bump: `0.1.31 → 0.2.0` (breaking — realm-config file removed). Lockstep: `Chart.yaml` + `blueprint.yaml` + bootstrap-kit slot pin (per `feedback_chart_version_collision_serialize_or_rebase.md`).
