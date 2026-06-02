# Tier-3 SSO audit — `bp-wordpress-tenant`

> Federates to: per-Org realm via 2-hop (per-Org realm → `sovereign` realm broker → catalyst-pin IdP).
> Chart path: `platform/wordpress-tenant/chart/`
> Chart version on `main`: `0.3.2` (Chart.yaml:71)
> Topology row (audit doc): rtz-A 🟢 A / rtz-B 🟡 P (App-tier CP-HA pair)

## Current SSO state — **BEST IN TIER-3** (full SSO chain via `openid-connect-generic` WP plugin)

This chart was authored under issue #915 (SME multi-tenant SSO) with a complete OIDC flow already wired. WordPress's `openid-connect-generic` plugin is installed + activated + configured by a post-install Job using `wp-cli`. The chart was DESIGNED for the per-Org realm model.

| Surface | Status | File:line |
|---|---|---|
| `oidc.*` values block (enabled / issuerURL / clientId / clientSecretName / defaultRole / identityKey / roleMapping / cliImage) | PRESENT | `platform/wordpress-tenant/chart/values.yaml:262-323` |
| Legacy back-compat `keycloak.*` alias | PRESENT | `platform/wordpress-tenant/chart/values.yaml:331-335` |
| Post-install Job that runs `wp plugin install openid-connect-generic --activate` + `wp option update openid_connect_generic_settings` | PRESENT | `platform/wordpress-tenant/chart/templates/oidc-config-job.yaml` |
| Per-app SSO Secret materialization | PARTIAL — values declares `clientSecretName: wordpress-oidc-client-secret` but chart does NOT emit the Secret; assumes it lands via PR #918 bp-keycloak tenant-realm ConfigMap render OR via per-Sovereign overlay's SealedSecret | `platform/wordpress-tenant/chart/values.yaml:288` |
| Per-Org realm registration | EXPECTED EXTERNAL — chart relies on `bp-keycloak` `configmap-tenant-realm.yaml` (Wave-2.C4) to register the `wordpress` client in the per-Org realm + emit the matching Secret | `platform/keycloak/chart/templates/configmap-tenant-realm.yaml` (exists per `ls`) |
| `kc_idp_hint=catalyst-pin` injection | MISSING — `oidc.issuerURL` is the discovery base; `openid-connect-generic` plugin derives `authorize_url` from discovery | `platform/wordpress-tenant/chart/values.yaml:279` |
| Cross-Org isolation | PER-vCLUSTER + PER-Org REALM — each tenant gets its own vCluster + its own KC realm (`sme-<subdomain>`) | architectural (#915) |

## Realm name divergence — `sme-<subdomain>` vs `org-<slug>`

The existing chart uses realm names like `sme-acme` (per `configmap-tenant-realm.yaml`). The G117 locked-decision #6 uses `org-<slug>`. Wave-2 must reconcile this:

**Option A — rename in chart values:** `oidc.realmName: "org-<slug>"` default; deprecate `sme-*` naming. Touches `wordpress-tenant` + `keycloak` (configmap-tenant-realm.yaml) + the orchestrator emit at `core/services/catalyst-api/internal/handler/sme_tenant_gitops.go` (`smeTenantBPWordPress`).

**Option B — keep `sme-*` as an alias for tenant-mode realms:** treat `sme-<subdomain>` and `org-<slug>` as the same thing depending on Org class. Document the equivalence and harmonize later.

Recommendation: **Option B for v1** (no chart churn, no orchestrator emit changes); **Option A in a follow-up** once Tier-3 fan-out stabilizes the per-Org realm shape across 5+ apps.

## Required Wave-2 patch plan — **MINIMAL** (chart is already 90% there)

### 1. `kc_idp_hint` injection — `openid-connect-generic` plugin supports it via `endpoint_login` override

Patch the `wp option update openid_connect_generic_settings` payload in `templates/oidc-config-job.yaml` to set `endpoint_login` explicitly with `?kc_idp_hint=sovereign-broker` appended:

```php
'endpoint_login' => '{{ trimSuffix "/" .Values.oidc.issuerURL }}/protocol/openid-connect/auth?kc_idp_hint=sovereign-broker',
```

(Other endpoints — `endpoint_userinfo`, `endpoint_token`, `endpoint_end_session` — keep discovery-derived defaults.)

### 2. Per-app SSO Secret materialization — chart-side `lookup`-or-generate (NEW template)

The chart today assumes the Secret `wordpress-oidc-client-secret` is materialized externally. Wave-2 adds a defensive chart-side Secret template that:

- Is `helm.sh/resource-policy: keep` annotated.
- Uses `lookup`-or-generate fallback so a fresh-prov Sovereign without bp-sso-bridge online still gets a stable client_secret.
- Is OVERRIDDEN by the bp-keycloak tenant-realm reconcile when it lands.

Add `platform/wordpress-tenant/chart/templates/sso-secret.yaml`:

```yaml
{{- if .Values.oidc.enabled }}
{{- $existing := lookup "v1" "Secret" .Release.Namespace .Values.oidc.clientSecretName -}}
{{- $clientSecret := "" -}}
{{- if $existing -}}{{- $clientSecret = index $existing.data "client-secret" | b64dec -}}{{- end -}}
{{- if not $clientSecret -}}{{- $clientSecret = randAlphaNum 32 -}}{{- end -}}
apiVersion: v1
kind: Secret
metadata:
  name: {{ .Values.oidc.clientSecretName }}
  namespace: {{ .Release.Namespace }}
  annotations:
    helm.sh/resource-policy: keep
  labels:
    catalyst.openova.io/blueprint: bp-wordpress-tenant
type: Opaque
stringData:
  client-secret: {{ $clientSecret | quote }}
{{- end }}
```

### 3. AppRegistration ConfigMap (new template — for bp-sso-bridge consumption)

Add `platform/wordpress-tenant/chart/templates/sso-app-registration.yaml`:

```yaml
{{- if .Values.oidc.enabled }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: bp-wordpress-tenant-sso-registration
  namespace: {{ .Release.Namespace }}
  labels:
    sso.openova.io/app-registration: wordpress
data:
  client.json: |
    {
      "clientId": "{{ .Values.oidc.clientId }}",
      "name": "WordPress ({{ .Values.smeDomain }})",
      "enabled": true,
      "publicClient": false,
      "standardFlowEnabled": true,
      "directAccessGrantsEnabled": false,
      "redirectUris": ["https://wordpress.{{ .Values.smeDomain }}/wp-admin/admin-ajax.php?action=openid-connect-authorize"],
      "webOrigins": ["https://wordpress.{{ .Values.smeDomain }}"],
      "defaultClientScopes": ["web-origins","profile","email","groups"]
    }
  realm: "{{ .Values.org.realm | default (printf "sme-%s" .Values.smeSubdomain) }}"
{{- end }}
```

### 4. Auto-derive `oidc.issuerURL` from `sovereign.fqdn` + `org.realm`

Patch `platform/wordpress-tenant/chart/values.yaml:279`:

```yaml
oidc:
  # Auto-derived when empty: https://auth.{{ sovereign.fqdn }}/realms/{{ org.realm | default sme-<subdomain> }}
  issuerURL: ""
```

Add corresponding `default` logic in `_helpers.tpl` `oidcIssuerURL`.

## 2-hop verification curl chain (8 hops)

```
HOP1: GET https://wordpress.<smeDomain>/wp-login.php?loginform=openid-connect&action=openid-connect-authorize
      → 302 → https://auth.<sov-fqdn>/realms/<org-realm>/protocol/openid-connect/auth?client_id=wordpress&kc_idp_hint=sovereign-broker&...
HOP2: GET HOP1 (jar w/ catalyst_session)
      → 303 → https://auth.<sov-fqdn>/realms/<org-realm>/broker/sovereign-broker/login?session_code=...
HOP3-HOP7: 2-hop chain through sovereign realm → catalyst-pin → /oidc/auth → callbacks
HOP8: GET final (jar)
      → 302 → https://wordpress.<smeDomain>/wp-admin/admin-ajax.php?action=openid-connect-authorize&code=...&state=...
HOP9: → 302 → https://wordpress.<smeDomain>/wp-admin/  (WordPress session cookie set)
```

## Cross-Org isolation test

```bash
# Org-A user session, hit Org-B WordPress:
curl -ks -b /tmp/org-a-jar -L --max-redirs 20 \
  "https://wordpress.org-b.<sov-fqdn>/wp-login.php?loginform=openid-connect&action=openid-connect-authorize"

# Expected: bounces through https://auth.<sov-fqdn>/realms/<org-b-realm>/...
# `openid-connect-generic` plugin matches users by `identityKey: preferred_username`
# Org-A's preferred_username may or may not exist in Org-B realm
# REQUIRED: WP plugin must verify `org_id` claim matches expected — if it doesn't,
#            user lands at WP login form (NOT auto-logged-in to org-b WP)
```

Note: today's chart's `openid-connect-generic` config doesn't enforce an `org_id` claim check — Wave-2 should add `roleMapping` / `acl_enabled=1` gating on the `org` claim that PR #2725 emits in the sovereign realm `org` clientScope. Without this, cross-Org token replay COULD log a user in (containment gap; track as follow-up under G117.X tenant-isolation hardening).

## Chart-patch size estimate

| File | Lines added | Lines removed |
|---|---|---|
| `platform/wordpress-tenant/chart/templates/sso-secret.yaml` (new) | ~25 | 0 |
| `platform/wordpress-tenant/chart/templates/sso-app-registration.yaml` (new) | ~25 | 0 |
| `platform/wordpress-tenant/chart/templates/oidc-config-job.yaml` (kc_idp_hint append) | ~2 | ~1 |
| `platform/wordpress-tenant/chart/templates/_helpers.tpl` (auto-derive issuerURL) | ~10 | 0 |
| `platform/wordpress-tenant/chart/values.yaml` | ~5 (sovereign.fqdn / org.realm refs) | ~2 |
| **Total per app** | **~67** | **~3** |

Chart bump: `0.3.2 → 0.4.0` (minor → minor: API surface unchanged for existing overlays; `oidc.*` block extended; back-compat `keycloak.*` alias preserved per existing chart pattern). Lockstep: `Chart.yaml` + `blueprint.yaml` + bootstrap-kit slot pin.

## Strength — chart already implements many Tier-3 idioms

The bp-wordpress-tenant chart is the canonical reference for Tier-3 SSO patterns in this audit:

- Post-install Job for app-side config (vs assuming app chart natively supports OIDC env vars).
- Per-tenant Keycloak realm separation (per `configmap-tenant-realm.yaml`).
- Idempotent `wp-cli` calls (`wp plugin is-installed` before install).
- Back-compat values alias for orchestrator-emit lag.

Wave-2 SSO agents authoring Langfuse / Temporal / LibreChat / etc. should crib from this chart.
