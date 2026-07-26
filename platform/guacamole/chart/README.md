# bp-guacamole — Apache Guacamole Helm chart

Catalyst-authored Blueprint chart for Apache Guacamole — a clientless
HTML5 remote-desktop gateway. Brokers SSH / RDP / VNC sessions AND
kubectl-exec sessions through one browser-accessible surface, with
Keycloak SSO and session recording to SeaweedFS.

**One Guacamole per Sovereign** (per ADR-0001 §11 — no Manara-style
multi-cluster fan-out).

See `../README.md` (the `platform/guacamole/README.md`) for the
architecture diagram + use-case matrix.

## SSO — `guacamole.sso.mode` (#5358)

- **`header` (DEFAULT)** — the Sovereign's `bp-oidc-gate` (oauth2-proxy,
  bootstrap-kit slot 13c, instance `guacamole`) fronts
  `guacamole.<fqdn>` and runs the Keycloak **authorization-code flow**
  server-side; the webapp consumes the gate-injected
  `X-Forwarded-Email` via the image-bundled `guacamole-auth-header`
  extension. No `id_token` ever transits the browser URL. In this mode
  the chart renders **no HTTPRoute** (the gate owns the hostname) and
  adds an **ingress NetworkPolicy** so only the gate pods reach the
  webapp `:8080` (header identity is trusted).
- **`openid` (legacy)** — the native `guacamole-auth-sso-openid`
  extension. Upstream is implicit-flow-only through 1.6.0 and the SPA
  re-submits the fragment `id_token` on every route change while the
  server-side nonce is single-use — the replay poisons the fresh
  session (`Rejected OpenID token with invalid/old nonce`, live on
  hw288 — issue #5358). Kept for operators fronting guacamole with
  their own code-flow proxy; requires `httproute.hostname` +
  `oidc.issuer`.

## Default-OFF gate

`values.yaml` ships with `guacamole.enabled: false`. Per-Sovereign
overlay flips the gate on AND populates:

- `guacamole.guacd.image.tag` — SHA-pinned (CI populates)
- `guacamole.webapp.image.tag` — SHA-pinned (CI populates)
- `guacamole.httproute.hostname` — browser-facing FQDN (`sso.mode=openid` only)
- `guacamole.oidc.issuer` — Keycloak realm URL (`sso.mode=openid` only)

Empty values for any required key `fail` the helm template render
(see `templates/_helpers.tpl`). This is INVIOLABLE-PRINCIPLES #4a +
#5 in action: the chart never ships floating tags or empty issuer
strings.

## Render check

```bash
# 0 resources when off
helm template bp-guacamole . | grep -c '^kind:'

# Full set when on (default header mode — no hostname/issuer required)
helm template bp-guacamole . \
  --set guacamole.enabled=true \
  --set guacamole.guacd.image.tag=1.5.5-r1 \
  --set guacamole.webapp.image.tag=1.5.5-r1 \
  | grep -c '^kind:'
```

## Resources rendered when ON (default header mode)

| Kind | Count | Purpose |
|---|---|---|
| Deployment | 2 | guacd + webapp |
| Service | 2 | guacd ClusterIP + webapp ClusterIP |
| NetworkPolicy | 2 | webapp egress + #5358 gate-only ingress guard |
| Job + SA + Role + RB + CR + CRB | 6 | recordings storageClass-migration hook (with `recordings.persistence=true`) |
| PersistentVolumeClaim | 1 | recordings (only with `recordings.persistence=true`) |

The CNPG `Cluster` + JDBC schema-seed Job + admin-enroll CronJob
(#3374 permission store) additionally render on clusters exposing the
`postgresql.cnpg.io/v1` API. In `sso.mode=openid` the HTTPRoute + the
chart-managed OIDC `Secret` (or SealedSecret/bootstrap-Job legacy pair)
render as before — see `tests/render.sh` for the exact bundles.

## Notes

- The public hostname `guacamole.<fqdn>` is served by the slot-13c
  `bp-oidc-gate` HTTPRoute (instance `guacamole`), NOT by this chart,
  in the default header mode.
- `bp-k8s-ws-proxy` is the per-node DaemonSet the webapp tunnels
  kubectl-exec sessions through. Without it the kubectl-shell
  protocol path is non-functional (other protocols still work).
