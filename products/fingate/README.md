# Fingate: open banking

The open-banking product of Catalyst: an API that third parties call to read accounts and make payments for a bank's customers, under a public open-banking standard.

**Status: specification kit only.** No chart, no Blueprint and no running service ship yet. `docs/STATUS.md` is the inventory of what is built.

## Role in Catalyst

An Application Blueprint, `bp-fingate`, not published yet. It is installed in an Organization of its own, and that Organization's realm is the open-banking realm. It is written fresh in Go on platform services: Keycloak authorises customers, OpenBao signs and holds credentials, the PostgreSQL capability stores consents and payments. Core banking is reached only through a typed port that an adapter serves. The decision record is [ADR-0034](../../docs/adr/0034-later-products-ai-hub-and-open-banking.md).

## What is here

- `profiles/rw-4.0.0/`: the first standards profile. `profile.yaml` names the six OpenAPI documents of the Read/Write API 4.0.0, their operation counts, the path prefixes that are not offered, the security rules and the consent status codes. The documents are the publisher's files, unchanged; `SOURCE.md` says where they come from and gives their checksums.
- `internal/profile/`: the Go package that loads a profile, checks each document against its checksum and counts its operations.

## Configuration

The standard is data, not code. A profile is one folder under `profiles/`, and its folder name is its identifier. One profile will be active per Application; no Blueprint parameter selects it yet.

## Operations

Run the tests with `cd products/fingate && go test ./...`. A vendored document is never edited: a newer release of the standard is a new profile folder with its own `SOURCE.md`.
