# openova-flow-server

Stateless HTTP+SSE event router for OpenovaFlow. Holds an in-memory ring
buffer of `FlowMessage` envelopes per `flowId` and replays them to SSE
subscribers. Emitters POST envelopes; consumers GET the snapshot or
subscribe to the stream.

This is Agent #2's Go backend; Agent #1's TypeScript `@openova/flow-core`
+ `@openova/flow-canvas` packages are the canvas consumer.

## Wire contract

All endpoints use the locked `FlowMessage` JSON shape (see
`internal/types/flow.go` for the Go definition + the brief for the
locked schema). Schema version 1.

| Method | Path                              | Purpose |
|--------|-----------------------------------|---------|
| POST   | `/v1/flows/{flowId}/events`       | Ingest one FlowMessage. 200 on accept, 400 on schema violation. |
| GET    | `/v1/flows/{flowId}/snapshot`     | Current folded state. 404 when the flow has never been ingested. |
| GET    | `/v1/flows/{flowId}/stream`       | SSE: synthetic snapshot frame, then live tail. Heartbeats every 15s. |
| DELETE | `/v1/flows/{flowId}`              | Purge a flow's state. Idempotent, returns 204. |
| GET    | `/healthz`                        | Liveness. |
| GET    | `/readyz`                         | Readiness. |

### SSE format

```
event: snapshot
id: 7
data: {"type":"snapshot",...}

event: upsert-nodes
id: 8
data: {"type":"upsert-nodes",...}

event: heartbeat
data: {}
```

The `id:` line carries the server-assigned monotonic sequence number,
so the EventSource client's `Last-Event-ID` resume header works
out-of-the-box on a future slice (we replay the buffer from
LastEventID+1 on reconnect — see backlog item).

## Behavior rules

- **Ring buffer per flow.** Default 4096 envelopes (env
  `FLOW_SERVER_RING_CAPACITY`). FIFO drop on overflow. Snapshot folds
  the buffer to current state on demand.
- **Concurrency.** Lock per flow, not global. Two flows mutate in
  parallel.
- **SSE backpressure.** 16-slot channel per client. Slowest client
  drops oldest events (mirrors catalyst-api k8scache fanout).
- **Validation.** Envelope `type` discriminates the variant; unknown
  variants reject with 400. Missing required fields (e.g. `snapshot`
  without a `flow`) reject with 400.
- **Storage.** None. State is in-memory, lost on restart. Replay
  relies on emitters re-emitting `snapshot` on reconnect.

## Env

| Name                         | Default     | Purpose |
|------------------------------|-------------|---------|
| `FLOW_SERVER_LISTEN_ADDR`    | `:8080`     | Listen address. |
| `FLOW_SERVER_RING_CAPACITY`  | `4096`      | Per-flow ring buffer size. |

Per `docs/PRINCIPLES.md` #4 every parameter is env-driven.

## Build

```bash
cd products/openova-flow/server
go build ./...
go test ./...
```

CI image: `harbor.openova.io/proxy-ghcr/openova-io/openova/openova-flow-server:<sha>`
per the MIRROR-EVERYTHING rule. Never built/pushed from a workstation.

## Tests

- `test/contract_test.go` — every FlowMessage variant round-trips JSON
  unchanged + every unknown / malformed variant rejects.
- `test/server_test.go` — full HTTP surface (health, ingest, snapshot,
  delete, SSE replay+tail).
