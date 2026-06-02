# Tier-3 SSO audit — `bp-openmeter`

> Federates to: per-Org realm via 2-hop (per-Org realm → `sovereign` realm broker → catalyst-pin IdP).
> Chart path: `platform/openmeter/chart/`
> Chart version on `main`: `1.0.1` (Chart.yaml:3)
> Topology row (audit doc): rtz-A 🟢 A / rtz-B 🟡 P (App-tier CP-HA pair)

## Current SSO state — **NONE**

| Surface | Status | File:line |
|---|---|---|
| `oidc.*` / `sso.*` / `auth.*` values block | MISSING | n/a |
| Upstream `openmeter.config.auth.*` overrides | EMPTY — `openmeter.config: {}` placeholder only | `platform/openmeter/chart/values.yaml:102` |
| Per-app SSO Secret materialization | MISSING | n/a |
| Per-Org realm registration | MISSING | n/a |
| `kc_idp_hint=catalyst-pin` injection | MISSING | n/a |

## Upstream chart OIDC handle — **AMBIGUOUS / WEAK**

OpenMeter's upstream chart (`oci://ghcr.io/openmeterio/helm-charts/openmeter` v1.0.0-beta.213) doesn't ship a dedicated OIDC block. OpenMeter's binary at this version is **API-first** with the UI being a separate `openmeter-cloud` product that's NOT open-source.

The OSS OpenMeter API supports auth via OIDC bearer tokens but the config surface is:

- `OPENMETER_AUTH_OIDC_ISSUER` — discovery URL
- `OPENMETER_AUTH_OIDC_AUDIENCE` — expected audience claim
- `OPENMETER_AUTH_OIDC_REQUIRED_SCOPES` — comma-separated

This is a **JWT validation surface (resource server)**, NOT a browser OIDC client. There is **no UI to silent-SSO into** — only an API to authorize.

## Reclassification recommendation

`bp-openmeter` belongs in a NEW classification:

> **Tier-3-API** — API-only services that validate bearer tokens. No browser redirect. No AppRegistration ConfigMap. No `kc_idp_hint`. Wave-2 wires JWT validation env vars only.

## Required Wave-2 patch plan (reduced — API-only tier)

### 1. Extend `openmeter.config` for OIDC JWT validation

Patch `platform/openmeter/chart/values.yaml:102`:

```yaml
openmeter:
  config:
    auth:
      oidc:
        enabled: true
        # Auto-derived when empty: https://auth.{{ sovereign.fqdn }}/realms/{{ org.realm }}
        issuer: ""
        audience: "openmeter"
        requiredScopes: "openmeter:read,openmeter:write"
```

### 2. ConfigMap-driven config — likely an envFrom on the OpenMeter API Deployment

Inspect upstream chart's API Deployment for env conventions. Wave-2 may need to ship a values overlay:

```yaml
openmeter:
  api:
    extraEnv:
      - name: OPENMETER_AUTH_OIDC_ISSUER
        value: "https://auth.{{ .Values.sovereign.fqdn }}/realms/{{ .Values.org.realm }}"
      - name: OPENMETER_AUTH_OIDC_AUDIENCE
        value: "openmeter"
      - name: OPENMETER_AUTH_OIDC_REQUIRED_SCOPES
        value: "openmeter:read,openmeter:write"
```

If upstream chart doesn't honor `extraEnv` on the API Deployment, Wave-2 must either upstream a chart PR OR add a Catalyst-side patch via Kyverno mutate or post-render Job.

### 3. Per-app SSO **client** registration (still needed)

Even though there's no browser flow, OpenMeter clients (other services calling its API) need an OIDC client_credentials grant against the per-Org realm. Wave-2 adds an AppRegistration ConfigMap that registers a **service-account client**:

```yaml
{{- if .Values.openmeter.config.auth.oidc.enabled }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: bp-openmeter-sso-registration
  namespace: {{ .Release.Namespace }}
  labels:
    sso.openova.io/app-registration: openmeter-api
data:
  client.json: |
    {
      "clientId": "openmeter",
      "name": "OpenMeter ({{ .Values.org.slug }})",
      "enabled": true,
      "publicClient": false,
      "serviceAccountsEnabled": true,
      "standardFlowEnabled": false,
      "directAccessGrantsEnabled": false,
      "redirectUris": [],
      "webOrigins": [],
      "defaultClientScopes": ["roles","openmeter:read","openmeter:write"]
    }
  realm: "{{ .Values.org.realm }}"
{{- end }}
```

### 4. No per-app SSO Secret materialization needed for the API server itself

The API server only validates tokens — it doesn't hold a client_secret. Service-account credentials for OpenMeter clients (other Apps calling OpenMeter) are minted by their own ExternalSecrets in their own namespaces. **Wave-2 does NOT add an ExternalSecret to `platform/openmeter/`**.

### 5. No `kc_idp_hint` injection

N/A — no browser flow.

## Verification probe (NOT 4-hop chain — single curl JWT-validation test)

```bash
# Mint a service-account token for openmeter client
TOKEN=$(curl -sk -X POST \
  "https://auth.<sov-fqdn>/realms/<org-realm>/protocol/openid-connect/token" \
  -d "client_id=openmeter&client_secret=<sec>&grant_type=client_credentials&scope=openmeter:read" \
  | jq -r .access_token)

# Present to OpenMeter API
curl -sk -H "Authorization: Bearer $TOKEN" \
  "https://openmeter.<org>.<sov-fqdn>/api/v1/meters"
# Expected: 200 with meter list (JWT validated, scopes extracted, request authorized)

# Cross-Org isolation
TOKEN_A=$(curl ... org-a realm ...)
curl -sk -H "Authorization: Bearer $TOKEN_A" \
  "https://openmeter.org-b.<sov-fqdn>/api/v1/meters"
# Expected: 401 — token issuer (org-a realm) doesn't match API server's expected issuer (org-b realm)
```

## Chart-patch size estimate

| File | Lines added | Lines removed |
|---|---|---|
| `platform/openmeter/chart/templates/sso-app-registration.yaml` (new) | ~25 | 0 |
| `platform/openmeter/chart/values.yaml` (config.auth.oidc.* block + api.extraEnv) | ~25 | 0 |
| **Total per app** | **~50** | **0** |

Smaller than other Tier-3 apps because no browser flow. Chart bump: `1.0.1 → 1.1.0` (minor — additive). Lockstep: `Chart.yaml` + `blueprint.yaml` + bootstrap-kit slot pin.

## Risk

If upstream `openmeter` chart 1.0.0-beta.213 doesn't expose `api.extraEnv` (some beta charts don't), Wave-2 must either:

1. PR upstream — slow.
2. Add a Kustomize patch in `catalystOverlay` (still chart-rendered) — fragile across upstream version bumps.
3. Add a post-install Job that `kubectl patch` the Deployment to inject env vars — least invasive.

Wave-2 should verify upstream chart shape BEFORE committing the patch direction. `helm pull oci://ghcr.io/openmeterio/helm-charts/openmeter --version 1.0.0-beta.213 --untar` and grep templates.
