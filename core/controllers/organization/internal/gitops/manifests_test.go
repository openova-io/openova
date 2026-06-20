package gitops

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestRender_AllPathsAndStructuralYAML(t *testing.T) {
	t.Parallel()
	out, err := Render(Inputs{
		Slug:                 "acme",
		DisplayName:          "ACME Corp",
		Tier: "org",
		SovereignFQDN:        "omantel.omani.works",
		HostCluster:          "hz-fsn-rtz-prod",
		VClusterChartVersion: "0.33.*",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	wantPaths := []string{
		"vcluster/namespace.yaml",
		"vcluster/vcluster.yaml",
		"vcluster/kustomization.yaml",
	}
	for _, p := range wantPaths {
		if _, ok := out[p]; !ok {
			t.Errorf("missing path %q", p)
		}
	}
	// Each rendered file must be valid YAML.
	for path, data := range out {
		var v any
		if err := yaml.Unmarshal(data, &v); err != nil {
			t.Errorf("rendered %s is not valid YAML: %v\n%s", path, err, string(data))
		}
	}
	// vcluster.yaml must reference the slug as namespace + control-plane
	// service hostname per NAMING §4.6 (no slug embedded in resource
	// names below the namespace, but the namespace itself == slug).
	vcl := string(out["vcluster/vcluster.yaml"])
	for _, want := range []string{
		"namespace: acme",
		"server: https://vcluster.acme:443",
		"openova.io/organization: acme",
		"openova.io/host-cluster: hz-fsn-rtz-prod",
		"openova.io/sovereign: omantel.omani.works",
		"version: \"0.33.*\"",
		// MIRROR-EVERYTHING (#3760): BOTH vcluster 0.33.x initContainer
		// images must pull through the Sovereign Harbor proxy-cache so the
		// harbor-proxy-pull Kyverno ClusterPolicy (Enforce, `*/proxy-*/*`
		// glob) admits the StatefulSet. The k8s distro image is
		// initContainers[0] (the live hw158 denial path) and the syncer is
		// initContainers[1] — neither may stay on raw ghcr.io.
		"registry: harbor.openova.io",
		"repository: proxy-ghcr/loft-sh/kubernetes",
		"repository: proxy-ghcr/loft-sh/vcluster-oss",
	} {
		if !strings.Contains(vcl, want) {
			t.Errorf("vcluster.yaml missing %q\nfull contents:\n%s", want, vcl)
		}
	}
	// Guard: no image FIELD may resolve to un-proxied ghcr.io — that is
	// exactly what harbor-proxy-pull denies (a regression would re-wedge the
	// funnel). Scan structured `registry:` lines only (explanatory comments
	// legitimately mention ghcr.io as the upstream being re-tagged).
	for _, line := range strings.Split(vcl, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "registry:") && strings.Contains(trimmed, "ghcr.io") {
			t.Errorf("vcluster.yaml has an un-proxied image registry %q — harbor-proxy-pull will DENY the StatefulSet\nfull contents:\n%s", trimmed, vcl)
		}
	}
	// namespace.yaml must carry the canonical labels.
	ns := string(out["vcluster/namespace.yaml"])
	for _, want := range []string{
		"name: acme",
		"openova.io/organization: acme",
		"openova.io/tier: org",
		"openova.io/sovereign: omantel.omani.works",
		"openova.io/host-cluster: hz-fsn-rtz-prod",
	} {
		if !strings.Contains(ns, want) {
			t.Errorf("namespace.yaml missing %q\nfull contents:\n%s", want, ns)
		}
	}
}

func TestRender_HelmRepoDefaults(t *testing.T) {
	t.Parallel()
	out, err := Render(Inputs{
		Slug:                 "acme",
		DisplayName:          "Acme",
		Tier: "org",
		SovereignFQDN:        "x.example",
		HostCluster:          "hz-fsn-rtz-prod",
		VClusterChartVersion: "0.33.*",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	vcl := string(out["vcluster/vcluster.yaml"])
	if !strings.Contains(vcl, "name: loft") {
		t.Errorf("expected default helmrepo name 'loft' in vcluster.yaml")
	}
	if !strings.Contains(vcl, "namespace: vcluster-system") {
		t.Errorf("expected default helmrepo namespace 'vcluster-system' in vcluster.yaml")
	}
}

// TestRender_VClusterImageRegistryOverride proves the proxy registry is
// operator-overridable (Principle #4) — cutover Step-04 flips it to
// harbor.<sovereign-fqdn> post-handover (#3760).
func TestRender_VClusterImageRegistryOverride(t *testing.T) {
	t.Parallel()
	out, err := Render(Inputs{
		Slug:                  "acme",
		DisplayName:           "Acme",
		Tier: "org",
		SovereignFQDN:         "omantel.omani.works",
		HostCluster:           "hz-fsn-rtz-prod",
		VClusterChartVersion:  "0.33.*",
		VClusterImageRegistry: "harbor.omantel.omani.works",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	vcl := string(out["vcluster/vcluster.yaml"])
	if !strings.Contains(vcl, "registry: harbor.omantel.omani.works") {
		t.Errorf("expected overridden registry 'harbor.omantel.omani.works' in vcluster.yaml\nfull contents:\n%s", vcl)
	}
	// The repository stays registry-relative + proxy-globbable regardless
	// of the host.
	if !strings.Contains(vcl, "repository: proxy-ghcr/loft-sh/kubernetes") {
		t.Errorf("expected proxy-relative k8s-distro repository under overridden registry\nfull contents:\n%s", vcl)
	}
	if strings.Contains(vcl, "harbor.openova.io") {
		t.Errorf("override leaked the default 'harbor.openova.io' into vcluster.yaml\nfull contents:\n%s", vcl)
	}
}
