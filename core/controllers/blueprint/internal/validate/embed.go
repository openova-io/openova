// Package validate — embedded JSON Schemas for runtime topology gating.
//
// G117.1 AC2 (#2740): the blueprint-controller must invoke the canonical
// blueprint-topology JSON Schema at runtime and surface findings on
// `Blueprint.status.conditions[]` as `topology-schema-violation` codes.
//
// The canonical source of the schema is
// `platform/_schemas/blueprint-topology.json`. Because that path lives
// OUTSIDE this Go module (the controller's `go.mod` is at
// `core/controllers/`, the schemas tree at `platform/_schemas/` is two
// levels above the module root), `//go:embed` cannot reach it directly.
//
// We vendor a snapshot into `schemas/blueprint-topology.json` co-located
// with this package. A CI guard (the
// `.github/workflows/blueprint-admission-validate.yaml` workflow + a
// `scripts/check-schema-vendor-drift.sh` runner) diff-checks the two
// files on every PR — drift fails the build.
//
// If you're updating the canonical schema, run
//
//	cp platform/_schemas/blueprint-topology.json \
//	   core/controllers/blueprint/internal/validate/schemas/blueprint-topology.json
//
// in the same PR.
package validate

import _ "embed"

//go:embed schemas/blueprint-topology.json
var blueprintTopologySchemaJSON []byte
