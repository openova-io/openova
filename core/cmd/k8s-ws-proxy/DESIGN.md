# k8s-ws-proxy — design

EPIC-4 K1 — `core/cmd/k8s-ws-proxy/` is a per-node WebSocket exec
proxy. It accepts HMAC-signed WebSocket upgrades from upstream callers
(catalyst-api, Guacamole) and bridges them to the local kube-apiserver
`/api/v1/.../pods/exec` stream.

## Why this exists

The browser cannot reach the kube-apiserver directly without exposing
kubeconfig tokens to the SPA — that violates INVIOLABLE-PRINCIPLES #5
(credential exposure). The catalyst-api could in principle proxy exec
streams itself; we put a per-node DaemonSet in front instead because:

1. Exec sessions are sticky to a single TCP connection. Landing on the
   target Pod's node minimizes session jitter.
2. Per-node proxies make NetworkPolicy effective (operator can
   `default-deny` exec from anywhere except the per-node DaemonSet
   IPs).
3. The catalyst-api Pod is a single Deployment behind one HTTPRoute;
   sharing one TCP connection across N concurrent exec streams creates
   head-of-line blocking the kube-apiserver doesn't have.

## Wire contract

### Inbound (browser → proxy)

```
WebSocket UPGRADE GET /proxy/exec/{namespace}/{pod}/{container}?command=sh&tty=true
Headers:
  Sec-WebSocket-Protocol: v4.channel.k8s.io
  X-Catalyst-Timestamp:   <unix-seconds>
  X-Catalyst-HMAC:        hex(HMAC-SHA256(shared-secret, "<unix-seconds>:<request-path>"))
```

The HMAC covers (timestamp, URL path). Query-string is excluded.
Replay window: ±5 minutes (operator-tunable via `HMAC_SKEW_SECONDS`).

### Inbound (guacd → proxy) — mTLS, #5991

Apache guacd cannot present the HMAC pair. Its `kubernetes` protocol
builds the upgrade through libwebsockets and exposes no hook for custom
HTTP headers (guacamole-server 1.5.5,
`src/protocols/kubernetes/kubernetes.c`: the `lws_client_connect_info`
it fills carries host/address/origin/port/protocol/path and nothing
else). It also builds the PATH with a literal `snprintf`
(`src/protocols/kubernetes/url.c`), so the path is not configurable
either. What it *does* expose is TLS client-certificate material —
`client-cert`, `client-key`, `ca-cert`, read as in-memory PEM in
`src/protocols/kubernetes/ssl.c`.

So the proxy serves a second listener and a second path shape:

```
WebSocket UPGRADE GET /api/v1/namespaces/{ns}/pods/{pod}/exec
                      ?command=…&container=…&stdin=true&stdout=true&tty=true
  over TLS on :8443, caller presenting a client certificate
Headers:
  Sec-WebSocket-Protocol: v4.channel.k8s.io
```

Both listeners serve the SAME mux and the same handler; the difference
is only which credential can authenticate. `auth.Authorizer`
(`internal/auth/authorize.go`) is the single seam that decides:

1. certificate PRESENTED and the mode enabled ⇒ the certificate decides,
   accept or deny, **no fallback to HMAC** (falling through would let a
   caller mask a denied identity behind a second credential);
2. otherwise the HMAC headers decide — byte-identical to pre-#5991.

Certificate acceptance requires BOTH a chain the Go TLS stack verified
against `TLS_CLIENT_CA_FILE` (`ClientAuth: VerifyClientCertIfGiven`, so
HMAC callers presenting nothing still work) AND a CN/DNS-SAN on the
`CLIENT_CERT_ALLOWED_SUBJECTS` allowlist. **An empty allowlist disables
the mode** — turning TLS on never by itself opens a second way in.

### Pod-alias resolution (#5991)

A Guacamole connection is a database row: written once, read on every
click. A literal Deployment/DaemonSet pod name in that row goes stale at
the next rollout, so the seeded connection would render in the list and
then 404 forever — the exact half-working shape UAT row 115's vacuity
guard exists to reject.

With `POD_ALIAS_LABEL` set, the pod segment may therefore name a
**workload**: the proxy GETs the literal Pod name first (unchanged for
every existing caller), and only on NotFound lists Pods carrying
`<POD_ALIAS_LABEL>=<segment>`, preferring a Running Pod on its own node.
Zero matches is a hard 404 — there is no "pick something nearby" branch.
Unset (default) = literal names only, no apiserver read per request.

### Inside the WebSocket (channelled binary frames)

K8s exec channel protocol (v4): each frame's first byte = channel id.

| Channel | Direction | Purpose |
|---|---|---|
| 0 | client → proxy | stdin |
| 1 | proxy → client | stdout |
| 2 | proxy → client | stderr |
| 3 | proxy → client | error |
| 4 | client → proxy | resize (E2 follow-up) |

