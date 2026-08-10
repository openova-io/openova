// applications_install_version_contract_test.go — the install-body
// DEFAULTING CONTRACT that products/openova-mcp depends on (#5516,
// UAT rows 221/222).
//
// The agentic create chain has TWO sides and they must not drift:
//
//	PRODUCER (this package) — applicationInstallRequestNormalize decides
//	which install-body fields a caller may omit; validateApplicationInstallRequest
//	decides which are mandatory.
//	CONSUMER (products/openova-mcp/internal/tools) — the MCP composes an
//	install body from an agent's tool call, where the agent may legitimately
//	know only "install wordpress in my org".
//
// The asymmetry that broke the chain: environmentRef, placement.mode and
// placement.regions ARE defaulted here, but blueprintRef.version is NOT — it
// is mandatory with no default and no server-side resolution. So a body that
// omitted only the version 400'd, and the failure surfaced several hops from
// its cause. The MCP now resolves an omitted version from the catalog (the
// same source the console's InstallPage pins from) and refuses loudly when it
// cannot.
//
// This test pins BOTH halves of that asymmetry, so if either side ever
// changes the other's assumption is caught here rather than on a live walk.
package handler

import "testing"

// Test_ApplicationInstall_VersionIsRequiredAndNeverDefaulted pins the exact
// defaulting asymmetry the openova-mcp facade is built around.
//
// The control is the whole point: the SAME body, through the SAME normalize
// call, comes back with environmentRef + placement.mode + placement.regions
// FILLED. So a failure here reads as "the version rule changed", never as
// "normalize does nothing" — the assertion can distinguish the two.
func Test_ApplicationInstall_VersionIsRequiredAndNeverDefaulted(t *testing.T) {
	// The body an agent-driven create produces: a Blueprint, a name, an Org,
	// and nothing else.
	body := applicationInstallRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-wordpress"},
		Name:            "shop",
		OrganizationRef: "acme",
	}

	got := applicationInstallRequestNormalize(body)

	// ── CONTROL: these three ARE defaulted, so the assertion below is a
	// statement about `version` specifically, not about normalize being inert.
	if got.EnvironmentRef != "acme-prod" {
		t.Fatalf("control failed: environmentRef must default to <org>-prod, got %q — "+
			"if this changed, the openova-mcp create_application tool description "+
			"(which tells agents environment is optional) is now wrong too", got.EnvironmentRef)
	}
	if got.Placement.Mode != "singleton" {
		t.Fatalf("control failed: placement.mode must default to singleton, got %q", got.Placement.Mode)
	}
	if len(got.Placement.Regions) != 1 || got.Placement.Regions[0] != "primary" {
		t.Fatalf("control failed: placement.regions must default to [primary], got %v", got.Placement.Regions)
	}

	// ── THE ASYMMETRY: version is NOT defaulted…
	if got.BlueprintRef.Version != "" {
		t.Errorf("blueprintRef.version is now defaulted server-side to %q. That is a CONTRACT CHANGE: "+
			"products/openova-mcp resolves an omitted version from GET /api/v1/catalog precisely because "+
			"this side does not. Update catalystapi.ResolveBlueprintVersion + its doc comment together with this.",
			got.BlueprintRef.Version)
	}

	// …and it is MANDATORY, so an omitted version is a hard 400 rather than a
	// benign default. This is why the MCP must refuse locally instead of
	// forwarding an empty version.
	msg, ok := validateApplicationInstallRequest(got)
	if ok {
		t.Fatalf("an install body with no blueprintRef.version was accepted. If the version is now optional, "+
			"the openova-mcp catalog round-trip is dead weight and should be removed; got msg=%q", msg)
	}
	if msg != "blueprintRef.version is required" {
		t.Errorf("the version rejection message changed to %q — openova-mcp's error text references this contract", msg)
	}

	// The SAME body with a version pinned validates — proving the rejection
	// above is attributable to the version and to nothing else in the body.
	withVersion := got
	withVersion.BlueprintRef.Version = "1.2.3"
	if msg, ok := validateApplicationInstallRequest(withVersion); !ok {
		t.Fatalf("the minimal body + a pinned version must validate; got %q", msg)
	}
}
