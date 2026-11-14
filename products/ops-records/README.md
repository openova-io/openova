# ops-records

The operations-records service: the records of signals, incidents, their timelines, problems,
judgements and feedback of each Organization, and the outbox of the events it publishes.

Role in Catalyst: an Application Blueprint of the agent plane. It is written in Go, as its own
module. Its place in the model is described in [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md).

State today: the module holds the identifiers (`internal/ids`), the health routes
(`internal/health`), the store interface with an in-memory store (`internal/store`) and the
contract test every store must pass. Nothing installs the service yet: it has no program, no
chart, no Blueprint file and no bootstrap-kit slot.

Records: one database schema, `ops`, with the tables `signals`, `incidents`, `incident_events`,
`problems`, `judgements`, `feedback` and `outbox`. Every row carries its Organization, and a
read for one Organization sees only that Organization's rows. The rules every store keeps are
written at the top of `internal/store/store.go`.

Tests: `go vet ./... && go test ./...` in this folder. The workflow
`.github/workflows/test-ungated-trees.yaml` runs them on every pull request that changes it.
