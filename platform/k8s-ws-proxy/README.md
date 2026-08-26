# bp-k8s-ws-proxy — k8s-ws-proxy Blueprint

Catalyst-built Go binary + Helm chart wrapping the per-node
WebSocket exec proxy (`core/cmd/k8s-ws-proxy/`).

## Why this exists

Browsers can't reach the kube-apiserver directly without exposing
kubeconfig tokens (INVIOLABLE-PRINCIPLES #5). Putting a per-node
DaemonSet in front lets:

1. The catalyst-api forward exec requests with HMAC-signed
   WebSocket upgrades — **no kubeconfig in the browser**.
2. Sessions stay node-local (`internalTrafficPolicy: Local`) — the
   kube-proxy short-circuits onto the same node's pod, eliminating
   cross-node hops.
3. NetworkPolicy gates exec traffic at the per-node DaemonSet's
   pod IPs (one selector, one policy).

See `core/cmd/k8s-ws-proxy/DESIGN.md` for the wire contract +
failure-mode matrix.

## Two credentials, one authority (0.1.17, #5991 / UAT row 115)

The proxy accepts the same authority in two presentations, decided in one
place — `auth.Authorizer` in `core/cmd/k8s-ws-proxy/internal/auth/authorize.go`:

| Presentation | Who uses it | Listener |
|---|---|---|
| `X-Catalyst-Timestamp` + `X-Catalyst-HMAC` headers | catalyst-api, the console SPA | `:8080` plaintext (and `:8443`) |
| TLS client certificate | Apache guacd | `:8443` only |

**Why the second one exists.** guacd cannot set HTTP headers on its
WebSocket upgrade — its `kubernetes` protocol builds the upgrade through
libwebsockets and the path with a literal `snprintf`
(guacamole-server 1.5.5, `src/protocols/kubernetes/{kubernetes,url}.c`).
The only credential it can present is a TLS client certificate
(`client-cert` / `client-key` / `ca-cert`, `ssl.c`). Until it could, no
Guacamole connection through this proxy could authenticate, which is why
UAT row 115's connections list had no producer whose row survived a click.

**The certificate leg is fail-closed, twice.** A certificate must chain to
`TLS_CLIENT_CA_FILE` (the Go TLS stack rejects anything else at handshake
time, before a handler runs) *and* carry a CN or DNS SAN on
`CLIENT_CERT_ALLOWED_SUBJECTS`. An **empty allowlist disables the mode**,
so enabling TLS never by itself opens a second way in; and a certificate
that is presented and denied does **not** fall back to the HMAC leg.

The chart mints both leaves from a private CA of its own
(`templates/certs.yaml`) and mirrors the client Secret into the namespaces
in `tls.reflectClientCertTo`. That CA signs two certificates and nothing
else, so holding some other cert-manager certificate on the Sovereign
grants nothing here.

## Pod-alias resolution

`k8sWsProxy.podAliasLabel` (default `app.kubernetes.io/name`) lets the pod
segment of an exec URL name a **workload**. This is what keeps a *stored*
Guacamole connection working: the row is written once and read on every
click, so a literal Deployment/DaemonSet pod name in it goes stale at the
first rollout. The literal Pod name is still tried first — existing callers
that name a real Pod are unaffected — and a workload name matching no
Running Pod is a hard 404, never a guess. Set it empty to disable
resolution entirely (no apiserver read per request).

## Default-OFF gate

`values.yaml` ships `k8sWsProxy.enabled: false`. Per-Sovereign
overlay flips on AND populates:

- `k8sWsProxy.image.tag` — SHA-pinned (CI populates)
- `k8sWsProxy.hmacSecret.name` — name of the SealedSecret holding
  the shared HMAC key (sovereign-admin pre-creates with `kubeseal`)

Empty values for either fail the `helm template` render.

## Render check

```bash
# 0 resources when off
helm template bp-k8s-ws-proxy . | grep -c '^kind:'

# Full set when on
helm template bp-k8s-ws-proxy . \
  --set k8sWsProxy.enabled=true \
  --set k8sWsProxy.image.tag=abc1234 \
  | grep -c '^kind:'
```
