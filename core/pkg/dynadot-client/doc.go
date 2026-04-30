// Package dynadot is the shared OpenOva client for the Dynadot api3.json
// REST endpoint. It is the single ground-truth implementation of the
// Dynadot HTTP transport, command builders, response decoding, and the
// safe read-modify-write semantics required for record management.
//
// The client is consumed by every Catalyst service that has to talk to
// api.dynadot.com:
//
//   - core/cmd/cert-manager-dynadot-webhook  (DNS-01 wildcard TLS)
//   - core/pool-domain-manager (NS-flip during BYO-domain provisioning)
//   - products/catalyst/bootstrap/api/cmd/catalyst-dns (Sovereign A-record set)
//
// Why a separate Go module under core/pkg/:
//
//   - The legacy clients live under each consumer's `internal/` tree, so
//     they cannot be imported across module boundaries (Go's internal-
//     package rule). Hosting the canonical client at core/pkg/ makes it
//     visible to every service module without breaking the convention
//     that service-private code stays in `internal/`.
//   - A standalone module keeps the dependency surface tiny: this package
//     uses only the standard library, so the consumers don't transitively
//     pick up Postgres / chi / etc. when all they need is a single API
//     call.
//   - Per docs/INVIOLABLE-PRINCIPLES.md #3 (one canonical implementation
//     per concern), all future Dynadot work should land here. The legacy
//     copies under products/catalyst/bootstrap/api/internal/dynadot and
//     core/pool-domain-manager/internal/registrar/dynadot remain in
//     place at the time of writing — they will be migrated to this
//     package in a follow-up. Do not extend them; extend this package.
//
// Safety contract — the package enforces the operator-memory rule that
// `set_dns2` calls without `add_dns_to_current_setting=yes` wipe the
// entire zone. Every record-mutating helper in this package either:
//
//   1. Uses `add_dns_to_current_setting=yes` (append-only path), or
//   2. Performs a read-modify-write against `domain_info` and writes the
//      reconstructed full record set so no record is ever silently
//      dropped.
//
// The caller cannot accidentally invoke the destructive variant — that
// command builder is unexported.
package dynadot
