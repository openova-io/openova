# Tier-3 SSO audit — `bp-temporal`

> Federates to: per-Org realm via 2-hop (per-Org realm → `sovereign` realm broker → catalyst-pin IdP).
> Chart path: `platform/temporal/chart/`
> Chart version on `main`: `1.0.1` (Chart.yaml:3)
> Topology row (audit doc): rtz-A 🟢 A / rtz-B 🟡 P (App-tier CP-HA pair)

## Current SSO state — **PARTIAL** (JWT validation chart-side; OIDC client flow for the Web UI is the gap)

Temporal is unusual: it has TWO auth surfaces, and the chart wires only one of them.

| Surface | Status | File:line |
|---|---|---|
| Temporal frontend gRPC **JWT validation** (`server.config.authorization.jwtKeyProvider`) | PRESENT chart-side — `catalystOverlay.auth.keycloak.{issuer,realm,clientId,jwksUri}` populates `bp-temporal-keycloak-oidc` ConfigMap consumed by upstream chart | `platform/temporal/chart/values.yaml:183-202` + `templates/keycloak-oidc-config.yaml:16-36` |
| Temporal Web UI **browser OIDC client** (`temporal.web.config.auth.*`) | MISSING entirely — no Web UI OIDC config block in values, no envFrom on web Deployment | n/a |
| Per-app SSO Secret materialization | MISSING (no Secret template) | n/a |
| Per-Org realm registration | MISSING (no AppRegistration ConfigMap) | n/a |
| `kc_idp_hint=catalyst-pin` injection | MISSING | n/a |

Upstream `temporalio/web` (Temporal UI Server) supports OIDC SSO via env vars or config file (`TEMPORAL_AUTH_*` family). Without these, the Web UI is unauthenticated AND the frontend gRPC requires JWT tokens — net result: a UI user CAN browse the UI but every API call fails. That's the gap Wave-2 must close.

## Required Wave-2 patch plan

### 1. Extend chart-side OIDC for Web UI

Add `temporal.web.additionalEnv[]` to `platform/temporal/chart/values.yaml` (block currently ends at line 142, ingress disabled — extend before):

```yaml
temporal:
  web:
    enabled: true
    replicaCount: 1
    # ... existing ...
    additionalEnv:
      - name: TEMPORAL_AUTH_ENABLED
        value: "true"
      - name: TEMPORAL_AUTH_PROVIDER_URL
        valueFrom:
          secretKeyRef: {name: temporal-sso-oidc-credentials, key: PROVIDER_URL, optional: true}
      - name: TEMPORAL_AUTH_CLIENT_ID
        valueFrom:
          secretKeyRef: {name: temporal-sso-oidc-credentials, key: CLIENT_ID, optional: true}
      - name: TEMPORAL_AUTH_CLIENT_SECRET
        valueFrom:
          secretKeyRef: {name: temporal-sso-oidc-credentials, key: CLIENT_SECRET, optional: true}
      - name: TEMPORAL_AUTH_CALLBACK_URL
        value: "https://temporal.{{ .Values.org.slug }}.{{ .Values.sovereign.fqdn }}/auth/sso/callback"
      - name: TEMPORAL_AUTH_SCOPES
        value: "openid,profile,email,groups"
```

### 2. Per-app SSO Secret (new template — ExternalSecret pattern)

Add `platform/temporal/chart/templates/sso-externalsecret.yaml`:

```yaml
{{- if .Values.catalystOverlay.auth.enabled }}
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: temporal-sso-oidc-credentials
  namespace: {{ .Release.Namespace }}
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault-region1
    kind: ClusterSecretStore
  target:
    name: temporal-sso-oidc-credentials
    creationPolicy: Owner
    template:
      type: Opaque
      data:
        PROVIDER_URL:  "https://auth.{{ .Values.sovereign.fqdn }}/realms/{{ .Values.catalystOverlay.auth.keycloak.realm }}"
        CLIENT_ID:     "{{ .Values.catalystOverlay.auth.keycloak.clientId }}"
        CLIENT_SECRET: "{{ "{{ .client_secret }}" }}"
  data:
    - secretKey: client_secret
      remoteRef:
        key: sso/{{ .Values.catalystOverlay.auth.keycloak.realm }}/{{ .Values.catalystOverlay.auth.keycloak.clientId }}
        property: client_secret
{{- end }}
```

### 3. AppRegistration ConfigMap (new template)

Add `platform/temporal/chart/templates/sso-app-registration.yaml`:

```yaml
{{- if .Values.catalystOverlay.auth.enabled }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: bp-temporal-sso-registration
  namespace: {{ .Release.Namespace }}
  labels:
    sso.openova.io/app-registration: temporal-frontend
data:
  client.json: |
    {
      "clientId": "{{ .Values.catalystOverlay.auth.keycloak.clientId | default "temporal-frontend" }}",
      "name": "Temporal Web UI ({{ .Values.org.slug }})",
      "enabled": true,
      "publicClient": false,
      "standardFlowEnabled": true,
      "directAccessGrantsEnabled": false,
      "redirectUris": ["https://temporal.{{ .Values.org.slug }}.{{ .Values.sovereign.fqdn }}/auth/sso/callback"],
      "webOrigins": ["https://temporal.{{ .Values.org.slug }}.{{ .Values.sovereign.fqdn }}"],
      "defaultClientScopes": ["web-origins","profile","email","groups"]
    }
  realm: "{{ .Values.catalystOverlay.auth.keycloak.realm }}"
{{- end }}
```

