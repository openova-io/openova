package gitops

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// #5439 — post-cutover re-tether guard for the per-Org vCluster IMAGE registry.
//
// Read the "if the defect were present, could THIS check have gone red?" test
// before touching these. The pair is deliberate:
//
//   - TestRender_VClusterImageRegistry_PostCutover_NoMothershipHost is the RED
//     guard. On the pre-fix tree Render() emits `harbor.openova.io` even with
//     every step-07 pivot fact set, so it FAILS. It cannot be satisfied by
//     deleting the mothership literal from the templates, because…
//   - TestRender_VClusterImageRegistry_PreCutover_ByteIdenticalToHistorical is
//     the CONTROL. It passes on BOTH the pre-fix and post-fix trees and
//     REQUIRES the mothership literal to still be rendered when no pivot fact
//     is present. A blanket suppression that satisfies the first assertion
//     breaks this one.
// ---------------------------------------------------------------------------

// pivotFactEnv sets the step-07 stamps a cut-over Sovereign's
// organization-controller carries. t.Setenv restores on cleanup, so these
// tests must not be t.Parallel().
func pivotFactEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CATALYST_LOCAL_IMAGE_REGISTRY_HOST", "harbor.hw292.omani.works")
	t.Setenv("CATALYST_LOCAL_REGISTRY_URL", "oci://registry.hw292.omani.works/openova-io")
	t.Setenv("CATALYST_PIN_ISSUER", "https://console.hw292.omani.works")
	t.Setenv("CATALYST_HANDOVER_JWT_ISSUER", "https://console.hw292.omani.works")
}

// noPivotFactEnv clears every fact so the process looks like a pre-cutover
// Sovereign / the mothership. Explicit rather than implicit: `go test` inherits
// the developer's environment, and a stray CATALYST_PIN_ISSUER would otherwise
// turn the control green for the wrong reason.
func noPivotFactEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CATALYST_LOCAL_IMAGE_REGISTRY_HOST", "")
	t.Setenv("CATALYST_LOCAL_REGISTRY_URL", "")
	t.Setenv("CATALYST_PIN_ISSUER", "")
	t.Setenv("CATALYST_HANDOVER_JWT_ISSUER", "")
}

// liveShapedInputs mirrors the Inputs the organization-controller built for the
// live hw292 Organization `uatco` whose rendered HelmRelease carried
// `image.registry: harbor.openova.io` three times AFTER cutoverComplete=true.
// VClusterImageRegistry is the mothership literal because the chart ALWAYS
// stamps CATALYST_VCLUSTER_IMAGE_REGISTRY with it — that is the live input, not
// a contrived one.
func liveShapedInputs() Inputs {
	return Inputs{
		Slug:                  "uatco",
		DisplayName:           "UAT Co",
		Tier:                  "org",
		PlanSlug:              "m",
		SovereignFQDN:         "hw292.omani.works",
		HostCluster:           "hw292.omani.works",
		VClusterChartVersion:  "0.33.*",
		VClusterImageRegistry: "harbor.openova.io",
	}
}

// TestRender_VClusterImageRegistry_PostCutover_NoMothershipHost is the RED
// guard: with the cutover pivot facts present, NOTHING the generator writes
// into the Flux-owned per-Org tree may name the mothership.
func TestRender_VClusterImageRegistry_PostCutover_NoMothershipHost(t *testing.T) {
	pivotFactEnv(t)
	out, err := Render(liveShapedInputs())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for path, body := range out {
		if strings.Contains(string(body), mothershipHarborHost) {
			t.Errorf("#5439 re-tether: %s names the mothership host %q on a cut-over Sovereign — "+
				"the next Organization reconcile writes it back into the Flux source and undoes the step-06/step-10 pivot\n%s",
				path, mothershipHarborHost, body)
		}
	}
	vcl := string(out["vcluster/vcluster.yaml"])
	if vcl == "" {
		t.Fatal("vcluster/vcluster.yaml not rendered — the guard would pass vacuously")
	}
	// Positive half: the local host must actually land, and it must be the
	// SAME host step-10 pivots the platform vClusters to (target_host=
	// harbor.${SOVEREIGN_FQDN}), or the generated source and the pivoted
	// objects disagree and Flux drift-corrects forever.
	if !strings.Contains(vcl, "registry: harbor.hw292.omani.works") {
		t.Errorf("expected the Sovereign-local host 'harbor.hw292.omani.works' in vcluster.yaml\n%s", vcl)
	}
	if !strings.Contains(vcl, "harbor.hw292.omani.works/proxy-dockerhub/coredns/coredns:") {
		t.Errorf("coredns image not pivoted — this exact key held harbor.openova.io live on hw292\n%s", vcl)
	}
	if !strings.Contains(vcl, "repository: proxy-ghcr/loft-sh/kubernetes") {
		t.Errorf("k8s-distro proxy path lost during the pivot\n%s", vcl)
	}
}

