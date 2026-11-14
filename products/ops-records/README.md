# ops-records

The operations-records service: the records of signals, incidents, their timelines, problems,
judgements and feedback of each Organization, and the outbox of the events it publishes.

Role in Catalyst: an Application Blueprint of the agent plane. It is written in Go, as its own
module. Its place in the model is described in [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md).

State today: the module holds the identifiers (`internal/ids`), the health routes
(`internal/health`), the store interface with an in-memory store and a PostgreSQL store
(`internal/store`), migration 0001 of the schema, the contract test every store must pass,
and the program `cmd/ops-records`. Nothing installs the service yet: it has no chart, no
Blueprint file and no bootstrap-kit slot.

Records: one database schema, `ops`, with the tables `signals`, `incidents`, `incident_events`,
`problems`, `judgements`, `feedback` and `outbox`. Every row carries its Organization, and a
read for one Organization sees only that Organization's rows. The rules every store keeps are
written at the top of `internal/store/store.go`.

Migrations: `internal/store/migrations/<NNNN>_<name>.up.sql` and `.down.sql`, built into the
program. The program applies the pending ones when it starts, each in its own transaction under
an advisory lock, and records them in `ops.schema_migrations`. It refuses a database whose
version it does not know. A released migration is never changed; a change is a new version.

Program: `ops-records` (or `ops-records serve`) applies the migrations and serves `GET /healthz`
and `GET /readyz` (ready when the database answers); `ops-records migrate` applies them and
exits; `ops-records version` prints the version. Configuration:

- `OPS_RECORDS_DATABASE_URL` (required): the database, as a `postgres://` URL with no password.
- `OPS_RECORDS_DATABASE_CREDENTIALS_DIR` (required): a directory with the files `username` and
  `password`, mounted from OpenBao. They are read for every new connection, so a rotated
  credential is used without a restart. A password anywhere else is refused.
- `OPS_RECORDS_LISTEN`: the address of the health routes, default `:8080`.

Tests: `go vet ./... && go test ./...` in this folder. The PostgreSQL tests need a database
named by `OPS_RECORDS_TEST_DSN` (for example
`postgres://ops:ops@localhost:5432/ops_test?sslmode=disable`) and are skipped without it; they
drop and create schema `ops` there. The job `ops-records` of
`.github/workflows/test-ungated-trees.yaml` runs every test on each pull request that changes
this folder, with a PostgreSQL service, and fails when a test is skipped.
