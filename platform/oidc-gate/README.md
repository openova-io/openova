# bp-oidc-gate — the ONE generic OIDC gate

**What:** A reusable oauth2-proxy instance set that brings login-less
external apps under the Sovereign zero-click SSO contract (#3374):
fresh browser → bare URL → Keycloak sovereign realm (silent via
catalyst-pin) → signed in. One component for every gated app — never
N bespoke per-app proxies.

**Role in Catalyst:** Control-plane infra (bootstrap-kit slot 13c).
Not an Application Blueprint — operators configure instances, end
users never see the gate itself.

**Canonical reference:** [`docs/SECURITY.md`](../../docs/SECURITY.md)
§6 (App-level SSO), [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md)
(bootstrap-kit slots).

## How it works

Per `instances[]` entry the chart renders:

| Resource | Purpose |
|---|---|
| Deployment `oidc-gate-<name>` | oauth2-proxy (keycloak-oidc provider, `--skip-provider-button`, `kc_idp_hint=catalyst-pin` login-url — the proven hubble-ui arg set, G117.5 #2744) |
| Service `oidc-gate-<name>` | :80 → :4180 |
| HTTPRoute `oidc-gate-<name>` | OWNS the app's public hostname on the Sovereign gateway. The gated app's own HTTPRoute must be disabled (two HTTPRoutes on one hostname is undefined routing) |
| ConfigMap `…-sso-registration` | `sso.openova.io/app-registration` label — bp-sso-bridge 0.2.3+ mints/adopts the Keycloak client in the sovereign realm and publishes the LIVE client_secret to OpenBao `sso/sovereign/<clientId>` |
| ExternalSecret `…-oidc` | materialises BOTH the `client-secret` and the `cookie-secret` for the proxy from the one `sso/<realm>/<clientId>` bundle (the credential topology that fixed hubble-ui, #3374, extended to the cookie secret in #5416) |

## Configuration knobs

```yaml
sovereignFQDN: hw130.omantel.biz   # REQUIRED (Inviolable Principle #4)
realm: sovereign
instances:
  - name: openova-flow             # hostname defaults <name>.<sovereignFQDN>
    clientId: openova-flow
    upstream: http://openova-flow-server.catalyst-system.svc.cluster.local:80
    skipJwtBearerTokens: true      # machine callers with issuer JWTs pass
    skipAuthRoutes:                # oauth2-proxy --skip-auth-route entries
      - "POST=^/v1/flows/[^/]+/events$"
```

Image is rendered `<global.registryMirror>/proxy-quay/oauth2-proxy/...`
so the kyverno `harbor-proxy-pull` glob (`*/proxy-*/*`) admits it in
non-excluded namespaces.

## Operational notes

- Stateless. Multi-region: per-cluster instances, but the session-cookie
  encryption key is NOT per-cluster state — bp-sso-bridge (≥ 0.2.26) derives
  one `cookie_secret` per Keycloak client into the OpenBao bundle and both
  regions resolve it, so a session minted behind either region is accepted by
  both. Before #5416 each region's Helm minted its own value and, with one
  shared wildcard VIP fanning the hostname at both regions' envoys, each gate
  rejected the peer's session cookie ("cookie signature not valid" / "CSRF
  token mismatch") — guacamole's SPA never rendered as a result.
- A gate instance serves 302s before bp-sso-bridge publishes the
  client bundle; the ExternalSecret converges eventually (same pattern
  as the Tier-1/2 SSO apps).
- Migrating an app that already has a bespoke proxy (e.g. hubble-ui in
  bp-cilium) = add an instance here + disable the bespoke proxy +
  route in that chart's values.
