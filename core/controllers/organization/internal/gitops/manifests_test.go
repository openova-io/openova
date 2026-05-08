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
		Tier:                 "sme",
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
	} {
		if !strings.Contains(vcl, want) {
			t.Errorf("vcluster.yaml missing %q\nfull contents:\n%s", want, vcl)
		}
	}
	// namespace.yaml must carry the canonical labels.
	ns := string(out["vcluster/namespace.yaml"])
	for _, want := range []string{
		"name: acme",
		"openova.io/organization: acme",
		"openova.io/tier: sme",
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
		Tier:                 "sme",
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
