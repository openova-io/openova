# Tier-3 SSO audit — `bp-librechat`

> Federates to: per-Org realm via 2-hop (per-Org realm → `sovereign` realm broker → catalyst-pin IdP).
> Chart path: `platform/librechat/chart/`
> Chart version on `main`: `1.0.0` (Chart.yaml:3)
> Topology row (audit doc): rtz-A 🟢 A / rtz-B 🟢 A (App-tier active-active, stateless once Mongo wire backend = FerretDB on CNPG)

## Current SSO state — **GOOD** (chart-side OIDC envFromis done; only per-Org wiring + AppRegistration missing)

The chart was authored with Keycloak SSO in mind — the deployment.yaml already pipes `OPENID_*` env vars from a Secret reference. Comparable to bp-powerdns-admin starting position.

| Surface | Status | File:line |
|---|---|---|
| `keycloak.*` values block (enabled / issuer / clientId / scope / callbackPath / buttonLabel / existingSecret / allowSocialLogin) | PRESENT | `platform/librechat/chart/values.yaml:114-129` |
| Deployment env vars OPENID_*, populated from secretKeyRef | PRESENT | `platform/librechat/chart/templates/deployment.yaml:130-158` |
| Per-app SSO Secret materialization | MISSING — operator-supplied `existingSecret` only; no chart-side `lookup`-or-generate; no ExternalSecret pattern | `platform/librechat/chart/values.yaml:127` |
| Per-Org realm registration | MISSING (no AppRegistration ConfigMap) | n/a |
| `kc_idp_hint=catalyst-pin` injection | MISSING — `OPENID_ISSUER` is the discovery base; LibreChat derives auth URL from `.well-known/openid-configuration` | `platform/librechat/chart/templates/deployment.yaml:136-137` |
| Auto-derive `issuer` from `sovereign.fqdn` | MISSING — operator-supplied; empty default disables OIDC (graceful) | `platform/librechat/chart/values.yaml:120` |

## Required Wave-2 patch plan

### 1. Per-app SSO Secret materialization (new template — ExternalSecret pattern)

Add `platform/librechat/chart/templates/sso-externalsecret.yaml`:

```yaml
{{- if .Values.keycloak.enabled }}
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: librechat-oidc
  namespace: {{ .Release.Namespace }}
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault-region1
    kind: ClusterSecretStore
  target:
    name: librechat-oidc
    creationPolicy: Owner
    template:
      type: Opaque
      data:
        OPENID_CLIENT_SECRET:  "{{ "{{ .client_secret }}" }}"
        # LibreChat needs an explicit 32-byte+ session secret distinct from
        # the OIDC client secret. Generate once + persist via bp-sso-bridge.
        OPENID_SESSION_SECRET: "{{ "{{ .session_secret }}" }}"
  data:
    - secretKey: client_secret
      remoteRef:
        key: sso/{{ .Values.org.realm }}/librechat
        property: client_secret
    - secretKey: session_secret
      remoteRef:
        key: sso/{{ .Values.org.realm }}/librechat
        property: session_secret
{{- end }}
```

Patch `values.yaml:127` so `existingSecret` defaults to `librechat-oidc` (matching the ExternalSecret target). Operator override remains possible.

### 2. AppRegistration ConfigMap (new template)

Add `platform/librechat/chart/templates/sso-app-registration.yaml`:

```yaml
{{- if .Values.keycloak.enabled }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: bp-librechat-sso-registration
  namespace: {{ .Release.Namespace }}
  labels:
    sso.openova.io/app-registration: librechat
data:
  client.json: |
    {
      "clientId": "librechat",
      "name": "LibreChat ({{ .Values.org.slug }})",
      "enabled": true,
      "publicClient": false,
      "standardFlowEnabled": true,
      "directAccessGrantsEnabled": false,
      "redirectUris": ["https://chat.{{ .Values.org.slug }}.{{ .Values.sovereign.fqdn }}/oauth/openid/callback"],
      "webOrigins": ["https://chat.{{ .Values.org.slug }}.{{ .Values.sovereign.fqdn }}"],
      "defaultClientScopes": ["web-origins","openid","profile","email","groups"]
    }
  realm: "{{ .Values.org.realm }}"
{{- end }}
```

