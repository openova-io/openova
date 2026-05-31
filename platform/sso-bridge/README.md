# bp-sso-bridge — Catalyst SSO framework

**What:** Auto-provisions a Keycloak OIDC Client for every Catalyst
Blueprint that opts in via `sso.enabled: true`, distributes the client
secret through OpenBao + External Secrets Operator, and grants the
Sovereign operator the application's `admin` role.

**Role in Catalyst:** Mandatory infra. Implements the App-level SSO
contract from [`docs/SECURITY.md`](../../docs/SECURITY.md) §6.3.

**Canonical reference:** [`docs/SECURITY.md`](../../docs/SECURITY.md) §6,
[`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md).

## Contract for an app author

A Blueprint opts in with a single value:

```yaml
sso:
  enabled: true
```

That's it. bp-sso-bridge owns:

- Reconciling the Keycloak `Client` CR in the Sovereign realm,
  derived from the HelmRelease name:
  - `clientId = <HR name with optional 'bp-' prefix stripped>`
  - `redirectURIs = [https://<clientId>.<sovereignFqdn>/...]` — broad
    set covering OAuth2, OIDC, Gitea, Grafana, Harbor, etc.
- Default OIDC scopes: `openid profile email groups org`
- ProtocolMappers for username, email, groups, org
- Storing the OAuth client secret at OpenBao `secret/sso/<clientId>`
- Emitting an `ExternalSecret` named `<clientId>-sso-oidc-credentials`
  in the app's namespace; bp-external-secrets writes a regular K8s
  Secret with keys `client_id`, `client_secret`, `issuer_url`,
  `authorize_url`, `token_url`, `userinfo_url`, `end_session_url`
- Granting the Sovereign operator the application's `admin` role —
  operator email is sourced from the per-Sovereign `sovereign-fqdn`
  ConfigMap key `orgEmail` (set by catalyst-api Phase-1 watcher from
  `deployment.request.OrgEmail`)

## How an app consumes it

The app's chart references the rendered Secret by name:

```yaml
gitea:
  oauth2:
    enabled: true
    credentialsSecret: gitea-sso-oidc-credentials   # the ExternalSecret target
```

Each app chart owns its own translation from Secret keys to its OIDC
config block — see `platform/gitea/chart/values.yaml` and
`platform/harbor/chart/values.yaml` for canonical patterns.

## Reconcile loop

Single-replica Deployment in the `sso-bridge` namespace. 60s tick.
On each tick:

1. Mint a Keycloak admin token via `client_credentials` grant against the
   catalyst-api-server SA client (mirrored from `keycloak` ns via
   emberstack/reflector).
2. Authenticate to OpenBao via Kubernetes-SA-token auth.
3. `kubectl get hr -A -o json` → filter `.spec.values.sso.enabled == true`.
4. For each app:
   - upsert Keycloak Client (POST new / PUT existing)
   - PUT `secret/sso/<clientId>` in OpenBao with full URL bundle
   - apply ExternalSecret CR in the HR's namespace
   - grant operator the client's `admin` role (best-effort — deferred if
     operator user not yet provisioned in the realm)
5. Sweep `bp-sso-bridge.openova.io/managed=true` ExternalSecrets whose
   `client-id` label is no longer in the managed set; delete those.

## Dependencies (Flux ordering)

- `bp-keycloak` (Sovereign realm + catalyst-api-server SA Client)
- `bp-openbao` (KV v2 store at `secret/`)
- `bp-external-secrets-stores` (ClusterSecretStore `vault-region1`)

## Issue trail

- G91.1 #2652 — framework (this chart)
- G91.gitea #2653 — bp-gitea opt-in
- G91.harbor #2654 — bp-harbor opt-in
- G91.openbao #2655 — bp-openbao opt-in (queued)
- G91.grafana #2656 — bp-grafana opt-in (queued)

Refs: SECURITY.md §6.3 (App-level SSO contract); ARCHITECTURE.md
naming conventions; `platform/keycloak/chart/templates/configmap-sovereign-realm.yaml`
(catalyst-api-server SA Client + reflector chain).
