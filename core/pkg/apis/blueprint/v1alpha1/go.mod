// G117 Blueprint topology types — canonical home per Wave-0 brief.
//
// Self-contained module so `go build ./...` from this directory
// succeeds without dragging in the heavier controllers/ go.mod.
// Wave-1 may fold this into a larger `core/pkg/apis` umbrella module
// once additional CRD types (Application, Organization, Environment)
// are also typed.
//
// The mirror at core/controllers/pkg/apis/blueprint/v1alpha1/ exists
// solely so existing controllers (which already vendor controller-runtime)
// can import without a module-level go.work entry.
module github.com/openova-io/openova/core/pkg/apis/blueprint/v1alpha1

go 1.23