### 4. Auto-derive `auth.keycloak.{issuer,jwksUri}` from realm + sovereign.fqdn

Patch `platform/temporal/chart/templates/keycloak-oidc-config.yaml:29` to derive these from a single `sovereign.fqdn` + `realm` value (drop operator burden of pasting 4 URLs):

```yaml
data:
  auth.yaml: |
    global:
      authorization:
        jwtKeyProvider:
          keySourceURIs:
            - "https://auth.{{ .Values.sovereign.fqdn }}/realms/{{ .Values.catalystOverlay.auth.keycloak.realm }}/protocol/openid-connect/certs"
          refreshInterval: 1h
        permissionsClaimName: permissions
        authorizer: default
        claimMapper: {{ .Values.catalystOverlay.auth.keycloak.claimMapper | quote }}
        issuer: "https://auth.{{ .Values.sovereign.fqdn }}/realms/{{ .Values.catalystOverlay.auth.keycloak.realm }}"
        audience: {{ .Values.catalystOverlay.auth.keycloak.clientId | quote }}
```

### 5. `kc_idp_hint` injection

Same as Langfuse: the Temporal Web UI's OAuth library derives the authorize URL from discovery. `kc_idp_hint` cannot be passed per-request. Silent SSO depends on per-Org realm IDR `defaultProvider=sovereign-broker` being bound — Wave-2.C4 owns that template.

## 2-hop verification curl chain (8 hops)

```
HOP1: GET https://temporal.<org>.<sov-fqdn>/
      → 302 → https://temporal.<org>.<sov-fqdn>/auth/sso/login
HOP2: GET HOP1 (jar)
      → 302 → https://auth.<sov-fqdn>/realms/<org-realm>/protocol/openid-connect/auth?client_id=temporal-frontend&...
HOP3-HOP6: 2-hop chain through per-Org realm IDR → sovereign realm → catalyst-pin → /oidc/auth → sovereign callback → per-Org callback
HOP7: → 302 → https://temporal.<org>.<sov-fqdn>/auth/sso/callback?code=...&state=...
HOP8: → 302 → https://temporal.<org>.<sov-fqdn>/  (silent SSO)
```

For the gRPC frontend probe (separate from Web UI):

```bash
# Mint a sovereign realm token, present to Temporal frontend gRPC
TOKEN=$(curl -sk -X POST "https://auth.<sov>/realms/<org-realm>/protocol/openid-connect/token" \
  -d "client_id=temporal-frontend&client_secret=<sec>&grant_type=client_credentials" | jq -r .access_token)
grpcurl -H "Authorization: Bearer $TOKEN" temporal.<org>.<sov-fqdn>:7233 temporal.api.workflowservice.v1.WorkflowService/GetSystemInfo
# Expected: 200 with SystemInfoResponse — JWT validated, claims extracted
```

## Cross-Org isolation test

The Temporal frontend's `audience` MUST match the per-Org realm's client name. An org-A-issued token (aud=temporal-frontend, iss=https://auth.<sov>/realms/org-a) presented to org-b's temporal frontend MUST be rejected by `jwtKeyProvider` because:

1. The JWKS URL for org-b's frontend points at `realms/org-b/.../certs` — won't validate signature from org-a realm.
2. The `issuer` config field for org-b's frontend is `realms/org-b` — issuer mismatch.

Verify:

```bash
TOKEN_A=$(curl ... org-a realm ...)
grpcurl -H "Authorization: Bearer $TOKEN_A" temporal.org-b.<sov-fqdn>:7233 ...
# Expected: 401 Unauthenticated, error_description=token signature verification failed
```

## Chart-patch size estimate

| File | Lines added | Lines removed |
|---|---|---|
| `platform/temporal/chart/templates/sso-externalsecret.yaml` (new) | ~30 | 0 |
| `platform/temporal/chart/templates/sso-app-registration.yaml` (new) | ~20 | 0 |
| `platform/temporal/chart/templates/keycloak-oidc-config.yaml` | ~5 (URL derivation) | ~5 |
| `platform/temporal/chart/values.yaml` | ~40 (`temporal.web.additionalEnv[]` block + auto-derive realm + sovereign.fqdn refs) | ~10 (drop separate jwksUri/issuer requirement) |
| **Total per app** | **~95** | **~15** |

Chart bump: `1.0.1 → 1.1.0` (minor — Web UI SSO is additive, gRPC JWT validation behavior unchanged when default off). Lockstep: `Chart.yaml` + `blueprint.yaml` + bootstrap-kit slot pin.

## Gotcha — `temporalio/ui-server` env-var name flip

Temporal's official UI server (`temporalio/ui-server`) renamed env vars across 2.x → 2.20+: `TEMPORAL_AUTH_*` was the 2.5+ shape; pre-2.5 used `TEMPORAL_UI_AUTH_*`. The chart's `appVersion: "1.31.0"` corresponds to Temporal server 1.31.0 — the upstream `temporal` chart 1.2.0 ships ui-server image 2.20+ which uses `TEMPORAL_AUTH_*`. If a per-Sovereign overlay pins an older ui-server image, the env-var names must flip.
