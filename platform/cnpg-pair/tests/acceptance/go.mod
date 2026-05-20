// d31-acceptance — Pillar 3 zero-tx-loss harness.
//
// Self-contained Go module under platform/cnpg-pair/tests/acceptance/
// (NOT joined to core/controllers/go.mod because the harness exists
// outside the controller-runtime estate and ships as its own image).
// Per CLAUDE.md §0 Pillar 3 deterministic step 10: 1M-row writes
// against the primary CNPG cluster → region-kill via scaling the
// primary Cluster CR instances=0 → assert the replica promotes
// within RTO=30s → reconnect and verify counter continuity (zero
// gaps == zero transactions lost).
//
// Dependency-discipline: stdlib only (os/exec to drive `psql` and
// `kubectl`). No pgx vendor pull, no client-go vendor pull — keeps
// the image tiny and the build hermetic. The operator-facing image
// already carries both binaries (postgresql-client + kubectl).
module github.com/openova-io/openova/platform/cnpg-pair/tests/acceptance

go 1.23
