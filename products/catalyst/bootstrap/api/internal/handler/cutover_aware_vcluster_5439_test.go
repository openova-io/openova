package handler

import "testing"

// ---------------------------------------------------------------------------
// #5439 — post-cutover re-tether guard for the catalyst-api org-tenants leg's
// IMAGE registry host. Same deliberate pair as the other two generators:
// a RED guard that fails on the pre-fix tree, and a CONTROL that is green on
// BOTH trees and REQUIRES the mothership literal pre-cutover, so the red guard
// cannot be satisfied by deleting the literal.
// ---------------------------------------------------------------------------

func apiPivotFactEnv(t *testing.T) {
	t.Helper()
	// Live hw292 shape: the step-07 issuer fact IS present but both FQDN envs
	// are EMPTY — which is why the Phase 3e stamp, not a derivation, is the
	// load-bearing seam for this process.
	t.Setenv("CATALYST_LOCAL_IMAGE_REGISTRY_HOST", "harbor.hw292.omani.works")
	t.Setenv("CATALYST_LOCAL_REGISTRY_URL", "")
	t.Setenv("CATALYST_PIN_ISSUER", "https://console.hw292.omani.works")
	t.Setenv("CATALYST_HANDOVER_JWT_ISSUER", "https://console.hw292.omani.works")
	t.Setenv("SOVEREIGN_FQDN", "")
	t.Setenv("CATALYST_OTECH_FQDN", "")
}

func apiNoPivotFactEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CATALYST_LOCAL_IMAGE_REGISTRY_HOST", "")
	t.Setenv("CATALYST_LOCAL_REGISTRY_URL", "")
	t.Setenv("CATALYST_PIN_ISSUER", "")
	t.Setenv("CATALYST_HANDOVER_JWT_ISSUER", "")
	t.Setenv("SOVEREIGN_FQDN", "")
	t.Setenv("CATALYST_OTECH_FQDN", "")
}

// TestVClusterImageRegistry_PostCutover_NoMothershipHost is the RED guard.
func TestVClusterImageRegistry_PostCutover_NoMothershipHost(t *testing.T) {
	apiPivotFactEnv(t)
	t.Setenv("CATALYST_VCLUSTER_IMAGE_REGISTRY", mothershipVClusterImageRegistry)
	got := vclusterImageRegistryFor(mothershipVClusterImageRegistry)
	if got == mothershipVClusterImageRegistry {
		t.Fatalf("#5439 re-tether: org-tenant overlay would declare the mothership host %q "+
			"on a cut-over Sovereign — the next Org signup/teardown writes it back into the Flux source",
			mothershipVClusterImageRegistry)
	}
	if got != "harbor.hw292.omani.works" {
		t.Fatalf("vclusterImageRegistryFor = %q, want %q (the SAME host step-10 pivots the "+
			"platform vClusters to; a different host makes the source and the pivoted objects disagree)",
			got, "harbor.hw292.omani.works")
	}
}

// TestVClusterImageRegistry_PreCutover_ByteIdenticalToHistorical is the
// CONTROL — green on BOTH trees. A pre-cutover Sovereign's own Harbor holds no
// mirrored images yet, so it MUST keep pulling through the mothership.
func TestVClusterImageRegistry_PreCutover_ByteIdenticalToHistorical(t *testing.T) {
	apiNoPivotFactEnv(t)
	if got := vclusterImageRegistryFor(mothershipVClusterImageRegistry); got != mothershipVClusterImageRegistry {
		t.Fatalf("pre-cutover resolve = %q, want the historical %q — pointing a pre-cutover "+
			"Sovereign at its own empty Harbor is an ImagePullBackOff, not a fix",
			got, mothershipVClusterImageRegistry)
	}
	if got := vclusterImageRegistryFor(""); got != mothershipVClusterImageRegistry {
		t.Fatalf("unset configured value = %q, want the historical default %q",
			got, mothershipVClusterImageRegistry)
	}
}

// TestVClusterImageRegistry_ExplicitOverrideWinsBothPhases pins the Principle
// #4 escape hatch in both phases.
func TestVClusterImageRegistry_ExplicitOverrideWinsBothPhases(t *testing.T) {
	for _, phase := range []struct {
		name string
		set  func(*testing.T)
	}{
		{"pre-cutover", apiNoPivotFactEnv},
		{"post-cutover", apiPivotFactEnv},
	} {
		t.Run(phase.name, func(t *testing.T) {
			phase.set(t)
			if got := vclusterImageRegistryFor("registry.internal.example"); got != "registry.internal.example" {
				t.Errorf("explicit override lost in %s: got %q", phase.name, got)
			}
		})
	}
}

// TestResolveVClusterImageRegistry_API pins the whole decision map of the pure
// core.
func TestResolveVClusterImageRegistry_API(t *testing.T) {
	t.Parallel()
	const fqdn = "hw292.omani.works"
	const mothership = mothershipVClusterImageRegistry
	cases := []struct {
		name                                             string
		configured, localHost, localURL, pinIss, handIss string
		fqdns                                            []string
		want                                             string
	}{
		{name: "no fact", configured: mothership, fqdns: []string{fqdn}, want: mothership},
		{name: "empty configured, no fact", configured: "", fqdns: []string{fqdn}, want: mothership},
		{name: "explicit host beats every fact", configured: "registry.internal.example",
			localHost: "harbor." + fqdn, pinIss: "https://console." + fqdn,
			fqdns: []string{fqdn}, want: "registry.internal.example"},
		{name: "Phase 3e stamp authoritative", configured: mothership,
			localHost: "harbor." + fqdn, fqdns: []string{"ignored.example"}, want: "harbor." + fqdn},
		{name: "Phase 3e stamp normalised", configured: mothership,
			localHost: "https://harbor." + fqdn + "/", fqdns: nil, want: "harbor." + fqdn},
		{name: "Phase 3d chart-OCI fact derives", configured: mothership,
			localURL: "oci://registry." + fqdn + "/openova-io", fqdns: []string{fqdn}, want: "harbor." + fqdn},
		{name: "Phase 3b issuer fact derives", configured: mothership,
			pinIss: "https://console." + fqdn, fqdns: []string{fqdn}, want: "harbor." + fqdn},
		{name: "handover issuer alone", configured: mothership,
			handIss: "https://console." + fqdn, fqdns: []string{fqdn}, want: "harbor." + fqdn},
		{name: "live hw292 shape: issuer fact but NO fqdn fails safe", configured: mothership,
			pinIss: "https://console." + fqdn, fqdns: []string{"", ""}, want: mothership},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveVClusterImageRegistry(c.configured, c.localHost, c.localURL, c.pinIss, c.handIss, c.fqdns...); got != c.want {
				t.Errorf("resolveVClusterImageRegistry = %q, want %q", got, c.want)
			}
		})
	}
}
