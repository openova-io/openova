# bp-guacamole — Apache Guacamole Helm chart

Catalyst-authored Blueprint chart for Apache Guacamole — a clientless
HTML5 remote-desktop gateway. Brokers SSH / RDP / VNC sessions AND
kubectl-exec sessions through one browser-accessible surface, with
Keycloak SSO and session recording to SeaweedFS.

**One Guacamole per Sovereign** (per ADR-0001 §11 — no Manara-style
multi-cluster fan-out).

See `../README.md` (the `platform/guacamole/README.md`) for the
architecture diagram + use-case matrix.

## Default-OFF gate

`values.yaml` ships with `guacamole.enabled: false`. Per-Sovereign
overlay flips the gate on AND populates:

- `guacamole.guacd.image.tag` — SHA-pinned (CI populates)
- `guacamole.webapp.image.tag` — SHA-pinned (CI populates)
- `guacamole.httproute.hostname` — browser-facing FQDN
- `guacamole.oidc.issuer` — Keycloak realm URL

Empty values for any of those four `fail` the helm template render
(see `templates/_helpers.tpl`). This is INVIOLABLE-PRINCIPLES #4a +
#5 in action: the chart never ships floating tags or empty issuer
strings.

## Render check

```bash
# 0 resources when off
helm template bp-guacamole . | grep -c '^kind:'

# Full set when on (with required values supplied)
helm template bp-guacamole . \
  --set guacamole.enabled=true \
  --set guacamole.guacd.image.tag=1.5.5-r1 \
  --set guacamole.webapp.image.tag=1.5.5-r1 \
  --set guacamole.httproute.hostname=guacamole.sandbox.openova.io \
  --set guacamole.oidc.issuer=https://keycloak.sandbox.openova.io/realms/catalyst \
  | grep -c '^kind:'
```

## Resources rendered when ON

| Kind | Count | Purpose |
|---|---|---|
| Deployment | 2 | guacd + webapp |
| Service | 2 | guacd ClusterIP + webapp ClusterIP |
| HTTPRoute | 1 | Cilium Gateway browser entrypoint |
| PersistentVolumeClaim | 1 | recordings (RWO, hcloud-volumes) |
| SealedSecret | 1 | Keycloak OIDC client secret (placeholder) |
| NetworkPolicy | 1 | webapp egress (default-deny + selective) |
| ConfigMap | 1 | bp-keycloak realm-patch (guacamole client + role + group) |

That's **9 resources** when fully on.

## Notes

- `bp-keycloak` post-deploy `keycloak-config-cli` Job picks up the
  `bp-guacamole-realm-patch` ConfigMap (label
  `catalyst.openova.io/keycloak-config: realm-patch`) and merges its
  `realm-patch.json` into the named realm. This is the same pattern
  the rest of the Catalyst-curated stack uses for OIDC-client
  bootstrapping.
- The SealedSecret carries an empty placeholder; the operator runs
  `kubeseal` once to seal the real secret value.
- `bp-k8s-ws-proxy` is the per-node DaemonSet the webapp tunnels
  kubectl-exec sessions through. Without it the kubectl-shell
  protocol path is non-functional (other protocols still work).
