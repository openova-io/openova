// G117 W2.C2 Application types — canonical home per the W2.C2 brief.
//
// Self-contained module so `go build ./...` from this directory
// succeeds without dragging in the heavier controllers/ go.mod.
// Wave-1 may fold this into a larger `core/pkg/apis` umbrella module
// once additional CRD types (Organization, Environment) are also typed.
//
// The mirror at core/controllers/pkg/apis/application/v1alpha1/ exists
// solely so existing controllers (which already vendor controller-runtime)
// can import without a module-level go.work entry — same precedent as
// the Blueprint typed package at core/pkg/apis/blueprint/v1alpha1.
module github.com/openova-io/openova/core/pkg/apis/application/v1alpha1

go 1.23
