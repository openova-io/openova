package gitops

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// #5439 — post-cutover re-tether guard for the provisioning generator's IMAGE
// registry host. Same deliberate pair as the org-controller's:
//
//   - the PostCutover test is the RED guard (fails on the pre-fix tree),
//   - the PreCutover test is the CONTROL: it passes on BOTH trees and REQUIRES
//     the mothership literal to still be emitted when no pivot fact is present,
//     so the red guard cannot be satisfied by deleting the literal.
// ---------------------------------------------------------------------------

func provPivotFactEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CATALYST_LOCAL_IMAGE_REGISTRY_HOST", "harbor.hw292.omani.works")
	t.Setenv("CATALYST_LOCAL_REGISTRY_URL", "oci://registry.hw292.omani.works/openova-io")
	t.Setenv("CATALYST_PIN_ISSUER", "")
	t.Setenv("CATALYST_HANDOVER_JWT_ISSUER", "")
	t.Setenv("SOVEREIGN_FQDN", "hw292.omani.works")
	t.Setenv("CATALYST_OTECH_FQDN", "")
}

func provNoPivotFactEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CATALYST_LOCAL_IMAGE_REGISTRY_HOST", "")
	t.Setenv("CATALYST_LOCAL_REGISTRY_URL", "")
	t.Setenv("CATALYST_PIN_ISSUER", "")
	t.Setenv("CATALYST_HANDOVER_JWT_ISSUER", "")
	t.Setenv("SOVEREIGN_FQDN", "hw292.omani.works")
	t.Setenv("CATALYST_OTECH_FQDN", "")
}

// liveShapedGenerator mirrors the live hw292 wiring: main.go reads
// VCLUSTER_IMAGE_REGISTRY, which the chart ALWAYS stamps with the mothership
// literal, and hands it to the generator.
func liveShapedGenerator() *ManifestGenerator {
	g := NewManifestGenerator("clusters/hw292.omani.works/org-tenants")
	g.RegistryMirror = mothershipHarborHost
	return g
}

// TestRegistryMirror_PostCutover_NoMothershipHost is the RED guard: every image
// this generator emits — datastores and catalog apps alike — must be re-tagged
// through the Sovereign-local Harbor once the cutover facts are present.
func TestRegistryMirror_PostCutover_NoMothershipHost(t *testing.T) {
	provPivotFactEnv(t)
	g := liveShapedGenerator()
	if got := g.registryMirror(); got != "harbor.hw292.omani.works" {
		t.Fatalf("#5439 re-tether: registryMirror() = %q on a cut-over Sovereign, want %q — "+
			"every datastore + catalog-app image the next org mutation writes into the Flux "+
			"source would name the mothership Harbor", got, "harbor.hw292.omani.works")
	}
	// The rendered artefacts, not just the accessor: an assertion on the
	// accessor alone would go green if a caller stopped using it.
	for name, body := range map[string]string{
		"db-postgres.yaml": generatePostgres("uatco-apps", "pw", []string{"wordpress"}, nil, g.registryMirror()),
		"db-mysql.yaml":    generateMySQL("uatco-apps", "pw", []string{"wordpress"}, nil, g.registryMirror()),
		"db-redis.yaml":    generateRedis("uatco-apps", g.registryMirror()),
	} {
		if strings.Contains(body, mothershipHarborHost) {
			t.Errorf("#5439 re-tether: %s names the mothership host %q post-cutover\n%s",
				name, mothershipHarborHost, body)
		}
		if !strings.Contains(body, "harbor.hw292.omani.works/proxy-") {
			t.Errorf("%s did not route through the Sovereign-local Harbor proxy\n%s", name, body)
		}
	}
}