### 3. Auto-derive `issuer` from `sovereign.fqdn` + `org.realm`

Patch `platform/librechat/chart/values.yaml:118-121`:

```yaml
keycloak:
  enabled: true
  # Auto-derived when empty: https://auth.{{ sovereign.fqdn }}/realms/{{ org.realm }}
  issuer: ""
  clientId: "librechat"
```

Patch `platform/librechat/chart/templates/deployment.yaml:136-137`:

```yaml
- name: OPENID_ISSUER
  value: "{{ .Values.keycloak.issuer | default (printf "https://auth.%s/realms/%s" .Values.sovereign.fqdn .Values.org.realm) }}"
```

### 4. `kc_idp_hint` injection

Same as Langfuse / Temporal — LibreChat (openid-client npm package internally) derives authorize_url from discovery. Append `?kc_idp_hint=sovereign-broker` is non-trivial without a `customAuthorizationParams` override in LibreChat's config. Per-Org realm IDR `defaultProvider=sovereign-broker` (codified in Wave-2.C4 `configmap-per-org-realm.yaml`) is the canonical hop-2 trigger.

Optional defense-in-depth: LibreChat 0.7+ supports `OPENID_REQUIRED_ROLE` env var. Wave-2 can add `OPENID_REQUIRED_ROLE: "org-{{ .Values.org.slug }}-user"` to reject any token that didn't pass through the org-realm flow (catches mis-federated tokens).

## 2-hop verification curl chain (8-9 hops)

```
HOP1: GET https://chat.<org>.<sov-fqdn>/oauth/openid
      → 302 → https://auth.<sov-fqdn>/realms/<org-realm>/protocol/openid-connect/auth?client_id=librechat&...
HOP2: GET HOP1 (jar w/ catalyst_session)
      → 303 → https://auth.<sov-fqdn>/realms/<org-realm>/broker/sovereign-broker/login?session_code=...
      (per-Org realm IDR auto-delegates)
HOP3-HOP7: 2-hop chain through sovereign realm → catalyst-pin → /oidc/auth → broker callbacks
HOP8: GET final redirect (jar)
      → 302 → https://chat.<org>.<sov-fqdn>/oauth/openid/callback?code=...&state=...
HOP9: → 302 → https://chat.<org>.<sov-fqdn>/c/new  (LibreChat session cookie set, redirect to chat home)
```

## Cross-Org isolation test

```bash
curl -ksI -b /tmp/org-a-jar -L --max-redirs 20 \
  "https://chat.org-b.<sov-fqdn>/oauth/openid"

# Expected: chain bounces through https://auth.<sov-fqdn>/realms/org-b/...
# Org-A's sovereign-realm catalyst_session is valid but Org-B realm has NO matching user
# → per-Org realm firstBrokerLoginFlow MUST gate on org_id claim
# REQUIRED: chain ends at consent screen OR error page when org_id != "org-b"
```

LibreChat additionally honors `OPENID_REQUIRED_ROLE` — if set to `org-{slug}-user`, an org-a-issued token (without org-b-user role) will be rejected at callback even if signature validates.

## Chart-patch size estimate

| File | Lines added | Lines removed |
|---|---|---|
| `platform/librechat/chart/templates/sso-externalsecret.yaml` (new) | ~30 | 0 |
| `platform/librechat/chart/templates/sso-app-registration.yaml` (new) | ~20 | 0 |
| `platform/librechat/chart/templates/deployment.yaml` | ~2 (issuer auto-derive) | ~1 |
| `platform/librechat/chart/values.yaml` | ~10 (sovereign.fqdn + org.{slug,realm} refs + existingSecret default flip) | ~3 |
| **Total per app** | **~62** | **~4** |

Chart bump: `1.0.0 → 1.1.0` (minor — additive). Lockstep: `Chart.yaml` + `blueprint.yaml` + bootstrap-kit slot pin.

## Gotcha — MongoDB user-doc auto-creation race

LibreChat creates a user document in MongoDB on first SSO sign-in. With multi-region Active-Active (audit doc shows rtz-A 🟢 A / rtz-B 🟢 A), simultaneous logins from the same user against both regions can race to create duplicate docs (FerretDB / Mongo wire). Wave-2 should consider adding a unique index on `users.email` via post-install Job — separate from SSO scope but discovered during this audit.
