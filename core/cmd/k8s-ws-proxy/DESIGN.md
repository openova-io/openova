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

## Tests

| Test | Tier |
|---|---|
| `internal/auth.*_test.go` | Unit — HMAC compute/verify/timing/skew |
| `internal/proxy.*_test.go` | Integration — upgrade + protocol echo via httptest |

`go test -count=1 -race ./...` is run in CI on every push touching
`core/cmd/k8s-ws-proxy/**`.
