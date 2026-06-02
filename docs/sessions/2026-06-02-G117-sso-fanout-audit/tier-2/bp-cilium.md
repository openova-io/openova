# Tier-2 SSO audit — `bp-cilium` (Hubble UI)

> Federates to: `sovereign` realm (single hop) — Catalyst-CP admin UI tier.
> Chart path: `platform/cilium/chart/`
> Chart version on `main`: `1.3.6` (Chart.yaml:49)
> Topology row (audit doc): per-host-cluster singleton (`hubble.<sov>` exposed only on mgmt-A by default)

## Current SSO state — **SCAFFOLD-ONLY** (HTTPRoute exists, OIDC enforcement does not)

Hubble UI's per-Sovereign exposure is implemented at the HTTPRoute level. The chart documents an `auth: oidc` knob but **does not actually emit any OIDC filter, secret, or env-var wiring**. Hubble UI itself is a static SPA + backend with no native OIDC client — enforcement must happen at the gateway/proxy boundary.

| Surface | Status | File:line |
|---|---|---|
| HTTPRoute exposing Hubble UI through Cilium Gateway | PRESENT | `platform/cilium/chart/templates/hubble-ui-httproute.yaml:1-67` |
| `catalystOverlay.hubbleUI.auth: oidc` knob | PRESENT but DECORATIVE — only emitted as an HTTPRoute annotation, no filter attached | `platform/cilium/chart/values.yaml:313` + `hubble-ui-httproute.yaml:52` |
| Realm-patch ConfigMap / KC client registration | MISSING entirely | n/a |
| Per-app SSO Secret | MISSING entirely | n/a |
| OIDC enforcement filter (oauth2-proxy sidecar, Envoy ext_authz, Cilium L7 OIDC filter) | MISSING | n/a |
| `kc_idp_hint=catalyst-pin` injection | N/A (no OIDC layer to inject into yet) | n/a |

Chart comment at `hubble-ui-httproute.yaml:27-33` explicitly acknowledges the gap:

> "The OIDC enforcement at the gateway boundary is NOT yet wired here — Cilium's L7 OIDC filter is the target (configured via CiliumEnvoyConfig or HTTPRoute filter once bp-keycloak ships the realm). For Phase-0 this template gives EPIC-5 the seam to extend; today the route forwards unauthenticated, so flipping the gate without the OIDC layer leaves Hubble UI publicly accessible."

## Required Wave-2 patch plan

### 1. OIDC enforcement strategy — oauth2-proxy sidecar (recommended)

Hubble UI's upstream chart has no `oidc` block. The canonical Catalyst pattern for "wrap an OIDC-unaware backend in silent SSO" is an **oauth2-proxy sidecar** in front of the Hubble UI Service, fronted by the HTTPRoute. Alternative is a CiliumEnvoyConfig with the OIDC filter — heavier and ties Hubble's auth lifecycle to envoy proxy reload.

Wave-2 SSO agent ships:

- New template `platform/cilium/chart/templates/hubble-ui-oauth2-proxy-deployment.yaml` — `quay.io/oauth2-proxy/oauth2-proxy:v7.7.x` Deployment with envFrom pointing at the per-app SSO Secret.
- New template `platform/cilium/chart/templates/hubble-ui-oauth2-proxy-service.yaml` — ClusterIP Service on `:4180`.
- Patch `platform/cilium/chart/templates/hubble-ui-httproute.yaml` `backendRefs` from `hubble-ui:80` → `hubble-ui-oauth2-proxy:4180`.

### 2. Per-app SSO Secret materialization (new template)

Add `platform/cilium/chart/templates/secret-sso-hubble-credentials.yaml`:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: hubble-ui-sso-oidc-credentials
  namespace: {{ .Values.catalystOverlay.hubbleUI.serviceRef.namespace }}  # kube-system
  annotations:
    helm.sh/resource-policy: keep
  labels:
    catalyst.openova.io/blueprint: bp-cilium
    catalyst.openova.io/component: hubble-ui-sso
type: Opaque
stringData:
  # oauth2-proxy env-var names
  OAUTH2_PROXY_CLIENT_ID: "hubble-ui"
  OAUTH2_PROXY_CLIENT_SECRET: {{ (lookup "v1" "Secret" "kube-system" "hubble-ui-sso-oidc-credentials").data.OAUTH2_PROXY_CLIENT_SECRET | default (randAlphaNum 32 | b64enc) | b64dec | quote }}
  OAUTH2_PROXY_COOKIE_SECRET: {{ randAlphaNum 32 | b64enc | quote }}  # rotate-tolerant
  OAUTH2_PROXY_OIDC_ISSUER_URL: "https://auth.{{ required "sovereign.fqdn required" .Values.sovereign.fqdn }}/realms/sovereign"
  OAUTH2_PROXY_REDIRECT_URL: "https://hubble.{{ .Values.sovereign.fqdn }}/oauth2/callback"
  OAUTH2_PROXY_LOGIN_URL: "https://auth.{{ .Values.sovereign.fqdn }}/realms/sovereign/protocol/openid-connect/auth?kc_idp_hint=catalyst-pin"
