# Tier-2 SSO audit — `bp-powerdns-admin`

> Federates to: `sovereign` realm (single hop) — Catalyst-CP admin UI tier.
> Chart path: `platform/powerdns-admin/chart/`
> Chart version on `main`: `0.1.0` (Chart.yaml:3)
> Topology row (audit doc): per-host-cluster singleton (`pdns-admin.<sov>`)

## Current SSO state — **GOOD** (best Tier-2 starting position)

`bp-powerdns-admin` already implements the canonical SSO chart-side wiring expected by the Wave-1.B6 Tier-1 pattern. The chart was authored under the G91.powerdns-admin (#2656) work with bp-sso-bridge integration in mind.

| Surface | Status | File:line |
|---|---|---|
| `sso.*` values block (enabled / realm / sovereignFqdn / clientId / externalSecretStoreName / adminGroup / autoOnboard) | PRESENT | `platform/powerdns-admin/chart/values.yaml:119-139` |
| Deployment OIDC env vars (`OIDC_OAUTH_*`) populated from envFrom | PRESENT | `platform/powerdns-admin/chart/templates/deployment.yaml:88-120` |
| Per-app SSO Secret materialization | PRESENT via **ExternalSecret** (pulls from OpenBao at `sso/powerdns-admin`) | `platform/powerdns-admin/chart/templates/sso-oauth-source-externalsecret.yaml:12-69` |
| Realm-client registration | MISSING from `configmap-sovereign-realm.yaml`. Chart assumes `bp-sso-bridge` writes the OpenBao bundle + provisions the KC client at runtime | n/a |
| `kc_idp_hint=catalyst-pin` injection | MISSING — `OIDC_OAUTH_AUTHORIZE_URL` is templated from KC discovery, no query param appended | `platform/powerdns-admin/chart/templates/sso-oauth-source-externalsecret.yaml:37` |
| Direct `lookup`-or-generate Secret + `helm.sh/resource-policy: keep` | NOT NEEDED (ExternalSecret path supersedes; OpenBao is the durable cred store) | n/a |
| Default `sovereignFqdn` derivation | MISSING — operator-supplied; empty value disables OIDC (graceful fallback) | `platform/powerdns-admin/chart/values.yaml:132` |

## Required Wave-2 patch plan

### 1. Per-app SSO Secret materialization — already done (no change)

The ExternalSecret at `platform/powerdns-admin/chart/templates/sso-oauth-source-externalsecret.yaml` is the canonical materialization. **Wave-2 must NOT add a parallel `lookup`-or-generate Secret** — it would race with the External-Secrets Operator's `creationPolicy: Owner`.

Wave-2 still owns: the `bp-sso-bridge` reconciler must write the `sso/powerdns-admin` OpenBao path on Org boot. That work is OUT OF SCOPE for B5 and lives under separate G117.X.

### 2. Required KC client config (additions to `platform/keycloak/chart/templates/configmap-sovereign-realm.yaml` `clients[]` array)

```jsonc
{
  "clientId": "powerdns-admin",
  "name": "PowerDNS-Admin (Sovereign DNS UI)",
  "enabled": true,
  "publicClient": false,
  "secret": "<bp-sso-bridge writes to OpenBao; realm import reads same value via shared template helper>",
  "standardFlowEnabled": true,
  "directAccessGrantsEnabled": false,
  "redirectUris": [
    "https://pdns-admin.{{ .Values.sovereignFQDN }}/oidc/authorized",
    "https://pdns-admin.{{ .Values.sovereignFQDN }}/*"
  ],
  "webOrigins": ["https://pdns-admin.{{ .Values.sovereignFQDN }}"],
  "defaultClientScopes": ["web-origins","acr","profile","roles","email","groups","org"],
  "attributes": {
    "access.token.lifespan": "900",
    "post.logout.redirect.uris": "https://pdns-admin.{{ .Values.sovereignFQDN }}/*"
  }
}
```

> The `secret` value should be derived from a shared `_helpers.tpl` named template (mirror `bp-keycloak.pinBrokerClientSecret` from PR #2730) so the realm-import value + the OpenBao-stored value never drift. Wave-2 SSO agent will introduce `bp-keycloak.appClientSecret` helper.

### 3. `kc_idp_hint=catalyst-pin` injection point (chart edit)

PowerDNS-Admin's `OIDC_OAUTH_AUTO_CONFIGURE=True` consumes the OIDC discovery doc for endpoints. There is no Flask-side knob to append a query param post-discovery. Two options:

**Option A (preferred — chart edit):** override `OIDC_OAUTH_AUTHORIZE_URL` explicitly in the ExternalSecret template and append `?kc_idp_hint=catalyst-pin`:

```yaml
# platform/powerdns-admin/chart/templates/sso-oauth-source-externalsecret.yaml:37 (patch)
OIDC_OAUTH_AUTHORIZE_URL: "{{ "{{ .authorize_url }}" }}?kc_idp_hint=catalyst-pin"
```

**Option B (preferred — bp-sso-bridge edit):** `bp-sso-bridge` reconciler writes `authorize_url` to OpenBao with the query param already appended. This decouples the kc_idp_hint convention from per-app chart logic but increases the SSO bridge contract surface.

Recommendation: **Option A** for B5 scope (chart-local, no bridge contract change). If bp-sso-bridge gains a `kcIdpHint` field in its CRD later, Option B can supersede.

### 4. `sovereign.fqdn` derivation default

Patch `platform/powerdns-admin/chart/values.yaml:132` so the default derives from `Release.Namespace` env or accepts an envsubst at install time. Maintain empty-default fallback behavior (graceful local-account-only auth).

## 4-hop curl probe (verification)

```
HOP1: GET https://pdns-admin.<sov-fqdn>/login
      → 302 → https://auth.<sov-fqdn>/realms/sovereign/protocol/openid-connect/auth?client_id=powerdns-admin&kc_idp_hint=catalyst-pin&...
HOP2: GET HOP1 (jar w/ catalyst_session)
      → 303 → https://auth.<sov-fqdn>/realms/sovereign/broker/catalyst-pin/login?session_code=...&client_id=powerdns-admin&tab_id=...
HOP3: GET HOP2 (jar)
      → 303 → https://api.<sov-fqdn>/oidc/auth?scope=openid+profile+email+groups&state=...
HOP4: GET HOP3 (jar)
      → 302 → https://pdns-admin.<sov-fqdn>/oidc/authorized?code=...&state=...
HOP5: GET HOP4 (jar) → 302 → https://pdns-admin.<sov-fqdn>/dashboard  (silent SSO success)
```

Note: PDA uses Authlib's OIDC flow with an explicit `/oidc/authorized` callback path (NOT the bare `/`). HOP4's callback endpoint mints the PDA Flask session cookie and 302s to the post-login landing page (`config.defaultLandingPage: dashboard`).

If HOP1 → 200 (login form): `sso.enabled=false` OR ExternalSecret never materialized (verify with `kubectl -n powerdns-admin get externalsecret pda-sso-oidc-credentials` SyncedStatus). If HOP2 → 200: IDR `defaultProvider` config not bound on realm (Wave-1.B6 codification not deployed). If HOP4 → 400 invalid_grant: client_secret mismatch between OpenBao bundle and KC realm-import.

## Chart-patch size estimate

| File | Lines added | Lines removed |
|---|---|---|
| `platform/powerdns-admin/chart/templates/sso-oauth-source-externalsecret.yaml` | ~1 (append `?kc_idp_hint=catalyst-pin` to authorize_url) | ~1 |
| `platform/powerdns-admin/chart/values.yaml` | ~3 (add default-derivation note) | 0 |
| **Total per app** | **~4** | **~1** |

Smallest chart-patch in scope. Chart bump: `0.1.0 → 0.1.1`. Lockstep: `Chart.yaml` + `blueprint.yaml` + bootstrap-kit slot pin.
