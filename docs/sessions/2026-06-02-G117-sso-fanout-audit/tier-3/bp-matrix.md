# Tier-3 SSO audit — `bp-matrix` (Synapse)

> Federates to: per-Org realm via 2-hop (per-Org realm → `sovereign` realm broker → catalyst-pin IdP).
> Chart path: `platform/matrix/chart/` (folder + chart confirmed)
> Chart version on `main`: `1.0.1` (Chart.yaml:3)
> Topology row (audit doc): rtz-A 🟢 A / rtz-B 🟡 P (App-tier, per-Org vCluster, CP-tier HA pair on rtz)

## Current SSO state — **PARTIAL** (values block + upstream chart path exist; per-app Secret + 2-hop wiring + realm registration missing)

Synapse natively supports OIDC via `homeserver.yaml` `oidc_providers[]`. The chart exposes a Catalyst-overlay `oidc.*` block that the upstream `matrix-synapse` chart consumes via `extraConfig.oidc_providers`. The wiring lives there — no Catalyst-authored template materializes it.

| Surface | Status | File:line |
|---|---|---|
| `oidc.*` values block (enabled / issuer / clientId / scopes / existingSecret) | PRESENT | `platform/matrix/chart/values.yaml:159-168` |
| Synapse `oidc_providers[]` config emission | INDIRECT — depends on per-Sovereign overlay piping `oidc.*` into upstream `extraConfig.oidc_providers` (NOT auto-emitted by Catalyst overlay templates today) | n/a |
| Per-app SSO Secret materialization | MISSING — operator-supplied `existingSecret` only; no chart-side `lookup`-or-generate | `platform/matrix/chart/values.yaml:168` |
| Per-Org realm registration | MISSING entirely (no per-Org realm template exists in `platform/keycloak/chart/templates/` yet — Wave-2.C4 lands it) | n/a |
| `kc_idp_hint=catalyst-pin` injection | MISSING — Synapse `oidc_providers[].authorization_endpoint` would need explicit query param (Synapse honors it verbatim) | n/a |
| Cross-Org isolation | UNDEFINED — Wave-2 must enforce that Org-A's matrix instance trusts only Org-A's per-Org realm | n/a |

## Per-Org realm — 2-hop federation pattern (locked architecture, decision #6)

Each Org gets its own KC realm (`org-<slug>`) with a `Keycloak OIDC` Identity Provider entry that brokers to the `sovereign` realm's broker. The flow:

```
User → Matrix /_matrix/client/v3/login/sso/redirect
    → Matrix 302 to https://auth.<sov>/realms/org-<slug>/protocol/openid-connect/auth?client_id=matrix-synapse&kc_idp_hint=sovereign-broker
    → per-Org realm IDR fires → 303 to https://auth.<sov>/realms/org-<slug>/broker/sovereign-broker/login?session_code=...
    → per-Org KC server-side calls sovereign realm's authorize endpoint → silent SSO chain as Tier-1 (sovereign realm → catalyst-pin IdP)
    → catalyst-api /oidc/token mints code → per-Org realm gets id_token → per-Org realm mints its OWN id_token for matrix-synapse client
    → 302 back to Matrix /_synapse/client/oidc/callback?code=...&state=...
    → Matrix logs user in silently
```

## Per-Org realm client registration mechanism

Each Org's realm carries a `matrix-synapse` client. The realm itself is **provisioned by application-controller at Application install time** (not by the bp-matrix chart) — per locked decision #6, the per-Org realm is a one-realm-per-Org primitive owned by the bp-sso-bridge reconciler. The bp-matrix chart's only realm-side input is a ClientRegistrationConfigMap (or equivalent CRD) that bp-sso-bridge reads to mint the client when the App is installed.

Two viable mechanisms:

**Option A — `bp-sso-bridge` AppRegistration CRD (cleaner):** bp-matrix chart emits a `AppRegistration` CR (apiVersion `sso.openova.io/v1alpha1`) declaring its OIDC client requirements. bp-sso-bridge reconciler reads it, calls per-Org realm KC admin API, mints client, writes secret to OpenBao at `sso/org-<slug>/matrix-synapse`. ExternalSecret in matrix ns materializes K8s Secret.

**Option B — chart-side ConfigMap with label (faster):** bp-matrix chart emits a ConfigMap labeled `sso.openova.io/app-registration: matrix-synapse` carrying client config JSON. bp-sso-bridge watches ConfigMaps with this label and does the same work as Option A.

Recommendation: **Option B for V1** (less new CRD surface), promote to **Option A for V2** once 3+ Tier-3 apps share the pattern and the contract stabilizes.

## Required Wave-2 patch plan

### 1. Per-app SSO Secret materialization (new template — ExternalSecret pattern)

Add `platform/matrix/chart/templates/sso-externalsecret.yaml`:

