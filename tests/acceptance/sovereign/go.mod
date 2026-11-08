// sovereign-acceptance — ADR-0015 D9 live acceptance suite.
//
// Self-contained module (same discipline as
// platform/cnpg-pair/tests/acceptance): stdlib only. The Sovereign API is
// driven over net/http exactly like the console; cluster-side assertions
// shell out to `kubectl` (read-only verbs + SELECT-only psql via exec).
module github.com/openova-io/openova/tests/acceptance/sovereign

go 1.23
