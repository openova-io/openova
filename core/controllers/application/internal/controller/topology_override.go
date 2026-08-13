package controller

import (
	"strings"

	"github.com/openova-io/openova/core/controllers/internal/placement"
	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

// topologyOverrideFromPlacement answers ONE question for the legacy
// (no-`targets[]`) fan-out branch of Reconcile: which BCP posture did the
// OPERATOR actually choose for this Application?
//
// # The defect this replaces (UAT row 60, Refs #3375)
//
// That branch used to read `spec.Topology`, parsed by parseSpec as
//
//	unstructured.NestedString(app.Object, "spec", "topology")
//
// but `spec.topology` is declared in the Application CRD as an OBJECT —
// `products/catalyst/chart/crds/application.yaml:221` opens
// `topology: {type: object}` carrying autoFailover / rto / rpo /
// minReplicas. A string read of an object field returns "" (NestedString
// reports a type error the call site discarded), and the apiserver would
// reject a string there anyway. So `spec.Topology` was not merely unset in
// practice — it was STRUCTURALLY incapable of being non-empty, which made
// ResolveTopology's "operator override wins" branch
// (internal/render/topology.go:137) dead code on this path. Every
// Application resolved the Blueprint's region-count default instead
// (topology.go:150-153).
//
// The operator's choice does exist on the CR: since #3373 `spec.placement`
// is dual-form and its `mode` lands in `spec.Placement` (parseSpec
// application_controller.go:3426). The create-instance door writes exactly
// that — `newApplicationCRFromSeed` stamps
// `spec.placement.mode = canonicalizeTopology(seed.Topology)`
// (endpoint_handler.go:2339) and never writes `spec.topology`. The
// bootstrap-adoption producer already resolves off the right field
// (`reconcileBootstrapContinuum`, application_controller.go:3161); this
// helper closes the divergence for the main path.
//
// # Why row 60 turned red on it
//
// `placement.Resolve(spec.Placement, spec.Regions)` — the per-region
// primary/standby roles the Topology tab paints — always read the operator's
// posture, while the (choice, variant) pair feeding the per-app Continuum
// producer (`buildContinuumPlan`, continuum.go:141) read the Blueprint
// default. When those two disagree the Application renders a 2-region pair
// whose Switchover is never armed: buildContinuumPlan's first gate is
// `choice != BcpActiveHotStandby && choice != BcpActivePassive`, so a
// Blueprint defaulting to `singleton` on a single-region Sovereign silently
// drops the DR contract for an app the operator explicitly placed
// hot-standby across two regions.
//
// # The contract
//
// Returns the Blueprint's OWN spelling of the operator's posture when the
// Blueprint declares it in `spec.topology.supported[]`, else "" — which
// hands ResolveTopology the pre-existing region-count default.
//
//   - The Blueprint's own spelling (not the canonical token) is returned so
//     both `isSupported` and `lookupVariant` match: a Blueprint that still
//     declares the legacy `active-hotstandby` keeps resolving its own
//     perTopology entry. Comparison is canonical on BOTH sides via
//     placement.Canonicalize, so spelling never decides support.
//   - An unsupported posture falls back rather than failing. `spec.Placement`
//     is validated upstream against `placementSchema.modes[]`
//     (application_controller.go:765) — a DIFFERENT list from
//     `topology.supported[]`. Failing here would newly break Applications
//     whose Blueprint declares the two inconsistently, which is a Blueprint-
//     authoring problem and not this row's. Falling back preserves exactly
//     today's behaviour for that case and changes behaviour ONLY where the
//     Blueprint can honour what the operator picked.
func topologyOverrideFromPlacement(mode string, bpTopo *bpv1alpha1.Topology) string {
	if bpTopo == nil {
		return ""
	}
	canon := placement.Canonicalize(strings.TrimSpace(mode))
	if canon == "" {
		return ""
	}
	for _, s := range bpTopo.Supported {
		if placement.Canonicalize(string(s)) == canon {
			return string(s)
		}
	}
	return ""
}
