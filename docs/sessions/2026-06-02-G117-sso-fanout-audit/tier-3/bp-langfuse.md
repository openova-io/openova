# Tier-3 SSO audit — `bp-langfuse`

> Federates to: per-Org realm via 2-hop (per-Org realm → `sovereign` realm broker → catalyst-pin IdP).
> Chart path: `platform/langfuse/chart/`
> Chart version on `main`: `1.0.0` (Chart.yaml:37)
> Topology row (audit doc): rtz-A 🟢 A / rtz-B 🟡 P (App-tier CP-HA pair)

## Current SSO state — **NONE**

The chart wires bp-cnpg / bp-valkey / bp-clickhouse / bp-seaweedfs deps but has **zero OIDC values, zero KC client wiring, zero templates referencing auth**. Langfuse upstream supports OIDC via `next-auth` provider env vars (`AUTH_KEYCLOAK_ISSUER`, `AUTH_KEYCLOAK_CLIENT_ID`, `AUTH_KEYCLOAK_CLIENT_SECRET`).

| Surface | Status | File:line |
|---|---|---|
| `oidc.*` / `sso.*` / `auth.*` values block | MISSING | n/a |
| upstream `langfuse.langfuse.additionalEnv[]` block exposing AUTH_* vars | MISSING | n/a (chart has no `additionalEnv` pass-through) |
| Per-app SSO Secret materialization | MISSING | n/a |
| Per-Org realm registration | MISSING | n/a |
| `kc_idp_hint=catalyst-pin` injection | MISSING | n/a |

## Upstream chart OIDC handle

Per `langfuse/langfuse-k8s` chart 1.5.x, the way to inject env vars is via `langfuse.additionalEnv[]` or `langfuse.envFrom[]`. The upstream container reads (from langfuse 3.x source):

- `AUTH_KEYCLOAK_ISSUER` — full realm URL
- `AUTH_KEYCLOAK_CLIENT_ID` — string
- `AUTH_KEYCLOAK_CLIENT_SECRET` — string (also accepts `_FILE` variant for mounted secret)
- `AUTH_KEYCLOAK_REQUIRE_EMAIL_VERIFICATION` — bool
- `AUTH_KEYCLOAK_ALLOW_ACCOUNT_LINKING` — bool

next-auth Keycloak provider derives `authorization_endpoint` from issuer's `.well-known/openid-configuration`, which means `kc_idp_hint` must be injected differently — see Step 3 below.

## Required Wave-2 patch plan

### 1. New `oidc.*` values block

Add to `platform/langfuse/chart/values.yaml` after the `langfuseOverlay:` block:

```yaml
# ─── SSO via per-Org Keycloak realm (G117.5) ───────────────────────────
oidc:
  enabled: false                  # default OFF until per-Org realm exists
  realm: ""                       # operator-supplied: "org-<slug>"
  clientId: "langfuse"
  externalSecretStoreName: "vault-region1"
  refreshInterval: 1h
  # Override authorization_endpoint to force kc_idp_hint=sovereign-broker
  # (next-auth's Keycloak provider doesn't add it natively)
  overrideAuthorizationEndpoint: true
```

### 2. Per-app SSO Secret materialization (new template)

Add `platform/langfuse/chart/templates/sso-externalsecret.yaml`:

```yaml
{{- if .Values.oidc.enabled }}
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: langfuse-sso-oidc-credentials
  namespace: {{ .Release.Namespace }}
spec:
  refreshInterval: {{ .Values.oidc.refreshInterval | default "1h" }}
  secretStoreRef:
    name: {{ .Values.oidc.externalSecretStoreName }}
    kind: ClusterSecretStore
  target:
    name: langfuse-sso-oidc-credentials
    creationPolicy: Owner
    template:
      type: Opaque
      data:
        AUTH_KEYCLOAK_ISSUER:                  "https://auth.{{ .Values.sovereign.fqdn }}/realms/{{ .Values.oidc.realm }}"
        AUTH_KEYCLOAK_CLIENT_ID:               "{{ .Values.oidc.clientId }}"
        AUTH_KEYCLOAK_CLIENT_SECRET:           "{{ "{{ .client_secret }}" }}"
        AUTH_KEYCLOAK_REQUIRE_EMAIL_VERIFICATION: "true"
        AUTH_KEYCLOAK_ALLOW_ACCOUNT_LINKING:      "true"
        # next-auth NEXTAUTH_URL — required for callback host derivation
        NEXTAUTH_URL: "https://langfuse.{{ .Values.org.slug }}.{{ .Values.sovereign.fqdn }}"
  data:
    - secretKey: client_secret
      remoteRef:
        key: sso/{{ .Values.oidc.realm }}/{{ .Values.oidc.clientId }}
        property: client_secret
{{- end }}
```

### 3. AppRegistration ConfigMap (new template)

Add `platform/langfuse/chart/templates/sso-app-registration.yaml`:

```yaml
{{- if .Values.oidc.enabled }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: bp-langfuse-sso-registration
  namespace: {{ .Release.Namespace }}
  labels:
    sso.openova.io/app-registration: langfuse
data:
  client.json: |
    {
      "clientId": "{{ .Values.oidc.clientId }}",
      "name": "Langfuse ({{ .Values.org.slug }})",
      "enabled": true,
      "publicClient": false,
      "standardFlowEnabled": true,
      "directAccessGrantsEnabled": false,
      "redirectUris": ["https://langfuse.{{ .Values.org.slug }}.{{ .Values.sovereign.fqdn }}/api/auth/callback/keycloak"],
      "webOrigins": ["https://langfuse.{{ .Values.org.slug }}.{{ .Values.sovereign.fqdn }}"],
      "defaultClientScopes": ["web-origins","profile","email","groups"]
    }
  realm: "{{ .Values.oidc.realm }}"
{{- end }}
```

