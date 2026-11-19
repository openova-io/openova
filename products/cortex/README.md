# Cortex: knowledge search

The AI hub's document search for one Organization: uploaded documents are split into passages, embedded through the platform's model gateway, and found again by meaning, each search trimmed to what the caller may read.

**Status: store and query process.** No chart, no Blueprint and no running service ship yet. `docs/STATUS.md` is the inventory of what is built.

## Role in Catalyst

An Application Blueprint, `bp-cortex`, not published yet, written fresh in Go on platform services: the PostgreSQL capability with the `vector` extension holds documents, passages and grants, and every model is reached through the model gateway. The decision record is [ADR-0034](../../docs/adr/0034-later-products-ai-hub-and-open-banking.md).

## What is here

- `internal/store/`: migration `0001`, the two database roles and the `Store` port. `cortex_query` (the query process) reads and queues jobs; `cortex_index` (the index process) writes the rest. Row-level security is forced on `chunk` and `document`: a session reads only rows whose readers meet the principals it set in `cortex.principals`, and a session that set none fails.
- `cmd/cortex`: `cortex query`, the query process. It serves `POST /v1/search` and `GET /v1/sources` behind the check of a token of the Organization's realm (`internal/auth`; signature, issuer, audience `cortex`, expiry), and `/healthz`, `/readyz` and `/metrics` on a second listener that no route publishes. A search is embedded and reranked through the model gateway (`internal/gateway`), with the virtual key read from its mounted file; the text of a search is never logged.
- `internal/definitions`: the loader that watches the definitions ConfigMap through an informer and applies its `sources` to the store; the index process will run it.
- Each passage row carries its readers, the principals of its source's grants or of the document's own reader list plus the source's managers. The search binds the caller's principals as well, so a narrow reader gets an exact scan of its own rows (spikes PRD-060 and PRD-061, under `docs/ledger/plan/spikes/`).

## Configuration

The vector length is `models.embed.dimensions` (64 to 4096); it is written into migration `0001`, and the store refuses to open on a database of another length. The store needs `vector` 0.8.0 or later. Migrations run as the database owner; the first run creates the two roles, so the owner needs `CREATEROLE` then, unless both roles exist and it may grant them.

## Operations

- Run the unit tests with `cd products/cortex && go test ./...`. With `CORTEX_TEST_DATABASE_URL` (a superuser address of a PostgreSQL with `vector`) the store's tests run against it; `.github/workflows/cortex-ci.yaml` runs them on the platform's PostgreSQL image and fails on a skipped test.
- A grant change rewrites the readers of every passage it covers, in the transaction that changes the grant: run it from the index process, never from a request. Such updates are not HOT; vacuum the `chunk` table after a large one.
- Measured limit: a search stays under 200 ms for readers of up to about 20,000 passages in one store (2 % of a million on four CPUs). Broader readers are slower; the numbers are in spike PRD-061.
