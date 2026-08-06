// #5616 — the create-instance contract must refuse a placement tier
// this Sovereign cannot resolve.
//
// WHAT WENT WRONG (hw292, funnel Organization `uatco`, 2026-08-04):
//
//	POST /catalyst/v1/apps/instances {"placement":{"vcluster":"rtz"}} → 201
//	Application uatco/uatco-agenity  phase=Degraded
//	  Ready=False reason=GiteaError
//	  upsert per-cluster HelmRelease rtz/uatco-agenity-rtz-a:
//	    namespaces "rtz" not found
//
// `placement.vcluster` is a TIER KEY that the application-controller maps
// to a HOST NAMESPACE (VClusterPlacements, default namespace == tier
// name). `clusters/_template/bootstrap-kit/` installs no mgmt / dmz / rtz
// vCluster, so three of the four values this endpoint accepted resolved
// to a namespace that exists on no Sovereign — a 201 followed by a
// permanently broken install, with a raw Kubernetes error as the only
// explanation the operator ever sees.
//
// WHY A FRONT-END FIX IS NOT ENOUGH: PR #5622 stopped OFFERING the dead
// options in products/catalyst/bootstrap/ui. The other doors into this
// same endpoint — a direct API call, a Git edit of the Application CR,
// the MCP `create_application` tool, the products/catalyst/console tree —
// never consulted that dropdown. The contract is the only place that
// covers all of them.
//
// HOW THESE TESTS FAIL IF THE DEFECT COMES BACK: TestGuard_… asserts on
// the ShapeError the production validator returns for an uninstalled
// tier. Before the fix ValidateShape returned nil for "rtz" and the guard
// goes red on `want placement-vcluster-unavailable, got <nil>`. The
// controls below stay green on BOTH trees, so a green run is not the
// gate being switched off.

package instances

import (
	"encoding/json"
	"strings"
	"testing"
)

// defaultSovereign resets the available-tier set to what every Sovereign
// this repo provisions actually has: host only. Mirrors an unset
// CATALYST_PLACEMENT_VCLUSTER_TIERS.
func defaultSovereign(t *testing.T) {
	t.Helper()
	SetAvailableVClusterTiers("")
	t.Cleanup(func() { SetAvailableVClusterTiers("") })
}

// ── THE GUARD — red on the pre-fix tree ──────────────────────────────
//
// Pre-fix output (origin/main @ 0b6174597):
//
//	--- FAIL: TestGuard_UninstalledTierIsRefused_5616/rtz
//	    want ShapeError code "placement-vcluster-unavailable", got <nil>
//	    (accepted → 201, then Degraded: namespaces "rtz" not found)
func TestGuard_UninstalledTierIsRefused_5616(t *testing.T) {
	for _, tier := range []string{"mgmt", "dmz", "rtz"} {
		t.Run(tier, func(t *testing.T) {
			defaultSovereign(t)
			r := validReq()
			r.Placement = &InstancePlacementRequest{VCluster: tier}
			err := r.ValidateShape()
			if err == nil {
				t.Fatalf("vcluster %q was ACCEPTED on a Sovereign that installs no %s vCluster — "+
					"this is #5616: the create answers 201 and the Application then Degrades on "+
					"`namespaces %q not found`", tier, tier, tier)
			}
			if err.Code != "placement-vcluster-unavailable" {
				t.Fatalf("want ShapeError code %q, got %q (%s)",
					"placement-vcluster-unavailable", err.Code, err.Message)
			}
			// The message must tell the operator what to do instead —
			// the whole complaint in #5616 was a Kubernetes-internal
			// error with no remedy in it.
			if !strings.Contains(err.Message, "host") {
				t.Errorf("refusal message must name the working alternative, got: %s", err.Message)
			}
		})
	}
}

// TestGuard_UninstalledTierRefusedThroughBuild_5616 exercises the path
// PRODUCTION takes: HandleCreateInstance calls ValidateShape and then
// Build, and Build re-validates before minting a seed. A tier the
// Sovereign lacks must never reach an ApplicationSeed — that seed is
// what becomes the Application CR.
func TestGuard_UninstalledTierRefusedThroughBuild_5616(t *testing.T) {
	defaultSovereign(t)
	r := validReq()
	r.Placement = &InstancePlacementRequest{VCluster: "rtz"}
	seed, err := r.Build("singleton")
	if err == nil {
		t.Fatalf("Build minted a seed for an uninstalled tier: %+v", seed.Placement)
	}
	var se *ShapeError
	if !asShapeError(err, &se) || se.Code != "placement-vcluster-unavailable" {
		t.Fatalf("want placement-vcluster-unavailable from Build, got %v", err)
	}
}