```

oauth2-proxy reads `OAUTH2_PROXY_LOGIN_URL` and uses it verbatim (it does NOT re-derive from discovery), so this is the kc_idp_hint injection point.

### 3. Required KC client config (additions to `configmap-sovereign-realm.yaml` `clients[]` array)

```jsonc
{
  "clientId": "hubble-ui",
  "name": "Cilium Hubble UI (Sovereign network observability)",
  "enabled": true,
  "publicClient": false,
  "secret": "<materialized-via-helper>",
  "standardFlowEnabled": true,
  "directAccessGrantsEnabled": false,
  "redirectUris": ["https://hubble.{{ .Values.sovereignFQDN }}/oauth2/callback"],
  "webOrigins": ["https://hubble.{{ .Values.sovereignFQDN }}"],
  "defaultClientScopes": ["web-origins","acr","profile","roles","email","groups"],
  "attributes": {"access.token.lifespan": "900"}
}
```

### 4. `kc_idp_hint=catalyst-pin` injection point

`OAUTH2_PROXY_LOGIN_URL` env in the Secret above (Step 2). oauth2-proxy 7.7+ honors a fully-formed login URL with query params.

### 5. NetworkPolicy + namespace placement note

Hubble UI lives in `kube-system` per `01-cilium.yaml` `targetNamespace`. The oauth2-proxy sidecar Deployment + Service + Secret must ALL land in `kube-system` to allow same-namespace ClusterIP routing without cross-namespace ReferenceGrants. Wave-2 SSO agent honors `.Values.catalystOverlay.hubbleUI.serviceRef.namespace` consistently across all 3 new templates.

## 4-hop curl probe (verification)

```
HOP1: GET https://hubble.<sov-fqdn>/  (no cookie)
      → 302 → https://hubble.<sov-fqdn>/oauth2/start?rd=%2F
HOP2: GET HOP1 (jar)
      → 302 → https://auth.<sov-fqdn>/realms/sovereign/protocol/openid-connect/auth?client_id=hubble-ui&kc_idp_hint=catalyst-pin&...
HOP3: GET HOP2 (jar w/ catalyst_session)
      → 303 → https://auth.<sov-fqdn>/realms/sovereign/broker/catalyst-pin/login?session_code=...
HOP4: GET HOP3 (jar)
      → 303 → https://api.<sov-fqdn>/oidc/auth?... → 302 back through KC broker callback
HOP5: GET final redirect (jar)
      → 302 → https://hubble.<sov-fqdn>/oauth2/callback?code=...&state=...
HOP6: GET HOP5 (jar) → 302 → https://hubble.<sov-fqdn>/  (oauth2-proxy sets session cookie, proxies to Hubble UI)
```

Six hops because oauth2-proxy adds 2 (start + callback). All 4 OIDC hops in the middle are silent (HTTP 302/303 chain with no HTML rendered).

If HOP1 returns 200 instead of 302: oauth2-proxy Deployment not Ready OR HTTPRoute still points at `hubble-ui:80` directly (Wave-2 backendRefs patch missed). If HOP6 returns 401: client_secret mismatch (chart-cred Defense pattern).

## Chart-patch size estimate

| File | Lines added | Lines removed |
|---|---|---|
| `platform/cilium/chart/templates/hubble-ui-oauth2-proxy-deployment.yaml` (new) | ~60 | 0 |
| `platform/cilium/chart/templates/hubble-ui-oauth2-proxy-service.yaml` (new) | ~15 | 0 |
| `platform/cilium/chart/templates/secret-sso-hubble-credentials.yaml` (new) | ~25 | 0 |
| `platform/cilium/chart/templates/hubble-ui-httproute.yaml` | ~2 (backendRefs swap) | ~2 |
| `platform/cilium/chart/values.yaml` | ~15 (new `catalystOverlay.hubbleUI.oauth2Proxy.{image,resources}` block + `sovereign.fqdn` ref) | 0 |
| **Total per app** | **~117** | **~2** |

LARGEST Tier-2 patch by ~3× because OIDC enforcement layer is entirely new. Chart bump: `1.3.6 → 1.4.0` (minor — additive feature, oauth2-proxy is opt-in via existing `hubbleUI.auth: oidc` gate). Lockstep: `Chart.yaml` + `blueprint.yaml` + bootstrap-kit slot pin.

## Risk note — Hubble UI ON by default

Today `catalystOverlay.hubbleUI.enabled: false` chart-side, but `clusters/_template/bootstrap-kit/01-cilium.yaml` flips it ON for every Sovereign. **Wave-2 PR must NOT regress: oauth2-proxy MUST gate the same enable bit so a fresh prov never exposes Hubble UI unauthenticated**. The current `auth: oidc` annotation is decorative; the SECOND a Sovereign reconciles bp-cilium 1.4.0 the proxy MUST be in front. Recommend a hollow-chart-style render-gate in `tests/`:

```bash
helm template ... --set catalystOverlay.hubbleUI.enabled=true ... | yq 'select(.kind == "HTTPRoute" and .metadata.name == "hubble-ui") | .spec.rules[].backendRefs[].name' | grep oauth2-proxy
```