// TestRender_VClusterImageRegistry_PreCutover_ByteIdenticalToHistorical is the
// CONTROL. It passes on the pre-fix tree AND the post-fix tree. Its job is to
// make the RED guard unsatisfiable by suppression: a pre-cutover Sovereign
// MUST still render the mothership Harbor, because its own Harbor has no
// proxy-cache projects (step-02) and no mirrored images (step-03) yet — every
// per-Org vCluster would ImagePullBackOff.
func TestRender_VClusterImageRegistry_PreCutover_ByteIdenticalToHistorical(t *testing.T) {
	noPivotFactEnv(t)
	out, err := Render(liveShapedInputs())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	vcl := string(out["vcluster/vcluster.yaml"])
	if vcl == "" {
		t.Fatal("vcluster/vcluster.yaml not rendered")
	}
	for _, want := range []string{
		"registry: harbor.openova.io",
		"image: harbor.openova.io/proxy-dockerhub/coredns/coredns:1.11.3",
		"repository: proxy-ghcr/loft-sh/kubernetes",
		"repository: proxy-ghcr/loft-sh/vcluster-oss",
	} {
		if !strings.Contains(vcl, want) {
			t.Errorf("pre-cutover render lost %q — a pre-cutover Sovereign pulls vCluster images "+
				"through the MOTHERSHIP Harbor; pointing it at its own empty Harbor is an ImagePullBackOff, not a fix\n%s",
				want, vcl)
		}
	}
}

// TestRender_VClusterImageRegistry_ExplicitOverrideWinsBothPhases proves the
// Principle #4 escape hatch survives the fix in both phases: an operator (or a
// future cutover step) naming an explicit non-mothership host is authoritative
// and never second-guessed by the derivation.
func TestRender_VClusterImageRegistry_ExplicitOverrideWinsBothPhases(t *testing.T) {
	for _, phase := range []struct {
		name string
		set  func(*testing.T)
	}{
		{"pre-cutover", noPivotFactEnv},
		{"post-cutover", pivotFactEnv},
	} {
		t.Run(phase.name, func(t *testing.T) {
			phase.set(t)
			in := liveShapedInputs()
			in.VClusterImageRegistry = "registry.internal.example"
			out, err := Render(in)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			vcl := string(out["vcluster/vcluster.yaml"])
			if !strings.Contains(vcl, "registry: registry.internal.example") {
				t.Errorf("explicit override lost in %s\n%s", phase.name, vcl)
			}
			if strings.Contains(vcl, mothershipHarborHost) {
				t.Errorf("explicit override leaked the mothership host in %s\n%s", phase.name, vcl)
			}
		})
	}
}

// TestResolveVClusterImageRegistry is the pure-core table. It pins the whole
// decision map, not just the happy path — an action-flip that silently
// re-ordered the precedence would go red here.
func TestResolveVClusterImageRegistry(t *testing.T) {
	t.Parallel()
	const fqdn = "hw292.omani.works"
	cases := []struct {
		name                                                      string
		configured, localHost, localURL, pinIss, handIss, wantOut string
		fqdns                                                     []string
	}{
		{
			name:       "no fact — mothership literal unchanged",
			configured: mothershipHarborHost, wantOut: mothershipHarborHost,
			fqdns: []string{fqdn},
		},
		{
			name:       "no fact — empty configured falls back to the historical default",
			configured: "", wantOut: mothershipHarborHost, fqdns: []string{fqdn},
		},
		{
			name:       "explicit non-mothership host wins over every fact",
			configured: "registry.internal.example", localHost: "harbor." + fqdn,
			localURL: "oci://registry." + fqdn + "/openova-io", pinIss: "https://console." + fqdn,
			wantOut: "registry.internal.example", fqdns: []string{fqdn},
		},
		{
			name:       "step-07 Phase 3e stamp is authoritative",
			configured: mothershipHarborHost, localHost: "harbor." + fqdn,
			wantOut: "harbor." + fqdn, fqdns: []string{"ignored.example"},
		},
		{
			name:       "step-07 Phase 3e stamp normalised (scheme + trailing slash)",
			configured: mothershipHarborHost, localHost: "https://harbor." + fqdn + "/",
			wantOut: "harbor." + fqdn, fqdns: []string{fqdn},
		},
		{
			name:       "Phase 3d chart-OCI fact derives harbor.<fqdn>",
			configured: mothershipHarborHost, localURL: "oci://registry." + fqdn + "/openova-io",
			wantOut: "harbor." + fqdn, fqdns: []string{fqdn},
		},
		{
			name:       "Phase 3b issuer fact derives harbor.<fqdn>",
			configured: mothershipHarborHost, pinIss: "https://console." + fqdn,
			wantOut: "harbor." + fqdn, fqdns: []string{fqdn},
		},
		{
			name:       "handover issuer alone is enough",
			configured: mothershipHarborHost, handIss: "https://console." + fqdn,
			wantOut: "harbor." + fqdn, fqdns: []string{fqdn},
		},
		{
			name:       "second FQDN candidate used when the first is empty",
			configured: mothershipHarborHost, pinIss: "https://console." + fqdn,
			wantOut: "harbor." + fqdn, fqdns: []string{"", fqdn},
		},
		{
			name:       "fact without any FQDN fails safe to the configured host",
			configured: mothershipHarborHost, pinIss: "https://console." + fqdn,
			wantOut: mothershipHarborHost, fqdns: []string{"", ""},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveVClusterImageRegistry(c.configured, c.localHost, c.localURL, c.pinIss, c.handIss, c.fqdns...)
			if got != c.wantOut {
				t.Errorf("resolveVClusterImageRegistry = %q, want %q", got, c.wantOut)
			}
		})
	}
}