// TestRegistryMirror_PreCutover_ByteIdenticalToHistorical is the CONTROL —
// green on BOTH trees. A pre-cutover Sovereign's own Harbor has no proxy-cache
// projects (step-02) and no mirrored images (step-03), so it MUST keep pulling
// through the mothership.
func TestRegistryMirror_PreCutover_ByteIdenticalToHistorical(t *testing.T) {
	provNoPivotFactEnv(t)
	g := liveShapedGenerator()
	if got := g.registryMirror(); got != mothershipHarborHost {
		t.Fatalf("pre-cutover registryMirror() = %q, want %q — pointing a pre-cutover Sovereign "+
			"at its own empty Harbor is an ImagePullBackOff, not a fix", got, mothershipHarborHost)
	}
	body := generateRedis("uatco-apps", g.registryMirror())
	if !strings.Contains(body, mothershipHarborHost+"/proxy-dockerhub/valkey/valkey:8-alpine") {
		t.Errorf("pre-cutover render lost the historical mothership proxy path\n%s", body)
	}
	// Unset generator (direct callers / Catalyst-Zero) keeps the historical
	// bootstrap default.
	empty := NewManifestGenerator("x")
	if got := empty.registryMirror(); got != defaultVClusterRegistryMirror {
		t.Errorf("unset RegistryMirror = %q, want the historical default %q", got, defaultVClusterRegistryMirror)
	}
}

// TestRegistryMirror_ExplicitOverrideWinsBothPhases pins the Principle #4
// escape hatch in both phases.
func TestRegistryMirror_ExplicitOverrideWinsBothPhases(t *testing.T) {
	for _, phase := range []struct {
		name string
		set  func(*testing.T)
	}{
		{"pre-cutover", provNoPivotFactEnv},
		{"post-cutover", provPivotFactEnv},
	} {
		t.Run(phase.name, func(t *testing.T) {
			phase.set(t)
			g := NewManifestGenerator("x")
			g.RegistryMirror = "registry.internal.example"
			if got := g.registryMirror(); got != "registry.internal.example" {
				t.Errorf("explicit override lost in %s: got %q", phase.name, got)
			}
		})
	}
}

// TestResolveVClusterImageRegistry_Provisioning pins the whole decision map of
// the pure core (an action-flip that re-ordered precedence goes red here).
func TestResolveVClusterImageRegistry_Provisioning(t *testing.T) {
	t.Parallel()
	const fqdn = "hw292.omani.works"
	cases := []struct {
		name                                             string
		configured, localHost, localURL, pinIss, handIss string
		fqdns                                            []string
		want                                             string
	}{
		{name: "no fact", configured: mothershipHarborHost, fqdns: []string{fqdn}, want: mothershipHarborHost},
		{name: "empty configured, no fact", configured: "", fqdns: []string{fqdn}, want: mothershipHarborHost},
		{name: "explicit host beats every fact", configured: "registry.internal.example",
			localHost: "harbor." + fqdn, localURL: "oci://registry." + fqdn + "/openova-io",
			pinIss: "https://console." + fqdn, fqdns: []string{fqdn}, want: "registry.internal.example"},
		{name: "Phase 3e stamp authoritative", configured: mothershipHarborHost,
			localHost: "harbor." + fqdn, fqdns: []string{"ignored.example"}, want: "harbor." + fqdn},
		{name: "Phase 3e stamp normalised", configured: mothershipHarborHost,
			localHost: "https://harbor." + fqdn + "/", fqdns: []string{fqdn}, want: "harbor." + fqdn},
		{name: "Phase 3d chart-OCI fact derives", configured: mothershipHarborHost,
			localURL: "oci://registry." + fqdn + "/openova-io", fqdns: []string{fqdn}, want: "harbor." + fqdn},
		{name: "Phase 3b issuer fact derives", configured: mothershipHarborHost,
			pinIss: "https://console." + fqdn, fqdns: []string{fqdn}, want: "harbor." + fqdn},
		{name: "handover issuer alone", configured: mothershipHarborHost,
			handIss: "https://console." + fqdn, fqdns: []string{fqdn}, want: "harbor." + fqdn},
		{name: "second FQDN candidate", configured: mothershipHarborHost,
			pinIss: "https://console." + fqdn, fqdns: []string{"", fqdn}, want: "harbor." + fqdn},
		{name: "fact without FQDN fails safe", configured: mothershipHarborHost,
			pinIss: "https://console." + fqdn, fqdns: []string{"", ""}, want: mothershipHarborHost},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveVClusterImageRegistry(c.configured, c.localHost, c.localURL, c.pinIss, c.handIss, c.fqdns...); got != c.want {
				t.Errorf("resolveVClusterImageRegistry = %q, want %q", got, c.want)
			}
		})
	}
}
