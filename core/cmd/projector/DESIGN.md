# projector — design

EPIC-4 P1 — `core/cmd/projector/` consumes K8s resource Events from
NATS JetStream `catalyst.events` and projects them into Valkey under
the canonical key shape:

```
cluster:{cluster-id}:kind:{kind}:{namespace}/{name}
```

(Cluster-scoped resources use the empty namespace token, producing
`cluster:{c}:kind:{k}:/{name}`.)

## Why this exists

`catalyst-api` scales horizontally. Without an external KV, every
SSE consumer per replica needs its own copy of every cluster's
state. Valkey collapses that to one shared store; future SSE
endpoints in catalyst-api read directly from these keys.

## Cold-start

On boot, the projector performs a full LIST against the in-cluster
apiserver for every kind in `lister.DefaultKinds`, projecting each
result. Then it hooks up the JetStream consumer and catches up on
the 24h replay window. Any overlap is idempotent (same key, same or
newer body — last-write-wins).

This pairs with the JetStream's finite retention: cold-start covers
state older than the retention window; consumer catch-up covers
everything inside it.

## Configuration env-vars

| Var | Default | Purpose |
|---|---|---|
| `NATS_URL` | (empty → nats default) | NATS broker URL |
| `NATS_STREAM` | `catalyst.events` | JetStream Stream name |
| `NATS_SUBJECT` | `catalyst.events.>` | Subject filter |
| `NATS_DURABLE` | `catalyst-projector-${HOSTNAME}` | Durable consumer name |
| `NATS_ACK_WAIT` | `30s` | Time before redelivery on missing ack |
| `NATS_MAX_DELIVER` | `5` | Bound on retries before message → DLQ |
| `NATS_BACKOFF_MIN` | `1s` | Initial nack backoff |
| `NATS_BACKOFF_MAX` | `30s` | Capped backoff |
| `VALKEY_ADDR` | (REQUIRED) | host:port of Valkey |
| `VALKEY_USERNAME` | (empty) | Valkey ACL username |
| `VALKEY_PASSWORD_FILE` | (empty) | Path to file with Valkey password |
| `VALKEY_TTL` | `24h` | Per-key TTL |
| `CLUSTER_ID` | (REQUIRED) | Sovereign cluster id used in keys |
| `COLD_START` | `true` | Run cold-start on boot |
| `LOG_LEVEL` | `info` | debug/info/warn/error |

## Wire contract

The NATS message body MUST be a JSON object matching
`internal/valkey.Event`. Mirrors the `Event` shape in
`products/catalyst/bootstrap/api/internal/k8scache/factory.go` so the
producer (catalyst-api) and consumer (this projector) share one wire
format:

```jsonc
{
  "cluster": "omantel",
  "kind":    "pod",
  "type":    "ADDED" | "MODIFIED" | "DELETED",
  "object":  { "apiVersion":"v1", "kind":"Pod", "metadata": {...}, "spec":{...}, "status":{...} },
  "at":      "2026-01-01T00:00:00Z"
}
```

The projector does NOT re-marshal `object` — it stores the raw
message bytes verbatim under the computed key. This guarantees
zero-drift between what the publisher wrote and what SSE consumers
read; sparse / unknown CRD fields survive intact.

## Failure modes + idempotency

| Failure | Handling |
|---|---|
| KV write fails | Nack — JetStream redelivers after `NATS_BACKOFF_MIN` |
| Message JSON malformed | Term — no point retrying a permanent decode error |
| KV connection drops | Nack — projector's reconnect logic kicks in |
| Multiple replicas | Safe — JetStream queue-group on shared `Durable` name; writes are idempotent (LWW on namespacedName) |

## Tests

| Test | Scope |
|---|---|
| `internal/valkey/projector_test.go` | Apply / Key shape / DELETE / validation |
| `internal/nats/consumer_test.go` | handleOne — Ack / Nak / Term routing |
| `internal/lister/coldstart_test.go` | Full LIST → project, error continuation |

`go test -count=1 -race ./...` is run in CI on every push touching
`core/cmd/projector/**`.