// TestGuard_RefusalIsWireSerialisable_5616 pins the VALUE that reaches
// the client, not merely the Go struct: HandleCreateInstance writes
// {"code":…,"message":…} with HTTP 400, and clients branch on `code`.
func TestGuard_RefusalIsWireSerialisable_5616(t *testing.T) {
	defaultSovereign(t)
	r := validReq()
	r.Placement = &InstancePlacementRequest{VCluster: "mgmt"}
	err := r.ValidateShape()
	if err == nil {
		t.Fatal("mgmt accepted on a host-only Sovereign — #5616 regression")
	}
	body, merr := json.Marshal(map[string]string{"code": err.Code, "message": err.Message})
	if merr != nil {
		t.Fatalf("marshal: %v", merr)
	}
	var got map[string]string
	if uerr := json.Unmarshal(body, &got); uerr != nil {
		t.Fatalf("unmarshal: %v", uerr)
	}
	if got["code"] != "placement-vcluster-unavailable" {
		t.Fatalf("wire code = %q, want placement-vcluster-unavailable (body=%s)", got["code"], body)
	}
}

// ── CONTROLS — green on BOTH the pre-fix and post-fix trees ──────────

// Control 1. The two placements that always work must keep working.
// If the new gate were a blanket reject, this goes red — which is
// exactly the failure mode a "refuse the tier" fix invites.
func TestControl_BlankAndHostAlwaysAccepted_5616(t *testing.T) {
	for _, tier := range []string{"", "host"} {
		defaultSovereign(t)
		r := validReq()
		r.Placement = &InstancePlacementRequest{VCluster: tier}
		if err := r.ValidateShape(); err != nil {
			t.Errorf("vcluster %q must always validate (blank = inherit the Blueprint "+
				"default, host = the Organization's own namespace), got %v", tier, err)
		}
	}
}

// Control 2. A value outside the VOCABULARY is still the pre-existing
// malformed-request error, not the new availability error. Proves the
// #5616 gate was added ALONGSIDE the shape check rather than replacing
// it — a swap would silently downgrade a typo to "unavailable".
func TestControl_UnknownTierStillShapeInvalid_5616(t *testing.T) {
	defaultSovereign(t)
	r := validReq()
	r.Placement = &InstancePlacementRequest{VCluster: "warp-zone"}
	err := r.ValidateShape()
	if err == nil || err.Code != "placement-vcluster-invalid" {
		t.Fatalf("want placement-vcluster-invalid for a value outside the vocabulary, got %v", err)
	}
}

// Control 3. Placement omitted entirely — the silent-accept default flow
// the overwhelming majority of installs take, and the one the hw292
// control Applications (uatco-openclaw, uatco-mail) actually used.
func TestControl_NilPlacementUnaffected_5616(t *testing.T) {
	defaultSovereign(t)
	r := validReq()
	if err := r.ValidateShape(); err != nil {
		t.Fatalf("nil placement must stay valid, got %v", err)
	}
}

// ── VACUITY CHECK — the gate must be able to say YES ─────────────────
//
// A guard that refuses everything would pass every test above. This
// drives the knob an operator turns after installing the tier's vCluster
// and asserts the SAME request now succeeds — and that a sibling tier
// they did NOT install stays refused, so the knob is per-tier and not a
// master off-switch.
func TestVacuity_InstalledTierAcceptedSiblingStillRefused_5616(t *testing.T) {
	SetAvailableVClusterTiers("mgmt")
	t.Cleanup(func() { SetAvailableVClusterTiers("") })

	ok := validReq()
	ok.Placement = &InstancePlacementRequest{VCluster: "mgmt"}
	if err := ok.ValidateShape(); err != nil {
		t.Fatalf("mgmt must be accepted once the operator declares it installed, got %v", err)
	}

	nope := validReq()
	nope.Placement = &InstancePlacementRequest{VCluster: "dmz"}
	err := nope.ValidateShape()
	if err == nil || err.Code != "placement-vcluster-unavailable" {
		t.Fatalf("declaring mgmt must NOT also enable dmz, got %v", err)
	}
}

// TestVacuity_TierListParsingIgnoresJunk_5616 — an operator typo must
// never widen the accepted set. `parseAvailableTiers` keeps only values
// that are in the vocabulary.
func TestVacuity_TierListParsingIgnoresJunk_5616(t *testing.T) {
	SetAvailableVClusterTiers(" RTZ , warp-zone ,, host ")
	t.Cleanup(func() { SetAvailableVClusterTiers("") })

	good := validReq()
	good.Placement = &InstancePlacementRequest{VCluster: "rtz"}
	if err := good.ValidateShape(); err != nil {
		t.Errorf("rtz declared (case/space-insensitively) must be accepted, got %v", err)
	}
	bad := validReq()
	bad.Placement = &InstancePlacementRequest{VCluster: "warp-zone"}
	if err := bad.ValidateShape(); err == nil || err.Code != "placement-vcluster-invalid" {
		t.Errorf("a junk entry in %s must not become an accepted tier, got %v", TierEnvVar, err)
	}
}

// asShapeError is a tiny errors.As stand-in kept local so the guard has
// no dependency beyond the package under test.
func asShapeError(err error, out **ShapeError) bool {
	se, ok := err.(*ShapeError)
	if ok {
		*out = se
	}
	return ok
}
