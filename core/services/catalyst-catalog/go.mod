// Module path: github.com/openova-io/openova/core/services/catalyst-catalog
//
// catalyst-catalog is the Sovereign-wide Blueprint catalog HTTP REST
// service shipped by EPIC-2 Slice L (#1097). It REPLACES the per-Org
// per-Org `catalog` service (different scope: it was Org-bound; this is
// Sovereign-wide multi-source) per ADR-0001 §4.3.
//
// SEAM DECISION: catalyst-catalog is a SERVICE not a CONTROLLER, so it
// lives outside `core/controllers/go.mod`. Group `core/services/` is for
// HTTP services; group `core/controllers/` is for CRD reconcilers.
//
// The unified Gitea client (CC2 #1136) lives at
// `core/controllers/pkg/gitea` (promoted from `internal/gitea` by EPIC-2
// Slice L, #1097 — Go's `internal/` rule blocks cross-module imports).
// We import it via a `replace` directive so a single canonical client
// surface is maintained across controllers + services.
module github.com/openova-io/openova/core/services/catalyst-catalog

go 1.23

require (
	github.com/openova-io/openova/core/controllers v0.0.0-00010101000000-000000000000
	sigs.k8s.io/yaml v1.4.0
)

replace github.com/openova-io/openova/core/controllers => ../../controllers
