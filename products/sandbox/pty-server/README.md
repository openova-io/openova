# pty-server

Per-Sandbox PTY broker. Spawns the agent binary (`claude`, `cursor-agent`,
`qwen-code`, `aider`, `opencode`) in a fresh PTY and fans its raw ANSI
output to N concurrent WebSocket clients (browser xterm.js, mobile card
view). One persistent process, many short-lived viewers — see
`products/sandbox/docs/architecture.md` §1 and §2.

> This is intentionally **not tmux**. We keep tmux's behaviour model
> (one PTY, multiple clients, persistent across disconnect) and skip its
> TUI, prefix-key, and window-manager baggage.

## Surface

```
POST   /sessions                 spawn:  body {command, env?, cwd?, rows?, cols?}
GET    /sessions                 list:   {sessions:[id, ...]}
GET    /sessions/{id}            describe
WS     /sessions/{id}/attach     bidi raw bytes (WS <-> PTY)
WS     /sessions/{id}/cards      JSON card stream (mobile)
POST   /sessions/{id}/resize     body {rows, cols}  -> SIGWINCH
POST   /sessions/{id}/signal     body {signal}  one of INT|QUIT|TERM|HUP
DELETE /sessions/{id}            graceful SIGTERM, then SIGKILL after 5s
GET    /healthz                  liveness
```

On `attach` connect the server replays the last 256 KiB of PTY output
(one binary frame) before joining the live fan-out. Slow consumers do
not stall the PTY read loop — bytes are dropped for that subscriber
only.

## Run locally

```bash
cd products/sandbox/pty-server
go build ./...
PTY_SERVER_ADDR=:7681 go run ./cmd/pty-server
```

Smoke:

```bash
curl -sX POST localhost:7681/sessions \
  -H 'content-type: application/json' \
  -d '{"command":["bash","-l"],"rows":40,"cols":120}'
# {"id":"abc...","createdAt":"..."}
```

## Container

```bash
docker build -t ghcr.io/openova-io/openova/sandbox-pty-server:dev .
docker run --rm -p 7681:7681 ghcr.io/openova-io/openova/sandbox-pty-server:dev
```

## Not yet wired

- Auth: today the surface is open; the production deploy is fronted by
  the Org's gateway with the Sandbox JWT enforced. The TODO is an
  in-process middleware that verifies the JWT and binds `org_id` /
  `sandbox_id` claims to every session record.
- JetStream emit on session lifecycle events. The contract is
  `catalyst.sandbox.session.{created,exited}` (ADR-0001 §6).