```yaml
{{- if .Values.oidc.enabled }}
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: matrix-oidc-credentials
  namespace: {{ .Release.Namespace }}
  labels:
    catalyst.openova.io/blueprint: bp-matrix
    bp-sso-bridge.openova.io/consumer: bp-matrix
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault-region1
    kind: ClusterSecretStore
  target:
    name: matrix-oidc-credentials
    creationPolicy: Owner
  data:
    - secretKey: OIDC_CLIENT_SECRET
      remoteRef:
        key: sso/org-{{ .Values.org.slug }}/matrix-synapse
        property: client_secret
{{- end }}
```

### 2. Chart-side AppRegistration ConfigMap (new template — Option B)

Add `platform/matrix/chart/templates/sso-app-registration.yaml`:

```yaml
{{- if .Values.oidc.enabled }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: bp-matrix-sso-registration
  namespace: {{ .Release.Namespace }}
  labels:
    sso.openova.io/app-registration: matrix-synapse
data:
  client.json: |
    {
      "clientId": "matrix-synapse",
      "name": "Matrix Synapse ({{ .Values.org.slug }})",
      "enabled": true,
      "publicClient": false,
      "standardFlowEnabled": true,
      "directAccessGrantsEnabled": false,
      "redirectUris": ["https://matrix.{{ .Values.org.slug }}.{{ .Values.sovereign.fqdn }}/_synapse/client/oidc/callback"],
      "webOrigins": ["https://matrix.{{ .Values.org.slug }}.{{ .Values.sovereign.fqdn }}"],
      "defaultClientScopes": ["web-origins","profile","email","groups"],
      "attributes": {"access.token.lifespan": "900"}
    }
  realm: "org-{{ .Values.org.slug }}"
{{- end }}
```

### 3. Synapse `oidc_providers[]` config emission with `kc_idp_hint`

Wave-2 SSO agent extends `platform/matrix/chart/values.yaml:140-150` `matrix-synapse.extraConfig` (upstream subchart) to render `homeserver.yaml` `oidc_providers[]` with explicit query-param injection. Patch shape:

```yaml
matrix-synapse:
  extraConfig:
    oidc_providers:
      - idp_id: "catalyst-pin"
        idp_name: "Catalyst Sign-In"
        issuer: "https://auth.{{ .Values.sovereign.fqdn }}/realms/org-{{ .Values.org.slug }}"
        client_id: "matrix-synapse"
        client_secret_path: "/secrets/oidc/OIDC_CLIENT_SECRET"  # mounted from matrix-oidc-credentials Secret
        scopes: ["openid", "profile", "email", "groups"]
        # CRITICAL: explicit authorization_endpoint with kc_idp_hint forces silent SSO through the per-Org realm's IDR
        authorization_endpoint: "https://auth.{{ .Values.sovereign.fqdn }}/realms/org-{{ .Values.org.slug }}/protocol/openid-connect/auth?kc_idp_hint=sovereign-broker"
        token_endpoint: "https://auth.{{ .Values.sovereign.fqdn }}/realms/org-{{ .Values.org.slug }}/protocol/openid-connect/token"
        user_mapping_provider:
          config:
            localpart_template: "{{ "{{ user.preferred_username }}" }}"
            display_name_template: "{{ "{{ user.name }}" }}"
            email_template: "{{ "{{ user.email }}" }}"
```

### 4. Per-Org realm config (template owned by bp-keycloak, NOT bp-matrix)

Wave-2.C4 ships `platform/keycloak/chart/templates/configmap-per-org-realm.yaml` rendered once per Org with shape:

```jsonc
{
  "realm": "org-<slug>",
  "enabled": true,
  "identityProviders": [
    {
      "alias": "sovereign-broker",
      "providerId": "oidc",
      "enabled": true,
      "trustEmail": true,
      "config": {
        "clientId": "org-<slug>-broker",
        "clientSecret": "<helper-derived>",
        "authorizationUrl": "https://auth.<sov>/realms/sovereign/protocol/openid-connect/auth?kc_idp_hint=catalyst-pin",
        "tokenUrl": "https://auth.<sov>/realms/sovereign/protocol/openid-connect/token",
        "issuer": "https://auth.<sov>/realms/sovereign",
        "defaultScope": "openid email profile groups"
      }
    }
  ],
  "authenticatorConfig": [
    {"alias": "sovereign-broker-redirector", "config": {"defaultProvider": "sovereign-broker"}}
  ],
  "authenticationFlows": [
    {"alias": "browser", "authenticationExecutions": [
      {"authenticator": "identity-provider-redirector", "authenticatorConfig": "sovereign-broker-redirector", "requirement": "ALTERNATIVE", "priority": 25},
      {"flowAlias": "forms", "requirement": "ALTERNATIVE", "priority": 30, "autheticatorFlow": true}
    ]}
  ],
  "clients": [...]   // populated from each AppRegistration ConfigMap in the Org's namespace
}
```