### 4. `kc_idp_hint` injection — IDR `defaultProvider` on per-Org realm

Because next-auth's Keycloak provider derives the authorize URL from discovery (NOT from a configurable env), the `kc_idp_hint` cannot be appended client-side. The ONLY way to silent-SSO through to the sovereign realm is to have the per-Org realm's IDR `defaultProvider=sovereign-broker` bound — same pattern as Tier-1 codification in PR #2730.

This means `kc_idp_hint` per-app injection is a NO-OP for Langfuse — the redirect chain auto-delegates because the per-Org realm IDR is bound. Wave-2 SSO agent ships the IDR config in `configmap-per-org-realm.yaml` (owned by bp-keycloak chart, Wave-2.C4).

### 5. Wire envFrom in upstream subchart values

Extend `platform/langfuse/chart/values.yaml` `langfuse.langfuse` block:

```yaml
langfuse:
  langfuse:
    # ... existing ...
    additionalEnv:
      # Selectively pull AUTH_* + NEXTAUTH_URL from the per-app Secret
      - name: AUTH_KEYCLOAK_ISSUER
        valueFrom:
          secretKeyRef: {name: langfuse-sso-oidc-credentials, key: AUTH_KEYCLOAK_ISSUER, optional: true}
      - name: AUTH_KEYCLOAK_CLIENT_ID
        valueFrom:
          secretKeyRef: {name: langfuse-sso-oidc-credentials, key: AUTH_KEYCLOAK_CLIENT_ID, optional: true}
      - name: AUTH_KEYCLOAK_CLIENT_SECRET
        valueFrom:
          secretKeyRef: {name: langfuse-sso-oidc-credentials, key: AUTH_KEYCLOAK_CLIENT_SECRET, optional: true}
      - name: NEXTAUTH_URL
        valueFrom:
          secretKeyRef: {name: langfuse-sso-oidc-credentials, key: NEXTAUTH_URL, optional: true}
```

Verify upstream chart 1.5.28 supports `additionalEnv` (it does per `langfuse-k8s` README — fall back to `envFrom: [{secretRef: {name: langfuse-sso-oidc-credentials}}]` if not).

## 2-hop verification curl chain (8 hops)

```
HOP1: GET https://langfuse.<org>.<sov-fqdn>/auth/sign-in
      → 302 → https://langfuse.<org>.<sov-fqdn>/api/auth/signin/keycloak
HOP2: POST HOP1 (CSRF token via prior fetch)
      → 302 → https://auth.<sov-fqdn>/realms/<org-realm>/protocol/openid-connect/auth?client_id=langfuse&...
HOP3: GET HOP2 (jar w/ catalyst_session)
      → 303 → https://auth.<sov-fqdn>/realms/<org-realm>/broker/sovereign-broker/login?session_code=...
      (per-Org realm IDR auto-delegates because defaultProvider=sovereign-broker bound)
HOP4-HOP7: 2-hop chain through sovereign realm → catalyst-pin → /oidc/auth → callback back to per-Org realm
HOP8: GET final redirect (jar)
      → 302 → https://langfuse.<org>.<sov-fqdn>/api/auth/callback/keycloak?code=...&state=...
HOP9: → 200 → langfuse session cookie set, redirect to dashboard
```

next-auth's POST sign-in is a CSRF-protected handshake — initial probe needs `GET /api/auth/csrf` first, then `POST /api/auth/signin/keycloak` with the csrfToken in the body.

## Cross-Org isolation test

```bash
# Org-A user session, hit Org-B Langfuse:
curl -ks -b /tmp/org-a-jar -c /tmp/org-a-jar -L --max-redirs 20 \
  "https://langfuse.org-b.<sov-fqdn>/api/auth/signin/keycloak"

# Expected: chain bounces through https://auth.<sov-fqdn>/realms/org-b/...
# Org-A's catalyst_session is bridge-valid in sovereign realm but Org-B's realm has NO session for this user
# → KC sovereign realm session is reused (silent SSO succeeds at sovereign level)
# → per-Org realm sees a sovereign-broker token with user attributes
# → per-Org realm's firstBrokerLoginFlow MUST gate on org_id claim matching the realm's org
# REQUIRED: Auto-link MUST fail if id_token's org_id != "org-b" → user lands at consent screen OR error
```

## Chart-patch size estimate

| File | Lines added | Lines removed |
|---|---|---|
| `platform/langfuse/chart/templates/sso-externalsecret.yaml` (new) | ~30 | 0 |
| `platform/langfuse/chart/templates/sso-app-registration.yaml` (new) | ~20 | 0 |
| `platform/langfuse/chart/values.yaml` | ~30 (oidc.* block + additionalEnv block) | 0 |
| **Total per app** | **~80** | **0** |

Chart bump: `1.0.0 → 1.1.0` (minor — additive SSO feature). Lockstep: `Chart.yaml` + `blueprint.yaml` + bootstrap-kit slot pin.