Browsers using xterm.js + the `@kubernetes/client-node` helpers
already speak this protocol; the proxy is byte-transparent.

### Outbound (proxy → kube-apiserver)

The proxy uses `k8s.io/client-go/tools/remotecommand.NewSPDYExecutor`
with the in-cluster ServiceAccount token. The apiserver speaks SPDY
(or v4 channelled WebSocket — the SDK negotiates); the SDK abstracts
this away behind `StreamWithContext(StreamOptions{Stdin, Stdout,
Stderr, Tty})`.

## tmux cascade

When `TMUX_CASCADE=true`, the requested exec command is wrapped in:

```
sh -c 'tmux attach -t catalyst-ops 2>/dev/null || tmux new -s catalyst-ops "<orig>"'
```

This is the bastion-shell pattern: every operator dropping into the
same node lands in the same tmux session — useful for SRE incident
pair-debugging. Default off; only enable on dedicated bastion nodes.

## Failure modes

| Failure | HTTP status | Action |
|---|---|---|
| Bad path | 404 | None |
| HMAC missing/malformed/expired/wrong | 401 | Logged WARN with reason |
| Client cert from an untrusted CA | TLS handshake failure | No handler runs |
| Client cert chain-valid, subject not allowlisted | 401 | Logged WARN with cn + dns |
| Pod segment resolves to no Running Pod | 404 | Logged WARN with the segment |
| Namespace not in AllowedNamespaces | 403 | Logged WARN |
| WS upgrade failed | upgrade writes its own error | Logged WARN |
| Apiserver dial failed | WS close 1011 (server-error) | Logged WARN, frame carries err |
| Browser closed | WS clean close | None |

## Configuration env-vars

| Var | Default | Purpose |
|---|---|---|
| `WS_PROXY_LISTEN_ADDR` | `:8080` | HTTP listen |
| `SHARED_SECRET_FILE` | (REQUIRED) | Path to file holding HMAC secret |
| `HMAC_SKEW_SECONDS` | `300` | Clock-skew tolerance (both directions) |
| `TMUX_CASCADE` | `false` | Wrap exec in shared tmux session |
| `ALLOWED_NAMESPACES` | (empty=all) | Comma-separated allowlist |
| `LOG_LEVEL` | `info` | debug/info/warn/error |
| `WS_PING_PERIOD` | `30s` | WebSocket keepalive |
| `WS_HANDSHAKE_TIMEOUT` | `10s` | HTTP→WS upgrade cap |
| `TLS_LISTEN_ADDR` | `:8443` | TLS bind; used only when the keypair is set |
| `TLS_CERT_FILE` | (empty) | Server cert PEM; pairs with `TLS_KEY_FILE` |
| `TLS_KEY_FILE` | (empty) | Server key PEM; pairs with `TLS_CERT_FILE` |
| `TLS_CLIENT_CA_FILE` | (empty) | CA bundle client certs must chain to |
| `CLIENT_CERT_ALLOWED_SUBJECTS` | (empty) | CN/DNS-SAN allowlist; **empty disables mTLS auth** |
| `POD_ALIAS_LABEL` | (empty) | Label key for workload-name resolution |
| `NODE_NAME` | (empty) | Downward API; node-locality preference only |

Startup fails (exit 2) rather than degrading quietly when: only one half
of the keypair is set; an allowlist is configured with no client CA; or
an allowlist is configured with no TLS listener. Each of those states
looks like working mTLS from the outside while verifying nothing.

## Tests

| Test | Tier |
|---|---|
| `internal/auth.*_test.go` | Unit — HMAC compute/verify/timing/skew; client-cert allowlist + the Authorizer policy |
| `internal/proxy.*_test.go` | Integration — upgrade + protocol echo via httptest; pod-alias resolution + the handler's call site |
| `internal/runtime/config_test.go` | Unit — the fail-fast startup rules |
| `main_test.go` | End-to-end over real TLS: the binary's own `newMux` + `buildTLSConfig`, with real CAs and real certificates |

`main_test.go` carries the CONTROL that keeps "auth accepted" meaningful:
an intruder certificate issued by the SAME CA, over the same handshake,
differing only in subject, must be denied 401 while the allowlisted one
reaches the namespace gate (403). A proxy that accepted every
chain-valid certificate would pass the positive test and fail that one.
A second control forces a certificate from an untrusted CA onto the wire
via `GetClientCertificate` — without that, Go's client silently
withholds the certificate against the server's advertised CA list and
the test would pass for the wrong reason.

`go test -count=1 -race ./...` is run in CI on every push touching
`core/cmd/k8s-ws-proxy/**`.