The sovereign realm side ALSO needs a matching client registration `org-<slug>-broker` (so the per-Org realm can authenticate AS a client of the sovereign realm). That client lands in `configmap-sovereign-realm.yaml` `clients[]` array.

## 2-hop verification curl chain

Eight hops total (4 per realm):

```
HOP1: GET https://matrix.<org>.<sov-fqdn>/_matrix/client/v3/login/sso/redirect?redirectUrl=https://element.<org>.<sov-fqdn>
      → 302 → https://auth.<sov-fqdn>/realms/org-<slug>/protocol/openid-connect/auth?client_id=matrix-synapse&kc_idp_hint=sovereign-broker&...
HOP2: GET HOP1 (jar w/ catalyst_session)
      → 303 → https://auth.<sov-fqdn>/realms/org-<slug>/broker/sovereign-broker/login?session_code=...
HOP3: GET HOP2 (jar)
      → 302 → https://auth.<sov-fqdn>/realms/sovereign/protocol/openid-connect/auth?client_id=org-<slug>-broker&kc_idp_hint=catalyst-pin&...
HOP4: GET HOP3 (jar)
      → 303 → https://auth.<sov-fqdn>/realms/sovereign/broker/catalyst-pin/login?session_code=...
HOP5: GET HOP4 (jar)
      → 303 → https://api.<sov-fqdn>/oidc/auth?...
HOP6: GET HOP5 (jar)
      → 302 → KC sovereign broker callback (drops sovereign realm session cookie)
HOP7: GET HOP6 (jar)
      → 302 → KC per-Org realm broker callback (drops per-Org realm session cookie)
HOP8: GET HOP7 (jar)
      → 302 → https://matrix.<org>.<sov-fqdn>/_synapse/client/oidc/callback?code=...&state=...
HOP9: GET HOP8 (jar)
      → 200 — Matrix session established, redirect to original Element URL with login_token
```

## Cross-Org isolation test

```bash
# Setup: 2 orgs (org-a, org-b), 1 Matrix App per Org, 1 user per Org realm
# Test: org-a's user attempts to SSO into matrix.org-b.<sov-fqdn>

curl -ksI -b /tmp/org-a-session-jar -c /tmp/org-a-session-jar \
  "https://matrix.org-b.<sov-fqdn>/_matrix/client/v3/login/sso/redirect" \
  -L --max-redirs 20

# Expected: chain bounces to https://auth.<sov-fqdn>/realms/org-b/... → org-a user has NO session in org-b realm
# → KC returns either 200 (login form because org-a user not in org-b) OR 302 to org-b realm consent
# → Synapse callback receives id_token issued by org-b realm with org_id="org-b" claim
# → Synapse user_mapping_provider rejects (localpart already exists OR claim mismatch)
# REQUIRED: user authenticated to org-a CANNOT log into matrix.org-b URL without going through org-b's IdP flow
```

If the chain succeeds with the org-a session cookie, the per-Org realm broker config leaked sovereign-realm session bleed-through — STOP and fix the sovereign-realm broker's `syncMode: FORCE` + per-Org realm `firstBrokerLoginFlow=auto-link-by-email-IF-AND-ONLY-IF-org-claim-matches`.

## Chart-patch size estimate

| File | Lines added | Lines removed |
|---|---|---|
| `platform/matrix/chart/templates/sso-externalsecret.yaml` (new) | ~25 | 0 |
| `platform/matrix/chart/templates/sso-app-registration.yaml` (new) | ~20 | 0 |
| `platform/matrix/chart/values.yaml` | ~30 (extraConfig.oidc_providers shape + org.slug + sovereign.fqdn refs) | ~5 |
| `platform/matrix/chart/templates/deployment.yaml` patch — N/A (upstream chart owns deployment) | 0 | 0 |
| **Total per app** | **~75** | **~5** |

Chart bump: `1.0.1 → 1.1.0` (minor — adds SSO opt-in, no breaking change for off-by-default deploys). Lockstep: `Chart.yaml` + `blueprint.yaml` + bootstrap-kit slot pin.

## Gotchas

- Synapse OIDC requires `client_secret_path` (file) NOT `client_secret` (string) in 1.151+. The ExternalSecret-rendered Secret must be MOUNTED as a file at `/secrets/oidc/OIDC_CLIENT_SECRET`, NOT envFrom. Wave-2 patch MUST add a volume + volumeMount for this.
- Synapse rejects multiple `idp_id` collisions on reload; ensure the per-Org realm's `org-<slug>` value doesn't collide with a hardcoded `idp_id: "keycloak"` from chart values.yaml:161 — drop the `idp_id` default or namespace it.
